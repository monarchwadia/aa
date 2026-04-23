// verb_attach.go — `aa` (default) and `aa attach` adapters.
//
// Default `aa` (no explicit verb): if a session record already exists
// for this repo+branch and its state is RUNNING, attach. Otherwise, do
// the first-time session-start dance: uncommitted-changes warning,
// session-start banner, StartSession, then attach.
//
// `aa attach` (explicit verb): force attach regardless of state — this
// is the "I know what I'm doing; drop me into bash" escape hatch from
// README § "Command reference".
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// verbAttach is the default entrypoint. If `explicit` is true, the user
// ran `aa attach`; otherwise they ran bare `aa`. Behavior:
//
//   - Load the session record (if any).
//   - If absent: run the uncommitted-changes check, print the banner,
//     StartSession, then attach.
//   - If present and RUNNING: print a "reattach" line and attach.
//   - If present but terminal: print the status block instead of
//     attaching (per README § "Command reference" — `aa` on a finished
//     session shows status, never attaches).
//
// Returns 0 on success, 1 on any error.
func verbAttach(ctx context.Context, sm *SessionManager, cfg Config, explicit bool, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// Check whether a local record already exists.
	rec, exists, err := sm.Store.Load(id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_ = rec

	if exists {
		// Existing session: figure out the state and decide what to do.
		state, remote, err := sm.Status(ctx, id)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}

		// Bare `aa` on a terminal-state session: show the status block
		// instead of attaching. `aa attach` forces attach anyway.
		if !explicit && state != StateRunning && state != StateProvisioning {
			printStatusBlock(stdout, id, state, remote)
			return 0
		}

		fmt.Fprintf(stdout, "aa: reattaching to existing session %s (state=%s)\n", id, state)
		if err := sm.Attach(ctx, id); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	// New session: uncommitted-changes warning, then banner, then start.
	if !confirmUncommittedChanges(stdin, stdout) {
		fmt.Fprintln(stderr, "aa: aborted by user (uncommitted changes declined)")
		return 1
	}

	printSessionStartBanner(stdout, id, cfg)

	repoRoot, branch, err := repoRootAndBranch()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	newID, err := sm.StartSession(ctx, repoRoot, branch, cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa: session %s started\n", newID)

	// Attach to the fresh session. For the `process` backend the SSHRunner
	// is never exercised; Attach just returns nil from the fake runner in
	// tests and from a real runner with no target in production — good
	// enough for v1 and matches the "process backend is dev/test only"
	// positioning in INTENT.md.
	if err := sm.Attach(ctx, newID); err != nil {
		// An attach failure is not fatal for the session itself; the
		// session keeps running. Surface the error but return 0 so the
		// session-started path is observable as success.
		fmt.Fprintf(stderr, "aa: attach after start failed (session still running): %v\n", err)
	}
	return 0
}

// repoRootAndBranch returns the absolute repo root and current branch
// name, shelling out to git for both.
func repoRootAndBranch() (string, string, error) {
	root, err := runGit("rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	branch, err := runGit("branch", "--show-current")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(root), strings.TrimSpace(branch), nil
}

// confirmUncommittedChanges implements README § Quickstart's
// "Uncommitted changes" warning: list every modified / untracked file
// grouped by git-status code, then prompt `[y/N]`. Returns true if the
// user accepts (or the tree is clean; nothing to prompt about), false
// otherwise.
func confirmUncommittedChanges(stdin io.Reader, stdout io.Writer) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	raw, err := cmd.Output()
	if err != nil {
		// Not a git repo or git unavailable — the StartSession path will
		// fail downstream with a clearer message; treat as "nothing to
		// warn about".
		return true
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return true
	}

	fmt.Fprintf(stdout, "  ⚠  Uncommitted changes:\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fmt.Fprintf(stdout, "       %s\n", line)
	}
	fmt.Fprintln(stdout)
	fmt.Fprint(stdout, "     These will be included in the session. Continue? [y/N] ")

	reader := bufio.NewReader(stdin)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(strings.ToLower(ans))
	return ans == "y" || ans == "yes"
}

// printSessionStartBanner emits the README § Quickstart banner shape:
// one line for the session name, one for the egress allowlist (⚡ safe
// baseline, ⚠ less-safe), one for the backend mode, one for the
// ephemeral key TTL and spend cap.
func printSessionStartBanner(w io.Writer, id SessionID, cfg Config) {
	fmt.Fprintf(w, "  ◆ starting session: %s\n", id)

	// Egress line: ⚡ if a specific allowlist is configured, ⚠ if it is
	// unrestricted (["*"]) or absent.
	var agent AgentConfig
	for _, a := range cfg.Agents {
		agent = a
		break
	}
	egressLabel := "⚡"
	egressValue := strings.Join(agent.EgressAllowlist, ", ")
	if len(agent.EgressAllowlist) == 0 || (len(agent.EgressAllowlist) == 1 && agent.EgressAllowlist[0] == "*") {
		egressLabel = "⚠"
		egressValue = "UNRESTRICTED"
	}
	fmt.Fprintf(w, "  %s egress allowlist: %s\n", egressLabel, egressValue)

	// Backend line.
	backendLabel := "⚡"
	backendName := cfg.DefaultBackend
	if bc, ok := cfg.Backends[backendName]; ok {
		if bc.Type == "process" {
			backendLabel = "⚠"
			backendName = backendName + " (no isolation — dev/test only)"
		}
	}
	fmt.Fprintf(w, "  %s backend: %s\n", backendLabel, backendName)

	// Ephemeral key line: conventional TTL 8h / spend cap $50 from
	// README § Credentials.
	fmt.Fprintln(w, "  ⚡ ephemeral key: TTL 8h, spend cap $50")
}
