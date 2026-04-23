// Package dockerimage — cmd.go: top-level dispatcher for `aa docker image`.
//
// Run executes one of build/push/ls/rm against injected collaborators.
// Production wires a real extbin.Runner and registry.Registry; tests inject
// fakes (an in-memory runner and an httptest.Server-backed registry).
package dockerimage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"aa/v2/extbin"
	"aa/v2/imageref"
	"aa/v2/registry"
)

// Deps is the injection surface for Run.
type Deps struct {
	DockerRunner extbin.Runner
	Registry     registry.Registry
	Token        string
	TokenKey     string // e.g. "token.flyio" — named in error messages.
	Stdout       io.Writer
	Stderr       io.Writer
}

// loginCache memoises `docker login` per (token,host) pair so that two Run
// invocations sharing the same Deps fire login at most once (ADR 4).
var loginCache sync.Map // key: "token|host" -> *sync.Once

// Run executes one of build/push/ls/rm and returns a process exit code.
// argv is everything AFTER "docker image" (e.g. ["build", "./myapi"]).
func Run(ctx context.Context, deps Deps, argv []string) int {
	if deps.Stdout == nil {
		deps.Stdout = io.Discard
	}
	if deps.Stderr == nil {
		deps.Stderr = io.Discard
	}
	if len(argv) == 0 {
		fmt.Fprintln(deps.Stderr, "usage: aa docker image <build|push|ls|rm> [args]")
		return 2
	}
	switch argv[0] {
	case "build":
		return runBuild(ctx, deps, argv[1:])
	case "push":
		return runPush(ctx, deps, argv[1:])
	case "ls":
		return runLs(ctx, deps, argv[1:])
	case "rm":
		return runRm(ctx, deps, argv[1:])
	default:
		fmt.Fprintf(deps.Stderr, "unknown docker image verb %q\n", argv[0])
		return 2
	}
}

func runBuild(ctx context.Context, deps Deps, args []string) int {
	path := "."
	tag := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tag" || a == "-t":
			if i+1 >= len(args) {
				fmt.Fprintf(deps.Stderr, "build: %s requires a value\n", a)
				return 2
			}
			tag = args[i+1]
			i++
		case strings.HasPrefix(a, "--tag="):
			tag = strings.TrimPrefix(a, "--tag=")
		default:
			path = a
		}
	}
	dockerfilePath := filepath.Join(path, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err != nil {
		fmt.Fprintf(deps.Stderr, "build: no Dockerfile at %s: %v\n", dockerfilePath, err)
		return 1
	}
	resolved, err := imageref.ResolveTag(path, tag)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "build: %v\n", err)
		return 1
	}
	ref, err := imageref.ParseFullyQualified(resolved)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "build: %v\n", err)
		return 1
	}
	code, err := deps.DockerRunner.Run(ctx, extbin.Invocation{
		Name:   "docker",
		Argv:   BuildArgv(ref, path),
		Stdout: deps.Stdout,
		Stderr: deps.Stderr,
	})
	if err != nil {
		fmt.Fprintf(deps.Stderr, "build: docker: %v\n", err)
		return 1
	}
	if code == 0 {
		fmt.Fprintf(deps.Stdout, "built %s\n", resolved)
	}
	return code
}

func runPush(ctx context.Context, deps Deps, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(deps.Stderr, "push: tag required")
		return 2
	}
	tag := args[0]
	ref, err := imageref.ParseFullyQualified(tag)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "push: %v\n", err)
		return 1
	}
	if code := ensureLogin(ctx, deps, ref.Host); code != 0 {
		return code
	}
	code, err := deps.DockerRunner.Run(ctx, extbin.Invocation{
		Name:   "docker",
		Argv:   PushArgv(ref),
		Stdout: deps.Stdout,
		Stderr: deps.Stderr,
	})
	if err != nil {
		fmt.Fprintf(deps.Stderr, "push: docker: %v\n", err)
		return 1
	}
	if code == 0 {
		fmt.Fprintf(deps.Stdout, "pushed %s\n", tag)
	}
	return code
}

func ensureLogin(ctx context.Context, deps Deps, host string) int {
	key := fmt.Sprintf("%p|%s|%s", deps.DockerRunner, deps.Token, host)
	val, _ := loginCache.LoadOrStore(key, &sync.Once{})
	once := val.(*sync.Once)
	var loginErr error
	var loginCode int
	once.Do(func() {
		loginCode, loginErr = deps.DockerRunner.Run(ctx, extbin.Invocation{
			Name:   "docker",
			Argv:   LoginArgv(host, deps.Token),
			Stdout: deps.Stdout,
			Stderr: deps.Stderr,
		})
	})
	if loginErr != nil {
		fmt.Fprintf(deps.Stderr, "login: docker: %v\n", loginErr)
		// Drop the cached once so a retry can attempt a fresh login.
		loginCache.Delete(key)
		return 1
	}
	if loginCode != 0 {
		loginCache.Delete(key)
		return loginCode
	}
	return 0
}

func runLs(ctx context.Context, deps Deps, args []string) int {
	prefix := imageref.DefaultNamespace + "/"
	for _, a := range args {
		if a == "--all" {
			prefix = ""
		}
	}
	images, err := deps.Registry.List(ctx, prefix)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "ls: %v\n", err)
		return 1
	}
	for _, img := range images {
		fmt.Fprintln(deps.Stdout, img.Tag)
	}
	return 0
}

func runRm(ctx context.Context, deps Deps, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(deps.Stderr, "rm: at least one tag required")
		return 2
	}
	exit := 0
	for _, tag := range args {
		if err := deps.Registry.Delete(ctx, tag); err != nil {
			fmt.Fprintf(deps.Stderr, "rm %s: %v\n", tag, err)
			exit = 1
			continue
		}
		fmt.Fprintf(deps.Stdout, "removed %s\n", tag)
	}
	return exit
}
