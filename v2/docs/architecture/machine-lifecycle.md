# Machine lifecycle — Architecture Notes

**Slug:** `machine-lifecycle`
**Status:** proposed
**Date:** 2026-04-23
**Scope:** how the `aa machine <verb>` command surface is built — seams, data flow, failure, state, tests, ADRs, and the workstream plan later steps schedule against.

Read alongside:
- Intent: [`../intent/machine-lifecycle.md`](../intent/machine-lifecycle.md)
- User-facing doc: [`../machine-lifecycle.md`](../machine-lifecycle.md)
- Philosophy: `/workspaces/aa/v1/docs/PHILOSOPHY.md`
- Related slugs: `config-store`, `test-harness`, `docker-up`

---

## Executive summary

Today's `v2/main.go` + `v2/machines.go` already hold working bodies for spawn / ls / start / stop / rm. What is missing is:

1. **Dispatcher migration.** Flat `aa spawn` / `aa ls` / ... are retired in favour of `aa machine <verb>`. The handler bodies stay; the routing layer is replaced.
2. **Seam extraction.** Three inline concerns — HTTP to Fly, config reads, and shelling out to `flyctl` — must become injectable interfaces so tests can substitute fakes. Inline `http.DefaultClient.Do` and direct `readConfig()` calls are the current blockers for deterministic testing.
3. **A sixth verb, `attach`,** which today is baked into `spawn` only (via `attachSSH`). It must exist as a standalone verb.
4. **Explicit state-machine handling** of the backend-ready → shell-reachable gap, with named states and a pinned retry policy.

No new cloud features. No fleet / region logic. No persistence. YAGNI applies hard.

---

## Seams & boundaries

Three external contracts, one internal contract. All four become files in wave 1.

### S1. Fly HTTP client interface — `v2/internal/flyapi/client.go` (contract)

The handlers stop talking to `net/http` directly. They talk to a narrow interface that models only the five verbs we actually use against the Fly Machines API.

```go
// NOT code — signatures pinned in the contract file.

type Client interface {
    EnsureApp(ctx context.Context, app string) error
    CreateMachine(ctx context.Context, app string, spec CreateSpec) (Machine, error)
    GetMachine(ctx context.Context, app, id string) (Machine, error)
    ListMachines(ctx context.Context, app string) ([]Machine, error)
    StartMachine(ctx context.Context, app, id string) error
    StopMachine(ctx context.Context, app, id string) error
    DestroyMachine(ctx context.Context, app, id string, force bool) error
}

type Machine struct { ID, State, Region string }
type CreateSpec struct { Image, Region string }
```

- Constructor: `New(baseURL, token string, httpDoer Doer) Client` where `Doer` is a one-method `Do(*http.Request) (*http.Response, error)`. Base URL is read once at startup from `FLY_API_BASE` (default `https://api.machines.dev/v1`) by the wiring layer, not by the client itself — keeps the client a pure I/O translator.
- No retries inside the client. Retry lives in the state machine (see S4).
- `ctx` is threaded through every call. No `context.Background()` in handlers (philosophy anti-pattern).
- Errors wrap HTTP status + body; callers pattern-match on sentinel types (`ErrNotFound`, `ErrConflict`) rather than string-sniffing.

**What must not leak:** `*http.Response`, `json.RawMessage`, Fly-specific struct tags. Handlers never touch JSON.

### S2. Config reader interface — `v2/internal/config/reader.go` (contract, consumed from `config-store` slug)

The `config-store` slug owns the file path and shape. Machine-lifecycle consumes only this:

```go
type Reader interface {
    Get(key string) (string, bool)    // e.g. "token.flyio", "defaults.app", "defaults.image"
}
```

- No `Set`, no `List`, no file path, no parsing. A single read method. This is the minimum philosophy-axis-2 ("thin seams") allows.
- Wiring layer constructs the concrete reader from the `config-store` package and passes it in. Handlers never call `os.UserConfigDir` or read files.
- Resolution precedence (flag → env → config → built-in) lives in the handler, not the reader. The reader is dumb.

**Dependency note:** the exported type might physically live in the `config-store` slug's package; machine-lifecycle imports it. If the signature above is wrong, machine-lifecycle's workstreams block on wave 1 until it's pinned.

### S3. External-binary runner interface — `v2/internal/extbin/runner.go` (contract)

`attach` and `spawn`'s tail shell out to `flyctl`. That call becomes:

```go
type Runner interface {
    Run(ctx context.Context, name string, args []string, env []string, io StdIO) error
}

type StdIO struct { Stdin io.Reader; Stdout, Stderr io.Writer }
```

- Real implementation wraps `exec.Command`.
- Test implementation records calls, returns scripted exit codes, optionally feeds scripted stdout. Interactive TTY is not simulated — tests targeting `attach` do not exercise a real PTY.
- Preflight ("is `flyctl` on PATH?") is a separate method: `Lookup(name string) error`. Failure mode: a clear "install flyctl" message, not a raw `exec: "flyctl": executable file not found in $PATH`.

**Safety-at-boundary note:** strict mode does **not** apply here today. The only user-supplied string that reaches the `flyctl` command line is the machine ID, which was either returned by Fly (trusted) or typed by the user on their own laptop as an argv element (argv is safe; there is no shell). No shell interpolation happens. If that ever changes — e.g. if we start building a bash `-c` string — this file moves into strict mode and the Fly ID must be validated against `^[0-9a-f]{14,}$`.

### S4. Internal: spawn state machine — `v2/machine_spawn_state.go`

Not an interface, a pure-function concern worth naming as its own seam so tests can hit it without touching HTTP or exec.

```
  requested
      │  createMachine OK
      ▼
  creating ───fail──► failed (terminal)
      │  HTTP returns machine-ID
      ▼
  backend-pending
      │  getMachine.state == "started"      │ deadline passes
      ▼                                     ▼
  backend-ready                          timed-out-backend (terminal; instance MAY exist)
      │  flyctl ssh console exit 0
      ▼                                     │ maxAttempts exhausted
  shell-reachable (terminal; success)    timed-out-shell (terminal; instance exists)
```

`backend-ready` and `shell-reachable` are the two lines the doc promises. Everything between is retry noise.

Pure functions in this file:
- `NextBackendPollDelay(attempt int) time.Duration` — returns `2*time.Second` today (mirrors current code). Named so it's trivially replaceable.
- `NextShellAttemptDelay(attempt int) time.Duration` — returns `3*time.Second`.
- `ShellAttemptBudget() int` — returns `15` (see ADR-4).
- `BackendDeadline() time.Duration` — returns `90*time.Second` (mirrors current code).

No struct, no state variable. Just policy constants behind named functions so unit tests pin them.

---

## Data flow per verb

Each verb's stages, named so logs and tests can refer to them.

### `spawn`
1. **parse-flags** (`--token --app --image --region`)
2. **resolve-defaults** (flag → env → config → built-in). Emits one line naming the image + app actually chosen.
3. **ensure-app** — idempotent; GETs then POSTs if 404.
4. **create-machine** — POST, receive ID + region.
5. **wait-backend-ready** — poll `GET /machines/:id` until `state == "started"` or `BackendDeadline()` passes.
6. **wait-shell-reachable** — call runner with `flyctl ssh console ...` up to `ShellAttemptBudget()` times, sleeping `NextShellAttemptDelay()` between failures.
7. **attached** — runner owns the terminal until the remote shell exits; return.

### `ls`
1. parse-flags → 2. resolve-defaults → 3. `ListMachines` → 4. format table (tabwriter) → 5. emit `(no machines in "<app>")` if empty.

### `start` / `stop` — identical shape
1. parse-flags → 2. resolve-defaults → 3. require ≥1 positional ID → 4. per-ID `StartMachine` / `StopMachine` → 5. `verb <id> ok` line per success.

### `rm`
1. parse-flags incl. `--force` → 2. resolve-defaults → 3. require ≥1 ID → 4. per-ID `DestroyMachine(force)` → 5. `rm <id> ok`.

No confirmation prompt (ADR-7).

### `attach`
1. parse-flags → 2. resolve-defaults → 3. require exactly one ID → 4. `GetMachine` once to surface state-based errors (`stopped` → clear message, not a transport error) → 5. invoke runner → 6. return runner's exit code.

---

## Failure modes

| Stage | Failure | Behaviour |
|---|---|---|
| resolve-defaults | no token | exit 1 with `no Fly.io token found — run: aa config token.flyio=<token>` |
| ensure-app | HTTP ≠ 200 and ≠ 404 on GET | exit 1, name status + body |
| ensure-app | POST fails | exit 1, name status + body. **No instance exists yet.** |
| create-machine | HTTP ≥ 300 | exit 1. No instance exists. |
| wait-backend-ready | deadline elapsed | exit 1. **Instance may exist and be listed by `ls`.** Message names both facts. |
| wait-shell-reachable | attempts exhausted | exit 1. Instance exists and is running. `ls` / `attach` / `rm` can be used next. User is told this explicitly. (See ADR-5.) |
| attach | machine state `stopped` | exit 1 with `machine <id> is stopped — run: aa machine start <id>` |
| attach | runner exits non-zero after backend-ready | surface `flyctl`'s stderr as-is plus a one-line summary |
| any | `flyctl` not on PATH | exit 1 with install hint (from runner preflight) |
| any | context cancelled (SIGINT) | HTTP in flight is cancelled; partial-instance cleanup is **not** attempted (ADR-5) |

Errors follow the philosophy axis-3 rule: what-why-what-next. No raw transport dumps without a framing sentence.

---

## State and lifecycle

Covered above under S4 (spawn state machine). Two additional notes:

- **`start`, `stop`, `rm` are stateless with respect to each other.** Each is a single API call. No retry, no polling for terminal state confirmation. If the user wants to confirm, they run `ls`. (Philosophy axis 4: ceremony-free.)
- **Destroyed means gone.** No tombstones, no local "recently destroyed" tracking. YAGNI.

---

## Concurrency & ordering

- No goroutines in handlers. Every verb is linear.
- `start`/`stop`/`rm` iterate IDs sequentially. If one fails, the command exits with the current behaviour (matches existing `runLifecycle`). Parallelising is YAGNI — three IDs is not a performance problem.
- Idempotency: `start` on an already-started machine is a Fly-side no-op (documented); we pass this through. Same for `stop`.

---

## Testing surface

| Concern | Where it lives | How it's tested |
|---|---|---|
| Resolution precedence (flag → env → config → built-in) | handler pure helper | unit test; table-driven |
| Retry policy constants | `machine_spawn_state.go` | unit test; pins values so future changes are visible in diff |
| State-machine transitions | `machine_spawn_state.go` | unit test over a mock `flyapi.Client` + `extbin.Runner` |
| HTTP request shape (URL, method, body, headers) | `internal/flyapi/client.go` | integration test against `test-harness` snapshot server |
| `ls` table formatting | handler | unit test; golden string |
| Error message phrasing | handler | unit test; substring match on key diagnostic tokens |
| End-to-end `spawn` → `attach` → `rm` | across everything | e2e test, separate workstream, uses snapshot HTTP + fake `flyctl` runner |

Anything not in this table is either out of scope or not a test target (e.g. we do not test `tabwriter`, we do not test `exec.Command` itself).

---

## Assumptions being baked in (surfaced for confirmation)

1. `config-store` will ship a `Get(key string) (string, bool)` reader. If it ships something richer, machine-lifecycle only imports the subset.
2. `test-harness` will ship (a) a snapshot HTTP server that can be pointed at via `FLY_API_BASE`, (b) a fake external-binary runner. Both are wave 1.
3. `flyctl` remains the attach mechanism for the near term. If a native SSH path replaces it, the runner seam absorbs the change.
4. Only one `flyctl` subcommand is ever invoked: `ssh console --app <app> --machine <id>`. No other `flyctl` verbs are part of this slug.
5. No TTY simulation in integration tests. Interactive attach is validated by unit-asserting the runner was invoked with the right args; the actual terminal behaviour is a manual-smoke concern.

---

## ADRs

### ADR-1: Default base image — `ubuntu:22.04`

**Date:** 2026-04-23. **Status:** proposed.

**Context.** Current code hard-codes `ubuntu:22.04`. Intent allows the default to be anything, and lets users override via `--image` or `defaults.image`.

**Options considered.**
1. `ubuntu:22.04` — matches current code. LTS. Mature, familiar to solo devs. Large image.
2. `ubuntu:24.04` — newer LTS. Marginally more modern tooling.
3. `debian:12-slim` — smaller, faster boot. Less familiar default for "drop into a scratch box".
4. `alpine:3.19` — smallest, fastest. Musl surprises break many dev workflows (`go`, `glibc` binaries). Poor fit for "hit it with an LLM agent".

**Decision.** `ubuntu:22.04`.

**Philosophy walk.**
- Clarity (axis 1): `ubuntu:22.04` is the most recognisable name to a fresh LLM reader. Alpine/musl surprises cost clarity.
- Evolvability (axis 2): the value is a single string behind a config key. Changing it later is trivial. No lock-in.
- Observability (axis 3): neutral.
- Low ceremony (axis 4): neutral; all four options are equally cheap to wire.
- Safety (axis 5): neutral.

Axis 1 wins → Ubuntu family. Axis 2 says don't chase the newest LTS just because it exists (`24.04` is fine but contains zero new capability we need; changing it costs a churn diff). `22.04` it is.

**Consequences.** If a user reports a missing tool from `22.04`'s default layer, the fix is either (a) they override via `--image` or `defaults.image`, or (b) we bump the default in one line. No architectural change.

### ADR-2: Default app namespace — `aa-apps`

**Date:** 2026-04-23. **Status:** accepted (user-pinned).

**Context.** User has pinned `aa-apps` as the built-in fallback. Not re-derived.

**Decision.** Built-in fallback for `defaults.app` is the literal string `aa-apps`. Users may override via `--app` or `defaults.app` in the config store. `ensureApp` idempotently creates the namespace on first use.

**Consequences.** Any user running `aa machine spawn` with no config gets an `aa-apps` app created in their Fly org on first run. This is a side effect; it is announced with `App "aa-apps" not found, creating it...` (current behaviour), satisfying axis 3.

### ADR-3: Identifier form — backend-assigned ID only

**Date:** 2026-04-23. **Status:** proposed.

**Context.** Intent open question: ID, name, or both.

**Options considered.**
1. **Backend ID only.** 14-hex-char Fly machine ID. What `ls` prints today. What `start/stop/rm` takes today.
2. **Human name only.** User picks on `spawn --name foo`. Requires a name→ID index somewhere.
3. **Both.** Accept either; disambiguate.

**Philosophy walk.**
- Clarity: option 1 wins. One thing means one thing. Option 3 forces every handler and every error message to explain which form it parsed.
- Evolvability: option 1 wins. Adding names later is strictly additive — we can extend the `Reader` interface and add a `--name` flag when someone asks. Option 2/3 bakes a lookup layer in now.
- Observability: option 1 wins. The ID that appears on `spawn` is the same one you paste into `attach`, `rm`. No translation step.
- Low ceremony: option 1 wins hardest. No name storage, no uniqueness checks, no rename verb.
- Safety: neutral.

**Decision.** Option 1. User passes the backend-assigned ID to every verb that targets a specific machine. No names yet.

**Consequences.** If users start asking for memorable names, we add `--name` to `spawn` + a local `~/.aa/machines.json` index + a name→ID resolver step in handler argv parsing. That work is not in scope.

### ADR-4: Shell-ready timeout — 15 attempts × 3s = 45s

**Date:** 2026-04-23. **Status:** proposed (confirm current code).

**Context.** Current `attachSSH` loops 15 times with 3-second sleeps. Empirically bridges the typical Fly backend-ready → shell-reachable gap.

**Options considered.**
1. Keep `15 × 3s = 45s`.
2. Shorten to `10 × 3s = 30s` — risks premature failure on slow regions.
3. Longer (`20 × 3s = 60s`) — punishes the interactive user with a long wait on genuine failure.
4. Exponential backoff — over-engineered for a 45-second window.

**Philosophy walk.** Axis 4 (ceremony/YAGNI) kills backoff immediately. Axis 1 (clarity) says keep the simplest form. Axis 3 (observability) is satisfied either way since we print a line per attempt.

**Decision.** Keep `15 × 3s`. Extract as `ShellAttemptBudget() = 15` and `NextShellAttemptDelay(_) = 3s` in `machine_spawn_state.go`. Pinned by a unit test so a future one-line change to either value is visible in review.

**Consequences.** If real-world traces start showing `>45s` bridge times, bump one constant. No redesign.

### ADR-5: Partial-instance cleanup — retain, do not auto-destroy

**Date:** 2026-04-23. **Status:** proposed.

**Context.** Intent open question: when `spawn` fails between backend-ready and shell-reachable (or is Ctrl-C'd during that window), is the partial instance torn down?

**Options considered.**
1. **Retain** (current doc + current code behaviour). Instance exists, appears in `ls`, user decides.
2. **Auto-destroy** on shell-reachable timeout. User has nothing to clean up, but also nothing to debug.
3. **Prompt.** User-hostile in the unattended / agent case.

**Philosophy walk.**
- Clarity: option 1 wins; "I still have a thing" is easier to reason about than "I sometimes have a thing depending on which error".
- Evolvability: option 1 wins; adding `--cleanup-on-failure` later is trivial.
- Observability: option 1 wins massively; the failure is inspectable with `ls` + `attach` + logs from inside the VM. Option 2 destroys the evidence.
- Low ceremony: neutral.
- Safety: option 1 has the cost that a forgotten instance costs money. Acceptable — `ls` surfaces it, axis 3 picks it up.

**Decision.** Retain. On shell-ready timeout, print a line naming the ID, the state, and the next-step suggestion (`aa machine rm <id>` or `aa machine attach <id>` to retry).

**Consequences.** This is the contract the doc promises. It is now load-bearing — if we ever want to change it we update doc + ADR + tests together.

### ADR-6: `ls` scope — everything in the app namespace

**Date:** 2026-04-23. **Status:** proposed (confirm current code).

**Context.** Intent open question: show only `aa`-provisioned machines, or everything in the namespace.

**Options considered.**
1. **Everything in `--app`** (current code): one `GET /apps/:app/machines` call, no client-side filtering, no local tracking.
2. **Only `aa`-provisioned.** Requires a local registry or a tag on the machine config.

**Philosophy walk.**
- Clarity: option 1 wins; `ls` matches the user's mental model of "what's in my namespace".
- Evolvability: option 1 wins; no new state surface. Tagging can be added later (Fly machines support metadata) and a `--mine` filter layered on.
- Observability: option 1 is strictly more informative — an instance created by some other path is still visible.
- Low ceremony: option 1 wins hard.

**Decision.** `ls` shows every machine in the app the user is pointed at. Defaults to `aa-apps` (ADR-2).

**Consequences.** A user who runs `aa` with a non-default `--app` that also contains machines spawned by other tools will see all of them. That is the point.

### ADR-7: `rm` confirmation — never prompt; accept `--force`

**Date:** 2026-04-23. **Status:** proposed (confirm current code).

**Context.** Intent open question: prompt, force flag, or neither.

**Options considered.**
1. **Never prompt; `--force` destroys a running machine** (current behaviour).
2. Prompt on running machines.
3. Prompt always.

**Philosophy walk.**
- Clarity: option 1 wins; a prompt introduces a state ("am I at a TTY?") the handler has to branch on.
- Evolvability: option 1 wins; adding a prompt later is trivial.
- Observability: neutral.
- Low ceremony: option 1 wins hardest — agents driving the CLI cannot answer prompts.
- Safety: option 2/3 add trivially little safety. IDs are 14-character hex strings that cannot be typoed into the wrong machine in practice. `--force` gates the one truly destructive edge (running machine).

**Decision.** No prompt. `--force` flag on `rm` for running machines. Matches current code.

**Consequences.** If a user ever deletes the wrong machine, it's gone. Acceptable given the backup story ("destroyed means gone" is already an intent non-goal).

### ADR-8: Detached spawn — not supported in v1

**Date:** 2026-04-23. **Status:** proposed.

**Context.** Intent open question: support `--no-attach` / `--detach` on spawn.

**Options considered.**
1. Attach-on-success only (current behaviour).
2. `--detach` flag that stops after shell-reachable is confirmed.
3. `--detach` that stops after backend-ready.

**Philosophy walk.**
- Clarity: option 1 wins; one success path.
- Evolvability: option 1 wins; adding `--detach` later is additive and doesn't restructure the state machine.
- Observability: neutral.
- Low ceremony: option 1 wins.
- Safety: neutral.

**Decision.** No `--detach` in v1. `spawn` always attaches on success. Users who want a standalone machine can `spawn` + Ctrl-D out of the shell, or `spawn` in one terminal and `attach` from another (both instances of `attach` are cheap).

**Consequences.** If agents start needing background provisioning (likely), we add `--detach` returning after the shell-reachable line. The state machine already separates those stages so the flag is a one-line branch.

---

## Workstreams

### Contract files (wave 1, single-author, locked before any workstream starts)

These files are written **first** by a single author (the plan-implementation step). They contain only type declarations, interface declarations, and constructor signatures — no logic. Once merged, every downstream workstream codes against them.

1. `v2/internal/flyapi/client.go` — the `Client` interface, `Doer`, `Machine`, `CreateSpec`, sentinel errors, `New(...)` signature.
2. `v2/internal/config/reader.go` — the `Reader` interface. If `config-store` exports the same type, this file re-exports / aliases and becomes a single-line bridge. If `config-store` slug's types diverge, this file blocks until reconciled.
3. `v2/internal/extbin/runner.go` — the `Runner` interface, `StdIO`, `Lookup`.
4. `v2/machine_spawn_state.go` — pure policy-constant functions + named state enum. No behaviour.

### Wave 1 — foundations (parallel)

External to this slug but listed because we block on them:

- **config-store-read** (owned by config-store slug). **Produces:** `config.Reader` as pinned above. **Fakes:** `config.MapReader{map[string]string}` used everywhere.
- **test-harness-http** (owned by test-harness slug). **Produces:** snapshot-server binary, `httptest.Server` factory. **Consumed by:** flyapi-client integration tests (wave 2).
- **test-harness-extbin** (owned by test-harness slug). **Produces:** `extbin.FakeRunner` recording calls + returning scripted exits. **Consumed by:** spawn + attach integration tests (wave 2).

Within this slug:

- **W1a: contracts** — the four files listed above. **Owns:** those four files. **Consumes:** nothing. **Produces:** every interface downstream workstreams code against. **Fakes:** n/a (no bodies). **Tests:** none.

### Wave 2 — bodies (parallel once W1a is frozen)

- **W2a: fly-client**
  - **Owns:** `v2/internal/flyapi/client_impl.go`, `v2/internal/flyapi/errors.go`.
  - **Consumes:** `flyapi.Client` contract, `FLY_API_BASE`.
  - **Produces:** a concrete `Client` implementation.
  - **Fakes needed:** test-harness snapshot server.
  - **Tests:** `v2/internal/flyapi/client_integration_test.go` (against the harness server, hits every verb, asserts URL/method/body/headers), `v2/internal/flyapi/errors_test.go` (error-sentinel mapping).

- **W2b: extbin-runner**
  - **Owns:** `v2/internal/extbin/runner_impl.go`.
  - **Consumes:** `extbin.Runner` contract.
  - **Produces:** concrete `ExecRunner` wrapping `os/exec`.
  - **Fakes needed:** none (it *is* the real; tests use a tiny `/bin/true`-style script).
  - **Tests:** `v2/internal/extbin/runner_test.go` (argv passthrough, env merge, exit-code propagation).

- **W2c: spawn-state**
  - **Owns:** `v2/machine_spawn_state_test.go` (logic already pinned in the wave-1 file).
  - **Consumes:** nothing; pure functions.
  - **Produces:** tests that pin the retry/timeout constants.
  - **Fakes needed:** none.
  - **Tests:** `v2/machine_spawn_state_test.go` (table test asserting `ShellAttemptBudget() == 15`, `NextShellAttemptDelay(*) == 3s`, `BackendDeadline() == 90s`, `NextBackendPollDelay(*) == 2s`).

- **W2d: handlers**
  - **Owns:** `v2/machine.go` (single file — *all six verb handlers*, dispatcher, resolution helper). See file-layout note below.
  - **Consumes:** `flyapi.Client`, `config.Reader`, `extbin.Runner`, `machine_spawn_state.go`.
  - **Produces:** `RunMachine(args []string, deps Deps)` entry point — called by `main.go`.
  - **Fakes needed:** `flyapi.FakeClient` (in-memory map of machines; implements the interface); `config.MapReader`; `extbin.FakeRunner`.
  - **Tests:** `v2/machine_handlers_test.go` (per-verb unit tests; resolution precedence; error-message substrings; `ls` golden-table; spawn state-machine transitions using the three fakes).

File-layout note: philosophy says <400 LOC comfort, 800 ceiling. Current `main.go` + `machines.go` together are ~510 LOC, of which ~70 LOC is config code that moves to the `config-store` slug. After extraction, all six verb handlers + dispatcher fit comfortably in a single `v2/machine.go` (~350 LOC estimated). We do **not** split per-verb — the three-strike rule (axis 2) says the six handlers share enough structure (common flag registration, common resolution, common API-error surfacing) that six files would be premature fragmentation. If `v2/machine.go` passes 500 LOC in the implement step, split `spawn` + `attach` into `v2/machine_spawn.go` (they share the state machine) and leave ls/start/stop/rm in `v2/machine.go`.

### Wave 3 — integration (after wave 2 lands)

- **W3a: wiring**
  - **Owns:** `v2/main.go` (rewritten to `aa machine <verb>` dispatcher, flat aliases removed).
  - **Consumes:** every wave-2 deliverable plus `config-store`.
  - **Produces:** the binary.
  - **Fakes needed:** none.
  - **Tests:** none directly; smoke-tested via e2e.

- **W3b: e2e**
  - **Owns:** `v2/e2e/machine_lifecycle_test.go`.
  - **Consumes:** the built binary, snapshot harness, fake `flyctl`.
  - **Produces:** one end-to-end journey per persona in intent doc (solo dev, agent, returning-user cleanup).
  - **Fakes needed:** all of the above.
  - **Tests:** the file itself.

### Shared / single-writer files

| File | Single owner | Wave |
|---|---|---|
| `v2/main.go` | W3a: wiring | 3 |
| `v2/machine.go` | W2d: handlers | 2 |
| `v2/internal/flyapi/client.go` (contract) | W1a: contracts | 1 |
| `v2/internal/config/reader.go` (contract) | W1a: contracts | 1 |
| `v2/internal/extbin/runner.go` (contract) | W1a: contracts | 1 |
| `v2/machine_spawn_state.go` | W1a: contracts | 1 |

### Known unavoidable conflict points

- `v2/main.go` is edited by this slug (W3a) *and* by the `config-store` slug's wiring. Merge strategy: `config-store` lands its wiring first (it has no runtime dependency on machine-lifecycle), then W3a rebases and adds the `aa machine` dispatcher. No co-editing.

- `go.mod` / `go.sum` — none of the workstreams add new external deps (stdlib only per philosophy). If a workstream finds itself reaching for a third-party package, it stops and raises.

---

## Non-goals revisited

Re-read before handoff:

- Not implementing `--detach`. (ADR-8)
- Not implementing human-readable names. (ADR-3)
- Not implementing cross-backend portability. (intent non-goal)
- Not implementing region placement beyond passing `--region` through. (intent non-goal)
- Not implementing confirmation prompts. (ADR-7)
- Not implementing auto-cleanup on failure. (ADR-5)
- Not implementing a non-interactive remote-exec verb. (intent non-goal)
- Not implementing resource sizing flags. (intent non-goal)

If an implementer finds themselves adding code for any of these, stop and return to planning.

---

## Amendments — 2026-04-23

### Label support added to spawn / list contract

The original plan exposed machines by backend Fly ID only. `docker-up`'s `--force` feature requires a way to identify a machine by a stable application-level tag derived from a user's working directory. Rather than a local state file, we add label support to the machine-lifecycle contract.

**Contract additions:**

- `SpawnSpec` gains `Labels map[string]string`. Values are written through to Fly Machine metadata on create.
- `Machine` (the list/get result) gains `Labels map[string]string`, populated from Fly Machine metadata on read.
- New method on the interface consumed by sibling slugs: `FindByLabel(ctx, key, value string) ([]Machine, error)`. Returns zero, one, or many machines matching that exact label kv pair in the configured app. No special behavior on empty — empty slice is a valid result.

**Usage invariant:** a single label `key=value` pair uniquely identifies at most one machine *for docker-up's purpose*. The method returns a slice because the underlying metadata surface doesn't enforce uniqueness; docker-up is responsible for deciding what to do when it finds more than one.

**Label key convention:** keys used by `aa` itself are prefixed `aa.`. The only `aa.*` label in v1 is `aa.up-id` (owned by docker-up). User-visible CLI does not currently expose label read/write — labels are an internal contract between sibling slugs.

**Impact on workstreams:**
- `fly-client` (Wave 2): the HTTP body for `POST /apps/{app}/machines` gains a `metadata` field on `config`; the GET response is already metadata-carrying.
- `handlers` (Wave 2): no user-facing verb changes. `ls` may gain a hidden `--label key=value` filter for debugging; deferred.
- Contract file gets the `FindByLabel` signature added before Wave 2 starts.
