// Package dockerimage — tag_test.go: unit tests for tag derivation.
//
// Covers ADR 1: default tag is registry.fly.io/aa-apps/<basename>:latest,
// with basename lowercased and stripped to [a-z0-9-]. Each test asserts one
// behavior in isolation.
package dockerimage

import (
	"path/filepath"
	"strings"
	"testing"
)

// ResolveTag derives the default tag from the path basename when --tag is empty.
func TestResolveTagDefaultFromBasename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myapi")
	got, err := ResolveTag(dir, "")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	want := "registry.fly.io/aa-apps/myapi:latest"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// ResolveTag honors an explicit --tag by returning it verbatim.
func TestResolveTagExplicitWins(t *testing.T) {
	got, err := ResolveTag("/irrelevant/path", "registry.fly.io/aa-apps/custom:v1")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	if got != "registry.fly.io/aa-apps/custom:v1" {
		t.Fatalf("got %q", got)
	}
}

// ResolveTag lowercases an uppercase basename per ADR 1 sanitization.
func TestResolveTagLowercasesBasename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "MyAPI")
	got, err := ResolveTag(dir, "")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	if !strings.HasSuffix(got, "/myapi:latest") {
		t.Fatalf("basename not lowercased: %q", got)
	}
}

// ResolveTag strips characters outside [a-z0-9-] from the basename.
func TestResolveTagStripsInvalidChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_api!!")
	got, err := ResolveTag(dir, "")
	if err != nil {
		t.Fatalf("ResolveTag: %v", err)
	}
	if !strings.HasSuffix(got, "/myapi:latest") {
		t.Fatalf("invalid chars not stripped: %q", got)
	}
}

// ResolveTag errors if the sanitized basename is empty.
func TestResolveTagErrorsOnEmptySanitizedBasename(t *testing.T) {
	_, err := ResolveTag("!!!", "")
	if err == nil {
		t.Fatal("expected error for unsanitizable basename, got nil")
	}
}

// SanitizeBasename lowercases ASCII input.
func TestSanitizeBasenameLowercases(t *testing.T) {
	if got := SanitizeBasename("MyAPI"); got != "myapi" {
		t.Fatalf("got %q, want %q", got, "myapi")
	}
}

// SanitizeBasename strips underscores, dots, and punctuation outside [a-z0-9-].
func TestSanitizeBasenameStripsPunctuation(t *testing.T) {
	if got := SanitizeBasename("hello_world.v2!"); got != "helloworldv2" {
		t.Fatalf("got %q", got)
	}
}

// SanitizeBasename keeps hyphens.
func TestSanitizeBasenameKeepsHyphens(t *testing.T) {
	if got := SanitizeBasename("my-cool-app"); got != "my-cool-app" {
		t.Fatalf("got %q", got)
	}
}

// SanitizeBasename returns empty string for empty input.
func TestSanitizeBasenameEmpty(t *testing.T) {
	if got := SanitizeBasename(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// SanitizeBasename trims leading and trailing hyphens.
func TestSanitizeBasenameTrimsHyphens(t *testing.T) {
	if got := SanitizeBasename("---foo---"); got != "foo" {
		t.Fatalf("got %q", got)
	}
}

// ParseFullyQualified splits host/repo:tag into parts.
func TestParseFullyQualifiedHostRepoTag(t *testing.T) {
	ref, err := ParseFullyQualified("registry.fly.io/aa-apps/myapi:latest")
	if err != nil {
		t.Fatalf("ParseFullyQualified: %v", err)
	}
	if ref.Host != "registry.fly.io" {
		t.Errorf("Host=%q", ref.Host)
	}
	if ref.Repo != "aa-apps/myapi" {
		t.Errorf("Repo=%q", ref.Repo)
	}
	if ref.Reference != "latest" {
		t.Errorf("Reference=%q", ref.Reference)
	}
}

// ParseFullyQualified accepts a digest reference.
func TestParseFullyQualifiedDigest(t *testing.T) {
	ref, err := ParseFullyQualified("registry.fly.io/aa-apps/myapi@sha256:abc123")
	if err != nil {
		t.Fatalf("ParseFullyQualified: %v", err)
	}
	if !strings.HasPrefix(ref.Reference, "sha256:") {
		t.Errorf("Reference=%q", ref.Reference)
	}
}

// ParseFullyQualified rejects bare names with no host segment.
func TestParseFullyQualifiedRejectsBareName(t *testing.T) {
	if _, err := ParseFullyQualified("myapi"); err == nil {
		t.Fatal("expected error on bare name")
	}
}

// ParseFullyQualified rejects an empty string.
func TestParseFullyQualifiedRejectsEmpty(t *testing.T) {
	if _, err := ParseFullyQualified(""); err == nil {
		t.Fatal("expected error on empty string")
	}
}

// FuzzSanitizeBasename: no input panics; output contains only [a-z0-9-].
func FuzzSanitizeBasename(f *testing.F) {
	f.Add("MyAPI")
	f.Add("")
	f.Add("---")
	f.Add("foo_bar.baz!")
	f.Fuzz(func(t *testing.T, in string) {
		out := SanitizeBasename(in)
		for _, r := range out {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				t.Fatalf("SanitizeBasename(%q) returned forbidden rune %q in %q", in, r, out)
			}
		}
	})
}

// FuzzParseFullyQualified: no input panics.
func FuzzParseFullyQualified(f *testing.F) {
	f.Add("registry.fly.io/aa-apps/myapi:latest")
	f.Add("")
	f.Add("x/y:z")
	f.Add("@@@")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = ParseFullyQualified(in)
	})
}
