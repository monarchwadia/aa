package main

// config_loader.go is on the strict-mode path (docs/PHILOSOPHY.md § Strict
// mode): it's the user-config boundary. Every JSON decoder uses
// DisallowUnknownFields. Every validation error names the file, field, or
// value at fault. A Config is returned only when fully valid; the zero value
// is returned on any error.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretResolver resolves a single secret reference (e.g. "keyring:foo")
// to its concrete value. Pluggable so tests can supply an in-memory
// resolver without touching a real OS keyring.
type SecretResolver func(ref string) (string, error)

// keyringPrefix marks an env-value that must be resolved via a
// SecretResolver. Plain values without this prefix pass through unchanged.
const keyringPrefix = "keyring:"

// LoadGlobal reads and parses `<homeDir>/.aa/config.json`, then runs the
// full structural validation defined by the strict-mode rules: every
// required top-level field present, every backend has a valid type,
// every process backend has egress_enforcement == "none", every agent
// has a non-empty run command.
//
// Example:
//
//	cfg, err := LoadGlobal("/home/alice")
//	// reads /home/alice/.aa/config.json
func LoadGlobal(homeDir string) (Config, error) {
	path := filepath.Join(homeDir, ".aa", "config.json")

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("loading global config at %s: file not found (config.json missing) — run 'aa init --global' to scaffold one", path)
		}
		return Config{}, fmt.Errorf("loading global config at %s: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("loading global config at %s: invalid JSON: %w", path, err)
	}

	if err := validateGlobalConfig(cfg, path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateGlobalConfig checks the invariants that apply to a global
// Config regardless of any repo overlay. Called once from LoadGlobal and
// again from Merge so every seam re-proves the contract.
func validateGlobalConfig(cfg Config, path string) error {
	if cfg.DefaultBackend == "" {
		return fmt.Errorf("loading global config at %s: missing required field 'default_backend'", path)
	}
	if len(cfg.Backends) == 0 {
		return fmt.Errorf("loading global config at %s: missing required field 'backends' (at least one backend must be defined)", path)
	}
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("loading global config at %s: missing required field 'agents' (at least one agent must be defined)", path)
	}
	if _, ok := cfg.Backends[cfg.DefaultBackend]; !ok {
		return fmt.Errorf("loading global config at %s: default_backend %q is not a key in 'backends'", path, cfg.DefaultBackend)
	}

	for name, backend := range cfg.Backends {
		if backend.Type == "" {
			return fmt.Errorf("loading global config at %s: backend %q missing required field 'type'", path, name)
		}
		switch backend.Type {
		case "local", "fly", "process":
			// valid
		default:
			return fmt.Errorf("loading global config at %s: backend %q has invalid type %q (valid: local, fly, process)", path, name, backend.Type)
		}
		if backend.Type == "process" && backend.EgressEnforcement != "none" {
			return fmt.Errorf("loading global config at %s: backend %q is type 'process' but egress_enforcement = %q; 'process' backend requires egress_enforcement == \"none\"", path, name, backend.EgressEnforcement)
		}
	}

	for name, agent := range cfg.Agents {
		if strings.TrimSpace(agent.Run) == "" {
			return fmt.Errorf("loading global config at %s: agent %q missing required field 'run'", path, name)
		}
	}
	return nil
}

// LoadRepo reads and parses `<repoDir>/aa.json`. The repo config is
// the small, committed-to-the-repo half of the two-config-file split
// (see README § "The two config files"): two required fields, no secrets.
//
// Example:
//
//	rc, err := LoadRepo("/home/alice/src/myapp")
//	// reads /home/alice/src/myapp/aa.json
func LoadRepo(repoDir string) (RepoConfig, error) {
	path := filepath.Join(repoDir, "aa.json")

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RepoConfig{}, fmt.Errorf("loading repo config at %s: file not found (aa.json missing) — run 'aa init' in the repo root to scaffold one", path)
		}
		return RepoConfig{}, fmt.Errorf("loading repo config at %s: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()

	var rc RepoConfig
	if err := decoder.Decode(&rc); err != nil {
		return RepoConfig{}, fmt.Errorf("loading repo config at %s: invalid JSON: %w", path, err)
	}

	if strings.TrimSpace(rc.Image) == "" {
		return RepoConfig{}, fmt.Errorf("loading repo config at %s: missing required field 'image'", path)
	}
	if strings.TrimSpace(rc.Agent) == "" {
		return RepoConfig{}, fmt.Errorf("loading repo config at %s: missing required field 'agent'", path)
	}
	return rc, nil
}

// Merge combines a parsed global Config with a parsed repo RepoConfig
// and re-validates the cross-field invariants. The returned Config keeps
// the full Backends and Agents maps intact; callers select the active
// agent by looking up repo.Agent in Agents. No pruning — the session
// manager is responsible for that downstream.
//
// Example:
//
//	merged, err := Merge(global, repo)
//	// merged.Agents[repo.Agent] is guaranteed to exist
func Merge(global Config, repo RepoConfig) (Config, error) {
	if _, ok := global.Agents[repo.Agent]; !ok {
		return Config{}, fmt.Errorf("merging configs: repo's agent %q is not a key in global agents (known: %s)", repo.Agent, knownKeys(global.Agents))
	}
	if _, ok := global.Backends[global.DefaultBackend]; !ok {
		return Config{}, fmt.Errorf("merging configs: default_backend %q is not a key in global backends (known: %s)", global.DefaultBackend, knownBackendKeys(global.Backends))
	}

	// Re-prove the process-backend invariant at merge time. A hand-built
	// Config handed to Merge (as the tests do) hasn't been through
	// LoadGlobal's validator, so this seam enforces it again.
	for name, backend := range global.Backends {
		if backend.Type == "process" && backend.EgressEnforcement != "none" {
			return Config{}, fmt.Errorf("merging configs: backend %q is type 'process' but egress_enforcement = %q; 'process' backend requires egress_enforcement == \"none\"", name, backend.EgressEnforcement)
		}
	}

	return global, nil
}

// knownKeys returns a sorted-order-ish string listing of agent names,
// used only for error messages to help the user see what they could
// have typed instead of the bad value.
func knownKeys(agents map[string]AgentConfig) string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// knownBackendKeys is the backend-map equivalent of knownKeys.
func knownBackendKeys(backends map[string]BackendConfig) string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// ResolveSecretRefs walks every AgentConfig.Env entry and replaces any
// value beginning with "keyring:" by calling the resolver with the full
// reference string. Non-"keyring:" values pass through unchanged.
//
// The returned Config is a shallow copy with freshly-allocated Agents
// and Env maps so the caller's input is not mutated.
//
// Example:
//
//	resolver := func(ref string) (string, error) {
//	    return keyring.Get("aa", strings.TrimPrefix(ref, "keyring:"))
//	}
//	resolved, err := ResolveSecretRefs(merged, resolver)
func ResolveSecretRefs(cfg Config, resolver SecretResolver) (Config, error) {
	out := cfg
	out.Agents = make(map[string]AgentConfig, len(cfg.Agents))

	for agentName, agent := range cfg.Agents {
		resolvedAgent := agent
		if agent.Env != nil {
			resolvedAgent.Env = make(map[string]string, len(agent.Env))
			for envName, envValue := range agent.Env {
				if strings.HasPrefix(envValue, keyringPrefix) {
					value, err := resolver(envValue)
					if err != nil {
						return Config{}, fmt.Errorf("resolving secret for agent %q env %q ref %q: %w", agentName, envName, envValue, err)
					}
					resolvedAgent.Env[envName] = value
				} else {
					resolvedAgent.Env[envName] = envValue
				}
			}
		}
		out.Agents[agentName] = resolvedAgent
	}
	return out, nil
}
