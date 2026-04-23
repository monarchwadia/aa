// Package extbin is the external-binary runner contract.
// Shelling out to external commands (flyctl, docker) goes through a Runner so tests can inject a fake.
// Bodies are stubs; Wave 2 fills them in.
package extbin

import (
	"context"
	"io"
)

// Invocation is one call to an external binary.
type Invocation struct {
	Name   string            // the binary name as found on PATH (e.g., "flyctl", "docker")
	Argv   []string          // arguments excluding argv[0]
	Env    map[string]string // extra env to merge on top of the parent process
	Stdin  io.Reader         // nil = no stdin
	Stdout io.Writer         // nil = discard
	Stderr io.Writer         // nil = discard
}

// Runner shells out to external commands.
// Implementations are goroutine-safe.
type Runner interface {
	// Run executes inv and returns the exit code and any error from starting the process.
	// A non-zero exit code is returned as an int; it is NOT wrapped in an error.
	// Err is non-nil only for setup failures (binary missing, context cancelled, stdin pipe error).
	Run(ctx context.Context, inv Invocation) (exitCode int, err error)
}

// New is defined in runner.go; see that file for the constructor doc comment.
