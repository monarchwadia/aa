// Package registry is the container-registry client contract.
// Implementations talk to registry.fly.io (or equivalent) using a token.
// Bodies are stubs; Wave 2 fills them in.
package registry

import "context"

// Image is one image record from the registry.
type Image struct {
	Tag     string // fully-qualified, e.g. registry.fly.io/aa-apps/myapi:latest
	Digest  string
	SizeB   int64
	PushedAt string // RFC3339
}

// Registry is the surface that docker-images and docker-up consume.
type Registry interface {
	// Login performs whatever auth handshake the registry needs.
	// Idempotent; safe to call repeatedly.
	Login(ctx context.Context) error

	// Push pushes a locally-built image (already tagged) to the registry.
	Push(ctx context.Context, tag string) error

	// List returns every image reachable with the current credential.
	// If prefix is non-empty, List returns only images whose tag starts with prefix.
	List(ctx context.Context, prefix string) ([]Image, error)

	// Delete removes an image from the registry by tag.
	Delete(ctx context.Context, tag string) error
}

// New is defined in registry.go.
