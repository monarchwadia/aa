package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"aa/v2/configstore"
	"aa/v2/dockerimage"
	"aa/v2/dockerup"
	"aa/v2/extbin"
	"aa/v2/flyclient"
	"aa/v2/registry"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, `usage: aa <command> [args]

commands:
  config [key=value ...]                       get or set config values
  machine <spawn|ls|start|stop|rm|attach> ...  machine lifecycle
  docker image <build|push|ls|rm> ...          container image management
  docker up <path> [--force]                   build, push, spawn, attach in one step
`)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "config":
		runConfig(os.Args[2:])
	case "machine":
		os.Exit(runMachineFromMain(os.Args[2:]))
	case "docker":
		os.Exit(runDockerFromMain(os.Args[2:]))
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
	token, _ := ResolveFlyToken("", os.Getenv, cfg)
	client := flyclient.New(cfg.ResolveAPIBase(), token)
	deps := MachineDeps{
		Client: client,
		Runner: extbin.New(),
		Config: cfg,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	return RunMachine(args, deps)
}

// runDockerFromMain dispatches `aa docker image ...` and `aa docker up ...`.
func runDockerFromMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: aa docker <image|up> ...")
		return 2
	}
	cfg, err := configstore.NewReader(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		return 1
	}
	token, _ := ResolveFlyToken("", os.Getenv, cfg)
	switch args[0] {
	case "image":
		if token == "" {
			fmt.Fprintln(os.Stderr, `no Fly.io token found — run: aa config token.flyio=<token>`)
			return 1
		}
		reg := registry.New(cfg.ResolveRegistryBase(), token)
		deps := dockerimage.Deps{
			DockerRunner: extbin.New(),
			Registry:     reg,
			Token:        token,
			TokenKey:     configstore.KeyFlyToken,
			Stdout:       os.Stdout,
			Stderr:       os.Stderr,
		}
		return dockerimage.Run(context.Background(), deps, args[1:])
	case "up":
		return runDockerUp(args[1:], cfg, token)
	default:
		fmt.Fprintf(os.Stderr, "unknown docker subcommand %q\n", args[0])
		return 2
	}
}

func runDockerUp(args []string, cfg *configstore.Reader, token string) int {
	fs := flag.NewFlagSet("docker up", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "destroy any existing machine bound to <path>")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: aa docker up <path> [--force]")
		return 2
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, `no Fly.io token found — run: aa config token.flyio=<token>`)
		return 1
	}
	client := flyclient.New(cfg.ResolveAPIBase(), token)
	reg := registry.New(cfg.ResolveRegistryBase(), token)
	opts := dockerup.Options{
		BuildContextPath: rest[0],
		Force:            *force,
		AppName:          cfg.ResolveDefaultApp(),
		RegistryBase:     cfg.ResolveRegistryBase(),
		Fly:              client,
		Registry:         reg,
		ExtBin:           extbin.New(),
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
	}
	if err := dockerup.Run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

