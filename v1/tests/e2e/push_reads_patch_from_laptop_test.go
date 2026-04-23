package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffAndPushTrustOnlyTheLaptop
//
// PERSONA
//   Sasha, security-conscious contributor. Has read the README section on
//   the security model and specifically the claim: "Agent forging the diff
//   you review — protected; patch is read as text over SSH and diff is
//   rendered locally; agent host is not in the review trust path."
//
//   Sasha wants to verify this is true. The threat she's modelling: a fully
//   compromised agent host could show a benign diff via `aa diff` while the
//   actual workspace contains a malicious patch. The product claim is that
//   aa's diff and push DO NOT trust the agent host for rendering.
//
// JOURNEY
//   1. A session on the agent host has completed (state DONE) with a real
//      patch written to `$AA_WORKSPACE/.aa/result.patch`.
//      WHY: normal post-agent state.
//      OBSERVES: nothing yet.
//
//   2. Sasha runs `aa diff`.
//      WHY: she wants to see what the agent wrote before deciding to push.
//      OBSERVES: aa reads the patch text over SSH (for a remote backend) or
//                via docker-exec (for the local backend), pipes it through
//                her local pager, and displays it. The text displayed is
//                EXACTLY the bytes from `$AA_WORKSPACE/.aa/result.patch` — no
//                server-side filtering, formatting, or colouring.
//
//   3. Simulating a hostile agent host: the test, between `aa diff` and
//      `aa push`, forcibly stops the agent container. Sasha then runs
//      `aa push`.
//      WHY: models the threat "the agent host goes evil (or goes dark)
//           between review and push". If the push silently re-fetched
//           content from the agent host, a live-then-hostile host could
//           show a clean diff and then deliver a malicious patch on push.
//      OBSERVES: `aa push` uses the patch bytes Sasha already reviewed,
//                NOT a fresh fetch. Either the push succeeds from the
//                cached patch, or it fails loudly with a clear "agent host
//                is unreachable; the reviewed patch is at <laptop path>,
//                use `git am` manually if you still want to ship" message.
//                It never silently re-fetches.
//
//   4. Sasha inspects her laptop for the cached patch.
//      WHY: she wants to confirm the patch lives on her laptop and can be
//           re-applied by hand.
//      OBSERVES: a file under `~/.aa/sessions/<id>/` holds the exact patch
//                text, and the local clone used for `git am` is preserved
//                until teardown (per README § "I ran aa push and the push
//                failed").
//
// BUSINESS IMPACT IF BROKEN
//   The "agent host can't forge the review" claim is explicit in the
//   README's security-model table. If `aa push` round-trips to the agent
//   host for the patch AFTER review, a compromised host can substitute a
//   malicious patch in the interval. All the patch-file architecture
//   (plan § 5 & 6) and the "no relay" collapse rest on this invariant.
//   Sasha walks away if it doesn't hold; so do all other security-minded
//   users.
func TestDiffAndPushTrustOnlyTheLaptop(t *testing.T) {
	home := newIsolatedHome(t)
	writeGlobalConfig(t, home, processConfigWithCIRule) // any well-formed config; rules aren't exercised here

	origin := newBareOriginRepo(t)
	repo := newGitRepoWithOrigin(t, origin, `{"image":".devcontainer/Dockerfile","agent":"agent-that-edits-ci"}`)
	preloadPatch(t, home, repo, benignPatch)

	// Step 2: diff reads the patch. Assert the literal patch header appears.
	diff := runAa(t, aaInvocation{
		Args:    []string{"diff"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertExitCode(t, diff.ExitCode, 0, "aa diff")
	assertContains(t, diff.Stdout, "From: aa-test", "patch header rendered locally")

	// Step 3: sever the agent host (kill the container forcefully). The
	// test hook expected here is an `aa-test-killhost` subcommand or a
	// documented mechanism — if the implementation chooses a different
	// affordance, update here. What matters: the host is gone before push.
	sever := runAa(t, aaInvocation{
		Args:    []string{"kill", "--host-only"}, // tear down compute, keep local record
		HomeDir: home,
		WorkDir: repo,
	})
	if sever.ExitCode != 0 {
		t.Fatalf("could not sever agent host: exit=%d stderr=%q", sever.ExitCode, sever.Stderr)
	}

	// Push — either succeeds from laptop-side cache, OR fails loudly. Both
	// are acceptable; silently re-fetching is NOT.
	push := runAa(t, aaInvocation{
		Args:    []string{"push"},
		HomeDir: home,
		WorkDir: repo,
		Stdin:   "a\n", // accept (no rule violations on benign patch)
	})
	combined := push.Stdout + push.Stderr

	if push.ExitCode != 0 {
		// Acceptable fail path: must include the word "unreachable" or
		// equivalent, AND must surface the laptop cache path.
		if !strings.Contains(combined, "unreachable") && !strings.Contains(combined, "host is gone") {
			t.Fatalf("push failed with unclear cause; must mention host unreachable.\nOutput:\n%s", combined)
		}
		assertContains(t, combined, ".aa/sessions/", "cached patch path shown in failure")
	} else {
		// Acceptable success path: patch was applied from the laptop cache,
		// and origin now has the commit.
		if !originHasPatch(t, origin) {
			t.Fatalf("push reported success but origin has no new commits")
		}
	}

	// Step 4: regardless of success/failure, the cached patch exists.
	// We walk ~/.aa/sessions/ and assert SOME file contains the patch bytes.
	// Exact path is implementation-defined; the invariant is "it's under
	// ~/.aa/sessions/".
	if !laptopHasCachedPatch(t, home, benignPatch) {
		t.Fatalf("laptop-side cached patch not found under ~/.aa/sessions/")
	}
}

const benignPatch = `From bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb Mon Sep 17 00:00:00 2001
From: aa-test <aa-test@example.invalid>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] agent: add a greeter

---
 greeter.txt | 1 +
 1 file changed, 1 insertion(+)

diff --git a/greeter.txt b/greeter.txt
new file mode 100644
index 0000000..2222222
--- /dev/null
+++ b/greeter.txt
@@ -0,0 +1 @@
+hello from the agent
--
2.40.0

`

// laptopHasCachedPatch walks ~/.aa/sessions/ looking for any file whose
// contents match the patch. Implementation may choose any exact filename;
// the invariant is that some laptop-side file holds the reviewed bytes.
func laptopHasCachedPatch(t *testing.T, home, patch string) bool {
	t.Helper()
	// Minimal search: expected path ~/.aa/sessions/<id>/result.patch OR
	// ~/.aa/sessions/<id>/reviewed.patch. If neither the implementation
	// writes, this test fails and prompts a spec clarification.
	candidates := []string{
		home + "/.aa/sessions",
	}
	for _, root := range candidates {
		if containsFileWithBytes(root, patch) {
			return true
		}
	}
	return false
}

// containsFileWithBytes does a minimal recursive search for a file whose
// contents contain `needle`. Intentionally simple; this is test code.
func containsFileWithBytes(root, needle string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
