package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKillTearsDownEverything
//
// PERSONA
//   Raj again, same solo engineer from the detach/reattach journey. This
//   time he's decided the agent went off the rails — it's burning API
//   quota on the wrong task. He wants OUT. No push, no patch, no cleanup
//   ambiguity. Just stop everything and leave no trace.
//
// JOURNEY
//   1. Raj has a running session. `aa list` shows it as RUNNING.
//      WHY: normal mid-run state.
//      OBSERVES: session exists; an ephemeral API key has been minted;
//                a container or VM is running on the backend.
//
//   2. Raj runs `aa kill` from the repo directory.
//      WHY: he wants to abandon the session. Fast.
//      OBSERVES: aa prints progress as it tears down each resource:
//                container stopped, egress rules removed, ephemeral key
//                revoked, laptop-side session file deleted. Exit 0.
//
//   3. Raj inspects the aftermath.
//      WHY: he wants to be SURE nothing is lingering (cost, security).
//      OBSERVES:
//         - `aa list` no longer shows the session.
//         - `aa status` in the repo reports no session.
//         - `~/.aa/sessions/<id>.json` is gone.
//         - The ephemeral API key is revoked (the fake Anthropic API saw
//           a DELETE for it).
//         - No container with the session's docker name is running.
//
// BUSINESS IMPACT IF BROKEN
//   `aa kill` is the "I changed my mind" escape hatch. If it leaves
//   compute running, users pay for an agent they wanted off. If it
//   leaves keys un-revoked, an exfiltrated key's window stays open
//   until TTL instead of immediately. If the laptop record lingers,
//   the user sees a ghost session and loses confidence in every
//   listing. `aa kill` must be atomic from the user's perspective:
//   one command in, everything out.
func TestKillTearsDownEverything(t *testing.T) {
	home := newIsolatedHome(t)
	fake := startFakeAnthropic(t)

	// Config routes aa's ephemeral-key calls at the fake API. The exact
	// config field for overriding the Anthropic API base URL is TBD per
	// intent; we use `admin_api_base_url` as the reasonable name. If the
	// implementation chooses differently, update here.
	writeGlobalConfig(t, home, `{
  "default_backend": "process",
  "backends": {"process": {"type": "process", "egress_enforcement": "none"}},
  "agents": {
    "tail-agent": {
      "run": "sh -c 'while true; do sleep 1; done'",
      "env": {"ANTHROPIC_API_KEY": "ephemeral"},
      "egress_allowlist": ["*"],
      "admin_api_base_url": "`+fake.Server.URL+`"
    }
  },
  "rules": []
}`)

	repo := newGitRepo(t, `{"image":".devcontainer/Dockerfile","agent":"tail-agent"}`)

	// Step 1 — start a session.
	_ = runAa(t, aaInvocation{
		Args:     []string{},
		HomeDir:  home,
		WorkDir:  repo,
		Deadline: 30 * time.Second,
		ExtraEnv: map[string]string{"ANTHROPIC_ADMIN_KEY": "test-admin-key"},
	})

	// Confirm a key was minted.
	keysBefore := fake.IssuedKeyIDs()
	if len(keysBefore) == 0 {
		t.Fatalf("expected at least one ephemeral key to have been minted; none observed")
	}

	// Confirm the session appears in `aa list`.
	listBefore := runAa(t, aaInvocation{
		Args:    []string{"list"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertContains(t, listBefore.Stdout, "RUNNING", "aa list before kill")

	// Step 2 — kill. The admin key must be present so Revoke goes through
	// the real provider (against the fake server) rather than the noop.
	kill := runAa(t, aaInvocation{
		Args:     []string{"kill"},
		HomeDir:  home,
		WorkDir:  repo,
		ExtraEnv: map[string]string{"ANTHROPIC_ADMIN_KEY": "test-admin-key"},
	})
	assertExitCode(t, kill.ExitCode, 0, "aa kill")
	// Kill output must mention each teardown step explicitly; users rely on
	// this to be sure nothing was missed.
	for _, step := range []string{"container", "key", "session"} {
		assertContains(t, kill.Stdout+kill.Stderr, step, "kill output mentions "+step)
	}

	// Step 3 — verify aftermath.

	// (a) aa list shows nothing.
	listAfter := runAa(t, aaInvocation{
		Args:    []string{"list"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertNotContains(t, listAfter.Stdout, "RUNNING", "aa list after kill")

	// (b) ~/.aa/sessions/ has no remaining session files (it may still exist
	// as an empty directory).
	sessionsDir := filepath.Join(home, ".aa", "sessions")
	if entries, err := os.ReadDir(sessionsDir); err == nil {
		var remaining []string
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".json") {
				remaining = append(remaining, name)
			}
		}
		if len(remaining) > 0 {
			t.Fatalf("~/.aa/sessions/ still contains session files after kill: %v", remaining)
		}
	}

	// (c) Every minted key was revoked.
	for _, id := range keysBefore {
		fake.AssertKeyRevoked(t, id)
	}
}
