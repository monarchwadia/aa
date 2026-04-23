package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the contract of the `config-loader` workstream.
// They must be RED (panic from the stub) until the wave-1 implementation
// lands in `config_loader.go`.
//
// Contract choice for Merge (documented here so the implementation can
// conform): Merge returns a Config whose shape is the same as the global
// Config — all backends and all agents remain present. The RepoConfig's
// Agent field identifies which agent the session will use, but Merge
// does NOT prune the other agents from the map. Validation errors
// (missing default_backend, missing agent, bad process-backend egress)
// are surfaced here; field-level pruning is deferred to the session
// manager.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeFile creates the parent directory tree under root and writes
// `body` to root/relpath. Fails the test on I/O error.
func writeFile(t *testing.T, root, relpath, body string) {
	t.Helper()
	full := filepath.Join(root, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// validGlobalJSON returns a well-formed global config body used by
// happy-path tests across files in this package.
func validGlobalJSON() string {
	return `{
  "default_backend": "local",
  "backends": {
    "local": { "type": "local", "egress_enforcement": "strict" },
    "process": { "type": "process", "egress_enforcement": "none" }
  },
  "agents": {
    "claude-code": {
      "run": "claude --dangerously-skip-permissions",
      "env": { "ANTHROPIC_API_KEY": "keyring:anthropic" },
      "egress_allowlist": ["api.anthropic.com"]
    }
  },
  "rules": [
    { "type": "gitHooksChanged", "severity": "error" },
    { "type": "packageManifestChanged", "severity": "warn" }
  ]
}`
}

// validRepoJSON returns a well-formed repo config body.
func validRepoJSON() string {
	return `{
  "image": ".devcontainer/Dockerfile",
  "agent": "claude-code"
}`
}

// ---------------------------------------------------------------------------
// LoadGlobal
// ---------------------------------------------------------------------------

func TestLoadGlobal_HappyPath(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, ".aa/config.json", validGlobalJSON())

	cfg, err := LoadGlobal(home)
	if err != nil {
		t.Fatalf("LoadGlobal: unexpected error: %v", err)
	}
	if cfg.DefaultBackend != "local" {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, "local")
	}
	if _, ok := cfg.Backends["local"]; !ok {
		t.Errorf("Backends missing `local`")
	}
	if _, ok := cfg.Backends["process"]; !ok {
		t.Errorf("Backends missing `process`")
	}
	agent, ok := cfg.Agents["claude-code"]
	if !ok {
		t.Fatalf("Agents missing `claude-code`")
	}
	if agent.Run == "" {
		t.Errorf("agent.Run is empty")
	}
	if agent.Env["ANTHROPIC_API_KEY"] != "keyring:anthropic" {
		t.Errorf("agent.Env[ANTHROPIC_API_KEY] = %q, want keyring:anthropic", agent.Env["ANTHROPIC_API_KEY"])
	}
	if len(agent.EgressAllowlist) != 1 || agent.EgressAllowlist[0] != "api.anthropic.com" {
		t.Errorf("agent.EgressAllowlist = %v, want [api.anthropic.com]", agent.EgressAllowlist)
	}
	if len(cfg.Rules) != 2 {
		t.Errorf("Rules len = %d, want 2", len(cfg.Rules))
	}
}

func TestLoadGlobal_MissingFile(t *testing.T) {
	home := t.TempDir() // no .aa/config.json written
	_, err := LoadGlobal(home)
	if err == nil {
		t.Fatal("LoadGlobal: expected error for missing file, got nil")
	}
	// Make sure the error mentions the file we were looking for so the
	// user knows which path was wrong.
	msg := err.Error()
	if !strings.Contains(msg, "config.json") {
		t.Errorf("error message should mention `config.json`; got: %v", err)
	}
}

func TestLoadGlobal_InvalidJSON(t *testing.T) {
	home := t.TempDir()
	writeFile(t, home, ".aa/config.json", "{ this is not valid json")
	_, err := LoadGlobal(home)
	if err == nil {
		t.Fatal("LoadGlobal: expected error for invalid JSON, got nil")
	}
}

func TestLoadGlobal_MissingDefaultBackend(t *testing.T) {
	// No default_backend key at all.
	body := `{
  "backends": { "local": { "type": "local", "egress_enforcement": "strict" } },
  "agents": { "x": { "run": "echo hi" } },
  "rules": []
}`
	home := t.TempDir()
	writeFile(t, home, ".aa/config.json", body)
	_, err := LoadGlobal(home)
	if err == nil {
		t.Fatal("LoadGlobal: expected error for missing default_backend, got nil")
	}
	if !strings.Contains(err.Error(), "default_backend") {
		t.Errorf("error should name the missing field `default_backend`; got: %v", err)
	}
}

func TestLoadGlobal_MissingBackends(t *testing.T) {
	body := `{
  "default_backend": "local",
  "agents": { "x": { "run": "echo hi" } },
  "rules": []
}`
	home := t.TempDir()
	writeFile(t, home, ".aa/config.json", body)
	_, err := LoadGlobal(home)
	if err == nil {
		t.Fatal("LoadGlobal: expected error for missing backends, got nil")
	}
	if !strings.Contains(err.Error(), "backends") {
		t.Errorf("error should name `backends`; got: %v", err)
	}
}

func TestLoadGlobal_MissingAgents(t *testing.T) {
	body := `{
  "default_backend": "local",
  "backends": { "local": { "type": "local", "egress_enforcement": "strict" } },
  "rules": []
}`
	home := t.TempDir()
	writeFile(t, home, ".aa/config.json", body)
	_, err := LoadGlobal(home)
	if err == nil {
		t.Fatal("LoadGlobal: expected error for missing agents, got nil")
	}
	if !strings.Contains(err.Error(), "agents") {
		t.Errorf("error should name `agents`; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadRepo
// ---------------------------------------------------------------------------

func TestLoadRepo_HappyPath(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "aa.json", validRepoJSON())

	rc, err := LoadRepo(repo)
	if err != nil {
		t.Fatalf("LoadRepo: unexpected error: %v", err)
	}
	if rc.Image != ".devcontainer/Dockerfile" {
		t.Errorf("Image = %q, want .devcontainer/Dockerfile", rc.Image)
	}
	if rc.Agent != "claude-code" {
		t.Errorf("Agent = %q, want claude-code", rc.Agent)
	}
}

func TestLoadRepo_MissingFile(t *testing.T) {
	repo := t.TempDir()
	_, err := LoadRepo(repo)
	if err == nil {
		t.Fatal("LoadRepo: expected error for missing aa.json, got nil")
	}
	if !strings.Contains(err.Error(), "aa.json") {
		t.Errorf("error should mention `aa.json`; got: %v", err)
	}
}

func TestLoadRepo_InvalidJSON(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "aa.json", "{ not json")
	_, err := LoadRepo(repo)
	if err == nil {
		t.Fatal("LoadRepo: expected error for invalid JSON, got nil")
	}
}

func TestLoadRepo_MissingAgent(t *testing.T) {
	body := `{ "image": "./Dockerfile" }`
	repo := t.TempDir()
	writeFile(t, repo, "aa.json", body)
	_, err := LoadRepo(repo)
	if err == nil {
		t.Fatal("LoadRepo: expected error for missing agent, got nil")
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("error should name the missing `agent` field; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// makeGlobalConfig returns a typed Config mirroring validGlobalJSON,
// so Merge tests do not depend on LoadGlobal's JSON parsing.
func makeGlobalConfig() Config {
	return Config{
		DefaultBackend: "local",
		Backends: map[string]BackendConfig{
			"local": {Type: "local", EgressEnforcement: "strict"},
			"process": {Type: "process", EgressEnforcement: "none"},
		},
		Agents: map[string]AgentConfig{
			"claude-code": {
				Run:             "claude --dangerously-skip-permissions",
				Env:             map[string]string{"ANTHROPIC_API_KEY": "keyring:anthropic"},
				EgressAllowlist: []string{"api.anthropic.com"},
			},
		},
		Rules: []Rule{{Type: "gitHooksChanged", Severity: "error"}},
	}
}

func TestMerge_HappyPath(t *testing.T) {
	global := makeGlobalConfig()
	repo := RepoConfig{Image: ".devcontainer/Dockerfile", Agent: "claude-code"}

	merged, err := Merge(global, repo)
	if err != nil {
		t.Fatalf("Merge: unexpected error: %v", err)
	}
	// Contract: merged Config preserves backends + agents maps intact.
	// The repo's agent choice is expressed by the fact that it exists in
	// the merged Agents map; callers pick the one by name.
	if _, ok := merged.Agents[repo.Agent]; !ok {
		t.Errorf("merged.Agents missing the repo's agent %q", repo.Agent)
	}
	if merged.DefaultBackend != "local" {
		t.Errorf("merged.DefaultBackend = %q, want local", merged.DefaultBackend)
	}
	if _, ok := merged.Backends["local"]; !ok {
		t.Errorf("merged.Backends missing `local`")
	}
}

func TestMerge_AgentNotInGlobal(t *testing.T) {
	global := makeGlobalConfig()
	repo := RepoConfig{Image: "./Dockerfile", Agent: "no-such-agent"}
	_, err := Merge(global, repo)
	if err == nil {
		t.Fatal("Merge: expected error when repo agent missing from global, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-agent") {
		t.Errorf("error should name the missing agent; got: %v", err)
	}
}

func TestMerge_DefaultBackendNotInBackends(t *testing.T) {
	global := makeGlobalConfig()
	global.DefaultBackend = "bogus"
	repo := RepoConfig{Image: "./Dockerfile", Agent: "claude-code"}
	_, err := Merge(global, repo)
	if err == nil {
		t.Fatal("Merge: expected error when default_backend missing from backends, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") && !strings.Contains(err.Error(), "default_backend") {
		t.Errorf("error should mention the bogus default_backend; got: %v", err)
	}
}

func TestMerge_ProcessBackendWithStrictEgress(t *testing.T) {
	global := makeGlobalConfig()
	// Force the invariant violation per INTENT: process backend cannot
	// enforce egress.
	global.Backends["process"] = BackendConfig{Type: "process", EgressEnforcement: "strict"}
	repo := RepoConfig{Image: "./Dockerfile", Agent: "claude-code"}
	_, err := Merge(global, repo)
	if err == nil {
		t.Fatal("Merge: expected error when process backend has strict egress, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "process") {
		t.Errorf("error should mention `process`; got: %v", err)
	}
	if !strings.Contains(msg, "egress") {
		t.Errorf("error should mention `egress`; got: %v", err)
	}
}

func TestMerge_ProcessBackendWithEmptyEgress(t *testing.T) {
	// Any value other than "none" is invalid for process. Empty string
	// must also fail — "none" has to be explicit.
	global := makeGlobalConfig()
	global.Backends["process"] = BackendConfig{Type: "process", EgressEnforcement: ""}
	repo := RepoConfig{Image: "./Dockerfile", Agent: "claude-code"}
	_, err := Merge(global, repo)
	if err == nil {
		t.Fatal("Merge: expected error when process backend has empty egress_enforcement, got nil")
	}
}

// ---------------------------------------------------------------------------
// ResolveSecretRefs
// ---------------------------------------------------------------------------

// mapResolver returns a SecretResolver that looks up refs in a map.
// Missing entries return an error.
func mapResolver(m map[string]string) SecretResolver {
	return func(ref string) (string, error) {
		v, ok := m[ref]
		if !ok {
			return "", errors.New("no value for " + ref)
		}
		return v, nil
	}
}

func TestResolveSecretRefs_KeyringResolves(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"claude-code": {
				Run: "claude",
				Env: map[string]string{"ANTHROPIC_API_KEY": "keyring:anthropic"},
			},
		},
	}
	resolver := mapResolver(map[string]string{"keyring:anthropic": "sk-test-abc"})

	out, err := ResolveSecretRefs(cfg, resolver)
	if err != nil {
		t.Fatalf("ResolveSecretRefs: unexpected error: %v", err)
	}
	got := out.Agents["claude-code"].Env["ANTHROPIC_API_KEY"]
	if got != "sk-test-abc" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want sk-test-abc", got)
	}
}

func TestResolveSecretRefs_PlainValuesPassThrough(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"x": {
				Run: "echo",
				Env: map[string]string{
					"PLAIN":   "literal-value",
					"SECRET":  "keyring:foo",
				},
			},
		},
	}
	resolver := mapResolver(map[string]string{"keyring:foo": "FOO_VALUE"})

	out, err := ResolveSecretRefs(cfg, resolver)
	if err != nil {
		t.Fatalf("ResolveSecretRefs: unexpected error: %v", err)
	}
	env := out.Agents["x"].Env
	if env["PLAIN"] != "literal-value" {
		t.Errorf("PLAIN = %q, want literal-value", env["PLAIN"])
	}
	if env["SECRET"] != "FOO_VALUE" {
		t.Errorf("SECRET = %q, want FOO_VALUE", env["SECRET"])
	}
}

func TestResolveSecretRefs_ResolverError(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"x": {
				Run: "echo",
				Env: map[string]string{"API_KEY": "keyring:missing"},
			},
		},
	}
	resolver := mapResolver(map[string]string{}) // nothing present

	_, err := ResolveSecretRefs(cfg, resolver)
	if err == nil {
		t.Fatal("ResolveSecretRefs: expected error from failing resolver, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "API_KEY") {
		t.Errorf("error should name the env var `API_KEY`; got: %v", err)
	}
	if !strings.Contains(msg, "keyring:missing") {
		t.Errorf("error should name the unresolved ref `keyring:missing`; got: %v", err)
	}
}

func TestResolveSecretRefs_NoAgents(t *testing.T) {
	// An empty config should resolve cleanly.
	cfg := Config{Agents: map[string]AgentConfig{}}
	resolver := mapResolver(nil)
	if _, err := ResolveSecretRefs(cfg, resolver); err != nil {
		t.Fatalf("ResolveSecretRefs: unexpected error on empty config: %v", err)
	}
}
