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
//  1. RunContainer refuses to launch unless the environment variable
//     AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 is set in the aa process's env.
//     Config alone is not sufficient.
//
//  2. InstallEgress refuses any allowlist more specific than "nothing to
//     enforce" (empty slice or the universal ["*"] escape hatch). Any other
//     value is a hard error, because this backend cannot enforce egress
//     without interfering with the user's own networking, and a silent
//     no-op would create the false impression that an allowlist is in force.
//
// The file that owns this backend is one of the deliverables of the
// `backend-process` workstream in docs/architecture/aa.md § "Workstreams".
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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

	// pidsMu guards pidsByWorkspace. Teardown and RunContainer can run from
	// different goroutines in tests and in production (aa kill arrives while
	// the agent is still running), so a mutex is load-bearing here.
	pidsMu sync.Mutex

	// pidsByWorkspace maps host.Workspace → PID of the started agent
	// process. Keyed by workspace rather than SessionID because Teardown's
	// signature receives a Host, not a SessionID, and Workspace is the
	// stable per-session identifier on this backend.
	pidsByWorkspace map[string]int
}

// NewProcessBackend constructs a ProcessBackend rooted at the given absolute
// workspaces directory. The directory does not need to exist yet; Provision
// creates per-session subdirectories inside it.
//
// Example:
//
//	backend := NewProcessBackend("/home/monarch/.aa/workspaces")
func NewProcessBackend(workspacesRootDir string) *ProcessBackend {
	return &ProcessBackend{
		WorkspacesRootDir: workspacesRootDir,
		pidsByWorkspace:   map[string]int{},
	}
}

// Provision creates a per-session workspace directory at
// WorkspacesRootDir/<id> and returns a Host describing the laptop. The
// returned Host has BackendType="process", Address="" (same machine), and
// Workspace set to the absolute path of the newly-created directory.
//
// Provision is part of the `backend-process` workstream.
func (b *ProcessBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	workspace := filepath.Join(b.WorkspacesRootDir, string(id))
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return Host{}, fmt.Errorf("process backend: create workspace %q: %w", workspace, err)
	}
	return Host{
		BackendType: "process",
		Address:     "",
		Workspace:   workspace,
	}, nil
}

// InstallEgress is a guarded no-op. The process backend cannot enforce egress
// without interfering with the user's own laptop networking, so the only
// acceptable allowlists are the empty slice and ["*"]. Any other value is a
// hard error so the user cannot believe an allowlist is in effect when it
// isn't.
//
// InstallEgress is part of the `backend-process` workstream.
func (b *ProcessBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	if len(allowlist) == 0 {
		return nil
	}
	if len(allowlist) == 1 && allowlist[0] == "*" {
		return nil
	}
	return fmt.Errorf(
		"process backend cannot enforce egress: allowlist %v is not empty or [\"*\"]; "+
			"set egress_enforcement=\"none\" in backend config (process backend forces it) "+
			"or switch to the `local` or `fly` backend for real enforcement",
		allowlist,
	)
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
	if os.Getenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND") != "1" {
		return ContainerHandle{}, fmt.Errorf(
			"process backend refused: AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 is required " +
				"because this backend runs the agent with full laptop access " +
				"(no container, no VM, no egress firewall); set the env var only " +
				"if you understand the risk — dev/test use only",
		)
	}

	start := b.StartAgentCommand
	if start == nil {
		start = exec.Command
	}
	cmd := start("bash", "-lc", spec.AgentRun)
	cmd.Dir = host.Workspace

	env := make([]string, 0, len(spec.Env)+2)
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, "AA_WORKSPACE="+host.Workspace)
	env = append(env, "AA_SESSION_ID="+string(spec.SessionID))
	cmd.Env = env

	// Detach stdio from the parent aa process. If we inherited aa's
	// stdout/stderr, the detached agent would keep those fds open after
	// aa exits — and when aa itself was spawned via exec.Cmd (as in the
	// e2e tests), its io.Copy pumps never see EOF and Wait() hangs
	// forever. Redirect to an agent.log file inside .aa/ so logs are
	// still observable.
	logDir := filepath.Join(host.Workspace, ".aa")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return ContainerHandle{}, fmt.Errorf("process backend: create log dir: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ContainerHandle{}, fmt.Errorf("process backend: open agent log: %w", err)
	}
	// Close our handle after Start — the child inherits its own dup.
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil

	// Detach: new session + new process group so the agent outlives the
	// aa process and does not receive its SIGHUP. INTENT success criterion
	// "detach, close the laptop, reopen hours later, and reattach" depends
	// on this.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setsid alone creates a new session AND a new process group — the
	// child becomes session leader and process-group leader. Combining
	// Setsid with Setpgid is rejected by setpgid(2) on Linux (EPERM), so
	// we set only Setsid.
	cmd.SysProcAttr.Setsid = true

	if err := cmd.Start(); err != nil {
		return ContainerHandle{}, fmt.Errorf("process backend: start agent: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	b.pidsMu.Lock()
	if b.pidsByWorkspace == nil {
		b.pidsByWorkspace = map[string]int{}
	}
	b.pidsByWorkspace[host.Workspace] = pid
	b.pidsMu.Unlock()

	// Persist the PID to disk so a subsequent `aa kill` invocation (which
	// runs in a fresh process and starts with an empty in-memory map) can
	// still find the agent process group to terminate. Without this, the
	// detached agent (Setsid) outlives every aa invocation forever.
	if pid != 0 {
		pidFile := filepath.Join(host.Workspace, ".aa", "pid")
		_ = os.MkdirAll(filepath.Dir(pidFile), 0o755)
		_ = os.WriteFile(pidFile, fmt.Appendf(nil, "%d\n", pid), 0o644)
	}

	return ContainerHandle{
		ID:   fmt.Sprintf("%d", pid),
		Host: host,
	}, nil
}

// ReadRemoteFile reads a file from the session's workspace directly using
// os.ReadFile. relpath is interpreted relative to host.Workspace. There is
// no SSH, no docker, no indirection — this backend runs on the laptop.
//
// ReadRemoteFile is part of the `backend-process` workstream.
func (b *ProcessBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	full := filepath.Join(host.Workspace, relpath)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("process backend: read %q: %w", full, err)
	}
	return data, nil
}

// StreamLogs tails a file under host.Workspace by polling its size and
// copying new bytes into w. The poll loop exits when ctx is cancelled.
//
// StreamLogs is part of the `backend-process` workstream.
func (b *ProcessBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	full := filepath.Join(host.Workspace, relpath)
	f, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("process backend: open log %q: %w", full, err)
	}
	defer f.Close()

	// Goroutine-free tail: a 100ms ticker polls for new bytes. Exit path is
	// ctx.Done — documented here as required by docs/PHILOSOPHY.md
	// "every `go func()` says how it terminates". There is no goroutine
	// here, but the exit contract is the same.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	buf := make([]byte, 32*1024)
	for {
		// Drain whatever is currently readable before checking ctx, so a
		// fast writer + fast cancel still gets everything written before
		// the cancel.
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return fmt.Errorf("process backend: write log chunk: %w", werr)
				}
			}
			if readErr == io.EOF || n == 0 {
				break
			}
			if readErr != nil {
				return fmt.Errorf("process backend: read log %q: %w", full, readErr)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Teardown kills the agent's process group (if it is still running) and
// removes host.Workspace from disk. Idempotent: calling Teardown a second
// time after the workspace is already gone returns nil. Calling Teardown
// after Provision but before RunContainer cleans up the empty workspace
// without process-kill errors.
//
// Teardown is part of the `backend-process` workstream.
func (b *ProcessBackend) Teardown(ctx context.Context, host Host) error {
	b.pidsMu.Lock()
	pid, hadPid := b.pidsByWorkspace[host.Workspace]
	if hadPid {
		delete(b.pidsByWorkspace, host.Workspace)
	}
	b.pidsMu.Unlock()

	// If the in-memory map is empty (typical for `aa kill` running in a
	// fresh process after the start invocation has long since exited),
	// fall back to the PID file written at RunContainer time.
	if !hadPid {
		pidFile := filepath.Join(host.Workspace, ".aa", "pid")
		if data, err := os.ReadFile(pidFile); err == nil {
			if parsed, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && parsed > 0 {
				pid = parsed
				hadPid = true
			}
		}
	}

	if hadPid && pid > 0 {
		// Signal the whole process group. Low ceremony: SIGKILL is the
		// one-shot option that doesn't require a follow-up timer. If the
		// group is already gone, Kill returns ESRCH, which is fine — we
		// are idempotent.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}

	if err := os.RemoveAll(host.Workspace); err != nil {
		return fmt.Errorf("process backend: remove workspace %q: %w", host.Workspace, err)
	}
	return nil
}
