package main

import (
	"context"
	"io"
)

// Backend is the abstraction over where an aa session's sandbox runs.
// Every v1 backend — `local` (Docker), `fly` (Firecracker), `process` (laptop
// child process) — implements this interface.
//
// Lifecycle from SessionManager's perspective:
//
//	Provision          →  acquire a Host
//	InstallEgress      →  install host firewall + proxy (if enforcing)
//	RunContainer       →  start the sandbox, exec the agent
//	ReadRemoteFile /   →  interact with the running session
//	StreamLogs
//	Teardown           →  destroy the sandbox and Host
//
// Every method is idempotent where reasonable, so `aa kill` from any point
// in the lifecycle can safely reverse partial progress.
//
// See docs/architecture/aa.md § "Decision 1" for the rationale behind the
// shape of this interface.
type Backend interface {
	// Provision acquires a Host the session can run on. For ephemeral
	// backends (fly) this may spin up a fresh VM; for local/process it is
	// a near-no-op.
	Provision(ctx context.Context, id SessionID) (Host, error)

	// InstallEgress installs the host-kernel firewall rules and starts the
	// forward proxy. After this call returns, no traffic can leave the
	// future container except to allowlisted hostnames via the proxy.
	//
	// For backends that set egress_enforcement="none" (e.g. `process`),
	// this is a documented no-op.
	InstallEgress(ctx context.Context, host Host, allowlist []string) error

	// RunContainer starts the sandbox (docker container / Fly machine /
	// detached child process) with the agent's run command. aa injects
	// AA_WORKSPACE and AA_SESSION_ID into the environment before exec.
	RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error)

	// ReadRemoteFile reads a single file from inside the sandbox, by its
	// path relative to $AA_WORKSPACE. Used by `aa diff`, `aa status`, and
	// the push flow to pull state and patches onto the laptop.
	ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error)

	// StreamLogs tails a file inside the sandbox, writing bytes to w as
	// they become available, until the context cancels.
	StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error

	// Teardown destroys the sandbox and, for ephemeral backends, the
	// provisioned Host. Safe to call repeatedly; safe to call after a
	// partial Provision failure.
	Teardown(ctx context.Context, host Host) error
}

// SessionID uniquely identifies an aa session on the laptop. Format is
// `<repo-slug>-<branch-slug>` with path-unsafe characters escaped.
// Exact escaping rules are TBD in `implement`.
type SessionID string

// Host is a concrete backend-provisioned address plus the metadata the
// session manager needs to interact with it.
type Host struct {
	// Address is the backend-specific connection string. For SSH-based
	// backends it's `user@host:port`. For local/process it's empty.
	Address string

	// BackendType is one of "local", "fly", "process". Used for diagnostics
	// and display; dispatch happens through the Backend interface.
	BackendType string

	// Workspace is the absolute path that AA_WORKSPACE should point at
	// inside the sandbox. Container backends typically use "/workspace";
	// the process backend uses a laptop path under ~/.aa/workspaces/<id>.
	Workspace string
}

// ContainerSpec is everything RunContainer needs to exec the agent.
type ContainerSpec struct {
	// Image is the Dockerfile path or base image to run. Ignored by the
	// process backend.
	Image string

	// AgentRun is the shell command the agent was configured with in
	// ~/.aa/config.json. Passed verbatim to `bash -lc`.
	AgentRun string

	// Env contains the agent's environment variables, already resolved from
	// keyring references on the laptop. aa prepends AA_WORKSPACE and
	// AA_SESSION_ID before exec.
	Env map[string]string

	// SessionID is injected into the agent's environment as AA_SESSION_ID.
	SessionID SessionID
}

// ContainerHandle is returned by RunContainer and used by later method calls
// to identify the running sandbox.
type ContainerHandle struct {
	ID   string
	Host Host
}
