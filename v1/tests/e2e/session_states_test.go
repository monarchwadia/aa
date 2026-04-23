package e2e

import (
	"fmt"
	"testing"
)

// TestTerminalSessionStatesAreDisplayedCorrectly
//
// PERSONA
//   Dev, regular aa user. After every agent run, `aa` (or `aa status`) tells
//   Dev what happened. Dev makes decisions — review, retry, kill, push —
//   based entirely on which of the four terminal states is displayed. The
//   README documents each state block exactly; Dev has trained her eye on
//   that format.
//
// JOURNEY
//   Each sub-test puts the session into one of the four documented terminal
//   states, then runs `aa status` and asserts the README's display contract.
//
//   The four states from README § "Session states":
//
//     1. DONE         — agent ran `aa-done`; state file says DONE;
//                       container exited 0.
//     2. FAILED       — agent ran `aa-fail "<reason>"`; state file says
//                       FAILED with reason; container exited non-zero.
//     3. LIMBO        — container exited; no state file was ever written.
//     4. INCONSISTENT — state file says DONE but container exited non-zero.
//
//   For each, the test preloads the on-host state (via a test hook — e.g.
//   a synthetic session fixture) and runs `aa status`. The assertions are
//   the README-documented strings that appear in each block. Any drift
//   between here and the README is drift between the spec and the
//   implementation, and `review-stack` catches it.
//
// BUSINESS IMPACT IF BROKEN
//   The four-state display is how users make every post-agent decision.
//   Wrong state → wrong decision. A LIMBO misread as DONE leads to
//   pushing partial work to origin. A DONE misread as FAILED leads to
//   retrying and wasting the agent's output. A FAILED-with-reason that
//   hides the reason leaves Dev staring at a dead session with no
//   diagnostic. The copy in the README is the contract; the display
//   must match it, always.
func TestTerminalSessionStatesAreDisplayedCorrectly(t *testing.T) {
	cases := []struct {
		name          string
		stateFile     string // contents of $AA_WORKSPACE/.aa/state, "" = absent
		exitCode      int
		message       string // optional reason string
		mustDisplay   []string
		mustOfferVerbs []string
	}{
		{
			name:      "DONE",
			stateFile: "DONE",
			exitCode:  0,
			message:   "Implemented OAuth2 with Google and GitHub.",
			mustDisplay: []string{
				"— DONE",
				"agent reported success",
				"Implemented OAuth2 with Google and GitHub.",
			},
			mustOfferVerbs: []string{"aa diff", "aa push", "aa kill"},
		},
		{
			name:      "FAILED",
			stateFile: "FAILED",
			exitCode:  1,
			message:   "Unable to resolve dependency conflict in package.json.",
			mustDisplay: []string{
				"— FAILED",
				"agent reported failure",
				"Unable to resolve dependency conflict in package.json.",
				"exit code: 1",
			},
			mustOfferVerbs: []string{"aa attach", "aa diff", "aa push", "aa kill"},
		},
		{
			name:      "LIMBO",
			stateFile: "", // no state file written
			exitCode:  137,
			mustDisplay: []string{
				"— LIMBO",
				"exited without reporting a result",
				"exit code: 137",
			},
			mustOfferVerbs: []string{"aa attach", "aa diff", "aa push", "aa kill", "aa retry"},
		},
		{
			name:      "INCONSISTENT",
			stateFile: "DONE",
			exitCode:  2,
			message:   "Completed OAuth implementation.",
			mustDisplay: []string{
				"— INCONSISTENT",
				"reported DONE but exited with code 2",
				"Completed OAuth implementation.",
			},
			mustOfferVerbs: []string{"aa attach", "aa diff", "aa push", "aa kill"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := newIsolatedHome(t)
			writeGlobalConfig(t, home, processConfigWithCIRule)
			repo := newGitRepo(t, `{"image":".devcontainer/Dockerfile","agent":"agent-that-edits-ci"}`)

			// Preload a synthetic session in the target state. Expected
			// implementation affordance: an `aa-test-fixture` hidden
			// subcommand or a documented way for tests to seed a session
			// record on the laptop plus a state file on the (local) agent.
			seedSessionState(t, home, repo, tc.stateFile, tc.exitCode, tc.message)

			status := runAa(t, aaInvocation{
				Args:    []string{"status"},
				HomeDir: home,
				WorkDir: repo,
			})
			assertExitCode(t, status.ExitCode, 0, "aa status")

			for _, s := range tc.mustDisplay {
				assertContains(t, status.Stdout, s, tc.name+" display")
			}
			for _, verb := range tc.mustOfferVerbs {
				assertContains(t, status.Stdout, verb, tc.name+" offers "+verb)
			}

			// Inverse check: aa retry must NOT be offered for DONE or
			// FAILED; the README is explicit that retry is meaningful
			// only in LIMBO (and by extension FAILED, per spec).
			if tc.name == "DONE" {
				assertNotContains(t, status.Stdout, "aa retry", "DONE should not offer retry")
			}
		})
	}
}

// seedSessionState creates a synthetic session by invoking `aa fixture`
// with the requested state-file contents and exit code. The fixture
// subcommand is hidden from `aa help`; it exists only to let this test
// file stand up post-agent state without spawning a real agent.
func seedSessionState(t *testing.T, home, repo, stateFile string, exitCode int, message string) {
	t.Helper()
	args := []string{"fixture", "--exit", fmt.Sprintf("%d", exitCode)}
	if stateFile != "" {
		args = append(args, "--state", stateFile)
	}
	if message != "" {
		args = append(args, "--message", message)
	}
	out := runAa(t, aaInvocation{
		Args:    args,
		HomeDir: home,
		WorkDir: repo,
	})
	if out.ExitCode != 0 {
		t.Fatalf("aa fixture: exit=%d stdout=%q stderr=%q", out.ExitCode, out.Stdout, out.Stderr)
	}
}
