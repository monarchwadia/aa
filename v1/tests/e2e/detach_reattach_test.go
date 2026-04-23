package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestSessionSurvivesLaptopCloseAndReattaches
//
// PERSONA
//   Raj, solo senior engineer on a client contract. He's starting a long-
//   running agent task at 5pm, packing his laptop, and expects to come back
//   tomorrow morning to a session that kept running overnight, with all the
//   scrollback preserved. This is the core promise of the product.
//
// JOURNEY
//   1. Raj runs `aa` in his project repo with a stubbed long-running "agent"
//      configured (a simple `tail -f $AA_WORKSPACE/.aa/agent.log` run command, so
//      the test is deterministic and doesn't cost API quota).
//      WHY: he wants to start a session that will keep running unattended.
//      OBSERVES: aa provisions, attaches, prints the session-start banner,
//                then enters attach mode. `aa list` in another terminal shows
//                the session as RUNNING.
//
//   2. Raj sends the tmux detach key sequence (simulated by the test by
//      killing the foreground aa process — from aa's perspective,
//      identical to his SSH session going away when his laptop sleeps).
//      WHY: he's closing the laptop. The session must survive.
//      OBSERVES: the local aa process exits; after a short settle, `aa status`
//                in a new shell still reports RUNNING; the session state file
//                on the agent host shows a recent heartbeat; no teardown
//                occurred.
//
//   3. Raj (in a new shell simulating tomorrow morning) runs `aa` again in
//      the same repo.
//      WHY: he wants to reattach and see what happened while he was away.
//      OBSERVES: aa reattaches to the SAME session (same container, same
//                process tree, same workspace), without re-provisioning. The
//                attach UI shows scrollback accumulated while he was
//                detached.
//
//   4. Raj runs `aa kill` to clean up.
//      WHY: he's done experimenting with this journey test, doesn't want to
//           leak a container.
//      OBSERVES: teardown completes; `aa list` no longer shows the session.
//
// BUSINESS IMPACT IF BROKEN
//   The "walk away" promise is THE product differentiator. If sessions die
//   when the laptop closes, aa is no better than running `claude` in a
//   terminal — and worse, because it adds setup overhead for no benefit.
//   Every pitch, every marketing claim, every architectural decision we
//   made about remote hosts evaporates if this journey fails.
func TestSessionSurvivesLaptopCloseAndReattaches(t *testing.T) {
	home := newIsolatedHome(t)
	writeGlobalConfig(t, home, processTailAgentConfig)
	fakeAPI := startFakeAnthropic(t)
	_ = fakeAPI // no API calls asserted in this journey; lifecycle tracked elsewhere

	repo := newGitRepo(t, `{"image":".devcontainer/Dockerfile","agent":"tail-agent"}`)

	// Step 1 — start the session. We run aa in a short-lived mode so the test
	// doesn't hang; the implementation must support a `--detach-after-start`
	// flag OR we drive detach by killing the process. For the test, we run
	// with a deadline and expect aa to print provisioning + attach lines.
	startRun := runAa(t, aaInvocation{
		Args:     []string{},
		HomeDir:  home,
		WorkDir:  repo,
		Stdin:    "", // no interactive input; agent is non-interactive tail
		Deadline: 30 * time.Second,
	})
	// With a deadline and non-interactive stdin, aa should print the banner
	// then block on attach; the deadline kills it (simulating laptop close).
	// Whether aa exits 0 or -1 doesn't matter for this step — what matters is
	// the observable artifact: the session exists on the backend after we return.
	_ = startRun

	// Step 2 — simulate "laptop closed". aa process is already gone.
	// Query `aa status` from a fresh invocation and assert RUNNING.
	time.Sleep(500 * time.Millisecond)
	status := runAa(t, aaInvocation{
		Args:    []string{"status"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertExitCode(t, status.ExitCode, 0, "aa status after detach")
	assertContains(t, status.Stdout, "RUNNING", "aa status output while detached")

	// Step 3 — reattach. We exercise `aa` again with a short deadline and
	// assert the output says "reattach" / "attaching to existing session"
	// rather than "provisioning".
	reattach := runAa(t, aaInvocation{
		Args:     []string{},
		HomeDir:  home,
		WorkDir:  repo,
		Deadline: 10 * time.Second,
	})
	combined := reattach.Stdout + reattach.Stderr
	if strings.Contains(combined, "Provisioning") {
		t.Errorf("expected reattach, not re-provisioning. Output:\n%s", combined)
	}
	assertContains(t, combined, "reattach", "aa reattach output")

	// Step 4 — clean kill.
	kill := runAa(t, aaInvocation{
		Args:    []string{"kill"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertExitCode(t, kill.ExitCode, 0, "aa kill")

	final := runAa(t, aaInvocation{
		Args:    []string{"list"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertNotContains(t, final.Stdout, "RUNNING", "aa list after kill")
}

// processTailAgentConfig is a global config that uses the `process` backend —
// no Docker required — and registers a deterministic non-LLM "tail" agent so
// this journey runs without API keys, quota, or containers.
//
// The agent writes to $AA_WORKSPACE/.aa/agent.log per the agent-environment
// contract (Concepts § "The agent ↔ aa environment contract"). The test
// harness sets AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 automatically (helpers.go).
const processTailAgentConfig = `{
  "default_backend": "process",
  "backends": {
    "process": {
      "type": "process",
      "egress_enforcement": "none"
    }
  },
  "agents": {
    "tail-agent": {
      "run": "sh -c 'while true; do echo tick >> $AA_WORKSPACE/.aa/agent.log; sleep 1; done'",
      "env": {},
      "egress_allowlist": ["*"]
    }
  },
  "rules": []
}`
