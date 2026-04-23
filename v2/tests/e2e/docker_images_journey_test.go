// Package e2e contains end-to-end user-journey tests for the aa v2 CLI.
//
// PERSONA
//   Priya, solo backend developer shipping a small personal API to the cloud
//   backend. She has already stored her cloud-backend token with `aa config`
//   once, months ago. She knows her Dockerfile works. She does NOT want to
//   learn `docker login`, registry URLs, or tag namespacing. She reaches for
//   one tool (`aa`) and expects it to do the whole artifact-to-registry dance.
//
// JOURNEY
//   1. Priya has a directory `myapi/` containing a minimal Dockerfile
//      (`FROM scratch`). Her cloud-backend token is already configured.
//      WHY: this is her real starting state — code on disk, credentials
//      previously set up, no ambient `docker login` session.
//      OBSERVES: nothing yet; preconditions held by the sandbox.
//
//   2. Priya runs `aa docker image build ./myapi`.
//      WHY: she wants a local image tagged with something she can later
//      push, without inventing a tag string herself.
//      OBSERVES: the fake `docker` binary receives
//      `build -t registry.fly.io/aa-apps/myapi:latest <path>`; aa exits 0
//      and stdout shows the fully-qualified tag so she knows what was built.
//
//   3. Priya runs `aa docker image push registry.fly.io/aa-apps/myapi:latest`.
//      WHY: ship the just-built artifact to the private registry using only
//      her stored token — no `docker login` of her own.
//      OBSERVES: the fake `docker` binary is invoked with `login` against
//      registry.fly.io and then `push <tag>`. aa exits 0.
//
//   4. Priya runs `aa docker image ls`.
//      WHY: confirm the artifact is actually in the registry before she
//      spawns a machine against it.
//      OBSERVES: at least one line of output containing the tag she pushed.
//
//   5. Priya runs `aa docker image rm registry.fly.io/aa-apps/myapi:latest`
//      and then `aa docker image ls` again.
//      WHY: she finished the experiment and wants the tag gone; she expects
//      the subsequent listing to reflect the deletion.
//      OBSERVES: rm exits 0; the follow-up ls does NOT contain the removed
//      tag.
//
//   6. Priya (or the agent on her behalf) fat-fingers a build against a
//      directory that does not exist / has no Dockerfile.
//      WHY: mistakes happen; she expects a loud, named error.
//      OBSERVES: non-zero exit; stderr mentions the missing Dockerfile.
//
// BUSINESS IMPACT IF BROKEN
//   The single-tool "Dockerfile → running instance" promise is the product's
//   differentiator over "just use docker + flyctl." If build/push/ls/rm break,
//   the user must context-switch to two tools and two credential stores —
//   exactly the seam aa was supposed to remove. Every onboarding user and
//   every LLM-authored session that tries to ship an image dies here. The
//   missing-Dockerfile negative path protects against silent "command not
//   found" fallout that sends users hunting through docker's docs instead
//   of aa's.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aa/v2/testhelpers"
)

// TestDockerImagesJourney exercises build → push → ls → rm plus one negative
// case (missing Dockerfile). One sandbox, one journey, no ordering between
// this test and any other e2e test.
func TestDockerImagesJourney(t *testing.T) {
	t.Skip("snapshot generation deferred — TODO: hand-craft tests/testdata/snapshots/docker_images_journey.json or run with AA_TEST_RECORD=1 against a live Fly registry to capture HTTP exchanges")
	sandbox := testhelpers.NewSandbox(t, "docker_images_journey")

	// Step 1: pre-stage. A temp dir with a minimal Dockerfile named myapi.
	projectDir := filepath.Join(t.TempDir(), "myapi")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir myapi: %v", err)
	}
	dockerfilePath := filepath.Join(projectDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	const expectedTag = "registry.fly.io/aa-apps/myapi:latest"

	// Fake docker: swallow build, login, push, rm, and ls-equivalent calls.
	// We don't over-constrain exact argv here because aa may add flags
	// (e.g. --quiet, --platform) the architecture does not pin. We assert on
	// the recorded invocations after RunAA returns.
	sandbox.ExpectBinary("docker", testhelpers.RespondExitCode(0))

	// Step 2: build.
	buildResult := sandbox.RunAA(t, []string{"docker", "image", "build", projectDir}, nil)
	if buildResult.ExitCode != 0 {
		t.Fatalf("aa docker image build: exit=%d stderr=%s", buildResult.ExitCode, buildResult.Stderr)
	}
	if !strings.Contains(buildResult.Stdout, expectedTag) {
		t.Errorf("aa docker image build stdout missing tag %q\nstdout=%s", expectedTag, buildResult.Stdout)
	}

	dockerCalls := sandbox.BinaryInvocations("docker")
	if len(dockerCalls) == 0 {
		t.Fatalf("expected docker to be invoked by build; got zero invocations")
	}
	buildCall := findInvocationContaining(dockerCalls, "build")
	if buildCall == nil {
		t.Fatalf("no docker invocation contained 'build'; invocations=%v", dockerCalls)
	}
	buildArgvJoined := strings.Join(buildCall.Argv, " ")
	// Architecture pins: `docker build -t <tag> <context>`. The `-t <tag>`
	// pairing and the context path are the invariants. Flag ORDER beyond that
	// is not pinned by the architecture doc, so we use Contains on the joined
	// argv for the non-pinned parts.
	if !containsAdjacentPair(buildCall.Argv, "-t", expectedTag) {
		t.Errorf("build invocation missing `-t %s` pair; argv=%v", expectedTag, buildCall.Argv)
	}
	if !strings.Contains(buildArgvJoined, projectDir) {
		t.Errorf("build invocation missing build context %q; argv=%v", projectDir, buildCall.Argv)
	}

	// Step 3: push. Expect login-then-push sequencing.
	pushResult := sandbox.RunAA(t, []string{"docker", "image", "push", expectedTag}, nil)
	if pushResult.ExitCode != 0 {
		t.Fatalf("aa docker image push: exit=%d stderr=%s", pushResult.ExitCode, pushResult.Stderr)
	}
	allCalls := sandbox.BinaryInvocations("docker")
	loginIdx := indexOfInvocationContaining(allCalls, "login")
	pushIdx := indexOfInvocationContainingPair(allCalls, "push", expectedTag)
	if loginIdx < 0 {
		t.Errorf("expected a `docker login ...` invocation before push; invocations=%v", allCalls)
	}
	if pushIdx < 0 {
		t.Errorf("expected a `docker push %s` invocation; invocations=%v", expectedTag, allCalls)
	}
	if loginIdx >= 0 && pushIdx >= 0 && loginIdx >= pushIdx {
		t.Errorf("expected login (idx=%d) to precede push (idx=%d)", loginIdx, pushIdx)
	}

	// Step 4: ls shows the tag.
	lsResult := sandbox.RunAA(t, []string{"docker", "image", "ls"}, nil)
	if lsResult.ExitCode != 0 {
		t.Fatalf("aa docker image ls: exit=%d stderr=%s", lsResult.ExitCode, lsResult.Stderr)
	}
	if !anyLineContains(lsResult.Stdout, expectedTag) {
		t.Errorf("aa docker image ls did not show %q; stdout=%s", expectedTag, lsResult.Stdout)
	}

	// Step 5: rm, then ls again — tag should be gone.
	rmResult := sandbox.RunAA(t, []string{"docker", "image", "rm", expectedTag}, nil)
	if rmResult.ExitCode != 0 {
		t.Fatalf("aa docker image rm: exit=%d stderr=%s", rmResult.ExitCode, rmResult.Stderr)
	}
	lsAfterRm := sandbox.RunAA(t, []string{"docker", "image", "ls"}, nil)
	if lsAfterRm.ExitCode != 0 {
		t.Fatalf("aa docker image ls (post-rm): exit=%d stderr=%s", lsAfterRm.ExitCode, lsAfterRm.Stderr)
	}
	if strings.Contains(lsAfterRm.Stdout, expectedTag) {
		t.Errorf("aa docker image ls after rm still shows %q; stdout=%s", expectedTag, lsAfterRm.Stdout)
	}

	// Step 6: negative — build against a path with no Dockerfile.
	missing := filepath.Join(t.TempDir(), "nonexistent-path-xyz")
	negResult := sandbox.RunAA(t, []string{"docker", "image", "build", missing}, nil)
	if negResult.ExitCode == 0 {
		t.Errorf("aa docker image build on missing path: expected non-zero exit, got 0; stdout=%s stderr=%s",
			negResult.Stdout, negResult.Stderr)
	}
	if !strings.Contains(strings.ToLower(negResult.Stderr), "dockerfile") {
		t.Errorf("aa docker image build on missing path: stderr should mention Dockerfile; stderr=%s", negResult.Stderr)
	}
}

// findInvocationContaining returns the first invocation whose argv contains token as any element.
func findInvocationContaining(invocations []testhelpers.Invocation, token string) *testhelpers.Invocation {
	idx := indexOfInvocationContaining(invocations, token)
	if idx < 0 {
		return nil
	}
	return &invocations[idx]
}

func indexOfInvocationContaining(invocations []testhelpers.Invocation, token string) int {
	for i, inv := range invocations {
		for _, arg := range inv.Argv {
			if arg == token {
				return i
			}
		}
	}
	return -1
}

func indexOfInvocationContainingPair(invocations []testhelpers.Invocation, first, second string) int {
	for i, inv := range invocations {
		hasFirst := false
		hasSecond := false
		for _, arg := range inv.Argv {
			if arg == first {
				hasFirst = true
			}
			if arg == second {
				hasSecond = true
			}
		}
		if hasFirst && hasSecond {
			return i
		}
	}
	return -1
}

// containsAdjacentPair reports whether argv contains `first` immediately followed by `second`.
func containsAdjacentPair(argv []string, first, second string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == first && argv[i+1] == second {
			return true
		}
	}
	return false
}

// anyLineContains reports whether any line of s contains substr.
func anyLineContains(s, substr string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}
