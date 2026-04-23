// Package e2e holds end-to-end journeys for the `aa` binary.
//
// Each *_test.go file in this package describes ONE user journey, drawn directly
// from INTENT.md and the README, and exercises that journey through real CLI
// invocations of the aa binary. No mocks of aa internals — only external
// fakes where a real collaborator (Anthropic API, an SSH target) would
// otherwise require paid infrastructure.
//
// Red/green discipline: until the `implement` step ships the aa binary, every
// test in this package must fail, and the failure reason must be
// "the behavior is not yet implemented" rather than a test bug.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Binary discovery
// --------------------------------------------------------------------------

// resolveAaBinaryPath returns the path to the aa binary the tests will exec.
// Resolution order:
//  1. AA_BIN environment variable (absolute path)
//  2. ./aa at the repository root (the conventional build output location)
//  3. "aa" on PATH
//
// If none resolve, the test is failed with an actionable message rather than
// silently skipped — a missing binary is the expected red state during
// documentation-driven development, but the failure must be loud so
// reviewers see it.
func resolveAaBinaryPath(t *testing.T) string {
	t.Helper()

	if fromEnv := os.Getenv("AA_BIN"); fromEnv != "" {
		if _, err := os.Stat(fromEnv); err == nil {
			return fromEnv
		}
		t.Fatalf("AA_BIN=%q does not exist on disk", fromEnv)
	}

	repoRoot := findRepoRoot(t)
	candidate := filepath.Join(repoRoot, "aa")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	if p, err := exec.LookPath("aa"); err == nil {
		return p
	}

	t.Fatalf(
		"aa binary not found. Set AA_BIN to an absolute path, or build it with "+
			"`go build -o %s ./cmd/aa`, or put it on PATH.",
		candidate,
	)
	return ""
}

// findRepoRoot walks up from the current working directory until it finds
// a go.mod file. Used so tests can locate the repo root regardless of the
// subdirectory they're invoked from.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (no go.mod above %s)", cwd)
		}
		dir = parent
	}
}

// --------------------------------------------------------------------------
// Running the aa binary
// --------------------------------------------------------------------------

// aaInvocation carries everything needed to run one aa command.
type aaInvocation struct {
	Args      []string          // positional arguments (e.g. ["push"], ["init", "--global"])
	Stdin     string            // fed to the process's stdin, may be empty
	ExtraEnv  map[string]string // added to inherited environment
	HomeDir   string            // set as $HOME for the invocation (required for isolation)
	WorkDir   string            // cwd for the invocation
	Deadline  time.Duration     // 0 means no deadline; otherwise test times out if exceeded
}

// aaResult captures the observable output of an aa invocation.
type aaResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runAa executes aa with the given invocation and returns the observed result.
// The test is NOT failed on non-zero exit; callers assert exit codes explicitly
// because many journeys exercise failure paths.
func runAa(t *testing.T, inv aaInvocation) aaResult {
	t.Helper()
	if inv.HomeDir == "" {
		t.Fatal("runAa: HomeDir is required — tests must never run against the real $HOME")
	}

	bin := resolveAaBinaryPath(t)

	ctx := context.Background()
	cancel := func() {}
	if inv.Deadline > 0 {
		ctx, cancel = context.WithTimeout(ctx, inv.Deadline)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, inv.Args...)
	cmd.Dir = inv.WorkDir
	cmd.Stdin = strings.NewReader(inv.Stdin)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.Env = append(os.Environ(),
		"HOME="+inv.HomeDir,
		"XDG_CONFIG_HOME="+filepath.Join(inv.HomeDir, ".config"),
		// Every e2e test runs under the `process` backend unless the test
		// explicitly switches; the env gate is always set so tests don't
		// fail on the safety check. Real-user scenarios set this env var
		// manually; tests set it automatically because this IS a test.
		"AA_ALLOW_UNSAFE_PROCESS_BACKEND=1",
	)
	for k, v := range inv.ExtraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Context timeout, binary missing, etc. — surface to test.
			t.Logf("aa run failed with non-exit error: %v (stderr=%q)", err, stderr.String())
			exitCode = -1
		}
	}

	return aaResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

// --------------------------------------------------------------------------
// Isolation helpers
// --------------------------------------------------------------------------

// newIsolatedHome creates a temp directory to use as $HOME for one test.
// All aa state (~/.aa/...) lives inside it. Cleaned up automatically when the
// test finishes. Using the real $HOME from a test is a fatal error — state
// would leak across runs.
func newIsolatedHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// newGitRepo creates a temp git repo with a trivial initial commit and the
// provided aa.json contents at the root. Returns the absolute path to the repo.
func newGitRepo(t *testing.T, aaJSON string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=aa-test",
			"GIT_AUTHOR_EMAIL=aa-test@example.invalid",
			"GIT_COMMITTER_NAME=aa-test",
			"GIT_COMMITTER_EMAIL=aa-test@example.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	run("git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if aaJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "aa.json"), []byte(aaJSON), 0o644); err != nil {
			t.Fatalf("write aa.json: %v", err)
		}
	}
	run("git", "add", "-A")
	run("git", "commit", "-qm", "initial")

	return dir
}

// writeGlobalConfig writes a `~/.aa/config.json` relative to `home`. The
// caller provides the JSON as a raw string so the test can be explicit about
// every field, including the typos and omissions each journey exercises.
func writeGlobalConfig(t *testing.T, home, contents string) {
	t.Helper()
	dir := filepath.Join(home, ".aa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir ~/.aa: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write ~/.aa/config.json: %v", err)
	}
}

// --------------------------------------------------------------------------
// Fake Anthropic API
// --------------------------------------------------------------------------

// apiCall records one observed call against the fake Anthropic API.
// Tests use these records to assert that aa made the expected calls — e.g.
// "a DELETE was issued for the ephemeral key on teardown".
type apiCall struct {
	Method string
	Path   string
	Body   []byte
}

// fakeAnthropic is a simple HTTP server that stands in for the subset of
// Anthropic's API surface aa uses: Messages (POST /v1/messages) and Admin
// API key lifecycle (POST /v1/organizations/.../api_keys,
// DELETE /v1/organizations/.../api_keys/<id>).
//
// It records every call so tests can assert lifecycle operations. It does not
// simulate rate limiting, auth, or real errors — that's the unit-test layer's
// concern, not e2e's.
type fakeAnthropic struct {
	Server *httptest.Server
	mu     sync.Mutex
	Calls  []apiCall
	Keys   map[string]bool // issued session key IDs; true if still live
}

// startFakeAnthropic brings up a fake API on localhost. Its URL is returned;
// tests should route aa at it via the allowlist + a DNS override or a
// test-only config field the implementation exposes. If no such hook is
// possible at the e2e layer, the test should skip with a clear message.
func startFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	fa := &fakeAnthropic{Keys: map[string]bool{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fa.mu.Lock()
		fa.Calls = append(fa.Calls, apiCall{Method: r.Method, Path: r.URL.Path, Body: body})
		fa.mu.Unlock()

		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/api_keys"):
			id := fmt.Sprintf("aa-test-key-%d", time.Now().UnixNano())
			fa.mu.Lock()
			fa.Keys[id] = true
			fa.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "key": "sk-test-" + id})

		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/api_keys/"):
			id := filepath.Base(r.URL.Path)
			fa.mu.Lock()
			fa.Keys[id] = false
			fa.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)

		default:
			// Minimal Messages stub — agents that aren't real test the egress
			// path, not the LLM response shape.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","content":[]}`))
		}
	})

	fa.Server = httptest.NewServer(mux)
	t.Cleanup(fa.Server.Close)
	return fa
}

// AssertKeyRevoked fails the test if the given key ID was issued but not
// revoked (DELETE call never observed).
func (fa *fakeAnthropic) AssertKeyRevoked(t *testing.T, keyID string) {
	t.Helper()
	fa.mu.Lock()
	defer fa.mu.Unlock()
	alive, issued := fa.Keys[keyID]
	if !issued {
		t.Fatalf("expected key %q to have been issued, but no issuance was observed", keyID)
	}
	if alive {
		t.Fatalf("expected key %q to have been revoked, but it was not", keyID)
	}
}

// IssuedKeyIDs returns every key ID the fake has handed out, in observation
// order. Useful when a test doesn't pre-know the ID (most of the time).
func (fa *fakeAnthropic) IssuedKeyIDs() []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	ids := make([]string, 0, len(fa.Keys))
	for id := range fa.Keys {
		ids = append(ids, id)
	}
	return ids
}

// --------------------------------------------------------------------------
// Fake "allowed" and "blocked" HTTP targets for egress tests
// --------------------------------------------------------------------------

// startLocalHTTPTarget returns a plain HTTP server on localhost that always
// replies 200 with the body "target:<label>". Used by the egress journey as
// both the allowed and the blocked target — only the hostname the test
// configures in the allowlist determines which is which.
func startLocalHTTPTarget(t *testing.T, label string) (url string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "target:"+label)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// freeTCPPort returns a port currently free on localhost. The port may be
// reused by the time the caller binds to it; good enough for best-effort test
// fixtures, not for production code.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// --------------------------------------------------------------------------
// Platform / dependency gates
// --------------------------------------------------------------------------

// requireLinux skips the test on non-Linux runners. Egress enforcement tests
// require real iptables; they only run on Linux.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("egress journey requires Linux kernel iptables; this runner is %s", runtime.GOOS)
	}
}

// requireDocker skips the test if `docker` is not on PATH or the daemon is
// unreachable. The local backend journeys need real Docker.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not on PATH")
	}
	out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		t.Skipf("docker daemon not reachable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// --------------------------------------------------------------------------
// Assertion helpers
// --------------------------------------------------------------------------

// assertContains fails the test if needle is not in haystack, with a clear
// diff-friendly error message.
func assertContains(t *testing.T, haystack, needle, context string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %s to contain %q.\nActual:\n%s", context, needle, haystack)
	}
}

// assertNotContains fails the test if needle IS in haystack.
func assertNotContains(t *testing.T, haystack, needle, context string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected %s NOT to contain %q.\nActual:\n%s", context, needle, haystack)
	}
}

// assertExitCode fails if the observed exit code does not match want.
func assertExitCode(t *testing.T, got, want int, context string) {
	t.Helper()
	if got != want {
		t.Fatalf("expected exit code %d for %s, got %d", want, context, got)
	}
}

// readFile returns a file's contents or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
