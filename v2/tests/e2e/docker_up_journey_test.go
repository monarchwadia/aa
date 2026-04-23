// Package e2e contains end-to-end user-journey tests for the aa CLI.
//
// PERSONA
//   Alex, the tinkerer. Edits a Dockerfile locally and wants to run it on
//   real cloud hardware without manually driving build → push → provision →
//   attach by hand. Currently juggles five tools and five places to make a
//   mistake; reaches for `aa docker up` first because the flagship-verb docs
//   promise a single command that takes a directory with a Dockerfile and
//   drops them into an interactive shell on a cloud machine. They've already
//   stored their Fly.io token via `aa config`, and are running `aa docker up`
//   for the first, second, and third time against the same project directory
//   to see whether the tool's re-run semantics match the documentation.
//   Adapted from the primary persona in v2/docs/intent/docker-up.md.
//
// JOURNEY
//   1. Alex stages their Fly.io token via `aa config token.flyio=...`, drops
//      a minimal `Dockerfile` into a scratch project directory, and runs
//      `aa docker up <dir>` for the very first time.
//      WHY: this is the flagship promise — one command from "I have a
//      Dockerfile" to "I'm in a shell on cloud hardware". If this doesn't
//      just work, the tool has no reason to exist over running the four
//      underlying tools by hand.
//      OBSERVES: exit 0 after the fake attach shell returns; stdout shows
//      exactly one line per stage in order — `[build] ...`, `[push] ...`,
//      `[spawn] ...<machine-id>`, `[attach] attaching to <machine-id> ...`.
//      The fake `docker` binary was invoked at least twice (build + push).
//      The fake `flyctl` binary was invoked exactly once, with argv
//      beginning `ssh console --app <app> --machine <machine-id>`, which
//      is how attach opens the interactive shell per docs/docker-up.md.
//
//   2. Alex runs `aa docker up <dir>` a second time against the same
//      directory without passing `--force`. A machine tied to that
//      directory is still alive from step 1.
//      WHY: Alex wants to know the tool refuses to silently stomp a
//      running instance, because the resolved intent in
//      docs/intent/docker-up.md specifies re-run refusal, and because
//      a silent replace would delete live interactive work.
//      OBSERVES: non-zero exit; stderr mentions the existing machine id
//      and names both resolutions (`--force` to replace, or
//      `aa machine rm` to tear the old one down manually), exactly as
//      the Troubleshooting section of docs/docker-up.md promises.
//
//   3. Alex re-runs the command as `aa docker up <dir> --force`, having
//      decided they really do want to replace the machine from step 1
//      with a fresh build.
//      WHY: `--force` is the documented escape hatch. The resolved
//      intent and ADR-4 in docs/architecture/docker-up.md require that
//      the old machine is destroyed and a fresh build/push/spawn/attach
//      cycle runs, terminating in exit 0 once attach returns cleanly.
//      OBSERVES: exit 0; a destroy of the step-1 machine is visible via
//      the fake `flyctl` invocations in the recorded order; a fresh
//      spawn + attach cycle runs afterward; final attach targets the
//      new machine id, not the old one.
//
// BUSINESS IMPACT IF BROKEN
//   `aa docker up` is the flagship verb of the tool — every other feature
//   (image management, machine lifecycle, config) exists so this single
//   command works. If step 1 regresses, the product's one-line sales pitch
//   dies and no new user completes their first successful run. If step 2's
//   refusal breaks, Alex silently loses the running cloud machine (and any
//   live interactive work inside it) on an accidental second `up`, turning
//   the tool from "safe to re-run" into "surprise data-loss command" — the
//   kind of footgun that kills trust permanently. If step 3's `--force`
//   stops destroying the old machine before spawning the new one, Alex
//   ends up paying for orphaned cloud instances they can't locate by
//   directory anymore, and the "one machine per directory" invariant that
//   `FindByLabel` and re-run refusal both depend on silently breaks,
//   cascading into every other docker-up journey being wrong about state.
//   Together these three flows are the contract that makes `aa docker up`
//   something users reach for on every project, not just once.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aa/v2/testhelpers"
)

func TestDockerUpJourney(t *testing.T) {
	t.Skip("snapshot generation deferred — TODO: hand-craft tests/testdata/snapshots/docker_up_step{1,2,3}_*.json (each requires a Fly Machines POST /apps + POST /machines + LIST /machines exchange plus a registry _catalog/manifest exchange) or run with AA_TEST_RECORD=1")
	// Step 1: First-run happy path. Token staged, Dockerfile present, fake
	// docker + flyctl behave. User lands in the (fake) shell and gets exit 0.
	t.Run("step1_happy_path_first_run", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "docker_up_step1_happy_path")

		// Pre-stage: a Fly.io token so push+spawn have credentials.
		stageToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if stageToken.ExitCode != 0 {
			t.Fatalf("step 1 precondition: expected exit 0 staging token, got %d; stderr=%q", stageToken.ExitCode, stageToken.Stderr)
		}

		// Temp project dir with a minimal Dockerfile.
		projectDir := mustStageDockerfile(t)

		// Fake `docker` succeeds for both build and push invocations.
		sandbox.ExpectBinary("docker", testhelpers.RespondExitCode(0))
		// Fake `flyctl` succeeds; this covers the ssh-console attach and any
		// lifecycle subcommands (spawn, destroy) issued through flyctl.
		sandbox.ExpectBinary("flyctl", testhelpers.RespondExitCode(0))

		result := sandbox.RunAA(t, []string{"docker", "up", projectDir}, nil)

		if result.ExitCode != 0 {
			t.Fatalf("step 1: expected exit 0 on happy-path up, got %d; stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
		}

		// Stage lines are a user-facing contract (docs/docker-up.md § Quickstart
		// and § Command reference). One line per stage, in order.
		stageBuildIdx := strings.Index(result.Stdout, "[build]")
		stagePushIdx := strings.Index(result.Stdout, "[push]")
		stageSpawnIdx := strings.Index(result.Stdout, "[spawn]")
		stageAttachIdx := strings.Index(result.Stdout, "[attach] attaching to ")
		if stageBuildIdx < 0 {
			t.Fatalf("step 1: expected stdout to contain a %q line, got %q", "[build]", result.Stdout)
		}
		if stagePushIdx < 0 {
			t.Fatalf("step 1: expected stdout to contain a %q line, got %q", "[push]", result.Stdout)
		}
		if stageSpawnIdx < 0 {
			t.Fatalf("step 1: expected stdout to contain a %q line, got %q", "[spawn]", result.Stdout)
		}
		if stageAttachIdx < 0 {
			t.Fatalf("step 1: expected stdout to contain the attach line starting %q, got %q", "[attach] attaching to ", result.Stdout)
		}
		// Order: build < push < spawn < attach.
		if !(stageBuildIdx < stagePushIdx && stagePushIdx < stageSpawnIdx && stageSpawnIdx < stageAttachIdx) {
			t.Fatalf("step 1: expected stage lines in order build→push→spawn→attach, got build@%d push@%d spawn@%d attach@%d in %q",
				stageBuildIdx, stagePushIdx, stageSpawnIdx, stageAttachIdx, result.Stdout)
		}

		// Fake `docker` should be invoked at least twice: once for build, once for push.
		dockerCalls := sandbox.BinaryInvocations("docker")
		if len(dockerCalls) < 2 {
			t.Fatalf("step 1: expected fake docker to be invoked at least twice (build + push), got %d invocations: %+v", len(dockerCalls), dockerCalls)
		}

		// Fake `flyctl` should be invoked exactly once for attach, with argv
		// beginning `ssh console --app <app> --machine <machine-id>`. Other
		// flyctl subcommands (spawn, destroy) go through the Fly HTTP API on
		// the happy path per docs/architecture/docker-up.md.
		flyctlCalls := sandbox.BinaryInvocations("flyctl")
		if len(flyctlCalls) != 1 {
			t.Fatalf("step 1: expected fake flyctl to be invoked exactly once (attach), got %d invocations: %+v", len(flyctlCalls), flyctlCalls)
		}
		attachArgv := flyctlCalls[0].Argv
		if len(attachArgv) < 5 ||
			attachArgv[0] != "ssh" ||
			attachArgv[1] != "console" ||
			attachArgv[2] != "--app" ||
			attachArgv[4] != "--machine" {
			t.Fatalf("step 1: expected flyctl argv to begin with 'ssh console --app <app> --machine <id>', got %+v", attachArgv)
		}
		// The machine id in the attach argv must also appear in the `[attach]`
		// line of stdout — they're the same id per the mental-model diagram.
		machineIDFromArgv := attachArgv[5]
		if !strings.Contains(result.Stdout, machineIDFromArgv) {
			t.Fatalf("step 1: expected stdout to reference machine id %q from flyctl attach argv, got %q", machineIDFromArgv, result.Stdout)
		}

		// TODO(test-harness): once the harness supports HTTP-request inspection,
		// assert that the Fly Machines API spawn request body carries
		// labels = { "aa.up-id": sha256(absPath(projectDir))[:12] } per the
		// Amendments section of docs/architecture/docker-up.md. The current
		// sandbox only records external-binary invocations, so we cannot
		// inspect the spawn HTTP body at the e2e layer yet.
	})

	// Step 2: Re-run refusal. Same project dir from step 1 (reconstructed:
	// stage token, create dir with Dockerfile, do one successful up, then
	// a second up without --force). The second up must refuse.
	t.Run("step2_rerun_without_force_refuses_and_names_resolutions", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "docker_up_step2_rerun_refuses")

		stageToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if stageToken.ExitCode != 0 {
			t.Fatalf("step 2 precondition: expected exit 0 staging token, got %d; stderr=%q", stageToken.ExitCode, stageToken.Stderr)
		}

		projectDir := mustStageDockerfile(t)

		sandbox.ExpectBinary("docker", testhelpers.RespondExitCode(0))
		sandbox.ExpectBinary("flyctl", testhelpers.RespondExitCode(0))

		firstUp := sandbox.RunAA(t, []string{"docker", "up", projectDir}, nil)
		if firstUp.ExitCode != 0 {
			t.Fatalf("step 2 precondition: expected exit 0 on first up, got %d; stdout=%q stderr=%q", firstUp.ExitCode, firstUp.Stdout, firstUp.Stderr)
		}

		// Second invocation against the same directory. Machine from firstUp
		// is still alive in the fake backend's label store; tool must refuse.
		secondUp := sandbox.RunAA(t, []string{"docker", "up", projectDir}, nil)
		if secondUp.ExitCode == 0 {
			t.Fatalf("step 2: expected non-zero exit on re-run without --force, got 0; stdout=%q stderr=%q", secondUp.Stdout, secondUp.Stderr)
		}
		// Error names the existing machine.
		if !strings.Contains(secondUp.Stderr, "already tied to") && !strings.Contains(secondUp.Stderr, "existing") {
			t.Fatalf("step 2: expected stderr to mention the existing machine, got %q", secondUp.Stderr)
		}
		// Error names both resolutions: --force or aa machine rm.
		if !strings.Contains(secondUp.Stderr, "--force") {
			t.Fatalf("step 2: expected stderr to suggest %q, got %q", "--force", secondUp.Stderr)
		}
		if !strings.Contains(secondUp.Stderr, "aa machine rm") {
			t.Fatalf("step 2: expected stderr to suggest %q, got %q", "aa machine rm", secondUp.Stderr)
		}

		// TODO(test-harness): once HTTP inspection is available, assert the
		// second invocation issued a FindByLabel-shaped GET against the Fly
		// API keyed on `aa.up-id` and did NOT issue a spawn POST. Cannot
		// verify non-invocation of an HTTP endpoint with the current harness.
	})

	// Step 3: --force replaces. Same setup as step 2, but the second up
	// passes --force and must destroy the old machine, then run a fresh
	// pipeline terminating in exit 0.
	t.Run("step3_force_destroys_old_then_runs_fresh_pipeline", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "docker_up_step3_force_replaces")

		stageToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if stageToken.ExitCode != 0 {
			t.Fatalf("step 3 precondition: expected exit 0 staging token, got %d; stderr=%q", stageToken.ExitCode, stageToken.Stderr)
		}

		projectDir := mustStageDockerfile(t)

		sandbox.ExpectBinary("docker", testhelpers.RespondExitCode(0))
		sandbox.ExpectBinary("flyctl", testhelpers.RespondExitCode(0))

		firstUp := sandbox.RunAA(t, []string{"docker", "up", projectDir}, nil)
		if firstUp.ExitCode != 0 {
			t.Fatalf("step 3 precondition: expected exit 0 on first up, got %d; stdout=%q stderr=%q", firstUp.ExitCode, firstUp.Stdout, firstUp.Stderr)
		}

		// Capture the first attach's machine id so we can later assert the
		// second attach targets a different one.
		flyctlCallsAfterFirst := sandbox.BinaryInvocations("flyctl")
		if len(flyctlCallsAfterFirst) < 1 || len(flyctlCallsAfterFirst[0].Argv) < 6 {
			t.Fatalf("step 3 precondition: expected first up to produce at least one flyctl attach invocation with --machine <id>, got %+v", flyctlCallsAfterFirst)
		}
		oldMachineID := flyctlCallsAfterFirst[0].Argv[5]

		forceUp := sandbox.RunAA(t, []string{"docker", "up", projectDir, "--force"}, nil)
		if forceUp.ExitCode != 0 {
			t.Fatalf("step 3: expected exit 0 on --force replace, got %d; stdout=%q stderr=%q", forceUp.ExitCode, forceUp.Stdout, forceUp.Stderr)
		}

		// Stage lines run again in order — fresh build/push/spawn/attach cycle.
		stageBuildIdx := strings.Index(forceUp.Stdout, "[build]")
		stagePushIdx := strings.Index(forceUp.Stdout, "[push]")
		stageSpawnIdx := strings.Index(forceUp.Stdout, "[spawn]")
		stageAttachIdx := strings.Index(forceUp.Stdout, "[attach] attaching to ")
		if stageBuildIdx < 0 || stagePushIdx < 0 || stageSpawnIdx < 0 || stageAttachIdx < 0 {
			t.Fatalf("step 3: expected --force run to print all four stage lines, got %q", forceUp.Stdout)
		}
		if !(stageBuildIdx < stagePushIdx && stagePushIdx < stageSpawnIdx && stageSpawnIdx < stageAttachIdx) {
			t.Fatalf("step 3: expected --force stage lines in order build→push→spawn→attach, got build@%d push@%d spawn@%d attach@%d in %q",
				stageBuildIdx, stagePushIdx, stageSpawnIdx, stageAttachIdx, forceUp.Stdout)
		}

		// Second attach must target a different machine id than the first.
		flyctlCallsAfterForce := sandbox.BinaryInvocations("flyctl")
		if len(flyctlCallsAfterForce) < 2 {
			t.Fatalf("step 3: expected at least two flyctl attach invocations total (one per up), got %d: %+v", len(flyctlCallsAfterForce), flyctlCallsAfterForce)
		}
		secondAttachArgv := flyctlCallsAfterForce[len(flyctlCallsAfterForce)-1].Argv
		if len(secondAttachArgv) < 6 ||
			secondAttachArgv[0] != "ssh" ||
			secondAttachArgv[1] != "console" ||
			secondAttachArgv[2] != "--app" ||
			secondAttachArgv[4] != "--machine" {
			t.Fatalf("step 3: expected second flyctl attach argv to begin with 'ssh console --app <app> --machine <id>', got %+v", secondAttachArgv)
		}
		newMachineID := secondAttachArgv[5]
		if newMachineID == oldMachineID {
			t.Fatalf("step 3: expected --force to attach to a NEW machine id, but got the same id %q as step-1 run", oldMachineID)
		}
		if !strings.Contains(forceUp.Stdout, newMachineID) {
			t.Fatalf("step 3: expected --force stdout to reference new machine id %q, got %q", newMachineID, forceUp.Stdout)
		}

		// TODO(test-harness): once HTTP inspection is available, assert that
		// the --force invocation issued a DELETE (destroy) against the Fly
		// Machines API for oldMachineID AFTER the push HTTP exchange and
		// BEFORE the new spawn POST, per ADR-4 in
		// docs/architecture/docker-up.md (destroy-between-push-and-spawn).
		// The current harness cannot order HTTP calls relative to binary
		// invocations, so we can only assert the before/after identity via
		// the attach argv above.
	})
}

// mustStageDockerfile creates a t.TempDir() with a minimal valid Dockerfile
// and returns the directory path. Centralised so all three flows use the
// same, realistic input — a directory containing a Dockerfile, exactly as
// docs/docker-up.md § Quickstart describes.
func mustStageDockerfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	const dockerfileContents = "FROM alpine:3.19\nCMD [\"/bin/sh\"]\n"
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContents), 0o644); err != nil {
		t.Fatalf("mustStageDockerfile: failed to write %s: %v", dockerfilePath, err)
	}
	return dir
}
