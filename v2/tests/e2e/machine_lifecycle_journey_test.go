// Package e2e holds end-to-end user-journey tests for aa v2.
//
// PERSONA
//   Priya, solo backend developer. She has a fresh clone of aa, a Fly.io
//   API token in her password manager, and no patience for multi-step
//   cloud-console clicking. Her mental model is "one command per verb
//   under `aa machine`"; she reads the machine-lifecycle doc first and
//   drives everything from the terminal.
//
// JOURNEY
//   1. Priya stores her Fly token once via `aa config token.flyio=...`.
//      WHY: the doc says every machine verb resolves the token from the
//      config store; without it every subsequent command fails loud.
//      OBSERVES: the config command returns exit 0 silently enough that
//      she moves on.
//
//   2. She runs `aa machine spawn` with no flags.
//      WHY: she wants a fresh scratch box and the doc promises spawn is
//      synchronous — when it returns she is sitting at a shell.
//      OBSERVES: a backend-ready line names a machine ID; a shell-ready
//      line follows; fake `flyctl ssh console --app <app> --machine <id>`
//      is invoked exactly once; exit code 0.
//
//   3. From a second terminal she runs `aa machine ls`.
//      WHY: she wants to confirm the tool sees the instance it just made
//      and be able to target it in follow-up commands.
//      OBSERVES: the `ID STATE REGION` header and a row containing the
//      spawned ID in state `started`.
//
//   4. She runs `aa machine stop <id>`.
//      WHY: she is stepping away and does not want to pay for idle compute;
//      stop is the reversible option the doc recommends over rm.
//      OBSERVES: exact line `stop <id> ok`; exit 0.
//
//   5. She returns and runs `aa machine start <id>`.
//      WHY: she wants the same instance back, not a fresh one.
//      OBSERVES: exact line `start <id> ok`; exit 0.
//
//   6. She runs `aa machine attach <id>` to get back into the shell.
//      WHY: the doc promises attach is the idempotent way back into an
//      already-provisioned instance without re-running spawn.
//      OBSERVES: fake flyctl invoked again with the same ssh-console argv
//      shape; exit 0.
//
//   7. She runs `aa machine rm <id>` and then `aa machine ls`.
//      WHY: she is done with the box; destroyed means gone, and ls is how
//      she confirms the teardown actually landed.
//      OBSERVES: `rm <id> ok` on the first call; the follow-up ls does
//      not contain the removed ID.
//
//   8. Negative case: a fresh sandbox with no token runs `aa machine ls`.
//      WHY: the tool has to fail loud with a "what-next" diagnostic, not
//      a transport error, when the token is missing.
//      OBSERVES: non-zero exit; stderr mentions `token.flyio`.
//
// BUSINESS IMPACT IF BROKEN
//   This is the entire machine-lifecycle surface. If spawn→ls→stop→start
//   →attach→rm does not work end-to-end, aa has no product: every solo
//   developer and every LLM agent driving the CLI loses the one reason
//   to use aa over clicking through the Fly web console. Silent success
//   on a missing token turns a configuration mistake into hours of
//   debugging transport errors instead of a one-line fix. Retaining a
//   flat-alias bug (e.g. `aa spawn` still routing) fragments the mental
//   model the doc sells and regresses the migration ADR-8 /
//   retirement-of-flat-aliases contract.

package e2e

import (
	"strings"
	"testing"

	"aa/v2/testhelpers"
)

func TestMachineLifecycleJourney(t *testing.T) {
	sandbox := testhelpers.NewSandbox(t, "machine_lifecycle_journey")

	// Step 1: pre-stage the Fly.io token in the config store.
	configResult := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_test"}, nil)
	if configResult.ExitCode != 0 {
		t.Fatalf("aa config token.flyio=...: exit=%d stderr=%q", configResult.ExitCode, configResult.Stderr)
	}

	// Step 2: spawn. Fake flyctl ssh console must be invoked once; exit 0.
	// We do not know the machine ID up-front, so WantArgs can only constrain
	// the leading shape of argv via a separate assertion below on
	// BinaryInvocations. Here we only script the exit code and stdout.
	sandbox.ExpectBinary("flyctl",
		testhelpers.RespondExitCode(0),
		testhelpers.RespondStdout(""),
	)
	spawnResult := sandbox.RunAA(t, []string{"machine", "spawn"}, nil)
	if spawnResult.ExitCode != 0 {
		t.Fatalf("aa machine spawn: exit=%d stderr=%q", spawnResult.ExitCode, spawnResult.Stderr)
	}
	if !strings.Contains(spawnResult.Stdout, "is running") {
		t.Errorf("spawn stdout missing backend-ready line: %q", spawnResult.Stdout)
	}
	if !strings.Contains(spawnResult.Stdout, "Machine ") {
		t.Errorf("spawn stdout missing shell-ready line naming machine ID: %q", spawnResult.Stdout)
	}

	// The flyctl invocation's argv identifies the machine ID spawn produced.
	invocations := sandbox.BinaryInvocations("flyctl")
	if len(invocations) != 1 {
		t.Fatalf("flyctl invocations during spawn: want 1, got %d", len(invocations))
	}
	machineID := extractMachineID(t, invocations[0].Argv)
	if !strings.Contains(spawnResult.Stdout, machineID) {
		t.Errorf("spawn stdout missing machine ID %q: %q", machineID, spawnResult.Stdout)
	}

	// Step 3: ls shows header + the spawned ID in state started.
	lsResult := sandbox.RunAA(t, []string{"machine", "ls"}, nil)
	if lsResult.ExitCode != 0 {
		t.Fatalf("aa machine ls: exit=%d stderr=%q", lsResult.ExitCode, lsResult.Stderr)
	}
	if !strings.Contains(lsResult.Stdout, "ID") || !strings.Contains(lsResult.Stdout, "STATE") || !strings.Contains(lsResult.Stdout, "REGION") {
		t.Errorf("ls stdout missing `ID STATE REGION` header: %q", lsResult.Stdout)
	}
	if !strings.Contains(lsResult.Stdout, machineID) {
		t.Errorf("ls stdout missing spawned ID %q: %q", machineID, lsResult.Stdout)
	}
	if !strings.Contains(lsResult.Stdout, "started") {
		t.Errorf("ls stdout missing state `started`: %q", lsResult.Stdout)
	}

	// Step 4: stop.
	stopResult := sandbox.RunAA(t, []string{"machine", "stop", machineID}, nil)
	if stopResult.ExitCode != 0 {
		t.Fatalf("aa machine stop: exit=%d stderr=%q", stopResult.ExitCode, stopResult.Stderr)
	}
	wantStop := "stop " + machineID + " ok"
	if !strings.Contains(stopResult.Stdout, wantStop) {
		t.Errorf("stop stdout missing %q: got %q", wantStop, stopResult.Stdout)
	}

	// Step 5: start.
	startResult := sandbox.RunAA(t, []string{"machine", "start", machineID}, nil)
	if startResult.ExitCode != 0 {
		t.Fatalf("aa machine start: exit=%d stderr=%q", startResult.ExitCode, startResult.Stderr)
	}
	wantStart := "start " + machineID + " ok"
	if !strings.Contains(startResult.Stdout, wantStart) {
		t.Errorf("start stdout missing %q: got %q", wantStart, startResult.Stdout)
	}

	// Step 6: attach. Fake flyctl invoked again with ssh console --app <app> --machine <id>.
	sandbox.ExpectBinary("flyctl",
		testhelpers.RespondExitCode(0),
		testhelpers.RespondStdout(""),
	)
	attachResult := sandbox.RunAA(t, []string{"machine", "attach", machineID}, nil)
	if attachResult.ExitCode != 0 {
		t.Fatalf("aa machine attach: exit=%d stderr=%q", attachResult.ExitCode, attachResult.Stderr)
	}
	allInvocations := sandbox.BinaryInvocations("flyctl")
	if len(allInvocations) != 2 {
		t.Fatalf("flyctl invocations after attach: want 2 (spawn + attach), got %d", len(allInvocations))
	}
	attachArgv := allInvocations[1].Argv
	assertSSHConsoleArgv(t, attachArgv, machineID)

	// Step 7: rm + follow-up ls does not show the ID.
	rmResult := sandbox.RunAA(t, []string{"machine", "rm", machineID}, nil)
	if rmResult.ExitCode != 0 {
		t.Fatalf("aa machine rm: exit=%d stderr=%q", rmResult.ExitCode, rmResult.Stderr)
	}
	wantRm := "rm " + machineID + " ok"
	if !strings.Contains(rmResult.Stdout, wantRm) {
		t.Errorf("rm stdout missing %q: got %q", wantRm, rmResult.Stdout)
	}
	lsAfterResult := sandbox.RunAA(t, []string{"machine", "ls"}, nil)
	if lsAfterResult.ExitCode != 0 {
		t.Fatalf("aa machine ls (after rm): exit=%d stderr=%q", lsAfterResult.ExitCode, lsAfterResult.Stderr)
	}
	if strings.Contains(lsAfterResult.Stdout, machineID) {
		t.Errorf("ls after rm still lists destroyed ID %q: %q", machineID, lsAfterResult.Stdout)
	}
}

func TestMachineLsWithoutTokenFailsLoud(t *testing.T) {
	// Step 8 (negative): fresh sandbox, no token configured. ls must fail
	// loud and name the config key the user has to set.
	sandbox := testhelpers.NewSandbox(t, "machine_ls_no_token")

	result := sandbox.RunAA(t, []string{"machine", "ls"}, nil)
	if result.ExitCode == 0 {
		t.Fatalf("aa machine ls with no token: want non-zero exit, got 0; stdout=%q", result.Stdout)
	}
	if !strings.Contains(result.Stderr, "token.flyio") {
		t.Errorf("stderr must name config key `token.flyio`, got %q", result.Stderr)
	}
}

// extractMachineID returns the value following `--machine` in the given argv.
// The argv shape promised by the architecture doc is:
//
//	ssh console --app <app-name> --machine <id>
func extractMachineID(t *testing.T, argv []string) string {
	t.Helper()
	for i, tok := range argv {
		if tok == "--machine" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	t.Fatalf("argv missing --machine <id>: %v", argv)
	return ""
}

// assertSSHConsoleArgv checks that argv matches `ssh console --app <app> --machine <id>`
// with the expected machine ID. The app name is dynamic (resolved from the
// config store's default) so we only assert its presence, not its value.
func assertSSHConsoleArgv(t *testing.T, argv []string, wantMachineID string) {
	t.Helper()
	if len(argv) < 6 {
		t.Fatalf("flyctl argv too short: %v", argv)
	}
	if argv[0] != "ssh" || argv[1] != "console" {
		t.Errorf("flyctl argv must start with `ssh console`, got %v", argv)
	}
	var sawApp, sawMachine bool
	for i := 2; i < len(argv)-1; i++ {
		switch argv[i] {
		case "--app":
			sawApp = argv[i+1] != ""
		case "--machine":
			if argv[i+1] != wantMachineID {
				t.Errorf("flyctl --machine: want %q, got %q", wantMachineID, argv[i+1])
			}
			sawMachine = true
		}
	}
	if !sawApp {
		t.Errorf("flyctl argv missing --app <app-name>: %v", argv)
	}
	if !sawMachine {
		t.Errorf("flyctl argv missing --machine <id>: %v", argv)
	}
}
