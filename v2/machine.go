// machine.go is the single-file home of the six `aa machine <verb>` handlers
// described in docs/architecture/machine-lifecycle.md (W2d).
//
// RunMachine is the `aa machine <verb>` dispatcher entry point. Each verb
// lives as a small function that reads flags, resolves the token/app/image,
// and calls through the injected flyclient.Client, extbin.Runner, and
// configstore.Reader seams. No file in this package talks to net/http or
// os/exec directly — those boundaries are crossed only via MachineDeps.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"aa/v2/configstore"
	"aa/v2/extbin"
	"aa/v2/flyclient"
)

// MachineDeps is the injected set of collaborators the machine handlers use.
// Every external boundary (HTTP, exec, config) crosses one of these seams.
type MachineDeps struct {
	Client flyclient.Client
	Runner extbin.Runner
	Config *configstore.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// RunMachine is the `aa machine <verb>` dispatcher entry point.
// argv[0] is the verb (spawn|ls|start|stop|rm|attach); argv[1:] is the verb's own arguments.
// Returns the process exit code; the caller owns os.Exit.
//
// Example:
//
//	deps := MachineDeps{Client: fly, Runner: extbin.New(), Config: cfg, Stdout: os.Stdout, Stderr: os.Stderr}
//	code := RunMachine([]string{"ls"}, deps)
func RunMachine(argv []string, deps MachineDeps) int {
	if deps.Stdout == nil {
		deps.Stdout = os.Stdout
	}
	if deps.Stderr == nil {
		deps.Stderr = os.Stderr
	}
	if len(argv) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: aa machine <spawn|ls|start|stop|rm|attach> [args]")
		return 2
	}
	verb := argv[0]
	rest := argv[1:]
	switch verb {
	case "spawn":
		return runSpawnVerb(rest, deps)
	case "ls":
		return runLsVerb(rest, deps)
	case "start":
		return runStartVerb(rest, deps)
	case "stop":
		return runStopVerb(rest, deps)
	case "rm":
		return runRmVerb(rest, deps)
	case "attach":
		return runAttachVerb(rest, deps)
	default:
		fmt.Fprintf(deps.Stderr, "unknown machine verb %q\n", verb)
		return 2
	}
}

// ResolveFlyToken applies the flag → env → config → (no built-in) precedence.
// flagValue is the --token flag's value (empty string if not passed).
// envLookup is injected so tests can supply a fake environment; nil means
// "use os.Getenv".
// Returns ("", false) if no token is resolvable at any layer.
//
// Example:
//
//	tok, ok := ResolveFlyToken("tok_explicit", nil, cfg)
//	// tok == "tok_explicit", ok == true
func ResolveFlyToken(flagValue string, envLookup func(string) string, cfg *configstore.Reader) (string, bool) {
	if flagValue != "" {
		return flagValue, true
	}
	if envLookup == nil {
		envLookup = os.Getenv
	}
	if v := envLookup("FLY_API_TOKEN"); v != "" {
		return v, true
	}
	if cfg != nil {
		// cfg.ResolveFlyToken checks flags first, env second, file third.
		// envLookup above has already been consulted for env; we still
		// delegate to cfg because a test may pre-load token.flyio into its
		// flags map (configstore.NewReader(map[...])), and because on the
		// real CLI path the file layer lives behind cfg.
		if v, ok := cfg.ResolveFlyToken(); ok {
			return v, true
		}
	}
	return "", false
}

// ResolveApp applies the flag → config → built-in precedence for --app.
// Example: ResolveApp("", cfg) == "aa-apps" when cfg has no override.
func ResolveApp(flagValue string, cfg *configstore.Reader) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg != nil {
		return cfg.ResolveDefaultApp()
	}
	return configstore.DefaultApp
}

// ResolveImage applies the flag → config → built-in precedence for --image.
// Built-in default is "ubuntu:22.04" per ADR-1.
// Example: ResolveImage("", cfg) == "ubuntu:22.04" when cfg has no override.
func ResolveImage(flagValue string, cfg *configstore.Reader) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg != nil {
		return cfg.ResolveDefaultImage()
	}
	return configstore.DefaultImage
}

// FormatMachineTable returns the tab-aligned table `aa machine ls` prints.
// An empty slice returns the `(no machines in "<app>")` message instead.
// Example: FormatMachineTable("aa-apps", nil) == "(no machines in \"aa-apps\")\n".
func FormatMachineTable(appName string, machines []flyclient.Machine) string {
	if len(machines) == 0 {
		return fmt.Sprintf("(no machines in %q)\n", appName)
	}
	var buf tabBuffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tREGION")
	for _, m := range machines {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.ID, m.State, m.Region)
	}
	w.Flush()
	return buf.String()
}

// tabBuffer is a tiny string builder that satisfies io.Writer without
// pulling in bytes.Buffer — avoids the extra import just for one helper.
type tabBuffer struct{ b []byte }

func (t *tabBuffer) Write(p []byte) (int, error) { t.b = append(t.b, p...); return len(p), nil }
func (t *tabBuffer) String() string               { return string(t.b) }

// requireToken resolves the Fly token or writes the canonical "no Fly.io token
// found" error to stderr and returns ("", false). The error message is the
// exact string the machine-lifecycle tests and docs pin.
func requireToken(flagValue string, deps MachineDeps) (string, bool) {
	tok, ok := ResolveFlyToken(flagValue, nil, deps.Config)
	if !ok {
		fmt.Fprintln(deps.Stderr, `no Fly.io token found — run: aa config token.flyio=<token>`)
	}
	return tok, ok
}

// --- per-verb handlers ---

func runSpawnVerb(args []string, deps MachineDeps) int {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(deps.Stderr)
	tokenFlag := fs.String("token", "", "Fly.io API token")
	appFlag := fs.String("app", "", "Fly.io app name")
	imageFlag := fs.String("image", "", "base image (e.g. ubuntu:22.04)")
	regionFlag := fs.String("region", "", "Fly.io region (e.g. iad)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tok, ok := requireToken(*tokenFlag, deps)
	if !ok {
		return 1
	}
	app := ResolveApp(*appFlag, deps.Config)
	image := ResolveImage(*imageFlag, deps.Config)
	ctx := context.Background()

	if err := preflight(); err != nil {
		fmt.Fprintln(deps.Stderr, err)
		return 1
	}
	if err := deps.Client.EnsureApp(ctx, app); err != nil {
		fmt.Fprintf(deps.Stderr, "ensure app: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "Creating machine in app %q (image: %s)...\n", app, image)
	m, err := deps.Client.Create(ctx, app, flyclient.SpawnSpec{Image: image, Region: *regionFlag})
	if err != nil {
		fmt.Fprintf(deps.Stderr, "create machine: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "Machine %s created (region: %s), waiting to start...\n", m.ID, m.Region)

	waitCtx, cancel := context.WithTimeout(ctx, BackendDeadline())
	defer cancel()
	if err := deps.Client.WaitStarted(waitCtx, app, m.ID); err != nil {
		fmt.Fprintf(deps.Stderr, "wait: %v\n", err)
		return 1
	}
	fmt.Fprintf(deps.Stdout, "Machine %s is running — waiting for SSH...\n", m.ID)

	if exit := bridgeShellReachable(ctx, app, m.ID, tok, deps); exit != 0 {
		return exit
	}
	return 0
}

// bridgeShellReachable invokes the runner with `flyctl ssh console` up to
// ShellAttemptBudget() times, sleeping NextShellAttemptDelay() between
// failures. Returns the final runner exit code, or 1 on exhausted retries.
func bridgeShellReachable(ctx context.Context, app, machineID, token string, deps MachineDeps) int {
	budget := ShellAttemptBudget()
	for attempt := 1; attempt <= budget; attempt++ {
		code, err := deps.Runner.Run(ctx, flyctlSSHInvocation(app, machineID, token, deps))
		if err == nil && code == 0 {
			return 0
		}
		if attempt == budget {
			if err != nil {
				fmt.Fprintf(deps.Stderr, "ssh console: %v\n", err)
			}
			fmt.Fprintf(deps.Stderr, "machine %s: SSH never became reachable after %d attempts — run: aa machine rm %s\n", machineID, budget, machineID)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "  SSH not ready yet (attempt %d/%d), retrying in 3s...\n", attempt, budget)
		select {
		case <-ctx.Done():
			return 1
		case <-time.After(NextShellAttemptDelay(attempt)):
		}
	}
	return 1
}

// flyctlSSHInvocation is the single argv shape the runner is ever asked to
// execute on behalf of machine-lifecycle. Centralised so tests pin it once.
func flyctlSSHInvocation(app, machineID, token string, deps MachineDeps) extbin.Invocation {
	return extbin.Invocation{
		Name:   "flyctl",
		Argv:   []string{"ssh", "console", "--app", app, "--machine", machineID},
		Env:    map[string]string{"FLY_API_TOKEN": token},
		Stdin:  os.Stdin,
		Stdout: deps.Stdout,
		Stderr: deps.Stderr,
	}
}

func runLsVerb(args []string, deps MachineDeps) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(deps.Stderr)
	tokenFlag := fs.String("token", "", "Fly.io API token")
	appFlag := fs.String("app", "", "Fly.io app name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, ok := requireToken(*tokenFlag, deps); !ok {
		return 1
	}
	app := ResolveApp(*appFlag, deps.Config)
	ms, err := deps.Client.List(context.Background(), app)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "list: %v\n", err)
		return 1
	}
	fmt.Fprint(deps.Stdout, FormatMachineTable(app, ms))
	return 0
}

func runStartVerb(args []string, deps MachineDeps) int {
	return runLifecycleVerb("start", args, deps, func(fs *flag.FlagSet) {},
		func(ctx context.Context, _ *flag.FlagSet, app, id string) error {
			return deps.Client.Start(ctx, app, id)
		})
}

func runStopVerb(args []string, deps MachineDeps) int {
	return runLifecycleVerb("stop", args, deps, func(fs *flag.FlagSet) {},
		func(ctx context.Context, _ *flag.FlagSet, app, id string) error {
			return deps.Client.Stop(ctx, app, id)
		})
}

func runRmVerb(args []string, deps MachineDeps) int {
	var force bool
	return runLifecycleVerb("rm", args, deps,
		func(fs *flag.FlagSet) { fs.BoolVar(&force, "force", false, "force destroy running machines") },
		func(ctx context.Context, _ *flag.FlagSet, app, id string) error {
			return deps.Client.Destroy(ctx, app, id, force)
		})
}

// runLifecycleVerb is the shared scaffolding for start/stop/rm: parse common
// flags, require ≥1 positional ID, iterate, print `<verb> <id> ok` per success.
func runLifecycleVerb(
	verb string,
	args []string,
	deps MachineDeps,
	extraFlags func(*flag.FlagSet),
	fn func(ctx context.Context, fs *flag.FlagSet, app, id string) error,
) int {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(deps.Stderr)
	tokenFlag := fs.String("token", "", "Fly.io API token")
	appFlag := fs.String("app", "", "Fly.io app name")
	extraFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ids := fs.Args()
	if len(ids) == 0 {
		fmt.Fprintf(deps.Stderr, "%s: at least one machine ID required\n", verb)
		return 2
	}
	if _, ok := requireToken(*tokenFlag, deps); !ok {
		return 1
	}
	app := ResolveApp(*appFlag, deps.Config)
	ctx := context.Background()
	for _, id := range ids {
		if err := fn(ctx, fs, app, id); err != nil {
			fmt.Fprintf(deps.Stderr, "%s %s: %v\n", verb, id, err)
			return 1
		}
		fmt.Fprintf(deps.Stdout, "%s %s ok\n", verb, id)
	}
	return 0
}

func runAttachVerb(args []string, deps MachineDeps) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(deps.Stderr)
	tokenFlag := fs.String("token", "", "Fly.io API token")
	appFlag := fs.String("app", "", "Fly.io app name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ids := fs.Args()
	if len(ids) != 1 {
		fmt.Fprintln(deps.Stderr, "attach: exactly one machine ID required")
		return 2
	}
	machineID := ids[0]
	tok, ok := requireToken(*tokenFlag, deps)
	if !ok {
		return 1
	}
	app := ResolveApp(*appFlag, deps.Config)
	if err := preflight(); err != nil {
		fmt.Fprintln(deps.Stderr, err)
		return 1
	}
	ctx := context.Background()
	m, err := deps.Client.Get(ctx, app, machineID)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "get machine: %v\n", err)
		return 1
	}
	if m.State == "stopped" {
		fmt.Fprintf(deps.Stderr, "machine %s is stopped — run: aa machine start %s\n", machineID, machineID)
		return 1
	}
	code, err := deps.Runner.Run(ctx, flyctlSSHInvocation(app, machineID, tok, deps))
	if err != nil {
		fmt.Fprintf(deps.Stderr, "attach: %v\n", err)
		return 1
	}
	return code
}
