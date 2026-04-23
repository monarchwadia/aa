package testhelpers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These meta-tests pin Sandbox lifecycle behavior:
//   - NewSandbox sets HOME and XDG_CONFIG_HOME into the per-test temp dir
//   - PATH is prepended with the sandbox bin dir
//   - FLY_API_BASE and AA_REGISTRY_BASE are wired to local httptest servers
//   - t.Cleanup tears the sandbox down (temp dir removed, env restored)
//   - Two sandboxes in the same test get different temp dirs
//
// Preconditions: a snapshot file must exist at v2/testdata/snapshots/<name>.json
// for replay-mode tests. These tests exercise the sandbox internals directly;
// they do not invoke the compiled aa binary (that's consumer-slug territory).

func TestSandbox_SetsIsolatedHome(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_home")
	sb := NewSandbox(t, "sandbox_meta_home")

	home := sb.HomeDir()
	if home == "" {
		t.Fatalf("sandbox HomeDir is empty")
	}
	realHome, _ := os.UserHomeDir()
	if realHome != "" && strings.HasPrefix(home, realHome) && home == realHome {
		t.Fatalf("sandbox home must not equal real $HOME: %q", home)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("sandbox home dir should exist on disk: %v", err)
	}
}

func TestSandbox_SetsXDGConfigHomeUnderSandbox(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_xdg")
	sb := NewSandbox(t, "sandbox_meta_xdg")

	xdg := sb.XDGConfigHome()
	home := sb.HomeDir()
	if xdg == "" {
		t.Fatalf("XDGConfigHome is empty")
	}
	if !strings.HasPrefix(xdg, home) {
		t.Fatalf("XDG_CONFIG_HOME %q must be under sandbox HOME %q", xdg, home)
	}
}

func TestSandbox_PrependsBinDirToPATH(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_path")
	sb := NewSandbox(t, "sandbox_meta_path")

	bin := sb.BinDir()
	path := sb.PATH()
	if bin == "" || path == "" {
		t.Fatalf("BinDir or PATH empty: bin=%q path=%q", bin, path)
	}
	// The sandbox's bin dir must be the first entry.
	first := strings.Split(path, string(os.PathListSeparator))[0]
	if first != bin {
		t.Fatalf("expected bin dir %q to be first PATH entry, got %q (full: %q)", bin, first, path)
	}
}

func TestSandbox_WiresFlyAPIBase(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_api")
	sb := NewSandbox(t, "sandbox_meta_api")

	if got := sb.APIBaseURL(); !strings.HasPrefix(got, "http://") && !strings.HasPrefix(got, "https://") {
		t.Fatalf("FLY_API_BASE should be a local http(s) URL, got %q", got)
	}
}

func TestSandbox_WiresRegistryBase(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_registry")
	sb := NewSandbox(t, "sandbox_meta_registry")

	if got := sb.RegistryBaseURL(); !strings.HasPrefix(got, "http://") && !strings.HasPrefix(got, "https://") {
		t.Fatalf("AA_REGISTRY_BASE should be a local http(s) URL, got %q", got)
	}
}

func TestSandbox_CleanupRemovesTempDir(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_cleanup")

	var homePath string
	t.Run("inner", func(inner *testing.T) {
		sb := NewSandbox(inner, "sandbox_meta_cleanup")
		homePath = sb.HomeDir()
	})
	// After the subtest finishes, t.Cleanup callbacks registered on `inner`
	// must have fired. The sandbox temp dir should no longer exist.
	if _, err := os.Stat(homePath); !os.IsNotExist(err) {
		t.Fatalf("expected sandbox home dir %q to be removed after cleanup, stat err=%v", homePath, err)
	}
}

func TestSandbox_TwoSandboxesGetDifferentDirs(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_two_a")
	writeEmptySnapshotFixture(t, "sandbox_meta_two_b")
	a := NewSandbox(t, "sandbox_meta_two_a")
	b := NewSandbox(t, "sandbox_meta_two_b")
	if a.HomeDir() == b.HomeDir() {
		t.Fatalf("two sandboxes must have distinct HOME dirs; both = %q", a.HomeDir())
	}
	if a.BinDir() == b.BinDir() {
		t.Fatalf("two sandboxes must have distinct bin dirs")
	}
}

func TestSandbox_EnvRestoredAfterCleanup(t *testing.T) {
	writeEmptySnapshotFixture(t, "sandbox_meta_envreset")

	const marker = "__AA_TEST_META_MARKER__"
	if err := os.Setenv(marker, "before"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv(marker) })

	t.Run("inner", func(inner *testing.T) {
		_ = NewSandbox(inner, "sandbox_meta_envreset")
		// Sandbox is free to mutate process env during its life; after
		// inner's Cleanup fires the process env should be restored.
	})

	if got := os.Getenv(marker); got != "before" {
		t.Fatalf("env var %s should be restored to 'before' after sandbox cleanup, got %q", marker, got)
	}
}

// writeEmptySnapshotFixture seeds an empty snapshot for the given name under
// v2/testdata/snapshots/. Replay mode requires the file to exist; an empty
// array is the minimum that lets NewSandbox succeed.
func writeEmptySnapshotFixture(t *testing.T, name string) {
	t.Helper()
	// The snapshots dir is a sibling of testhelpers, under v2/testdata.
	// The meta-tests run with cwd = v2/testhelpers.
	base := filepath.Join("..", "testdata", "snapshots")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir snapshots: %v", err)
	}
	path := filepath.Join(base, name+".json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("seed snapshot %s: %v", path, err)
	}
	t.Cleanup(func() { os.Remove(path) })
}
