// main.go — the aa binary's entry point, verb dispatch, and collaborator wiring.
//
// main() parses argv, identifies the verb, wires a SessionManager against
// the real Backend / SessionStore / EphemeralKeyProvider / SSHRunner
// chosen by config, and dispatches to a verb_*.go handler. Each handler
// returns an exit code, which main feeds to os.Exit.
//
// Global flag surface:
//
//	-v   verbose — route progress lines to stdout (non-verbose: stderr-only errors).
//
// main.go is NOT in strict mode (see docs/PHILOSOPHY.md). It is the CLI
// adapter; boundary validation lives in config_loader.go and the strict
// files listed there. The governing concerns here are Clarity (axis 1),
// Low ceremony (axis 4), and Observability (axis 3).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// aaVersion is the binary's version string. Printed by `aa version`.
const aaVersion = "aa v0.1.0-dev"

// main parses argv, identifies the verb, wires the SessionManager, and
// dispatches. Never panics; errors surface as exit codes with messages.
func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the testable entrypoint: argv without program name, plus
// injectable I/O. It returns the exit code rather than calling os.Exit
// directly, so the main() wrapper is trivial.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Separate global flags from verb + verb args. The conventional shape
	// is `aa [-v] <verb> [<verb-args>...]`. For `aa` (no verb) we fall
	// through to verbAttach, still honoring global flags.
	fs := flag.NewFlagSet("aa", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var verbose bool
	fs.BoolVar(&verbose, "v", false, "verbose: route progress lines to stdout")

	// We want `aa -v` to work and `aa -v status` to work. flag.Parse stops
	// at the first non-flag argument, which is what we want.
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()

	verb := ""
	verbArgs := []string(nil)
	if len(rest) > 0 {
		verb = rest[0]
		verbArgs = rest[1:]
	}

	// `aa init` and `aa version` don't need a SessionManager — they are
	// config-scaffold / self-describing commands. Dispatch them early so
	// a missing ~/.aa/config.json doesn't block `aa init --global`.
	switch verb {
	case "init":
		return verbInit(verbArgs, stdout, stderr)
	case "version":
		return verbVersion(stdout)
	}

	// Everything else needs a Config, a SessionManager, and the verb args.
	ctx := context.Background()
	sm, cfg, err := buildSessionManager(stdin, stdout, stderr, verbose)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	switch verb {
	case "", "attach":
		return verbAttach(ctx, sm, cfg, verb == "attach", verbArgs, stdin, stdout, stderr)
	case "status":
		return verbStatus(ctx, sm, verbArgs, stdout, stderr)
	case "diff":
		return verbDiff(ctx, sm, verbArgs, stdout, stderr)
	case "push":
		return verbPush(ctx, sm, verbArgs, stdout, stderr)
	case "kill":
		return verbKill(ctx, sm, verbArgs, stdout, stderr)
	case "retry":
		return verbRetry(ctx, sm, verbArgs, stdout, stderr)
	case "list":
		return verbList(ctx, sm, verbArgs, stdout, stderr)
	case "sweep":
		return verbSweep(ctx, sm, verbArgs, stdout, stderr)
	case "fixture":
		return verbFixture(ctx, sm, cfg, verbArgs, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "aa: unknown verb %q; run `aa version` or see README § Command reference\n", verb)
		return 2
	}
}

// buildSessionManager loads config, resolves secrets, constructs the
// concrete Backend / SessionStore / EphemeralKeyProvider / SSHRunner,
// and returns a SessionManager ready to run a verb. The returned Config
// is the merged + resolved form for verbs that need it (e.g. attach's
// EgressAllowlist for the session-start banner).
//
// Example:
//
//	sm, cfg, err := buildSessionManager(os.Stdin, os.Stdout, os.Stderr, false)
func buildSessionManager(stdin io.Reader, stdout, stderr io.Writer, verbose bool) (*SessionManager, Config, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, Config{}, fmt.Errorf("aa: locate home directory: %w", err)
	}

	global, err := LoadGlobal(homeDir)
	if err != nil {
		return nil, Config{}, err
	}

	// Repo config is loaded best-effort: some verbs (list, sweep) work
	// without one. If present, merge it; if absent, use the global as-is.
	cfg := global
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		if repo, rErr := LoadRepo(cwd); rErr == nil {
			merged, mErr := Merge(global, repo)
			if mErr != nil {
				return nil, Config{}, mErr
			}
			cfg = merged
		}
	}

	resolved, err := ResolveSecretRefs(cfg, keyringResolver)
	if err != nil {
		return nil, Config{}, err
	}

	backend, err := buildBackend(resolved, homeDir)
	if err != nil {
		return nil, Config{}, err
	}

	store := NewFileSessionStore(homeDir)

	keyProvider := buildKeyProvider(resolved)

	controlDir := filepath.Join(homeDir, ".aa", "controlmaster")
	_ = os.MkdirAll(controlDir, 0o700)
	ssh := NewRealSSHRunner(controlDir)

	sm := NewSessionManager(backend, store, keyProvider, ssh, resolved.Rules)
	sm.Clock = time.Now
	sm.Confirm = makeTerminalConfirm(stdin, stdout)
	if verbose {
		sm.Out = stdout
	} else {
		sm.Out = io.Discard
	}
	sm.Err = stderr
	sm.LaptopCacheRoot = filepath.Join(homeDir, ".aa", "sessions")

	return sm, resolved, nil
}

// buildBackend dispatches on the default backend's Type field.
func buildBackend(cfg Config, homeDir string) (Backend, error) {
	bc, ok := cfg.Backends[cfg.DefaultBackend]
	if !ok {
		return nil, fmt.Errorf("aa: default_backend %q not defined in backends", cfg.DefaultBackend)
	}
	switch bc.Type {
	case "local":
		return NewLocalBackend(), nil
	case "process":
		workspaces := filepath.Join(homeDir, ".aa", "workspaces")
		return NewProcessBackend(workspaces), nil
	case "fly":
		controlDir := filepath.Join(homeDir, ".aa", "controlmaster")
		_ = os.MkdirAll(controlDir, 0o700)
		runner := NewRealSSHRunner(controlDir)
		return NewFlyBackend(bc.Region, "shared-cpu-2x", runner), nil
	default:
		return nil, fmt.Errorf("aa: backend type %q is not supported (valid: local, process, fly)", bc.Type)
	}
}

// buildKeyProvider constructs the EphemeralKeyProvider from the first
// agent that declares a keyring-anchored admin API base URL. If no agent
// declares one, a noopKeyProvider is returned: `process` backends and
// agents that don't need an ephemeral key never hit the provider, so a
// noop is the Clarity-friendly default (PHILOSOPHY axis 1).
func buildKeyProvider(cfg Config) EphemeralKeyProvider {
	baseURL := ""
	for _, agent := range cfg.Agents {
		if agent.AdminAPIBaseURL != "" {
			baseURL = agent.AdminAPIBaseURL
			break
		}
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	adminKey := os.Getenv("ANTHROPIC_ADMIN_KEY")
	if adminKey == "" {
		return &noopKeyProvider{}
	}
	return NewAnthropicKeyProvider(baseURL, adminKey)
}

// noopKeyProvider is the default ephemeral-key provider when no admin
// key is configured. Mint returns an empty KeyHandle and empty key
// string; Revoke is a no-op. This matches the scenario intent describes:
// a `process` backend with a non-Anthropic agent may have no keys at all.
type noopKeyProvider struct{}

// Mint returns a sentinel KeyHandle so the session record has a stable
// (but empty) identifier. No external call is made.
func (n *noopKeyProvider) Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error) {
	return KeyHandle{Provider: "noop", ID: ""}, "", nil
}

// Revoke is a no-op. There is no external key to destroy.
func (n *noopKeyProvider) Revoke(ctx context.Context, handle KeyHandle) error {
	return nil
}

// keyringResolver resolves a "keyring:<name>" reference to a secret.
//
// For v1 simplicity and to preserve the zero-dependency constraint (no
// OS keyring library imported), the resolver reads from environment
// variables. The reference "keyring:anthropic" maps to `ANTHROPIC_API_KEY`;
// the reference "keyring:openai" maps to `OPENAI_API_KEY`; any other
// reference "keyring:<name>" maps to the upper-cased env var
// `<NAME>_API_KEY` as a fallback. The resolver returns an error if the
// env var is not set so missing secrets surface loudly rather than
// silently becoming empty strings.
//
// Example:
//
//	v, err := keyringResolver("keyring:anthropic")
//	// reads $ANTHROPIC_API_KEY
func keyringResolver(ref string) (string, error) {
	if !strings.HasPrefix(ref, "keyring:") {
		return ref, nil
	}
	name := strings.TrimPrefix(ref, "keyring:")
	// The `ephemeral` sentinel means "aa will mint a fresh key at
	// session start via the EphemeralKeyProvider; the env value is a
	// placeholder until then". Return a non-empty string so downstream
	// validation (e.g. "env value must be non-empty") doesn't fail.
	if name == "ephemeral" {
		return "ephemeral", nil
	}
	envName := strings.ToUpper(name) + "_API_KEY"
	// Well-known aliases: anthropic→ANTHROPIC_API_KEY, openai→OPENAI_API_KEY.
	// The upper-cased form already matches; the explicit switch is here so
	// the mapping is searchable.
	switch strings.ToLower(name) {
	case "anthropic":
		envName = "ANTHROPIC_API_KEY"
	case "openai":
		envName = "OPENAI_API_KEY"
	}
	value := os.Getenv(envName)
	if value == "" {
		return "", fmt.Errorf("keyring reference %q: environment variable %s is not set", ref, envName)
	}
	return value, nil
}

// makeTerminalConfirm returns a Confirm function that reads one line
// from the supplied stdin, trims it, and maps yes/no answers. Bare
// Enter returns defaultYes (the severity-sensitive default per
// README § Rules).
func makeTerminalConfirm(stdin io.Reader, stdout io.Writer) func(string, bool) bool {
	reader := bufio.NewReader(stdin)
	return func(prompt string, defaultYes bool) bool {
		fmt.Fprintln(stdout, prompt)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return defaultYes
		}
		ans := strings.TrimSpace(strings.ToLower(line))
		// README § Rules uses `[r] [s] [a] [q]` for push confirmations.
		// We accept the accept/abort keys in addition to y/n so verb_push
		// and sweep share one prompt function.
		switch ans {
		case "y", "yes", "a", "accept":
			return true
		case "n", "no", "q", "abort", "quit":
			return false
		case "":
			return defaultYes
		default:
			return defaultYes
		}
	}
}

// currentSessionID derives the SessionID for the cwd repo + current
// branch. Shell out to git for both reads (per PHILOSOPHY axis 4: shell
// out to git, do not reimplement it).
//
// Example:
//
//	id, err := currentSessionID()
//	// id == SessionID("myapp-feature-oauth")
func currentSessionID() (SessionID, error) {
	repo, err := runGit("rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("derive session id: not a git repo: %w", err)
	}
	branch, err := runGit("branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("derive session id: read branch: %w", err)
	}
	return deriveSessionID(strings.TrimSpace(repo), strings.TrimSpace(branch)), nil
}

// runGit runs `git <args>` in the current working directory and returns
// trimmed stdout. Any non-zero exit surfaces as an error with the
// captured stderr appended.
func runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(stderr))
	}
	return string(out), nil
}
