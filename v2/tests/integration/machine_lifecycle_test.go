// Package integration — machine_lifecycle_test.go covers the full
// spawn → list → rm journey of the `aa machine <verb>` surface against
// a net/http/httptest.Server playing a canned Fly Machines API script,
// with a fake `flyctl` binary on PATH.
//
// This exercises every seam in docs/architecture/machine-lifecycle.md
// (flyclient HTTP contract, extbin runner contract, handler resolution
// precedence) via the real compiled `aa` binary, matching the pattern
// established by config_store_test.go. The e2e journey test covers the
// same flow at the persona/journey level; this file pins the HTTP
// request shape and the exit-code contract.
//
// Expected RED until Wave 2 bodies land. No third-party deps; stdlib only.
package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// scriptedFlyAPI returns an httptest.Server implementing a tiny script:
//
//	GET  /apps/aa-apps                → 200 (app already exists)
//	POST /apps/aa-apps/machines       → 200 {id, state:"created", region:"iad"}
//	GET  /apps/aa-apps/machines/:id   → 200 {id, state:"started", region:"iad"}
//	GET  /apps/aa-apps/machines       → 200 [the one machine we created]
//	DELETE /apps/aa-apps/machines/:id → 200
//
// requests is a thread-safe log of every request the server saw; tests
// assert against it via the returned mutex.
func scriptedFlyAPI(t *testing.T) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	const createdID = "9080e6f3a12345"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/apps/aa-apps":
			w.WriteHeader(200)
		case r.Method == "POST" && r.URL.Path == "/apps/aa-apps/machines":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": createdID, "state": "created", "region": "iad",
			})
		case r.Method == "GET" && r.URL.Path == "/apps/aa-apps/machines/"+createdID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": createdID, "state": "started", "region": "iad",
			})
		case r.Method == "GET" && r.URL.Path == "/apps/aa-apps/machines":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": createdID, "state": "started", "region": "iad"},
			})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/apps/aa-apps/machines/"):
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &requests, &mu
}

// installFakeFlyctl writes a no-op `flyctl` shell script into a tempdir
// and returns that dir so it can be prepended to PATH.
func installFakeFlyctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	path := filepath.Join(dir, "flyctl")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake flyctl: %v", err)
	}
	return dir
}

// runAAWithEnv invokes the compiled aa binary with the given argv and env overrides.
func runAAWithEnv(t *testing.T, bin string, extraEnv map[string]string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return so.String(), se.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("run aa: %v; stderr=%q", err, se.String())
	}
	return so.String(), se.String(), 0
}

// Spawn then list then rm round-trips through every documented HTTP exchange.
func TestMachineLifecycleSpawnListRmRoundTripsAgainstScriptedFlyAPI(t *testing.T) {
	bin := buildAA(t)
	srv, requests, mu := scriptedFlyAPI(t)
	flyctlDir := installFakeFlyctl(t)
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"FLY_API_BASE":    srv.URL,
		"FLY_API_TOKEN":   "tok_test",
		"PATH":            flyctlDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}

	if _, se, code := runAAWithEnv(t, bin, env, "machine", "spawn"); code != 0 {
		t.Fatalf("spawn exit = %d, stderr=%q", code, se)
	}
	if so, se, code := runAAWithEnv(t, bin, env, "machine", "ls"); code != 0 {
		t.Fatalf("ls exit = %d, stderr=%q stdout=%q", code, se, so)
	} else if !strings.Contains(so, "9080e6f3a12345") {
		t.Errorf("ls stdout missing ID: %q", so)
	}
	if _, se, code := runAAWithEnv(t, bin, env, "machine", "rm", "9080e6f3a12345"); code != 0 {
		t.Fatalf("rm exit = %d, stderr=%q", code, se)
	}

	mu.Lock()
	defer mu.Unlock()
	wantAny := []string{
		"GET /apps/aa-apps",
		"POST /apps/aa-apps/machines",
		"GET /apps/aa-apps/machines/9080e6f3a12345",
		"GET /apps/aa-apps/machines",
		"DELETE /apps/aa-apps/machines/9080e6f3a12345",
	}
	for _, w := range wantAny {
		found := false
		for _, r := range *requests {
			if r == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("requests missing %q; saw %v", w, *requests)
		}
	}
}

// A scripted 500 on app-create surfaces as a non-zero exit and a diagnostic stderr.
func TestMachineSpawnExitsNonZeroWhenEnsureAppFailsWithFiveHundred(t *testing.T) {
	bin := buildAA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/apps/aa-apps" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	flyctlDir := installFakeFlyctl(t)
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"FLY_API_BASE":    srv.URL,
		"FLY_API_TOKEN":   "tok_test",
		"PATH":            flyctlDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
	_, se, code := runAAWithEnv(t, bin, env, "machine", "spawn")
	if code == 0 {
		t.Fatalf("spawn exit = 0, want non-zero on 500 from POST /apps; stderr=%q", se)
	}
}

// `aa machine ls` with no token and no config surfaces the config-key diagnostic.
func TestMachineLsWithoutAnyTokenSourceExitsNonZeroAndNamesTokenFlyio(t *testing.T) {
	bin := buildAA(t)
	home := t.TempDir()
	env := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"FLY_API_TOKEN":   "",
	}
	_, se, code := runAAWithEnv(t, bin, env, "machine", "ls")
	if code == 0 {
		t.Fatalf("ls exit = 0, want non-zero without token; stderr=%q", se)
	}
	if !strings.Contains(se, "token.flyio") {
		t.Errorf("stderr must mention config key `token.flyio`, got %q", se)
	}
}
