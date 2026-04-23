package main

// Red tests for the backend-process workstream. Every test in this file
// currently fails by panic from the stubs in backend_process.go; the
// `implement` skill is what turns them green.
//
// Every enforcement test pins an INTENT.md constraint:
//
//   - AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 gate (INTENT Constraints §3)
//     → TestProcessBackend_RefusesWithoutEnvGate
//     → TestProcessBackend_AcceptsWithEnvGateSet
//
//   - egress_enforcement forced to "none" (INTENT Constraints §3)
//     → TestProcessBackend_InstallEgressRejectsNonEmptyAllowlist
//     → TestProcessBackend_InstallEgressAcceptsEmptyAllowlist
//     → TestProcessBackend_InstallEgressAcceptsWildcardAllowlist
//
//   - Workspace is a real path under WorkspacesRootDir/<id> (INTENT
//     success-criteria: agent-env contract uses AA_WORKSPACE as an absolute
//     path per session)
//     → TestProcessBackend_WorkspaceExistsAfterProvision
//
// Lifecycle tests cover the rest of the Backend interface under the process
// backend's "no isolation, laptop filesystem" model.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Enforcement: the AA_ALLOW_UNSAFE_PROCESS_BACKEND env gate.
// ---------------------------------------------------------------------------

func TestProcessBackend_RefusesWithoutEnvGate(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx := context.Background()
	host, err := backend.Provision(ctx, SessionID("test-session"))
	if err != nil {
		t.Fatalf("Provision returned error before RunContainer check: %v", err)
	}

	_, err = backend.RunContainer(ctx, host, ContainerSpec{
		AgentRun:  "true",
		SessionID: SessionID("test-session"),
	})
	if err == nil {
		t.Fatal("RunContainer must refuse when AA_ALLOW_UNSAFE_PROCESS_BACKEND is unset")
	}
	if !strings.Contains(err.Error(), "AA_ALLOW_UNSAFE_PROCESS_BACKEND") {
		t.Errorf("error must name the env var; got: %v", err)
	}
}

func TestProcessBackend_RefusesWhenEnvGateSetToZero(t *testing.T) {
	// Not "=1" is not enough — only "1" opts in.
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "0")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx := context.Background()
	host, err := backend.Provision(ctx, SessionID("test-session"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	_, err = backend.RunContainer(ctx, host, ContainerSpec{
		AgentRun:  "true",
		SessionID: SessionID("test-session"),
	})
	if err == nil {
		t.Fatal("RunContainer must refuse when AA_ALLOW_UNSAFE_PROCESS_BACKEND != 1")
	}
}

func TestProcessBackend_AcceptsWithEnvGateSet(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "1")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	var captured *exec.Cmd
	backend.StartAgentCommand = func(name string, args ...string) *exec.Cmd {
		// Use "true" (or the Windows equivalent) so the process exits
		// immediately without side effects — this test only asserts the
		// gate lets us through, not anything about the child.
		c := exec.Command("true")
		captured = c
		return c
	}

	ctx := context.Background()
	host, err := backend.Provision(ctx, SessionID("test-session"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	_, err = backend.RunContainer(ctx, host, ContainerSpec{
		AgentRun:  "true",
		SessionID: SessionID("test-session"),
	})
	if err != nil {
		t.Fatalf("RunContainer with env gate set must succeed; got: %v", err)
	}
	if captured == nil {
		t.Fatal("StartAgentCommand hook was not invoked")
	}
}

// ---------------------------------------------------------------------------
// Enforcement: InstallEgress accepts only "no enforcement" allowlists.
// ---------------------------------------------------------------------------

func TestProcessBackend_InstallEgressAcceptsEmptyAllowlist(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	host := Host{BackendType: "process", Workspace: filepath.Join(root, "test-session")}

	if err := backend.InstallEgress(context.Background(), host, nil); err != nil {
		t.Errorf("nil allowlist must be accepted (no enforcement); got: %v", err)
	}
	if err := backend.InstallEgress(context.Background(), host, []string{}); err != nil {
		t.Errorf("empty allowlist must be accepted (no enforcement); got: %v", err)
	}
}

func TestProcessBackend_InstallEgressAcceptsWildcardAllowlist(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	host := Host{BackendType: "process", Workspace: filepath.Join(root, "test-session")}

	if err := backend.InstallEgress(context.Background(), host, []string{"*"}); err != nil {
		t.Errorf("[\"*\"] allowlist must be accepted (documented escape hatch); got: %v", err)
	}
}

func TestProcessBackend_InstallEgressRejectsNonEmptyAllowlist(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	host := Host{BackendType: "process", Workspace: filepath.Join(root, "test-session")}

	cases := [][]string{
		{"api.anthropic.com"},
		{"api.anthropic.com", "registry.npmjs.org"},
		{"*", "api.anthropic.com"}, // wildcard mixed with real entries: not the escape hatch
	}
	for _, allowlist := range cases {
		err := backend.InstallEgress(context.Background(), host, allowlist)
		if err == nil {
			t.Errorf("InstallEgress must reject allowlist %v — process backend cannot enforce egress", allowlist)
		}
	}
}

// ---------------------------------------------------------------------------
// Provision creates a workspace directory and returns a well-formed Host.
// ---------------------------------------------------------------------------

func TestProcessBackend_WorkspaceExistsAfterProvision(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	id := SessionID("myapp-feature-oauth")
	host, err := backend.Provision(context.Background(), id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if host.BackendType != "process" {
		t.Errorf("Host.BackendType = %q, want %q", host.BackendType, "process")
	}
	if host.Address != "" {
		t.Errorf("Host.Address = %q, want empty string (same machine)", host.Address)
	}

	wantWorkspace := filepath.Join(root, string(id))
	if host.Workspace != wantWorkspace {
		t.Errorf("Host.Workspace = %q, want %q", host.Workspace, wantWorkspace)
	}
	if !filepath.IsAbs(host.Workspace) {
		t.Errorf("Host.Workspace = %q, must be absolute", host.Workspace)
	}

	info, err := os.Stat(host.Workspace)
	if err != nil {
		t.Fatalf("workspace not created on disk: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("workspace %q exists but is not a directory", host.Workspace)
	}
}

// ---------------------------------------------------------------------------
// RunContainer: cwd, env, detachment.
// ---------------------------------------------------------------------------

func TestProcessBackend_RunContainerSetsCwdAndContractEnv(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "1")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	var captured *exec.Cmd
	backend.StartAgentCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command("true")
		captured = c
		return c
	}

	ctx := context.Background()
	id := SessionID("contract-env-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	spec := ContainerSpec{
		AgentRun:  "echo hello",
		SessionID: id,
	}
	if _, err := backend.RunContainer(ctx, host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if captured == nil {
		t.Fatal("StartAgentCommand hook was not invoked")
	}

	if captured.Dir != host.Workspace {
		t.Errorf("cmd.Dir = %q, want %q (agent cwd must be the workspace)", captured.Dir, host.Workspace)
	}

	env := envMap(captured.Env)
	if got := env["AA_WORKSPACE"]; got != host.Workspace {
		t.Errorf("env AA_WORKSPACE = %q, want %q", got, host.Workspace)
	}
	if got := env["AA_SESSION_ID"]; got != string(id) {
		t.Errorf("env AA_SESSION_ID = %q, want %q", got, string(id))
	}
}

func TestProcessBackend_RunContainerPropagatesSpecEnv(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "1")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	var captured *exec.Cmd
	backend.StartAgentCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command("true")
		captured = c
		return c
	}

	ctx := context.Background()
	id := SessionID("env-propagation-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	spec := ContainerSpec{
		AgentRun:  "echo hi",
		SessionID: id,
		Env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-abc123",
			"CUSTOM_AGENT_FLAG": "enabled",
		},
	}
	if _, err := backend.RunContainer(ctx, host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if captured == nil {
		t.Fatal("StartAgentCommand hook was not invoked")
	}

	env := envMap(captured.Env)
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-test-abc123" {
		t.Errorf("spec env ANTHROPIC_API_KEY not propagated; got %q", got)
	}
	if got := env["CUSTOM_AGENT_FLAG"]; got != "enabled" {
		t.Errorf("spec env CUSTOM_AGENT_FLAG not propagated; got %q", got)
	}
}

func TestProcessBackend_RunContainerDetachesViaSetsid(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "1")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	var captured *exec.Cmd
	backend.StartAgentCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command("true")
		captured = c
		return c
	}

	ctx := context.Background()
	id := SessionID("detach-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if _, err := backend.RunContainer(ctx, host, ContainerSpec{
		AgentRun:  "true",
		SessionID: id,
	}); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if captured == nil {
		t.Fatal("StartAgentCommand hook was not invoked")
	}
	if captured.SysProcAttr == nil {
		t.Fatal("SysProcAttr must be set so the child is detached (setsid/Setpgid)")
	}
	// On Unix, Setpgid=true + Setsid=true gives the child a new session so
	// SIGHUP from the parent is not propagated. Either Setsid or Setpgid is
	// acceptable evidence the backend did the right thing.
	if !captured.SysProcAttr.Setsid && !captured.SysProcAttr.Setpgid {
		t.Errorf("SysProcAttr must enable Setsid or Setpgid for process-group detachment; got %+v", captured.SysProcAttr)
	}
}

// ---------------------------------------------------------------------------
// ReadRemoteFile: direct os.ReadFile from the workspace.
// ---------------------------------------------------------------------------

func TestProcessBackend_ReadRemoteFileReadsFromWorkspace(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx := context.Background()
	id := SessionID("read-file-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Simulate the agent writing its state file.
	stateDir := filepath.Join(host.Workspace, ".aa")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir .aa: %v", err)
	}
	want := []byte("DONE\n")
	if err := os.WriteFile(filepath.Join(stateDir, "state"), want, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	got, err := backend.ReadRemoteFile(ctx, host, ".aa/state")
	if err != nil {
		t.Fatalf("ReadRemoteFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ReadRemoteFile returned %q, want %q", got, want)
	}
}

func TestProcessBackend_ReadRemoteFileMissingFileErrors(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx := context.Background()
	host, err := backend.Provision(ctx, SessionID("read-missing-session"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	_, err = backend.ReadRemoteFile(ctx, host, ".aa/result.patch")
	if err == nil {
		t.Fatal("ReadRemoteFile must return an error for a missing file")
	}
}

// ---------------------------------------------------------------------------
// StreamLogs: polls a file under the workspace and writes bytes as they arrive.
// ---------------------------------------------------------------------------

func TestProcessBackend_StreamLogsTailsFile(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id := SessionID("stream-logs-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Pre-create the log file so StreamLogs can open it from the start.
	logDir := filepath.Join(host.Workspace, ".aa")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir .aa: %v", err)
	}
	logPath := filepath.Join(logDir, "agent.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("touch agent.log: %v", err)
	}

	var sink syncBuffer
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- backend.StreamLogs(ctx, host, ".aa/agent.log", &sink)
	}()

	// Writer goroutine: append lines over time.
	writer := make(chan struct{})
	go func() {
		defer close(writer)
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Errorf("open log for append: %v", err)
			return
		}
		defer f.Close()
		for _, line := range []string{"line-one\n", "line-two\n", "line-three\n"} {
			if _, err := f.WriteString(line); err != nil {
				t.Errorf("write log: %v", err)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	<-writer

	// Poll the sink until we see everything, then cancel the stream.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sink.String(), "line-three") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-streamDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("StreamLogs returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not return after ctx cancel")
	}

	got := sink.String()
	for _, want := range []string{"line-one", "line-two", "line-three"} {
		if !strings.Contains(got, want) {
			t.Errorf("StreamLogs output missing %q; got %q", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Teardown: kills process group and removes the workspace directory.
// ---------------------------------------------------------------------------

func TestProcessBackend_TeardownDeletesWorkspace(t *testing.T) {
	t.Setenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND", "1")

	root := t.TempDir()
	backend := NewProcessBackend(root)

	backend.StartAgentCommand = func(name string, args ...string) *exec.Cmd {
		// A short-lived sleep process so there's something to kill.
		return exec.Command("sleep", "30")
	}

	ctx := context.Background()
	id := SessionID("teardown-session")
	host, err := backend.Provision(ctx, id)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := backend.RunContainer(ctx, host, ContainerSpec{
		AgentRun:  "sleep 30",
		SessionID: id,
	}); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}

	if err := backend.Teardown(ctx, host); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	if _, err := os.Stat(host.Workspace); !os.IsNotExist(err) {
		t.Errorf("workspace %q must not exist after Teardown; stat err = %v", host.Workspace, err)
	}

	// Idempotent: second Teardown returns nil even though the directory is
	// already gone and the process is already dead.
	if err := backend.Teardown(ctx, host); err != nil {
		t.Errorf("second Teardown must be a no-op; got: %v", err)
	}
}

func TestProcessBackend_TeardownAfterProvisionOnly(t *testing.T) {
	root := t.TempDir()
	backend := NewProcessBackend(root)

	ctx := context.Background()
	host, err := backend.Provision(ctx, SessionID("teardown-early-session"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// No RunContainer — there is no child process to kill. Teardown should
	// still remove the workspace without erroring on the absent process.
	if err := backend.Teardown(ctx, host); err != nil {
		t.Fatalf("Teardown after Provision-only must not error: %v", err)
	}
	if _, err := os.Stat(host.Workspace); !os.IsNotExist(err) {
		t.Errorf("workspace %q must be removed", host.Workspace)
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// envMap turns an exec.Cmd-style []string env into a map for assertion.
// Unknown keys are ignored; duplicate keys resolve to the last value, matching
// the usual shell precedence.
func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// syncBuffer is a bytes.Buffer with a mutex so the StreamLogs writer can be
// read concurrently from the test goroutine without a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Compile-time assertions.
var (
	_ Backend   = (*ProcessBackend)(nil)
	_ io.Writer = (*syncBuffer)(nil)
	// Silence unused-import complaints for syscall on platforms where the
	// SysProcAttr fields we touch are Unix-only. The assertion is a no-op
	// at runtime.
	_ = syscall.SIGTERM
)
