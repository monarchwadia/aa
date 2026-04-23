package main

import (
	"strings"
	"time"
)

// provisionTimeout is the window after CreatedAt during which a
// still-zero-value Host is considered PROVISIONING rather than
// FAILED_PROVISION. Past this window, if the backend has still not produced
// a reachable Host, we assume provisioning failed. Ten minutes is long
// enough to cover slow cold-starts on remote backends and short enough that
// a truly-stuck session surfaces without the user having to look.
const provisionTimeout = 10 * time.Minute

// SessionState is the user-visible state of an aa session, computed on
// demand from three inputs (laptop record + remote state file + container
// exit code). It is NEVER stored; see ComputeSessionState.
type SessionState string

const (
	// StateProvisioning is during host + container startup, before the
	// agent has begun running.
	StateProvisioning SessionState = "PROVISIONING"

	// StateFailedProvision is a terminal state indicating host or container
	// startup failed. Teardown still needs to run.
	StateFailedProvision SessionState = "FAILED_PROVISION"

	// StateRunning is while the agent process is alive inside the
	// container.
	StateRunning SessionState = "RUNNING"

	// StateDone is a terminal state: the agent wrote DONE and exited 0.
	StateDone SessionState = "DONE"

	// StateFailed is a terminal state: the agent wrote FAILED:<reason> and
	// exited non-zero.
	StateFailed SessionState = "FAILED"

	// StateLimbo is a terminal state: the agent process exited without
	// writing any state file. Cause is unknown; may be OOM, segfault,
	// user-typed `exit`, etc.
	StateLimbo SessionState = "LIMBO"

	// StateInconsistent is a terminal state: the state file and the exit
	// code disagree (e.g. state=DONE but exit=2). aa shows both signals
	// and lets the user decide.
	StateInconsistent SessionState = "INCONSISTENT"

	// StatePushed is post-terminal: the user ran `aa push` successfully.
	StatePushed SessionState = "PUSHED"

	// StateTornDown is post-terminal: all infrastructure is gone, key
	// revoked, local record removed. A session in this state is effectively
	// no longer a session.
	StateTornDown SessionState = "TORN_DOWN"
)

// LocalSessionRecord is the laptop's stored record of a session, persisted
// at ~/.aa/sessions/<id>.json. It carries the operational state (pushed,
// torn down) that the agent host does not know about, plus the host
// address + ephemeral key handle needed to reconnect.
type LocalSessionRecord struct {
	ID     SessionID `json:"id"`
	Repo   string    `json:"repo"` // absolute path on the laptop
	Branch string    `json:"branch"`

	// Backend names the backend config entry used for this session (one of
	// "local", "fly", "process", or a user-defined name).
	Backend string `json:"backend"`

	// Host is the provisioned backend host for this session.
	Host Host `json:"host"`

	// SSHKeyPath is the laptop-side absolute path to the private SSH key
	// aa generated for this session when the backend needs SSH (fly,
	// user-provided SSH backends). The key is per-session and deleted at
	// teardown. Empty for backends that don't use SSH (local, process).
	SSHKeyPath string `json:"ssh_key_path,omitempty"`

	// EphemeralKeyID is the handle (provider-specific) for the LLM API
	// key minted at session start. Used by teardown to call Revoke.
	EphemeralKeyID string `json:"ephemeral_key_id,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	PushedAt   *time.Time `json:"pushed_at,omitempty"`
	TornDownAt *time.Time `json:"torn_down_at,omitempty"`
}

// RemoteStatus captures the agent host's view of a session at one moment.
// Produced fresh each time SessionManager wants to know the state.
type RemoteStatus struct {
	// StateFile contains the bytes of $AA_WORKSPACE/.aa/state, or "" if
	// the file does not exist.
	StateFile string

	// AgentMessage is a human-readable reason parsed from StateFile (the
	// substring after "FAILED: " or similar). Empty if not present.
	AgentMessage string

	// ExitCode is the container's process exit code, or -1 if the
	// container is still running.
	ExitCode int

	// ContainerAlive is true iff the sandbox's main process is still up.
	ContainerAlive bool
}

// ComputeSessionState returns the displayed session state given fresh reads
// of both the laptop record and the remote status. Pure function; no I/O.
//
// Precedence, high to low:
//  1. TornDownAt set  → TORN_DOWN (wins over PushedAt; teardown is past push).
//  2. PushedAt set    → PUSHED    (wins over any remote view; the laptop's
//     operational record is the source of truth).
//  3. Zero-value Host → PROVISIONING if the record is younger than
//     provisionTimeout, else FAILED_PROVISION.
//  4. Container alive → RUNNING.
//  5. Container dead  → merge the state file and exit code:
//     "DONE"+exit 0               → DONE
//     "DONE"+exit!=0              → INCONSISTENT
//     "FAILED…"+exit!=0           → FAILED
//     "FAILED…"+exit 0            → INCONSISTENT
//     ""  (any exit, incl. 0)     → LIMBO (the agent never reported)
//
// See docs/architecture/aa.md § "Decision 2" for why this is a function, not
// a stored enum.
func ComputeSessionState(rec LocalSessionRecord, remote RemoteStatus) SessionState {
	if rec.TornDownAt != nil {
		return StateTornDown
	}
	if rec.PushedAt != nil {
		return StatePushed
	}

	if rec.Host.Address == "" && rec.Host.BackendType == "" {
		if time.Since(rec.CreatedAt) > provisionTimeout {
			return StateFailedProvision
		}
		return StateProvisioning
	}

	if remote.ContainerAlive {
		return StateRunning
	}

	stateFileSaidDone := remote.StateFile == "DONE" || strings.HasPrefix(remote.StateFile, "DONE:") || strings.HasPrefix(remote.StateFile, "DONE ")
	stateFileSaidFailed := strings.HasPrefix(remote.StateFile, "FAILED")
	exitClean := remote.ExitCode == 0

	switch {
	case stateFileSaidDone && exitClean:
		return StateDone
	case stateFileSaidFailed && !exitClean:
		return StateFailed
	case stateFileSaidDone || stateFileSaidFailed:
		return StateInconsistent
	default:
		return StateLimbo
	}
}
