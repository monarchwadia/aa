// Package main — patch_parser.go implements ParsePatch over a git-format-patch
// stream. This file lives in Strict mode per docs/PHILOSOPHY.md: agent-produced
// patch bytes are adversarial input, every field boundary is untrusted, and
// every error names the offending line and the reason it failed.
//
// The parser is deliberately hand-written (no regex, no framework) so a fresh
// Claude session can read it top-to-bottom and understand exactly which bytes
// are classified how. Any line whose role inside a diff section cannot be
// classified is either a known hunk/context line (ignored) or an error — never
// a shrug.
package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ParsePatch reads a git-format-patch stream and returns a Patch.
//
// Example:
//
//	patchBytes, _ := os.ReadFile("/tmp/agent-output.patch")
//	p, err := ParsePatch(bytes.NewReader(patchBytes))
//	if err != nil {
//	    log.Fatalf("parse patch: %v", err)
//	}
//	for _, cf := range p.ChangedFiles {
//	    fmt.Printf("%-8s %s\n", cf.Change, cf.Path)
//	}
//
// Behavior summary:
//   - Each `From <sha>` header increments CommitCount.
//   - Each `diff --git a/<path> b/<path>` starts a new file section. Quoted
//     paths (e.g. `"a/path with spaces.txt"`) are unquoted; `a/` and `b/`
//     prefixes are stripped.
//   - `new file mode` → Change="add"; `deleted file mode` → Change="delete";
//     `rename from <old>` + `rename to <new>` → Change="rename" with OldPath;
//     otherwise Change="modify".
//   - Binary sections (`Binary files ... differ` or `GIT binary patch`) still
//     produce a ChangedFile with the classification derived from any mode
//     lines above them.
//   - If the same Path appears in multiple commits, the earliest Change wins
//     (the patch's net effect on the upstream tree is what matters).
//   - Malformed input (e.g. a `diff --git` line without two path components)
//     returns an error naming the offending line.
//   - Empty input returns an empty Patch and no error.
//
// The returned Patch.RawBytes contains the exact bytes read from r.
func ParsePatch(r io.Reader) (Patch, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Patch{}, fmt.Errorf("reading patch bytes: %w", err)
	}

	patch := Patch{RawBytes: raw}
	if len(raw) == 0 {
		return patch, nil
	}

	// Track dedupe: a path seen before keeps its earliest Change.
	seenIndex := make(map[string]int)

	lines := strings.Split(string(raw), "\n")

	// current is the ChangedFile being accumulated for the active
	// `diff --git` section; nil when outside any section.
	var current *ChangedFile
	// flush commits current to patch.ChangedFiles (or merges into the
	// first-seen entry for that Path).
	flush := func() {
		if current == nil {
			return
		}
		if current.Change == "" {
			current.Change = "modify"
		}
		if idx, ok := seenIndex[current.Path]; ok {
			// Path already recorded; preserve first-seen Change / OldPath.
			// Only upgrade a previously-empty OldPath when the later
			// section carries rename metadata AND the first-seen entry is
			// also a rename (shouldn't happen in practice, but keeps the
			// merge idempotent).
			if patch.ChangedFiles[idx].Change == "rename" && patch.ChangedFiles[idx].OldPath == "" && current.OldPath != "" {
				patch.ChangedFiles[idx].OldPath = current.OldPath
			}
		} else {
			seenIndex[current.Path] = len(patch.ChangedFiles)
			patch.ChangedFiles = append(patch.ChangedFiles, *current)
		}
		current = nil
	}

	for lineNum, line := range lines {
		switch {
		case strings.HasPrefix(line, "From ") && looksLikeFromCommitHeader(line):
			// Top-level mbox `From <sha> Mon Sep 17 ...` — new commit.
			// Close out any open diff section first.
			flush()
			patch.CommitCount++

		case strings.HasPrefix(line, "diff --git "):
			flush()
			oldPath, newPath, parseErr := parseDiffGitLine(line)
			if parseErr != nil {
				return Patch{}, fmt.Errorf("line %d %q: %w", lineNum+1, line, parseErr)
			}
			// Default Path is the b-side (post-change); overridden by
			// `rename to` later in the section if present.
			current = &ChangedFile{Path: newPath}
			_ = oldPath // retained only via rename lines; a/b split alone
			// is not treated as a rename because non-renamed files also
			// have distinct a/ and b/ prefixes.

		case current != nil && strings.HasPrefix(line, "new file mode "):
			current.Change = "add"

		case current != nil && strings.HasPrefix(line, "deleted file mode "):
			current.Change = "delete"

		case current != nil && strings.HasPrefix(line, "rename from "):
			oldPath, unquoteErr := unquoteGitPath(strings.TrimPrefix(line, "rename from "))
			if unquoteErr != nil {
				return Patch{}, fmt.Errorf("line %d %q: rename from: %w", lineNum+1, line, unquoteErr)
			}
			if validateErr := validateRenamePath(oldPath); validateErr != nil {
				return Patch{}, fmt.Errorf("line %d %q: rename from: %w", lineNum+1, line, validateErr)
			}
			current.Change = "rename"
			current.OldPath = oldPath

		case current != nil && strings.HasPrefix(line, "rename to "):
			newPath, unquoteErr := unquoteGitPath(strings.TrimPrefix(line, "rename to "))
			if unquoteErr != nil {
				return Patch{}, fmt.Errorf("line %d %q: rename to: %w", lineNum+1, line, unquoteErr)
			}
			if validateErr := validateRenamePath(newPath); validateErr != nil {
				return Patch{}, fmt.Errorf("line %d %q: rename to: %w", lineNum+1, line, validateErr)
			}
			current.Change = "rename"
			current.Path = newPath

		default:
			// All other lines inside a diff section (hunk headers, context
			// lines, +/- lines, index line, ---/+++ markers, similarity
			// index, copy from/to, Binary files line, GIT binary patch
			// body, mbox From:/Subject:/Date: headers, the `--` trailer,
			// signature lines) are intentionally ignored. None of them
			// change our classification once mode and rename lines have
			// been observed.
		}
	}
	flush()

	return patch, nil
}

// looksLikeFromCommitHeader distinguishes the mbox commit-header form
// `From <40-hex-sha> Mon Sep 17 00:00:00 2001` from the `From: author@...`
// RFC-822 header. We key on the second token being a run of hex digits —
// git-format-patch always emits a 40-char SHA there.
func looksLikeFromCommitHeader(line string) bool {
	// "From " prefix already matched by caller.
	rest := line[len("From "):]
	sp := strings.IndexByte(rest, ' ')
	if sp <= 0 {
		return false
	}
	sha := rest[:sp]
	if len(sha) < 7 { // minimum abbreviated SHA length git ever emits
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// parseDiffGitLine splits a `diff --git <a-path> <b-path>` line into its two
// path components, handling the quoted form git uses for paths containing
// special characters. Returned paths have their `a/` or `b/` prefix stripped.
// Returns an error if the line does not contain exactly two path tokens.
func parseDiffGitLine(line string) (oldPath string, newPath string, err error) {
	// Strip the fixed prefix.
	const prefix = "diff --git "
	rest := strings.TrimPrefix(line, prefix)

	tokens, tokErr := tokenizePathPair(rest)
	if tokErr != nil {
		return "", "", tokErr
	}
	if len(tokens) != 2 {
		return "", "", fmt.Errorf("diff --git expects 2 path tokens, got %d", len(tokens))
	}

	oldPath, err = stripSidePrefix(tokens[0], "a/")
	if err != nil {
		return "", "", fmt.Errorf("a-side path: %w", err)
	}
	newPath, err = stripSidePrefix(tokens[1], "b/")
	if err != nil {
		return "", "", fmt.Errorf("b-side path: %w", err)
	}
	return oldPath, newPath, nil
}

// validateRenamePath enforces the same invariants on a `rename from` /
// `rename to` path that stripSidePrefix enforces on a `diff --git` path:
// non-empty, no trailing slash, no leaked `a/` or `b/` prefix.
func validateRenamePath(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("path %q ends with / (not a file)", p)
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return fmt.Errorf("path %q has a/ or b/ prefix (should be bare repo-relative)", p)
	}
	return nil
}

// stripSidePrefix strips the required side prefix (`a/` or `b/`) and
// validates the remaining path is non-empty, has no trailing slash, and
// does not itself begin with `a/` or `b/` — the last check preserves the
// invariant tested by FuzzParsePatch that no returned Path leaks a
// side-prefix.
func stripSidePrefix(raw, required string) (string, error) {
	if !strings.HasPrefix(raw, required) {
		return "", fmt.Errorf("expected %s prefix on %q", required, raw)
	}
	stripped := raw[len(required):]
	if stripped == "" {
		return "", fmt.Errorf("empty path after stripping %s on %q", required, raw)
	}
	if strings.HasSuffix(stripped, "/") {
		return "", fmt.Errorf("path %q ends with / (not a file)", stripped)
	}
	if strings.HasPrefix(stripped, "a/") || strings.HasPrefix(stripped, "b/") {
		return "", fmt.Errorf("path %q still has side prefix after stripping", stripped)
	}
	return stripped, nil
}

// tokenizePathPair splits a trailing-of-diff-git string into quoted or
// bare whitespace-delimited tokens. Quoted tokens use git's C-style escape
// rules (same as unquoteGitPath).
func tokenizePathPair(s string) ([]string, error) {
	var tokens []string
	i := 0
	for i < len(s) {
		// Skip leading whitespace.
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '"' {
			// Find matching closing quote, honoring backslash escapes.
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					j += 2
					continue
				}
				if s[j] == '"' {
					break
				}
				j++
			}
			if j >= len(s) {
				return nil, errors.New("unterminated quoted path")
			}
			quoted := s[i : j+1]
			unquoted, err := unquoteGitPath(quoted)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, unquoted)
			i = j + 1
		} else {
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '\t' {
				j++
			}
			tokens = append(tokens, s[i:j])
			i = j
		}
	}
	return tokens, nil
}

// unquoteGitPath decodes a path the way git renders one when it contains
// characters that need quoting (spaces, non-ASCII, control chars). Git
// wraps the path in double quotes and C-escapes special bytes. If the input
// is not double-quoted, it's returned as-is (already literal).
//
// Supported escapes (per git's quote.c): \a \b \t \n \v \f \r \\ \" and
// \NNN octal byte. Unknown escapes are an error — Strict mode.
func unquoteGitPath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s, nil
	}
	inner := s[1 : len(s)-1]
	var out strings.Builder
	out.Grow(len(inner))
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}
		if i+1 >= len(inner) {
			return "", errors.New("trailing backslash in quoted path")
		}
		next := inner[i+1]
		switch next {
		case 'a':
			out.WriteByte('\a')
			i++
		case 'b':
			out.WriteByte('\b')
			i++
		case 't':
			out.WriteByte('\t')
			i++
		case 'n':
			out.WriteByte('\n')
			i++
		case 'v':
			out.WriteByte('\v')
			i++
		case 'f':
			out.WriteByte('\f')
			i++
		case 'r':
			out.WriteByte('\r')
			i++
		case '\\':
			out.WriteByte('\\')
			i++
		case '"':
			out.WriteByte('"')
			i++
		default:
			// Octal \NNN (three digits).
			if next >= '0' && next <= '7' && i+3 < len(inner) &&
				inner[i+2] >= '0' && inner[i+2] <= '7' &&
				inner[i+3] >= '0' && inner[i+3] <= '7' {
				value := (int(next-'0') << 6) | (int(inner[i+2]-'0') << 3) | int(inner[i+3]-'0')
				if value > 0xFF {
					return "", fmt.Errorf("octal escape \\%c%c%c out of byte range", next, inner[i+2], inner[i+3])
				}
				out.WriteByte(byte(value))
				i += 3
				continue
			}
			return "", fmt.Errorf("unknown escape \\%c in quoted path", next)
		}
	}
	return out.String(), nil
}
