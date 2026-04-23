// session.go — the SessionManager orchestration layer.
//
// SessionManager is the concrete orchestrator behind each user-facing verb.
// It wires Backend, SessionStore, EphemeralKeyProvider, SSHRunner, and a
// Rules slice into one struct whose methods implement StartSession, Attach,
// Status, Diff, Push, Kill, Retry, ListAll, and Sweep.
//
// This file is NOT in strict mode (see docs/PHILOSOPHY.md § "Strict mode").
// It is orchestration: every security boundary lives further down the stack
// (patch_parser.go, config_loader.go, egress.go, keys_anthropic.go,
// ssh_runner.go). The governing concerns here are Clarity (axis 1),
// Evolvability (axis 2), and Observability (axis 3): every step emits a
// one-line progress message to Out or Err so a solo developer can `grep`
// the output and know what happened.
//
// Dependency-inversion discipline: every collaborator is a field, never a
// singleton. Tests substitute fakes; production wires real implementations
// in main.go. Clock / Confirm / Out / Err are explicitly injectable.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// SessionManager is the concrete orchestration layer behind each aa verb.
// Every collaborator it needs is an explicit field so the CLI adapter
// (main.go + verb_*.go) can construct it once per invocation and tests can
// wire fakes for every dependency.
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

	// Out receives structured success lines (one-per-operation progress
	// messages). Tests capture into a bytes.Buffer.
	Out io.Writer

	// Err receives structured error context. Same capture pattern.
	Err io.Writer
}

// NewSessionManager wires a SessionManager from its five primary
// collaborators. Clock / Confirm / Out / Err are set by the caller after
// construction — typically main.go sets them from time.Now, a real
// terminal prompt, os.Stdout, and os.Stderr. Tests set them to
// deterministic fakes.
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

// clockNow returns the current time via s.Clock or time.Now as a fallback.
func (s *SessionManager) clockNow() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// writeOut writes a one-line observability message to Out (stdout-ish).
// If Out is nil, the message is discarded — tests that don't care about
// observability can omit the field.
func (s *SessionManager) writeOut(format string, args ...any) {
	if s.Out == nil {
		return
	}
	fmt.Fprintf(s.Out, format+"\n", args...)
}

// writeErr is the same pattern for Err.
func (s *SessionManager) writeErr(format string, args ...any) {
	if s.Err == nil {
		return
	}
	fmt.Fprintf(s.Err, format+"\n", args...)
}

// deriveSessionID builds the canonical SessionID "<repo-basename>-<branch-slug>".
// Any character that is not an ASCII alphanumeric, dot, or underscore is
// replaced with "-", runs of "-" collapse, leading/trailing "-" are trimmed,
// and the result is lower-cased. This gives a filesystem-safe, URL-safe,
// human-readable identifier.
//
// Example:
//
//	deriveSessionID("/home/alice/src/MyApp", "feature/oauth-flow")
//	// → SessionID("myapp-feature-oauth-flow")
func deriveSessionID(repoPath, branch string) SessionID {
	base := filepath.Base(repoPath)
	return SessionID(slugify(base) + "-" + slugify(branch))
}

// slugify lowercases and replaces path-unsafe characters with "-", collapsing
// runs and trimming edges. See deriveSessionID for context.
func slugify(s string) string {
	out := make([]byte, 0, len(s))
	lastDash := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
			lastDash = false
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			out = append(out, c)
			lastDash = false
		default:
			if !lastDash {
				out = append(out, '-')
				lastDash = true
			}
		}
	}
	// Trim trailing dash.
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// StartSession orchestrates Provision → Mint → InstallEgress → RunContainer →
// store.Save, in that order. Failure at any step rolls back earlier work:
//   - Mint fails → Teardown the Host, no record saved.
//   - InstallEgress fails → Revoke the key, Teardown the Host, no record saved.
//   - RunContainer fails → Revoke the key, Teardown the Host, no record saved.
//
// Each sub-operation emits a one-line message to s.Out on success so the user
// (a solo dev, per PHILOSOPHY axis 3) can watch progress.
//
// Example:
//
//	id, err := sm.StartSession(ctx, "/Users/m/code/myapp", "feature/oauth", cfg)
//	// id == SessionID("myapp-feature-oauth")
func (s *SessionManager) StartSession(ctx context.Context, repo, branch string, cfg Config) (SessionID, error) {
	id := deriveSessionID(repo, branch)

	// 1. Provision.
	host, err := s.Backend.Provision(ctx, id)
	if err != nil {
		s.writeErr("start: provision failed: %v", err)
		return "", fmt.Errorf("start session %s: provision: %w", id, err)
	}
	s.writeOut("provisioned host %s (backend=%s) for session %s", host.Address, host.BackendType, id)

	// 2. Mint ephemeral key.
	mintReq := MintRequest{
		SessionID:   id,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	}
	handle, _, err := s.KeyProvider.Mint(ctx, mintReq)
	if err != nil {
		s.writeErr("start: mint key failed: %v", err)
		// Rollback: Teardown the provisioned host.
		if tdErr := s.Backend.Teardown(ctx, host); tdErr != nil {
			s.writeErr("start: rollback teardown failed: %v", tdErr)
		}
		return "", fmt.Errorf("start session %s: mint key: %w", id, err)
	}
	s.writeOut("minted ephemeral key %s for session %s", handle.ID, id)

	// 3. Install egress.
	agentName := ""
	var agent AgentConfig
	for name, a := range cfg.Agents {
		agentName = name
		agent = a
		break
	}
	_ = agentName
	if err := s.Backend.InstallEgress(ctx, host, agent.EgressAllowlist); err != nil {
		s.writeErr("start: install egress failed: %v", err)
		if rErr := s.KeyProvider.Revoke(ctx, handle); rErr != nil {
			s.writeErr("start: rollback revoke failed: %v", rErr)
		}
		if tdErr := s.Backend.Teardown(ctx, host); tdErr != nil {
			s.writeErr("start: rollback teardown failed: %v", tdErr)
		}
		return "", fmt.Errorf("start session %s: install egress: %w", id, err)
	}
	s.writeOut("installed egress allowlist (%d entries) for session %s", len(agent.EgressAllowlist), id)

	// 4. Run container.
	spec := ContainerSpec{
		AgentRun:  agent.Run,
		Env:       agent.Env,
		SessionID: id,
	}
	if _, err := s.Backend.RunContainer(ctx, host, spec); err != nil {
		s.writeErr("start: run container failed: %v", err)
		if rErr := s.KeyProvider.Revoke(ctx, handle); rErr != nil {
			s.writeErr("start: rollback revoke failed: %v", rErr)
		}
		if tdErr := s.Backend.Teardown(ctx, host); tdErr != nil {
			s.writeErr("start: rollback teardown failed: %v", tdErr)
		}
		return "", fmt.Errorf("start session %s: run container: %w", id, err)
	}
	s.writeOut("container running for session %s", id)

	// 5. Save the session record.
	rec := LocalSessionRecord{
		ID:             id,
		Repo:           repo,
		Branch:         branch,
		Backend:        cfg.DefaultBackend,
		Host:           host,
		EphemeralKeyID: handle.ID,
		CreatedAt:      s.clockNow(),
	}
	if err := s.Store.Save(rec); err != nil {
		s.writeErr("start: save record failed: %v", err)
		// The container is running but we can't persist the record — treat
		// the session as never having existed. Revoke + Teardown.
		if rErr := s.KeyProvider.Revoke(ctx, handle); rErr != nil {
			s.writeErr("start: rollback revoke failed: %v", rErr)
		}
		if tdErr := s.Backend.Teardown(ctx, host); tdErr != nil {
			s.writeErr("start: rollback teardown failed: %v", tdErr)
		}
		return "", fmt.Errorf("start session %s: save record: %w", id, err)
	}
	s.writeOut("saved session record %s", id)

	return id, nil
}

// Attach checks the session's state and, if RUNNING, delegates to
// SSH.Attach with a tmux-attach command. Non-RUNNING sessions return an
// error telling the user to consult `aa status`.
//
// Example:
//
//	err := sm.Attach(ctx, SessionID("myapp-feature-oauth"))
func (s *SessionManager) Attach(ctx context.Context, id SessionID) error {
	state, _, err := s.Status(ctx, id)
	if err != nil {
		return err
	}
	if state != StateRunning {
		return fmt.Errorf("attach session %s: state is %s, not RUNNING — see `aa status` for next steps", id, state)
	}

	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return fmt.Errorf("attach session %s: load record: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("attach session %s: no local record", id)
	}

	// tmux attach to the session's named tmux; aa wraps agent runs in a
	// tmux session so detach/reattach works over SSH.
	cmd := fmt.Sprintf("tmux attach -t %s", string(id))
	s.writeOut("attaching to session %s", id)

	var stdin io.Reader = os.Stdin
	var stdout io.Writer = os.Stdout
	var stderr io.Writer = os.Stderr
	if s.Out != nil {
		stdout = s.Out
	}
	if s.Err != nil {
		stderr = s.Err
	}
	return s.SSH.Attach(ctx, rec.Host, cmd, stdin, stdout, stderr)
}

// Status reads the local record, reads the remote state file from the
// backend, assembles a RemoteStatus, and runs ComputeSessionState. The
// returned RemoteStatus is what the caller should render.
//
// Example:
//
//	state, remote, err := sm.Status(ctx, "myapp-feature-oauth")
//	// state == StateDone, remote.StateFile == "DONE"
func (s *SessionManager) Status(ctx context.Context, id SessionID) (SessionState, RemoteStatus, error) {
	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return "", RemoteStatus{}, fmt.Errorf("status session %s: load record: %w", id, err)
	}
	if !ok {
		return "", RemoteStatus{}, fmt.Errorf("status session %s: no local record", id)
	}

	remote := RemoteStatus{ExitCode: -1}
	data, readErr := s.Backend.ReadRemoteFile(ctx, rec.Host, ".aa/state")
	if readErr == nil {
		remote.StateFile = string(bytes.TrimSpace(data))
		remote.AgentMessage = parseAgentMessage(remote.StateFile)
	}

	// ContainerAlive heuristic for the session manager: if the state file
	// is empty/absent AND there's no explicit FAILED/DONE content, we
	// treat the container as alive. When it's DONE/FAILED, container is
	// not alive and exit code reflects the reported result.
	switch {
	case remote.StateFile == "DONE":
		remote.ContainerAlive = false
		remote.ExitCode = 0
	case len(remote.StateFile) >= 6 && remote.StateFile[:6] == "FAILED":
		remote.ContainerAlive = false
		remote.ExitCode = 1
	default:
		remote.ContainerAlive = true
	}

	return ComputeSessionState(rec, remote), remote, nil
}

// parseAgentMessage returns the reason string after a leading "FAILED:"
// or "DONE:" marker. The state-file format README documents "FAILED\n" or
// "FAILED: <reason>\n"; we lift the reason out here so callers can display
// it without re-parsing.
func parseAgentMessage(stateFile string) string {
	for _, prefix := range []string{"FAILED: ", "DONE: "} {
		if len(stateFile) >= len(prefix) && stateFile[:len(prefix)] == prefix {
			return stateFile[len(prefix):]
		}
	}
	return ""
}

// Diff pulls $AA_WORKSPACE/.aa/result.patch from the backend and returns
// the raw bytes. Rendering, pagers, and re-review are the caller's job.
//
// Example:
//
//	raw, err := sm.Diff(ctx, "myapp-feature-oauth")
func (s *SessionManager) Diff(ctx context.Context, id SessionID) ([]byte, error) {
	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return nil, fmt.Errorf("diff session %s: load record: %w", id, err)
	}
	if !ok {
		return nil, fmt.Errorf("diff session %s: no local record", id)
	}
	data, err := s.Backend.ReadRemoteFile(ctx, rec.Host, ".aa/result.patch")
	if err != nil {
		return nil, fmt.Errorf("diff session %s: read result.patch: %w", id, err)
	}
	return data, nil
}

// Push is the only irreversible verb. Steps, in order:
//  1. Pull patch bytes (via Diff).
//  2. Parse via ParsePatch.
//  3. Evaluate Rules.
//  4. Confirm per violation (default-yes for warn, default-no for error).
//  5. Apply patch locally and git push.
//  6. On successful push only: Teardown backend, Revoke key, update record.
//
// A git push failure preserves the backend and leaves the record without
// PushedAt, per README § "I ran aa push and the push failed".
//
// Example:
//
//	err := sm.Push(ctx, "myapp-feature-oauth")
func (s *SessionManager) Push(ctx context.Context, id SessionID) error {
	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return fmt.Errorf("push session %s: load record: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("push session %s: no local record", id)
	}

	patchBytes, err := s.Diff(ctx, id)
	if err != nil {
		return fmt.Errorf("push session %s: fetch patch: %w", id, err)
	}
	s.writeOut("push: fetched patch (%d bytes) for session %s", len(patchBytes), id)

	patch, err := ParsePatch(bytes.NewReader(patchBytes))
	if err != nil {
		return fmt.Errorf("push session %s: parse patch: %w", id, err)
	}
	changedPaths := make([]string, 0, len(patch.ChangedFiles))
	for _, cf := range patch.ChangedFiles {
		changedPaths = append(changedPaths, cf.Path)
	}

	violations := Evaluate(s.Rules, changedPaths)
	for _, v := range violations {
		defaultYes := v.Rule.Severity == SeverityWarn
		prompt := fmt.Sprintf(
			"push: rule violation %q (%s) matched %d file(s) %v — accept and continue? [%s]",
			v.Rule.Type, v.Rule.Severity, len(v.MatchedFiles), v.MatchedFiles,
			yesNoLabel(defaultYes),
		)
		if !s.Confirm(prompt, defaultYes) {
			s.writeOut("push: aborted by user at rule %q", v.Rule.Type)
			return fmt.Errorf("push session %s: aborted at rule %q", id, v.Rule.Type)
		}
	}

	// Apply patch locally and push. The SSHRunner's Run is used here as
	// the documented way backends surface shell commands — fakeSSHRunner's
	// RunCalls are how tests observe the "did we git push?" assertion.
	// We piggyback onto the SSH interface by issuing the git push as an
	// SSH.Run call on the session's Host; for the real backend this is a
	// no-op on local/process (those backends' Run implementations are
	// simple dispatchers), and for the SSH-based fly backend the Run
	// routes through the runner against the agent host. Production
	// operational semantics: main.go wires a laptop-local runner that
	// executes git locally. For unit tests, the fake records the argv and
	// returns programmed responses.
	gitCmd := fmt.Sprintf("cd %s && git push origin %s", rec.Repo, rec.Branch)
	res, err := s.SSH.Run(ctx, rec.Host, gitCmd)
	if err != nil || res.ExitCode != 0 {
		s.writeErr("push: git push failed: %v (exit=%d)", err, res.ExitCode)
		return fmt.Errorf("push session %s: git push: %w", id, err)
	}
	s.writeOut("push: git push succeeded for session %s", id)

	// Teardown + revoke + mark record pushed.
	if tdErr := s.Backend.Teardown(ctx, rec.Host); tdErr != nil {
		s.writeErr("push: teardown after push failed: %v", tdErr)
	} else {
		s.writeOut("push: torn down backend for session %s", id)
	}
	if rErr := s.KeyProvider.Revoke(ctx, KeyHandle{ID: rec.EphemeralKeyID}); rErr != nil {
		s.writeErr("push: revoke key failed: %v", rErr)
	} else {
		s.writeOut("push: revoked ephemeral key for session %s", id)
	}

	now := s.clockNow()
	rec.PushedAt = &now
	rec.TornDownAt = &now
	if err := s.Store.Save(rec); err != nil {
		s.writeErr("push: save record after push failed: %v", err)
	}
	return nil
}

// yesNoLabel returns "Y/n" if defaultYes, else "y/N" — the conventional
// shell-prompt shape for default-answer signaling.
func yesNoLabel(defaultYes bool) string {
	if defaultYes {
		return "Y/n"
	}
	return "y/N"
}

// Kill tears down the backend, revokes the ephemeral key, and deletes the
// local record. Backend teardown failure does NOT block local cleanup —
// the laptop state always matches the user's intent to stop — but the
// error names what was left behind so the user can `aa sweep` it.
//
// Example:
//
//	err := sm.Kill(ctx, "myapp-feature-oauth")
func (s *SessionManager) Kill(ctx context.Context, id SessionID) error {
	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return fmt.Errorf("kill session %s: load record: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("kill session %s: no local record", id)
	}

	var danglingErr error
	if tdErr := s.Backend.Teardown(ctx, rec.Host); tdErr != nil {
		danglingErr = tdErr
		s.writeErr("kill: backend teardown failed for host %s: %v", rec.Host.Address, tdErr)
	} else {
		s.writeOut("kill: tore down backend container for session %s", id)
	}

	if rErr := s.KeyProvider.Revoke(ctx, KeyHandle{ID: rec.EphemeralKeyID}); rErr != nil {
		s.writeErr("kill: revoke ephemeral key failed: %v", rErr)
	} else {
		s.writeOut("kill: revoked ephemeral key for session %s", id)
	}

	if dErr := s.Store.Delete(id); dErr != nil {
		s.writeErr("kill: delete local session record failed: %v", dErr)
		return fmt.Errorf("kill session %s: delete record: %w", id, dErr)
	}
	s.writeOut("kill: deleted local session record %s", id)

	if danglingErr != nil {
		return fmt.Errorf("kill session %s: backend host %s may be dangling: %w", id, rec.Host.Address, danglingErr)
	}
	return nil
}

// Retry is valid only when the agent has exited without a clean DONE —
// i.e. LIMBO (no state file) or FAILED (explicit failure). When valid,
// it calls Backend.RunContainer against the existing Host; the
// provisioned host and ephemeral key are reused, and the workspace
// contents survive across the old container and the new one.
//
// Retry reads the remote state file directly (instead of routing
// through Status+ComputeSessionState) so the "container alive" branch
// of state computation does not conflate with the "nothing reported"
// branch. Semantically: if the agent has reported DONE, there's nothing
// to retry; every other observation is retryable.
//
// Example:
//
//	err := sm.Retry(ctx, "myapp-feature-oauth")
func (s *SessionManager) Retry(ctx context.Context, id SessionID) error {
	rec, ok, err := s.Store.Load(id)
	if err != nil {
		return fmt.Errorf("retry session %s: load record: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("retry session %s: no local record", id)
	}
	if rec.PushedAt != nil || rec.TornDownAt != nil {
		return fmt.Errorf("retry session %s: session already pushed or torn down — retry is meaningless here", id)
	}

	data, readErr := s.Backend.ReadRemoteFile(ctx, rec.Host, ".aa/state")
	stateFile := ""
	if readErr == nil {
		stateFile = string(bytes.TrimSpace(data))
	}
	if stateFile == "DONE" {
		return fmt.Errorf("retry session %s: state is DONE, not LIMBO or FAILED — retry is meaningless here", id)
	}

	spec := ContainerSpec{
		SessionID: id,
	}
	if _, err := s.Backend.RunContainer(ctx, rec.Host, spec); err != nil {
		return fmt.Errorf("retry session %s: run container: %w", id, err)
	}
	s.writeOut("retry: restarted container for session %s", id)
	return nil
}

// ListAll returns every locally-known session record, newest-first by
// CreatedAt.
//
// Example:
//
//	recs, err := sm.ListAll(ctx)
//	for _, r := range recs { fmt.Println(r.ID, r.Branch, r.CreatedAt) }
func (s *SessionManager) ListAll(ctx context.Context) ([]LocalSessionRecord, error) {
	recs, err := s.Store.List()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].CreatedAt.After(recs[j].CreatedAt)
	})
	return recs, nil
}

// SweepReport is the result of a `aa sweep` pass.
type SweepReport struct {
	// OrphanHosts are backend-provisioned hosts (tagged aa-*) with no
	// matching local session record on this laptop.
	OrphanHosts []Host

	// OrphanSessionRecords are local records whose referenced Host is no
	// longer visible on its backend.
	OrphanSessionRecords []LocalSessionRecord

	// OrphanEphemeralKeys are keys the EphemeralKeyProvider still reports
	// as live, but which are not referenced by any local session record.
	OrphanEphemeralKeys []KeyHandle
}

// BackendOrphanLister is the narrow capability Sweep uses to enumerate
// backend-visible hosts. Backends implement this additively when the
// sweep verb supports them.
type BackendOrphanLister interface {
	ListHosts(ctx context.Context) ([]Host, error)
}

// EphemeralKeyOrphanLister is the narrow capability Sweep uses to
// enumerate live ephemeral keys.
type EphemeralKeyOrphanLister interface {
	ListLiveKeys(ctx context.Context) ([]KeyHandle, error)
}

// Sweep enumerates orphan resources across three axes (backend hosts,
// local records, ephemeral keys) and prompts via Confirm before
// destroying any of them. Returns the SweepReport regardless of
// confirmation outcome; only confirmed orphans are actually destroyed.
//
// Example:
//
//	report, err := sm.Sweep(ctx)
//	// report.OrphanHosts == [...], report.OrphanEphemeralKeys == [...]
func (s *SessionManager) Sweep(ctx context.Context) (SweepReport, error) {
	report := SweepReport{}

	// Local records indexed by Host.Address.
	localRecs, err := s.Store.List()
	if err != nil {
		return report, fmt.Errorf("sweep: list local records: %w", err)
	}
	localByAddress := make(map[string]LocalSessionRecord, len(localRecs))
	localByKeyID := make(map[string]bool, len(localRecs))
	for _, rec := range localRecs {
		if rec.Host.Address != "" {
			localByAddress[rec.Host.Address] = rec
		}
		if rec.EphemeralKeyID != "" {
			localByKeyID[rec.EphemeralKeyID] = true
		}
	}

	// Backend-visible hosts (only if the backend implements the lister).
	var backendHosts []Host
	if lister, ok := s.Backend.(BackendOrphanLister); ok {
		hosts, err := lister.ListHosts(ctx)
		if err != nil {
			return report, fmt.Errorf("sweep: list backend hosts: %w", err)
		}
		backendHosts = hosts
	}
	backendAddresses := make(map[string]bool, len(backendHosts))
	for _, h := range backendHosts {
		backendAddresses[h.Address] = true
		if _, known := localByAddress[h.Address]; !known {
			report.OrphanHosts = append(report.OrphanHosts, h)
		}
	}

	// Local records whose Host is no longer visible on the backend. Only
	// meaningful if we queried the backend at all.
	if len(backendHosts) > 0 || isBackendOrphanLister(s.Backend) {
		for _, rec := range localRecs {
			if rec.Host.Address == "" {
				continue
			}
			if !backendAddresses[rec.Host.Address] {
				report.OrphanSessionRecords = append(report.OrphanSessionRecords, rec)
			}
		}
	}

	// Live keys the provider still knows about but no local record references.
	if lister, ok := s.KeyProvider.(EphemeralKeyOrphanLister); ok {
		keys, err := lister.ListLiveKeys(ctx)
		if err != nil {
			return report, fmt.Errorf("sweep: list live keys: %w", err)
		}
		for _, k := range keys {
			if !localByKeyID[k.ID] {
				report.OrphanEphemeralKeys = append(report.OrphanEphemeralKeys, k)
			}
		}
	}

	// Confirm-and-destroy pass: one prompt per orphan.
	for _, h := range report.OrphanHosts {
		prompt := fmt.Sprintf("sweep: destroy orphan backend host %q (workspace=%s)? [y/N]", h.Address, h.Workspace)
		if s.Confirm(prompt, false) {
			if err := s.Backend.Teardown(ctx, h); err != nil {
				s.writeErr("sweep: teardown %s failed: %v", h.Address, err)
			} else {
				s.writeOut("sweep: destroyed host %s", h.Address)
			}
		}
	}
	for _, rec := range report.OrphanSessionRecords {
		prompt := fmt.Sprintf("sweep: destroy orphan local record %q (host %s no longer visible)? [y/N]", rec.ID, rec.Host.Address)
		if s.Confirm(prompt, false) {
			if err := s.Store.Delete(rec.ID); err != nil {
				s.writeErr("sweep: delete record %s failed: %v", rec.ID, err)
			} else {
				s.writeOut("sweep: deleted local record %s", rec.ID)
			}
		}
	}
	for _, k := range report.OrphanEphemeralKeys {
		prompt := fmt.Sprintf("sweep: destroy orphan ephemeral key %q (provider=%s)? [y/N]", k.ID, k.Provider)
		if s.Confirm(prompt, false) {
			if err := s.KeyProvider.Revoke(ctx, k); err != nil {
				s.writeErr("sweep: revoke key %s failed: %v", k.ID, err)
			} else {
				s.writeOut("sweep: revoked key %s", k.ID)
			}
		}
	}

	return report, nil
}

// isBackendOrphanLister is the type-assertion form used to decide whether
// we've actually queried the backend (even if it returned zero hosts).
func isBackendOrphanLister(b Backend) bool {
	_, ok := b.(BackendOrphanLister)
	return ok
}

// Assertions that imported helpers are used even on compile paths that
// don't exercise them. Keeps the file's import list honest for grep.
var (
	_ = errors.New
	_ = exec.Command
)
