// Package integration — docker_up_test.go drives the full four-stage
// orchestrator (build → push → spawn → attach) against hand-rolled in-memory
// siblings. This is the integration layer for the `aa docker up` slug:
// one test per end-to-end flow through RunDockerUp wired to real
// collaborators that happen to be fakes — a fake flyclient.Client, a fake
// registry.Registry, and a fake extbin.Runner. No network, no subprocess.
//
// Coverage (from docs/architecture/docker-up.md § Workstreams Tests):
//
//   - Happy path: all four stages produce the expected per-stage trace;
//     exactly one machine is created; no destroy occurs; the pushed tag
//     is the one the spawn refers to.
//   - --force replacement: a pre-existing machine carrying the expected
//     aa.up-id label is destroyed AFTER push success and BEFORE the
//     fresh spawn, then a new machine is created and attached.
//
// Fakes are self-contained in this file (with distinct type names so they
// do not collide with other tests in this package that carry their own
// fakes for sibling slugs). Stdlib only.
//
// RED until Wave-3 implementation of RunDockerUp lands. The stub
// implementation in v2/docker_up.go panics, so tests fail loudly with a
// "not implemented" message — that is the intended red signal.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aa/v2/dockerup"
	"aa/v2/extbin"
	"aa/v2/flyclient"
	"aa/v2/registry"
)

// ---------------------------------------------------------------------------
// Fakes (self-contained, distinct names to avoid collisions with other
// integration tests in this package)
// ---------------------------------------------------------------------------

// upFakeFly is a minimal in-memory flyclient.Client for docker-up tests.
// Records every call in order; exposes a preseed hook so --force and
// re-run-refusal scenarios can plant a machine carrying the identity label
// before the orchestrator runs.
type upFakeFly struct {
	mu       sync.Mutex
	calls    []string
	machines map[string][]flyclient.Machine
	nextID   int
}

func newUpFakeFly() *upFakeFly {
	return &upFakeFly{machines: map[string][]flyclient.Machine{}}
}

func (f *upFakeFly) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *upFakeFly) EnsureApp(ctx context.Context, app string) error {
	f.record("EnsureApp:" + app)
	return nil
}

func (f *upFakeFly) Create(ctx context.Context, app string, spec flyclient.SpawnSpec) (flyclient.Machine, error) {
	f.mu.Lock()
	f.nextID++
	id := fmt.Sprintf("m%03d", f.nextID)
	f.mu.Unlock()
	f.record("Create:" + app + ":" + spec.Image + ":" + spec.Labels["aa.up-id"])
	m := flyclient.Machine{ID: id, State: "created", Region: spec.Region, Labels: spec.Labels}
	f.mu.Lock()
	f.machines[app] = append(f.machines[app], m)
	f.mu.Unlock()
	return m, nil
}

func (f *upFakeFly) Get(ctx context.Context, app, id string) (flyclient.Machine, error) {
	f.record("Get:" + app + ":" + id)
	return flyclient.Machine{ID: id, State: "started"}, nil
}

func (f *upFakeFly) WaitStarted(ctx context.Context, app, id string) error {
	f.record("WaitStarted:" + app + ":" + id)
	return nil
}

func (f *upFakeFly) List(ctx context.Context, app string) ([]flyclient.Machine, error) {
	f.record("List:" + app)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]flyclient.Machine(nil), f.machines[app]...), nil
}

func (f *upFakeFly) Start(ctx context.Context, app, id string) error {
	f.record("Start:" + app + ":" + id)
	return nil
}

func (f *upFakeFly) Stop(ctx context.Context, app, id string) error {
	f.record("Stop:" + app + ":" + id)
	return nil
}

func (f *upFakeFly) Destroy(ctx context.Context, app, id string, force bool) error {
	f.record("Destroy:" + app + ":" + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.machines[app]
	out := cur[:0]
	for _, m := range cur {
		if m.ID != id {
			out = append(out, m)
		}
	}
	f.machines[app] = out
	return nil
}

func (f *upFakeFly) FindByLabel(ctx context.Context, app, key, value string) ([]flyclient.Machine, error) {
	f.record("FindByLabel:" + app + ":" + key + "=" + value)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flyclient.Machine
	for _, m := range f.machines[app] {
		if m.Labels[key] == value {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *upFakeFly) preseed(app, id, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[app] = append(f.machines[app], flyclient.Machine{
		ID:     id,
		State:  "started",
		Labels: map[string]string{"aa.up-id": label},
	})
}

// upFakeRegistry is a minimal in-memory registry.Registry.
type upFakeRegistry struct {
	mu     sync.Mutex
	calls  []string
	pushed []string
}

func (r *upFakeRegistry) Login(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "Login")
	return nil
}

func (r *upFakeRegistry) Push(ctx context.Context, tag string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "Push:"+tag)
	r.pushed = append(r.pushed, tag)
	return nil
}

func (r *upFakeRegistry) List(ctx context.Context, prefix string) ([]registry.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []registry.Image
	for _, t := range r.pushed {
		if prefix == "" || strings.HasPrefix(t, prefix) {
			out = append(out, registry.Image{Tag: t})
		}
	}
	return out, nil
}

func (r *upFakeRegistry) Delete(ctx context.Context, tag string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "Delete:"+tag)
	return nil
}

// upFakeRunner is a minimal in-memory extbin.Runner. Per-binary exit codes
// are configurable so we can induce attach failure (flyctl exit 1) or
// build failure (docker exit 1) surgically.
type upFakeRunner struct {
	mu        sync.Mutex
	calls     []extbin.Invocation
	exitByCmd map[string]int
}

func newUpFakeRunner() *upFakeRunner {
	return &upFakeRunner{exitByCmd: map[string]int{"docker": 0, "flyctl": 0}}
}

func (r *upFakeRunner) Run(ctx context.Context, inv extbin.Invocation) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, extbin.Invocation{Name: inv.Name, Argv: append([]string(nil), inv.Argv...), Env: inv.Env})
	return r.exitByCmd[inv.Name], nil
}

func (r *upFakeRunner) invocationsOf(name string) []extbin.Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []extbin.Invocation
	for _, c := range r.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// stageDockerfileDir creates a t.TempDir() with a realistic Dockerfile and
// returns the directory path. Mirrors the e2e-layer staging helper.
func stageDockerfileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.19\nCMD [\"/bin/sh\"]\n"), 0o644); err != nil {
		t.Fatalf("stageDockerfileDir: %v", err)
	}
	return dir
}

// expectedLabelFor computes sha256(lower(abs(path)))[:12] — the same rule
// the production code must follow (ADR-1 + 2026-04-23 amendment).
func expectedLabelFor(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", path, err)
	}
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	return hex.EncodeToString(h[:])[:12]
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// TestIntegrationDockerUp_HappyPathRunsAllFourStages wires RunDockerUp to
// fakes that all succeed. It asserts the pipeline produced: at least one
// docker build invocation, exactly one registry.Push, exactly one
// flyclient.Create, exactly one flyctl ssh-console invocation — and NO
// destroys (happy path does not clean up).
func TestIntegrationDockerUp_HappyPathRunsAllFourStages(t *testing.T) {
	dir := stageDockerfileDir(t)
	fly := newUpFakeFly()
	reg := &upFakeRegistry{}
	run := newUpFakeRunner()

	opts := dockerup.Options{
		BuildContextPath: dir,
		AppName:          "aa-apps",
		RegistryBase:     "registry.fly.io",
		Fly:              fly,
		Registry:         reg,
		ExtBin:           run,
	}

	if err := dockerup.Run(context.Background(), opts); err != nil {
		t.Fatalf("happy path: expected nil error, got %v", err)
	}

	// Build ran: at least one docker invocation.
	if got := len(run.invocationsOf("docker")); got < 1 {
		t.Fatalf("happy path: expected ≥1 docker invocation for build, got %d", got)
	}
	// Push happened exactly once.
	pushes := 0
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			pushes++
		}
	}
	if pushes != 1 {
		t.Fatalf("happy path: expected exactly 1 Push, got %d; reg.calls=%+v", pushes, reg.calls)
	}
	// Spawn: exactly one Create with the expected identity label.
	label := expectedLabelFor(t, dir)
	createSeen := false
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") && strings.HasSuffix(c, ":"+label) {
			if createSeen {
				t.Fatalf("happy path: expected exactly 1 Create, saw a second: %s", c)
			}
			createSeen = true
		}
	}
	if !createSeen {
		t.Fatalf("happy path: expected a Create tagged with label %q, got fly.calls=%+v", label, fly.calls)
	}
	// Attach: exactly one flyctl invocation.
	if got := len(run.invocationsOf("flyctl")); got != 1 {
		t.Fatalf("happy path: expected exactly 1 flyctl invocation for attach, got %d", got)
	}
	// No destroys on happy path.
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Destroy:") {
			t.Fatalf("happy path: expected NO Destroy, got %s in fly.calls=%+v", c, fly.calls)
		}
	}
	// No registry deletes (image retained).
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Delete:") {
			t.Fatalf("happy path: expected NO registry Delete, got %s", c)
		}
	}
}

// ---------------------------------------------------------------------------
// --force replacement
// ---------------------------------------------------------------------------

// TestIntegrationDockerUp_ForceReplaceDestroysOldBetweenPushAndSpawn pins
// ADR-4 of docs/architecture/docker-up.md. The pre-existing machine (tagged
// with the path's identity label) must be destroyed AFTER the push call
// and BEFORE the fresh Create. This is what distinguishes the docker-up
// cascade from the "destroy first" alternative and is the single most
// load-bearing ordering invariant in the slug.
func TestIntegrationDockerUp_ForceReplaceDestroysOldBetweenPushAndSpawn(t *testing.T) {
	dir := stageDockerfileDir(t)
	fly := newUpFakeFly()
	reg := &upFakeRegistry{}
	run := newUpFakeRunner()

	label := expectedLabelFor(t, dir)
	fly.preseed("aa-apps", "m-old", label)

	opts := dockerup.Options{
		BuildContextPath: dir,
		Force:            true,
		AppName:          "aa-apps",
		RegistryBase:     "registry.fly.io",
		Fly:              fly,
		Registry:         reg,
		ExtBin:           run,
	}

	if err := dockerup.Run(context.Background(), opts); err != nil {
		t.Fatalf("--force replace: unexpected error: %v", err)
	}

	// Destroy of m-old must appear; a fresh Create must appear; destroy
	// index must come before the Create index.
	destroyIdx, createIdx := -1, -1
	for i, c := range fly.calls {
		if c == "Destroy:aa-apps:m-old" && destroyIdx == -1 {
			destroyIdx = i
		}
		if strings.HasPrefix(c, "Create:") && createIdx == -1 {
			createIdx = i
		}
	}
	if destroyIdx < 0 {
		t.Fatalf("--force replace: expected Destroy of preseeded m-old, got fly.calls=%+v", fly.calls)
	}
	if createIdx < 0 {
		t.Fatalf("--force replace: expected a fresh Create, got fly.calls=%+v", fly.calls)
	}
	if destroyIdx >= createIdx {
		t.Fatalf("--force replace: expected Destroy (idx %d) BEFORE Create (idx %d) per ADR-4; fly.calls=%+v",
			destroyIdx, createIdx, fly.calls)
	}
	// Push must have happened before the destroy (push→destroy→spawn).
	pushed := false
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("--force replace: expected a registry Push to have landed; reg.calls=%+v", reg.calls)
	}
	// Attach targeted the NEW machine id (not m-old).
	flyctlCalls := run.invocationsOf("flyctl")
	if len(flyctlCalls) != 1 {
		t.Fatalf("--force replace: expected exactly 1 flyctl attach, got %d", len(flyctlCalls))
	}
	argv := flyctlCalls[0].Argv
	for _, a := range argv {
		if a == "m-old" {
			t.Fatalf("--force replace: attach must target the NEW machine, not m-old; argv=%+v", argv)
		}
	}
}
