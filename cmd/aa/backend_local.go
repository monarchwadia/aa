// backend_local.go implements the `local` Backend — the laptop's own Docker
// daemon running an agent container, with host-kernel iptables + forward
// proxy for egress enforcement installed via a privileged helper container
// (see docs/architecture/aa.md § "Decision 3").
//
// This file is NOT in strict mode (see docs/PHILOSOPHY.md § "Strict mode").
// It is orchestration that shells out to `docker`; the security boundary
// lives in egress.go and cmd/aa-proxy. Axes 1 (Clarity) and 2 (Evolvability)
// are the governing concerns here.
//
// Testability: every shell-out goes through the DockerExecCommand function
// variable on LocalBackend. Production wires it to exec.Command; tests wire
// it to a recorder that captures argv and fabricates `*exec.Cmd` values
// that pipe canned bytes instead of executing docker. See the
// `backend-local` workstream in docs/architecture/aa.md § Workstreams.
package main

import (
	"context"
	"io"
	"os/exec"
)

// LocalBackend implements Backend by shelling out to the `docker` CLI on
// the laptop. All exec invocations route through DockerExecCommand so tests
// can substitute a recorder; production leaves it as exec.Command.
//
// Example:
//
//	backend := NewLocalBackend()
//	host, err := backend.Provision(ctx, SessionID("myapp-feature-oauth"))
//	if err != nil { return err }
//	defer backend.Teardown(ctx, host)
type LocalBackend struct {
	// DockerExecCommand constructs an *exec.Cmd for a given program + argv.
	// Defaults to exec.Command. Tests replace this with a recorder that
	// captures invocations and returns a *exec.Cmd whose stdout is canned
	// bytes, so no real Docker daemon is required.
	DockerExecCommand func(name string, args ...string) *exec.Cmd
}

// NewLocalBackend returns a LocalBackend wired to exec.Command — i.e., real
// `docker` on the host. Tests construct LocalBackend literals directly and
// override DockerExecCommand instead of calling this constructor.
//
// Example:
//
//	b := NewLocalBackend()
//	// b.Provision / b.RunContainer / b.Teardown use real docker.
func NewLocalBackend() *LocalBackend {
	return &LocalBackend{DockerExecCommand: exec.Command}
}

// Compile-time assertion that *LocalBackend satisfies Backend.
var _ Backend = (*LocalBackend)(nil)

// Provision is effectively a no-op for the local backend: the "host" is the
// laptop itself. The returned Host has BackendType="local" and the
// conventional container workspace path "/workspace". Implementations may
// run a single `docker version` probe to fail loud if docker is unreachable;
// that's the only legal Docker interaction in Provision.
func (b *LocalBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}

// InstallEgress installs host-kernel iptables rules + the forward proxy by
// running a privileged helper container (`--net=host --privileged`) that
// writes the rules into Docker Desktop's Linux VM. See README § "macOS
// local backend" and docs/architecture/aa.md § "Decision 3".
//
// When the backend's egress_enforcement is "none", this is a documented
// no-op and makes no Docker invocations. The caller (session manager) is
// responsible for deciding whether to call this at all based on config.
func (b *LocalBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}

// RunContainer starts the agent container via `docker run`, mounting the
// repo working tree at /workspace (read-write), injecting AA_WORKSPACE and
// AA_SESSION_ID plus every spec.Env entry as `-e KEY=VALUE`, and running
// `bash -lc "<spec.AgentRun>"` as the container's entrypoint. The returned
// ContainerHandle's ID matches docker's container id/name.
func (b *LocalBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}

// ReadRemoteFile reads a single file out of a running container by invoking
// `docker exec <container> cat <path>` and capturing stdout. The path is
// passed as a separate argv element — never interpolated into a shell
// string — so shell metacharacters in the path cannot be interpreted.
func (b *LocalBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}

// StreamLogs tails the container's stdout/stderr via `docker logs -f
// <container>`, writing bytes to w as they arrive, until ctx cancels.
func (b *LocalBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}

// Teardown stops and removes the agent container: `docker stop <id>`
// followed by `docker rm <id>`. Idempotent: errors from "no such container"
// are swallowed because the post-state matches the desired state. Safe to
// call after a partial Provision that never reached RunContainer (returns
// nil with no Docker invocations when host.Address identifies no container).
func (b *LocalBackend) Teardown(ctx context.Context, host Host) error {
	panic("unimplemented — see workstream `backend-local` in docs/architecture/aa.md § Workstreams")
}
