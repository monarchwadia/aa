package main

// Config is the fully-resolved, merged configuration for one aa invocation.
// Produced by config-loader from the global config + repo aa.json.
type Config struct {
	DefaultBackend string                   `json:"default_backend"`
	Backends       map[string]BackendConfig `json:"backends"`
	Agents         map[string]AgentConfig   `json:"agents"`
	Rules          []Rule                   `json:"rules"`
}

// RepoConfig is the parsed contents of a repo's `aa.json`.
// Per intent, the v1 fields are exactly `image` and `agent`; additional
// fields (per-repo env, allowlist extras) are proposed but not yet specified.
type RepoConfig struct {
	Image string `json:"image"`
	Agent string `json:"agent"`
}

// BackendConfig is one entry under `backends` in the global config.
type BackendConfig struct {
	// Type is one of "local", "fly", "process" in v1.
	Type string `json:"type"`

	// EgressEnforcement is "strict" or "none". Required. For the process
	// backend, the only valid value is "none".
	EgressEnforcement string `json:"egress_enforcement"`

	// Region is backend-specific (e.g. Fly region). Ignored by backends
	// that don't use it.
	Region string `json:"region,omitempty"`

	// Additional backend-specific fields are TBD per the "proposed" note
	// in the global config reference.
}

// AgentConfig is one entry under `agents` in the global config. The tuple
// (run, env, egress_allowlist) is the entire declaration of what an agent
// is from aa's perspective.
type AgentConfig struct {
	// Run is the shell command aa passes to `bash -lc` inside the sandbox.
	Run string `json:"run"`

	// Env is a map of name → secret-reference. Keys like
	// `ANTHROPIC_API_KEY` carry values like `keyring:anthropic` which are
	// resolved on the laptop at session start.
	Env map[string]string `json:"env"`

	// EgressAllowlist is the list of hostnames the agent is permitted to
	// reach. A single element ["*"] means unrestricted; see the Egress
	// section of the README.
	EgressAllowlist []string `json:"egress_allowlist"`

	// AdminAPIBaseURL overrides the default endpoint for the ephemeral-key
	// provider (Anthropic Admin API). Two legitimate uses:
	//   (1) enterprise/self-hosted Anthropic deployments,
	//   (2) tests that point it at a local httptest.Server.
	// Omit for the default public endpoint.
	AdminAPIBaseURL string `json:"admin_api_base_url,omitempty"`

	// EgressTestResolve is a map of hostname → IP that the forward proxy
	// uses instead of DNS for the listed hosts. A narrow override for
	// (a) routing test hostnames to a local httptest server, and
	// (b) enterprise environments with private DNS for allowlisted hosts.
	// Each entry documented here weakens the egress guarantee for that
	// one hostname; aa logs a warning at session start listing every
	// override in effect.
	EgressTestResolve map[string]string `json:"egress_test_resolve,omitempty"`
}

// Rule is one entry under `rules` in the global config. See README § Rules.
type Rule struct {
	// Type is the built-in rule type name, e.g. "gitHooksChanged",
	// "ciConfigChanged", or the generic "fileChanged".
	Type string `json:"type"`

	// Severity is "off", "warn", or "error".
	Severity string `json:"severity"`

	// Include is the list of glob patterns the rule matches against
	// changed-file paths. Required for "fileChanged"; ignored for built-in
	// types that carry their own globs.
	Include []string `json:"include,omitempty"`
}

// Severity levels, for comparison against Rule.Severity.
const (
	SeverityOff   = "off"
	SeverityWarn  = "warn"
	SeverityError = "error"
)
