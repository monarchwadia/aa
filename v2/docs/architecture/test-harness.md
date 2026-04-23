# Test harness — architecture notes

**Status:** proposed, wave 1.
**Date:** 2026-04-23.
**Sources:** `docs/intent/test-harness.md`, `docs/test-harness.md`, `v1/docs/PHILOSOPHY.md`, `v2/main.go`, `v2/machines.go`.

This document settles the open points left by the intent and spec phases, defines the public API of the harness so later-wave workstreams can draft their tests against it, and lays out the workstream breakdown for building the harness itself. No code is written here.

Pinned project-wide decisions (carried in from the `plan-implementation` brief; not re-litigated):

- Go stdlib only. `net/http/httptest`, `testing`, `os`, `os/exec`, `encoding/json`, `io`, `path/filepath`.
- Base-URL injection via `FLY_API_BASE` (default `https://api.machines.dev/v1`); `aa` reads it at startup.
- Record-mode trigger: `AA_TEST_RECORD=1`, requires `FLY_API_TOKEN`.
- Snapshot storage: one JSON file per test at `v2/testdata/snapshots/<test-name>.json`, containing an ordered array of `{request, response}` entries.
- Fake binaries: shell scripts in a per-test temp `bin/`; `PATH=$tmp/bin:$PATH` prepended; fakes log argv+env to a file the test reads back.
- `HOME=$tmp` and `XDG_CONFIG_HOME=$tmp/.config` per test.
- `aa` binary compiled once per `TestMain` into a temp dir, cached across tests in the package.

---

## Seams & boundaries

The harness is a Go package at `v2/testhelpers/` that every other slug's test code imports. It is the sole consumer of the environment-variable injection points listed above; everything else in `aa` remains blind to it. Two boundaries:

1. **Between the test process and the compiled `aa` child.** Crossed only by: argv, environment variables (`FLY_API_BASE`, `FLY_API_TOKEN`, `HOME`, `XDG_CONFIG_HOME`, `PATH`), stdio, exit code, and files written under `$HOME`. The harness never reaches into `aa`'s internals and never re-invokes `main` in-process.
2. **Between the harness and the recording on disk.** The snapshot JSON file is the source of truth in replay; it is the sink in record. Everything between — matching, scrubbing, diffing — is internal.

### Go-level entry points (contract — see `ADR-6` for the full signature set)

The public API lives in two files under `v2/testhelpers/`:

- `sandbox.go` — the `Sandbox` type, its constructor, and run/assert methods.
- `fakes.go` — the `FakeBinary` declaration type.

```go
// Sandbox is the per-test isolation boundary. One test = one Sandbox.
// Call NewSandbox once from the top of a test function. Teardown is automatic
// via t.Cleanup.
type Sandbox struct { /* unexported */ }

// NewSandbox builds a fresh sandbox for the given test. snapshotName selects
// the on-disk snapshot at v2/testdata/snapshots/<snapshotName>.json.
// In replay mode the snapshot must exist; in record mode it is overwritten.
func NewSandbox(t *testing.T, snapshotName string) *Sandbox

// ExpectBinary declares a fake binary that must be on PATH for the child
// process. Each call appends a declaration; order is significant only if
// the same binary name is declared more than once (rare).
func (s *Sandbox) ExpectBinary(b FakeBinary)

// RunAA executes the compiled aa binary inside the sandbox with the given
// argv and extra env. HOME, XDG_CONFIG_HOME, PATH, FLY_API_BASE are already
// wired. The returned Result is fully captured — the child has exited.
func (s *Sandbox) RunAA(t *testing.T, argv []string, env map[string]string) Result

// BinaryInvocations returns the ordered log of calls into a named fake
// binary captured during the most recent RunAA. Each entry records argv,
// filtered env, and stdin bytes.
func (s *Sandbox) BinaryInvocations(name string) []Invocation

// SnapshotPath returns the absolute path of the snapshot file backing this
// sandbox. Useful for tests that want to assert the snapshot exists or
// inspect it after a record-mode run.
func (s *Sandbox) SnapshotPath() string

// Result is the outcome of a single aa child process.
type Result struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}

// FakeBinary declares a fake executable to plant on PATH. ExitCode, Stdout,
// Stderr are the canned response. If the binary is invoked more times than
// declared, the extra invocations reuse the last declaration.
type FakeBinary struct {
    Name     string // e.g. "flyctl"
    ExitCode int
    Stdout   string
    Stderr   string
}

// Invocation is one observed call into a fake binary during RunAA.
type Invocation struct {
    Argv  []string
    Env   map[string]string // filtered to AA_*, FLY_*, HOME, PATH, XDG_*
    Stdin []byte
}
```

That is the complete public surface. Any slug writing e2e tests consumes only these symbols.

---

## Data flow

Two fakes, two modes.

### HTTP fake — replay mode

1. `NewSandbox` reads `v2/testdata/snapshots/<name>.json` into an ordered queue of `{request, response}` entries.
2. Starts an `httptest.Server` whose handler pops the next entry, matches the incoming request (see `ADR-1`), and serves the recorded response.
3. Exports `FLY_API_BASE=<httptest.Server.URL>` into the child's environment.
4. On mismatch, the handler writes an HTTP 599 with a diagnostic body *and* records the failure on the Sandbox; the next `RunAA` assertion pulls it out (see `ADR-3`). Matching order is strict FIFO.

### HTTP fake — record mode

1. `AA_TEST_RECORD=1` detected. `FLY_API_TOKEN` required or the sandbox panics before starting.
2. `httptest.Server` runs as a reverse proxy to `https://api.machines.dev/v1`. Authorization header is forwarded from `FLY_API_TOKEN`.
3. Every exchange is captured, scrubbed (see `ADR-2`), and appended to an in-memory slice.
4. On `t.Cleanup` (only if the test did not fail), the slice is marshaled and written to the snapshot file, replacing any prior content (see `ADR-5`).

### Binary fake — both modes

1. Before `RunAA`, the sandbox writes one shell script per declared `FakeBinary` into `$tmp/bin/<name>`.
2. Each script is a two-line `sh` dispatcher: it appends `$0 $@` plus a `printf`-ed env dump to `$tmp/invocations/<name>.log` (one line per field, null-separated so argv and env are unambiguous), then emits the declared stdout/stderr via `printf` and exits with the declared code.
3. The child's `PATH` is `$tmp/bin:<original PATH>`, so `exec.Command("flyctl", ...)` inside `aa` resolves to the fake.
4. After `aa` exits, `BinaryInvocations` parses the log file and returns the list.

Record mode does not fake binaries — real `flyctl` must be on the developer's `PATH`. That is a deliberate asymmetry: the binary fakes exist to avoid dependencies in replay; in record mode the developer is already accepting live dependencies.

---

## Failure modes

| Failure | Detection | Behavior |
| --- | --- | --- |
| Replay request does not match next snapshot entry | HTTP handler compares per `ADR-1` | Next `RunAA` assertion call surfaces a multi-line diff per `ADR-3`; test fails via `t.Fatalf`. |
| Replay ends with un-consumed snapshot entries | `t.Cleanup` inspects the queue | Test fails per `ADR-4`. |
| Record-mode run when `FLY_API_TOKEN` unset | `NewSandbox` | `t.Fatalf` before any subprocess spawns. |
| Snapshot file missing in replay mode | `NewSandbox` | `t.Fatalf` with hint: "run with AA_TEST_RECORD=1 to generate". |
| Snapshot file malformed JSON | `NewSandbox` | `t.Fatalf` citing file and offset. |
| Fake binary invoked but not declared | Sandbox has no script for name; `aa` gets "not found" from the OS | Surfaces as non-zero exit from `aa` with `exec: "<name>": executable file not found`; test asserts on that naturally. No special harness path. |
| Declared fake binary never invoked | Not an error — declarations are an allowlist, not an expectation list. A separate test assertion on `BinaryInvocations(name)` length catches this if the test cares. |
| `aa` writes outside `$HOME` | Not detected by the harness. Out of scope; `aa` itself is what would need to be audited. Covered by the intent's "run under a filesystem with those paths unreadable" verification step, which is a developer workflow, not a harness feature. |
| Child `aa` process hangs | `RunAA` has a 30s hard timeout; on expiry it kills the child and fails the test with stdout/stderr captured so far. |

Loud failure on every row, per PHILOSOPHY axis 3.

---

## State & lifecycle

Per-test sandbox lifecycle, in strict order:

1. `NewSandbox(t, name)` creates `tmp := t.TempDir()`. `t.TempDir()` already registers its own cleanup, so the whole directory vanishes at end of test.
2. Writes layout under `tmp`: `bin/`, `invocations/`, `.config/aa/`.
3. Loads the snapshot (replay) or prepares an empty capture slice (record).
4. Starts the `httptest.Server`. Registers `t.Cleanup(server.Close)`.
5. Registers `t.Cleanup` hook that:
   - In replay: asserts the snapshot queue is empty; fails the test if not.
   - In record: marshals captures, scrubs, writes to disk. Skipped if `t.Failed()` so a broken test never poisons a snapshot.
6. Returns the `Sandbox`.

`RunAA` is re-entrant — a single test may invoke `aa` multiple times. The snapshot queue and invocation logs are **cumulative across calls** within one sandbox, because that is the natural model: one test = one flow = one snapshot.

The compiled `aa` binary is built exactly once per test package via `TestMain`. A package-level `var aaBinaryPath string` is populated there and read by `RunAA`. The build goes into a package-level `t.TempDir()`-equivalent (`os.MkdirTemp`) keyed off the package; `TestMain` deletes it before exiting.

---

## Testing surface

Two distinct categories. Only the first lives in this workstream.

### Meta-tests (harness correctness) — **in scope for this workstream**

Unit tests of the harness's own internals, written with pure `testing`:

- **Snapshot matcher** (`snapshot_match_test.go`): table-driven. Cases cover method mismatch, path mismatch, query param reordering (must pass — see `ADR-1`), body mismatch when body is load-bearing, body absence when body is `null` in the snapshot (must pass), unknown-field body mismatch, and a known-good exact match.
- **Scrubber** (`scrub_test.go`): table-driven. Cases cover Authorization header redaction, `fo_`-prefixed tokens in bodies, org slug and machine ID patterns, and a case with no sensitive data (output equals input byte-for-byte).
- **Snapshot roundtrip** (`snapshot_io_test.go`): marshal → write → read → unmarshal produces identical entries; malformed file surfaces a useful error.
- **Fake binary** (`fakebin_test.go`): write a declaration, invoke via `exec.Command` inside the sandbox's temp bin dir, assert the log file parses back to the expected `Invocation`.
- **Sandbox lifecycle** (`sandbox_lifecycle_test.go`): `NewSandbox` + no calls + test ends; assert no files remain after `t.Cleanup` runs. Covered partially by `t.TempDir`'s own guarantees; meta-test verifies our `t.Cleanup` hooks don't leak goroutines or fail to close the httptest server.

**No e2e test of the harness itself.** The harness is e2e infrastructure; testing it end-to-end would be circular. Its correctness is established by unit tests here plus the fact that later-wave feature slugs' e2e tests pass.

### Tests-that-use-harness — **out of scope for this workstream**

Every other slug's e2e tests — machine-lifecycle, config-store, docker-up, docker-images, etc. — are consumers. They live in their own packages, own their own snapshot JSON files, and import `v2/testhelpers`. They arrive in later waves.

---

## ADRs

### ADR-1: Match strictness on replay

**Status:** accepted.

**Context.** Too strict → benign changes (header reordering, JSON key reordering) break every test. Too loose → drift that matters slips through. PHILOSOPHY axis 3 says drift is the main thing the harness exists to catch; axis 4 says no ceremony for no reason.

**Options considered.**
1. Byte-exact everything — path, query, headers, body.
2. Method + path + query + body (canonicalized); headers ignored entirely.
3. Method + path always; query set-compared; body compared after JSON canonicalization when both are JSON, else byte-exact; headers ignored except a whitelist.

**Decision.** Option 3, with the concrete rules:

- **Method:** exact match.
- **Path:** exact match (no wildcards in snapshots).
- **Query:** parsed, compared as a multimap. Order does not matter; duplicate values do. (`aa` does not currently emit queries, but `force=true` on delete does — the future-proofing is free.)
- **Body:** if both snapshot and incoming body are valid JSON, they are unmarshaled into `any`, canonicalized (map key sort, no whitespace), and byte-compared. If either is not valid JSON, byte-exact compare. If the snapshot body is `null`/empty, the incoming body is ignored entirely — the snapshot author declared it non-load-bearing.
- **Headers:** ignored entirely for matching. Authorization is the only one `aa` sets that matters, and it is scrubbed in the snapshot anyway.

**Consequences.** Tests survive benign JSON reordering. Header-only regressions are invisible to the harness — acceptable given `aa`'s HTTP surface is narrow and explicit. A test that *does* want to assert header content can do so via a future extension; YAGNI until then.

### ADR-2: Scrubbing policy

**Status:** accepted.

**Context.** Sensitive fields must not land in committed snapshots. A generic rule set risks missing new fields; per-recording declarations are tedious. PHILOSOPHY axis 5 says "safety at the boundary, not everywhere" — the boundary here is the moment a recording is written to disk.

**Options considered.**
1. Generic scrubbing rules only.
2. Per-snapshot declared redaction list.
3. Generic rules + a post-scrub forbidden-patterns check that panics if anything suspicious survives.

**Decision.** Option 3. The generic rules scrub, the check catches regressions. Rules applied to both request and response, headers and bodies:

- **Authorization header:** replaced with `Bearer REDACTED`.
- **Body tokens matching `fo_[A-Za-z0-9]{16,}` or `FlyV1 [A-Za-z0-9_-]{20,}`:** replaced with `REDACTED_TOKEN`.
- **Org slug field `org_slug`** in JSON request bodies: replaced with `REDACTED_ORG`.
- **Machine IDs in response bodies** — the `id` field at top level of machine objects: left as-is. (They are generated server-side; they don't identify the developer. Keeping them preserves test realism and lets matchers see request/response correlation.)
- **IP addresses** matching the standard IPv4/IPv6 regex in response bodies: replaced with `REDACTED_IP`.

**Post-scrub check.** After scrubbing, the serialized snapshot is grepped for:
- `FLY_API_TOKEN`'s literal value (looked up from env at record time).
- `$USER`.
- The developer's `$HOME` as an absolute path.
- Any `fo_` or `FlyV1 ` token-shaped string.

If any match, `NewSandbox` panics with the offending pattern before the file is written. Developer fixes the scrubber and re-records.

**Consequences.** The scrubber is small and greppable; the post-scrub check is a safety net that makes "I forgot to teach the scrubber about this field" a loud failure at record time, not a silent commit.

### ADR-3: Drift error format

**Status:** accepted.

**Context.** When a replay request does not match, the test must fail with enough information to diagnose immediately, per intent.

**Decision.** On mismatch, the harness `t.Fatalf`s with this exact shape:

```
snapshot drift in <test name> (snapshot <path>):
  expected request #<n>: <METHOD> <PATH>?<QUERY>
  actual   request #<n>: <METHOD> <PATH>?<QUERY>
  first differing field: <one of: method | path | query | body>
  expected <field>: <pretty-printed>
  actual   <field>: <pretty-printed>
```

If only the body differs, both bodies are JSON-canonicalized and a unified-diff-ish line-by-line compare is emitted (custom, stdlib-only: `strings.Split` on `\n`, mark changed lines with `- ` / `+ `). No external diff library.

If the test has made more requests than the snapshot has entries, the expected side reads `(no more expected requests)`.

**Consequences.** One format, always. No verbose/quiet modes. The message is verbose by default because debugging drift is exactly the situation where verbosity earns its keep.

### ADR-4: Unmatched remainder

**Status:** accepted.

**Context.** If the snapshot has 5 entries and the test consumes 3, should the test pass or fail?

**Options considered.**
1. Pass — snapshot is an upper bound.
2. Fail — snapshot is an exact expectation.

**Decision.** Fail. The snapshot is an ordered exact expectation. An unconsumed remainder means the code under test stopped making requests it used to make — that is drift, and by PHILOSOPHY axis 3 it must be loud.

The `t.Cleanup` hook checks the remaining queue. If non-empty and `!t.Failed()` (we don't pile on when the test is already failing for another reason), it emits:

```
snapshot has <k> unconsumed entries; the code under test stopped making these requests:
  #<n>: <METHOD> <PATH>
  ...
```

**Consequences.** Removing a real API call from `aa` becomes an observable event that forces a snapshot update. That is the desired property.

### ADR-5: Recording update workflow

**Status:** accepted.

**Context.** When a response shape legitimately changes, how does the developer update the snapshot?

**Options considered.**
1. Record-mode silently overwrites.
2. Require manual delete before record.
3. Diff-and-approve flow.

**Decision.** Option 1 — record mode overwrites. The safety net is `git diff` at commit time: the developer reviews the snapshot change like any other file. Adding ceremony to record mode would make it harder to fire, not easier, and record mode is already gated by two env vars.

**Workflow.**
1. Developer runs `AA_TEST_RECORD=1 FLY_API_TOKEN=fo_xxx go test ./<pkg> -run TestX`.
2. The snapshot file is overwritten.
3. Developer runs `git diff v2/testdata/snapshots/TestX.json` and inspects.
4. If the diff is as expected, commit. If not, fix `aa` and re-record.

Partial re-recording of individual exchanges within a snapshot is **not supported** — YAGNI. If surgical edits become common, the architecture reopens.

**Consequences.** Record mode is a hammer. That is acceptable because the input to record mode is narrow (one test at a time via `-run`) and the output is reviewed in a human diff before it lands.

### ADR-6: Package layout

**Status:** accepted.

**Context.** The harness must be small and rewriteable in one LLM session (PHILOSOPHY axis 2 and 4). One file per responsibility.

**Decision.** `v2/testhelpers/` with:

- `sandbox.go` — `Sandbox` type, `NewSandbox`, `RunAA`, `BinaryInvocations`, `SnapshotPath`, lifecycle glue. Public.
- `fakes.go` — `FakeBinary`, `Invocation`, `Result` types. Public.
- `snapshot.go` — snapshot JSON schema (`snapshotEntry`, `recordedRequest`, `recordedResponse`), read/write, queue type. Unexported.
- `match.go` — per `ADR-1` matcher; pure function over two `recordedRequest`s. Unexported.
- `scrub.go` — per `ADR-2` scrubber and post-scrub check. Unexported.
- `httpfake.go` — the `httptest.Server` handler for replay, and the reverse-proxy handler for record. Unexported.
- `fakebin.go` — shell script writer and invocation log parser. Unexported.
- `aabin.go` — `TestMain` helper that compiles `aa` once per package and exposes the path. Exported function, taking `*testing.M`.

Plus meta-tests alongside: `snapshot_match_test.go`, `scrub_test.go`, `snapshot_io_test.go`, `fakebin_test.go`, `sandbox_lifecycle_test.go`.

Nine non-test files, five test files. Each well under 400 LOC. The whole package is legible in one LLM pass.

**Consequences.** Each workstream owns a tight file set. The `Sandbox` methods in `sandbox.go` delegate to internals, so the public surface is stable even as internals evolve.

---

## Workstreams

The harness itself is one workstream — `test-harness` — scheduled in **wave 1**. It has no consumers in wave 1: the contract file (`sandbox.go` skeleton with type and method signatures, bodies stubbed) lands first, and every other slug's e2e tests draft against it in later waves.

### Contract files (written first, locked before any workstream starts)

1. `v2/testhelpers/sandbox.go` — exported types and method signatures per the "Seams & boundaries" section above. Bodies: `panic("not implemented")`. This file is the public API.
2. `v2/testhelpers/fakes.go` — `FakeBinary`, `Invocation`, `Result` type definitions. No functions; data only.

Both files are authored by the owner of the `test-harness` workstream as the first commit, before any other workstream reads them.

### Wave 1 — this workstream

**Name:** `test-harness`.

**Owns:**
- `v2/testhelpers/sandbox.go`
- `v2/testhelpers/fakes.go`
- `v2/testhelpers/snapshot.go`
- `v2/testhelpers/match.go`
- `v2/testhelpers/scrub.go`
- `v2/testhelpers/httpfake.go`
- `v2/testhelpers/fakebin.go`
- `v2/testhelpers/aabin.go`
- `v2/testhelpers/snapshot_match_test.go`
- `v2/testhelpers/scrub_test.go`
- `v2/testhelpers/snapshot_io_test.go`
- `v2/testhelpers/fakebin_test.go`
- `v2/testhelpers/sandbox_lifecycle_test.go`

**Consumes:** nothing in `v2/` other than the existing `v2/main.go` and `v2/machines.go`, and only through the `FLY_API_BASE` env var contract. `v2/main.go` must honor `FLY_API_BASE`; if it does not yet, a **single-line change** to `v2/main.go` adds that (see "Shared / single-writer files" below).

**Produces:** the public API in the two contract files. Later-wave e2e tests import these symbols and nothing else.

**Fakes needed:** none. The harness is the leaf of the dependency tree.

**Tests:** the five meta-test files listed above. All unit scope; no e2e.

### Wave 1 has no other workstreams

The test harness is the only wave-1 item because every other e2e-capable slug transitively depends on it. The `config-store`, `machine-lifecycle`, `docker-up`, and `docker-images` slugs are all wave-2-or-later and schedule their e2e-tests step against this harness.

### Shared / single-writer files

- `v2/main.go` — currently defines `flyAPIBase` as a `const`. The harness requires it to be read from the `FLY_API_BASE` env var at startup with the current value as default. This is a one-line change; owner is the `test-harness` workstream; it lands with the contract files. Other slugs' implementation work in `v2/main.go` is scheduled later and merges on top.
- `v2/machines.go` — no changes required. `flyAPIBase` is read as a package-level identifier; changing its initialization in `main.go` is transparent to `machines.go`.

### Known unavoidable conflict points

None within wave 1. The one shared file (`v2/main.go`) has a single change with a single owner this wave; later waves' edits to `main.go` are additive (new subcommands) and don't touch the `flyAPIBase` initialization.

### Rules in force for this workstream

- Interfaces in `sandbox.go` and `fakes.go` are locked once committed. Any change requires a revision of this architecture doc.
- No file in `v2/testhelpers/` exceeds 400 LOC. If it does, split per `ADR-6`'s one-responsibility rule.
- The harness imports only the Go standard library. Violations are a block.
- Meta-tests do not spawn subprocesses of real `aa` — they test the harness's internals directly. Subprocess testing is exercised by consumer slugs in later waves, which is where it belongs.

---

## Amendments — 2026-04-23

### Two HTTP fakes, not one

The original plan faked one HTTP surface (Fly Machines API at `api.machines.dev`). `docker-images` requires a second: the container registry at `registry.fly.io`. These are distinct hosts with distinct protocols (Fly Machines REST vs Docker Registry v2 HTTP API).

**Harness changes:**
- `Sandbox` now starts **two** `httptest.Server` instances — one for each surface.
- Sandbox constructor wires both URLs into the child `aa` process via env vars: `FLY_API_BASE` and `AA_REGISTRY_BASE`.
- Snapshot files remain one per test, but each record now includes a `surface` discriminator (`"api"` or `"registry"`) so replay routes correctly.

**Record-mode change:** when recording, the harness captures from both real services and writes them to the same per-test snapshot file in chronological order, tagged by surface.

### Env vars are overrides, not sources of truth

The config-store amendment promoted `endpoints.api` and `endpoints.registry` to first-class config keys with env-var overrides. Tests continue to use env vars (it's how `httptest.Server` URLs get injected), but the harness no longer needs to seed the config file with endpoint values — the env vars take precedence.

### Label-aware HTTP snapshots

`machine-lifecycle` now reads/writes labels via Fly Machine metadata. Recordings that exercise label round-trips (`docker-up --force`) must preserve the `metadata` field in response bodies. Scrubbing policy treats `metadata` as non-sensitive by default — it's application-level tagging, not credentials. If a specific label value is sensitive in a particular test, add a per-test scrub rule.
