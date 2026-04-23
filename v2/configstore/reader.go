// Package configstore is the config persistence + resolution contract.
// Resolution order for every Resolve*() call: flag (if passed) > env var > config file > built-in default.
package configstore

import "os"

// Built-in defaults and env-var names. Exported so consumers can reference
// them in error messages.
const (
	DefaultAPIBase      = "https://api.machines.dev/v1"
	DefaultRegistryBase = "registry.fly.io"
	DefaultApp          = "aa-apps"
	DefaultImage        = "ubuntu:22.04"

	EnvFlyToken     = "FLY_API_TOKEN"
	EnvAPIBase      = "FLY_API_BASE"
	EnvRegistryBase = "AA_REGISTRY_BASE"

	KeyFlyToken        = "token.flyio"
	KeyEndpointsAPI    = "endpoints.api"
	KeyEndpointsReg    = "endpoints.registry"
	KeyDefaultsApp     = "defaults.app"
	KeyDefaultsImage   = "defaults.image"
)

// Reader exposes the values the rest of aa reads from config.
// One Reader per command invocation. Reads are cheap and idempotent.
type Reader struct {
	flags map[string]string
	file  map[string]string
}

// NewReader loads the config file (if any) and returns a Reader.
// flagValues is the per-command flag overrides, keyed by the same keys used in the file (e.g., "token.flyio").
// Pass nil for flagValues if no per-command overrides apply.
func NewReader(flagValues map[string]string) (*Reader, error) {
	file, err := Read()
	if err != nil {
		return nil, err
	}
	if flagValues == nil {
		flagValues = map[string]string{}
	}
	return &Reader{flags: flagValues, file: file}, nil
}

// resolve returns the first non-empty value from flag > env > file > "".
func (r *Reader) resolve(key, envName string) string {
	if v, ok := r.flags[key]; ok && v != "" {
		return v
	}
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}
	if v, ok := r.file[key]; ok && v != "" {
		return v
	}
	return ""
}

// ResolveFlyToken returns the Fly.io API token.
// Returns ("", false) if no token is set at any layer.
func (r *Reader) ResolveFlyToken() (string, bool) {
	v := r.resolve(KeyFlyToken, EnvFlyToken)
	if v == "" {
		return "", false
	}
	return v, true
}

// ResolveAPIBase returns the Fly Machines API base URL.
func (r *Reader) ResolveAPIBase() string {
	if v := r.resolve(KeyEndpointsAPI, EnvAPIBase); v != "" {
		return v
	}
	return DefaultAPIBase
}

// ResolveRegistryBase returns the container registry host.
func (r *Reader) ResolveRegistryBase() string {
	if v := r.resolve(KeyEndpointsReg, EnvRegistryBase); v != "" {
		return v
	}
	return DefaultRegistryBase
}

// ResolveDefaultApp returns the default Fly app/namespace.
func (r *Reader) ResolveDefaultApp() string {
	if v := r.resolve(KeyDefaultsApp, ""); v != "" {
		return v
	}
	return DefaultApp
}

// ResolveDefaultImage returns the default base image for aa machine spawn.
func (r *Reader) ResolveDefaultImage() string {
	if v := r.resolve(KeyDefaultsImage, ""); v != "" {
		return v
	}
	return DefaultImage
}
