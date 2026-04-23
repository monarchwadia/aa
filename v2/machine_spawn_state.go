// Package main: machine_spawn_state.go holds the pure-function policy constants
// for the backend-ready → shell-reachable state machine described in
// docs/architecture/machine-lifecycle.md (S4).
//
// Each constant lives behind a named function so a future change to the retry
// or timeout budget is a one-line diff that a unit test will light up in
// review (philosophy axes 1 & 2).
package main

import "time"

// SpawnState is the named enum of states the spawn state machine can be in.
// The string form of each state is stable; tests pin these names.
type SpawnState string

const (
	SpawnStateRequested       SpawnState = "requested"
	SpawnStateCreating        SpawnState = "creating"
	SpawnStateBackendPending  SpawnState = "backend-pending"
	SpawnStateBackendReady    SpawnState = "backend-ready"
	SpawnStateShellReachable  SpawnState = "shell-reachable"
	SpawnStateFailed          SpawnState = "failed"
	SpawnStateTimedOutBackend SpawnState = "timed-out-backend"
	SpawnStateTimedOutShell   SpawnState = "timed-out-shell"
)

// NextBackendPollDelay returns the delay before the next GET /machines/:id
// poll while waiting for backend-ready. Attempt is 1-indexed.
// Example: NextBackendPollDelay(1) == 2*time.Second.
func NextBackendPollDelay(attempt int) time.Duration { return 2 * time.Second }

// NextShellAttemptDelay returns the delay before the next flyctl ssh console
// attempt while bridging backend-ready → shell-reachable. Attempt is 1-indexed.
// Example: NextShellAttemptDelay(1) == 3*time.Second.
func NextShellAttemptDelay(attempt int) time.Duration { return 3 * time.Second }

// ShellAttemptBudget returns the maximum number of shell-reachability attempts
// before spawn declares timed-out-shell. Pinned at 15 by ADR-4.
// Example: ShellAttemptBudget() == 15.
func ShellAttemptBudget() int { return 15 }

// BackendDeadline returns the total wall-clock budget for backend-ready.
// Example: BackendDeadline() == 90*time.Second.
func BackendDeadline() time.Duration { return 90 * time.Second }

// TransitionOnBackendState returns the next SpawnState given the current
// state and the latest Fly-reported machine state string (e.g. "starting",
// "started"). Pure function; safe to call with any inputs. Terminal states
// do not advance.
// Example: TransitionOnBackendState(SpawnStateBackendPending, "started") == SpawnStateBackendReady.
func TransitionOnBackendState(current SpawnState, flyState string) SpawnState {
	if isTerminal(current) {
		return current
	}
	if current == SpawnStateBackendPending && flyState == "started" {
		return SpawnStateBackendReady
	}
	return current
}

// TransitionOnShellAttempt returns the next SpawnState given the current
// state, a shell-attempt outcome (nil == success), and the attempt index.
// Example: TransitionOnShellAttempt(SpawnStateBackendReady, nil, 1) == SpawnStateShellReachable.
func TransitionOnShellAttempt(current SpawnState, attemptErr error, attempt int) SpawnState {
	if isTerminal(current) {
		return current
	}
	if current != SpawnStateBackendReady {
		return current
	}
	if attemptErr == nil {
		return SpawnStateShellReachable
	}
	if attempt >= ShellAttemptBudget() {
		return SpawnStateTimedOutShell
	}
	return SpawnStateBackendReady
}

func isTerminal(s SpawnState) bool {
	switch s {
	case SpawnStateShellReachable, SpawnStateFailed, SpawnStateTimedOutBackend, SpawnStateTimedOutShell:
		return true
	}
	return false
}
