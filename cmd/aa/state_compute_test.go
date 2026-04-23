package main

import (
	"fmt"
	"testing"
	"time"
)

// callComputeSessionState invokes ComputeSessionState and converts any panic
// into a returned error so each subtest fails independently (red) rather
// than aborting the whole test binary. Once ComputeSessionState is
// implemented and no longer panics, every subtest with matching input →
// output expectations turns green on its own.
func callComputeSessionState(rec LocalSessionRecord, remote RemoteStatus) (state SessionState, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ComputeSessionState panicked: %v", r)
		}
	}()
	state = ComputeSessionState(rec, remote)
	return state, nil
}

// TestComputeSessionState is the table-driven truth table for
// ComputeSessionState. It encodes, as executable subtests, the state machine
// described in README.md § "Session states" and docs/architecture/aa.md
// § "Decision 2: Session state is a merge function, not a stored enum".
//
// Conventions used across these tests:
//   - An empty Host.Address signals "not yet provisioned". The contract file
//     for Host.Address explicitly says it is empty for local/process, so a
//     real provisioned-ness check in production code may need richer signals
//     — but the task instructions pin this convention for the test matrix,
//     and the tests document it here.
//   - FAILED_PROVISION is signalled when the host is still zero-valued
//     (Address == "" AND BackendType == "") AND the record has been around
//     longer than a "provision should have finished" window. We pick 10
//     minutes as that window; the exact number is an implementation detail
//     of ComputeSessionState, so this test uses a record that is an hour old
//     to be unambiguously past any reasonable window, and a record that is
//     a second old to be unambiguously inside the provisioning window.
//   - RemoteStatus.ExitCode == -1 means "container still running" per the
//     doc comment on that field.
//   - A nil PushedAt / TornDownAt means the corresponding operation has not
//     happened yet.
//
// Every subtest builds its own LocalSessionRecord and RemoteStatus inline so
// tests are independent and can be reordered or run in isolation.
func TestComputeSessionState(t *testing.T) {
	// A stable "now-ish" reference point used as CreatedAt for records where
	// freshness matters. Tests that care about freshness set their own
	// CreatedAt relative to time.Now() so the pure function can make its
	// own decisions against the wall clock.
	freshCreated := time.Now().Add(-1 * time.Second)
	staleCreated := time.Now().Add(-1 * time.Hour)

	pushedAt := time.Now().Add(-30 * time.Minute)
	tornDownAt := time.Now().Add(-10 * time.Minute)

	t.Run("PROVISIONING_hostNotYetAssigned", func(t *testing.T) {
		// Claim: a record that has just been created and whose Host has no
		// Address yet is in PROVISIONING — the laptop has written the
		// session record but the backend has not produced a reachable Host.
		rec := LocalSessionRecord{
			ID:        "p-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{}, // zero value: not provisioned
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       -1,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateProvisioning {
			t.Fatalf("want %q, got %q", StateProvisioning, got)
		}
	})

	t.Run("FAILED_PROVISION_staleZeroValueHost", func(t *testing.T) {
		// Claim: if the host never came up (still zero-value) and enough time
		// has passed that provisioning should have either succeeded or
		// reported failure, the state is FAILED_PROVISION. This test uses a
		// one-hour-old record to be unambiguously past any reasonable
		// provision-timeout window.
		rec := LocalSessionRecord{
			ID:        "p-2",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{}, // zero value: still unprovisioned
			CreatedAt: staleCreated,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       -1,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateFailedProvision {
			t.Fatalf("want %q, got %q", StateFailedProvision, got)
		}
	})

	t.Run("RUNNING_containerAliveNoStateFile", func(t *testing.T) {
		// Claim: once the container is alive and the agent has not yet
		// written a state file, the session is RUNNING.
		rec := LocalSessionRecord{
			ID:        "r-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       -1, // still running
			ContainerAlive: true,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateRunning {
			t.Fatalf("want %q, got %q", StateRunning, got)
		}
	})

	t.Run("DONE_cleanExit", func(t *testing.T) {
		// Claim: not alive, state file "DONE", exit 0, and no post-terminal
		// markers (PushedAt / TornDownAt) yields DONE.
		rec := LocalSessionRecord{
			ID:        "d-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
			// PushedAt / TornDownAt nil
		}
		remote := RemoteStatus{
			StateFile:      "DONE",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateDone {
			t.Fatalf("want %q, got %q", StateDone, got)
		}
	})

	t.Run("FAILED_agentSignalledFailure", func(t *testing.T) {
		// Claim: state file begins with "FAILED" and exit code is non-zero —
		// the agent signalled failure cleanly. This is FAILED, not
		// INCONSISTENT.
		rec := LocalSessionRecord{
			ID:        "f-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "FAILED: dependency conflict",
			AgentMessage:   "dependency conflict",
			ExitCode:       1,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateFailed {
			t.Fatalf("want %q, got %q", StateFailed, got)
		}
	})

	t.Run("LIMBO_exitedWithoutStateFile", func(t *testing.T) {
		// Claim: container not alive, no state file written, and the process
		// died with a non-zero exit (e.g. 137 for OOM). Cause is unknown —
		// this is LIMBO. Convention for this test: exit != 0 with empty
		// state file is LIMBO regardless of sign, because the agent made no
		// report at all.
		rec := LocalSessionRecord{
			ID:        "l-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       137, // SIGKILL / OOM
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateLimbo {
			t.Fatalf("want %q, got %q", StateLimbo, got)
		}
	})

	t.Run("LIMBO_exitedZeroWithoutStateFile", func(t *testing.T) {
		// Claim: even an exit-0 process that did not write the state file is
		// LIMBO — the agent never reported success, so we cannot call it
		// DONE. Documented here because it's a judgment call: the README
		// says LIMBO is "exited without signalling," and a bare exit 0
		// qualifies.
		rec := LocalSessionRecord{
			ID:        "l-2",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateLimbo {
			t.Fatalf("want %q, got %q", StateLimbo, got)
		}
	})

	t.Run("INCONSISTENT_stateDoneButExitNonzero", func(t *testing.T) {
		// Claim: state file says DONE but the process exited non-zero. The
		// README example is "post-hook failed after the agent signalled
		// completion." ComputeSessionState must not silently pick a side.
		rec := LocalSessionRecord{
			ID:        "i-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "DONE",
			ExitCode:       2,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateInconsistent {
			t.Fatalf("want %q, got %q", StateInconsistent, got)
		}
	})

	t.Run("INCONSISTENT_stateFailedButExitZero", func(t *testing.T) {
		// Claim: state file reports FAILED but the exit code is 0. Same
		// disagreement as the DONE+nonzero case, in the other direction.
		rec := LocalSessionRecord{
			ID:        "i-2",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
		}
		remote := RemoteStatus{
			StateFile:      "FAILED: something went wrong",
			AgentMessage:   "something went wrong",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateInconsistent {
			t.Fatalf("want %q, got %q", StateInconsistent, got)
		}
	})

	t.Run("PUSHED_recordHasPushedAt", func(t *testing.T) {
		// Claim: a record with PushedAt set (and TornDownAt still nil) is
		// PUSHED. The agent's remote status is irrelevant — the laptop has
		// already applied and pushed the patch.
		rec := LocalSessionRecord{
			ID:        "pu-1",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
			PushedAt:  &pushedAt,
		}
		remote := RemoteStatus{
			StateFile:      "DONE",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StatePushed {
			t.Fatalf("want %q, got %q", StatePushed, got)
		}
	})

	t.Run("TORN_DOWN_recordHasTornDownAt", func(t *testing.T) {
		// Claim: a record with TornDownAt set is TORN_DOWN regardless of
		// remote status. Infrastructure is gone; the session is effectively
		// no longer a session.
		rec := LocalSessionRecord{
			ID:         "td-1",
			Repo:       "/home/user/repo",
			Branch:     "feature/x",
			Backend:    "fly",
			Host:       Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt:  freshCreated,
			TornDownAt: &tornDownAt,
		}
		remote := RemoteStatus{
			StateFile:      "DONE",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateTornDown {
			t.Fatalf("want %q, got %q", StateTornDown, got)
		}
	})

	// -----------------------------------------------------------------
	// Precedence tests — these pin the ordering between post-terminal
	// markers and the underlying agent-reported state.
	// -----------------------------------------------------------------

	t.Run("TORN_DOWN_winsOverPushed", func(t *testing.T) {
		// Claim: when both PushedAt and TornDownAt are set, TORN_DOWN wins.
		// README says the transition goes PUSHED → TORN_DOWN, so once
		// TornDownAt exists the session is past PUSHED.
		rec := LocalSessionRecord{
			ID:         "td-2",
			Repo:       "/home/user/repo",
			Branch:     "feature/x",
			Backend:    "fly",
			Host:       Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt:  freshCreated,
			PushedAt:   &pushedAt,
			TornDownAt: &tornDownAt,
		}
		remote := RemoteStatus{
			StateFile:      "DONE",
			ExitCode:       0,
			ContainerAlive: false,
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StateTornDown {
			t.Fatalf("want %q, got %q", StateTornDown, got)
		}
	})

	t.Run("PUSHED_winsOverRunningRemote", func(t *testing.T) {
		// Claim: if the laptop has recorded PushedAt but the remote is still
		// (reported as) running, PUSHED wins. The laptop's record is the
		// source of truth for operational state; teardown may not have
		// completed yet, but the push has.
		rec := LocalSessionRecord{
			ID:        "pu-2",
			Repo:      "/home/user/repo",
			Branch:    "feature/x",
			Backend:   "fly",
			Host:      Host{Address: "fly@10.0.0.1:22", BackendType: "fly", Workspace: "/workspace"},
			CreatedAt: freshCreated,
			PushedAt:  &pushedAt,
		}
		remote := RemoteStatus{
			StateFile:      "",
			ExitCode:       -1,
			ContainerAlive: true, // remote still thinks it's alive
		}
		got, err := callComputeSessionState(rec, remote)
		if err != nil {
			t.Fatal(err)
		}
		if got != StatePushed {
			t.Fatalf("want %q, got %q", StatePushed, got)
		}
	})
}
