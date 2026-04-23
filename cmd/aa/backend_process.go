// Package main — backend_process.go
//
// ProcessBackend runs the agent as a detached child process directly on the
// laptop. There is NO isolation: no container, no VM, no egress firewall.
// The agent's process sees the laptop's filesystem, environment, and network
// the same way any other child process does.
//
// This backend exists so the dev loop and integration-test suite can run on
// any machine where Go runs — including CI runners without Docker. It is
// NOT intended for running real agents on untrusted code. See
// docs/architecture/aa.md § "Decision 4" and INTENT.md for the full rationale.
//
// Two guardrails make the danger explicit at runtime:
//
//   1. RunContainer refuses to launch unless the environment variable
//      AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 is set in the aa process's env.
//      Config alone is not sufficient.
//
//   2. InstallEgress refuses any allowlist more specific than "nothing to
//      enforce" (empty slice or the universal ["*"] escape hatch). Any other
//      value is a hard error, because this backend cannot enforce egress
//      without interfering with the user's own networking, and a silent
//      no-op would create the false impression that an allowlist is in force.
//
// The file that owns this backend is one of the deliverables of the
// `backend-process` workstream in docs/architecture/aa.md § "Workstreams".
package main

import (
	"context"
	"io"
	"os/exec"
)

// ProcessBackend implements Backend by running the agent as a detached child
// process on the laptop. WorkspacesRootDir is the absolute path under which
// each session gets its own workspace subdirectory; e.g. in production that's
// "~/.aa/workspaces" (expanded to an absolute path), and in tests it's a
// t.TempDir().
//
// StartAgentCommand is an optional hook that tests can set to intercept the
// exec.Cmd construction; in production it is nil and the backend uses
// exec.Command directly.
//
// Example:
//
//	backend := NewProcessBackend("/home/monarch/.aa/workspaces")
//	host, err := backend.Provision(ctx, SessionID("myapp-feature-oauth"))
type ProcessBackend struct {
	// WorkspacesRootDir is the absolute path under which per-session
	// workspace directories are created. Example: "/home/monarch/.aa/workspaces".
	WorkspacesRootDir string

	// StartAgentCommand, if non-nil, is used instead of exec.Command to
	// construct the *exec.Cmd that launches the agent. Tests set this to
	// inspect the Cmd before it's actually started (or to substitute a
	// harmless no-op binary).
	StartAgentCommand func(name string, args ...string) *exec.Cmd
}

// NewProcessBackend constructs a ProcessBackend rooted at the given absolute
// workspaces directory. The directory does not need to exist yet; Provision
// creates per-session subdirectories inside it.
//
// Example:
//
//	backend := NewProcessBackend("/home/monarch/.aa/workspaces")
func NewProcessBackend(workspacesRootDir string) *ProcessBackend {
	return &ProcessBackend{WorkspacesRootDir: workspacesRootDir}
}

// Provision creates a per-session workspace directory at
// WorkspacesRootDir/<id> and returns a Host describing the laptop. The
// returned Host has BackendType="process", Address="" (same machine), and
// Workspace set to the absolute path of the newly-created directory.
//
// Provision is part of the `backend-process` workstream.
func (b *ProcessBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	panic("backend-process workstream: ProcessBackend.Provision not implemented")
}

// InstallEgress is a guarded no-op. The process backend cannot enforce egress
// without interfering with the user's own laptop networking, so the only
// acceptable allowlists are the empty slice and ["*"]. Any other value is a
// hard error so the user cannot believe an allowlist is in effect when it
// isn't.
//
// InstallEgress is part of the `backend-process` workstream.
func (b *ProcessBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	panic("backend-process workstream: ProcessBackend.InstallEgress not implemented")
}

// RunContainer launches the agent's run command as a detached child process.
// Preconditions enforced here:
//
//   - AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 must be set in the aa process's
//     environment, or the call is refused with an error naming the variable.
//
// The child process:
//
//   - Has its cwd set to host.Workspace.
//   - Inherits AA_WORKSPACE and AA_SESSION_ID plus every entry in spec.Env.
//   - Is started in its own process group (setsid / Setpgid=true) so that
//     the aa process exiting does not cascade a SIGHUP to the agent.
//
// RunContainer is part of the `backend-process` workstream.
func (b *ProcessBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	panic("backend-process workstream: ProcessBackend.RunContainer not implemented")
}

// ReadRemoteFile reads a file from the session's workspace directly using
// os.ReadFile. relpath is interpreted relative to host.Workspace. There is
// no SSH, no docker, no indirection — this backend runs on the laptop.
//
// ReadRemoteFile is part of the `backend-process` workstream.
func (b *ProcessBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	panic("backend-process workstream: ProcessBackend.ReadRemoteFile not implemented")
}

// StreamLogs tails a file under host.Workspace by polling its size and
// copying new bytes into w. The poll loop exits when ctx is cancelled.
//
// StreamLogs is part of the `backend-process` workstream.
func (b *ProcessBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	panic("backend-process workstream: ProcessBackend.StreamLogs not implemented")
}

// Teardown kills the agent's process group (if it is still running) and
// removes host.Workspace from disk. Idempotent: calling Teardown a second
// time after the workspace is already gone returns nil. Calling Teardown
// after Provision but before RunContainer cleans up the empty workspace
// without process-kill errors.
//
// Teardown is part of the `backend-process` workstream.
func (b *ProcessBackend) Teardown(ctx context.Context, host Host) error {
	panic("backend-process workstream: ProcessBackend.Teardown not implemented")
}
