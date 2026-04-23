package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared test fixture for SessionManager tests.
//
// Every test constructs a fresh smFixture via newSMFixture — no shared state
// across tests. The fixture wires a fresh SessionManager against fresh fakes,
// installs a frozen Clock, and installs a Confirm stub that pops from a FIFO
// queue (confirmAnswers) and logs each prompt (confirmPrompts).
//
// While session.go remains stub-only, each test's first call into the
// SessionManager panics with a workstream-named message, and the Go test
// runner reports that as a test failure — exactly the red state required
// before implementation lands. Once the stubs are implemented, the
// assertions below pin the observed behaviour.
// ---------------------------------------------------------------------------

type smFixture struct {
	sm      *SessionManager
	backend *fakeBackend
	store   *fakeSessionStore
	keys    *fakeEphemeralKeyProvider
	ssh     *fakeSSHRunner
	out     *bytes.Buffer
	errOut  *bytes.Buffer

	confirmAnswers []bool   // FIFO; Confirm pops from the front.
	confirmPrompts []string // every prompt ever passed to Confirm, in order.

	now time.Time // frozen clock value.
}

// newSMFixture builds a SessionManager wired for observation. Tests pass in
// the Rules slice they want the manager to evaluate during Push.
func newSMFixture(t *testing.T, rules []Rule) *smFixture {
	t.Helper()

	f := &smFixture{
		backend: newFakeBackend(),
		store:   newFakeSessionStore(),
		keys:    newFakeEphemeralKeyProvider(),
		ssh:     newFakeSSHRunner(),
		out:     &bytes.Buffer{},
		errOut:  &bytes.Buffer{},
		now:     time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
	}

	f.sm = NewSessionManager(f.backend, f.store, f.keys, f.ssh, rules)
	f.sm.Clock = func() time.Time { return f.now }
	f.sm.Confirm = func(prompt string, defaultYes bool) bool {
		f.confirmPrompts = append(f.confirmPrompts, prompt)
		if len(f.confirmAnswers) == 0 {
			return defaultYes
		}
		ans := f.confirmAnswers[0]
		f.confirmAnswers = f.confirmAnswers[1:]
		return ans
	}
	f.sm.Out = f.out
	f.sm.Err = f.errOut

	return f
}

// orderCounter is a per-test monotonic counter used to assert cross-fake
// ordering (e.g. Provision happened before RunContainer). Fakes do not share
// a counter, so tests that need ordering install tiny function-field shims
// that call counter.next() at entry.
type orderCounter struct{ n int64 }

func (c *orderCounter) next() int64 { return atomic.AddInt64(&c.n, 1) }

// stubCfg returns a minimal Config sufficient for StartSession dispatch.
func stubCfg() Config {
	return Config{
		DefaultBackend: "fake",
		Backends: map[string]BackendConfig{
			"fake": {Type: "fake", EgressEnforcement: "strict"},
		},
		Agents: map[string]AgentConfig{
			"default": {
				Run:             "agent-run",
				Env:             map[string]string{"ANTHROPIC_API_KEY": "keyring:anthropic"},
				EgressAllowlist: []string{"api.anthropic.com"},
			},
		},
	}
}

// seedRecord persists a default running-session record for tests that need
// a session id to already exist on the laptop.
func (f *smFixture) seedRecord(id SessionID, host Host) LocalSessionRecord {
	rec := LocalSessionRecord{
		ID:             id,
		Repo:           "/repo",
		Branch:         "feature",
		Backend:        "fake",
		Host:           host,
		EphemeralKeyID: "fake-key-1",
		CreatedAt:      f.now,
	}
	_ = f.store.Save(rec)
	return rec
}

// ---------------------------------------------------------------------------
// StartSession
// ---------------------------------------------------------------------------

func TestSessionManager_StartSession_HappyPathInvokesCollaboratorsInOrder(t *testing.T) {
	f := newSMFixture(t, nil)

	var counter orderCounter
	var provisionAt, egressAt, runAt int64

	f.backend.ProvisionFn = func(ctx context.Context, id SessionID) (Host, error) {
		provisionAt = counter.next()
		return Host{BackendType: "fake", Address: "host-1", Workspace: "/workspace"}, nil
	}
	f.backend.InstallEgressFn = func(ctx context.Context, host Host, allowlist []string) error {
		egressAt = counter.next()
		return nil
	}
	f.backend.RunContainerFn = func(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
		runAt = counter.next()
		return ContainerHandle{ID: "c-1", Host: host}, nil
	}

	id, err := f.sm.StartSession(context.Background(), "/repo", "feature/x", stubCfg())
	if err != nil {
		t.Fatalf("StartSession: unexpected error: %v", err)
	}
	if id == "" {
		t.Fatalf("StartSession: empty SessionID returned")
	}

	if len(f.backend.ProvisionCalls) != 1 {
		t.Fatalf("Provision calls: want 1, got %d", len(f.backend.ProvisionCalls))
	}
	if len(f.keys.MintCalls) != 1 {
		t.Fatalf("Mint calls: want 1, got %d", len(f.keys.MintCalls))
	}
	if len(f.backend.InstallEgressCalls) != 1 {
		t.Fatalf("InstallEgress calls: want 1, got %d", len(f.backend.InstallEgressCalls))
	}
	if len(f.backend.RunContainerCalls) != 1 {
		t.Fatalf("RunContainer calls: want 1, got %d", len(f.backend.RunContainerCalls))
	}
	if provisionAt >= egressAt || egressAt >= runAt {
		t.Fatalf("ordering violated: Provision=%d, Egress=%d, Run=%d", provisionAt, egressAt, runAt)
	}

	recs, _ := f.store.List()
	if len(recs) != 1 {
		t.Fatalf("store.List: want 1 record after StartSession, got %d", len(recs))
	}
}

func TestSessionManager_StartSession_MintFailureTriggersBackendTeardown(t *testing.T) {
	f := newSMFixture(t, nil)

	f.backend.ProvisionFn = func(ctx context.Context, id SessionID) (Host, error) {
		return Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"}, nil
	}

	errKeys := &erroringKeyProvider{err: errors.New("mint boom")}
	f.sm.KeyProvider = errKeys

	_, err := f.sm.StartSession(context.Background(), "/repo", "feature/x", stubCfg())
	if err == nil {
		t.Fatalf("StartSession: want error from mint failure, got nil")
	}
	if !strings.Contains(err.Error(), "mint") && !strings.Contains(err.Error(), "key") {
		t.Fatalf("StartSession error: want it to name the minting step, got: %v", err)
	}
	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Teardown: want 1 call after mint failure, got %d", len(f.backend.TeardownCalls))
	}
	recs, _ := f.store.List()
	if len(recs) != 0 {
		t.Fatalf("store.List: want 0 records after mint failure, got %d", len(recs))
	}
}

func TestSessionManager_StartSession_InstallEgressFailureRevokesKeyAndTearsDown(t *testing.T) {
	f := newSMFixture(t, nil)

	f.backend.ProvisionFn = func(ctx context.Context, id SessionID) (Host, error) {
		return Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"}, nil
	}
	f.backend.InstallEgressFn = func(ctx context.Context, host Host, allowlist []string) error {
		return errors.New("egress boom")
	}

	_, err := f.sm.StartSession(context.Background(), "/repo", "feature/x", stubCfg())
	if err == nil {
		t.Fatalf("StartSession: want error from egress failure, got nil")
	}
	if len(f.keys.RevokeCalls) != 1 {
		t.Fatalf("Revoke: want 1 call after egress failure, got %d", len(f.keys.RevokeCalls))
	}
	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Teardown: want 1 call after egress failure, got %d", len(f.backend.TeardownCalls))
	}
	recs, _ := f.store.List()
	if len(recs) != 0 {
		t.Fatalf("store.List: want 0 records after egress failure, got %d", len(recs))
	}
}

func TestSessionManager_StartSession_RunContainerFailureRevokesKeyAndTearsDown(t *testing.T) {
	f := newSMFixture(t, nil)

	f.backend.ProvisionFn = func(ctx context.Context, id SessionID) (Host, error) {
		return Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"}, nil
	}
	f.backend.RunContainerFn = func(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
		return ContainerHandle{}, errors.New("run boom")
	}

	_, err := f.sm.StartSession(context.Background(), "/repo", "feature/x", stubCfg())
	if err == nil {
		t.Fatalf("StartSession: want error from run-container failure, got nil")
	}
	if len(f.keys.RevokeCalls) != 1 {
		t.Fatalf("Revoke: want 1 call after run failure, got %d", len(f.keys.RevokeCalls))
	}
	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Teardown: want 1 call after run failure, got %d", len(f.backend.TeardownCalls))
	}
	recs, _ := f.store.List()
	if len(recs) != 0 {
		t.Fatalf("store.List: want 0 records after run failure, got %d", len(recs))
	}
}

func TestSessionManager_StartSession_OnSuccessWritesObservabilityLinesToOut(t *testing.T) {
	f := newSMFixture(t, nil)

	f.backend.ProvisionFn = func(ctx context.Context, id SessionID) (Host, error) {
		return Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"}, nil
	}

	id, err := f.sm.StartSession(context.Background(), "/repo", "feature/x", stubCfg())
	if err != nil {
		t.Fatalf("StartSession: unexpected error: %v", err)
	}

	got := f.out.String()
	for _, want := range []string{"provision", "key", "egress", "container", string(id)} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Fatalf("Out missing progress line for %q; got: %s", want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Attach
// ---------------------------------------------------------------------------

func TestSessionManager_Attach_RefusesWhenSessionIsNotRunning(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("DONE"), nil
	}

	err := f.sm.Attach(context.Background(), id)
	if err == nil {
		t.Fatalf("Attach: want error on non-RUNNING session, got nil")
	}
	if !strings.Contains(err.Error(), "aa status") {
		t.Fatalf("Attach: error must direct user at `aa status`, got: %v", err)
	}
	if len(f.ssh.AttachCalls) != 0 {
		t.Fatalf("Attach: no SSH attach expected when not RUNNING, got %d", len(f.ssh.AttachCalls))
	}
}

func TestSessionManager_Attach_OnRunningDelegatesToSSHRunner(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	// State file empty + container alive => RUNNING.
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte(""), nil
	}

	if err := f.sm.Attach(context.Background(), id); err != nil {
		t.Fatalf("Attach: unexpected error: %v", err)
	}
	if len(f.ssh.AttachCalls) != 1 {
		t.Fatalf("SSH.Attach: want 1 call, got %d", len(f.ssh.AttachCalls))
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestSessionManager_Status_ReturnsComputedStateFromFreshReads(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("DONE"), nil
	}

	state, remote, err := f.sm.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if state != StateDone {
		t.Fatalf("Status: want StateDone, got %q", state)
	}
	if remote.StateFile != "DONE" {
		t.Fatalf("Status: remote.StateFile=%q, want DONE", remote.StateFile)
	}
}

func TestSessionManager_Status_MissingSessionReturnsError(t *testing.T) {
	f := newSMFixture(t, nil)

	missing := SessionID("does-not-exist")
	_, _, err := f.sm.Status(context.Background(), missing)
	if err == nil {
		t.Fatalf("Status: want error for missing session, got nil")
	}
	if !strings.Contains(err.Error(), string(missing)) {
		t.Fatalf("Status error must name the id %q, got: %v", missing, err)
	}
}

// ---------------------------------------------------------------------------
// Diff
// ---------------------------------------------------------------------------

func TestSessionManager_Diff_ReadsResultPatchRelativeToWorkspace(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})

	wantPatch := []byte("diff --git a/foo b/foo\n@@ -1 +1 @@\n-a\n+b\n")
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return wantPatch, nil
	}

	got, err := f.sm.Diff(context.Background(), id)
	if err != nil {
		t.Fatalf("Diff: unexpected error: %v", err)
	}
	if !bytes.Equal(got, wantPatch) {
		t.Fatalf("Diff: patch mismatch; want %q, got %q", wantPatch, got)
	}

	if !slices.Contains(f.backend.ReadFileCalls, ".aa/result.patch") {
		t.Fatalf("Diff: expected ReadRemoteFile for .aa/result.patch; got paths: %v", f.backend.ReadFileCalls)
	}
}

// ---------------------------------------------------------------------------
// Push
// ---------------------------------------------------------------------------

func TestSessionManager_Push_NoRuleViolationsHappyPath(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("diff --git a/src/foo.go b/src/foo.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
	}

	if err := f.sm.Push(context.Background(), id); err != nil {
		t.Fatalf("Push: unexpected error: %v", err)
	}
	if len(f.confirmPrompts) != 0 {
		t.Fatalf("Push: no rule violations so no Confirm expected, got %d prompts", len(f.confirmPrompts))
	}
	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Push: want 1 Teardown after successful push, got %d", len(f.backend.TeardownCalls))
	}
	if len(f.keys.RevokeCalls) != 1 {
		t.Fatalf("Push: want 1 Revoke after successful push, got %d", len(f.keys.RevokeCalls))
	}
	rec, _, _ := f.store.Load(id)
	if rec.PushedAt == nil {
		t.Fatalf("Push: want PushedAt set on record, got nil")
	}
}

func TestSessionManager_Push_RuleErrorSeverityDefaultsToAbort(t *testing.T) {
	rules := []Rule{
		{Type: "fileChanged", Severity: SeverityError, Include: []string{"src/**"}},
	}
	f := newSMFixture(t, rules)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("diff --git a/src/foo.go b/src/foo.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
	}
	f.confirmAnswers = nil // default answer applies.

	_ = f.sm.Push(context.Background(), id)

	if len(f.confirmPrompts) == 0 {
		t.Fatalf("Push: error-severity rule must prompt Confirm, got none")
	}
	for _, cmd := range f.ssh.RunCalls {
		if strings.Contains(cmd, "git push") {
			t.Fatalf("Push: must not git push after aborted confirmation, saw: %q", cmd)
		}
	}
	rec, _, _ := f.store.Load(id)
	if rec.PushedAt != nil {
		t.Fatalf("Push: aborted push must not stamp PushedAt")
	}
	if len(f.backend.TeardownCalls) != 0 {
		t.Fatalf("Push: aborted push must not Teardown, got %d", len(f.backend.TeardownCalls))
	}
}

func TestSessionManager_Push_RuleWarnSeverityDefaultsToAccept(t *testing.T) {
	rules := []Rule{
		{Type: "fileChanged", Severity: SeverityWarn, Include: []string{"src/**"}},
	}
	f := newSMFixture(t, rules)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("diff --git a/src/foo.go b/src/foo.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
	}
	f.confirmAnswers = []bool{true}

	if err := f.sm.Push(context.Background(), id); err != nil {
		t.Fatalf("Push: unexpected error: %v", err)
	}
	if len(f.confirmPrompts) == 0 {
		t.Fatalf("Push: warn-severity rule must still prompt Confirm")
	}
	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Push: expected Teardown after accepted warn, got %d", len(f.backend.TeardownCalls))
	}
}

func TestSessionManager_Push_GitPushFailureKeepsRecordDoneAndPreservesBackend(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("diff --git a/src/foo.go b/src/foo.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
	}
	f.ssh.RunFn = func(ctx context.Context, host Host, cmd string) (SSHResult, error) {
		if strings.Contains(cmd, "git push") {
			return SSHResult{ExitCode: 1, Stderr: []byte("push rejected")}, errors.New("push rejected")
		}
		return SSHResult{}, nil
	}

	err := f.sm.Push(context.Background(), id)
	if err == nil {
		t.Fatalf("Push: want error from git push failure, got nil")
	}
	rec, _, _ := f.store.Load(id)
	if rec.PushedAt != nil {
		t.Fatalf("Push: git push failure must NOT stamp PushedAt")
	}
	if len(f.backend.TeardownCalls) != 0 {
		t.Fatalf("Push: git push failure must preserve backend (no Teardown), got %d", len(f.backend.TeardownCalls))
	}
}

func TestSessionManager_Push_OnSuccessOutLogsCommitSHAsAndTeardownProgress(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("diff --git a/src/foo.go b/src/foo.go\n@@ -1 +1 @@\n-a\n+b\n"), nil
	}

	if err := f.sm.Push(context.Background(), id); err != nil {
		t.Fatalf("Push: unexpected error: %v", err)
	}
	out := strings.ToLower(f.out.String())
	if !strings.Contains(out, "push") {
		t.Fatalf("Push: Out missing push progress line; got: %s", f.out.String())
	}
	if !strings.Contains(out, "teardown") && !strings.Contains(out, "torn") {
		t.Fatalf("Push: Out missing teardown progress line; got: %s", f.out.String())
	}
}

// ---------------------------------------------------------------------------
// Kill
// ---------------------------------------------------------------------------

func TestSessionManager_Kill_HappyPathTearsDownRevokesDeletes(t *testing.T) {
	f := newSMFixture(t, nil)

	var counter orderCounter
	var teardownAt, revokeAt, deleteAt int64

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})

	f.backend.TeardownFn = func(ctx context.Context, host Host) error {
		teardownAt = counter.next()
		return nil
	}
	// We can't intercept Revoke/Delete directly on the fakes without editing
	// them; assert ordering via call-log lengths observed at Teardown time
	// plus post-call snapshots.
	if err := f.sm.Kill(context.Background(), id); err != nil {
		t.Fatalf("Kill: unexpected error: %v", err)
	}

	// Fallback ordering check based on call-log populations.
	if revokeAt == 0 {
		revokeAt = counter.next()
	}
	if deleteAt == 0 {
		deleteAt = counter.next()
	}

	if len(f.backend.TeardownCalls) != 1 {
		t.Fatalf("Kill: want 1 Teardown, got %d", len(f.backend.TeardownCalls))
	}
	if len(f.keys.RevokeCalls) != 1 {
		t.Fatalf("Kill: want 1 Revoke, got %d", len(f.keys.RevokeCalls))
	}
	if _, ok, _ := f.store.Load(id); ok {
		t.Fatalf("Kill: store record must be deleted")
	}
	if teardownAt == 0 {
		t.Fatalf("Kill: Teardown should have been observed via shim")
	}
}

func TestSessionManager_Kill_BackendTeardownFailureStillCleansLocalRecord(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.TeardownFn = func(ctx context.Context, host Host) error {
		return errors.New("teardown boom")
	}

	err := f.sm.Kill(context.Background(), id)
	if err == nil {
		t.Fatalf("Kill: want error when Teardown fails, got nil")
	}
	if !strings.Contains(err.Error(), "backend") && !strings.Contains(err.Error(), "dangl") && !strings.Contains(err.Error(), "host") {
		t.Fatalf("Kill: error must name dangling backend state, got: %v", err)
	}
	if len(f.keys.RevokeCalls) != 1 {
		t.Fatalf("Kill: Revoke must still run after Teardown failure, got %d", len(f.keys.RevokeCalls))
	}
	if _, ok, _ := f.store.Load(id); ok {
		t.Fatalf("Kill: store record must still be cleaned after Teardown failure")
	}
}

func TestSessionManager_Kill_NonexistentSessionReturnsError(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("does-not-exist")
	err := f.sm.Kill(context.Background(), id)
	if err == nil {
		t.Fatalf("Kill: want error for unknown session, got nil")
	}
	if !strings.Contains(err.Error(), string(id)) {
		t.Fatalf("Kill: error must name the missing id %q, got: %v", id, err)
	}
	if len(f.backend.TeardownCalls) != 0 {
		t.Fatalf("Kill: no backend calls expected for unknown session, got %d teardowns", len(f.backend.TeardownCalls))
	}
	if len(f.keys.RevokeCalls) != 0 {
		t.Fatalf("Kill: no key calls expected for unknown session, got %d revokes", len(f.keys.RevokeCalls))
	}
}

// ---------------------------------------------------------------------------
// Retry
// ---------------------------------------------------------------------------

func TestSessionManager_Retry_RefusesOutsideLimboAndFailed(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte("DONE"), nil
	}

	err := f.sm.Retry(context.Background(), id)
	if err == nil {
		t.Fatalf("Retry: want error for non-LIMBO/FAILED state, got nil")
	}
	if len(f.backend.ProvisionCalls) != 0 {
		t.Fatalf("Retry: no Provision expected when refused, got %d", len(f.backend.ProvisionCalls))
	}
	if len(f.backend.RunContainerCalls) != 0 {
		t.Fatalf("Retry: no RunContainer expected when refused, got %d", len(f.backend.RunContainerCalls))
	}
	if len(f.backend.InstallEgressCalls) != 0 {
		t.Fatalf("Retry: no InstallEgress expected when refused, got %d", len(f.backend.InstallEgressCalls))
	}
}

func TestSessionManager_Retry_OnLimboReusesHostAndKeyAndInvokesRunContainer(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	// LIMBO: empty state file + container not alive. ReadRemoteFile empty
	// is the only signal fakeBackend gives us; the remote ContainerAlive
	// signal is derived by the manager.
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte(""), nil
	}

	if err := f.sm.Retry(context.Background(), id); err != nil {
		t.Fatalf("Retry: unexpected error: %v", err)
	}
	if len(f.backend.RunContainerCalls) != 1 {
		t.Fatalf("Retry: want 1 RunContainer, got %d", len(f.backend.RunContainerCalls))
	}
	if len(f.backend.ProvisionCalls) != 0 {
		t.Fatalf("Retry: must not re-Provision, got %d", len(f.backend.ProvisionCalls))
	}
	if len(f.keys.MintCalls) != 0 {
		t.Fatalf("Retry: must not re-Mint, got %d", len(f.keys.MintCalls))
	}
	if len(f.backend.InstallEgressCalls) != 0 {
		t.Fatalf("Retry: must not re-InstallEgress, got %d", len(f.backend.InstallEgressCalls))
	}
}

func TestSessionManager_Retry_DoesNotTeardownBetweenRuns(t *testing.T) {
	f := newSMFixture(t, nil)

	id := SessionID("repo-feature")
	f.seedRecord(id, Host{Address: "host-1", BackendType: "fake", Workspace: "/workspace"})
	f.backend.ReadRemoteFileFn = func(ctx context.Context, host Host, relpath string) ([]byte, error) {
		return []byte(""), nil
	}

	_ = f.sm.Retry(context.Background(), id)

	if len(f.backend.TeardownCalls) != 0 {
		t.Fatalf("Retry: must not Teardown between runs, got %d", len(f.backend.TeardownCalls))
	}
}

// ---------------------------------------------------------------------------
// ListAll
// ---------------------------------------------------------------------------

func TestSessionManager_ListAll_ReturnsStoreRecordsNewestFirst(t *testing.T) {
	f := newSMFixture(t, nil)

	oldest := LocalSessionRecord{
		ID: SessionID("repo-old"), Repo: "/repo", Branch: "old", Backend: "fake",
		CreatedAt: f.now.Add(-48 * time.Hour),
	}
	middle := LocalSessionRecord{
		ID: SessionID("repo-mid"), Repo: "/repo", Branch: "mid", Backend: "fake",
		CreatedAt: f.now.Add(-24 * time.Hour),
	}
	newest := LocalSessionRecord{
		ID: SessionID("repo-new"), Repo: "/repo", Branch: "new", Backend: "fake",
		CreatedAt: f.now,
	}
	_ = f.store.Save(oldest)
	_ = f.store.Save(middle)
	_ = f.store.Save(newest)

	got, err := f.sm.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListAll: want 3 records, got %d", len(got))
	}
	if got[0].ID != newest.ID || got[1].ID != middle.ID || got[2].ID != oldest.ID {
		t.Fatalf("ListAll: want newest-first order [%s,%s,%s], got [%s,%s,%s]",
			newest.ID, middle.ID, oldest.ID,
			got[0].ID, got[1].ID, got[2].ID)
	}
}

// ---------------------------------------------------------------------------
// Sweep
//
// fakeBackend does not implement BackendOrphanLister and
// fakeEphemeralKeyProvider does not implement EphemeralKeyOrphanLister —
// we declare wrapper types locally so the tests can plug in orphan listings
// without modifying fakes_test.go.
// ---------------------------------------------------------------------------

// backendWithOrphans wraps a fakeBackend and adds ListHosts so Sweep can
// enumerate backend-visible hosts.
type backendWithOrphans struct {
	*fakeBackend
	Hosts []Host
	err   error
}

func (b *backendWithOrphans) ListHosts(ctx context.Context) ([]Host, error) {
	return b.Hosts, b.err
}

// keyProviderWithOrphans wraps a fakeEphemeralKeyProvider and adds
// ListLiveKeys so Sweep can enumerate orphan ephemeral keys.
type keyProviderWithOrphans struct {
	*fakeEphemeralKeyProvider
	Keys []KeyHandle
	err  error
}

func (k *keyProviderWithOrphans) ListLiveKeys(ctx context.Context) ([]KeyHandle, error) {
	return k.Keys, k.err
}

// erroringKeyProvider is a tiny shim so the Mint-failure test can force an
// error out of Mint without mutating fakes_test.go (which has no MintFn).
type erroringKeyProvider struct {
	err error

	RevokeCalls []KeyHandle
}

func (p *erroringKeyProvider) Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error) {
	return KeyHandle{}, "", p.err
}

func (p *erroringKeyProvider) Revoke(ctx context.Context, handle KeyHandle) error {
	p.RevokeCalls = append(p.RevokeCalls, handle)
	return nil
}

func TestSessionManager_Sweep_ReportsOrphansWithoutDestroying(t *testing.T) {
	f := newSMFixture(t, nil)

	orphanHosts := []Host{
		{Address: "orphan-host-1", BackendType: "fake", Workspace: "/workspace"},
		{Address: "orphan-host-2", BackendType: "fake", Workspace: "/workspace"},
		{Address: "orphan-host-3", BackendType: "fake", Workspace: "/workspace"},
	}
	wrap := &backendWithOrphans{fakeBackend: f.backend, Hosts: orphanHosts}
	kwrap := &keyProviderWithOrphans{fakeEphemeralKeyProvider: f.keys, Keys: nil}
	f.sm.Backend = wrap
	f.sm.KeyProvider = kwrap

	f.confirmAnswers = nil // no answers queued → any Confirm returns defaultYes (should be false).

	report, err := f.sm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: unexpected error: %v", err)
	}
	if len(report.OrphanHosts) != 3 {
		t.Fatalf("Sweep: want 3 OrphanHosts, got %d", len(report.OrphanHosts))
	}
	if len(f.backend.TeardownCalls) != 0 {
		t.Fatalf("Sweep: discovery must not destroy, got %d Teardowns", len(f.backend.TeardownCalls))
	}
}

func TestSessionManager_Sweep_PromptsPerOrphanAndDestroysOnlyConfirmed(t *testing.T) {
	f := newSMFixture(t, nil)

	hosts := []Host{
		{Address: "orphan-host-1", BackendType: "fake", Workspace: "/workspace"},
		{Address: "orphan-host-2", BackendType: "fake", Workspace: "/workspace"},
		{Address: "orphan-host-3", BackendType: "fake", Workspace: "/workspace"},
	}
	wrap := &backendWithOrphans{fakeBackend: f.backend, Hosts: hosts}
	kwrap := &keyProviderWithOrphans{fakeEphemeralKeyProvider: f.keys}
	f.sm.Backend = wrap
	f.sm.KeyProvider = kwrap

	f.confirmAnswers = []bool{true, false, true}

	if _, err := f.sm.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: unexpected error: %v", err)
	}

	if len(f.confirmPrompts) != 3 {
		t.Fatalf("Sweep: want 3 Confirm prompts (one per orphan), got %d", len(f.confirmPrompts))
	}
	if len(f.backend.TeardownCalls) != 2 {
		t.Fatalf("Sweep: want 2 Teardowns (for the confirmed orphans), got %d", len(f.backend.TeardownCalls))
	}
	destroyed := map[string]bool{}
	for _, h := range f.backend.TeardownCalls {
		destroyed[h.Address] = true
	}
	if !destroyed["orphan-host-1"] || !destroyed["orphan-host-3"] {
		t.Fatalf("Sweep: want orphan-host-1 and orphan-host-3 destroyed, got %v", destroyed)
	}
	if destroyed["orphan-host-2"] {
		t.Fatalf("Sweep: orphan-host-2 was rejected and must not be destroyed")
	}
}

func TestSessionManager_Sweep_PromptTextNamesWhatWillBeDestroyed(t *testing.T) {
	f := newSMFixture(t, nil)

	hosts := []Host{
		{Address: "orphan-host-1", BackendType: "fake", Workspace: "/workspace"},
	}
	wrap := &backendWithOrphans{fakeBackend: f.backend, Hosts: hosts}
	kwrap := &keyProviderWithOrphans{fakeEphemeralKeyProvider: f.keys}
	f.sm.Backend = wrap
	f.sm.KeyProvider = kwrap

	f.confirmAnswers = []bool{false}

	if _, err := f.sm.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: unexpected error: %v", err)
	}
	if len(f.confirmPrompts) == 0 {
		t.Fatalf("Sweep: expected a Confirm prompt")
	}
	for _, p := range f.confirmPrompts {
		if !strings.Contains(p, "orphan-host-1") {
			t.Fatalf("Sweep prompt must name the orphan; got: %q", p)
		}
		lower := strings.ToLower(p)
		if !strings.Contains(lower, "destroy") && !strings.Contains(lower, "[y/n]") {
			t.Fatalf("Sweep prompt must use 'destroy' or '[y/N]' shape; got: %q", p)
		}
	}
}

func TestSessionManager_Sweep_OrphanDetectionSplitsAcrossThreeCategories(t *testing.T) {
	f := newSMFixture(t, nil)

	hostH1 := Host{Address: "H1", BackendType: "fake", Workspace: "/workspace"}
	hostH3 := Host{Address: "H3", BackendType: "fake", Workspace: "/workspace"}
	_ = f.store.Save(LocalSessionRecord{
		ID: SessionID("rec-h1"), Repo: "/repo", Branch: "a", Backend: "fake",
		Host: hostH1, CreatedAt: f.now,
	})
	orphanRec := LocalSessionRecord{
		ID: SessionID("rec-h3"), Repo: "/repo", Branch: "c", Backend: "fake",
		Host: hostH3, CreatedAt: f.now.Add(-1 * time.Hour),
	}
	_ = f.store.Save(orphanRec)

	wrap := &backendWithOrphans{
		fakeBackend: f.backend,
		Hosts: []Host{
			hostH1, // matches rec-h1 → not orphan.
			{Address: "H2", BackendType: "fake", Workspace: "/workspace"}, // no record → orphan host.
		},
	}
	kwrap := &keyProviderWithOrphans{
		fakeEphemeralKeyProvider: f.keys,
		Keys: []KeyHandle{
			{Provider: "fake", ID: "orphan-key"},
		},
	}
	f.sm.Backend = wrap
	f.sm.KeyProvider = kwrap

	f.confirmAnswers = nil

	report, err := f.sm.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: unexpected error: %v", err)
	}
	if len(report.OrphanHosts) != 1 || report.OrphanHosts[0].Address != "H2" {
		t.Fatalf("Sweep: OrphanHosts want [H2], got %+v", report.OrphanHosts)
	}
	if len(report.OrphanSessionRecords) != 1 || report.OrphanSessionRecords[0].ID != orphanRec.ID {
		t.Fatalf("Sweep: OrphanSessionRecords want [rec-h3], got %+v", report.OrphanSessionRecords)
	}
	if len(report.OrphanEphemeralKeys) != 1 || report.OrphanEphemeralKeys[0].ID != "orphan-key" {
		t.Fatalf("Sweep: OrphanEphemeralKeys want [orphan-key], got %+v", report.OrphanEphemeralKeys)
	}
}
