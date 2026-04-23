package main

// This file is a wave-0 contract stub for the `config-loader` workstream.
// The real bodies are produced by wave-1 `implement`. Until then, every
// exported function here panics so that tests written against these
// signatures compile and fail loudly at runtime — the red-tests step of
// the code-write workflow.
//
// Do not put logic here. The stubs exist only so the package compiles
// and the test files can reference these identifiers.

// SecretResolver resolves a single secret reference (e.g. "keyring:foo")
// to its concrete value. Pluggable so tests can supply an in-memory
// resolver without touching a real OS keyring.
type SecretResolver func(ref string) (string, error)

// LoadGlobal reads and parses `<homeDir>/.aa/config.json`.
func LoadGlobal(homeDir string) (Config, error) {
	panic("unimplemented — config-loader workstream, wave 1")
}

// LoadRepo reads and parses `<repoDir>/aa.json`.
func LoadRepo(repoDir string) (RepoConfig, error) {
	panic("unimplemented — config-loader workstream, wave 1")
}

// Merge combines a parsed global Config with a parsed repo RepoConfig,
// validates cross-field invariants (default_backend must exist in
// backends; the repo's agent must exist in agents; process backend
// cannot enforce egress), and returns the resolved Config the session
// manager uses.
func Merge(global Config, repo RepoConfig) (Config, error) {
	panic("unimplemented — config-loader workstream, wave 1")
}

// ResolveSecretRefs walks every AgentConfig.Env entry and replaces any
// value beginning with "keyring:" by calling the resolver with the full
// reference string. Non-"keyring:" values pass through unchanged.
func ResolveSecretRefs(cfg Config, resolver SecretResolver) (Config, error) {
	panic("unimplemented — config-loader workstream, wave 1")
}
