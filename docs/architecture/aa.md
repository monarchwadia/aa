# Architecture — aa

Load-bearing decisions that shape future code. Each entry: context, options, decision, consequences. Decisions stay `proposed` until the code that embodies them is written; `implement` will flip them to `accepted` or revise them.

---

## Decision 1: Backend as a narrow interface

**Date:** 2026-04-23
**Status:** proposed

### Context
aa ships two v1 backends (`local` Docker, `fly` Firecracker VMs) and will add more over time. The seam between session logic and backend must be stable enough that new providers don't churn session logic.

### Options considered
1. **Rich interface with many capability methods** (reserve IP, install DNS, configure networking, etc.). Rejected — premature; v1 needs very little of this.
2. **Narrow interface sized to what v1 actually needs.** Accepted.
3. **Plugin system with dynamic loading.** Rejected — YAGNI, zero-module-dep constraint, and the `script` backend (v2) already covers user-provided extension.

### Decision
Backend is a small Go interface covering exactly what v1 uses:

```go
type Backend interface {
    Provision(ctx context.Context, session SessionID) (Host, error)
    InstallEgress(ctx context.Context, host Host, allowlist []string) error
    RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error)
    ReadRemoteFile(ctx context.Context, host Host, path string) ([]byte, error)
    StreamLogs(ctx context.Context, host Host, path string, w io.Writer) error
    Teardown(ctx context.Context, host Host) error
}
```

### Consequences
- Each backend is a ~200–300 LOC Go file in `cmd/aa/backend_<name>.go`.
- Adding a cloud provider = one new file implementing this interface + config wiring.
- If a future backend needs a capability not in this surface, we widen deliberately, not incidentally.

---

## Decision 2: Session state is a merge function, not a stored enum

**Date:** 2026-04-23
**Status:** proposed

### Context
The displayed session state (`PROVISIONING`, `RUNNING`, `DONE`, `FAILED`, `LIMBO`, `INCONSISTENT`, `PUSHED`, `TORN_DOWN`) is derived from three inputs:
1. Laptop's session record (`~/.aa/sessions/<id>.json`) — operational state like "has this been pushed?" that the agent host doesn't know.
2. Remote state file (`/workspace/.aa/state`) — what the agent wrote before exiting, if anything.
3. Container's current status + exit code — observed by querying the backend.

Storing a canonical "state" in one place is a foot-gun: the other two sources of truth drift past it.

### Options considered
1. **Canonical state on the laptop**, updated on every operation. Rejected: crash windows between state updates and actual operations; agent host is source of truth for agent-side status.
2. **Canonical state on the agent host.** Rejected: laptop has operational state the host doesn't know (pushed, torn down).
3. **Compute on demand from the three sources.** Accepted.

### Decision
Implement a pure function:

```go
func ComputeSessionState(
    record LocalSessionRecord,
    remote RemoteStatus,  // includes remote state-file contents + exit code
) SessionState
```

Callers read the three inputs fresh and call this function. No state is stored centrally.

### Consequences
- The state-transition logic is one readable function, trivially table-tested with every combination of inputs.
- Display is always derived from fresh reads — no stale cache.
- Cost: SSH reads on every `aa` / `aa status`. Mitigated by SSH `ControlMaster` (single reused connection per session).
- The function is the canonical reference for the state machine — anything in docs or README describing states should map 1:1 to its test table.

---

## Decision 3: Ship an in-process Go proxy, not external `tinyproxy`

**Date:** 2026-04-23
**Status:** proposed

### Context
Egress enforcement has two components on the agent host: kernel `iptables` rules (drop all container outbound except to the proxy) and a forward HTTP CONNECT proxy that enforces the hostname allowlist. The proxy has to run as a separate process on the host (not inside the container).

### Options considered
1. **Install `tinyproxy`** via the host's package manager during egress install. Rejected:
   - Requires the host to have internet access to fetch packages — awkward ordering alongside egress lockdown.
   - Package availability varies across distros and base images.
   - Attack surface we didn't write and can't audit alongside the rest of aa.
2. **Embed a small forward proxy in the aa source tree**, compile to Linux binaries at release time, scp the right binary to the host at egress-install. Accepted.
3. **Run the proxy inside the aa CLI itself** on the laptop. Rejected: the kernel rules live on the agent host; a laptop-side proxy solves the wrong problem.

### Decision
Write a ~150-line HTTP CONNECT proxy in `cmd/aa-proxy/` that reads an allowlist from argv or a file, accepts CONNECT for allowlisted hostnames, rejects everything else with 403. Cross-compile for `linux/amd64` and `linux/arm64` alongside the main aa binary. At egress-install time, aa scp's the matching binary to the host and starts it under a simple supervisor (systemd user unit or nohup'd process with a PID file).

### Consequences
- No package-manager dependency on the host; works with any minimal Linux base image.
- Proxy source is in the same repo, audited as part of the same review surface.
- Cross-compile matrix expands if we add architectures later (`linux/arm`, `linux/riscv64`). Low cost, Go handles it.
- Attack surface is small, known, and tested alongside the rest of the code.

---

## Decision 4: `git push` is the only irreversible operation

**Date:** 2026-04-23
**Status:** proposed

### Context
aa performs many destructive-looking operations: provision VM, install iptables rules, create ephemeral API key, start container, apply patch to local clone, push to origin, teardown VM, revoke key. Most are reversible or idempotent. One is not: `git push`.

Stating this explicitly rules out several tempting anti-patterns.

### Options considered
N/A — this is a documenting decision, not a design choice.

### Decision
- Every operation except `git push` is designed to be idempotent and recoverable via `aa kill` or re-running the verb.
- `git push` sits behind the explicit user confirmation gate inside `aa push` (rule-violation display + final `[a] accept` prompt).
- No automation short-circuits that gate — no `--force`, no config flag, no env var.
- The local clone used for `git am` is kept under `~/.aa/sessions/<id>/clone/` until the session is torn down, so if the push itself fails (auth, remote rejected), the user can inspect and retry without losing work.

### Consequences
- Reinforces the intent non-goal: "auto-merge / auto-push is out of scope."
- Crash recovery has a simple invariant: if local session state exists but remote compute is gone, nothing landed in origin. Worst case is wasted ephemeral compute.
- Any future feature that wants to "just push" must confront this decision first.

---

## Decisions deferred

Intentionally not recorded here; will be made during `implement` or later `code-write` runs:

- Exact `SessionID` format (slug rules, branch name escaping for `feature/foo`).
- Error-wrapping conventions (stdlib `errors.Is/As`, sentinel errors for control flow).
- Exact package split if `cmd/aa/` grows past ~3000 lines.
- Ephemeral-key provider interface beyond Anthropic (second concrete provider drives the shape).
- Which systemd unit vs nohup supervisor form for the proxy process on the host.

---

## Workstreams

How the `e2e-tests`, `integration-unit-tests`, and `implement` steps will be parallelized to minimize merge conflicts. Each workstream owns its listed files exclusively; consumes only the contract files or the interfaces they declare; produces the types/functions named in the `Produces` field. Fakes named here are developed against when a real collaborator isn't ready.

### Contract files (wave 0 — single author, written first, locked)

Before any workstream starts, these files exist with final type signatures and doc comments. Bodies may be `panic("unimplemented")` stubs, but the shapes are frozen.

- `cmd/aa/backend.go` — `Backend` interface, `Host`, `ContainerSpec`, `ContainerHandle` types.
- `cmd/aa/state.go` — `SessionState` (enum), `LocalSessionRecord`, `RemoteStatus` types. `ComputeSessionState` signature only.
- `cmd/aa/config.go` — `Config`, `RepoConfig`, `AgentConfig`, `BackendConfig`, `Rule` types.
- `cmd/aa/keys.go` — `EphemeralKeyProvider` interface, `KeyHandle` type.
- `cmd/aa/rules.go` — `RuleViolation` type, `Evaluate` function signature.
- `cmd/aa/patch.go` — `Patch`, `ChangedFile` types, `ParsePatch` signature.
- `cmd/aa/ssh.go` — `SSHRunner` interface, `SSHResult` type.
- `cmd/aa/sessions.go` — `SessionStore` interface.
- `cmd/aa/testfakes.go` — shared fakes for the above interfaces (used by every workstream's tests).

### Wave 1 (fully parallel — pure code, no cross-workstream dependencies)

- **`config-loader`**
  - **Owns:** `cmd/aa/config_loader.go`
  - **Consumes:** types from `config.go`
  - **Produces:** `LoadGlobal`, `LoadRepo`, `Merge`, `ResolveSecretRefs` functions
  - **Fakes needed:** none (pure I/O + JSON)
  - **Tests:** `cmd/aa/config_loader_test.go` (unit), `cmd/aa/config_loader_integration_test.go` (integration against real JSON files in `testdata/`)

- **`patch-parser`**
  - **Owns:** `cmd/aa/patch_parser.go`
  - **Consumes:** types from `patch.go`
  - **Produces:** `ParsePatch(r io.Reader) (Patch, error)`
  - **Fakes needed:** none (pure)
  - **Tests:** `cmd/aa/patch_parser_test.go` + fuzz harness on patch bytes

- **`rules-engine`**
  - **Owns:** `cmd/aa/rules_engine.go`
  - **Consumes:** types from `rules.go`, `patch.go`
  - **Produces:** `Evaluate(rules []Rule, patch Patch) []RuleViolation`
  - **Fakes needed:** none (pure)
  - **Tests:** `cmd/aa/rules_engine_test.go` + fuzz harness

- **`state-compute`**
  - **Owns:** `cmd/aa/state_compute.go`
  - **Consumes:** types from `state.go`
  - **Produces:** `ComputeSessionState(...)` body
  - **Fakes needed:** none (pure)
  - **Tests:** `cmd/aa/state_compute_test.go` — table-driven over every input combination

- **`proxy-binary`**
  - **Owns:** `cmd/aa-proxy/main.go`, `cmd/aa-proxy/proxy.go`
  - **Consumes:** nothing from the main binary
  - **Produces:** standalone forward-proxy binary cross-compiled for `linux/{amd64,arm64}`
  - **Fakes needed:** none
  - **Tests:** `cmd/aa-proxy/proxy_test.go` — real sockets, localhost origin/destination

- **`ephemeral-key-anthropic`**
  - **Owns:** `cmd/aa/keys_anthropic.go`
  - **Consumes:** `EphemeralKeyProvider` interface from `keys.go`
  - **Produces:** `AnthropicKeyProvider` implementing the interface
  - **Fakes needed:** HTTP test server fake (in `testfakes.go`) for Anthropic Admin API responses
  - **Tests:** `cmd/aa/keys_anthropic_test.go`

- **`session-store`**
  - **Owns:** `cmd/aa/sessions_file.go`
  - **Consumes:** `SessionStore` interface
  - **Produces:** `FileSessionStore` implementing the interface (reads/writes `~/.aa/sessions/<id>.json`)
  - **Fakes needed:** temp-dir test helper
  - **Tests:** `cmd/aa/sessions_file_test.go`

### Wave 2 (depends only on wave 1 + contract files; workstreams in wave 2 are independent of each other)

- **`ssh-runner`**
  - **Owns:** `cmd/aa/ssh_runner.go`
  - **Consumes:** `SSHRunner` interface from `ssh.go`
  - **Produces:** `RealSSHRunner` (shells out to `ssh` with ControlMaster configured)
  - **Fakes needed:** fake `SSHRunner` already provided in `testfakes.go`
  - **Tests:** `cmd/aa/ssh_runner_test.go` — uses a local `sshd` in container for integration coverage; unit tests use the fake

- **`backend-local`**
  - **Owns:** `cmd/aa/backend_local.go`
  - **Consumes:** `Backend` interface from `backend.go`; shells out to `docker`
  - **Produces:** `LocalBackend` implementing `Backend`
  - **Fakes needed:** fake `Backend` in `testfakes.go`
  - **Tests:** `cmd/aa/backend_local_test.go` — gated on Docker availability

- **`egress-controller`**
  - **Owns:** `cmd/aa/egress.go`
  - **Consumes:** `SSHRunner`, `Host`
  - **Produces:** `InstallEgress(host, allowlist, proxyBinary)`, `RemoveEgress(host)`
  - **Fakes needed:** fake `SSHRunner`
  - **Tests:** `cmd/aa/egress_test.go` — asserts the right iptables/scp/exec calls are made via the fake

### Wave 3 (depends on wave 2)

- **`backend-fly`**
  - **Owns:** `cmd/aa/backend_fly.go`
  - **Consumes:** `Backend` interface; shells out to `flyctl`; uses `ssh-runner` for in-VM operations
  - **Produces:** `FlyBackend` implementing `Backend`
  - **Fakes needed:** fake `flyctl` helper in `testfakes.go`; reuses fake `SSHRunner`
  - **Tests:** `cmd/aa/backend_fly_test.go` — unit-level; full e2e gated on a real Fly account in CI

### Wave 4 (single writer; integrates everything)

- **`session-manager-and-cli`** — owner: one agent, at the end
  - **Owns:** `cmd/aa/session.go`, `cmd/aa/main.go`, `cmd/aa/verb_*.go` (one file per verb: `verb_attach.go`, `verb_status.go`, `verb_diff.go`, `verb_push.go`, `verb_kill.go`, `verb_retry.go`, `verb_list.go`, `verb_sweep.go`, `verb_init.go`, `verb_version.go`)
  - **Consumes:** every interface defined in the contract files; concrete implementations from waves 1–3
  - **Produces:** the `aa` binary entry point, verb dispatch, session orchestration
  - **Fakes needed:** fakes for every dependency (from `testfakes.go`) so this wave has independent unit-level tests
  - **Tests:** `cmd/aa/session_test.go` (unit: orchestration with fakes), `tests/e2e/**` (see below)

### E2E workstream (separate owner, runs alongside wave 4 or just after)

- **`e2e`** — owner: one agent
  - **Owns:** `tests/e2e/**` — journey-per-file, one per persona/goal
  - **Consumes:** the real built `aa` binary (no fakes at the e2e layer)
  - **Produces:** runnable e2e journeys
  - **Fakes needed:** none at the e2e layer; may spin up a local HTTP server as a stand-in for Anthropic API during egress tests

### Shared / single-writer files

| File | Single owner | Wave | Edit rule |
|---|---|---|---|
| `cmd/aa/main.go` | `session-manager-and-cli` | 4 | Only wave-4 owner edits. Contains `main()`, verb wiring, flag parsing. |
| `cmd/aa/testfakes.go` | wave-0 authors initially, then any workstream that needs a new fake | 0 → additive | Append-only. Each new fake is a new exported type/constructor; never edit existing fake signatures. |
| `go.mod`, `go.sum` | wave-0 author | 0 | Zero module dependencies goal means these should stay empty beyond `module` line. Any workstream that thinks it needs a dep must escalate — almost certainly wrong given intent. |
| `docs/architecture/aa.md` | any workstream that makes an architectural decision | any wave | Additive only; new ADRs are appended, existing ADRs' statuses can be edited by their author to flip `proposed` → `accepted`. |
| `README.md` | drift-fix only, not a workstream target | any | Edits only via the `document` skill when drift is detected. Workstreams don't edit docs to match their code. |

### Known unavoidable conflict points

None, given the layout above. Every functional file has exactly one workstream owning it. `main.go` is deferred to wave 4 so all collaborators are stable. `testfakes.go` is append-only by convention so concurrent extensions don't collide. If a real conflict appears during implementation, that's the signal to return here and revise.
