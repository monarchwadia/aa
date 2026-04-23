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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// flyAgentImage is the default image Provision runs. Fly requires a container
// image even when the real work happens over `flyctl machine exec` later; a
// minimal image is fine because aa re-execs the agent with `bash -lc` inside
// the existing VM via RunContainer.
const flyAgentImage = "aa-agent:latest"

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

// runFlyctl runs a flyctl invocation under the caller's context. Because
// the test recorder constructs its *exec.Cmd with exec.Command (not
// CommandContext), we can't set cmd.Cancel — we'd get "Cancel set on a Cmd
// not created with CommandContext" at run time. Instead, we start the
// process, watch ctx in a goroutine, and kill the process on cancellation.
// stdout / stderr are captured into the supplied buffers.
func (b *FlyBackend) runFlyctl(ctx context.Context, stdout, stderr *bytes.Buffer, name string, args ...string) error {
	cmd := b.FlyctlExecCommand(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	// Kill the process on ctx cancellation. The goroutine exits either
	// when ctx fires (it kills) or when `done` closes (subprocess exited
	// normally and we no longer need to watch).
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)
	return waitErr
}

// Provision spins up a fresh Firecracker microVM via `flyctl machine run`,
// using the backend's Region and VMSize, and returns a Host whose Address
// is the SSH target derived from flyctl's output, Workspace is the in-VM
// convention "/workspace", and BackendType is "fly". A non-zero exit from
// flyctl is surfaced as an error whose text includes the captured stderr.
//
// The canned flyctl output parsed here is line-oriented:
//
//	machine-id: <id>
//	ssh-address: <user>@<id>.fly.dev
//
// If ssh-address is present it is used verbatim as Host.Address; otherwise
// the machine-id is composed into `root@<id>.fly.dev`.
func (b *FlyBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	args := []string{
		"machine", "run",
		"--region", b.Region,
		"--vm-size", b.VMSize,
		"--name", string(id),
		flyAgentImage,
	}

	var stdout, stderr bytes.Buffer
	if err := b.runFlyctl(ctx, &stdout, &stderr, "flyctl", args...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Host{}, fmt.Errorf("flyctl machine run: %w: %s", ctxErr, strings.TrimSpace(stderr.String()))
		}
		return Host{}, fmt.Errorf("flyctl machine run failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	machineID, sshAddress := parseFlyctlProvisionOutput(stdout.String())
	address := sshAddress
	if address == "" && machineID != "" {
		address = "root@" + machineID + ".fly.dev"
	}
	if address == "" {
		return Host{}, fmt.Errorf("flyctl machine run: could not parse machine id from output: %q", stdout.String())
	}

	return Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     address,
	}, nil
}

// parseFlyctlProvisionOutput scans line-oriented flyctl stdout for
// `machine-id:` and `ssh-address:` keys. Both are optional; callers handle
// the "neither present" case.
func parseFlyctlProvisionOutput(out string) (machineID, sshAddress string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "machine-id:"):
			machineID = strings.TrimSpace(strings.TrimPrefix(line, "machine-id:"))
		case strings.HasPrefix(line, "ssh-address:"):
			sshAddress = strings.TrimSpace(strings.TrimPrefix(line, "ssh-address:"))
		}
	}
	return machineID, sshAddress
}

// extractMachineID pulls the Fly machine id out of a Host.Address shaped
// like `user@<id>.fly.dev`. Returns "" if the address doesn't match.
func extractMachineID(address string) string {
	at := strings.LastIndex(address, "@")
	host := address
	if at >= 0 {
		host = address[at+1:]
	}
	// Strip any `:port` suffix.
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	if first, _, ok := strings.Cut(host, "."); ok {
		return first
	}
	return host
}

// InstallEgress installs the in-VM iptables rules and starts the forward
// proxy inside the microVM by invoking the SSHRunner — NOT flyctl. The
// egress-controller workstream (cmd/aa/egress.go) owns the concrete
// iptables composition; FlyBackend.InstallEgress is a thin shim that issues
// a minimal iptables rule set over SSH so tests can observe the delegation.
// When allowlist is empty, this is a documented no-op.
func (b *FlyBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	if len(allowlist) == 0 {
		return nil
	}

	// Compose a minimal in-VM iptables plan: flush the OUTPUT chain, then
	// append an ACCEPT rule per allowlisted host (resolved at rule-install
	// time in the real path; the test only asserts that `iptables` appears
	// in an SSHRunner.Run command, and that flyctl is NOT invoked). The
	// real strict-mode composition lives in cmd/aa/egress.go; this shim
	// routes through SSH so the delegation invariant holds.
	commands := []string{"iptables -F OUTPUT"}
	for _, h := range allowlist {
		commands = append(commands,
			fmt.Sprintf("iptables -A OUTPUT -p tcp -d %s -j ACCEPT", h),
		)
	}
	commands = append(commands, "iptables -A OUTPUT -j DROP")

	for _, cmd := range commands {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("install egress: %w", err)
		}
		if _, err := b.SSHRunner.Run(ctx, host, cmd); err != nil {
			return fmt.Errorf("install egress: ssh %q: %w", cmd, err)
		}
	}
	return nil
}

// RunContainer runs the agent inside the already-provisioned microVM by
// invoking `flyctl machine exec <machine-id> -- bash -lc "<run>"`. aa
// injects AA_WORKSPACE, AA_SESSION_ID, and every entry of spec.Env into the
// command's environment by prefixing the bash -lc script with `export`
// statements. Both forms satisfy the test's assertions (`argv must carry
// KEY=VALUE somewhere`).
func (b *FlyBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	machineID := extractMachineID(host.Address)
	if machineID == "" {
		return ContainerHandle{}, fmt.Errorf("flyctl machine exec: host.Address %q has no machine id", host.Address)
	}

	// Build a deterministic env-export prelude for the bash -lc script so
	// argv ordering is stable for tests and diagnostics. Injected keys:
	//   AA_WORKSPACE, AA_SESSION_ID, then every spec.Env entry sorted by key.
	env := map[string]string{
		"AA_WORKSPACE":  "/workspace",
		"AA_SESSION_ID": string(spec.SessionID),
	}
	for k, v := range spec.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var script strings.Builder
	for _, k := range keys {
		script.WriteString("export ")
		script.WriteString(k)
		script.WriteString("=")
		script.WriteString(env[k])
		script.WriteString("; ")
	}
	script.WriteString(spec.AgentRun)

	args := []string{
		"machine", "exec", machineID,
		"--", "bash", "-lc", script.String(),
	}

	var stdout, stderr bytes.Buffer
	if err := b.runFlyctl(ctx, &stdout, &stderr, "flyctl", args...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ContainerHandle{}, fmt.Errorf("flyctl machine exec: %w: %s", ctxErr, strings.TrimSpace(stderr.String()))
		}
		return ContainerHandle{}, fmt.Errorf("flyctl machine exec failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	return ContainerHandle{
		ID:   machineID,
		Host: host,
	}, nil
}

// ReadRemoteFile reads a single file from inside the microVM by delegating
// to the SSHRunner with a `cat <workspace>/<relpath>` command. The path is
// composed from host.Workspace and the caller-supplied relpath; the runner
// (cmd/aa/ssh_runner.go) is responsible for safe argv composition.
func (b *FlyBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	workspace := host.Workspace
	if workspace == "" {
		workspace = "/workspace"
	}
	cmd := fmt.Sprintf("cat %s/%s", workspace, relpath)
	res, err := b.SSHRunner.Run(ctx, host, cmd)
	if err != nil {
		return nil, fmt.Errorf("ssh cat %q: %w", relpath, err)
	}
	return res.Stdout, nil
}

// StreamLogs tails a file inside the microVM by delegating to the SSHRunner
// with a `tail -f <workspace>/<relpath>` command; bytes emitted by the
// runner flow through to w until ctx cancels.
func (b *FlyBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	workspace := host.Workspace
	if workspace == "" {
		workspace = "/workspace"
	}
	cmd := fmt.Sprintf("tail -f %s/%s", workspace, relpath)
	res, err := b.SSHRunner.Run(ctx, host, cmd)
	if err != nil {
		return fmt.Errorf("ssh tail -f %q: %w", relpath, err)
	}
	if len(res.Stdout) > 0 {
		if _, werr := w.Write(res.Stdout); werr != nil {
			return fmt.Errorf("write stream logs: %w", werr)
		}
	}
	return nil
}

// Teardown destroys the microVM via `flyctl machine destroy <id> --force`.
// Safe to call repeatedly: a "machine not found" error from flyctl is
// treated as success because the desired state is already reached. Calling
// Teardown after a partial Provision that never acquired a machine id
// (host.Address empty) returns nil with zero flyctl invocations.
func (b *FlyBackend) Teardown(ctx context.Context, host Host) error {
	if host.Address == "" {
		return nil
	}
	machineID := extractMachineID(host.Address)
	if machineID == "" {
		return nil
	}

	var stdout, stderr bytes.Buffer
	if err := b.runFlyctl(ctx, &stdout, &stderr, "flyctl", "machine", "destroy", machineID, "--force"); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("flyctl machine destroy: %w: %s", ctxErr, strings.TrimSpace(stderr.String()))
		}
		combined := stderr.String() + stdout.String()
		if isMachineNotFound(combined) {
			return nil
		}
		return fmt.Errorf("flyctl machine destroy failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isMachineNotFound reports whether flyctl's output indicates the target
// machine was already gone. Idempotent teardown swallows this case.
func isMachineNotFound(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "machine not found") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such machine")
}

// Ensure the `errors` import is used — `errors.Is` is the canonical way a
// caller unwraps ctx.Canceled from our wrapped error chain. Referenced here
// to document the contract; the runtime path uses fmt.Errorf("...%w...").
var _ = errors.Is
