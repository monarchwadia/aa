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
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// dockerHelperImage is the image used for the privileged egress-install helper.
// It is a tiny busybox-flavored image with iptables available; the exact tag
// is an implementation detail we can revisit when wiring up egress.go.
const dockerHelperImage = "aa-egress-helper:latest"

// LocalBackend implements Backend by shelling out to the `docker` CLI on
// the laptop. All exec invocations route through DockerExecCommand so tests
// can substitute a recorder; production leaves it as exec.Command.
//
// Container identity across Backend methods (ReadRemoteFile, StreamLogs,
// Teardown) comes from Host.Address: RunContainer populates the returned
// ContainerHandle's ID with docker's stdout, and the session manager is
// expected to copy that ID into Host.Address before later calls. This keeps
// LocalBackend stateless — two concurrent sessions with their own Host
// values cannot tread on each other via a shared map on the backend.
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

// Provision is a no-op for the local backend: the "host" is the laptop
// itself, and Docker is assumed to be already running. No docker calls are
// made here; that keeps `aa kill` from any point reversible without having
// created any external state during Provision.
//
// Example:
//
//	host, err := b.Provision(ctx, SessionID("myapp-feature-oauth"))
//	// host == Host{BackendType: "local", Workspace: "/workspace"}
func (b *LocalBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	return Host{
		BackendType: "local",
		Workspace:   "/workspace",
	}, nil
}

// InstallEgress installs host-kernel iptables rules + the forward proxy by
// running a privileged helper container (`--net=host --privileged`) that
// writes the rules into Docker Desktop's Linux VM. See README § "macOS
// local backend" and docs/architecture/aa.md § "Decision 3".
//
// When allowlist is nil, this is a documented no-op and makes no Docker
// invocations. The caller (session manager) signals "egress_enforcement =
// none" by passing a nil allowlist; an empty but non-nil allowlist would
// still run the helper (which would block all egress).
//
// Example:
//
//	err := b.InstallEgress(ctx, host, []string{"api.anthropic.com"})
func (b *LocalBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	if allowlist == nil {
		return nil
	}

	// The helper container runs with `--net=host --privileged` so its
	// iptables calls land in Docker Desktop's Linux VM rather than in a
	// namespaced container. We pass the allowlist as a comma-separated
	// argument; the helper image is responsible for translating it into
	// concrete iptables/proxy config.
	args := []string{
		"run", "--rm",
		"--net=host",
		"--privileged",
		dockerHelperImage,
		"install-egress",
		strings.Join(allowlist, ","),
	}
	cmd := b.DockerExecCommand("docker", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker run (install-egress helper) failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// RunContainer starts the agent container via `docker run`, mounting the
// repo working tree at /workspace (read-write), injecting AA_WORKSPACE and
// AA_SESSION_ID plus every spec.Env entry as `-e KEY=VALUE`, and running
// `bash -lc "<spec.AgentRun>"` as the container's entrypoint. The returned
// ContainerHandle's ID matches docker's container id/name (whatever
// `docker run -d` printed on stdout, trimmed of whitespace).
//
// Example:
//
//	handle, err := b.RunContainer(ctx, host, ContainerSpec{
//	    Image:     "agent-image:latest",
//	    AgentRun:  "claude --dangerously-skip-permissions",
//	    Env:       map[string]string{"ANTHROPIC_API_KEY": "sk-..."},
//	    SessionID: SessionID("myapp-feature-oauth"),
//	})
func (b *LocalBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	// Deterministic argv order for readability and for tests:
	//   docker run -d --name <sid> -v .:/workspace -e AA_WORKSPACE=... -e AA_SESSION_ID=... -e K=V ... <image> bash -lc <run>
	args := []string{
		"run", "-d",
		"--name", string(spec.SessionID),
		"-v", ".:/workspace",
		"-e", "AA_WORKSPACE=" + host.Workspace,
		"-e", "AA_SESSION_ID=" + string(spec.SessionID),
	}

	// Sort spec.Env keys so argv is deterministic regardless of map
	// iteration order. This keeps test assertions and diagnostics stable.
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+spec.Env[k])
	}

	args = append(args, spec.Image, "bash", "-lc", spec.AgentRun)

	cmd := b.DockerExecCommand("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ContainerHandle{}, fmt.Errorf("docker run failed: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}

	containerID := strings.TrimSpace(stdout.String())
	return ContainerHandle{
		ID:   containerID,
		Host: host,
	}, nil
}

// ReadRemoteFile reads a single file out of a running container by invoking
// `docker exec <container> cat <path>` and capturing stdout. The container
// is identified by host.Address (populated by the caller from a prior
// RunContainer's ContainerHandle.ID). The path is passed as a separate argv
// element — never interpolated into a shell string — so shell
// metacharacters in the path cannot be interpreted.
//
// Example:
//
//	state, err := b.ReadRemoteFile(ctx, host, ".aa/state")
func (b *LocalBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	cmd := b.DockerExecCommand("docker", "exec", host.Address, "cat", relpath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker exec cat %q failed: %w: %s",
			relpath, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// StreamLogs tails the container's stdout/stderr via `docker logs -f
// <container>`, writing bytes to w as they arrive. The call returns when
// docker exits (e.g. container stopped) or when ctx cancels and docker
// notices via the *exec.Cmd's context wiring.
//
// Note: relpath is part of the Backend contract (process backend tails a
// file at $AA_WORKSPACE/<relpath>), but for the local container backend
// "logs" means the container's stdout/stderr stream. We ignore relpath
// here and let `docker logs -f` do what it does.
//
// Example:
//
//	err := b.StreamLogs(ctx, host, ".aa/agent.log", os.Stdout)
func (b *LocalBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	cmd := b.DockerExecCommand("docker", "logs", "-f", host.Address)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker logs -f %q failed: %w: %s",
			host.Address, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Teardown stops and removes the agent container: `docker stop <id>`
// followed by `docker rm <id>`. Idempotent: errors from either command are
// swallowed because the desired end state is "container gone" — if docker
// reports "no such container" (or stop fails because it's already stopped,
// or rm fails because it's already removed), the post-state is what we
// wanted anyway.
//
// If host.Address is empty, Teardown is a no-op with zero docker calls —
// a partial Provision that never reached RunContainer has nothing to clean
// up. This supports the "kill from any point in the lifecycle" invariant.
//
// Example:
//
//	_ = b.Teardown(ctx, host)
func (b *LocalBackend) Teardown(ctx context.Context, host Host) error {
	if host.Address == "" {
		return nil
	}

	// Intentionally swallow errors from both commands: "no such container"
	// is the most common failure mode here and it means the desired state
	// is already reached. A genuine docker-daemon outage will surface on
	// the next session start, where it is actionable.
	stopCmd := b.DockerExecCommand("docker", "stop", host.Address)
	_ = stopCmd.Run()

	rmCmd := b.DockerExecCommand("docker", "rm", host.Address)
	_ = rmCmd.Run()

	return nil
}
