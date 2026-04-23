// Package dockerimage — cmd.go: top-level dispatcher for `aa docker image`.
//
// Run executes one of build/push/ls/rm against injected collaborators.
// Production wires a real extbin.Runner and registry.Registry; tests inject
// fakes (an in-memory runner and an httptest.Server-backed registry).
// Body is a stub until Wave 3.
package dockerimage

import (
	"context"
	"io"

	"aa/v2/extbin"
	"aa/v2/registry"
)

// Deps is the injection surface for Run.
type Deps struct {
	DockerRunner extbin.Runner
	Registry     registry.Registry
	Token        string
	TokenKey     string // e.g. "token.flyio" — named in error messages.
	Stdout       io.Writer
	Stderr       io.Writer
}

// Run executes one of build/push/ls/rm and returns a process exit code.
// argv is everything AFTER "docker image" (e.g. ["build", "./myapi"]).
func Run(ctx context.Context, deps Deps, argv []string) int {
	panic("dockerimage.Run: not implemented (Wave 3)")
}
