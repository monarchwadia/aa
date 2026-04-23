// backend_fly.go implements the `fly` Backend — a fresh Firecracker microVM
// per session, provisioned via the `flyctl` CLI, with in-VM operations
// (file reads, log streaming, egress rule installation) routed through an
// injected SSHRunner.
//
// This file is NOT in strict mode (see docs/PHILOSOPHY.md § "Strict mode").
// It is orchestration that shells out to `flyctl`; the security boundary
// for shell composition lives in cmd/aa/ssh.go / cmd/aa/ssh_runner.go, and
// egress rule construction lives in cmd/aa/egress.go. Axes 1 (Clarity),
// 2 (Evolvability), and 3 (Observability) are the governing concerns here.
//
// Testability: every flyctl shell-out goes through FlyctlExecCommand on
// FlyBackend. Production wires it to exec.Command; tests wire it to a
// recorder that captures argv and fabricates *exec.Cmd values that pipe
// canned bytes instead of executing flyctl. In-VM operations route through
// FlyBackend.SSHRunner; tests inject the shared fakeSSHRunner defined in
// fakes_test.go. Together this means the entire backend can be exercised
// without a real Fly.io account.
//
// See docs/architecture/aa.md § "Workstreams" → `backend-fly`.
package main

import (
	"context"
	"io"
	"os/exec"
)

// FlyBackend implements Backend by shelling out to the `flyctl` CLI for VM
// lifecycle (provision, destroy) and delegating in-VM operations to an
// injected SSHRunner. All flyctl invocations route through FlyctlExecCommand
// so tests can substitute a recorder; production leaves it as exec.Command.
//
// Example:
//
//	runner := NewRealSSHRunner()
//	backend := NewFlyBackend("iad", "shared-cpu-2x", runner)
//	host, err := backend.Provision(ctx, SessionID("myapp-feature-oauth"))
//	if err != nil { return err }
//	defer backend.Teardown(ctx, host)
type FlyBackend struct {
	// FlyctlExecCommand constructs an *exec.Cmd for a given program + argv.
	// Defaults to exec.Command (wired by NewFlyBackend). Tests replace this
	// with a recorder that captures invocations and returns a *exec.Cmd
	// whose stdout is canned bytes, so no real Fly.io account is required.
	FlyctlExecCommand func(name string, args ...string) *exec.Cmd

	// SSHRunner runs commands inside the provisioned VM. Used by
	// InstallEgress (iptables composition), ReadRemoteFile (cat), and
	// StreamLogs (tail -f). The backend never opens raw SSH on its own —
	// every byte of in-VM interaction passes through this runner.
	SSHRunner SSHRunner

	// Region is the Fly region Provision passes to `flyctl machine run
	// --region`. Example: "iad", "yyz", "fra".
	Region string

	// VMSize is the Fly machine preset Provision passes to `flyctl machine
	// run --vm-size`. Example: "shared-cpu-2x", "performance-2x".
	VMSize string
}

// NewFlyBackend returns a FlyBackend wired to exec.Command — i.e., real
// `flyctl` on the host — and the provided SSHRunner for in-VM operations.
// Tests construct FlyBackend literals directly and override
// FlyctlExecCommand + SSHRunner instead of calling this constructor.
//
// Example:
//
//	runner := NewRealSSHRunner()
//	b := NewFlyBackend("iad", "shared-cpu-2x", runner)
//	// b.Provision / b.RunContainer / b.Teardown use real flyctl + ssh.
func NewFlyBackend(region, vmSize string, ssh SSHRunner) *FlyBackend {
	return &FlyBackend{
		FlyctlExecCommand: exec.Command,
		SSHRunner:         ssh,
		Region:            region,
		VMSize:            vmSize,
	}
}

// Compile-time assertion that *FlyBackend satisfies Backend. Forces a
// compile error if FlyBackend ever drifts out of the Backend interface.
var _ Backend = (*FlyBackend)(nil)

// Provision spins up a fresh Firecracker microVM via `flyctl machine run`,
// using the backend's Region and VMSize, and returns a Host whose Address
// is the SSH target derived from flyctl's output, Workspace is the in-VM
// convention "/workspace", and BackendType is "fly". A non-zero exit from
// flyctl is surfaced as an error whose text includes the captured stderr.
func (b *FlyBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}

// InstallEgress installs the in-VM iptables rules and starts the forward
// proxy inside the microVM by invoking the SSHRunner — NOT flyctl. The
// egress-controller workstream (cmd/aa/egress.go) owns the concrete
// iptables composition; FlyBackend.InstallEgress is a thin shim that
// delegates the SSH-side work to it. When allowlist is empty, this is a
// documented no-op.
func (b *FlyBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}

// RunContainer runs the agent inside the already-provisioned microVM by
// invoking `flyctl machine exec` (or the equivalent flyctl command for
// in-VM command execution) with `bash -lc "<spec.AgentRun>"`. aa injects
// AA_WORKSPACE and AA_SESSION_ID plus every entry of spec.Env into the
// command's environment before exec.
func (b *FlyBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}

// ReadRemoteFile reads a single file from inside the microVM by delegating
// to the SSHRunner with a `cat <workspace>/<relpath>` command. The path is
// composed from host.Workspace and the caller-supplied relpath; the runner
// (cmd/aa/ssh_runner.go) is responsible for safe argv composition.
func (b *FlyBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}

// StreamLogs tails a file inside the microVM by delegating to the SSHRunner
// with a `tail -f <workspace>/<relpath>` command; bytes emitted by the
// runner flow through to w until ctx cancels.
func (b *FlyBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}

// Teardown destroys the microVM via `flyctl machine destroy <id>`. Safe to
// call repeatedly: a second Teardown after the machine is already gone
// returns nil, not an error ("machine not found" from flyctl is treated as
// success because the desired state is already reached). Calling Teardown
// after a partial Provision that never acquired a machine id (host.Address
// empty) returns nil with zero flyctl invocations.
func (b *FlyBackend) Teardown(ctx context.Context, host Host) error {
	panic("unimplemented — see workstream `backend-fly` in docs/architecture/aa.md § Workstreams")
}
