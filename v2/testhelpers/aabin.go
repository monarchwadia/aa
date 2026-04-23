package testhelpers

// TestMain helper: compile the real aa binary once per package into a
// temporary directory, expose its path via AABinaryPath, and clean up at
// exit. Per ADR-6 the compile happens exactly once per test package to keep
// e2e test runs fast.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	aaBinaryMu   sync.Mutex
	aaBinaryPath string
)

// AABinaryPath returns the absolute path of the compiled aa binary set up by
// UseAAHarness. Returns an empty string if UseAAHarness has not been called.
func AABinaryPath() string {
	aaBinaryMu.Lock()
	defer aaBinaryMu.Unlock()
	return aaBinaryPath
}

// UseAAHarness compiles the aa binary once, runs the test package via
// m.Run, and removes the compiled binary on the way out. Call it from a
// consumer package's TestMain.
//
// Example:
//
//	func TestMain(m *testing.M) {
//	    testhelpers.UseAAHarness(m)
//	}
func UseAAHarness(m *testing.M) {
	dir, err := os.MkdirTemp("", "aa-testbin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "UseAAHarness: mkdirtemp: %v\n", err)
		os.Exit(2)
	}
	binName := "aa"
	if runtime.GOOS == "windows" {
		binName = "aa.exe"
	}
	binPath := filepath.Join(dir, binName)

	// Locate the v2 module root relative to this source file, then compile
	// the package there. Using the source file keeps the compile path
	// robust to whatever cwd the test runner chose.
	_, thisFile, _, _ := runtime.Caller(0)
	moduleDir := filepath.Dir(filepath.Dir(thisFile))

	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = moduleDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "UseAAHarness: go build: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(2)
	}

	aaBinaryMu.Lock()
	aaBinaryPath = binPath
	aaBinaryMu.Unlock()

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
