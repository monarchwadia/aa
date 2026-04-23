// Package dockerup: dockerup_test.go holds the unit tests for the
// `aa docker up` slug. Two bands of tests live here:
//
//  1. Label derivation (dockerup.Label) — pure function, exhaustive on the
//     ADR-1 + 2026-04-23 amendment contract: sha256(absolutePath(<path>))[:12]
//     with lower-cased path, stable across invocations, different for
//     different directories. No I/O, no collaborators.
//
//  2. Stage orchestrator (dockerup.Run) — drives the four-stage chain
//     against hand-rolled in-memory fakes of flyclient.Client,
//     registry.Registry, and extbin.Runner. Assertions cover happy path,
//     --force replacement (destroy ordering: after push success, before
//     spawn), refuse-without-force, attach-failure cleanup (destroy the
//     machine; keep the image), per-stage failure modes, and the
//     multi-match FindByLabel case.
//
// Fakes are defined in this file, not imported. They are deliberately
// tiny — record every call, let each test flip a failure switch per
// method. All tests are red until Wave-3 implementation lands.
package dockerup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aa/v2/extbin"
	"aa/v2/flyclient"
	"aa/v2/registry"
)

// ---------------------------------------------------------------------------
// Band 1: Label unit tests
// ---------------------------------------------------------------------------

// expectedLabel computes the reference value the same way ADR-1 + the
// 2026-04-23 amendment prescribe. Inlined in the test file (not a helper
// reused with the production code) so the test pins the spec, not the
// implementation.
func expectedLabel(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", path, err)
	}
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	return hex.EncodeToString(h[:])[:12]
}

// Label returns a deterministic 12-hex-char value derived from the
// absolute, lower-cased path of the given directory.
func TestLabel_KnownPathsProduceTheSpecifiedLiteral(t *testing.T) {
	dir := t.TempDir()
	want := expectedLabel(t, dir)

	got, err := Label(dir)
	if err != nil {
		t.Fatalf("Label(%q): unexpected error: %v", dir, err)
	}
	if got != want {
		t.Fatalf("Label(%q) = %q, want %q", dir, got, want)
	}
	if len(got) != 12 {
		t.Fatalf("Label(%q): expected 12-char label, got %d chars: %q", dir, len(got), got)
	}
}

// Label lower-cases the path before hashing, so "/Foo" and "/foo" (when
// both resolve to the same canonical absolute path shape) produce
// identical labels. The amendment pins this explicitly.
func TestLabel_PathIsLowercasedBeforeHashing(t *testing.T) {
	parent := t.TempDir()
	mixed := filepath.Join(parent, "MixedCaseProject")
	if err := os.MkdirAll(mixed, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", mixed, err)
	}
	gotMixed, err := Label(mixed)
	if err != nil {
		t.Fatalf("Label(%q): %v", mixed, err)
	}
	wantLower := expectedLabel(t, mixed)
	if gotMixed != wantLower {
		t.Fatalf("Label(%q) = %q; expected lower-case-before-hash result %q", mixed, gotMixed, wantLower)
	}
}

// Label is stable across repeated invocations from the same directory
// (no clock, no randomness, no environment leakage).
func TestLabel_StableAcrossInvocations(t *testing.T) {
	dir := t.TempDir()
	first, err := Label(dir)
	if err != nil {
		t.Fatalf("first Label(%q): %v", dir, err)
	}
	for i := 0; i < 5; i++ {
		again, err := Label(dir)
		if err != nil {
			t.Fatalf("repeat %d Label(%q): %v", i, dir, err)
		}
		if again != first {
			t.Fatalf("Label(%q): repeat %d = %q, want stable %q", dir, i, again, first)
		}
	}
}

// Label resolves relative paths to absolute before hashing, so a caller
// invoking with "./x" and with "/abs/.../x" from the same CWD gets the
// same label.
func TestLabel_RelativePathResolvesToAbsoluteBeforeHashing(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatalf("os.Chdir(%q): %v", parent, err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })

	rel, err := Label(base)
	if err != nil {
		t.Fatalf("Label(%q): %v", base, err)
	}
	abs, err := Label(dir)
	if err != nil {
		t.Fatalf("Label(%q): %v", dir, err)
	}
	if rel != abs {
		t.Fatalf("Label: relative %q gave %q, absolute %q gave %q — should match", base, rel, dir, abs)
	}
}

// Different directories must produce different labels; else --force and
// re-run refusal would conflate unrelated projects.
func TestLabel_DifferentDirectoriesProduceDifferentLabels(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	la, err := Label(a)
	if err != nil {
		t.Fatalf("Label(%q): %v", a, err)
	}
	lb, err := Label(b)
	if err != nil {
		t.Fatalf("Label(%q): %v", b, err)
	}
	if la == lb {
		t.Fatalf("Label: two different temp dirs (%q, %q) produced the same label %q", a, b, la)
	}
}

// FuzzLabel: no path input, however weird, may panic; every non-error
// output is exactly 12 lowercase hex characters.
func FuzzLabel(f *testing.F) {
	f.Add("/tmp/abc")
	f.Add(".")
	f.Add("relative/path")
	f.Add("/weird path with spaces")
	f.Add("")
	f.Fuzz(func(t *testing.T, p string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Label(%q) panicked: %v", p, r)
			}
		}()
		got, err := Label(p)
		if err != nil {
			return
		}
		if len(got) != 12 {
			t.Fatalf("Label(%q) = %q; want 12 chars, got %d", p, got, len(got))
		}
		for i, r := range got {
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
				t.Fatalf("Label(%q) = %q; char %d is %q, want lowercase hex", p, got, i, r)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Band 2: Fake collaborators (shared by all orchestrator tests below)
// ---------------------------------------------------------------------------

// fakeFly is a tiny in-memory flyclient.Client. Records every call; each
// method has a failure switch so tests can inject the exact failure-mode
// under test without plumbing stubs through a builder.
type fakeFly struct {
	mu sync.Mutex

	machines map[string][]flyclient.Machine
	calls    []string

	ensureAppErr error
	createErr    error
	destroyErr   error
	findErr      error

	nextID int
}

func newFakeFly() *fakeFly {
	return &fakeFly{machines: map[string][]flyclient.Machine{}}
}

func (f *fakeFly) record(tag string) {
	f.mu.Lock()
	f.calls = append(f.calls, tag)
	f.mu.Unlock()
}

func (f *fakeFly) EnsureApp(ctx context.Context, appName string) error {
	f.record("EnsureApp:" + appName)
	return f.ensureAppErr
}

func (f *fakeFly) Create(ctx context.Context, appName string, spec flyclient.SpawnSpec) (flyclient.Machine, error) {
	f.mu.Lock()
	f.nextID++
	id := fmt.Sprintf("m%03d", f.nextID)
	f.mu.Unlock()
	f.record("Create:" + appName + ":" + spec.Image + ":" + spec.Labels[LabelKey])
	if f.createErr != nil {
		return flyclient.Machine{}, f.createErr
	}
	m := flyclient.Machine{ID: id, State: "created", Region: spec.Region, Labels: spec.Labels}
	f.mu.Lock()
	f.machines[appName] = append(f.machines[appName], m)
	f.mu.Unlock()
	return m, nil
}

func (f *fakeFly) Get(ctx context.Context, appName, machineID string) (flyclient.Machine, error) {
	f.record("Get:" + appName + ":" + machineID)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines[appName] {
		if m.ID == machineID {
			return m, nil
		}
	}
	return flyclient.Machine{}, errors.New("not found")
}

func (f *fakeFly) WaitStarted(ctx context.Context, appName, machineID string) error {
	f.record("WaitStarted:" + appName + ":" + machineID)
	return nil
}

func (f *fakeFly) List(ctx context.Context, appName string) ([]flyclient.Machine, error) {
	f.record("List:" + appName)
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]flyclient.Machine(nil), f.machines[appName]...), nil
}

func (f *fakeFly) Start(ctx context.Context, appName, machineID string) error {
	f.record("Start:" + appName + ":" + machineID)
	return nil
}

func (f *fakeFly) Stop(ctx context.Context, appName, machineID string) error {
	f.record("Stop:" + appName + ":" + machineID)
	return nil
}

func (f *fakeFly) Destroy(ctx context.Context, appName, machineID string, force bool) error {
	f.record("Destroy:" + appName + ":" + machineID)
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.machines[appName]
	out := cur[:0]
	for _, m := range cur {
		if m.ID != machineID {
			out = append(out, m)
		}
	}
	f.machines[appName] = out
	return nil
}

func (f *fakeFly) FindByLabel(ctx context.Context, appName, key, value string) ([]flyclient.Machine, error) {
	f.record("FindByLabel:" + appName + ":" + key + "=" + value)
	if f.findErr != nil {
		return nil, f.findErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flyclient.Machine
	for _, m := range f.machines[appName] {
		if m.Labels[key] == value {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeFly) preseedMachine(appName, id, label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[appName] = append(f.machines[appName], flyclient.Machine{
		ID:     id,
		State:  "started",
		Labels: map[string]string{LabelKey: label},
	})
}

// fakeReg is a tiny in-memory registry.Registry.
type fakeReg struct {
	mu      sync.Mutex
	calls   []string
	pushed  []string
	pushErr error
}

func (f *fakeReg) Login(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Login")
	return nil
}

func (f *fakeReg) Push(ctx context.Context, tag string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "Push:"+tag)
	f.mu.Unlock()
	if f.pushErr != nil {
		return f.pushErr
	}
	f.mu.Lock()
	f.pushed = append(f.pushed, tag)
	f.mu.Unlock()
	return nil
}

func (f *fakeReg) List(ctx context.Context, prefix string) ([]registry.Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []registry.Image
	for _, t := range f.pushed {
		if prefix == "" || strings.HasPrefix(t, prefix) {
			out = append(out, registry.Image{Tag: t})
		}
	}
	return out, nil
}

func (f *fakeReg) Delete(ctx context.Context, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Delete:"+tag)
	return nil
}

// fakeExt is a tiny in-memory extbin.Runner. Records every invocation;
// tests can inject a per-binary response (exit code + error).
type fakeExt struct {
	mu       sync.Mutex
	calls    []extbin.Invocation
	response map[string]extResponse
}

type extResponse struct {
	exit int
	err  error
}

func newFakeExt() *fakeExt {
	return &fakeExt{response: map[string]extResponse{}}
}

func (f *fakeExt) Run(ctx context.Context, inv extbin.Invocation) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, extbin.Invocation{Name: inv.Name, Argv: append([]string(nil), inv.Argv...), Env: inv.Env})
	r := f.response[inv.Name]
	return r.exit, r.err
}

func (f *fakeExt) invocations(name string) []extbin.Invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []extbin.Invocation
	for _, c := range f.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// stageDockerfile stages a Dockerfile into a fresh t.TempDir() and
// returns the directory path. Mirrors the e2e-layer staging helper so
// unit tests exercise realistic inputs.
func stageDockerfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.19\nCMD [\"/bin/sh\"]\n"), 0o644); err != nil {
		t.Fatalf("stageDockerfile: %v", err)
	}
	return dir
}

// defaultOpts returns Options wired to fresh fakes that will all succeed.
// Tests override individual fields to induce specific failure modes.
func defaultOpts(t *testing.T, dir string) (Options, *fakeFly, *fakeReg, *fakeExt) {
	t.Helper()
	fly := newFakeFly()
	reg := &fakeReg{}
	ext := newFakeExt()
	ext.response["docker"] = extResponse{exit: 0}
	ext.response["flyctl"] = extResponse{exit: 0}
	return Options{
		BuildContextPath: dir,
		AppName:          "aa-apps",
		RegistryBase:     "registry.fly.io",
		Fly:              fly,
		Registry:         reg,
		ExtBin:           ext,
	}, fly, reg, ext
}

// ---------------------------------------------------------------------------
// Band 2: Run orchestrator tests
// ---------------------------------------------------------------------------

// Happy path: all four stages run in order — at least one docker build
// invocation, one push, one create, one flyctl attach. No destroys.
func TestRun_HappyPath_RunsAllFourStagesInOrderAndDestroysNothing(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)

	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("happy path: unexpected error: %v", err)
	}

	if len(ext.invocations("docker")) < 1 {
		t.Fatalf("happy path: expected ≥1 docker invocation for build, got %d", len(ext.invocations("docker")))
	}
	pushed := false
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("happy path: expected a Push call, got %+v", reg.calls)
	}
	creates, destroys := 0, 0
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") {
			creates++
		}
		if strings.HasPrefix(c, "Destroy:") {
			destroys++
		}
	}
	if creates != 1 {
		t.Fatalf("happy path: expected 1 Create, got %d; fly.calls=%+v", creates, fly.calls)
	}
	if destroys != 0 {
		t.Fatalf("happy path: expected 0 Destroys, got %d; fly.calls=%+v", destroys, fly.calls)
	}
	if got := len(ext.invocations("flyctl")); got != 1 {
		t.Fatalf("happy path: expected exactly 1 flyctl invocation for attach, got %d", got)
	}
}

// --force with one pre-existing machine: destroy ordered AFTER push
// success, BEFORE Create (ADR-4).
func TestRun_ForceReplaceOrdersDestroyAfterPushBeforeSpawn(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, _ := defaultOpts(t, dir)
	opts.Force = true

	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	label := hex.EncodeToString(h[:])[:12]
	fly.preseedMachine(opts.AppName, "m-old", label)

	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("--force happy path: unexpected error: %v", err)
	}

	destroyIdx, createIdx := -1, -1
	for i, c := range fly.calls {
		if c == "Destroy:"+opts.AppName+":m-old" && destroyIdx == -1 {
			destroyIdx = i
		}
		if strings.HasPrefix(c, "Create:") && createIdx == -1 {
			createIdx = i
		}
	}
	if destroyIdx < 0 {
		t.Fatalf("--force: expected Destroy of preseeded m-old; fly.calls=%+v", fly.calls)
	}
	if createIdx < 0 {
		t.Fatalf("--force: expected a Create for the new machine; fly.calls=%+v", fly.calls)
	}
	if destroyIdx >= createIdx {
		t.Fatalf("--force: expected Destroy (idx %d) BEFORE Create (idx %d) per ADR-4; fly.calls=%+v",
			destroyIdx, createIdx, fly.calls)
	}
	pushed := false
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			pushed = true
		}
	}
	if !pushed {
		t.Fatalf("--force: expected Push before destroy; reg.calls=%+v", reg.calls)
	}
}

// Re-run without --force, pre-existing machine: refuse with error naming
// the existing id; do no build, push, destroy, or create.
func TestRun_RerunWithoutForceRefusesWithoutTouchingAnything(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)

	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	label := hex.EncodeToString(h[:])[:12]
	fly.preseedMachine(opts.AppName, "m-existing", label)

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("re-run without --force: expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "m-existing") {
		t.Fatalf("re-run without --force: expected error to name the existing machine id, got %q", err.Error())
	}
	if got := len(ext.invocations("docker")); got != 0 {
		t.Fatalf("re-run without --force: expected 0 docker invocations, got %d", got)
	}
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			t.Fatalf("re-run without --force: expected NO Push, got %q in reg.calls=%+v", c, reg.calls)
		}
	}
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Destroy:") || strings.HasPrefix(c, "Create:") {
			t.Fatalf("re-run without --force: expected NO Destroy/Create, got %q in fly.calls=%+v", c, fly.calls)
		}
	}
}

// Attach failure: destroy the machine; do NOT delete the image
// (asymmetric cleanup per resolved intent).
func TestRun_AttachFailureDestroysMachineAndRetainsImage(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)
	ext.response["flyctl"] = extResponse{exit: 1}

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("attach failure: expected non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "attach") {
		t.Fatalf("attach failure: expected error to name %q stage, got %q", "attach", err.Error())
	}
	destroys := 0
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Destroy:") {
			destroys++
		}
	}
	if destroys == 0 {
		t.Fatalf("attach failure: expected Destroy of the spawned machine, got fly.calls=%+v", fly.calls)
	}
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Delete:") {
			t.Fatalf("attach failure: expected image retained, got unexpected %q in reg.calls=%+v", c, reg.calls)
		}
	}
}

// Build failure: no push, no spawn, no attach. Nothing on the remote.
func TestRun_BuildFailureShortCircuitsTheChain(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)
	ext.response["docker"] = extResponse{exit: 1}

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("build failure: expected non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "build") {
		t.Fatalf("build failure: expected error to name %q stage, got %q", "build", err.Error())
	}
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			t.Fatalf("build failure: expected NO Push, got %+v", reg.calls)
		}
	}
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") {
			t.Fatalf("build failure: expected NO Create, got %+v", fly.calls)
		}
	}
	if got := len(ext.invocations("flyctl")); got != 0 {
		t.Fatalf("build failure: expected NO flyctl invocations, got %d", got)
	}
}

// Push failure: build ran, push errored; no spawn, no attach; no image
// landed so there is nothing to retain or delete.
func TestRun_PushFailureLeavesNoSpawnNoAttach(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)
	reg.pushErr = errors.New("429 rate limited")

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("push failure: expected non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "push") {
		t.Fatalf("push failure: expected error to name %q stage, got %q", "push", err.Error())
	}
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") {
			t.Fatalf("push failure: expected NO Create, got %+v", fly.calls)
		}
	}
	if got := len(ext.invocations("flyctl")); got != 0 {
		t.Fatalf("push failure: expected NO flyctl invocations, got %d", got)
	}
	if len(reg.pushed) != 0 {
		t.Fatalf("push failure: expected 0 landed pushes, got %d", len(reg.pushed))
	}
}

// Spawn failure: image remains published (not deleted); no attach; no
// machine created, so no destroy either.
func TestRun_SpawnFailureKeepsImageAndRunsNoAttach(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, reg, ext := defaultOpts(t, dir)
	fly.createErr = errors.New("quota exhausted")

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("spawn failure: expected non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "spawn") {
		t.Fatalf("spawn failure: expected error to name %q stage, got %q", "spawn", err.Error())
	}
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Delete:") {
			t.Fatalf("spawn failure: expected image retained, got unexpected %q in reg.calls=%+v", c, reg.calls)
		}
	}
	if got := len(ext.invocations("flyctl")); got != 0 {
		t.Fatalf("spawn failure: expected NO flyctl invocations, got %d", got)
	}
}

// Multi-match under --force: a prior failed replace left TWO machines
// tagged with the same identity label. Next --force destroys BOTH before
// spawning fresh.
func TestRun_ForceDestroysAllMatchesWhenFindByLabelReturnsMultiple(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, _, _ := defaultOpts(t, dir)
	opts.Force = true

	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	label := hex.EncodeToString(h[:])[:12]
	fly.preseedMachine(opts.AppName, "m-old-A", label)
	fly.preseedMachine(opts.AppName, "m-old-B", label)

	if err := Run(context.Background(), opts); err != nil {
		t.Fatalf("--force multi-match: unexpected error: %v", err)
	}

	destroyedA, destroyedB := false, false
	for _, c := range fly.calls {
		if c == "Destroy:"+opts.AppName+":m-old-A" {
			destroyedA = true
		}
		if c == "Destroy:"+opts.AppName+":m-old-B" {
			destroyedB = true
		}
	}
	if !destroyedA || !destroyedB {
		t.Fatalf("--force multi-match: expected BOTH m-old-A and m-old-B destroyed, got destroyedA=%v destroyedB=%v; fly.calls=%+v",
			destroyedA, destroyedB, fly.calls)
	}
	created := false
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") {
			created = true
		}
	}
	if !created {
		t.Fatalf("--force multi-match: expected a fresh Create after destroying both matches, got fly.calls=%+v", fly.calls)
	}
}

// Re-run refusal with multi-match: lists ALL matching machine ids in
// the refusal error.
func TestRun_RerunRefusalWithMultiMatchListsAllMachineIDs(t *testing.T) {
	dir := stageDockerfile(t)
	opts, fly, _, _ := defaultOpts(t, dir)

	abs, _ := filepath.Abs(dir)
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	label := hex.EncodeToString(h[:])[:12]
	fly.preseedMachine(opts.AppName, "m-dup-A", label)
	fly.preseedMachine(opts.AppName, "m-dup-B", label)

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("multi-match without --force: expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "m-dup-A") || !strings.Contains(err.Error(), "m-dup-B") {
		t.Fatalf("multi-match refusal: expected error to list BOTH machine ids, got %q", err.Error())
	}
}

// No Dockerfile at <path>: exit early with an error naming the missing
// Dockerfile; nothing else runs.
func TestRun_NoDockerfileAtPathRefusesEarly(t *testing.T) {
	dir := t.TempDir() // no Dockerfile written.
	opts, fly, reg, ext := defaultOpts(t, dir)

	err := Run(context.Background(), opts)
	if err == nil {
		t.Fatalf("no Dockerfile: expected non-nil error, got nil")
	}
	if !strings.Contains(err.Error(), "Dockerfile") {
		t.Fatalf("no Dockerfile: expected error to name %q, got %q", "Dockerfile", err.Error())
	}
	if got := len(ext.invocations("docker")); got != 0 {
		t.Fatalf("no Dockerfile: expected NO docker invocations, got %d", got)
	}
	for _, c := range reg.calls {
		if strings.HasPrefix(c, "Push:") {
			t.Fatalf("no Dockerfile: expected NO Push, got reg.calls=%+v", reg.calls)
		}
	}
	for _, c := range fly.calls {
		if strings.HasPrefix(c, "Create:") {
			t.Fatalf("no Dockerfile: expected NO Create, got fly.calls=%+v", fly.calls)
		}
	}
}
