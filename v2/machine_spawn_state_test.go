// machine_spawn_state_test.go pins the pure-logic contract of the spawn
// state machine described in docs/architecture/machine-lifecycle.md (S4
// and ADR-4). Every test name reads as a sentence stating the behaviour
// being asserted. Tests are pure-logic; no HTTP, no exec, no filesystem.
package main

import (
	"errors"
	"testing"
	"time"
)

// Shell-attempt budget is 15 attempts, pinned by ADR-4.
func TestShellAttemptBudgetIsFifteen(t *testing.T) {
	got := ShellAttemptBudget()
	if got != 15 {
		t.Fatalf("ShellAttemptBudget() = %d, want 15 (ADR-4)", got)
	}
}

// Delay between shell-reachability attempts is 3 seconds regardless of
// attempt number (no backoff — ADR-4 rejected exponential backoff).
func TestShellAttemptDelayIsThreeSecondsForEveryAttempt(t *testing.T) {
	for attempt := 1; attempt <= 15; attempt++ {
		got := NextShellAttemptDelay(attempt)
		if got != 3*time.Second {
			t.Errorf("NextShellAttemptDelay(%d) = %v, want 3s", attempt, got)
		}
	}
}

// Backend-poll cadence is 2 seconds, mirroring the current waitForState loop.
func TestBackendPollDelayIsTwoSecondsForEveryAttempt(t *testing.T) {
	for attempt := 1; attempt <= 45; attempt++ {
		got := NextBackendPollDelay(attempt)
		if got != 2*time.Second {
			t.Errorf("NextBackendPollDelay(%d) = %v, want 2s", attempt, got)
		}
	}
}

// Total backend-ready wall-clock budget is 90 seconds.
func TestBackendDeadlineIsNinetySeconds(t *testing.T) {
	got := BackendDeadline()
	if got != 90*time.Second {
		t.Fatalf("BackendDeadline() = %v, want 90s", got)
	}
}

// Shell-budget × shell-delay is the 45-second bridge window ADR-4 promises.
func TestShellBudgetTimesDelayEqualsFortyFiveSeconds(t *testing.T) {
	got := time.Duration(ShellAttemptBudget()) * NextShellAttemptDelay(1)
	if got != 45*time.Second {
		t.Fatalf("budget*delay = %v, want 45s (ADR-4)", got)
	}
}

// backend-pending + flyState=="started" advances to backend-ready.
func TestTransitionOnBackendStateStartedAdvancesFromBackendPendingToBackendReady(t *testing.T) {
	got := TransitionOnBackendState(SpawnStateBackendPending, "started")
	if got != SpawnStateBackendReady {
		t.Fatalf("transition(backend-pending, started) = %q, want %q", got, SpawnStateBackendReady)
	}
}

// backend-pending + a non-terminal flyState stays in backend-pending.
func TestTransitionOnBackendStateStartingStaysInBackendPending(t *testing.T) {
	got := TransitionOnBackendState(SpawnStateBackendPending, "starting")
	if got != SpawnStateBackendPending {
		t.Fatalf("transition(backend-pending, starting) = %q, want %q", got, SpawnStateBackendPending)
	}
}

// backend-ready + shell attempt success moves to shell-reachable (terminal).
func TestTransitionOnShellAttemptSuccessMovesFromBackendReadyToShellReachable(t *testing.T) {
	got := TransitionOnShellAttempt(SpawnStateBackendReady, nil, 1)
	if got != SpawnStateShellReachable {
		t.Fatalf("transition(backend-ready, nil, 1) = %q, want %q", got, SpawnStateShellReachable)
	}
}

// backend-ready + failed shell attempt below budget stays in backend-ready.
func TestTransitionOnShellAttemptFailureBelowBudgetStaysInBackendReady(t *testing.T) {
	got := TransitionOnShellAttempt(SpawnStateBackendReady, errors.New("conn refused"), 3)
	if got != SpawnStateBackendReady {
		t.Fatalf("transition(backend-ready, err, 3) = %q, want %q", got, SpawnStateBackendReady)
	}
}

// backend-ready + failed shell attempt at the budget boundary moves to timed-out-shell.
func TestTransitionOnShellAttemptFailureAtBudgetMovesToTimedOutShell(t *testing.T) {
	got := TransitionOnShellAttempt(SpawnStateBackendReady, errors.New("conn refused"), ShellAttemptBudget())
	if got != SpawnStateTimedOutShell {
		t.Fatalf("transition(backend-ready, err, budget) = %q, want %q", got, SpawnStateTimedOutShell)
	}
}

// Terminal states do not advance under any input.
func TestTransitionFromShellReachableIsAlwaysShellReachable(t *testing.T) {
	got := TransitionOnBackendState(SpawnStateShellReachable, "started")
	if got != SpawnStateShellReachable {
		t.Fatalf("transition(shell-reachable, started) = %q, want %q", got, SpawnStateShellReachable)
	}
}

// Fuzz: no Fly-state string causes a panic in the backend transition.
func FuzzTransitionOnBackendStateNeverPanics(f *testing.F) {
	f.Add("started")
	f.Add("starting")
	f.Add("")
	f.Add("unknown-state")
	f.Fuzz(func(t *testing.T, flyState string) {
		// Must not panic regardless of input.
		_ = TransitionOnBackendState(SpawnStateBackendPending, flyState)
	})
}
