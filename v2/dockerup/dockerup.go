// Package dockerup is the wave-3 `aa docker up` slug: a thin composition
// over the sibling flyclient, registry, and extbin packages.
//
// This file is a stub. The real implementation is written in the
// `implement` step of the code-write workflow. The symbols declared here
// exist solely so the red unit and integration test suites can compile.
// Every function panics with "not implemented" — that is the red signal
// the test runner picks up.
//
// Shape of the eventual implementation is spelled out in
// docs/architecture/docker-up.md. In particular:
//   - Label: sha256(absolutePath(<path>))[:12], lower-cased path.
//   - Run:   the four-stage orchestrator (build → push → spawn → attach)
//     with the --force cascade (destroy old machine between push and spawn)
//     and attach-failure cleanup (destroy the new machine).
//
// The seams this slug binds against live in the sibling packages:
// flyclient.Client, registry.Registry, extbin.Runner. This package does
// not define parallel interfaces — it consumes those directly.
package dockerup

import (
	"context"

	"aa/v2/extbin"
	"aa/v2/flyclient"
	"aa/v2/registry"
)

// LabelKey is the literal label key written into SpawnSpec.Labels and
// queried via FindByLabel. Pinned by the docker-up architecture
// amendment (2026-04-23): "aa.up-id".
const LabelKey = "aa.up-id"

// Label returns the identity label value for the given on-disk path. Per
// ADR-1 + the 2026-04-23 amendment, the value is the first 12 lowercase
// hex characters of sha256 over the lower-cased absolute path of <path>.
// Stable across invocations from the same directory; different for
// different directories.
//
// Example:
//
//	Label("/home/alex/myapi") == "7e3f0b9a2c4d" // some stable 12-hex value
func Label(path string) (string, error) {
	panic("dockerup.Label: not implemented (Wave 3)")
}

// Options is the input to Run: everything the orchestrator needs to walk
// the four-stage chain. Collaborators are injected so tests can
// substitute fakes at the seams listed in docs/architecture/docker-up.md.
type Options struct {
	BuildContextPath string // absolute or relative directory containing a Dockerfile.
	Force            bool   // true = replace an existing instance tied to this directory.
	AppName          string // Fly app to spawn into (from ConfigReader.ResolveDefaultApp()).
	RegistryBase     string // registry host (from ConfigReader.ResolveRegistryBase()).

	Fly      flyclient.Client  // machine-lifecycle seam.
	Registry registry.Registry // docker-images push seam.
	ExtBin   extbin.Runner     // docker build + flyctl ssh console seam.
}

// Run is the four-stage orchestrator. It walks build → push → spawn →
// attach sequentially, honours --force (destroy old machine between push
// success and spawn start), and, on attach failure, destroys the spawned
// machine before returning the attach error.
//
// Returns nil on clean shell exit. Returns a non-nil error whose message
// names exactly one failed stage (build, push, spawn, attach).
//
// Stub; Wave 3.
func Run(ctx context.Context, opts Options) error {
	panic("dockerup.Run: not implemented (Wave 3)")
}
