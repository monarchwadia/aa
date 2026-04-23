// session.go — the SessionManager orchestration layer.
//
// This file contains stubs only. Every method body is `panic(...)` naming the
// `session-manager-and-cli` workstream. The real bodies are written in the
// implement step of the code-write workflow; for this wave the shape is frozen
// so session_test.go can be red-written against it, and so the wave-4 CLI
// adapter (main + verb dispatch) can import a stable surface when it lands.
//
// What belongs here:
//   - The SessionManager struct (concrete orchestrator).
//   - One method per user-facing verb that needs orchestration across
//     collaborators: StartSession, Attach, Status, Diff, Push, Kill, Retry,
//     ListAll, Sweep.
//   - The SweepReport value type returned by Sweep.
//   - Two narrow capability interfaces (BackendOrphanLister,
//     EphemeralKeyOrphanLister) that describe the extra reads Sweep needs
//     beyond the base Backend / EphemeralKeyProvider contracts. They are
//     defined here so SessionManager depends on small seams rather than
//     widening Backend and EphemeralKeyProvider for a housekeeping verb.
//
// What does NOT belong here (belongs to the wave-4 CLI adapter — main.go and
// verb_*.go — which are a separate concern):
//   - `main()` or flag parsing.
//   - `aa init` and `aa version` — trivial, no orchestration, no collaborator
//     wiring, so they are CLI-only.
//   - Pretty-printing session state for humans. SessionManager writes
//     structured lines to Out/Err; the CLI layer decides on colours and
//     banner shapes.
//
// Dependency-inversion discipline per PHILOSOPHY.md axis 2 (Evolvability):
// every collaborator is passed in at construction. No singletons, no globals,
// no package-level state. The Clock, Confirm, Out, and Err fields are
// explicitly injectable so tests can observe and drive behaviour deterministically.
package main

import (
	"context"
	"io"
	"time"
)

// SessionManager is the concrete orchestration layer behind each aa verb.
// It holds every collaborator it needs as an explicit field so the wave-4
// CLI adapter (main.go + verb_*.go) can construct it once per invocation
// and dispatch verbs against it, and so tests can wire fakes for every
// dependency.
//
// Every method reports what it did via the injected Out/Err writers (per
// PHILOSOPHY.md axis 3, Observability) and surfaces errors that name the
// failing operation (per the same axis).
type SessionManager struct {
	// Backend provisions hosts, runs containers, and tears them down.
	Backend Backend

	// Store persists local session records (~/.aa/sessions/<id>.json) and
	// is the source of truth for "does this session exist on this laptop".
	Store SessionStore

	// KeyProvider mints and revokes ephemeral LLM API keys.
	KeyProvider EphemeralKeyProvider

	// SSH runs non-interactive commands on the agent host and handles
	// interactive attach. Unused by the local/process backends; required
	// for fly and any future SSH-based backend.
	SSH SSHRunner

	// Rules is the configured list of patch safeguards evaluated during
	// Push. Order matters — violations are reported in list order.
	Rules []Rule

	// Clock is injectable so tests can pin CreatedAt / PushedAt /
	// TornDownAt to deterministic values. Defaults to time.Now when nil.
	Clock func() time.Time

	// Confirm is the prompt function the CLI adapter passes in. The CLI
	// layer implements it as a real terminal prompt; tests stub it with a
	// pre-canned answer queue. defaultYes controls the answer if the user
	// hits enter (true for warn-level rule violations, false for
	// error-level ones, per README § Rules).
	Confirm func(prompt string, defaultYes bool) bool

	// Out receives structured success lines (key=value / one-per-operation
	// progress messages). Tests capture into a bytes.Buffer.
	Out io.Writer

	// Err receives structured error context. Same capture pattern.
	Err io.Writer
}

// NewSessionManager wires a SessionManager from its five primary
// collaborators. Clock / Confirm / Out / Err are set by the caller after
// construction — typically the wave-4 CLI adapter sets them from
// time.Now, a real terminal prompt, os.Stdout, and os.Stderr. Tests set
// them to deterministic fakes.
//
// Example (from the CLI adapter):
//
//	sm := NewSessionManager(flyBackend, fileStore, anthropic, realSSH, cfg.Rules)
//	sm.Clock = time.Now
//	sm.Confirm = promptTerminalYesNo
//	sm.Out = os.Stdout
//	sm.Err = os.Stderr
//	id, err := sm.StartSession(ctx, repoPath, branchName, cfg)
func NewSessionManager(backend Backend, store SessionStore, key EphemeralKeyProvider, ssh SSHRunner, rules []Rule) *SessionManager {
	return &SessionManager{
		Backend:     backend,
		Store:       store,
		KeyProvider: key,
		SSH:         ssh,
		Rules:       rules,
	}
}

// StartSession is the orchestration behind `aa` when no session record
// exists for this repo+branch. It provisions a Host, mints a fresh
// ephemeral key, installs egress, runs the container, persists the
// session record, and (for interactive callers) attaches the user.
//
// Rollback on failure: if any of Mint / InstallEgress / RunContainer fail,
// every resource acquired earlier is released (Revoke the minted key,
// Teardown the provisioned host) and no record is saved.
//
// Observability: a one-line progress message is written to Out for each
// sub-operation (provisioned, minted, egress installed, container running,
// saved).
//
// Example:
//
//	id, err := sm.StartSession(ctx, "/Users/m/code/myapp", "feature/oauth", cfg)
//	// id == "myapp-feature-oauth"
func (s *SessionManager) StartSession(ctx context.Context, repo, branch string, cfg Config) (SessionID, error) {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Attach is the orchestration behind `aa` when a session record already
// exists and the computed state is RUNNING. It forwards the caller's
// terminal to the agent host via the SSHRunner's Attach, pointing at the
// tmux session that wraps the agent process.
//
// If the computed state is not RUNNING, Attach returns an error directing
// the user at `aa status` — forcing attach on a terminal session is what
// `aa attach` is for.
//
// Example:
//
//	err := sm.Attach(ctx, "myapp-feature-oauth")
func (s *SessionManager) Attach(ctx context.Context, id SessionID) error {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Status is the orchestration behind `aa status`. It reads the local
// record and the remote status fresh, runs ComputeSessionState, and
// returns both the state and the RemoteStatus the caller can render.
//
// Example:
//
//	state, remote, err := sm.Status(ctx, "myapp-feature-oauth")
//	// state == StateDone, remote.AgentMessage == "Implemented OAuth2…"
func (s *SessionManager) Status(ctx context.Context, id SessionID) (SessionState, RemoteStatus, error) {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Diff is the orchestration behind `aa diff`. It pulls the patch bytes
// from the backend (ReadRemoteFile at $AA_WORKSPACE/.aa/result.patch) and
// returns them so the CLI adapter can pipe through $PAGER.
//
// Diff rendering and review happen on the laptop, per README § "Review
// and push flow" — the agent host never formats what the user sees.
//
// Example:
//
//	raw, err := sm.Diff(ctx, "myapp-feature-oauth")
//	// raw is the bytes of result.patch, ready for `git apply --stat` + pager.
func (s *SessionManager) Diff(ctx context.Context, id SessionID) ([]byte, error) {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Push is the orchestration behind `aa push`, the only verb that
// performs an irreversible operation (`git push`, per Decision 6).
//
// Ordered steps, per README § "Review and push flow":
//  1. Pull patch bytes from backend.
//  2. Parse the patch, collect changed files.
//  3. Run rules. On any `error`-severity violation, prompt with default=false.
//     On `warn` severity, prompt with default=true.
//  4. If the user accepts, apply the patch in a local clone and `git push`.
//  5. On successful push only: Teardown the backend, Revoke the key,
//     update the record with PushedAt and TornDownAt.
//  6. If `git push` itself fails, the record remains DONE so the user can
//     recover via the local clone (README § "I ran aa push and the push
//     failed"). Teardown is NOT invoked.
//
// Observability: on success, the commit SHAs pushed and teardown
// progress are written to Out.
//
// Example:
//
//	err := sm.Push(ctx, "myapp-feature-oauth")
func (s *SessionManager) Push(ctx context.Context, id SessionID) error {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Kill is the orchestration behind `aa kill`. It tears down the backend
// host, revokes the ephemeral key, and deletes the local record. Each
// sub-operation is reported on Out. If backend teardown fails but the
// local record exists, the local record is cleaned up and the error
// names what was left dangling on the backend for the user to sweep.
//
// Example:
//
//	err := sm.Kill(ctx, "myapp-feature-oauth")
func (s *SessionManager) Kill(ctx context.Context, id SessionID) error {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// Retry is the orchestration behind `aa retry`. Valid only in LIMBO or
// FAILED; returns an error in every other state. When valid, invokes
// Backend.RunContainer on the existing Host — the provisioned host and
// ephemeral key are reused, and the workspace contents are preserved
// across the old container and the new one.
//
// Example:
//
//	err := sm.Retry(ctx, "myapp-feature-oauth")
func (s *SessionManager) Retry(ctx context.Context, id SessionID) error {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// ListAll is the orchestration behind `aa list`. It returns every
// locally-known session record, sorted newest-first by CreatedAt so the
// user's most recent work is always at the top of the list.
//
// Example:
//
//	recs, err := sm.ListAll(ctx)
//	for _, r := range recs { fmt.Println(r.ID, r.Branch, r.CreatedAt) }
func (s *SessionManager) ListAll(ctx context.Context) ([]LocalSessionRecord, error) {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}

// SweepReport is the result of a `aa sweep` pass. It reports every
// orphan the manager found across three axes: backend hosts with no
// local record, local records whose backend host is gone, and minted
// keys that are not tied to any known session. Sweep mutates nothing
// without confirmation — see SessionManager.Sweep.
type SweepReport struct {
	// OrphanHosts are backend-provisioned hosts (tagged aa-*) with no
	// matching local session record on this laptop.
	OrphanHosts []Host

	// OrphanSessionRecords are local records whose referenced Host is no
	// longer visible on its backend (likely reaped by the provider).
	OrphanSessionRecords []LocalSessionRecord

	// OrphanEphemeralKeys are keys the EphemeralKeyProvider still reports
	// as live, but which are not referenced by any local session record.
	OrphanEphemeralKeys []KeyHandle
}

// BackendOrphanLister is the narrow capability Sweep uses to enumerate
// backend-visible hosts tagged for aa. It is NOT part of the core
// Backend interface (which is deliberately sized to the lifecycle a
// session needs, per Decision 1) — housekeeping-only reads live on
// their own seam so the main interface stays small.
//
// Backends implement this additively when the `sweep` verb supports
// them; a backend that does not implement it simply contributes no
// OrphanHosts entries to the SweepReport.
type BackendOrphanLister interface {
	// ListHosts returns every host currently visible on the backend that
	// was provisioned by aa (by tag or naming convention). Used only by
	// Sweep; never part of the session lifecycle.
	ListHosts(ctx context.Context) ([]Host, error)
}

// EphemeralKeyOrphanLister is the narrow capability Sweep uses to
// enumerate live ephemeral keys. Same rationale as BackendOrphanLister:
// the core EphemeralKeyProvider interface stays sized to Mint/Revoke,
// and the housekeeping seam is separate.
type EphemeralKeyOrphanLister interface {
	// ListLiveKeys returns every key the provider currently considers
	// live (minted, not yet revoked, TTL not yet expired).
	ListLiveKeys(ctx context.Context) ([]KeyHandle, error)
}

// Sweep is the orchestration behind `aa sweep`. It enumerates orphan
// resources across three axes (backend hosts, local records, ephemeral
// keys) and prompts via Confirm before destroying any of them. Never
// silently destroys — the pinned business decision.
//
// Returns the SweepReport of every orphan found (regardless of
// confirmation outcome). Only orphans the user confirmed with `yes` are
// actually torn down / revoked / deleted.
//
// Example:
//
//	report, err := sm.Sweep(ctx)
//	// report.OrphanHosts == [...], report.OrphanEphemeralKeys == [...]
func (s *SessionManager) Sweep(ctx context.Context) (SweepReport, error) {
	panic("unimplemented — see workstream `session-manager-and-cli` in docs/architecture/aa.md § Workstreams")
}
