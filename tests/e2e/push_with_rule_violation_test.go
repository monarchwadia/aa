package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPushWithCIRuleViolationDefaultsToAbort
//
// PERSONA
//   Ray, platform engineer. His team requires that any change to CI
//   workflows is reviewed by a human. He uses aa to let agents attempt
//   feature work overnight, but absolutely refuses to accept anything that
//   touches `.github/workflows/` without a conscious decision.
//
//   aa's `ciConfigChanged` rule is configured with severity `error`
//   precisely to protect Ray. The UX contract in the README is explicit:
//   an `error`-severity violation flips the prompt default to abort; the
//   user must actively type `y` to push. Skimming and hitting Enter is
//   SAFE — it aborts.
//
// JOURNEY
//   1. Ray's agent has finished a task and edited
//      `.github/workflows/deploy.yml` along with some legitimate code.
//      The session is in state DONE with a committed patch on the agent
//      host.
//      WHY: agents sometimes touch workflow files — e.g. "add a job that
//           runs the new tests". Often legitimate; sometimes not.
//      OBSERVES: nothing yet; Ray has just come back to his desk.
//
//   2. Ray runs `aa push`.
//      WHY: he wants to ship the work if it's clean.
//      OBSERVES:
//         - aa fetches the patch from the agent host.
//         - aa shows a rule-violation banner naming the rule
//           (`ciConfigChanged`), the severity (`error`), the listed files,
//           and the documented attack vectors from the rule docstring.
//         - The prompt reads something like
//           `[r] view full diff [s] view flagged files [a] accept and push [q] abort`
//           and the DEFAULT action (on bare-Enter) is abort — because the
//           rule severity is `error`.
//
//   3. Ray presses Enter.
//      WHY: simulates the realistic "I skimmed and I'm tired" case. The
//           product must not treat a tired skim as consent.
//      OBSERVES: aa aborts. No `git push` is attempted. The session
//                remains intact (he can review again later with `aa diff`
//                or push explicitly).
//
//   4. Ray re-runs `aa push` and types `y`.
//      WHY: he has now actually reviewed and is deliberately accepting.
//      OBSERVES: aa applies the patch locally and pushes. The push is
//                attempted exactly once; the remote (a local bare repo
//                acting as origin in this test) receives the commits.
//
// BUSINESS IMPACT IF BROKEN
//   If the error-severity rule does not default to abort, the review gate
//   is a paper tiger. CI config changes are a classic supply-chain pivot;
//   a compromised agent who gets an automatic or default-yes push through
//   has de facto compromised deploy credentials. Users will stop adopting
//   any config that includes sensitive files in `error` severity, which
//   defeats the whole `rules` mechanism. The product needs this to be
//   robust.
func TestPushWithCIRuleViolationDefaultsToAbort(t *testing.T) {
	home := newIsolatedHome(t)
	writeGlobalConfig(t, home, processConfigWithCIRule)

	origin := newBareOriginRepo(t)
	repo := newGitRepoWithOrigin(t, origin, `{"image":".devcontainer/Dockerfile","agent":"agent-that-edits-ci"}`)

	// For the e2e, the "agent" is a shell script baked into the image-less
	// setup: it writes a workflow file, commits, signals DONE. We express
	// its effect by pre-populating the session's patch via the implementation's
	// test-mode hook. If the implementation does not expose such a hook, the
	// alternative is to run the real agent path — which is heavier but still
	// hits the same aa push behaviour.
	preloadPatch(t, home, repo, patchThatTouchesWorkflow)

	// Step 2 + 3: first push, default-Enter, expect abort.
	pushDefault := runAa(t, aaInvocation{
		Args:    []string{"push"},
		HomeDir: home,
		WorkDir: repo,
		Stdin:   "\n", // bare Enter
	})
	combined := pushDefault.Stdout + pushDefault.Stderr
	assertContains(t, combined, "ciConfigChanged", "rule name in violation banner")
	assertContains(t, combined, ".github/workflows/deploy.yml", "violating file named")
	assertContains(t, combined, "error", "severity shown")
	assertContains(t, combined, "abort", "abort option present")
	if pushDefault.ExitCode == 0 {
		t.Fatalf("expected non-zero exit for default-abort path; got 0.\nOutput:\n%s", combined)
	}

	if originHasPatch(t, origin) {
		t.Fatalf("origin received a push despite default-abort path")
	}

	// Step 4: re-run, explicit accept.
	pushAccept := runAa(t, aaInvocation{
		Args:    []string{"push"},
		HomeDir: home,
		WorkDir: repo,
		Stdin:   "a\n", // explicit accept key from the README prompt
	})
	if pushAccept.ExitCode != 0 {
		t.Fatalf("expected explicit accept to succeed; exit=%d stderr=%q",
			pushAccept.ExitCode, pushAccept.Stderr)
	}
	if !originHasPatch(t, origin) {
		t.Fatalf("origin did not receive the push after explicit accept")
	}

	_ = runAa(t, aaInvocation{Args: []string{"kill"}, HomeDir: home, WorkDir: repo})
}

// processConfigWithCIRule ships the `ciConfigChanged` rule at error severity
// and the `process` backend so the test focuses on the push path without
// requiring Docker. The agent "run" is `true` (immediate exit) because this
// journey exercises aa push, not agent execution — the patch is preloaded.
const processConfigWithCIRule = `{
  "default_backend": "process",
  "backends": {"process": {"type": "process", "egress_enforcement": "none"}},
  "agents": {
    "agent-that-edits-ci": {
      "run": "true",
      "env": {},
      "egress_allowlist": ["*"]
    }
  },
  "rules": [
    {"type": "ciConfigChanged", "severity": "error"}
  ]
}`

// patchThatTouchesWorkflow is a valid unified-diff-format patch (as produced
// by `git format-patch`) that modifies `.github/workflows/deploy.yml`. Kept
// inline for test self-containment; real journeys don't need to read it.
const patchThatTouchesWorkflow = `From aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa Mon Sep 17 00:00:00 2001
From: aa-test <aa-test@example.invalid>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] agent: adjust deploy workflow

---
 .github/workflows/deploy.yml | 3 ++-
 1 file changed, 2 insertions(+), 1 deletion(-)

diff --git a/.github/workflows/deploy.yml b/.github/workflows/deploy.yml
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/.github/workflows/deploy.yml
@@ -0,0 +1,3 @@
+name: deploy
+on: [push]
+jobs: {smoke: {runs-on: ubuntu-latest, steps: [{run: echo hi}]}}
--
2.40.0

`

// newBareOriginRepo creates a bare git repo to serve as `origin` for the
// push assertion. Returns its absolute path.
func newBareOriginRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", "-q", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

// newGitRepoWithOrigin creates a working-tree repo whose `origin` remote
// points at the provided bare repo path.
func newGitRepoWithOrigin(t *testing.T, origin, aaJSON string) string {
	t.Helper()
	dir := newGitRepo(t, aaJSON)
	cmd := exec.Command("git", "remote", "add", "origin", origin)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	push := exec.Command("git", "push", "-q", "origin", "main")
	push.Dir = dir
	push.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=aa-test", "GIT_AUTHOR_EMAIL=aa-test@example.invalid",
		"GIT_COMMITTER_NAME=aa-test", "GIT_COMMITTER_EMAIL=aa-test@example.invalid",
	)
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("seed push: %v\n%s", err, out)
	}
	return dir
}

// preloadPatch simulates an agent that has already finished and written a
// result.patch to its workspace. The implementation must expose a
// test-mode hook to accept a pre-made patch file without actually running an
// agent; if that's missing, this helper is the pressure point forcing it.
func preloadPatch(t *testing.T, home, repo, patch string) {
	t.Helper()
	stateDir := filepath.Join(home, ".aa", "testfixtures")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", stateDir, err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "preload.patch"), []byte(patch), 0o644); err != nil {
		t.Fatalf("write preload.patch: %v", err)
	}
	_ = repo
}

// originHasPatch returns true if the bare origin repo has received at least
// one commit beyond the seed.
func originHasPatch(t *testing.T, origin string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", origin, "rev-list", "--count", "main")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-list: %v\n%s", err, out)
	}
	return string(out) != "1\n" && string(out) != "0\n"
}
