// runner_test.go exercises the extbin.Runner implementation. The real
// runner wraps os/exec; we validate against tiny shell builtins (/bin/sh
// -c) so tests need no external fixture binary and run anywhere POSIX-y.
//
// Coverage (per docs/architecture/machine-lifecycle.md Wave 2b):
//   - argv passthrough (exit code comes back unchanged).
//   - stdout / stderr plumbing (bytes reach the supplied writers).
//   - stdin plumbing (bytes reach the child).
//   - env merge (extra entries visible to the child).
//   - binary-not-found surfaces as a setup error, not an exit code.
//   - context cancellation terminates the child.
//
// Every test name reads as a sentence describing the behaviour.
package extbin

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// Run with `sh -c "exit 0"` returns exit code 0 and no error.
func TestRunReturnsZeroExitCodeWhenChildExitsSuccessfully(t *testing.T) {
	r := New()
	code, err := r.Run(context.Background(), Invocation{
		Name: "sh",
		Argv: []string{"-c", "exit 0"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// A non-zero exit status is returned as an int, not wrapped in an error.
func TestRunReturnsNonZeroExitCodeWithoutWrappingInError(t *testing.T) {
	r := New()
	code, err := r.Run(context.Background(), Invocation{
		Name: "sh",
		Argv: []string{"-c", "exit 7"},
	})
	if err != nil {
		t.Fatalf("Run unexpected err: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// stdout bytes written by the child reach the supplied writer.
func TestRunPlumbsChildStdoutToSuppliedWriter(t *testing.T) {
	r := New()
	var out bytes.Buffer
	_, err := r.Run(context.Background(), Invocation{
		Name:   "sh",
		Argv:   []string{"-c", "printf hello"},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello")
	}
}

// stderr bytes written by the child reach the supplied writer (and not stdout's).
func TestRunPlumbsChildStderrToSuppliedWriter(t *testing.T) {
	r := New()
	var stdout, stderr bytes.Buffer
	_, err := r.Run(context.Background(), Invocation{
		Name:   "sh",
		Argv:   []string{"-c", "printf boom 1>&2"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stderr.String() != "boom" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "boom")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout leaked bytes: %q", stdout.String())
	}
}

// stdin bytes from the supplied reader reach the child.
func TestRunPlumbsSuppliedStdinToChild(t *testing.T) {
	r := New()
	var out bytes.Buffer
	_, err := r.Run(context.Background(), Invocation{
		Name:   "sh",
		Argv:   []string{"-c", "cat"},
		Stdin:  strings.NewReader("round-trip"),
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "round-trip" {
		t.Errorf("stdin->stdout = %q, want round-trip", out.String())
	}
}

// Extra env entries are visible to the child process.
func TestRunMergesExtraEnvIntoChildEnvironment(t *testing.T) {
	r := New()
	var out bytes.Buffer
	_, err := r.Run(context.Background(), Invocation{
		Name:   "sh",
		Argv:   []string{"-c", "printf %s \"$AA_TEST_SENTINEL\""},
		Env:    map[string]string{"AA_TEST_SENTINEL": "visible"},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "visible" {
		t.Errorf("child env view = %q, want visible", out.String())
	}
}

// A missing binary name surfaces as a setup error, never a zero exit.
func TestRunReturnsErrorWhenBinaryNotFoundOnPath(t *testing.T) {
	r := New()
	_, err := r.Run(context.Background(), Invocation{
		Name: "nonexistent-binary-aa-test-12345",
		Argv: nil,
	})
	if err == nil {
		t.Fatal("Run returned nil err for missing binary")
	}
}

// A cancelled context terminates a long-running child without hanging.
func TestRunHonoursContextCancellationAndTerminatesChild(t *testing.T) {
	r := New()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = r.Run(ctx, Invocation{
		Name: "sh",
		Argv: []string{"-c", "sleep 30"},
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v, want <5s (ctx timeout not honoured)", elapsed)
	}
}

// Argv elements that contain whitespace are passed verbatim (no shell-splitting).
func TestRunPassesArgvElementsVerbatimWithoutShellSplitting(t *testing.T) {
	r := New()
	var out bytes.Buffer
	_, err := r.Run(context.Background(), Invocation{
		Name:   "sh",
		Argv:   []string{"-c", "printf %s \"$1\"", "_", "hello world"},
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != "hello world" {
		t.Errorf("argv passthrough = %q, want %q", out.String(), "hello world")
	}
}
