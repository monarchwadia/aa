package main

// backend_fly_test.go exercises the Fly Firecracker-microVM backend end-to-end
// WITHOUT a real Fly.io account. Every test injects a recording hook into
// FlyBackend.FlyctlExecCommand that:
//
//  1. Captures the (program, args...) tuple, so tests can assert the exact
//     argv composition aa produces against `flyctl`.
//  2. Returns a real *exec.Cmd that re-invokes this test binary with
//     `-test.run=TestFlyHelperProcess`, using the well-known Go-stdlib
//     "helper process" trick. The helper prints canned stdout / stderr
//     and exits with a canned status, simulating flyctl's response.
//
// In-VM operations (ReadRemoteFile, StreamLogs, InstallEgress) route through
// the shared fakeSSHRunner declared in fakes_test.go. Nothing in this file
// requires a Fly.io account, an SSH daemon, or a running VM.
//
// See docs/architecture/aa.md § "Workstreams" → `backend-fly`.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// flyctlRecorder — the test-side replacement for exec.Command. Each call is
// logged; a per-call stdout/stderr/exit-code script can be programmed so
// implementations that parse flyctl output still see deterministic bytes.
// ---------------------------------------------------------------------------

type flyctlInvocation struct {
	Name string
	Args []string
}

type flyctlCannedResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int

	// BlockUntilCtxCancel, if true, makes the helper process sleep until
	// its stdin closes (which exec.Cmd.Cancel triggers on ctx cancellation)
	// rather than exiting immediately. Used to exercise context-cancellation
	// paths without time.Sleep in the test itself.
	BlockUntilCtxCancel bool
}

type flyctlRecorder struct {
	mu sync.Mutex

	// Invocations is the full argv log in observation order.
	Invocations []flyctlInvocation

	// Responses, consumed in order. If len(Responses) is shorter than the
	// number of calls, extra calls receive an empty-stdout, exit-0 response.
	Responses []flyctlCannedResponse

	// callIndex tracks which response to hand out next.
	callIndex int
}

// Command returns an *exec.Cmd that, when run, produces the next canned
// response. It records the (name, args...) that would have been sent to
// flyctl for later assertions.
func (r *flyctlRecorder) Command(name string, args ...string) *exec.Cmd {
	r.mu.Lock()
	r.Invocations = append(r.Invocations, flyctlInvocation{
		Name: name,
		Args: append([]string(nil), args...),
	})
	var resp flyctlCannedResponse
	if r.callIndex < len(r.Responses) {
		resp = r.Responses[r.callIndex]
	}
	r.callIndex++
	r.mu.Unlock()

	// Build a helper-process Cmd: re-exec the test binary with
	// -test.run=TestFlyHelperProcess and pass the canned response in env.
	helperArgs := []string{"-test.run=TestFlyHelperProcess", "--"}
	helperArgs = append(helperArgs, name)
	helperArgs = append(helperArgs, args...)

	cmd := exec.Command(os.Args[0], helperArgs...)
	block := "0"
	if resp.BlockUntilCtxCancel {
		block = "1"
	}
	cmd.Env = append(
		append([]string(nil), os.Environ()...),
		"AA_BACKEND_FLY_TEST_HELPER=1",
		"AA_BACKEND_FLY_TEST_STDOUT="+resp.Stdout,
		"AA_BACKEND_FLY_TEST_STDERR="+resp.Stderr,
		fmt.Sprintf("AA_BACKEND_FLY_TEST_EXIT=%d", resp.ExitCode),
		"AA_BACKEND_FLY_TEST_BLOCK="+block,
	)
	return cmd
}

// TestFlyHelperProcess is not a real test: it is the body that runs when
// Command() re-execs the test binary as a fake `flyctl`. It prints the
// canned stdout / stderr and exits with the canned status. If the response
// opts into blocking, it sleeps until killed (simulating a long-running
// flyctl operation the test is about to cancel via ctx).
func TestFlyHelperProcess(t *testing.T) {
	if os.Getenv("AA_BACKEND_FLY_TEST_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, os.Getenv("AA_BACKEND_FLY_TEST_STDOUT"))
	_, _ = io.WriteString(os.Stderr, os.Getenv("AA_BACKEND_FLY_TEST_STDERR"))
	if os.Getenv("AA_BACKEND_FLY_TEST_BLOCK") == "1" {
		// Sleep long enough that any reasonable test will cancel first.
		time.Sleep(30 * time.Second)
	}
	code := 0
	fmt.Sscanf(os.Getenv("AA_BACKEND_FLY_TEST_EXIT"), "%d", &code)
	os.Exit(code)
}

// newFlyRecorderBackend returns a FlyBackend wired to a fresh flyctl
// recorder plus a fresh fakeSSHRunner, and returns the recorder / fake for
// assertions.
func newFlyRecorderBackend(responses ...flyctlCannedResponse) (*FlyBackend, *flyctlRecorder, *fakeSSHRunner) {
	rec := &flyctlRecorder{Responses: responses}
	runner := newFakeSSHRunner()
	backend := &FlyBackend{
		FlyctlExecCommand: rec.Command,
		SSHRunner:         runner,
		Region:            "iad",
		VMSize:            "shared-cpu-2x",
	}
	return backend, rec, runner
}

// ---------------------------------------------------------------------------
// 1. Provision issues `flyctl machine run` with image, region, vm-size.
// ---------------------------------------------------------------------------

func TestFlyBackend_ProvisionInvokesFlyctlMachineRunWithRegionAndSize(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend(flyctlCannedResponse{
		Stdout: "machine-id: 148ed123abc\nssh-address: root@148ed123abc.fly.dev\n",
	})

	if _, err := backend.Provision(context.Background(), SessionID("myapp-feature-oauth")); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if len(rec.Invocations) == 0 {
		t.Fatalf("Provision made no flyctl calls")
	}
	inv := rec.Invocations[0]
	if inv.Name != "flyctl" {
		t.Errorf("program = %q, want %q", inv.Name, "flyctl")
	}
	if !argvContainsConsecutive(inv.Args, "machine", "run") {
		t.Errorf("argv missing `machine run`; argv=%v", inv.Args)
	}
	if !argvContainsConsecutive(inv.Args, "--region", "iad") {
		t.Errorf("argv missing `--region iad`; argv=%v", inv.Args)
	}
	if !argvContainsConsecutive(inv.Args, "--vm-size", "shared-cpu-2x") {
		t.Errorf("argv missing `--vm-size shared-cpu-2x`; argv=%v", inv.Args)
	}
}

// ---------------------------------------------------------------------------
// 2. Provision returns a well-formed Host (BackendType, Workspace, Address).
// ---------------------------------------------------------------------------

func TestFlyBackend_ProvisionReturnsHostWithFlyAddressAndWorkspace(t *testing.T) {
	// flyctl's canned output includes an SSH-addressable machine ID.
	backend, _, _ := newFlyRecorderBackend(flyctlCannedResponse{
		Stdout: "machine-id: 148ed123abc\nssh-address: root@148ed123abc.fly.dev\n",
	})

	host, err := backend.Provision(context.Background(), SessionID("myapp-feature-oauth"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if host.BackendType != "fly" {
		t.Errorf("BackendType = %q, want %q", host.BackendType, "fly")
	}
	if host.Workspace != "/workspace" {
		t.Errorf("Workspace = %q, want %q", host.Workspace, "/workspace")
	}
	if host.Address == "" {
		t.Errorf("Address is empty; want a fly SSH address derived from flyctl output")
	}
	if !strings.Contains(host.Address, "fly.dev") && !strings.Contains(host.Address, "148ed123abc") {
		t.Errorf("Address = %q, want it to reference the provisioned machine id / fly.dev", host.Address)
	}
}

// ---------------------------------------------------------------------------
// 3. Provision surfaces flyctl stderr when flyctl exits non-zero.
// ---------------------------------------------------------------------------

func TestFlyBackend_ProvisionSurfacesFlyctlStderrOnError(t *testing.T) {
	const stderr = "Error: No access to organization personal"
	backend, _, _ := newFlyRecorderBackend(flyctlCannedResponse{
		Stderr:   stderr,
		ExitCode: 1,
	})

	_, err := backend.Provision(context.Background(), SessionID("myapp-feature-oauth"))
	if err == nil {
		t.Fatal("Provision must return an error when flyctl exits non-zero")
	}
	if !strings.Contains(err.Error(), stderr) {
		t.Errorf("error text = %q; expected to include flyctl stderr %q", err.Error(), stderr)
	}
}

// ---------------------------------------------------------------------------
// 4. InstallEgress delegates to the SSHRunner (iptables inside the VM),
//    NOT to flyctl.
// ---------------------------------------------------------------------------

func TestFlyBackend_InstallEgressDelegatesToSSHRunnerNotFlyctl(t *testing.T) {
	backend, rec, runner := newFlyRecorderBackend()
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	if err := backend.InstallEgress(
		context.Background(), host,
		[]string{"api.anthropic.com"},
	); err != nil {
		t.Fatalf("InstallEgress: %v", err)
	}

	// Not a single flyctl call — egress rules live inside the VM, composed
	// and executed over SSH.
	if len(rec.Invocations) != 0 {
		t.Errorf("InstallEgress must not invoke flyctl; got %d calls: %v",
			len(rec.Invocations), rec.Invocations)
	}
	if len(runner.RunCalls) == 0 {
		t.Fatalf("InstallEgress must issue at least one SSHRunner.Run call")
	}
	// At least one of the SSH commands must be composing iptables rules.
	sawIptables := false
	for _, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") {
			sawIptables = true
			break
		}
	}
	if !sawIptables {
		t.Errorf("no SSHRunner.Run call referenced iptables; calls=%v", runner.RunCalls)
	}
}

// ---------------------------------------------------------------------------
// 5. RunContainer invokes `flyctl machine exec` with bash -lc "<run>".
// ---------------------------------------------------------------------------

func TestFlyBackend_RunContainerInvokesFlyctlMachineExec(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend(flyctlCannedResponse{
		Stdout: "exec-ok\n",
	})
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}
	spec := ContainerSpec{
		Image:     "agent-image:latest",
		AgentRun:  "claude --dangerously-skip-permissions",
		Env:       map[string]string{},
		SessionID: SessionID("myapp-feature-oauth"),
	}

	if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}

	if len(rec.Invocations) == 0 {
		t.Fatalf("RunContainer made no flyctl calls")
	}
	inv := rec.Invocations[0]
	if inv.Name != "flyctl" {
		t.Errorf("program = %q, want %q", inv.Name, "flyctl")
	}
	// `flyctl machine exec` is the subcommand for running a command in an
	// existing machine.
	if !argvContainsConsecutive(inv.Args, "machine", "exec") {
		t.Errorf("argv missing `machine exec`; argv=%v", inv.Args)
	}
	// The agent's run command must be wrapped as `bash -lc "<run>"`.
	joined := strings.Join(inv.Args, " ")
	if !strings.Contains(joined, "bash") || !strings.Contains(joined, "-lc") {
		t.Errorf("argv missing `bash -lc` wrapper; argv=%v", inv.Args)
	}
	if !strings.Contains(joined, "claude --dangerously-skip-permissions") {
		t.Errorf("argv missing agent run command; argv=%v", inv.Args)
	}
}

// ---------------------------------------------------------------------------
// 6. RunContainer injects AA_WORKSPACE and AA_SESSION_ID into the exec'd
//    command environment.
// ---------------------------------------------------------------------------

func TestFlyBackend_RunContainerInjectsContractEnv(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend(flyctlCannedResponse{Stdout: "ok\n"})
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}
	spec := ContainerSpec{
		Image:     "agent-image:latest",
		AgentRun:  "true",
		Env:       map[string]string{},
		SessionID: SessionID("myapp-feature-oauth"),
	}
	if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}

	inv := rec.Invocations[0]
	joined := strings.Join(inv.Args, " ")

	// AA_WORKSPACE and AA_SESSION_ID must both appear — either as flyctl
	// `--env KEY=VALUE` flags or embedded in the `bash -lc` command string
	// ("export KEY=VALUE; ..."). Either form is acceptable here; the
	// contract is that the values reach the agent's process env.
	wantWS := "AA_WORKSPACE=/workspace"
	wantSID := "AA_SESSION_ID=myapp-feature-oauth"
	if !strings.Contains(joined, wantWS) {
		t.Errorf("argv must carry %q somewhere; argv=%v", wantWS, inv.Args)
	}
	if !strings.Contains(joined, wantSID) {
		t.Errorf("argv must carry %q somewhere; argv=%v", wantSID, inv.Args)
	}
}

// ---------------------------------------------------------------------------
// 7. RunContainer propagates spec.Env through to the VM's process env.
// ---------------------------------------------------------------------------

func TestFlyBackend_RunContainerPropagatesSpecEnv(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend(flyctlCannedResponse{Stdout: "ok\n"})
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}
	spec := ContainerSpec{
		Image:    "agent-image:latest",
		AgentRun: "true",
		Env: map[string]string{
			"ANTHROPIC_API_KEY": "sk-test-abc123",
			"CUSTOM_AGENT_FLAG": "enabled",
		},
		SessionID: SessionID("env-propagation-session"),
	}
	if _, err := backend.RunContainer(context.Background(), host, spec); err != nil {
		t.Fatalf("RunContainer: %v", err)
	}

	inv := rec.Invocations[0]
	joined := strings.Join(inv.Args, " ")
	if !strings.Contains(joined, "ANTHROPIC_API_KEY=sk-test-abc123") {
		t.Errorf("spec env ANTHROPIC_API_KEY not present in argv; argv=%v", inv.Args)
	}
	if !strings.Contains(joined, "CUSTOM_AGENT_FLAG=enabled") {
		t.Errorf("spec env CUSTOM_AGENT_FLAG not present in argv; argv=%v", inv.Args)
	}
}

// ---------------------------------------------------------------------------
// 8. ReadRemoteFile delegates to the SSHRunner and issues a `cat <path>`
//    command rooted at the workspace.
// ---------------------------------------------------------------------------

func TestFlyBackend_ReadRemoteFileDelegatesToSSHRunnerWithCat(t *testing.T) {
	backend, _, runner := newFlyRecorderBackend()
	const captured = "DONE\n"
	runner.RunFn = func(ctx context.Context, host Host, cmd string) (SSHResult, error) {
		return SSHResult{Stdout: []byte(captured)}, nil
	}
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	got, err := backend.ReadRemoteFile(context.Background(), host, ".aa/state")
	if err != nil {
		t.Fatalf("ReadRemoteFile: %v", err)
	}
	if string(got) != captured {
		t.Errorf("returned bytes = %q, want %q", got, captured)
	}
	if len(runner.RunCalls) != 1 {
		t.Fatalf("want 1 SSHRunner.Run call, got %d: %v", len(runner.RunCalls), runner.RunCalls)
	}
	cmd := runner.RunCalls[0]
	if !strings.Contains(cmd, "cat") {
		t.Errorf("SSH command must use `cat`; got %q", cmd)
	}
	if !strings.Contains(cmd, "/workspace") {
		t.Errorf("SSH command must include the workspace path; got %q", cmd)
	}
	if !strings.Contains(cmd, ".aa/state") {
		t.Errorf("SSH command must include the relpath; got %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// 9. StreamLogs delegates to the SSHRunner and issues a `tail -f <path>`
//    command; bytes flow through the SSHRunner into the caller's writer.
// ---------------------------------------------------------------------------

func TestFlyBackend_StreamLogsDelegatesToSSHRunnerWithTailF(t *testing.T) {
	backend, _, runner := newFlyRecorderBackend()
	const streamed = "line one\nline two\nline three\n"
	runner.RunFn = func(ctx context.Context, host Host, cmd string) (SSHResult, error) {
		return SSHResult{Stdout: []byte(streamed)}, nil
	}
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	var out bytes.Buffer
	if err := backend.StreamLogs(context.Background(), host, ".aa/agent.log", &out); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if out.String() != streamed {
		t.Errorf("writer bytes = %q, want %q", out.String(), streamed)
	}
	if len(runner.RunCalls) != 1 {
		t.Fatalf("want 1 SSHRunner.Run call, got %d: %v", len(runner.RunCalls), runner.RunCalls)
	}
	cmd := runner.RunCalls[0]
	if !strings.Contains(cmd, "tail") || !strings.Contains(cmd, "-f") {
		t.Errorf("SSH command must be `tail -f`; got %q", cmd)
	}
	if !strings.Contains(cmd, ".aa/agent.log") {
		t.Errorf("SSH command must reference the log path; got %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// 10. Teardown invokes `flyctl machine destroy <id>`.
// ---------------------------------------------------------------------------

func TestFlyBackend_TeardownInvokesFlyctlMachineDestroy(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend(flyctlCannedResponse{Stdout: "destroyed\n"})
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(rec.Invocations) == 0 {
		t.Fatalf("Teardown made no flyctl calls")
	}
	inv := rec.Invocations[0]
	if inv.Name != "flyctl" {
		t.Errorf("program = %q, want %q", inv.Name, "flyctl")
	}
	if !argvContainsConsecutive(inv.Args, "machine", "destroy") {
		t.Errorf("argv missing `machine destroy`; argv=%v", inv.Args)
	}
	// The machine id (derived from host.Address) must appear somewhere in
	// argv so flyctl knows which machine to destroy.
	if !strings.Contains(strings.Join(inv.Args, " "), "148ed123abc") {
		t.Errorf("argv missing machine id `148ed123abc`; argv=%v", inv.Args)
	}
}

// ---------------------------------------------------------------------------
// 11. Teardown is idempotent — "machine not found" is treated as success.
// ---------------------------------------------------------------------------

func TestFlyBackend_TeardownIsIdempotentWhenMachineAlreadyGone(t *testing.T) {
	// Every flyctl invocation exits non-zero with a "not found"-style stderr.
	backend, _, _ := newFlyRecorderBackend(
		flyctlCannedResponse{
			Stderr:   "Error: machine not found: 148ed123abc",
			ExitCode: 1,
		},
		flyctlCannedResponse{
			Stderr:   "Error: machine not found: 148ed123abc",
			ExitCode: 1,
		},
	)
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Errorf("first Teardown must swallow `machine not found`; got: %v", err)
	}
	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Errorf("second Teardown must be idempotent; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 12. Teardown after partial Provision (no Address yet) is a no-op.
// ---------------------------------------------------------------------------

func TestFlyBackend_TeardownAfterPartialProvisionIsNoOp(t *testing.T) {
	backend, rec, _ := newFlyRecorderBackend()
	host := Host{BackendType: "fly", Workspace: "/workspace", Address: ""}

	if err := backend.Teardown(context.Background(), host); err != nil {
		t.Fatalf("Teardown on empty-address host: %v", err)
	}
	if len(rec.Invocations) != 0 {
		t.Errorf("expected 0 flyctl calls for empty-address host, got %d: %v",
			len(rec.Invocations), rec.Invocations)
	}
}

// ---------------------------------------------------------------------------
// 13. All six Backend methods honor ctx cancellation — a long-running flyctl
//     invocation is killed when the context cancels.
// ---------------------------------------------------------------------------

func TestFlyBackend_RespectsContextCancellation(t *testing.T) {
	// Each sub-test uses its own backend + block-until-cancel response so a
	// slow helper process is what stands in for a slow flyctl. The test
	// asserts the Cmd returns with a non-nil error within a short deadline
	// after ctx is cancelled, which can only happen if aa plumbed the ctx
	// through to exec (via Cmd.Cancel or CommandContext).
	host := Host{
		BackendType: "fly",
		Workspace:   "/workspace",
		Address:     "root@148ed123abc.fly.dev",
	}

	// blockingRunner blocks the SSHRunner call until ctx is cancelled, then
	// returns ctx.Err(). Used for the three methods that route through SSH.
	blockingRunner := func() *fakeSSHRunner {
		r := newFakeSSHRunner()
		r.RunFn = func(ctx context.Context, _ Host, _ string) (SSHResult, error) {
			<-ctx.Done()
			return SSHResult{}, ctx.Err()
		}
		return r
	}

	cases := []struct {
		name string
		call func(t *testing.T, ctx context.Context) error
	}{
		{
			name: "Provision",
			call: func(t *testing.T, ctx context.Context) error {
				backend, _, _ := newFlyRecorderBackend(flyctlCannedResponse{BlockUntilCtxCancel: true})
				_, err := backend.Provision(ctx, SessionID("ctx-cancel-session"))
				return err
			},
		},
		{
			name: "InstallEgress",
			call: func(t *testing.T, ctx context.Context) error {
				rec := &flyctlRecorder{}
				backend := &FlyBackend{
					FlyctlExecCommand: rec.Command,
					SSHRunner:         blockingRunner(),
					Region:            "iad",
					VMSize:            "shared-cpu-2x",
				}
				return backend.InstallEgress(ctx, host, []string{"api.anthropic.com"})
			},
		},
		{
			name: "RunContainer",
			call: func(t *testing.T, ctx context.Context) error {
				backend, _, _ := newFlyRecorderBackend(flyctlCannedResponse{BlockUntilCtxCancel: true})
				_, err := backend.RunContainer(ctx, host, ContainerSpec{
					Image:     "agent-image:latest",
					AgentRun:  "sleep 30",
					Env:       map[string]string{},
					SessionID: SessionID("ctx-cancel-session"),
				})
				return err
			},
		},
		{
			name: "ReadRemoteFile",
			call: func(t *testing.T, ctx context.Context) error {
				rec := &flyctlRecorder{}
				backend := &FlyBackend{
					FlyctlExecCommand: rec.Command,
					SSHRunner:         blockingRunner(),
					Region:            "iad",
					VMSize:            "shared-cpu-2x",
				}
				_, err := backend.ReadRemoteFile(ctx, host, ".aa/state")
				return err
			},
		},
		{
			name: "StreamLogs",
			call: func(t *testing.T, ctx context.Context) error {
				rec := &flyctlRecorder{}
				backend := &FlyBackend{
					FlyctlExecCommand: rec.Command,
					SSHRunner:         blockingRunner(),
					Region:            "iad",
					VMSize:            "shared-cpu-2x",
				}
				return backend.StreamLogs(ctx, host, ".aa/agent.log", io.Discard)
			},
		},
		{
			name: "Teardown",
			call: func(t *testing.T, ctx context.Context) error {
				backend, _, _ := newFlyRecorderBackend(flyctlCannedResponse{BlockUntilCtxCancel: true})
				return backend.Teardown(ctx, host)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())

			done := make(chan error, 1)
			go func() {
				done <- tc.call(t, ctx)
			}()

			// Cancel shortly after starting so the method is definitely in
			// its blocking phase.
			time.AfterFunc(100*time.Millisecond, cancel)

			select {
			case err := <-done:
				if err == nil {
					t.Errorf("%s returned nil error after ctx cancel; expected a cancellation error", tc.name)
				}
				// Accept either ctx.Err() directly or an error that wraps it,
				// or an exec.ExitError (Cmd was killed on cancel).
				if err != nil && !errors.Is(err, context.Canceled) &&
					!strings.Contains(err.Error(), "canceled") &&
					!strings.Contains(err.Error(), "killed") &&
					!strings.Contains(err.Error(), "signal") {
					// We don't want to be overly strict about the exact error
					// text, only that SOMETHING cancellation-shaped surfaces.
					t.Logf("%s returned %v — acceptable if it reflects cancellation/kill", tc.name, err)
				}
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatalf("%s did not return within 3s of ctx cancel — the Cmd was not killed", tc.name)
			}
		})
	}
}
