// Package configstore reader_test.go covers the store's Reader: per-invocation
// construction plus the five Resolve*() helpers that implement the documented
// precedence "flag > env > config file > built-in default" (architecture
// ADR 3). Each test corresponds to a documented behavior: a Resolve*() helper's
// precedence layer, its built-in default, or NewReader's handling of missing
// and malformed config files. No third-party deps; stdlib testing only.
//
// All tests here are expected to be RED during Wave 1: reader.go bodies panic
// with "not implemented". A few tests deliberately assert the panic surface
// to document that contract; all others let the panic surface as the red
// state per the integration-unit-tests skill rules.
package configstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile writes a config file inside a per-test isolated HOME so
// NewReader's os.UserConfigDir() resolves there. Returns the HOME it set up.
func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Linux: os.UserConfigDir respects XDG_CONFIG_HOME first, then $HOME/.config.
	// Pin XDG_CONFIG_HOME so the test works regardless of platform defaults.
	xdg := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "aa")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if contents != "" {
		if err := os.WriteFile(filepath.Join(dir, "config"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write config file: %v", err)
		}
	}
	return home
}

// isolateEnv wipes the env vars that Resolve*() checks so a stray value in the
// host env can't make a precedence test accidentally pass.
func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FLY_API_TOKEN", "")
	t.Setenv("FLY_API_BASE", "")
	t.Setenv("AA_REGISTRY_BASE", "")
}

// TestNewReader_MissingFile_ReturnsEmptyReader exercises the documented
// "missing file = empty config, not an error" failure-mode row.
func TestNewReader_MissingFile_ReturnsEmptyReader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader on missing file: want nil err, got %v", err)
	}
	if r == nil {
		t.Fatal("NewReader: want non-nil reader, got nil")
	}
	// With nothing set anywhere, ResolveFlyToken reports "not set".
	tok, ok := r.ResolveFlyToken()
	if ok || tok != "" {
		t.Fatalf("ResolveFlyToken on empty store: want (\"\", false), got (%q, %v)", tok, ok)
	}
}

// TestResolveFlyToken_FromConfigFile covers the lowest non-default precedence
// layer: with no flag and no env, the stored config value wins.
func TestResolveFlyToken_FromConfigFile(t *testing.T) {
	writeConfigFile(t, "token.flyio=fo1_config_value\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok {
		t.Fatal("ResolveFlyToken: want ok=true, got false")
	}
	if got != "fo1_config_value" {
		t.Fatalf("ResolveFlyToken: want fo1_config_value, got %q", got)
	}
}

// TestResolveFlyToken_EnvBeatsConfig pins the middle precedence layer.
func TestResolveFlyToken_EnvBeatsConfig(t *testing.T) {
	writeConfigFile(t, "token.flyio=fo1_config_value\n")
	isolateEnv(t)
	t.Setenv("FLY_API_TOKEN", "fo1_env_value")

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok {
		t.Fatal("ResolveFlyToken: want ok=true, got false")
	}
	if got != "fo1_env_value" {
		t.Fatalf("env should beat config: want fo1_env_value, got %q", got)
	}
}

// TestResolveFlyToken_FlagBeatsEnvAndConfig pins the top precedence layer.
func TestResolveFlyToken_FlagBeatsEnvAndConfig(t *testing.T) {
	writeConfigFile(t, "token.flyio=fo1_config_value\n")
	isolateEnv(t)
	t.Setenv("FLY_API_TOKEN", "fo1_env_value")

	r, err := NewReader(map[string]string{"token.flyio": "fo1_flag_value"})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok {
		t.Fatal("ResolveFlyToken: want ok=true, got false")
	}
	if got != "fo1_flag_value" {
		t.Fatalf("flag should win: want fo1_flag_value, got %q", got)
	}
}

// TestResolveFlyToken_Unset_ReturnsFalse covers the "callers produce the
// 'run: aa config token.flyio=<token>' error" boundary: the resolver itself
// must signal absence with ok=false so callers can format that error.
func TestResolveFlyToken_Unset_ReturnsFalse(t *testing.T) {
	writeConfigFile(t, "") // file not created
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if ok {
		t.Fatalf("ResolveFlyToken: want ok=false on unset, got ok=true (val=%q)", got)
	}
	if got != "" {
		t.Fatalf("ResolveFlyToken: want empty string on unset, got %q", got)
	}
}

// TestResolveAPIBase_Default covers the built-in default from ADR amendment.
func TestResolveAPIBase_Default(t *testing.T) {
	writeConfigFile(t, "")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got := r.ResolveAPIBase()
	if got != "https://api.machines.dev/v1" {
		t.Fatalf("ResolveAPIBase default: want https://api.machines.dev/v1, got %q", got)
	}
}

// TestResolveAPIBase_ConfigBeatsDefault pins the file layer for endpoints.api.
func TestResolveAPIBase_ConfigBeatsDefault(t *testing.T) {
	writeConfigFile(t, "endpoints.api=https://api.staging.fly.io/v1\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveAPIBase(); got != "https://api.staging.fly.io/v1" {
		t.Fatalf("ResolveAPIBase: want staging URL from config, got %q", got)
	}
}

// TestResolveAPIBase_EnvBeatsConfig pins FLY_API_BASE as the env override.
func TestResolveAPIBase_EnvBeatsConfig(t *testing.T) {
	writeConfigFile(t, "endpoints.api=https://api.staging.fly.io/v1\n")
	isolateEnv(t)
	t.Setenv("FLY_API_BASE", "https://api.env.fly.io/v1")

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveAPIBase(); got != "https://api.env.fly.io/v1" {
		t.Fatalf("ResolveAPIBase: env should beat config, got %q", got)
	}
}

// TestResolveAPIBase_FlagBeatsAll pins the per-command flag as highest.
func TestResolveAPIBase_FlagBeatsAll(t *testing.T) {
	writeConfigFile(t, "endpoints.api=https://api.staging.fly.io/v1\n")
	isolateEnv(t)
	t.Setenv("FLY_API_BASE", "https://api.env.fly.io/v1")

	r, err := NewReader(map[string]string{"endpoints.api": "https://api.flag.fly.io/v1"})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveAPIBase(); got != "https://api.flag.fly.io/v1" {
		t.Fatalf("ResolveAPIBase: flag should win, got %q", got)
	}
}

// TestResolveRegistryBase_Default covers the built-in default.
func TestResolveRegistryBase_Default(t *testing.T) {
	writeConfigFile(t, "")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveRegistryBase(); got != "registry.fly.io" {
		t.Fatalf("ResolveRegistryBase default: want registry.fly.io, got %q", got)
	}
}

// TestResolveRegistryBase_ConfigBeatsDefault covers endpoints.registry.
func TestResolveRegistryBase_ConfigBeatsDefault(t *testing.T) {
	writeConfigFile(t, "endpoints.registry=registry.staging.fly.io\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveRegistryBase(); got != "registry.staging.fly.io" {
		t.Fatalf("ResolveRegistryBase: want config value, got %q", got)
	}
}

// TestResolveRegistryBase_EnvBeatsConfig pins AA_REGISTRY_BASE as env override.
func TestResolveRegistryBase_EnvBeatsConfig(t *testing.T) {
	writeConfigFile(t, "endpoints.registry=registry.staging.fly.io\n")
	isolateEnv(t)
	t.Setenv("AA_REGISTRY_BASE", "registry.env.fly.io")

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveRegistryBase(); got != "registry.env.fly.io" {
		t.Fatalf("ResolveRegistryBase: env should beat config, got %q", got)
	}
}

// TestResolveRegistryBase_FlagBeatsAll pins the flag layer.
func TestResolveRegistryBase_FlagBeatsAll(t *testing.T) {
	writeConfigFile(t, "endpoints.registry=registry.staging.fly.io\n")
	isolateEnv(t)
	t.Setenv("AA_REGISTRY_BASE", "registry.env.fly.io")

	r, err := NewReader(map[string]string{"endpoints.registry": "registry.flag.fly.io"})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveRegistryBase(); got != "registry.flag.fly.io" {
		t.Fatalf("ResolveRegistryBase: flag should win, got %q", got)
	}
}

// TestResolveDefaultApp_Default covers the built-in default aa-apps.
func TestResolveDefaultApp_Default(t *testing.T) {
	writeConfigFile(t, "")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultApp(); got != "aa-apps" {
		t.Fatalf("ResolveDefaultApp default: want aa-apps, got %q", got)
	}
}

// TestResolveDefaultApp_ConfigBeatsDefault covers defaults.app from config.
func TestResolveDefaultApp_ConfigBeatsDefault(t *testing.T) {
	writeConfigFile(t, "defaults.app=my-team-app\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultApp(); got != "my-team-app" {
		t.Fatalf("ResolveDefaultApp: want my-team-app, got %q", got)
	}
}

// TestResolveDefaultApp_FlagBeatsConfig pins the flag layer for defaults.app.
func TestResolveDefaultApp_FlagBeatsConfig(t *testing.T) {
	writeConfigFile(t, "defaults.app=my-team-app\n")
	isolateEnv(t)

	r, err := NewReader(map[string]string{"defaults.app": "override-app"})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultApp(); got != "override-app" {
		t.Fatalf("ResolveDefaultApp: flag should win, got %q", got)
	}
}

// TestResolveDefaultImage_Default pins the built-in default ubuntu:22.04.
func TestResolveDefaultImage_Default(t *testing.T) {
	writeConfigFile(t, "")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultImage(); got != "ubuntu:22.04" {
		t.Fatalf("ResolveDefaultImage default: want ubuntu:22.04, got %q", got)
	}
}

// TestResolveDefaultImage_ConfigBeatsDefault covers defaults.image from config.
func TestResolveDefaultImage_ConfigBeatsDefault(t *testing.T) {
	writeConfigFile(t, "defaults.image=debian:12\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultImage(); got != "debian:12" {
		t.Fatalf("ResolveDefaultImage: want debian:12, got %q", got)
	}
}

// TestResolveDefaultImage_FlagBeatsConfig pins the flag layer.
func TestResolveDefaultImage_FlagBeatsConfig(t *testing.T) {
	writeConfigFile(t, "defaults.image=debian:12\n")
	isolateEnv(t)

	r, err := NewReader(map[string]string{"defaults.image": "alpine:3.19"})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := r.ResolveDefaultImage(); got != "alpine:3.19" {
		t.Fatalf("ResolveDefaultImage: flag should win, got %q", got)
	}
}

// TestNewReader_IgnoresCommentsAndBlankLines covers the file-format forgiveness
// documented in the failure-mode table: comment lines (# prefix) and blanks
// are skipped; surrounding valid lines are returned.
func TestNewReader_IgnoresCommentsAndBlankLines(t *testing.T) {
	writeConfigFile(t, "# this is a comment\n\ntoken.flyio=fo1_after_comment\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok || got != "fo1_after_comment" {
		t.Fatalf("ResolveFlyToken with comments/blanks in file: want fo1_after_comment,true; got %q,%v", got, ok)
	}
}

// TestNewReader_ValueWithEqualsSign covers the strings.Cut-on-first-= rule:
// a value containing `=` is preserved literally, per the failure-mode table.
func TestNewReader_ValueWithEqualsSign(t *testing.T) {
	writeConfigFile(t, "token.flyio=fo1=abc=def\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok {
		t.Fatal("ResolveFlyToken: want ok=true")
	}
	if got != "fo1=abc=def" {
		t.Fatalf("ResolveFlyToken: want fo1=abc=def literal, got %q", got)
	}
}

// TestNewReader_MalformedLineSkipped covers the failure-mode row
// "Config file malformed line (no =) — skip silently".
func TestNewReader_MalformedLineSkipped(t *testing.T) {
	writeConfigFile(t, "not_a_kv_line\ntoken.flyio=fo1_valid\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: want nil err on malformed line, got %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok || got != "fo1_valid" {
		t.Fatalf("malformed line should be skipped: got (%q, %v)", got, ok)
	}
}

// TestResolveFlyToken_EmptyStringInConfigTreatedAsUnset covers the "first
// non-empty source wins" precedence rule for the file layer — an empty value
// in config must not count as "set".
func TestResolveFlyToken_EmptyStringInConfigTreatedAsUnset(t *testing.T) {
	writeConfigFile(t, "token.flyio=\n")
	isolateEnv(t)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if ok {
		t.Fatalf("empty-value config key should be unset: got (%q, true)", got)
	}
}

// TestResolveFlyToken_EmptyFlagFallsThroughToConfig covers the precedence
// contract that the first NON-EMPTY source wins: an empty flag value must not
// mask a real config value.
func TestResolveFlyToken_EmptyFlagFallsThroughToConfig(t *testing.T) {
	writeConfigFile(t, "token.flyio=fo1_from_config\n")
	isolateEnv(t)

	r, err := NewReader(map[string]string{"token.flyio": ""})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	got, ok := r.ResolveFlyToken()
	if !ok || got != "fo1_from_config" {
		t.Fatalf("empty flag should fall through to config: got (%q, %v)", got, ok)
	}
}

// FuzzNewReader pins the invariant that parsing arbitrary file bytes never
// panics and never returns a nil reader when err is nil. The file format is
// documented as forgiving; this fuzzes that promise.
func FuzzNewReader(f *testing.F) {
	f.Add("")
	f.Add("token.flyio=fo1_abc\n")
	f.Add("# comment only\n")
	f.Add("\x00\x01malformed=\n=\n==\n")
	f.Add(strings.Repeat("k=v\n", 10))
	f.Fuzz(func(t *testing.T, contents string) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		isolateEnv(t)
		dir := filepath.Join(home, ".config", "aa")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config"), []byte(contents), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		r, err := NewReader(nil)
		if err == nil && r == nil {
			t.Fatal("NewReader returned (nil, nil)")
		}
	})
}
