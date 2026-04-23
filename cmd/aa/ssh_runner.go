// Package main: ssh_runner.go holds RealSSHRunner, the production
// implementation of the SSHRunner interface declared in ssh.go. It shells
// out to the OpenSSH `ssh` and `scp` binaries (never reimplements the
// protocol) and configures ControlMaster/ControlPath/ControlPersist so
// repeated calls against the same host share one transport.
//
// STRICT MODE — this file is listed in docs/PHILOSOPHY.md § "Strict mode —
// paths where defensive coding applies". Command composition here takes
// user-provided strings (hostnames, remote paths, remote commands) and
// must never let them become locally-executed shell. Every argv element is
// passed as a distinct exec.Cmd arg; nothing is interpolated into a shell
// invocation on the laptop side. The remote shell is the user's concern on
// the remote side (ssh inherently delegates there).
//
// Implementation is intentionally a stub at this point — see workstream
// `ssh-runner` in docs/architecture/aa.md § "Workstreams". Tests in
// ssh_runner_test.go pin the contract; a later implement-pass turns them
// green.
package main

import (
	"context"
	"io"
	"os/exec"
)

// RealSSHRunnerExecCommand is the indirection point the production code uses
// to spawn `ssh` / `scp` subprocesses. Tests override this to capture or
// fake the composed argv without touching the network or requiring an ssh
// binary. In production it is simply exec.CommandContext.
//
// Example (production):
//
//	cmd := RealSSHRunnerExecCommand(ctx, "ssh",
//	    "-o", "ControlMaster=auto", "user@example.com", "uname -a")
//	output, err := cmd.CombinedOutput()
//
// Example (test):
//
//	RealSSHRunnerExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
//	    // build a harmless *exec.Cmd that records name+args for later assertion
//	}
var RealSSHRunnerExecCommand = exec.CommandContext

// RealSSHRunner is the production SSHRunner. It shells out to `ssh` for
// Run/Attach and `scp` for Copy, layering ControlMaster options so a
// session that talks to the same host many times pays the handshake cost
// once.
//
// Example:
//
//	runner := NewRealSSHRunner("/home/alice/.ssh/aa-controlmaster")
//	result, err := runner.Run(ctx,
//	    Host{Address: "root@fly-vm-abc.internal", BackendType: "fly"},
//	    "cat /workspace/.aa/state")
//	if err != nil { return err }
//	fmt.Println(result.ExitCode, string(result.Stdout))
type RealSSHRunner struct {
	// ControlDir is the directory under which per-host ControlPath sockets
	// live. Conventionally `~/.ssh/aa-controlmaster`. The directory must
	// exist and be 0700 before Run/Attach/Copy is called.
	ControlDir string

	// ExtraSSHFlags are injected between `ssh` and `<host>` in the composed
	// argv. Used by tests and by callers that need extra `-o` options. Do
	// not put arguments that belong AFTER the host (the remote command)
	// here.
	ExtraSSHFlags []string

	// ExtraSCPFlags are injected between `scp` and the src/dst arguments.
	// Used by tests and callers that need extra `-o` options for scp only.
	ExtraSCPFlags []string
}

// NewRealSSHRunner constructs a RealSSHRunner rooted at the given control
// directory. The caller is responsible for creating controlDir with 0700
// permissions before use.
//
// Example:
//
//	runner := NewRealSSHRunner(filepath.Join(os.Getenv("HOME"), ".ssh", "aa-controlmaster"))
func NewRealSSHRunner(controlDir string) *RealSSHRunner {
	return &RealSSHRunner{ControlDir: controlDir}
}

// Run executes a non-interactive command on host and returns the captured
// stdout/stderr plus the remote process exit code. A non-zero exit code is
// reported via SSHResult.ExitCode with a nil error; only failures to spawn
// or cancellations return a non-nil error.
//
// Example:
//
//	result, err := runner.Run(ctx,
//	    Host{Address: "root@10.0.0.5"},
//	    "test -f /workspace/.aa/result.patch")
//	// result.ExitCode == 0 or 1 depending on whether the file exists.
func (r *RealSSHRunner) Run(ctx context.Context, host Host, cmd string) (SSHResult, error) {
	panic("unimplemented — see workstream ssh-runner in docs/architecture/aa.md § Workstreams")
}

// Attach runs an interactive command on host with a PTY, plumbing the
// supplied stdin/stdout/stderr through. This is the path `aa attach` uses
// to hand the user's terminal to a tmux session on the remote side.
//
// Example:
//
//	err := runner.Attach(ctx,
//	    Host{Address: "root@fly-vm-abc.internal"},
//	    "tmux attach -t aa-session",
//	    os.Stdin, os.Stdout, os.Stderr)
func (r *RealSSHRunner) Attach(ctx context.Context, host Host, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	panic("unimplemented — see workstream ssh-runner in docs/architecture/aa.md § Workstreams")
}

// Copy transfers a single file between laptop and host using scp.
// Direction is inferred from whether src or dst contains a "host:" prefix
// (scp syntax). The ControlMaster socket, if present, is reused.
//
// Example (local → remote):
//
//	err := runner.Copy(ctx,
//	    Host{Address: "root@10.0.0.5"},
//	    "/home/alice/aa-proxy", "root@10.0.0.5:/usr/local/bin/aa-proxy")
//
// Example (remote → local):
//
//	err := runner.Copy(ctx,
//	    Host{Address: "root@10.0.0.5"},
//	    "root@10.0.0.5:/workspace/.aa/result.patch",
//	    "/home/alice/repo/.aa/result.patch")
func (r *RealSSHRunner) Copy(ctx context.Context, host Host, src, dst string) error {
	panic("unimplemented — see workstream ssh-runner in docs/architecture/aa.md § Workstreams")
}

// Compile-time proof that *RealSSHRunner satisfies the SSHRunner contract.
// When the stub bodies are filled in, this line is what forces the method
// set to stay aligned with ssh.go.
var _ SSHRunner = (*RealSSHRunner)(nil)
