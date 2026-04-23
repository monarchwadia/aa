// Package flyclient is the Fly.io Machines API client contract.
// Implementations talk to api.machines.dev over HTTP using a token.
// Bodies are stubs; Wave 2 fills them in.
package flyclient

import (
	"context"
	"errors"
)

// ErrNotFound is returned when the backend responds 404 to a resource lookup.
// Callers match with errors.Is(err, ErrNotFound).
// Stub; Wave 2 wires the mapping.
var ErrNotFound = errors.New("flyclient: not found")

// ErrConflict is returned when the backend responds 409 (e.g. create-already-exists).
// Callers match with errors.Is(err, ErrConflict).
// Stub; Wave 2 wires the mapping.
var ErrConflict = errors.New("flyclient: conflict")

// SpawnSpec is the input to Client.Create.
type SpawnSpec struct {
	Image  string
	Region string            // "" = backend default
	Labels map[string]string // metadata tags; aa.* prefix reserved for aa itself
}

// Machine is the Fly-side representation of one machine.
type Machine struct {
	ID     string
	State  string
	Region string
	Labels map[string]string
}

// Client is the surface that machine-lifecycle and docker-up consume.
// Implementations are goroutine-safe.
type Client interface {
	// EnsureApp creates the app if it does not already exist. No-op on success.
	EnsureApp(ctx context.Context, appName string) error

	// Create provisions a new machine in appName and returns it in the initial (backend-assigned) state.
	Create(ctx context.Context, appName string, spec SpawnSpec) (Machine, error)

	// Get returns the machine's current state.
	Get(ctx context.Context, appName, machineID string) (Machine, error)

	// WaitStarted blocks until the machine reports state=="started" or the context expires.
	WaitStarted(ctx context.Context, appName, machineID string) error

	// List returns every machine in appName that the token can see.
	List(ctx context.Context, appName string) ([]Machine, error)

	// Start, Stop, Destroy are the lifecycle verbs. Destroy with force=true removes running machines.
	Start(ctx context.Context, appName, machineID string) error
	Stop(ctx context.Context, appName, machineID string) error
	Destroy(ctx context.Context, appName, machineID string, force bool) error

	// FindByLabel returns every machine in appName whose Labels contains an exact match for key=value.
	// Zero matches returns (nil, nil). Not an error.
	FindByLabel(ctx context.Context, appName, key, value string) ([]Machine, error)
}

// New is defined in client.go; see that file for the constructor doc comment.
