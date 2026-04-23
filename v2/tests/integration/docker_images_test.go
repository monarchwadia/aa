// Package integration — docker_images_test.go: full build → push → ls → rm
// integration test for the docker-images slug.
//
// Uses a fake extbin.Runner that records invocations, plus an httptest.Server
// posing as registry.fly.io. No real subprocess, no real network, no real $HOME.
//
// Covers: ADR 1 default tag convention, ADR 2 ls --all vs default scoping,
// ADR 3 multi-tag rm with per-item reporting, ADR 4 login-once-per-process,
// ADR 5 build output streamed verbatim to stdout/stderr. Plus the two
// documented negative paths (missing Dockerfile; missing docker binary).
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aa/v2/dockerimage"
	"aa/v2/extbin"
	"aa/v2/registry"
)

// recordedInvocation is one observed call on the fake docker runner.
type recordedInvocation struct {
	Name string
	Argv []string
	Env  map[string]string
}

// fakeRunner is an in-memory extbin.Runner that records calls and produces
// canned output/exit codes. Goroutine-safe.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []recordedInvocation
	exit    int
	stdout  string
	stderr  string
	missing bool // if true, behave like the binary is not installed.
}

func (f *fakeRunner) Run(ctx context.Context, inv extbin.Invocation) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missing {
		return 0, &missingBinaryError{name: inv.Name}
	}
	argv := append([]string(nil), inv.Argv...)
	env := map[string]string{}
	for k, v := range inv.Env {
		env[k] = v
	}
	f.calls = append(f.calls, recordedInvocation{Name: inv.Name, Argv: argv, Env: env})
	if f.stdout != "" && inv.Stdout != nil {
		io.WriteString(inv.Stdout, f.stdout)
	}
	if f.stderr != "" && inv.Stderr != nil {
		io.WriteString(inv.Stderr, f.stderr)
	}
	return f.exit, nil
}

func (f *fakeRunner) snapshot() []recordedInvocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedInvocation, len(f.calls))
	copy(out, f.calls)
	return out
}

type missingBinaryError struct{ name string }

func (e *missingBinaryError) Error() string {
	return "exec: \"" + e.name + "\": not found in $PATH"
}

// newRegistryTestServer returns an httptest.Server that answers list/delete
// requests against an in-memory repo→tags map and records every request.
func newRegistryTestServer(t *testing.T, contents map[string][]string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var mu sync.Mutex
	var captured []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = append(captured, r.Clone(r.Context()))
		mu.Unlock()
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(r.URL.Path, "_catalog"):
			repos := make([]string, 0, len(contents))
			for k := range contents {
				repos = append(repos, k)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": repos})
		case strings.Contains(r.URL.Path, "/tags/list"):
			repo := strings.TrimPrefix(r.URL.Path, "/v2/")
			repo = strings.TrimSuffix(repo, "/tags/list")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": repo,
				"tags": contents[repo],
			})
		case strings.Contains(r.URL.Path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// setupSandbox isolates $HOME and env, builds the injected Deps.
func setupSandbox(t *testing.T, regURL string) (*fakeRunner, registry.Registry, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AA_REGISTRY_BASE", regURL)
	t.Setenv("FLY_API_TOKEN", "test-token")
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{exit: 0, stdout: "step 1/3\nstep 2/3\nstep 3/3\n", stderr: ""}
	reg := registry.New(regURL, "test-token")
	return runner, reg, &stdout, &stderr
}

func makeDeps(runner extbin.Runner, reg registry.Registry, stdout, stderr io.Writer) dockerimage.Deps {
	return dockerimage.Deps{
		DockerRunner: runner,
		Registry:     reg,
		Token:        "test-token",
		TokenKey:     "token.flyio",
		Stdout:       stdout,
		Stderr:       stderr,
	}
}

// Full happy-path journey: build → push → ls → rm.
func TestIntegrationDockerImagesBuildPushLsRm(t *testing.T) {
	srv, regCaptured := newRegistryTestServer(t, map[string][]string{
		"aa-apps/myapi": {"latest"},
	})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	deps := makeDeps(runner, reg, stdout, stderr)

	projectDir := filepath.Join(t.TempDir(), "myapi")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := dockerimage.Run(context.Background(), deps, []string{"build", projectDir}); code != 0 {
		t.Fatalf("build exit=%d stderr=%s", code, stderr.String())
	}
	calls := runner.snapshot()
	if len(calls) == 0 {
		t.Fatal("expected docker to be invoked by build")
	}
	const wantTag = "registry.fly.io/aa-apps/myapi:latest"
	if !argvContainsPair(calls[0].Argv, "-t", wantTag) {
		t.Errorf("build argv missing `-t %s`; got %v", wantTag, calls[0].Argv)
	}

	stdout.Reset()
	stderr.Reset()
	if code := dockerimage.Run(context.Background(), deps, []string{"push", wantTag}); code != 0 {
		t.Fatalf("push exit=%d stderr=%s", code, stderr.String())
	}
	calls = runner.snapshot()
	loginIdx, pushIdx := -1, -1
	for i, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "login" {
			loginIdx = i
		}
		if len(c.Argv) > 0 && c.Argv[0] == "push" {
			pushIdx = i
		}
	}
	if loginIdx < 0 || pushIdx < 0 || loginIdx >= pushIdx {
		t.Errorf("expected login before push; calls=%v", calls)
	}

	stdout.Reset()
	if code := dockerimage.Run(context.Background(), deps, []string{"ls"}); code != 0 {
		t.Fatalf("ls exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aa-apps/myapi") {
		t.Errorf("ls output missing aa-apps/myapi; got %q", stdout.String())
	}

	stdout.Reset()
	if code := dockerimage.Run(context.Background(), deps, []string{"rm", wantTag}); code != 0 {
		t.Fatalf("rm exit=%d", code)
	}
	sawDelete := false
	for _, r := range *regCaptured {
		if r.Method == http.MethodDelete {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("expected DELETE request to registry")
	}
}

// ADR 2: `ls --all` returns repositories outside the aa-apps/* prefix.
func TestIntegrationLsAllIncludesForeignNamespaces(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{
		"aa-apps/myapi": {"latest"},
		"other-ns/app":  {"v1"},
	})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	deps := makeDeps(runner, reg, stdout, stderr)

	if code := dockerimage.Run(context.Background(), deps, []string{"ls"}); code != 0 {
		t.Fatalf("ls exit=%d", code)
	}
	if strings.Contains(stdout.String(), "other-ns/app") {
		t.Errorf("default ls leaked non-aa namespace: %q", stdout.String())
	}

	stdout.Reset()
	if code := dockerimage.Run(context.Background(), deps, []string{"ls", "--all"}); code != 0 {
		t.Fatalf("ls --all exit=%d", code)
	}
	if !strings.Contains(stdout.String(), "other-ns/app") {
		t.Errorf("ls --all missing other-ns/app; got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "aa-apps/myapi") {
		t.Errorf("ls --all missing aa-apps/myapi; got %q", stdout.String())
	}
}

// ADR 3: multi-tag rm reports each outcome; exit is non-zero iff any failed.
func TestIntegrationRmMultiTagPerItemReporting(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{
		"aa-apps/a": {"latest"},
	})
	// Override: any request for aa-apps/b fails with 404.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "aa-apps/b") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
		w.WriteHeader(http.StatusOK)
	})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	deps := makeDeps(runner, reg, stdout, stderr)

	code := dockerimage.Run(context.Background(), deps,
		[]string{"rm",
			"registry.fly.io/aa-apps/a:latest",
			"registry.fly.io/aa-apps/b:latest",
		})
	if code == 0 {
		t.Error("expected non-zero exit when any tag fails")
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "a:latest") {
		t.Errorf("expected report mentioning 'a:latest'; got %q", combined)
	}
	if !strings.Contains(combined, "b:latest") {
		t.Errorf("expected report mentioning 'b:latest'; got %q", combined)
	}
}

// ADR 4: `docker login` is invoked at most once per process, even across
// multiple pushes driven by the same Deps.
func TestIntegrationLoginIdempotentPerProcess(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	deps := makeDeps(runner, reg, stdout, stderr)

	const tag = "registry.fly.io/aa-apps/myapi:latest"
	_ = dockerimage.Run(context.Background(), deps, []string{"push", tag})
	_ = dockerimage.Run(context.Background(), deps, []string{"push", tag})

	loginCount := 0
	for _, c := range runner.snapshot() {
		if len(c.Argv) > 0 && c.Argv[0] == "login" {
			loginCount++
		}
	}
	if loginCount != 1 {
		t.Errorf("expected exactly 1 login across two pushes, got %d", loginCount)
	}
}

// ADR 5: build stdout/stderr from the runner reach the user's writers verbatim.
func TestIntegrationBuildStreamsOutputVerbatim(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	runner.stdout = "line-one\nline-two\n"
	runner.stderr = "warning: something\n"
	deps := makeDeps(runner, reg, stdout, stderr)

	projectDir := filepath.Join(t.TempDir(), "svc")
	_ = os.MkdirAll(projectDir, 0o755)
	_ = os.WriteFile(filepath.Join(projectDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)

	if code := dockerimage.Run(context.Background(), deps, []string{"build", projectDir}); code != 0 {
		t.Fatalf("build exit=%d", code)
	}
	if !strings.Contains(stdout.String(), "line-one") || !strings.Contains(stdout.String(), "line-two") {
		t.Errorf("stdout missing verbatim docker lines: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning: something") {
		t.Errorf("stderr missing verbatim docker stderr: %q", stderr.String())
	}
}

// Negative: build against a directory with no Dockerfile exits non-zero,
// stderr names the missing Dockerfile, and docker is never invoked.
func TestIntegrationBuildMissingDockerfile(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	deps := makeDeps(runner, reg, stdout, stderr)

	emptyDir := t.TempDir()
	code := dockerimage.Run(context.Background(), deps, []string{"build", emptyDir})
	if code == 0 {
		t.Errorf("expected non-zero exit; stdout=%q", stdout.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "dockerfile") {
		t.Errorf("stderr should name the missing Dockerfile; got %q", stderr.String())
	}
	for _, c := range runner.snapshot() {
		if len(c.Argv) > 0 && c.Argv[0] == "build" {
			t.Errorf("docker build invoked despite missing Dockerfile: %v", c.Argv)
		}
	}
}

// Negative: preflight catches a missing docker binary with a docker-naming error.
func TestIntegrationMissingDockerBinary(t *testing.T) {
	srv, _ := newRegistryTestServer(t, map[string][]string{})
	runner, reg, stdout, stderr := setupSandbox(t, srv.URL)
	runner.missing = true
	deps := makeDeps(runner, reg, stdout, stderr)

	projectDir := filepath.Join(t.TempDir(), "svc")
	_ = os.MkdirAll(projectDir, 0o755)
	_ = os.WriteFile(filepath.Join(projectDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)

	code := dockerimage.Run(context.Background(), deps, []string{"build", projectDir})
	if code == 0 {
		t.Error("expected non-zero exit when docker binary missing")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "docker") {
		t.Errorf("stderr should name docker; got %q", stderr.String())
	}
}

// argvContainsPair reports whether argv has first immediately followed by second.
func argvContainsPair(argv []string, first, second string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == first && argv[i+1] == second {
			return true
		}
	}
	return false
}
