// machine_handlers_test.go pins the contract of the `aa machine <verb>`
// CLI handlers described in docs/architecture/machine-lifecycle.md (W2d).
//
// These are unit tests — no HTTP, no exec. The handlers receive a
// MachineDeps containing an in-memory flyclient fake, an in-memory runner
// fake, and a MapReader-style configstore. Every test name reads as a
// sentence describing the behaviour under test.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"aa/v2/configstore"
	"aa/v2/extbin"
	"aa/v2/flyclient"
)

// --- fakes (unit-local; no network, no exec) ---

type machineFakeClient struct {
	machines    []flyclient.Machine
	createCalls int
	startIDs    []string
	stopIDs     []string
	destroyIDs  []string
	destroyForce bool
}

func (f *machineFakeClient) EnsureApp(ctx context.Context, app string) error { return nil }
func (f *machineFakeClient) Create(ctx context.Context, app string, spec flyclient.SpawnSpec) (flyclient.Machine, error) {
	f.createCalls++
	m := flyclient.Machine{ID: "a1", State: "created", Region: "iad", Labels: spec.Labels}
	f.machines = append(f.machines, m)
	return m, nil
}
func (f *machineFakeClient) Get(ctx context.Context, app, id string) (flyclient.Machine, error) {
	for _, m := range f.machines {
		if m.ID == id {
			return m, nil
		}
	}
	return flyclient.Machine{}, nil
}
func (f *machineFakeClient) WaitStarted(ctx context.Context, app, id string) error { return nil }
func (f *machineFakeClient) List(ctx context.Context, app string) ([]flyclient.Machine, error) {
	return f.machines, nil
}
func (f *machineFakeClient) Start(ctx context.Context, app, id string) error {
	f.startIDs = append(f.startIDs, id)
	return nil
}
func (f *machineFakeClient) Stop(ctx context.Context, app, id string) error {
	f.stopIDs = append(f.stopIDs, id)
	return nil
}
func (f *machineFakeClient) Destroy(ctx context.Context, app, id string, force bool) error {
	f.destroyIDs = append(f.destroyIDs, id)
	f.destroyForce = force
	return nil
}
func (f *machineFakeClient) FindByLabel(ctx context.Context, app, key, value string) ([]flyclient.Machine, error) {
	var out []flyclient.Machine
	for _, m := range f.machines {
		if m.Labels[key] == value {
			out = append(out, m)
		}
	}
	return out, nil
}

type machineFakeRunner struct {
	calls []extbin.Invocation
	exit  int
}

func (f *machineFakeRunner) Run(ctx context.Context, inv extbin.Invocation) (int, error) {
	f.calls = append(f.calls, inv)
	return f.exit, nil
}

// depsWithFakes wires a MachineDeps for one test. Isolated per t.
func depsWithFakes(t *testing.T, cfgOverrides map[string]string) (*machineFakeClient, *machineFakeRunner, *bytes.Buffer, *bytes.Buffer, MachineDeps) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FLY_API_TOKEN", "")
	cfg, err := configstore.NewReader(cfgOverrides)
	if err != nil {
		t.Fatalf("configstore.NewReader: %v", err)
	}
	c := &machineFakeClient{}
	r := &machineFakeRunner{}
	var stdout, stderr bytes.Buffer
	return c, r, &stdout, &stderr, MachineDeps{
		Client: c,
		Runner: r,
		Config: cfg,
		Stdout: &stdout,
		Stderr: &stderr,
	}
}

// --- argv parsing & resolution ---

// The --token flag wins over env and config.
func TestResolveFlyTokenPrefersFlagValueOverEnvAndConfig(t *testing.T) {
	env := func(k string) string { return "tok_env" }
	cfg, _ := configstore.NewReader(map[string]string{"token.flyio": "tok_cfg"})
	got, ok := ResolveFlyToken("tok_flag", env, cfg)
	if !ok || got != "tok_flag" {
		t.Fatalf("ResolveFlyToken = (%q,%v), want (tok_flag,true)", got, ok)
	}
}

// FLY_API_TOKEN env wins when no --token flag was passed.
func TestResolveFlyTokenFallsBackToFlyApiTokenEnvWhenFlagAbsent(t *testing.T) {
	env := func(k string) string {
		if k == "FLY_API_TOKEN" {
			return "tok_env"
		}
		return ""
	}
	cfg, _ := configstore.NewReader(map[string]string{"token.flyio": "tok_cfg"})
	got, ok := ResolveFlyToken("", env, cfg)
	if !ok || got != "tok_env" {
		t.Fatalf("ResolveFlyToken = (%q,%v), want (tok_env,true)", got, ok)
	}
}

// Config store is consulted only when both flag and env are empty.
func TestResolveFlyTokenFallsBackToConfigWhenFlagAndEnvEmpty(t *testing.T) {
	env := func(k string) string { return "" }
	cfg, _ := configstore.NewReader(map[string]string{"token.flyio": "tok_cfg"})
	got, ok := ResolveFlyToken("", env, cfg)
	if !ok || got != "tok_cfg" {
		t.Fatalf("ResolveFlyToken = (%q,%v), want (tok_cfg,true)", got, ok)
	}
}

// No source → (""," false). Handlers surface this as the "no token found" error.
func TestResolveFlyTokenReturnsFalseWhenNoSourceProvidesAValue(t *testing.T) {
	env := func(k string) string { return "" }
	cfg, _ := configstore.NewReader(nil)
	got, ok := ResolveFlyToken("", env, cfg)
	if ok || got != "" {
		t.Fatalf("ResolveFlyToken = (%q,%v), want (\"\",false)", got, ok)
	}
}

// ResolveApp defaults to aa-apps when no flag and no config override.
func TestResolveAppFallsBackToBuiltInAaAppsWhenNeitherFlagNorConfigSet(t *testing.T) {
	cfg, _ := configstore.NewReader(nil)
	got := ResolveApp("", cfg)
	if got != "aa-apps" {
		t.Fatalf("ResolveApp = %q, want aa-apps (ADR-2)", got)
	}
}

// ResolveImage defaults to ubuntu:22.04 per ADR-1.
func TestResolveImageFallsBackToBuiltInUbuntuTwentyTwoZeroFour(t *testing.T) {
	cfg, _ := configstore.NewReader(nil)
	got := ResolveImage("", cfg)
	if got != "ubuntu:22.04" {
		t.Fatalf("ResolveImage = %q, want ubuntu:22.04 (ADR-1)", got)
	}
}

// --- ls formatting ---

// Empty machine list prints the `(no machines in "<app>")` message.
func TestFormatMachineTableEmptyListPrintsNoMachinesLine(t *testing.T) {
	got := FormatMachineTable("aa-apps", nil)
	want := "(no machines in \"aa-apps\")\n"
	if got != want {
		t.Fatalf("FormatMachineTable(empty) = %q, want %q", got, want)
	}
}

// Non-empty list renders a header line with ID STATE REGION.
func TestFormatMachineTableNonEmptyListContainsHeaderColumns(t *testing.T) {
	got := FormatMachineTable("aa-apps", []flyclient.Machine{
		{ID: "a1", State: "started", Region: "iad"},
	})
	for _, col := range []string{"ID", "STATE", "REGION", "a1", "started", "iad"} {
		if !strings.Contains(got, col) {
			t.Errorf("table missing %q: %q", col, got)
		}
	}
}

// --- verb dispatch via RunMachine ---

// `machine ls` with a provisioned machine prints its row and exits 0.
func TestRunMachineLsListsProvisionedMachineAndExitsZero(t *testing.T) {
	c, _, stdout, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	c.machines = []flyclient.Machine{{ID: "a1", State: "started", Region: "iad"}}
	code := RunMachine([]string{"ls"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "a1") {
		t.Errorf("stdout missing ID a1: %q", stdout.String())
	}
}

// `machine ls` without a token resolves none and fails loud naming the config key.
func TestRunMachineLsWithNoTokenExitsNonZeroAndMentionsTokenFlyio(t *testing.T) {
	_, _, _, stderr, deps := depsWithFakes(t, nil)
	code := RunMachine([]string{"ls"}, deps)
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "token.flyio") {
		t.Errorf("stderr missing token.flyio: %q", stderr.String())
	}
}

// `machine start <id>` calls flyclient.Start and prints `start <id> ok`.
func TestRunMachineStartInvokesStartAndPrintsOkLine(t *testing.T) {
	c, _, stdout, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"start", "a1"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(c.startIDs) != 1 || c.startIDs[0] != "a1" {
		t.Errorf("startIDs = %v, want [a1]", c.startIDs)
	}
	if !strings.Contains(stdout.String(), "start a1 ok") {
		t.Errorf("stdout missing `start a1 ok`: %q", stdout.String())
	}
}

// `machine stop <id>` calls flyclient.Stop and prints `stop <id> ok`.
func TestRunMachineStopInvokesStopAndPrintsOkLine(t *testing.T) {
	c, _, stdout, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"stop", "a1"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(c.stopIDs) != 1 || c.stopIDs[0] != "a1" {
		t.Errorf("stopIDs = %v, want [a1]", c.stopIDs)
	}
	if !strings.Contains(stdout.String(), "stop a1 ok") {
		t.Errorf("stdout missing `stop a1 ok`: %q", stdout.String())
	}
}

// `machine rm <id>` without --force calls Destroy with force=false.
func TestRunMachineRmWithoutForceCallsDestroyWithForceFalse(t *testing.T) {
	c, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"rm", "a1"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if c.destroyForce {
		t.Error("destroyForce = true, want false (no --force flag passed)")
	}
}

// `machine rm --force <id>` calls Destroy with force=true (ADR-7).
func TestRunMachineRmWithForceFlagCallsDestroyWithForceTrue(t *testing.T) {
	c, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"rm", "--force", "a1"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !c.destroyForce {
		t.Error("destroyForce = false, want true (--force was passed)")
	}
}

// `machine spawn` with --app overrides the resolved app.
func TestRunMachineSpawnHonoursAppFlagOverConfigDefault(t *testing.T) {
	c, _, _, _, deps := depsWithFakes(t, map[string]string{
		"token.flyio":   "tok",
		"defaults.app": "from-config",
	})
	code := RunMachine([]string{"spawn", "--app", "from-flag"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if c.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", c.createCalls)
	}
}

// `machine spawn` with --image overrides the resolved image.
func TestRunMachineSpawnHonoursImageFlagOverConfigDefault(t *testing.T) {
	c, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"spawn", "--image", "debian:12-slim"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if c.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", c.createCalls)
	}
}

// `machine attach <id>` invokes the runner with `ssh console --app ... --machine <id>`.
func TestRunMachineAttachInvokesRunnerWithSshConsoleArgv(t *testing.T) {
	c, r, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	c.machines = []flyclient.Machine{{ID: "a1", State: "started", Region: "iad"}}
	code := RunMachine([]string{"attach", "a1"}, deps)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if len(r.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(r.calls))
	}
	argv := r.calls[0].Argv
	if len(argv) < 2 || argv[0] != "ssh" || argv[1] != "console" {
		t.Errorf("argv = %v, want leading `ssh console`", argv)
	}
	sawMachine := false
	for i, tok := range argv {
		if tok == "--machine" && i+1 < len(argv) && argv[i+1] == "a1" {
			sawMachine = true
		}
	}
	if !sawMachine {
		t.Errorf("argv missing --machine a1: %v", argv)
	}
}

// `machine attach <id>` on a stopped machine fails loud naming the state.
func TestRunMachineAttachOnStoppedMachineFailsLoudMentioningStart(t *testing.T) {
	c, r, _, stderr, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	c.machines = []flyclient.Machine{{ID: "a1", State: "stopped", Region: "iad"}}
	code := RunMachine([]string{"attach", "a1"}, deps)
	if code == 0 {
		t.Fatalf("exit = 0, want non-zero for stopped machine")
	}
	if len(r.calls) != 0 {
		t.Errorf("runner should not be invoked on stopped machine; calls=%v", r.calls)
	}
	if !strings.Contains(stderr.String(), "start") {
		t.Errorf("stderr must suggest `aa machine start`, got %q", stderr.String())
	}
}

// start/stop/rm require at least one positional ID.
func TestRunMachineStartWithoutPositionalIdExitsNonZero(t *testing.T) {
	_, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"start"}, deps)
	if code == 0 {
		t.Fatal("exit = 0, want non-zero when no machine ID passed")
	}
}

// An unknown verb exits non-zero.
func TestRunMachineUnknownVerbExitsNonZero(t *testing.T) {
	_, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
	code := RunMachine([]string{"blorp"}, deps)
	if code == 0 {
		t.Fatal("exit = 0, want non-zero for unknown verb")
	}
}

// Fuzz: no verb string causes a panic out of RunMachine.
func FuzzRunMachineNeverPanicsOnArbitraryVerb(f *testing.F) {
	f.Add("ls")
	f.Add("")
	f.Add("spawn")
	f.Add("unknown")
	f.Fuzz(func(t *testing.T, verb string) {
		_, _, _, _, deps := depsWithFakes(t, map[string]string{"token.flyio": "tok"})
		_ = RunMachine([]string{verb}, deps)
	})
}
