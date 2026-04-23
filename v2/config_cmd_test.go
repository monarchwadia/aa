// config_cmd_test.go covers the CLI-side behavior of `aa config`:
// argument parsing, list/set/remove dispatch, output formatting (including
// `saved <k>`, `removed <k>`, `(no config set)`, masking of token.* keys,
// `--show-secrets` bypass), and validation-before-mutation for multi-key
// set. Also covers the supporting helpers readConfig / writeConfig /
// configPath that `runConfig` composes with.
//
// Many cases drive `runConfig`, which calls `log.Fatalf` on failure. For
// those, we re-exec the test binary with an env-var switch (see TestMain)
// and assert on captured stdout/stderr/exit. Pure helpers are tested
// directly. All tests run in an isolated HOME via t.TempDir()+t.Setenv.
//
// Expected RED state: runConfig today does not sort output, does not mask
// token.* keys, does not accept --remove or --show-secrets, and does not
// perform validation-before-mutation. Tests for those behaviors must fail
// until the config-cli workstream lands.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Re-exec machinery: when this env var is set, TestMain runs runConfig with
// the arg list in AA_CONFIG_CMD_ARGV (space-separated, each arg quoted-safe
// via a null-byte separator) and exits based on outcome.
const reexecEnv = "AA_CONFIG_CMD_REEXEC"
const reexecArgs = "AA_CONFIG_CMD_ARGV"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		argv := splitNul(os.Getenv(reexecArgs))
		runConfig(argv)
		// runConfig returns only on success paths. log.Fatalf would have
		// exited non-zero already. Exit 0 here to signal success.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func splitNul(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x1f")
}

// runConfigSubprocess re-executes this test binary to invoke runConfig with
// the given argv, isolated under tempHome as $HOME. Returns stdout, stderr,
// exitCode.
func runConfigSubprocess(t *testing.T, tempHome string, argv []string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$") // run no tests in subprocess
	cmd.Env = append(os.Environ(),
		reexecEnv+"=1",
		reexecArgs+"="+strings.Join(argv, "\x1f"),
		"HOME="+tempHome,
		"XDG_CONFIG_HOME="+filepath.Join(tempHome, ".config"),
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
			t.Fatalf("subprocess run: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exit
}

// prepareIsolatedHome sets up a fresh $HOME/XDG_CONFIG_HOME and returns
// the tempHome path plus the absolute config file path.
func prepareIsolatedHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	return home, cfgPath
}

// captureStdout runs fn and returns everything it wrote to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = origStdout
	return <-done
}

// ---------- configPath ----------

// TestConfigPath_UsesXDGConfigHome asserts config file lives at
// $XDG_CONFIG_HOME/aa/config on Linux (the path spec in docs).
func TestConfigPath_UsesXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	got, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	want := filepath.Join(home, ".config", "aa", "config")
	if got != want {
		t.Fatalf("configPath: want %q, got %q", want, got)
	}
}

// ---------- readConfig ----------

// TestReadConfig_MissingFile_ReturnsEmptyMap covers failure-mode row
// "Config file does not exist on read — treat as empty map".
func TestReadConfig_MissingFile_ReturnsEmptyMap(t *testing.T) {
	prepareIsolatedHome(t)

	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig missing file: want nil err, got %v", err)
	}
	if len(cfg) != 0 {
		t.Fatalf("readConfig missing file: want empty map, got %v", cfg)
	}
}

// TestReadConfig_IgnoresCommentAndBlankLines matches the failure-mode row
// covering comment/blank line tolerance.
func TestReadConfig_IgnoresCommentAndBlankLines(t *testing.T) {
	_, cfgPath := prepareIsolatedHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("# hi\n\ntoken.flyio=abc\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if cfg["token.flyio"] != "abc" || len(cfg) != 1 {
		t.Fatalf("readConfig: want {token.flyio: abc} only, got %v", cfg)
	}
}

// ---------- writeConfig ----------

// TestWriteConfig_CreatesFileWith0600Mode checks the documented storage
// security contract: the config file is created mode 0600.
func TestWriteConfig_CreatesFileWith0600Mode(t *testing.T) {
	_, cfgPath := prepareIsolatedHome(t)

	if err := writeConfig(map[string]string{"token.flyio": "abc"}); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config file mode: want 0600, got %o", mode)
	}
}

// TestWriteConfig_CreatesParentDirWith0700Mode checks the documented
// parent-dir permission.
func TestWriteConfig_CreatesParentDirWith0700Mode(t *testing.T) {
	_, cfgPath := prepareIsolatedHome(t)

	if err := writeConfig(map[string]string{"token.flyio": "abc"}); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	info, err := os.Stat(filepath.Dir(cfgPath))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("config dir mode: want 0700, got %o", mode)
	}
}

// ---------- runConfig: list path ----------

// TestRunConfig_List_EmptyPrintsNoConfigSet covers docs spec:
// "If nothing is stored, prints `(no config set)` and exits 0".
func TestRunConfig_List_EmptyPrintsNoConfigSet(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	stdout, _, exit := runConfigSubprocess(t, home, nil)
	if exit != 0 {
		t.Fatalf("exit: want 0, got %d", exit)
	}
	if strings.TrimSpace(stdout) != "(no config set)" {
		t.Fatalf("stdout: want `(no config set)`, got %q", stdout)
	}
}

// TestRunConfig_List_MasksTokenKeys pins ADR 2: token.* keys render as
// `token.foo=<set>` in list output; non-secrets render literally.
func TestRunConfig_List_MasksTokenKeys(t *testing.T) {
	home, cfgPath := prepareIsolatedHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "token.flyio=fo1_realsecret\ndefaults.app=my-team-app\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, exit := runConfigSubprocess(t, home, nil)
	if exit != 0 {
		t.Fatalf("exit: %d, stdout=%q", exit, stdout)
	}
	if !strings.Contains(stdout, "token.flyio=<set>") {
		t.Fatalf("list output should mask token.flyio as <set>, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "fo1_realsecret") {
		t.Fatalf("list output leaked the real secret:\n%s", stdout)
	}
	if !strings.Contains(stdout, "defaults.app=my-team-app") {
		t.Fatalf("non-secret should render literally, got:\n%s", stdout)
	}
}

// TestRunConfig_List_SortedOutput pins the architecture choice to sort keys
// lexicographically for stable `aa config | diff`.
func TestRunConfig_List_SortedOutput(t *testing.T) {
	home, cfgPath := prepareIsolatedHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write keys in reverse-sorted order so map iteration can't accidentally
	// produce sorted output by luck.
	body := "zzz.last=z\ndefaults.app=a\naaa.first=1\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, exit := runConfigSubprocess(t, home, nil)
	if exit != 0 {
		t.Fatalf("exit: %d", exit)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	sortedCopy := append([]string(nil), lines...)
	sort.Strings(sortedCopy)
	for i := range lines {
		if lines[i] != sortedCopy[i] {
			t.Fatalf("list output not lex-sorted:\n%s", stdout)
		}
	}
}

// TestRunConfig_List_ShowSecretsFlag covers ADR 2's escape hatch:
// `aa config --show-secrets` reveals real token values.
func TestRunConfig_List_ShowSecretsFlag(t *testing.T) {
	home, cfgPath := prepareIsolatedHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("token.flyio=fo1_realsecret\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, exit := runConfigSubprocess(t, home, []string{"--show-secrets"})
	if exit != 0 {
		t.Fatalf("exit: %d, stdout=%q", exit, stdout)
	}
	if !strings.Contains(stdout, "token.flyio=fo1_realsecret") {
		t.Fatalf("--show-secrets should reveal token: got:\n%s", stdout)
	}
	if strings.Contains(stdout, "<set>") {
		t.Fatalf("--show-secrets should NOT render <set>: got:\n%s", stdout)
	}
}

// ---------- runConfig: set path ----------

// TestRunConfig_Set_PrintsSavedPerKeyInInputOrder pins ADR 4: one
// `saved <key>` per input pair, in input order, value never echoed.
func TestRunConfig_Set_PrintsSavedPerKeyInInputOrder(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	stdout, _, exit := runConfigSubprocess(t, home, []string{
		"token.flyio=fo1_abc",
		"defaults.app=my-aa-apps",
	})
	if exit != 0 {
		t.Fatalf("exit: %d", exit)
	}
	want := "saved token.flyio\nsaved defaults.app\n"
	if stdout != want {
		t.Fatalf("set output:\n want %q\n got  %q", want, stdout)
	}
	if strings.Contains(stdout, "fo1_abc") {
		t.Fatalf("set output leaked value: %q", stdout)
	}
}

// TestRunConfig_Set_PersistsValues covers docs: after set, the file holds
// the value; a subsequent readConfig observes it.
func TestRunConfig_Set_PersistsValues(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	_, _, exit := runConfigSubprocess(t, home, []string{"token.flyio=fo1_abc"})
	if exit != 0 {
		t.Fatalf("exit: %d", exit)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if cfg["token.flyio"] != "fo1_abc" {
		t.Fatalf("after set, readConfig[token.flyio]=%q, want fo1_abc", cfg["token.flyio"])
	}
}

// TestRunConfig_Set_ValidationBeforeMutation pins the architecture
// requirement: `aa config a=1 bareword c=3` must persist neither a nor c.
func TestRunConfig_Set_ValidationBeforeMutation(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	_, stderr, exit := runConfigSubprocess(t, home, []string{
		"a.first=1",
		"bareword",
		"c.third=3",
	})
	if exit == 0 {
		t.Fatal("exit: want non-zero, got 0")
	}
	if !strings.Contains(stderr, "bareword") {
		t.Fatalf("stderr must name the bad arg, got: %q", stderr)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if _, present := cfg["a.first"]; present {
		t.Fatalf("a.first was persisted despite malformed sibling arg: %v", cfg)
	}
	if _, present := cfg["c.third"]; present {
		t.Fatalf("c.third was persisted despite malformed sibling arg: %v", cfg)
	}
}

// TestRunConfig_Set_BarewordArgErrorMessage covers the failure-mode row
// "aa config bareword (no =) — print invalid config argument ... expected
// key=value".
func TestRunConfig_Set_BarewordArgErrorMessage(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	_, stderr, exit := runConfigSubprocess(t, home, []string{"bareword"})
	if exit == 0 {
		t.Fatal("exit: want non-zero, got 0")
	}
	wantSub := `invalid config argument "bareword"`
	if !strings.Contains(stderr, wantSub) {
		t.Fatalf("stderr: want substring %q, got %q", wantSub, stderr)
	}
	if !strings.Contains(stderr, "expected key=value") {
		t.Fatalf("stderr should mention expected format, got %q", stderr)
	}
}

// TestRunConfig_Set_OverwritesExistingKey covers docs: "Existing keys are
// replaced."
func TestRunConfig_Set_OverwritesExistingKey(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	if _, _, exit := runConfigSubprocess(t, home, []string{"token.flyio=old"}); exit != 0 {
		t.Fatalf("first set exit: %d", exit)
	}
	if _, _, exit := runConfigSubprocess(t, home, []string{"token.flyio=new"}); exit != 0 {
		t.Fatalf("second set exit: %d", exit)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if cfg["token.flyio"] != "new" {
		t.Fatalf("overwrite failed: want new, got %q", cfg["token.flyio"])
	}
}

// TestRunConfig_Set_ValueMayContainEquals covers the failure-mode row
// "Key with = in value — handled via strings.Cut first =".
func TestRunConfig_Set_ValueMayContainEquals(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	if _, _, exit := runConfigSubprocess(t, home, []string{"token.flyio=fo1=a=b"}); exit != 0 {
		t.Fatalf("exit: %d", exit)
	}
	cfg, err := readConfig()
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if cfg["token.flyio"] != "fo1=a=b" {
		t.Fatalf("value-with-equals: want fo1=a=b, got %q", cfg["token.flyio"])
	}
}

// ---------- runConfig: remove path ----------

// TestRunConfig_Remove_RemovesKeyAndListOmitsIt covers ADR 1 + docs: after
// --remove, subsequent list does not contain the key.
func TestRunConfig_Remove_RemovesKeyAndListOmitsIt(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	if _, _, exit := runConfigSubprocess(t, home, []string{"token.flyio=fo1_abc"}); exit != 0 {
		t.Fatalf("seed set exit: %d", exit)
	}
	stdout, _, exit := runConfigSubprocess(t, home, []string{"--remove", "token.flyio"})
	if exit != 0 {
		t.Fatalf("remove exit: %d", exit)
	}
	if !strings.Contains(stdout, "removed token.flyio") {
		t.Fatalf("remove stdout: want `removed token.flyio`, got %q", stdout)
	}
	listStdout, _, _ := runConfigSubprocess(t, home, nil)
	if strings.Contains(listStdout, "token.flyio") {
		t.Fatalf("after remove, list still contains token.flyio:\n%s", listStdout)
	}
}

// TestRunConfig_Remove_Idempotent covers failure-mode row:
// "aa config --remove missing.key — exit 0, idempotent".
func TestRunConfig_Remove_Idempotent(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	stdout, _, exit := runConfigSubprocess(t, home, []string{"--remove", "never.set"})
	if exit != 0 {
		t.Fatalf("remove-missing exit: want 0, got %d", exit)
	}
	if !strings.Contains(stdout, "removed never.set") {
		t.Fatalf("remove-missing stdout: want `removed never.set`, got %q", stdout)
	}
}

// TestRunConfig_Remove_NoArgs covers failure-mode row:
// "aa config --remove with no key arg — print usage, exit non-zero".
func TestRunConfig_Remove_NoArgs(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	_, _, exit := runConfigSubprocess(t, home, []string{"--remove"})
	if exit == 0 {
		t.Fatal("exit: want non-zero, got 0")
	}
}

// TestRunConfig_Remove_MultipleKeys covers ADR 1 consequence: --remove
// accepts multiple keys in one invocation.
func TestRunConfig_Remove_MultipleKeys(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	if _, _, exit := runConfigSubprocess(t, home, []string{
		"token.flyio=fo1_abc",
		"defaults.app=my-team-app",
	}); exit != 0 {
		t.Fatalf("seed set exit: %d", exit)
	}
	stdout, _, exit := runConfigSubprocess(t, home, []string{"--remove", "token.flyio", "defaults.app"})
	if exit != 0 {
		t.Fatalf("multi-remove exit: %d", exit)
	}
	if !strings.Contains(stdout, "removed token.flyio") || !strings.Contains(stdout, "removed defaults.app") {
		t.Fatalf("multi-remove stdout missing lines: %q", stdout)
	}
	cfg, _ := readConfig()
	if _, present := cfg["token.flyio"]; present {
		t.Fatalf("token.flyio still present: %v", cfg)
	}
	if _, present := cfg["defaults.app"]; present {
		t.Fatalf("defaults.app still present: %v", cfg)
	}
}

// ---------- misc/regression ----------

// TestRunConfig_Set_RejectsEmptyKey covers the failure-mode row
// "Empty key (=value) — treated as invalid; reject at parse stage".
func TestRunConfig_Set_RejectsEmptyKey(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	_, stderr, exit := runConfigSubprocess(t, home, []string{"=somevalue"})
	if exit == 0 {
		t.Fatal("empty key: want non-zero exit, got 0")
	}
	if stderr == "" {
		t.Fatal("empty key: want non-empty stderr")
	}
}

// TestRunConfig_List_FreshSandbox_MatchesDocsExactly combines the
// empty-sandbox path with exact-output assertion since scripts rely on
// the literal string per docs.
func TestRunConfig_List_FreshSandbox_MatchesDocsExactly(t *testing.T) {
	home, _ := prepareIsolatedHome(t)

	stdout, _, exit := runConfigSubprocess(t, home, nil)
	if exit != 0 {
		t.Fatalf("exit: %d", exit)
	}
	if stdout != "(no config set)\n" {
		t.Fatalf("empty-store list: want `(no config set)\\n`, got %q", stdout)
	}
}

// captureStdout sanity usage — compile-time reference so the helper is not
// flagged unused if every test ends up using the subprocess route.
var _ = captureStdout
var _ = fmt.Sprintf
