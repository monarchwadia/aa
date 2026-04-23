package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Integration tests: exercise the full config-loader pipeline against
// real files on disk in a temp directory. No in-memory shortcuts.
//
// These tests must be RED (panic from stub bodies) until the wave-1
// implementation lands in `config_loader.go`.

// writeIntegrationFile is a local helper so this file does not depend on
// any helpers in config_loader_test.go — each test file stays
// self-contained, matching the "each test builds its own fixtures"
// convention.
func writeIntegrationFile(t *testing.T, root, relpath, body string) {
	t.Helper()
	full := filepath.Join(root, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// TestLoadGlobal_Integration_ReadmeExample exercises the exact example
// from README § "Global config (`~/.aa/config.json`)". If the README
// drifts or the loader drifts from it, this breaks.
func TestLoadGlobal_Integration_ReadmeExample(t *testing.T) {
	home := t.TempDir()
	body := `{
  "default_backend": "local",

  "backends": {
    "local": {
      "type": "local",
      "egress_enforcement": "strict"
    },
    "fly": {
      "type": "fly",
      "region": "iad"
    }
  },

  "agents": {
    "claude-code": {
      "run": "claude --dangerously-skip-permissions",
      "env": {
        "ANTHROPIC_API_KEY": "keyring:anthropic"
      },
      "egress_allowlist": ["api.anthropic.com"]
    },
    "aider": {
      "run": "aider --yes",
      "env": { "OPENAI_API_KEY": "keyring:openai" },
      "egress_allowlist": ["api.openai.com"]
    }
  },

  "rules": [
    { "type": "gitHooksChanged",        "severity": "error" },
    { "type": "ciConfigChanged",        "severity": "error" },
    { "type": "packageManifestChanged", "severity": "warn"  },
    { "type": "lockfileChanged",        "severity": "warn"  },
    { "type": "dockerfileChanged",      "severity": "warn"  },
    { "type": "buildScriptChanged",     "severity": "warn"  }
  ]
}`
	writeIntegrationFile(t, home, ".aa/config.json", body)

	cfg, err := LoadGlobal(home)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}

	// default_backend
	if cfg.DefaultBackend != "local" {
		t.Errorf("DefaultBackend = %q, want local", cfg.DefaultBackend)
	}

	// backends
	if got := cfg.Backends["local"]; got.Type != "local" || got.EgressEnforcement != "strict" {
		t.Errorf("Backends[local] = %+v, want {local, strict}", got)
	}
	if got := cfg.Backends["fly"]; got.Type != "fly" || got.Region != "iad" {
		t.Errorf("Backends[fly] = %+v, want {fly, iad}", got)
	}

	// agents
	cc, ok := cfg.Agents["claude-code"]
	if !ok {
		t.Fatalf("Agents missing claude-code")
	}
	if cc.Run != "claude --dangerously-skip-permissions" {
		t.Errorf("claude-code.Run = %q", cc.Run)
	}
	if cc.Env["ANTHROPIC_API_KEY"] != "keyring:anthropic" {
		t.Errorf("claude-code.Env[ANTHROPIC_API_KEY] = %q", cc.Env["ANTHROPIC_API_KEY"])
	}
	if len(cc.EgressAllowlist) != 1 || cc.EgressAllowlist[0] != "api.anthropic.com" {
		t.Errorf("claude-code.EgressAllowlist = %v", cc.EgressAllowlist)
	}
	aider, ok := cfg.Agents["aider"]
	if !ok {
		t.Fatalf("Agents missing aider")
	}
	if aider.Run != "aider --yes" {
		t.Errorf("aider.Run = %q", aider.Run)
	}

	// rules — 6 entries, first is gitHooksChanged/error
	if len(cfg.Rules) != 6 {
		t.Fatalf("Rules len = %d, want 6", len(cfg.Rules))
	}
	if cfg.Rules[0].Type != "gitHooksChanged" || cfg.Rules[0].Severity != "error" {
		t.Errorf("Rules[0] = %+v, want {gitHooksChanged, error}", cfg.Rules[0])
	}
}

// TestConfigLoader_Integration_EndToEnd exercises the full pipeline:
//
//   1. Write <tmp>/.aa/config.json and <tmp>/repo/aa.json to disk.
//   2. LoadGlobal + LoadRepo.
//   3. Merge.
//   4. ResolveSecretRefs with a fake resolver returning fixed values.
//
// Then assert the final Config has the expected backend, agent run
// string, and resolved env value.
func TestConfigLoader_Integration_EndToEnd(t *testing.T) {
	tmp := t.TempDir()

	globalBody := `{
  "default_backend": "local",
  "backends": {
    "local":   { "type": "local",   "egress_enforcement": "strict" },
    "process": { "type": "process", "egress_enforcement": "none" }
  },
  "agents": {
    "claude-code": {
      "run": "claude --dangerously-skip-permissions",
      "env": {
        "ANTHROPIC_API_KEY": "keyring:anthropic",
        "AA_STATIC": "hello"
      },
      "egress_allowlist": ["api.anthropic.com"]
    }
  },
  "rules": [
    { "type": "gitHooksChanged", "severity": "error" }
  ]
}`
	repoBody := `{
  "image": ".devcontainer/Dockerfile",
  "agent": "claude-code"
}`

	writeIntegrationFile(t, tmp, ".aa/config.json", globalBody)
	writeIntegrationFile(t, tmp, "repo/aa.json", repoBody)

	global, err := LoadGlobal(tmp)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	repo, err := LoadRepo(filepath.Join(tmp, "repo"))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	merged, err := Merge(global, repo)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Resolver returns a fixed mapping for keyring references.
	resolver := func(ref string) (string, error) {
		switch ref {
		case "keyring:anthropic":
			return "sk-real-resolved-value", nil
		}
		return "", &fixedResolverError{ref: ref}
	}

	resolved, err := ResolveSecretRefs(merged, resolver)
	if err != nil {
		t.Fatalf("ResolveSecretRefs: %v", err)
	}

	// Backend assertions.
	b, ok := resolved.Backends[resolved.DefaultBackend]
	if !ok {
		t.Fatalf("merged default backend %q not found in Backends", resolved.DefaultBackend)
	}
	if b.Type != "local" || b.EgressEnforcement != "strict" {
		t.Errorf("default backend = %+v, want {local, strict}", b)
	}

	// Agent assertions.
	agent, ok := resolved.Agents[repo.Agent]
	if !ok {
		t.Fatalf("merged Agents missing %q", repo.Agent)
	}
	if agent.Run != "claude --dangerously-skip-permissions" {
		t.Errorf("agent.Run = %q", agent.Run)
	}

	// Env: keyring ref was resolved; plain value passed through.
	if agent.Env["ANTHROPIC_API_KEY"] != "sk-real-resolved-value" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want sk-real-resolved-value", agent.Env["ANTHROPIC_API_KEY"])
	}
	if agent.Env["AA_STATIC"] != "hello" {
		t.Errorf("AA_STATIC = %q, want hello (plain value must pass through unchanged)", agent.Env["AA_STATIC"])
	}

	// Egress allowlist preserved.
	if len(agent.EgressAllowlist) != 1 || agent.EgressAllowlist[0] != "api.anthropic.com" {
		t.Errorf("EgressAllowlist = %v", agent.EgressAllowlist)
	}
}

// TestConfigLoader_Integration_MergeRejectsUnknownAgent proves that a
// repo pointing at an agent the user hasn't configured fails fast at
// Merge time, with a message naming the missing agent.
func TestConfigLoader_Integration_MergeRejectsUnknownAgent(t *testing.T) {
	tmp := t.TempDir()

	globalBody := `{
  "default_backend": "local",
  "backends": { "local": { "type": "local", "egress_enforcement": "strict" } },
  "agents": {
    "claude-code": { "run": "claude", "env": {}, "egress_allowlist": ["api.anthropic.com"] }
  },
  "rules": []
}`
	repoBody := `{
  "image": "./Dockerfile",
  "agent": "aider-not-configured"
}`

	writeIntegrationFile(t, tmp, ".aa/config.json", globalBody)
	writeIntegrationFile(t, tmp, "repo/aa.json", repoBody)

	global, err := LoadGlobal(tmp)
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	repo, err := LoadRepo(filepath.Join(tmp, "repo"))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	_, err = Merge(global, repo)
	if err == nil {
		t.Fatal("Merge: expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "aider-not-configured") {
		t.Errorf("error should name the missing agent; got: %v", err)
	}
}

// fixedResolverError is a small typed error used to prove that
// resolver errors returned to ResolveSecretRefs are propagated with
// enough context for a human to debug.
type fixedResolverError struct {
	ref string
}

func (e *fixedResolverError) Error() string {
	return "no value for ref " + e.ref
}
