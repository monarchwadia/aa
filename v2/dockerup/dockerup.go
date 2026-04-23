// Package dockerup is the wave-3 `aa docker up` slug: a thin composition
// over the sibling flyclient, registry, and extbin packages.
package dockerup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aa/v2/extbin"
	"aa/v2/flyclient"
	"aa/v2/registry"
)

// LabelKey is the literal label key written into SpawnSpec.Labels and
// queried via FindByLabel. Pinned by the docker-up architecture
// amendment (2026-04-23): "aa.up-id".
const LabelKey = "aa.up-id"

// Label returns the identity label value for the given on-disk path:
// the first 12 lowercase hex chars of sha256(strings.ToLower(absPath(path))).
func Label(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(strings.ToLower(abs)))
	return hex.EncodeToString(h[:])[:12], nil
}

// Options is the input to Run.
type Options struct {
	BuildContextPath string
	Force            bool
	AppName          string
	RegistryBase     string

	Fly      flyclient.Client
	Registry registry.Registry
	ExtBin   extbin.Runner

	Stdout io.Writer
	Stderr io.Writer
}

// Run is the four-stage orchestrator: build → push → spawn → attach.
func Run(ctx context.Context, opts Options) error {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	dockerfilePath := filepath.Join(opts.BuildContextPath, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err != nil {
		return fmt.Errorf("no Dockerfile at %s — aa docker up requires a directory containing a Dockerfile", dockerfilePath)
	}

	label, err := Label(opts.BuildContextPath)
	if err != nil {
		return fmt.Errorf("label: %w", err)
	}

	matches, err := opts.Fly.FindByLabel(ctx, opts.AppName, LabelKey, label)
	if err != nil {
		return fmt.Errorf("find existing: %w", err)
	}
	if len(matches) > 0 && !opts.Force {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		joined := strings.Join(ids, ", ")
		return fmt.Errorf(
			"instance %s is already tied to %s — pass --force to replace it, or run 'aa machine rm %s' first",
			joined, opts.BuildContextPath, ids[0],
		)
	}

	regBase := opts.RegistryBase
	if regBase == "" {
		regBase = "registry.fly.io"
	}
	tag := fmt.Sprintf("%s/aa-apps/aa-up-%s:latest", regBase, label)

	// Build
	code, err := opts.ExtBin.Run(ctx, extbin.Invocation{
		Name:   "docker",
		Argv:   []string{"build", "-t", tag, opts.BuildContextPath},
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("build: docker exited %d", code)
	}

	// Push
	if err := opts.Registry.Push(ctx, tag); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	// Destroy old (force)
	if opts.Force {
		for _, m := range matches {
			if err := opts.Fly.Destroy(ctx, opts.AppName, m.ID, true); err != nil {
				return fmt.Errorf("destroy old machine %s: %w", m.ID, err)
			}
		}
	}

	// Spawn
	machine, err := opts.Fly.Create(ctx, opts.AppName, flyclient.SpawnSpec{
		Image:  tag,
		Labels: map[string]string{LabelKey: label},
	})
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}

	// Attach
	code, err = opts.ExtBin.Run(ctx, extbin.Invocation{
		Name:   "flyctl",
		Argv:   []string{"ssh", "console", "--app", opts.AppName, "--machine", machine.ID},
		Stdin:  os.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
	if err != nil || code != 0 {
		_ = opts.Fly.Destroy(ctx, opts.AppName, machine.ID, true)
		if err != nil {
			return fmt.Errorf("attach: %w", err)
		}
		return fmt.Errorf("attach: flyctl exited %d", code)
	}
	return nil
}
