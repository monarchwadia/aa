// Package imageref is a leaf package: parsing, sanitization, and default-tag
// derivation for fully-qualified container registry references.
//
// It exists as its own package so both v2/dockerimage (the CLI verb layer)
// and v2/registry (the HTTP client) can depend on it without creating an
// import cycle. See ADR 1 of v2/docs/architecture/docker-images.md and the
// "imageref split" amendment for the rationale.
package imageref

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultRegistryHost is the Fly registry host used for defaults per ADR 1.
const DefaultRegistryHost = "registry.fly.io"

// DefaultNamespace is the tool-owned namespace per ADR 1.
const DefaultNamespace = "aa-apps"

// ImageRef is a parsed fully-qualified registry reference.
// Example: ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"}.
type ImageRef struct {
	Host      string
	Repo      string
	Reference string // tag (":latest") or digest ("sha256:...")
}

// String returns the fully-qualified form: host/repo:reference (or host/repo@digest).
func (r ImageRef) String() string {
	if strings.HasPrefix(r.Reference, "sha256:") {
		return r.Host + "/" + r.Repo + "@" + r.Reference
	}
	return r.Host + "/" + r.Repo + ":" + r.Reference
}

// ResolveTag produces the fully-qualified tag for a build invocation.
// If explicit is non-empty it wins; otherwise the tag is derived from the
// basename of path: registry.fly.io/aa-apps/<sanitized-basename>:latest.
func ResolveTag(path string, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	abs := path
	if path == "." || path == "" {
		if cwd, err := filepath.Abs("."); err == nil {
			abs = cwd
		}
	}
	base := filepath.Base(abs)
	sanitized := SanitizeBasename(base)
	if sanitized == "" {
		return "", fmt.Errorf("cannot derive tag: basename %q sanitized to empty — pass --tag explicitly", base)
	}
	return fmt.Sprintf("%s/%s/%s:latest", DefaultRegistryHost, DefaultNamespace, sanitized), nil
}

// ParseFullyQualified parses a tag string into an ImageRef.
// Accepts host/repo:tag and host/repo@digest; rejects bare names and empty input.
func ParseFullyQualified(s string) (ImageRef, error) {
	if s == "" {
		return ImageRef{}, fmt.Errorf("empty image reference")
	}
	var left, reference string
	if at := strings.LastIndex(s, "@"); at >= 0 {
		left = s[:at]
		reference = s[at+1:]
	} else if colon := strings.LastIndex(s, ":"); colon >= 0 {
		if !strings.Contains(s[colon:], "/") {
			left = s[:colon]
			reference = s[colon+1:]
		} else {
			left = s
		}
	} else {
		left = s
	}
	if reference == "" {
		return ImageRef{}, fmt.Errorf("reference missing tag or digest: %q", s)
	}
	slash := strings.Index(left, "/")
	if slash < 0 {
		return ImageRef{}, fmt.Errorf("reference missing host segment: %q", s)
	}
	host := left[:slash]
	repo := left[slash+1:]
	if host == "" || repo == "" {
		return ImageRef{}, fmt.Errorf("reference missing host or repo: %q", s)
	}
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return ImageRef{}, fmt.Errorf("reference missing host segment: %q", s)
	}
	return ImageRef{Host: host, Repo: repo, Reference: reference}, nil
}

// SanitizeBasename lowercases s and removes characters not in [a-z0-9-].
// Leading/trailing hyphens are trimmed. Empty input yields empty output.
func SanitizeBasename(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.Trim(out, "-")
	return out
}
