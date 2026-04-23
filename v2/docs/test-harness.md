# Test harness — developer-facing documentation

Audience: a developer working on `aa` itself. This is not user documentation for the CLI.

Status: **not yet implemented**. This document is the spec. Tests and code written later must make this document true. Open points from the intent doc (`docs/intent/test-harness.md`) are carried forward here as **proposed, not yet specified**; the architecture phase will settle them before any code lands.

---

## Pitch

The end-to-end test harness lets you run the **real compiled `aa` binary** against **recorded HTTP exchanges** and **fake external binaries**, with no network access, no paid cloud account, and no writes to your real home directory. You write a test that invokes `aa machine spawn` (or any other subcommand); the harness transparently serves the Fly.io API responses from files on disk, resolves `flyctl` to a fake binary the harness planted on `PATH`, and confines `aa`'s config directory to a per-test temp dir. The same test runs offline in CI-less `go test ./...` and, when you flip into **record mode**, hits the live services once to capture fresh fixtures you can commit.

## Why this exists

`aa` has exactly two external dependencies at runtime (see `v2/main.go`):

1. **One HTTP API** — the Fly.io Machines API at `https://api.machines.dev/v1/...` (app existence checks, machine create, machine poll, machine list/start/stop/rm).
2. **Two external binaries** shelled out via `os/exec` — `flyctl` (for `flyctl ssh console`) and, in the container-build cut, a container build tool. Both are invoked as subprocesses and expect real stdin/stdout/stderr and a real exit code.

Any honest e2e test has to simulate both surfaces. The harness is the single shared place that does that.

---

## Mental model

Two kinds of fake, two modes, one isolation boundary.

### Two kinds of fake

| Surface | Real thing | Fake thing |
| --- | --- | --- |
| Fly.io HTTP API | `https://api.machines.dev/v1/...` | A local HTTP server the harness starts, backed by recorded exchange files on disk. `aa` is pointed at it via its base-URL injection mechanism (**proposed, not yet specified** — candidates: an env var, a build-tag-gated constant, or a config key). |
| `flyctl`, container build tool | Real binaries on `$PATH` | Fake binaries the harness writes into a temp `bin/` directory and prepends to `$PATH` for the child `aa` process. They read a scripted response from a file and print it, honoring an exit code the test declared. |

### Two modes

- **Replay mode** (default). Fully offline. The harness refuses any network egress and refuses to invoke real external binaries. Unmatched requests fail the test loudly (see "Drift handling" below).
- **Record mode** (opt-in, rare). The harness permits real network and real external binaries, captures every HTTP exchange, scrubs sensitive fields, and writes the recordings into the repo tree. The developer reviews the diff and commits.

### Isolation boundary

Every test gets a fresh per-test temp directory, referred to below as `$AA_TEST_HOME`. Before spawning the compiled `aa` binary, the harness:

- Sets `$HOME` (and on Linux `$XDG_CONFIG_HOME`) to paths under `$AA_TEST_HOME`, so that `os.UserConfigDir()` inside `aa` resolves to `$AA_TEST_HOME/config/...`. This is the same path `v2/main.go:configPath()` reads from.
- Prepends `$AA_TEST_HOME/bin` to `$PATH`, so that `exec.Command("flyctl", ...)` resolves to the harness's fake.
- Points `aa`'s Fly API base URL at the harness's local HTTP server. Mechanism **proposed, not yet specified**.
- Unsets or overrides `FLY_API_TOKEN` so the real developer's token never leaks into a test.
- Clears any other `AA_*` and `FLY_*` env vars that might have followed the developer's shell in.

After the test, the temp directory is removed. Nothing persists outside the repo working tree.

---

## Running tests

Default run — fully offline, no accounts, no network:

```
go test ./...
```

That is the only command a contributor (or LLM session) ever needs to know to verify the suite is green.

Recording — one-time, against a real Fly.io account:

```
AA_TEST_RECORD=1 FLY_API_TOKEN=fo_xxx go test ./test/e2e -run TestSpawnHappyPath
```

The exact env var name (`AA_TEST_RECORD` here) and whether the token is consumed from `FLY_API_TOKEN` or a test-specific variable is **proposed, not yet specified**. The shape of the command above is indicative: one env var toggles record mode, one env var (or config entry) provides the live token. Record mode must be hard to fire accidentally — the intent doc lists this as an open design question.

---

## How to write a new e2e test (the recipe)

The harness is consumed from Go tests using stdlib only (`testing`, `os`, `os/exec`, `net/http/httptest`, `encoding/json`). No third-party test libraries.

A new end-to-end test follows this eight-step recipe:

1. **Add a Go test file** under the e2e package directory. Directory location is **proposed, not yet specified**; candidates are `v2/test/e2e/` or `v2/e2e/`.
2. **Call the harness constructor** to get an isolated sandbox. The sandbox owns `$AA_TEST_HOME`, the fake HTTP server, and the fake-binary directory. It implements `t.Cleanup` so teardown is automatic.
3. **Declare which external-binary calls you expect** (e.g. `flyctl ssh console --app <app-name> --machine <id>`, where `<app-name>` comes from the configured `defaults.app`) and what canned output and exit code each should return. Mechanism **proposed, not yet specified**.
4. **Point the harness at a named recording** — e.g. `"spawn_happy_path"`. In replay mode the harness loads the recording and asserts every request matches; in record mode it captures into that name.
5. **Invoke the real compiled `aa` binary** via the harness (it compiles `aa` once per test run and caches the binary path). Pass argv, stdin, and any env overrides.
6. **Assert** on exit code, stdout, stderr, files written under `$AA_TEST_HOME`, and the sequence of fake-binary invocations.
7. **Run once in record mode** with a live token to capture the recording. Review the scrubbed output in `git diff`. Commit the recording alongside the test.
8. **Run in default (replay) mode** to confirm it passes offline. Push.

After step 7 the test is self-contained: anyone with the repo can run it offline forever.

### Example (indicative — real API unspecified)

```go
package e2e

import (
    "testing"
)

func TestSpawnHappyPath(t *testing.T) {
    sandbox := newTestSandbox(t, "spawn_happy_path")

    sandbox.ExpectExternalBinary("flyctl",
        // default app name is illustrative — real value will come from defaults.app
        WantArgs("ssh", "console", "--app", "<app-name>", "--machine", "d8e7..."),
        RespondExitCode(0),
        RespondStdout(""),
    )

    result := sandbox.RunAA(t,
        Args("machine", "spawn", "--image", "ubuntu:22.04"),
        Env("FLY_API_TOKEN", "dummy-replay-token"),
    )

    if result.ExitCode != 0 {
        t.Fatalf("aa machine spawn exited %d; stderr:\n%s", result.ExitCode, result.Stderr)
    }
    if !contains(result.Stdout, "is running") {
        t.Fatalf("stdout missing running confirmation:\n%s", result.Stdout)
    }
}
```

All type and function names in that example (`newTestSandbox`, `ExpectExternalBinary`, `WantArgs`, `RespondExitCode`, `RespondStdout`, `RunAA`, `Args`, `Env`, `result.ExitCode`) are **proposed, not yet specified**. The shape — sandbox constructor, declarative expectations, a single `RunAA` call returning a result value — is the intent.

---

## How recordings are stored

**Proposed, not yet specified.** The intent doc explicitly leaves the grain and layout of recorded artifacts open. What is decided:

- Recordings live **inside the repository tree**, under the test package, so they can be committed as-is and reviewed in pull requests.
- They are **human-readable, diff-friendly text** — no binary blobs, no gzip, no base64 of a whole HTTP response. A reviewer must be able to read a recording in a PR without tooling.
- Sensitive fields are **absent** from committed recordings (see "Scrubbing" below).

What is not yet decided:

- Whether a recording is one file per HTTP exchange, one file per test, one file per named flow, or a directory tree mirroring the test name.
- Whether two tests can share a recording or each test owns its own.
- The exact on-disk format (raw HTTP wire format, JSON, a custom line-oriented format).

Indicative-only example of what a layout might look like, to make the shape concrete — **do not take as spec**:

```
v2/test/e2e/
  spawn_test.go
  recordings/
    spawn_happy_path/
      01_get_app.txt
      02_post_machine.txt
      03_get_machine_state.txt
      04_get_machine_state.txt
```

The architecture phase picks one layout and records it as an ADR.

---

## Scrubbing sensitive fields

Any field that would identify a real account, a real machine, or a real developer must be absent from the committed recording. Concretely, the harness scrubs (at least):

- **Bearer tokens** in `Authorization` headers.
- **Fly.io account identifiers** and organization slugs.
- **Machine IDs** generated by the backend, where the test does not care about the specific value.
- **IP addresses** appearing in responses.
- **Machine-local identifiers** (hostnames, the developer's `$USER`, absolute paths under the developer's real home).

The mechanism that performs this scrubbing is **proposed, not yet specified**. The intent doc leaves open whether scrubbing is driven by a generic rule set (known header and field names), by per-recording explicit declarations, or by both. What is decided:

- The suite must provide a check the developer can run that greps the entire recordings tree for forbidden patterns (the real token prefix, the developer's account slug, etc.) and fails if any are found. This check runs before any recording is committed.
- When a new sensitive field appears on the backend and nobody teaches the scrubber about it, the harness must fail loud the first time it sees the field in a recording, not silently commit it.

---

## Fake external-binary layer

`aa` shells out to `flyctl` (and, later, a container build tool) via `exec.Command`. The harness does not stub those calls in-process — `aa` is a real compiled binary, so the fake has to be a real executable on `$PATH`.

Mechanism:

- The harness writes a tiny executable into `$AA_TEST_HOME/bin/flyctl` before the `aa` process starts.
- `$AA_TEST_HOME/bin` is prepended to the child's `$PATH`.
- When invoked, the fake records its argv, env (filtered), and stdin into a per-test log file, then emits the canned stdout/stderr declared by the test and exits with the declared code.
- After `aa` exits, the test reads the log file and asserts on the sequence of invocations.

Exactly how the fake is structured (a prebuilt Go binary the harness compiles once, a shell script the harness writes per-test, or a single dispatcher binary with per-call scripting) is **proposed, not yet specified**.

Interactivity: the production `flyctl ssh console` path is interactive (attaches the user's terminal). Whether the e2e harness supports fakes that consume scripted stdin, or only non-interactive fakes (exit-code + canned output), is **proposed, not yet specified** — flagged as an open question in the intent doc.

---

## Assertions available to a test

A test can assert against any of:

- **Exit code** of the `aa` process.
- **Stdout** — full captured bytes.
- **Stderr** — full captured bytes.
- **Files written under `$AA_TEST_HOME`**, including `aa`'s config file (`$AA_TEST_HOME/config/aa/config`, per `v2/main.go:configPath()`) and any other artifact the CLI may write in future.
- **The sequence of fake-binary invocations** — argv, filtered env, stdin bytes, and order relative to HTTP calls.
- **The sequence of HTTP requests** matched against the recording — method, path, and whichever body/header fields the matcher deems load-bearing.

The test must **not** reach into `aa`'s internals or re-invoke `main` in-process. The process boundary, flag parsing, and exit-code path are part of what's being tested.

Matching strictness for HTTP requests (which fields must match exactly, which may drift) is **proposed, not yet specified** — listed as an open question in the intent doc. The rule of thumb: too strict causes flaky tests on benign changes; too loose defeats the point.

---

## Drift handling

Drift is the main failure mode the harness exists to catch. Two flavors:

### An unmatched request during replay

When the compiled `aa` makes an HTTP request (or invokes an external binary) that has no matching entry in the recording, the test **fails immediately and loudly** with a message that names:

1. The test that was running.
2. The unmatched request — method, path, and (scrubbed) body.
3. The closest recorded exchange by whatever similarity metric the matcher uses, to help the developer see what drifted.

No silent fallback, no best-effort match, no "pass with a warning." Per intent and per `PHILOSOPHY.md` axis 3 (Observability), loud failure beats silent drift every time.

### Updating a recording when the API shape changes

When Fly.io changes a response field and the existing recording goes stale, the developer's workflow is:

1. Delete or invalidate the recording for the affected test.
2. Re-run in record mode against a live account to capture a fresh one.
3. Review the diff — both the substantive change and any new sensitive fields the scrubber needs to learn about.
4. Commit.

Whether the harness also supports **partial re-record** (re-capture only specific exchanges within a recording, leaving others untouched) or a **diff-and-approve** workflow that highlights the backend change without requiring a full re-record, is **proposed, not yet specified** — the intent doc flags this as the most likely future pain point and requires an answer before the harness ships.

---

## Constraints the harness itself lives under

These are hard constraints, lifted from `docs/intent/test-harness.md`:

- **Go standard library only.** No third-party testing libraries, no recording/replay libraries, no assertion libraries, no fake-process libraries. `net/http/httptest`, `testing`, `os/exec`, `encoding/json` — that's the toolkit.
- **Zero footprint outside the repo working tree.** No writes to `$HOME`, no writes to OS config/cache dirs, no env mutations that outlive the test process. Verifiable by running the suite under a process with those paths made unreadable.
- **Works against the real compiled `aa` binary**, not an in-process re-invocation of `main`.
- **Recordings are human-readable diff-friendly text.**
- **Replay failures are loud and specific.** Silent fallbacks are forbidden.
- **The harness is small enough to rewrite in one LLM session.** Per `PHILOSOPHY.md` axis 2 (Evolvability) and axis 4 (Low ceremony). If the harness grows its own framework, it has failed.

---

## Non-goals

Lifted from `docs/intent/test-harness.md`. The harness does not do, and will not grow to do, any of the following:

- Performance or load testing of `aa` or the backend.
- Integration and unit tests for individual features — those live with each feature.
- Mutation testing or coverage-threshold enforcement.
- Continuous-integration pipeline wiring (GitHub Actions, etc.). The suite passes on a laptop; CI is a separate decision.
- Real-cloud smoke tests in the default test run. A manual opt-in path may exist; it is not part of routine `go test`.
- A general-purpose mocking framework for code outside `aa`.
- Multi-contributor test data management. Single-maintainer assumption holds.

---

## Open points carried forward from intent

These are the design questions the architecture phase must resolve. Until then, code and tests must not assume an answer.

- **Record-mode trigger.** Env var vs. build tag vs. standalone `go run` recorder. Proposed surface: `AA_TEST_RECORD=1`. Not yet specified.
- **Snapshot format and grain.** One file per exchange, per test, or per flow. Not yet specified.
- **Per-test vs. per-flow recordings.** Isolation vs. duplication tradeoff unresolved.
- **Re-recording workflow on API drift.** Full re-record vs. partial vs. diff-and-approve. Not yet specified.
- **Scrubbing mechanism.** Generic rule set vs. per-recording declarations vs. both. Not yet specified.
- **Interactive stdin for fake binaries.** In scope for `flyctl ssh console` coverage, or out of scope? Not yet specified.
- **Matching strictness on replay.** Which request fields must match exactly. Not yet specified.
- **Base-URL injection into `aa`.** How the compiled binary is told to hit the harness server instead of `https://api.machines.dev/v1`. Not yet specified.
