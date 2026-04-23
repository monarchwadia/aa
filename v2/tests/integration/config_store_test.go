// Package integration — config_store_test.go covers the seams between the
// configstore package and the real filesystem: file-roundtrip (set → read →
// list → remove → list), 0600 file mode and 0700 parent-dir mode, merge
// semantics (set doesn't clobber untouched keys), multi-key atomic set
// (validation-before-mutation), and the "unset token → resolver returns
// (_, false)" contract consumers rely on to produce the
// "run: aa config token.flyio=<token>" error.
//
// These tests use the real `aa` binary (compiled via `go build` at test
// start) and a real temp HOME/XDG_CONFIG_HOME. No fakes. No third-party
// deps. Expected RED until Wave 1 (configstore bodies + config_cmd handler)
// lands.
package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"aa/v2/configstore"
)

// buildAA compiles the aa CLI into the test tempdir and returns its path.
// Called once per test that needs the binary; go build is cached so the
// repeat cost is negligible.
func buildAA(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "aa")
	cmd := exec.Command("go", "build", "-o", bin, "aa/v2")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build aa: %v", err)
	}
	return bin
}

// isolatedHome returns a fresh tempdir and configures HOME/XDG_CONFIG_HOME
// so neither the test binary nor a child aa invocation touches the real
// user config.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Clear env layers Resolve*() checks so they don't mask file-layer tests.
	t.Setenv("FLY_API_TOKEN", "")
	t.Setenv("FLY_API_BASE", "")
	t.Setenv("AA_REGISTRY_BASE", "")
	return home
}

// runAA runs the compiled aa binary with argv in the isolated HOME.
func runAA(t *testing.T, bin, home string, argv ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, argv...)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"FLY_API_TOKEN=",
		"FLY_API_BASE=",
		"AA_REGISTRY_BASE=",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("aa run: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exit
}

// configFilePath is where the on-disk file should live inside the isolated
// HOME. Linux: $XDG_CONFIG_HOME/aa/config.
func configFilePath(home string) string {
	return filepath.Join(home, ".config", "aa", "config")
}

// TestIntegration_ConfigSet_CreatesFileAt0600 verifies the documented
// storage security contract: after the first `aa config k=v`, the config
// file exists with mode 0600.
func TestIntegration_ConfigSet_CreatesFileAt0600(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	_, stderr, exit := runAA(t, bin, home, "config", "token.flyio=fo1_integ_abc")
	if exit != 0 {
		t.Fatalf("aa config set exit=%d stderr=%q", exit, stderr)
	}
	info, err := os.Stat(configFilePath(home))
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config file mode: want 0600, got %o", mode)
	}
}

// TestIntegration_ConfigSet_ParentDirAt0700 verifies the documented dir
// permission contract.
func TestIntegration_ConfigSet_ParentDirAt0700(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	if _, _, exit := runAA(t, bin, home, "config", "token.flyio=fo1_integ_abc"); exit != 0 {
		t.Fatalf("aa config set exit=%d", exit)
	}
	info, err := os.Stat(filepath.Dir(configFilePath(home)))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("config dir mode: want 0700, got %o", mode)
	}
}

// TestIntegration_Roundtrip_SetReadListRemoveList exercises the full
// file-roundtrip documented as the first "medium" integration test in the
// architecture testing surface.
func TestIntegration_Roundtrip_SetReadListRemoveList(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	// set
	if _, stderr, exit := runAA(t, bin, home, "config",
		"token.flyio=fo1_abc",
		"defaults.app=my-team-app",
	); exit != 0 {
		t.Fatalf("set exit=%d stderr=%q", exit, stderr)
	}

	// list — token masked, defaults literal, sorted
	listStdout, _, exit := runAA(t, bin, home, "config")
	if exit != 0 {
		t.Fatalf("list exit=%d", exit)
	}
	if !strings.Contains(listStdout, "token.flyio=<set>") {
		t.Fatalf("list: token not masked as <set>:\n%s", listStdout)
	}
	if strings.Contains(listStdout, "fo1_abc") {
		t.Fatalf("list leaked real token:\n%s", listStdout)
	}
	if !strings.Contains(listStdout, "defaults.app=my-team-app") {
		t.Fatalf("list: non-secret not literal:\n%s", listStdout)
	}

	// remove
	rmStdout, _, exit := runAA(t, bin, home, "config", "--remove", "defaults.app")
	if exit != 0 {
		t.Fatalf("remove exit=%d", exit)
	}
	if !strings.Contains(rmStdout, "removed defaults.app") {
		t.Fatalf("remove stdout: want `removed defaults.app`, got %q", rmStdout)
	}

	// list again — defaults.app gone, token remains
	afterStdout, _, _ := runAA(t, bin, home, "config")
	if strings.Contains(afterStdout, "defaults.app") {
		t.Fatalf("after remove, list still contains defaults.app:\n%s", afterStdout)
	}
	if !strings.Contains(afterStdout, "token.flyio=<set>") {
		t.Fatalf("token.flyio lost after unrelated remove:\n%s", afterStdout)
	}
}

// TestIntegration_EmptyStore_PrintsNoConfigSet covers the documented
// empty-state contract.
func TestIntegration_EmptyStore_PrintsNoConfigSet(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	stdout, _, exit := runAA(t, bin, home, "config")
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	if stdout != "(no config set)\n" {
		t.Fatalf("want `(no config set)\\n`, got %q", stdout)
	}
}

// TestIntegration_MergeSemantics_SecondSetPreservesFirstKey covers the
// "Load current → mutate in memory → persist" data-flow: a second `aa
// config k2=v2` must not clobber an unrelated k1 set earlier.
func TestIntegration_MergeSemantics_SecondSetPreservesFirstKey(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	if _, _, exit := runAA(t, bin, home, "config", "token.flyio=fo1_first"); exit != 0 {
		t.Fatalf("first set exit=%d", exit)
	}
	if _, _, exit := runAA(t, bin, home, "config", "defaults.app=my-team-app"); exit != 0 {
		t.Fatalf("second set exit=%d", exit)
	}

	stdout, _, _ := runAA(t, bin, home, "config")
	if !strings.Contains(stdout, "token.flyio=<set>") {
		t.Fatalf("first key lost after unrelated second set:\n%s", stdout)
	}
	if !strings.Contains(stdout, "defaults.app=my-team-app") {
		t.Fatalf("second key missing:\n%s", stdout)
	}
}

// TestIntegration_ValidationBeforeMutation pins the architecture rule:
// `aa config a=1 bareword c=3` persists neither a nor c.
func TestIntegration_ValidationBeforeMutation(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	_, stderr, exit := runAA(t, bin, home, "config", "a.first=1", "bareword", "c.third=3")
	if exit == 0 {
		t.Fatal("want non-zero exit on malformed arg")
	}
	if !strings.Contains(stderr, "bareword") {
		t.Fatalf("stderr must name the bad arg, got %q", stderr)
	}
	// Config file either doesn't exist (nothing persisted) or exists but
	// lacks both a.first and c.third.
	listStdout, _, _ := runAA(t, bin, home, "config")
	if strings.Contains(listStdout, "a.first") || strings.Contains(listStdout, "c.third") {
		t.Fatalf("validation-before-mutation violated; list shows:\n%s", listStdout)
	}
}

// TestIntegration_ListOutputIsSorted pins the architecture choice to sort
// for stable `aa config | diff`.
func TestIntegration_ListOutputIsSorted(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	// write multiple non-secret keys so the literal rendering makes
	// sort-order directly observable.
	if _, _, exit := runAA(t, bin, home, "config",
		"zzz.last=z",
		"aaa.first=1",
		"mmm.middle=m",
	); exit != 0 {
		t.Fatalf("set exit=%d", exit)
	}
	stdout, _, _ := runAA(t, bin, home, "config")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	sortedCopy := append([]string(nil), lines...)
	sort.Strings(sortedCopy)
	for i := range lines {
		if lines[i] != sortedCopy[i] {
			t.Fatalf("list output not lex-sorted:\n%s", stdout)
		}
	}
}

// TestIntegration_ResolveFlyToken_UnsetReturnsFalse is the contract
// consumers rely on: when no flag, env, or config value is set, the
// resolver returns (_, false). Callers use that signal to produce the
// "no Fly.io token found — run: aa config token.flyio=<token>" error.
func TestIntegration_ResolveFlyToken_UnsetReturnsFalse(t *testing.T) {
	isolatedHome(t)

	r, err := configstore.NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if ok {
		t.Fatalf("ResolveFlyToken on empty store: want ok=false, got ok=true (val=%q)", got)
	}
	if got != "" {
		t.Fatalf("ResolveFlyToken on empty store: want empty value, got %q", got)
	}
}

// TestIntegration_ResolveFlyToken_AfterSetReturnsTrue verifies the file
// layer: after `aa config token.flyio=...`, the shared resolver reads it.
func TestIntegration_ResolveFlyToken_AfterSetReturnsTrue(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	if _, _, exit := runAA(t, bin, home, "config", "token.flyio=fo1_integ_xyz"); exit != 0 {
		t.Fatalf("set exit=%d", exit)
	}
	r, err := configstore.NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok {
		t.Fatal("ResolveFlyToken after set: want ok=true")
	}
	if got != "fo1_integ_xyz" {
		t.Fatalf("ResolveFlyToken: want fo1_integ_xyz, got %q", got)
	}
}

// TestIntegration_TokenNotLeakedInListOutput is an explicit regression
// check on the masking contract, at the integration layer.
func TestIntegration_TokenNotLeakedInListOutput(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	secret := "fo1_DoNotLeakThisValue123"
	if _, _, exit := runAA(t, bin, home, "config", "token.flyio="+secret); exit != 0 {
		t.Fatalf("set exit=%d", exit)
	}
	stdout, _, _ := runAA(t, bin, home, "config")
	if strings.Contains(stdout, secret) {
		t.Fatalf("list output leaked real token value:\n%s", stdout)
	}
}

// TestIntegration_ShowSecretsFlagReveals pins ADR 2's --show-secrets
// escape hatch at the integration layer.
func TestIntegration_ShowSecretsFlagReveals(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	secret := "fo1_RevealMe456"
	if _, _, exit := runAA(t, bin, home, "config", "token.flyio="+secret); exit != 0 {
		t.Fatalf("set exit=%d", exit)
	}
	stdout, _, exit := runAA(t, bin, home, "config", "--show-secrets")
	if exit != 0 {
		t.Fatalf("--show-secrets exit=%d", exit)
	}
	if !strings.Contains(stdout, secret) {
		t.Fatalf("--show-secrets must reveal real token, got:\n%s", stdout)
	}
}

// TestIntegration_RemoveThenListOmitsKey covers the docs line:
// "After removal, the key no longer appears in aa config output".
func TestIntegration_RemoveThenListOmitsKey(t *testing.T) {
	bin := buildAA(t)
	home := isolatedHome(t)

	if _, _, exit := runAA(t, bin, home, "config", "token.flyio=fo1_x"); exit != 0 {
		t.Fatalf("set exit=%d", exit)
	}
	if _, _, exit := runAA(t, bin, home, "config", "--remove", "token.flyio"); exit != 0 {
		t.Fatalf("remove exit=%d", exit)
	}
	stdout, _, _ := runAA(t, bin, home, "config")
	if strings.Contains(stdout, "token.flyio") {
		t.Fatalf("list still contains token.flyio after remove:\n%s", stdout)
	}
	// With nothing left, list should print (no config set).
	if strings.TrimSpace(stdout) != "(no config set)" {
		t.Fatalf("after sole key removed, want `(no config set)`, got %q", stdout)
	}
}
