// Package dockerimage is the docker-images slug's business logic:
// tag derivation, argv construction, and command orchestration.
//
// tag.go in particular owns the rules for turning a user-supplied build-context
// path plus optional --tag override into a fully-qualified registry tag, per
// ADR 1 of docs/architecture/docker-images.md.
package dockerimage

import (
	"fmt"
	"path/filepath"
	"strings"
)

// defaultRegistryHost is the Fly registry host used for defaults per ADR 1.
const defaultRegistryHost = "registry.fly.io"

// defaultNamespace is the tool-owned namespace per ADR 1.
const defaultNamespace = "aa-apps"

// ImageRef is a parsed fully-qualified registry reference.
// Example: ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"}.
type ImageRef struct {
	Host      string
	Repo      string
	Reference string // tag (":latest") or digest ("sha256:...")
}

// String returns the fully-qualified form: host/repo:reference (or host/repo@digest).
//
// Example: ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"}.String()
// returns "registry.fly.io/aa-apps/myapi:latest".
func (r ImageRef) String() string {
	if strings.HasPrefix(r.Reference, "sha256:") {
		return r.Host + "/" + r.Repo + "@" + r.Reference
	}
	return r.Host + "/" + r.Repo + ":" + r.Reference
}

// ResolveTag produces the fully-qualified tag for a build invocation.
// If explicit is non-empty it wins; otherwise the tag is derived from the
// basename of path: registry.fly.io/aa-apps/<sanitized-basename>:latest.
//
// Example: ResolveTag("/home/alice/myapi", "") returns
// "registry.fly.io/aa-apps/myapi:latest".
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
	return fmt.Sprintf("%s/%s/%s:latest", defaultRegistryHost, defaultNamespace, sanitized), nil
}

// ParseFullyQualified parses a tag string into an ImageRef.
// Accepts host/repo:tag and host/repo@digest; rejects bare names and empty input.
//
// Example: ParseFullyQualified("registry.fly.io/aa-apps/myapi:latest") returns
// ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"}.
func ParseFullyQualified(s string) (ImageRef, error) {
	if s == "" {
		return ImageRef{}, fmt.Errorf("empty image reference")
	}
	// Split off digest first ("@sha256:..."), else tag (":...").
	var left, reference string
	if at := strings.LastIndex(s, "@"); at >= 0 {
		left = s[:at]
		reference = s[at+1:]
	} else if colon := strings.LastIndex(s, ":"); colon >= 0 {
		// Only treat as tag separator if there's no '/' after it
		// (otherwise ':' could be part of a port number like host:port/repo).
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
	// Host must look like a host (contain a dot or a colon, or equal localhost)
	// to avoid accepting "aa-apps/myapi:latest" as host=aa-apps.
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return ImageRef{}, fmt.Errorf("reference missing host segment: %q", s)
	}
	return ImageRef{Host: host, Repo: repo, Reference: reference}, nil
}

// SanitizeBasename lowercases s and removes characters not in [a-z0-9-].
// Leading/trailing hyphens are trimmed. Empty input yields empty output.
//
// Example: SanitizeBasename("My_Cool.App!") returns "mycoolapp".
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
