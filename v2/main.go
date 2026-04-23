package main

import (
	"fmt"
	"log"
	"os"

	"aa/v2/configstore"
	"aa/v2/extbin"
	"aa/v2/flyclient"
)

// flyAPIBase is the Fly Machines API root, overridable by FLY_API_BASE for
// integration tests that point the binary at a httptest.Server. Captured at
// startup so a change mid-run has no effect.
var flyAPIBase = func() string {
	if v := os.Getenv("FLY_API_BASE"); v != "" {
		return v
	}
	return "https://api.machines.dev/v1"
}()

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, `usage: aa <command> [args]

commands:
  config [key=value ...]              get or set config values
  machine <spawn|ls|start|stop|rm|attach> [args]   machine lifecycle
`)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "config":
		runConfig(os.Args[2:])
	case "machine":
		os.Exit(runMachineFromMain(os.Args[2:]))
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

// runMachineFromMain wires real collaborators (HTTP flyclient, os/exec
// runner, on-disk configstore) and delegates to RunMachine. Token is
// resolved inside RunMachine via requireToken; what we need here is a
// token to pass through to the flyclient constructor.
func runMachineFromMain(args []string) int {
	cfg, err := configstore.NewReader(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		return 1
	}
	// Resolve token early so flyclient can be constructed with a bearer.
	// If no token is set anywhere, RunMachine's own requireToken will print
	// the canonical diagnostic when it tries to use the client.
	token, _ := ResolveFlyToken("", os.Getenv, cfg)
	client := flyclient.New(flyAPIBase, token)
	deps := MachineDeps{
		Client: client,
		Runner: extbin.New(),
		Config: cfg,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return RunMachine(args, deps)
}
