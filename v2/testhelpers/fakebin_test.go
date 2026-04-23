package testhelpers

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These meta-tests pin the fake-binary layer (ADR-6 fakebin.go):
//   - writeFakeBinary drops a real executable into $bindir/<name>
//   - invocation honors declared exit code, stdout, stderr
//   - argv and stdin are captured
//   - multiple invocations are recorded in order via readInvocations

func TestFakeBinary_ExitCodeReturned(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{
		Name:     "flyctl",
		ExitCode: 7,
	}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}

	cmd := exec.Command(filepath.Join(binDir, "flyctl"))
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if ee.ExitCode() != 7 {
		t.Fatalf("expected exit code 7, got %d", ee.ExitCode())
	}
}

func TestFakeBinary_StdoutPlumbed(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{
		Name:   "flyctl",
		Stdout: "hello stdout",
	}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}

	out, err := exec.Command(filepath.Join(binDir, "flyctl")).Output()
	if err != nil {
		t.Fatalf("cmd failed: %v", err)
	}
	if !strings.Contains(string(out), "hello stdout") {
		t.Fatalf("stdout missing: %q", out)
	}
}

func TestFakeBinary_StderrPlumbed(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{
		Name:   "flyctl",
		Stderr: "hello stderr",
	}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}

	cmd := exec.Command(filepath.Join(binDir, "flyctl"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd failed: %v", err)
	}
	if !strings.Contains(stderr.String(), "hello stderr") {
		t.Fatalf("stderr missing: %q", stderr.String())
	}
}

func TestFakeBinary_ArgvCaptured(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{Name: "flyctl"}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}

	cmd := exec.Command(filepath.Join(binDir, "flyctl"),
		"ssh", "console", "--app", "a", "--machine", "d8e7")
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd failed: %v", err)
	}

	invs, err := readInvocations(logDir, "flyctl")
	if err != nil {
		t.Fatalf("readInvocations failed: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(invs))
	}
	want := []string{"ssh", "console", "--app", "a", "--machine", "d8e7"}
	if len(invs[0].Argv) != len(want) {
		t.Fatalf("argv length mismatch: got %v", invs[0].Argv)
	}
	for i, w := range want {
		if invs[0].Argv[i] != w {
			t.Fatalf("argv[%d]: want %q got %q", i, w, invs[0].Argv[i])
		}
	}
}

func TestFakeBinary_StdinCaptured(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{Name: "flyctl"}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}

	cmd := exec.Command(filepath.Join(binDir, "flyctl"))
	cmd.Stdin = strings.NewReader("hello stdin payload")
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd failed: %v", err)
	}

	invs, err := readInvocations(logDir, "flyctl")
	if err != nil {
		t.Fatalf("readInvocations failed: %v", err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(invs))
	}
	if !strings.Contains(string(invs[0].Stdin), "hello stdin payload") {
		t.Fatalf("stdin not captured: %q", invs[0].Stdin)
	}
}

func TestFakeBinary_MultipleInvocationsOrdered(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	logDir := filepath.Join(dir, "invocations")

	if err := writeFakeBinary(binDir, logDir, FakeBinary{Name: "flyctl"}); err != nil {
		t.Fatalf("writeFakeBinary failed: %v", err)
	}
	path := filepath.Join(binDir, "flyctl")

	for _, args := range [][]string{
		{"first"},
		{"second", "--x"},
		{"third"},
	} {
		if err := exec.Command(path, args...).Run(); err != nil {
			t.Fatalf("exec failed: %v", err)
		}
	}

	invs, err := readInvocations(logDir, "flyctl")
	if err != nil {
		t.Fatalf("readInvocations: %v", err)
	}
	if len(invs) != 3 {
		t.Fatalf("expected 3 invocations, got %d", len(invs))
	}
	if invs[0].Argv[0] != "first" || invs[1].Argv[0] != "second" || invs[2].Argv[0] != "third" {
		t.Fatalf("ordering wrong: %v %v %v", invs[0].Argv, invs[1].Argv, invs[2].Argv)
	}
}
