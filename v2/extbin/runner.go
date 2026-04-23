// runner.go implements extbin.Runner on top of os/exec.
//
// The concrete runner shells out with exec.CommandContext so context
// cancellation terminates a long-running child. Stdin / stdout / stderr are
// plumbed verbatim: nil means "discard / empty"; a non-nil reader or writer is
// passed straight to the child. The child's environment is the parent's
// os.Environ() merged with Invocation.Env — later keys win over earlier keys.
package extbin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// execRunner is the real Runner; zero-value safe.
type execRunner struct{}

// New returns a Runner that shells out via os/exec. The returned Runner is
// stateless and safe to share across goroutines.
//
// Example:
//
//	r := extbin.New()
//	code, err := r.Run(ctx, extbin.Invocation{
//	    Name: "flyctl",
//	    Argv: []string{"ssh", "console", "--app", "aa-apps", "--machine", "9080e6f3a12345"},
//	    Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
//	})
func New() Runner {
	return execRunner{}
}

// Run starts the binary named in inv, waits for it to exit, and returns its
// exit code. A non-zero exit code is NOT wrapped in an error. An error is
// returned only when the child could not be started (binary missing, fork
// failure) or when setup / context cancellation tore the process down.
func (execRunner) Run(ctx context.Context, inv Invocation) (int, error) {
	cmd := exec.CommandContext(ctx, inv.Name, inv.Argv...)
	cmd.Stdin = inv.Stdin
	cmd.Stdout = inv.Stdout
	cmd.Stderr = inv.Stderr
	if len(inv.Env) > 0 {
		cmd.Env = mergeEnv(os.Environ(), inv.Env)
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	// A non-zero exit is reported via ExitError; unwrap and return the code
	// without propagating the error, per the Invocation contract.
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("extbin: run %s: %w", inv.Name, err)
}

// mergeEnv returns parent env with the entries in extra appended — the Go
// exec package uses the last occurrence of a KEY= prefix, so appending is
// the idiomatic override.
func mergeEnv(parent []string, extra map[string]string) []string {
	out := make([]string, 0, len(parent)+len(extra))
	out = append(out, parent...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
