package main

import "io"

// Patch is the parsed representation of a git-format patch file produced by
// `git format-patch origin/<branch>..HEAD --stdout`.
type Patch struct {
	// RawBytes are the unmodified patch bytes as read from the sandbox.
	// Persisted to the laptop for review and re-application.
	RawBytes []byte

	// ChangedFiles lists every file the patch touches, in the order they
	// appear in the patch. Populated by the parser.
	ChangedFiles []ChangedFile

	// CommitCount is the number of commits the patch encompasses (number
	// of `From <sha>` headers in git-format-patch output).
	CommitCount int
}

// ChangedFile is one file touched by a patch.
type ChangedFile struct {
	// Path is the file path relative to repo root, as it appears in the
	// patch's `diff --git a/<path> b/<path>` line.
	Path string

	// Change is "add", "modify", "delete", or "rename".
	Change string

	// OldPath is the pre-rename path when Change == "rename"; otherwise "".
	OldPath string
}

// ParsePatch reads a git-format-patch stream and returns a Patch.
// Pure function over an io.Reader. Implemented in patch_parser.go (wave 1
// workstream `patch-parser`).
func ParsePatch(r io.Reader) (Patch, error) {
	panic("unimplemented — see workstream `patch-parser` in docs/architecture/aa.md § Workstreams")
}
