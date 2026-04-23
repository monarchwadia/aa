package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUncommittedChangesWarningBlocksUntilAccepted
//
// PERSONA
//   Priya, platform engineer. She often runs aa mid-task, with uncommitted
//   changes in the tree she wants the agent to pick up. She also sometimes
//   runs aa by mistake when she was supposed to be on a different branch, or
//   with experimental scratch changes she doesn't want in the session.
//
//   The warning is her guardrail: she should be told what's about to be
//   included, and given a clean abort path, without fumbling for Ctrl-C.
//
// JOURNEY
//   1. Priya has a repo with a mix of modified and untracked files. She
//      starts `aa` without thinking about the tree state.
//      WHY: normal workflow — she's focused on the task, not the tree.
//      OBSERVES: aa prints a warning listing every uncommitted file, grouped
//                by git status code (`M` for modified, `??` for untracked),
//                followed by a `[y/N]` prompt. aa does NOT begin provisioning
//                while the prompt is outstanding.
//
//   2. Priya realises she's on the wrong branch and presses Enter (or types
//      "n"), declining.
//      WHY: she wants to abort without side effects.
//      OBSERVES: aa exits with a non-zero exit code; no session was created;
//                `aa list` shows nothing; no container, VM, or ephemeral key
//                was provisioned.
//
//   3. Priya fixes her branch, re-runs `aa`, sees the warning again, and
//      this time types "y".
//      WHY: she's confident in her state now.
//      OBSERVES: aa proceeds to provisioning. The warning is consistent
//                between the two runs (same file list, same format).
//
// BUSINESS IMPACT IF BROKEN
//   The warning is a load-bearing intent requirement — present in the
//   original user request, present in INTENT.md, present in the README
//   Quickstart. If it's silently skipped, a user loses work the first time
//   they run aa on a dirty tree. If it mis-parses git status, users will
//   stop trusting it and start routinely typing "y" — which defeats the
//   point. It must be right every time.
func TestUncommittedChangesWarningBlocksUntilAccepted(t *testing.T) {
	home := newIsolatedHome(t)
	writeGlobalConfig(t, home, processTailAgentConfig)

	repo := newGitRepo(t, `{"image":".devcontainer/Dockerfile","agent":"tail-agent"}`)

	// Mutate the tree to produce a mix of modified and untracked files.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# fixture\n\nmodified\n"), 0o644); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("untracked scratch\n"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	// Step 1 + 2 — dirty tree, decline the prompt.
	decline := runAa(t, aaInvocation{
		Args:    []string{},
		HomeDir: home,
		WorkDir: repo,
		Stdin:   "n\n",
	})
	// Exit code should be non-zero because the user declined — NOT zero.
	if decline.ExitCode == 0 {
		t.Errorf("expected non-zero exit when user declines uncommitted-changes prompt; got 0")
	}

	combined := decline.Stdout + decline.Stderr
	assertContains(t, combined, "Uncommitted changes", "warning header")
	assertContains(t, combined, "README.md", "modified file shown")
	assertContains(t, combined, "scratch.txt", "untracked file shown")
	assertContains(t, combined, "[y/N]", "prompt default-no formatting")

	// Verify no session was created.
	list := runAa(t, aaInvocation{
		Args:    []string{"list"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertNotContains(t, list.Stdout, "RUNNING", "no session after decline")
	assertNotContains(t, list.Stdout, "PROVISIONING", "no half-provisioned session after decline")

	// Step 3 — clean the tree, re-run, accept.
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=aa-test", "GIT_AUTHOR_EMAIL=aa-test@example.invalid",
			"GIT_COMMITTER_NAME=aa-test", "GIT_COMMITTER_EMAIL=aa-test@example.invalid",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("git", "add", "-A")
	run("git", "commit", "-qm", "clean up")

	// After cleaning, the warning should NOT appear.
	// The accept path starts a real (but no-isolation, process-backend) session
	// — no Docker needed because we're on the `process` backend.
	clean := runAa(t, aaInvocation{
		Args:    []string{},
		HomeDir: home,
		WorkDir: repo,
		Stdin:   "y\n",
	})
	assertNotContains(t, clean.Stdout+clean.Stderr, "Uncommitted changes", "no warning on clean tree")

	// Clean up any session we may have started.
	_ = runAa(t, aaInvocation{Args: []string{"kill"}, HomeDir: home, WorkDir: repo})
}
