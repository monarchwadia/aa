// Package testhelpers is the e2e test harness used by every slug's e2e
// tests. It provides one primary type — Sandbox — that isolates a test's
// HOME, wires two local httptest.Server instances (api + registry) into the
// child aa process via env vars, and plants fake external binaries on PATH.
//
// The harness is stdlib-only. See docs/architecture/test-harness.md for the
// architecture and ADR set that this package implements.
package testhelpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Sandbox is the per-test isolation boundary. One Sandbox = one test.
// Teardown is registered on the *testing.T passed to NewSandbox.
type Sandbox struct {
	t            *testing.T
	snapshotName string
	snapshotPath string

	tmpDir        string
	homeDir       string
	xdgConfigHome string
	binDir        string
	logDir        string

	pathValue string

	apiFake      *httpFake
	registryFake *httpFake
	queue        *snapshotQueue
	drift        *driftLog

	// fake-binary declarations; last write wins on re-declaration of the
	// same Name.
	fakeDecls map[string]FakeBinary
	fakeOpts  map[string][]fakeBinaryConfig

	// lastRunEnv is retained for debugging; tests read invocations via the
	// BinaryInvocations method.
	runMu sync.Mutex
}

// NewSandbox creates a fresh sandbox for the given test. snapshotName selects
// the on-disk snapshot at v2/testdata/snapshots/<snapshotName>.json. In replay
// mode (default) the snapshot must exist; in record mode (AA_TEST_RECORD=1,
// not exercised by wave-1 meta-tests) it is overwritten at cleanup.
//
// Example:
//
//	sandbox := NewSandbox(t, "spawn_happy_path")
//	sandbox.RunAA(t, []string{"machine", "spawn"}, nil)
func NewSandbox(t *testing.T, snapshotName string) *Sandbox {
	t.Helper()

	snapshotPath := resolveSnapshotPath(snapshotName)
	entries, err := loadSnapshot(snapshotPath)
	if err != nil {
		t.Fatalf("NewSandbox: %v (hint: run with AA_TEST_RECORD=1 to generate)", err)
	}

	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	xdg := filepath.Join(home, ".config")
	binDir := filepath.Join(tmp, "bin")
	logDir := filepath.Join(tmp, "invocations")
	for _, dir := range []string{home, xdg, binDir, logDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("NewSandbox: mkdir %s: %v", dir, err)
		}
	}

	queue := newSnapshotQueue(entries)
	drift := &driftLog{}
	apiFake := newReplayHTTPFake("api", queue, drift)
	registryFake := newReplayHTTPFake("registry", queue, drift)

	// Prepend our bin dir so fakes take precedence over real binaries.
	pathValue := binDir + string(os.PathListSeparator) + os.Getenv("PATH")

	// The sandbox exports env into child aa processes via RunAA. It does
	// not mutate the parent test process's env — doing so would break
	// os.UserHomeDir() calls in sibling code and is unnecessary because
	// the compiled aa binary runs as a child with its own env block.

	sb := &Sandbox{
		t:             t,
		snapshotName:  snapshotName,
		snapshotPath:  snapshotPath,
		tmpDir:        tmp,
		homeDir:       home,
		xdgConfigHome: xdg,
		binDir:        binDir,
		logDir:        logDir,
		pathValue:     pathValue,
		apiFake:       apiFake,
		registryFake:  registryFake,
		queue:         queue,
		drift:         drift,
		fakeDecls:     map[string]FakeBinary{},
		fakeOpts:      map[string][]fakeBinaryConfig{},
	}

	t.Cleanup(func() {
		apiFake.close()
		registryFake.close()
		// Surface any drift captured by the HTTP handlers. Per ADR-3 this
		// is a loud failure; we use t.Errorf (not Fatalf) from cleanup
		// because Fatalf is not valid from goroutine-spawned callbacks.
		for _, msg := range drift.drain() {
			t.Errorf("snapshot drift in %s (snapshot %s):\n%s",
				t.Name(), snapshotPath, msg)
		}
		// ADR-4: unconsumed snapshot entries are a failure, but only if
		// the test didn't already fail for another reason.
		if !t.Failed() {
			if rest := queue.remaining(); len(rest) > 0 {
				var msg string
				for i, e := range rest {
					msg += fmt.Sprintf("  #%d: %s %s\n", i, e.Request.Method, e.Request.Path)
				}
				t.Errorf("snapshot has %d unconsumed entries; the code under test stopped making these requests:\n%s",
					len(rest), msg)
			}
		}
	})

	return sb
}

// resolveSnapshotPath returns the absolute path of the snapshot file for
// name. It searches up from the current working directory for a testdata
// dir, matching the convention set by writeEmptySnapshotFixture in
// sandbox_test.go.
func resolveSnapshotPath(name string) string {
	candidates := []string{
		filepath.Join("..", "testdata", "snapshots", name+".json"),
		filepath.Join("testdata", "snapshots", name+".json"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	// Return the first candidate so error messages in loadSnapshot name a
	// sensible path.
	abs, _ := filepath.Abs(candidates[0])
	return abs
}

// ExpectBinary declares a fake external binary the test expects aa to
// invoke. Each call appends a declaration; re-declaration of the same Name
// overwrites the prior one. Must be called before RunAA.
//
// Example:
//
//	sandbox.ExpectBinary("flyctl",
//	    WantArgs("ssh", "console", "--app", "my-app", "--machine", "d8e7"),
//	    RespondExitCode(0),
//	)
func (s *Sandbox) ExpectBinary(name string, opts ...FakeBinaryOption) {
	s.t.Helper()
	cfg := fakeBinaryConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	s.fakeDecls[name] = FakeBinary{
		Name:     name,
		ExitCode: cfg.respondExit,
		Stdout:   cfg.respondStdout,
		Stderr:   cfg.respondStderr,
	}
	s.fakeOpts[name] = append(s.fakeOpts[name], cfg)
}

// RunAA executes the compiled aa binary inside the sandbox with the given
// argv and extra env. The sandbox env vars (HOME, XDG_CONFIG_HOME, PATH,
// FLY_API_BASE, AA_REGISTRY_BASE) are already wired. The returned Result is
// fully captured — the child has exited.
//
// Example:
//
//	result := sandbox.RunAA(t, []string{"config"}, nil)
//	if result.ExitCode != 0 { t.Fatal(result.Stderr) }
func (s *Sandbox) RunAA(t *testing.T, argv []string, env map[string]string) Result {
	t.Helper()
	s.runMu.Lock()
	defer s.runMu.Unlock()

	// Plant any declared fakes just before exec.
	for name, decl := range s.fakeDecls {
		if err := writeFakeBinary(s.binDir, s.logDir, decl); err != nil {
			t.Fatalf("writeFakeBinary %s: %v", name, err)
		}
	}

	path := AABinaryPath()
	if path == "" {
		t.Fatalf("RunAA: aa binary not built; call UseAAHarness(m) from TestMain")
	}

	cmd := exec.Command(path, argv...)

	// Build env: start from the parent process env (so PATH's standard
	// dirs survive), then apply the sandbox's isolation overrides, then
	// per-call overrides on top.
	cmd.Env = append([]string(nil), os.Environ()...)
	cmd.Env = appendEnv(cmd.Env, "HOME", s.homeDir)
	cmd.Env = appendEnv(cmd.Env, "XDG_CONFIG_HOME", s.xdgConfigHome)
	cmd.Env = appendEnv(cmd.Env, "PATH", s.pathValue)
	cmd.Env = appendEnv(cmd.Env, "FLY_API_BASE", s.apiFake.url())
	cmd.Env = appendEnv(cmd.Env, "AA_REGISTRY_BASE", s.registryFake.url())
	cmd.Env = appendEnv(cmd.Env, "FLY_API_TOKEN", "")
	for k, v := range env {
		cmd.Env = appendEnv(cmd.Env, k, v)
	}

	var stdout, stderr bufferSink
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("RunAA: start: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		exit := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else {
				t.Fatalf("RunAA: wait: %v", err)
			}
		}
		return Result{
			ExitCode: exit,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("RunAA: child aa process timed out after 30s; stdout=%q stderr=%q",
			stdout.String(), stderr.String())
		return Result{}
	}
}

// BinaryInvocations returns the ordered log of calls into the named fake
// binary captured during any RunAA call so far.
//
// Example:
//
//	invs := sandbox.BinaryInvocations("flyctl")
//	if len(invs) != 1 { t.Fatalf("expected one flyctl call, got %d", len(invs)) }
func (s *Sandbox) BinaryInvocations(name string) []Invocation {
	s.t.Helper()
	invs, err := readInvocations(s.logDir, name)
	if err != nil {
		s.t.Fatalf("BinaryInvocations(%s): %v", name, err)
	}
	return invs
}

// SnapshotPath returns the absolute path of the snapshot file backing this
// sandbox.
func (s *Sandbox) SnapshotPath() string { return s.snapshotPath }

// ConfigPath returns the absolute path to aa's config file inside the
// sandbox. Useful for asserting that writes landed.
func (s *Sandbox) ConfigPath() string {
	return filepath.Join(s.xdgConfigHome, "aa", "config")
}

// HomeDir returns the sandbox's isolated $HOME path.
func (s *Sandbox) HomeDir() string { return s.homeDir }

// XDGConfigHome returns the sandbox's isolated $XDG_CONFIG_HOME path.
func (s *Sandbox) XDGConfigHome() string { return s.xdgConfigHome }

// BinDir returns the directory where fake binaries are planted and which is
// prepended to the child process's PATH.
func (s *Sandbox) BinDir() string { return s.binDir }

// PATH returns the value of $PATH that the sandbox exports into child
// processes. Begins with the sandbox bin dir.
func (s *Sandbox) PATH() string { return s.pathValue }

// APIBaseURL returns the local httptest.Server URL wired into $FLY_API_BASE.
func (s *Sandbox) APIBaseURL() string { return s.apiFake.url() }

// RegistryBaseURL returns the local httptest.Server URL wired into
// $AA_REGISTRY_BASE.
func (s *Sandbox) RegistryBaseURL() string { return s.registryFake.url() }

// Result is the outcome of one RunAA child process.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// appendEnv replaces any existing KEY= entry in env with "KEY=value", or
// appends it if absent. Child-process env construction helper.
func appendEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// bufferSink is a minimal thread-safe byte buffer implementing io.Writer.
// Used to capture child stdout/stderr without pulling in bytes.Buffer's full
// surface; the sandbox never reads these concurrently with the child.
type bufferSink struct {
	mu  sync.Mutex
	buf []byte
}

func (b *bufferSink) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *bufferSink) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
