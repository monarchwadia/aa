package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture patches. Hand-crafted to be minimal but realistic git-format-patch
// output. Each constant isolates a single scenario so test failures point to
// the exact behavior that broke.
//
// Format reminders (RFC-mbox-ish style emitted by `git format-patch`):
//   From <sha> Mon Sep 17 00:00:00 2001
//   From: Author <a@b>
//   Date: ...
//   Subject: [PATCH] ...
//
//   <body>
//   ---
//    path | N +-
//    1 file changed, ...
//
//   diff --git a/<path> b/<path>
//   [new file mode <mode>|deleted file mode <mode>|similarity index N%
//    rename from <old>
//    rename to <new>]
//   index <hash>..<hash> <mode>
//   --- a/<path>|/dev/null
//   +++ b/<path>|/dev/null
//   @@ hunk
//    <context>
//   +<added>
//   -<removed>
//   --
//   2.40.0
// ---------------------------------------------------------------------------

const patchSingleAdd = `From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] add hello

---
 hello.txt | 1 +
 1 file changed, 1 insertion(+)
 create mode 100644 hello.txt

diff --git a/hello.txt b/hello.txt
new file mode 100644
index 0000000..ce01362
--- /dev/null
+++ b/hello.txt
@@ -0,0 +1 @@
+hello
--
2.40.0

`

const patchSingleModify = `From 2222222222222222222222222222222222222222 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] tweak hello

---
 hello.txt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/hello.txt b/hello.txt
index ce01362..3b18e51 100644
--- a/hello.txt
+++ b/hello.txt
@@ -1 +1 @@
-hello
+hello world
--
2.40.0

`

const patchSingleDelete = `From 3333333333333333333333333333333333333333 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] drop hello

---
 hello.txt | 1 -
 1 file changed, 1 deletion(-)
 delete mode 100644 hello.txt

diff --git a/hello.txt b/hello.txt
deleted file mode 100644
index ce01362..0000000
--- a/hello.txt
+++ /dev/null
@@ -1 +0,0 @@
-hello
--
2.40.0

`

const patchRename = `From 4444444444444444444444444444444444444444 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] rename hello

---
 hello.txt => greetings.txt | 0
 1 file changed, 0 insertions(+), 0 deletions(-)

diff --git a/hello.txt b/greetings.txt
similarity index 100%
rename from hello.txt
rename to greetings.txt
--
2.40.0

`

// patchMultiCommit: two `From <sha>` headers; first adds a.txt, second
// modifies b.txt. Parser must see both commits and both files.
const patchMultiCommit = `From aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH 1/2] add a

---
 a.txt | 1 +
 1 file changed, 1 insertion(+)
 create mode 100644 a.txt

diff --git a/a.txt b/a.txt
new file mode 100644
index 0000000..7898192
--- /dev/null
+++ b/a.txt
@@ -0,0 +1 @@
+a
--
2.40.0


From bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:01:00 +0000
Subject: [PATCH 2/2] touch b

---
 b.txt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/b.txt b/b.txt
index 6178079..61780aa 100644
--- a/b.txt
+++ b/b.txt
@@ -1 +1 @@
-old
+new
--
2.40.0

`

// patchBinary: a binary-file change rendered via `Binary files differ`. The
// parser must still recognize the Path and a Change classification.
const patchBinary = `From 5555555555555555555555555555555555555555 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] add logo

---
 assets/logo.png | Bin 0 -> 42 bytes
 1 file changed, 0 insertions(+), 0 deletions(-)
 create mode 100644 assets/logo.png

diff --git a/assets/logo.png b/assets/logo.png
new file mode 100644
index 0000000000000000000000000000000000000000..deadbeefcafebabe1234567890abcdef00000000
Binary files /dev/null and b/assets/logo.png differ
--
2.40.0

`

// patchOnlyHeaders: a From-header with no diff body at all. Edge case — an
// agent that somehow committed nothing with a diff (shouldn't happen in
// practice but the parser must not crash).
const patchOnlyHeaders = `From 6666666666666666666666666666666666666666 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] nothing

---
--
2.40.0

`

// patchSpaceInPath: git quotes paths containing special characters. The
// parser must unquote the `"a/path with spaces.txt"` form back to
// `path with spaces.txt`.
const patchSpaceInPath = `From 7777777777777777777777777777777777777777 Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] add spaced

---
 "path with spaces.txt" | 1 +
 1 file changed, 1 insertion(+)
 create mode 100644 "path with spaces.txt"

diff --git "a/path with spaces.txt" "b/path with spaces.txt"
new file mode 100644
index 0000000..ce01362
--- /dev/null
+++ "b/path with spaces.txt"
@@ -0,0 +1 @@
+hi
--
2.40.0

`

// patchAddThenModify: commit 1 adds c.txt; commit 2 modifies c.txt. The net
// effect is still "add" — a file that did not exist upstream now exists.
//
// CHOICE: we collapse per-path, preserving only the earliest Change for a
// given Path. Rule evaluation cares about what this patch does to the
// upstream working tree, not the intermediate steps. So the expected output
// is ONE ChangedFile with Path="c.txt", Change="add".
//
// The parser must dedupe and preserve the first-seen Change.
const patchAddThenModify = `From cccccccccccccccccccccccccccccccccccccccc Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH 1/2] add c

---
 c.txt | 1 +
 1 file changed, 1 insertion(+)
 create mode 100644 c.txt

diff --git a/c.txt b/c.txt
new file mode 100644
index 0000000..7898192
--- /dev/null
+++ b/c.txt
@@ -0,0 +1 @@
+c
--
2.40.0


From dddddddddddddddddddddddddddddddddddddddd Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:01:00 +0000
Subject: [PATCH 2/2] tweak c

---
 c.txt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/c.txt b/c.txt
index 7898192..61780aa 100644
--- a/c.txt
+++ b/c.txt
@@ -1 +1 @@
-c
+c again
--
2.40.0

`

// patchMalformed: a diff --git line with only one path component instead of
// two. A strict parser must refuse this rather than guess.
const patchMalformed = `From eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee Mon Sep 17 00:00:00 2001
From: Test Agent <agent@example.com>
Date: Thu, 23 Apr 2026 12:00:00 +0000
Subject: [PATCH] broken

---
diff --git a/only-one-path
new file mode 100644
`

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func findFile(files []ChangedFile, path string) (ChangedFile, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return ChangedFile{}, false
}

// ---------------------------------------------------------------------------
// TestParsePatch — behavior tests. Subtest names read as assertions.
// ---------------------------------------------------------------------------

func TestParsePatch(t *testing.T) {
	t.Run("new_file_mode_yields_add", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchSingleAdd))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := p.CommitCount, 1; got != want {
			t.Errorf("CommitCount = %d, want %d", got, want)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1 (%+v)", len(p.ChangedFiles), p.ChangedFiles)
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "hello.txt" {
			t.Errorf("Path = %q, want %q", cf.Path, "hello.txt")
		}
		if cf.Change != "add" {
			t.Errorf("Change = %q, want %q", cf.Change, "add")
		}
		if cf.OldPath != "" {
			t.Errorf("OldPath = %q, want empty", cf.OldPath)
		}
	})

	t.Run("plain_diff_yields_modify", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchSingleModify))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1", len(p.ChangedFiles))
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "hello.txt" || cf.Change != "modify" {
			t.Errorf("got %+v, want {Path:hello.txt Change:modify}", cf)
		}
	})

	t.Run("deleted_file_mode_yields_delete", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchSingleDelete))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1", len(p.ChangedFiles))
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "hello.txt" || cf.Change != "delete" {
			t.Errorf("got %+v, want {Path:hello.txt Change:delete}", cf)
		}
	})

	t.Run("rename_headers_populate_OldPath", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchRename))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1", len(p.ChangedFiles))
		}
		cf := p.ChangedFiles[0]
		if cf.Change != "rename" {
			t.Errorf("Change = %q, want %q", cf.Change, "rename")
		}
		if cf.Path != "greetings.txt" {
			t.Errorf("Path = %q, want %q (post-rename)", cf.Path, "greetings.txt")
		}
		if cf.OldPath != "hello.txt" {
			t.Errorf("OldPath = %q, want %q", cf.OldPath, "hello.txt")
		}
	})

	t.Run("multi_commit_sums_commits_and_collects_all_files", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchMultiCommit))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := p.CommitCount, 2; got != want {
			t.Errorf("CommitCount = %d, want %d", got, want)
		}
		if len(p.ChangedFiles) != 2 {
			t.Fatalf("ChangedFiles len = %d, want 2 (%+v)", len(p.ChangedFiles), p.ChangedFiles)
		}
		if a, ok := findFile(p.ChangedFiles, "a.txt"); !ok || a.Change != "add" {
			t.Errorf("a.txt entry = %+v ok=%v, want Change=add", a, ok)
		}
		if b, ok := findFile(p.ChangedFiles, "b.txt"); !ok || b.Change != "modify" {
			t.Errorf("b.txt entry = %+v ok=%v, want Change=modify", b, ok)
		}
	})

	t.Run("binary_change_is_still_classified", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchBinary))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1", len(p.ChangedFiles))
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "assets/logo.png" {
			t.Errorf("Path = %q, want %q", cf.Path, "assets/logo.png")
		}
		if cf.Change != "add" {
			t.Errorf("Change = %q, want %q (new file mode present)", cf.Change, "add")
		}
	})

	t.Run("empty_input_yields_empty_patch", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(""))
		if err != nil {
			t.Fatalf("unexpected error on empty input: %v", err)
		}
		if p.CommitCount != 0 {
			t.Errorf("CommitCount = %d, want 0", p.CommitCount)
		}
		if len(p.ChangedFiles) != 0 {
			t.Errorf("ChangedFiles len = %d, want 0", len(p.ChangedFiles))
		}
	})

	t.Run("headers_only_no_body_yields_empty_changed_files", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchOnlyHeaders))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.CommitCount != 1 {
			t.Errorf("CommitCount = %d, want 1", p.CommitCount)
		}
		if len(p.ChangedFiles) != 0 {
			t.Errorf("ChangedFiles len = %d, want 0 (%+v)", len(p.ChangedFiles), p.ChangedFiles)
		}
	})

	t.Run("path_with_spaces_is_unquoted", func(t *testing.T) {
		p, err := ParsePatch(strings.NewReader(patchSpaceInPath))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1", len(p.ChangedFiles))
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "path with spaces.txt" {
			t.Errorf("Path = %q, want %q (quotes stripped, spaces preserved)", cf.Path, "path with spaces.txt")
		}
		if cf.Change != "add" {
			t.Errorf("Change = %q, want add", cf.Change)
		}
	})

	t.Run("add_then_modify_collapses_to_single_add_entry", func(t *testing.T) {
		// CHOICE (documented in fixture comment): dedupe per-Path, preserve
		// first-seen Change. Net effect of add-then-modify is add.
		p, err := ParsePatch(strings.NewReader(patchAddThenModify))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.CommitCount != 2 {
			t.Errorf("CommitCount = %d, want 2", p.CommitCount)
		}
		if len(p.ChangedFiles) != 1 {
			t.Fatalf("ChangedFiles len = %d, want 1 (collapsed); got %+v", len(p.ChangedFiles), p.ChangedFiles)
		}
		cf := p.ChangedFiles[0]
		if cf.Path != "c.txt" || cf.Change != "add" {
			t.Errorf("got %+v, want {Path:c.txt Change:add}", cf)
		}
	})

	t.Run("malformed_patch_returns_error", func(t *testing.T) {
		_, err := ParsePatch(strings.NewReader(patchMalformed))
		if err == nil {
			t.Fatalf("expected error on malformed patch, got nil")
		}
	})

	t.Run("a_and_b_prefixes_are_stripped", func(t *testing.T) {
		// Covered implicitly by every fixture above (none of them expect
		// "a/" or "b/" prefixes in their Path assertions). Pin it explicitly
		// here so a regression is unambiguous.
		p, err := ParsePatch(strings.NewReader(patchSingleModify))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, cf := range p.ChangedFiles {
			if strings.HasPrefix(cf.Path, "a/") || strings.HasPrefix(cf.Path, "b/") {
				t.Errorf("Path %q retains a/ or b/ prefix", cf.Path)
			}
			if strings.HasPrefix(cf.OldPath, "a/") || strings.HasPrefix(cf.OldPath, "b/") {
				t.Errorf("OldPath %q retains a/ or b/ prefix", cf.OldPath)
			}
		}
	})

	t.Run("RawBytes_round_trip_equals_input", func(t *testing.T) {
		// Patch.RawBytes is spec'd as "the unmodified patch bytes as read";
		// the parser consumes an io.Reader, so it must buffer those bytes
		// into the returned struct.
		in := []byte(patchSingleAdd)
		p, err := ParsePatch(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(p.RawBytes, in) {
			t.Errorf("RawBytes mismatch: got %d bytes, want %d", len(p.RawBytes), len(in))
		}
	})

	t.Run("reader_error_propagates", func(t *testing.T) {
		_, err := ParsePatch(errReader{})
		if err == nil {
			t.Fatalf("expected error from failing reader, got nil")
		}
	})
}

// errReader always fails on Read. Lets us assert the parser surfaces I/O
// errors rather than swallowing them.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }

var _ io.Reader = errReader{}

// ---------------------------------------------------------------------------
// FuzzParsePatch — invariants that must hold over arbitrary input.
//
// Seed with every happy-path fixture so the fuzzer starts from realistic
// shapes and mutates outward.
//
// Invariants:
//   1. Never panic.
//   2. If err == nil, every ChangedFile.Path is non-empty and does NOT
//      begin with "a/" or "b/".
//   3. If err == nil, every ChangedFile.Change is one of the four spec'd
//      values.
// ---------------------------------------------------------------------------

func FuzzParsePatch(f *testing.F) {
	seeds := []string{
		patchSingleAdd,
		patchSingleModify,
		patchSingleDelete,
		patchRename,
		patchMultiCommit,
		patchBinary,
		patchOnlyHeaders,
		patchSpaceInPath,
		patchAddThenModify,
		"",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePatch(bytes.NewReader(data))
		if err != nil {
			return // errors are allowed; panics are not.
		}
		for i, cf := range p.ChangedFiles {
			if cf.Path == "" {
				t.Errorf("ChangedFiles[%d].Path is empty", i)
			}
			if strings.HasPrefix(cf.Path, "a/") || strings.HasPrefix(cf.Path, "b/") {
				t.Errorf("ChangedFiles[%d].Path = %q still has a/ or b/ prefix", i, cf.Path)
			}
			switch cf.Change {
			case "add", "modify", "delete", "rename":
			default:
				t.Errorf("ChangedFiles[%d].Change = %q, not one of add|modify|delete|rename", i, cf.Change)
			}
		}
	})
}
