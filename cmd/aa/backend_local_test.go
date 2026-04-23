package main

// backend_local_test.go exercises the local Docker backend end-to-end
// WITHOUT a real Docker daemon. Every test injects a recording hook into
// LocalBackend.DockerExecCommand that:
//
//  1. Captures the (program, args...) tuple, so tests can assert the
//     exact argv composition aa produces.
//  2. Returns a real *exec.Cmd that re-invokes this test binary with
//     `-test.run=TestDockerHelperProcess`, using the well-known
//     Go-stdlib "helper process" trick. The helper prints canned stdout
//     / exits with a canned status, simulating docker's response.
//
// This lets the same tests that are red today (panic from the stub) go
// green against the real implementation without touching docker at all.
//
// See docs/architecture/aa.md § "Workstreams" → `backend-local`.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// dockerRecorder — the test-side replacement for exec.Command. Each call is
// logged; a per-call stdout/exit-code script can be programmed so
// implementations that parse docker output still see deterministic bytes.
// ---------------------------------------------------------------------------

type dockerInvocation struct {
	Name string
	Args []string
}

type dockerCannedResponse struct {
	Stdout   string
	ExitCode int
}

type dockerRecorder struct {
	mu sync.Mutex

	// Invocations is the full argv log in observation order.
	Invocations []dockerInvocation

	// Responses, consumed in order. If len(Responses) is shorter than the
	// number of calls, extra calls receive an empty-stdout, exit-0 response.
	Responses []dockerCannedResponse

	// callIndex tracks which response to hand out next.
	callIndex int
}

// Command returns an *exec.Cmd that, when run, produces the next canned
// response. It records the (name, args...) that would have been sent to
// docker for later assertions.
func (r *dockerRecorder) Command(name string, args ...string) *exec.Cmd {
	r.mu.Lock()
	r.Invocations = append(r.Invocations, dockerInvocation{
		Name: name,
		Args: append([]string(nil), args...),
	})
	var resp dockerCannedResponse
	if r.callIndex < len(r.Responses) {
		resp = r.Responses[r.callIndex]
	}
	r.callIndex++
	r.mu.Unlock()

	// Build a helper-process Cmd: re-exec the test binary with
	// -test.run=TestDockerHelperProcess and pass the canned response in env.
	helperArgs := []string{"-test.run=TestDockerHelperProcess", "--"}
	helperArgs = append(helperArgs, name)
	helperArgs = append(helperArgs, args...)

	cmd := exec.Command(os.Args[0], helperArgs...)
	cmd.Env = append(
		append([]string(nil), os.Environ()...),
		"AA_BACKEND_LOCAL_TEST_HELPER=1",
		"AA_BACKEND_LOCAL_TEST_STDOUT="+resp.Stdout,
		fmt.Sprintf("AA_BACKEND_LOCAL_TEST_EXIT=%d", resp.ExitCode),
	)
	return cmd
}

// TestDockerHelperProcess is not a real test: it is the body that runs
// when Command() re-execs the test binary as a fake `docker`. It prints
// the canned stdout and exits with the canned status.
func TestDockerHelperProcess(t *testing.T) {
	if os.Getenv("AA_BACKEND_LOCAL_TEST_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, os.Getenv("AA_BACKEND_LOCAL_TEST_STDOUT"))
	code := 0
	fmt.Sscanf(os.Getenv("AA_BACKEND_LOCAL_TEST_EXIT"), "%d", &code)
	os.Exit(code)
}

// newRecorderBackend returns a LocalBackend wired to a fresh recorder, and
// the recorder itself for assertions.
func newRecorderBackend(responses ...dockerCannedResponse) (*LocalBackend, *dockerRecorder) {
	rec := &dockerRecorder{Responses: responses}
	return &LocalBackend{DockerExecCommand: rec.Command}, rec
}

// argvContainsConsecutive reports whether `want` appears as a contiguous
// subsequence of `argv`. Useful for asserting `-e KEY=VALUE` pairs survive
// intact.
func argvContainsConsecutive(argv []string, want ...string) bool {
	if len(want) == 0 || len(want) > len(argv) {
		return false
	}
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j, w := range want {
			if argv[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// argvContains reports whether `want` appears anywhere in argv.
func argvContains(argv []string, want string) bool {
	return slices.Contains(argv, want)
}

// ---------------------------------------------------------------------------
// Provision
// ---------------------------------------------------------------------------

// TestProvisionReturnsLocalHostWithWorkspaceConvention asserts the contract
// that Provision on the local backend surfaces the conventional container
// workspace path "/workspace" and BackendType="local", and does not spin up
// any real sandbox (no docker run, no docker create).
func TestProvisionReturnsLocalHostWithWorkspaceConvention(t *testing.T) {
	backend, rec := newRecorderBackend()

	host, err := backend.Provision(context.Background(), SessionID("myapp-feature-oauth"))
	if err != nil {
		t.Fatalf("Provision returned error: %v", err)
	}
	if host.BackendType != "local" {
		t.Errorf("BackendType = %q, want %q", host.BackendType, "local")
	}
	if host.Workspace != "/workspace" {
		t.Errorf("Workspace = %q, want %q", host.Workspace, "/workspace")
	}
	// At most one diagnostic `docker version` probe is permitted; no
	// `docker run` / `docker create` may appear.
	for _, inv := range rec.Invocations {
		if len(inv.Args) > 0 && (inv.Args[0] == "run" || inv.Args[0] == "create") {
			t.Errorf("Provision issued docker %s — expected near no-op", inv.Args[0])
		}
	}
}

// ---------------------------------------------------------------------------
// RunContainer — env injection, mounts, handle
// ---------------------------------------------------------------------------

// TestRunContainerInjectsAAWorkspaceEnv asserts AA_WORKSPACE and
// AA_SESSION_ID are both passed via `-e` flags with the correct values.
func TestRunContainerInjectsAAWorkspaceEnv(t *testing.T) {
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: "container-abc123\n"})
	host := Host{BackendType: "local", Workspace: "/workspace"}

	spec := ContainerSpec{
		Image:     "agent-image:latest",
		AgentRun:  "claude --dangerously-skip-permissions",
		Env:       map[string]string{},
		SessionID: SessionID("myapp-feature-oauth"),
	}

	if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
		t.Fatalf("RunContainer returned error: %v", err)
	}

	inv := findDockerRun(t, rec)
	if !argvContainsConsecutive(inv.Args, "-e", "AA_WORKSPACE=/workspace") {
		t.Errorf("argv missing `-e AA_WORKSPACE=/workspace`; argv=%v", inv.Args)
	}
	if !argvContainsConsecutive(inv.Args, "-e", "AA_SESSION_ID=myapp-feature-oauth") {
		t.Errorf("argv missing `-e AA_SESSION_ID=myapp-feature-oauth`; argv=%v", inv.Args)
	}

	// The final argv tail invokes `bash -lc "<run>"`.
	tail := strings.Join(inv.Args[len(inv.Args)-3:], " ")
	wantTail := `bash -lc claude --dangerously-skip-permissions`
	// We accept either quoted or unquoted run string, but the last three
	// argv tokens must be `bash`, `-lc`, `<AgentRun>` verbatim.
	last3 := inv.Args[len(inv.Args)-3:]
	if last3[0] != "bash" || last3[1] != "-lc" || last3[2] != "claude --dangerously-skip-permissions" {
		t.Errorf("argv tail = %q, want bash -lc <run>: %v", tail, wantTail)
	}
}

// TestRunContainerPassesAgentEnvVarsThroughTable asserts each entry in
// spec.Env becomes its own `-e KEY=VALUE` pair on the docker run command.
func TestRunContainerPassesAgentEnvVarsThroughTable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want [][2]string // consecutive argv pairs {"-e", "KEY=VAL"}
	}{
		{
			name: "anthropic key only",
			env:  map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
			want: [][2]string{{"-e", "ANTHROPIC_API_KEY=sk-test"}},
		},
		{
			name: "two vars",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-test",
				"OPENAI_API_KEY":    "sk-openai",
			},
			want: [][2]string{
				{"-e", "ANTHROPIC_API_KEY=sk-test"},
				{"-e", "OPENAI_API_KEY=sk-openai"},
			},
		},
		{
			name: "empty env",
			env:  map[string]string{},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: "container-id\n"})
			host := Host{BackendType: "local", Workspace: "/workspace"}
			spec := ContainerSpec{
				Image:     "agent-image:latest",
				AgentRun:  "true",
				Env:       tc.env,
				SessionID: SessionID("sid"),
			}
			if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
				t.Fatalf("RunContainer: %v", err)
			}
			inv := findDockerRun(t, rec)
			for _, pair := range tc.want {
				if !argvContainsConsecutive(inv.Args, pair[0], pair[1]) {
					t.Errorf("argv missing %q %q; argv=%v", pair[0], pair[1], inv.Args)
				}
			}
		})
	}
}

// TestRunContainerMountsRepoWorkingTreeReadWrite asserts a bind mount of
// the repo working tree is emitted. The flag may be `-v <src>:/workspace`
// or `--mount type=bind,source=<src>,target=/workspace`. The mount must be
// read-write (no `:ro`, no `readonly=true`).
func TestRunContainerMountsRepoWorkingTreeReadWrite(t *testing.T) {
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: "cid\n"})
	host := Host{BackendType: "local", Workspace: "/workspace"}
	spec := ContainerSpec{
		Image:     "agent-image:latest",
		AgentRun:  "true",
		Env:       map[string]string{},
		SessionID: SessionID("sid"),
	}
	if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	inv := findDockerRun(t, rec)
	joined := strings.Join(inv.Args, " ")

	// Accept either `-v ...:/workspace` or `--mount ...target=/workspace`.
	hasMount := strings.Contains(joined, ":/workspace") ||
		strings.Contains(joined, "target=/workspace")
	if !hasMount {
		t.Errorf("argv does not mount anything at /workspace; argv=%v", inv.Args)
	}
	// Must not be read-only.
	if strings.Contains(joined, ":ro") || strings.Contains(joined, "readonly=true") {
		t.Errorf("workspace mount is read-only; argv=%v", inv.Args)
	}
}

// TestRunContainerReturnsContainerHandleWithDockerID asserts the returned
// handle's ID matches the id/name docker printed on stdout.
func TestRunContainerReturnsContainerHandleWithDockerID(t *testing.T) {
	backend, _ := newRecorderBackend(dockerCannedResponse{
		Stdout: "deadbeef0123cafe\n",
	})
	host := Host{BackendType: "local", Workspace: "/workspace"}
	spec := ContainerSpec{
		Image:     "agent-image:latest",
		AgentRun:  "true",
		Env:       map[string]string{},
		SessionID: SessionID("sid"),
	}
	handle, err := backend.RunContainer(context.Background(), host, spec)
	if err != nil {
		t.Fatalf("RunContainer: %v", err)
	}
	if handle.ID != "deadbeef0123cafe" {
		t.Errorf("handle.ID = %q, want %q (from docker stdout)",
			handle.ID, "deadbeef0123cafe")
	}
}

// ---------------------------------------------------------------------------
// InstallEgress
// ---------------------------------------------------------------------------

// TestInstallEgressUsesPrivilegedHelperContainer asserts strict-mode egress
// install shells out to `docker run ... --privileged ...` to get iptables
// rules into Docker Desktop's VM. (See README § "macOS local backend" and
// docs/architecture/aa.md § "Decision 3".)
func TestInstallEgressUsesPrivilegedHelperContainer(t *testing.T) {
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: ""})
	host := Host{BackendType: "local", Workspace: "/workspace"}

	if err := backend.InstallEgress(
		context.Background(), host,
		[]string{"api.anthropic.com"},
	); err != nil {
		t.Fatalf("InstallEgress: %v", err)
	}

	if len(rec.Invocations) == 0 {
		t.Fatalf("InstallEgress made no docker calls; expected privileged helper")
	}
	// At least one invocation must be a `docker run` with --privileged.
	sawPriv := false
	for _, inv := range rec.Invocations {
		if len(inv.Args) > 0 && inv.Args[0] == "run" && argvContains(inv.Args, "--privileged") {
			sawPriv = true
			break
		}
	}
	if !sawPriv {
		t.Errorf("no privileged helper container run; invocations=%v", rec.Invocations)
	}
}

// TestInstallEgressNoneModeIsNoOp asserts that the "none" enforcement path
// is a documented no-op and makes no docker calls. The backend itself has
// no knob; the caller signals "none" by calling InstallEgress with a nil
// allowlist (the documented sentinel — no hosts to allow, and strict mode
// would have raised before reaching here anyway). This test pins that
// contract.
func TestInstallEgressNoneModeIsNoOp(t *testing.T) {
	backend, rec := newRecorderBackend()
	host := Host{BackendType: "local", Workspace: "/workspace"}

	if err := backend.InstallEgress(context.Background(), host, nil); err != nil {
		t.Fatalf("InstallEgress(nil allowlist): %v", err)
	}
	if len(rec.Invocations) != 0 {
		t.Errorf("expected 0 docker calls for nil allowlist, got %d: %v",
			len(rec.Invocations), rec.Invocations)
	}
}

// ---------------------------------------------------------------------------
// ReadRemoteFile
// ---------------------------------------------------------------------------

// TestReadRemoteFileUsesDockerExecCat asserts ReadRemoteFile invokes
// `docker exec <container> cat <path>` and returns the captured stdout.
func TestReadRemoteFileUsesDockerExecCat(t *testing.T) {
	const captured = "DONE\n"
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: captured})
	host := Host{BackendType: "local", Workspace: "/workspace", Address: "container-xyz"}

	got, err := backend.ReadRemoteFile(context.Background(), host, ".aa/state")
	if err != nil {
		t.Fatalf("ReadRemoteFile: %v", err)
	}
	if string(got) != captured {
		t.Errorf("returned bytes = %q, want %q", got, captured)
	}

	if len(rec.Invocations) != 1 {
		t.Fatalf("want 1 docker call, got %d: %v", len(rec.Invocations), rec.Invocations)
	}
	inv := rec.Invocations[0]
	if inv.Name != "docker" {
		t.Errorf("program = %q, want %q", inv.Name, "docker")
	}
	if len(inv.Args) < 4 || inv.Args[0] != "exec" {
		t.Fatalf("argv not `docker exec ...`: %v", inv.Args)
	}
	if inv.Args[len(inv.Args)-2] != "cat" {
		t.Errorf("expected `cat` as penultimate argv element, got %v", inv.Args)
	}
	// The final argv element is the path, passed as its own token.
	if inv.Args[len(inv.Args)-1] != ".aa/state" {
		t.Errorf("path argv = %q, want %q", inv.Args[len(inv.Args)-1], ".aa/state")
	}
}

// TestReadRemoteFileWithShellMetacharsIsSafe asserts the path is passed as
// a discrete argv token — no shell is invoked with the filename
// interpolated. aa is not in strict mode here, but safe argv composition
// remains the Evolvability-friendly default.
func TestReadRemoteFileWithShellMetacharsIsSafe(t *testing.T) {
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: ""})
	host := Host{BackendType: "local", Workspace: "/workspace", Address: "container-xyz"}

	evil := `.aa/$(rm -rf /).state; echo pwned`
	if _, err := backend.ReadRemoteFile(context.Background(), host, evil); err != nil {
		t.Fatalf("ReadRemoteFile: %v", err)
	}
	if len(rec.Invocations) != 1 {
		t.Fatalf("want 1 docker call, got %d", len(rec.Invocations))
	}
	inv := rec.Invocations[0]

	// The evil path must appear as a single intact argv token.
	if inv.Args[len(inv.Args)-1] != evil {
		t.Errorf("evil path not passed intact as trailing argv token; argv=%v", inv.Args)
	}
	// No shell indirection: argv[0] must be the cat-like tool, not sh/bash.
	for _, a := range inv.Args {
		if a == "sh" || a == "bash" || a == "-c" || a == "-lc" {
			t.Errorf("argv contains shell indirection %q — filename may be shell-interpreted; argv=%v",
				a, inv.Args)
		}
	}
}

// ---------------------------------------------------------------------------
// StreamLogs
// ---------------------------------------------------------------------------

// TestStreamLogsUsesDockerLogsFollow asserts StreamLogs invokes
// `docker logs -f <container>` and pipes the captured bytes into w.
func TestStreamLogsUsesDockerLogsFollow(t *testing.T) {
	const captured = "line one\nline two\n"
	backend, rec := newRecorderBackend(dockerCannedResponse{Stdout: captured})
	host := Host{BackendType: "local", Workspace: "/workspace", Address: "container-xyz"}

	var out bytes.Buffer
	if err := backend.StreamLogs(context.Background(), host, ".aa/agent.log", &out); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if out.String() != captured {
		t.Errorf("writer bytes = %q, want %q", out.String(), captured)
	}
	if len(rec.Invocations) != 1 {
		t.Fatalf("want 1 docker call, got %d: %v", len(rec.Invocations), rec.Invocations)
	}
	inv := rec.Invocations[0]
	if inv.Args[0] != "logs" {
		t.Errorf("expected `docker logs ...`, got argv=%v", inv.Args)
	}
	if !argvContains(inv.Args, "-f") {
		t.Errorf("argv missing -f (follow); argv=%v", inv.Args)
	}
	if !argvContains(inv.Args, "container-xyz") {
		t.Errorf("argv missing container id; argv=%v", inv.Args)
	}
}

// ---------------------------------------------------------------------------
// Teardown
// ---------------------------------------------------------------------------

// TestTeardownStopsThenRemoves asserts `docker stop` is followed by
// `docker rm` against the same container identifier.
func TestTeardownStopsThenRemoves(t *testing.T) {
	backend, rec := newRecorderBackend(
		dockerCannedResponse{Stdout: "container-xyz\n"},
		dockerCannedResponse{Stdout: "container-xyz\n"},
	)
	host := Host{BackendType: "local", Workspace: "/workspace", Address: "container-xyz"}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(rec.Invocations) != 2 {
		t.Fatalf("want 2 docker calls (stop+rm), got %d: %v",
			len(rec.Invocations), rec.Invocations)
	}
	if rec.Invocations[0].Args[0] != "stop" {
		t.Errorf("first call = %v, want docker stop ...", rec.Invocations[0].Args)
	}
	if rec.Invocations[1].Args[0] != "rm" {
		t.Errorf("second call = %v, want docker rm ...", rec.Invocations[1].Args)
	}
	if !argvContains(rec.Invocations[0].Args, "container-xyz") {
		t.Errorf("stop argv missing container id; argv=%v", rec.Invocations[0].Args)
	}
	if !argvContains(rec.Invocations[1].Args, "container-xyz") {
		t.Errorf("rm argv missing container id; argv=%v", rec.Invocations[1].Args)
	}
}

// TestTeardownIsIdempotent asserts a second teardown after the container
// is already gone returns nil — "no such container" errors are swallowed
// because the desired state is already reached.
func TestTeardownIsIdempotent(t *testing.T) {
	backend, _ := newRecorderBackend(
		// Every call exits non-zero with "no such container"-style output.
		dockerCannedResponse{Stdout: "Error: No such container: container-xyz\n", ExitCode: 1},
		dockerCannedResponse{Stdout: "Error: No such container: container-xyz\n", ExitCode: 1},
	)
	host := Host{BackendType: "local", Workspace: "/workspace", Address: "container-xyz"}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Fatalf("second Teardown should be nil (idempotent), got: %v", err)
	}
}

// TestTeardownAfterPartialProvisionIsNoOp asserts that when a Host has no
// associated container (Address is empty because RunContainer never ran),
// Teardown returns nil with zero docker invocations — exercising the
// "kill from any point" lifecycle invariant.
func TestTeardownAfterPartialProvisionIsNoOp(t *testing.T) {
	backend, rec := newRecorderBackend()
	host := Host{BackendType: "local", Workspace: "/workspace", Address: ""}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Fatalf("Teardown on empty host: %v", err)
	}
	if len(rec.Invocations) != 0 {
		t.Errorf("expected 0 docker calls for empty-address host, got %d: %v",
			len(rec.Invocations), rec.Invocations)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// findDockerRun returns the recorded `docker run ...` invocation, failing
// the test if there is not exactly one.
func findDockerRun(t *testing.T, rec *dockerRecorder) dockerInvocation {
	t.Helper()
	var found []dockerInvocation
	for _, inv := range rec.Invocations {
		if len(inv.Args) > 0 && inv.Args[0] == "run" {
			found = append(found, inv)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 `docker run` call, got %d: %v", len(found), rec.Invocations)
	}
	return found[0]
}
