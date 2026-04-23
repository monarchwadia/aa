package main

import (
	"context"
	"io"
)

// SSHRunner is aa's abstraction for shelling out to `ssh` / `scp`. It uses
// the real ssh binary under the hood (never reimplements the protocol) and
// configures ControlMaster so repeated invocations against the same host
// share one TCP/SSH connection.
//
// See docs/architecture/aa.md § "Decision 1" and plan-1.md § "Shelling out
// to ssh/sftp" for why we shell out instead of using a Go SSH library.
type SSHRunner interface {
	// Run executes a non-interactive command on the remote host and
	// returns the captured stdout/stderr plus the process exit code.
	Run(ctx context.Context, host Host, cmd string) (SSHResult, error)

	// Attach runs an interactive command on the remote host, plumbing the
	// caller's stdin/stdout/stderr (and terminal size) through. Used for
	// `aa attach`. Returns when the remote command exits.
	Attach(ctx context.Context, host Host, cmd string, stdin io.Reader, stdout, stderr io.Writer) error

	// Copy transfers a single file to or from the remote host. Direction
	// is inferred from the presence of a scheme on src/dst: paths starting
	// with "host:" refer to the remote side.
	Copy(ctx context.Context, host Host, src, dst string) error
}

// SSHResult captures the output of a non-interactive SSH command.
type SSHResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}
