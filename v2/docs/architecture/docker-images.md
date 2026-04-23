# docker-images — Architecture Notes

**Slug:** `docker-images`
**Status:** proposed
**Date:** 2026-04-23
**Scope:** `aa docker image {build,push,ls,rm}`. The glue command `aa docker up` is a separate slug and is explicitly not in scope here.

This document is the plan for how the `aa docker image` command group gets built. It reads intent (`/workspaces/aa/v2/docs/intent/docker-images.md`) and documentation (`/workspaces/aa/v2/docs/docker-images.md`) as source of truth. Philosophy (`/workspaces/aa/v1/docs/PHILOSOPHY.md`) is the tiebreaker.

Writing code is not in scope for this step. No `.go` files are touched by this document. The first test or line of code lands in later workflow steps, scheduled against the `## Workstreams` section below.

---

## Seams & boundaries

Four boundaries matter. Everything that crosses a boundary is named here and nowhere else in the codebase gets to invent a parallel crossing.

### Seam 1: The `docker` subprocess (outbound)

**Where:** every `os/exec` invocation of the local `docker` binary.
**What crosses out of `aa`:**

- An argv slice (never a shell string) — `docker build -t <tag> <context>`, `docker push <tag>`, `docker login registry.fly.io -u x -p <token>`, `docker image inspect <tag>`.
- A filtered environment — inherit `$PATH`, `$HOME`, nothing else by default. Explicitly do **not** export `$FLY_API_TOKEN` to `docker`; the token is passed as `-p` to `docker login` and no further.
- `stdin` attached (build may consume a Dockerfile from stdin only if a future slug needs it — not today).

**What crosses back in:**

- Exit code (the contract).
- `stdout` / `stderr` bytes — forwarded verbatim to the user's terminal for `build` and `push` (see ADR 5). Captured into a buffer only for small probe commands like `docker image inspect`.

**What must not leak across:**

- No Fly token in `$DOCKER_*` env. No token in argv except the one `login` call.
- No interactive tty assumptions beyond what `docker` itself inherits.
- No `~/.docker/config.json` writes we depend on lasting between invocations — login state is treated as ephemeral (see ADR 4).

This seam is fronted by a small typed interface (`dockerRunner`) so the test harness can substitute the fake binary described in `/workspaces/aa/v2/docs/test-harness.md` via `$PATH` prepending. Inside production code, there is exactly one implementation: `execDockerRunner`. The interface exists because a test fake needs to substitute — not for hypothetical future runners (YAGNI).

### Seam 2: The Fly registry HTTP API (outbound)

**Where:** `ls` and `rm` paths that query and delete from `registry.fly.io`.
**What crosses out:**

- `GET` / `DELETE` requests against the registry's HTTP v2 API under `FLY_API_BASE` (default: the real Fly API; overridable for tests). Bearer token in `Authorization` header sourced from config-store.
- JSON request bodies where required; otherwise empty.

**What crosses back:**

- HTTP status code.
- Response JSON — a narrow, named Go struct per endpoint. No `map[string]any` at the business layer.

**What must not leak across:**

- No raw `http.Response` objects up into CLI-handler code. The HTTP layer returns decoded structs or a typed error with the status code and a scrubbed body preview.
- No token in logs, not even at `-v`.

### Seam 3: The config-store (inbound read)

**Where:** one function call at the top of every subcommand.
**Contract:** `func ReadFlyToken() (string, error)` — returns the bearer token or a typed "missing key" error that names the exact config key (`token.flyio`). Implementation lives in the config-store slug. This slug consumes it; it does not re-implement config reading. The existing `v2/main.go` reader is the de-facto contract until config-store's own architecture doc lands — the interface this slug depends on is exactly that shape.

### Seam 4: The preflight check (inbound registration)

**Where:** the existing `preflight()` in `v2/main.go`.
**Contract:** this slug appends `docker` to the list of required binaries preflight searches for. Preflight is the single writer; this slug files a one-line addition and does not invent a parallel mechanism.

---

## Data flow

The happy path, by stage. Each stage is a named function in the eventual implementation.

```
aa docker image build ./myapi --tag myapi
  ├── (1) resolveTag(path, --tag)                → "registry.fly.io/aa-apps/myapi:latest"
  ├── (2) ensureDockerfile(path)                  → error if missing
  └── (3) runDockerBuild(runner, tag, path)       → streams stdout/stderr; exit code

aa docker image push myapi
  ├── (1) resolveTag("myapi", nil)                → "registry.fly.io/aa-apps/myapi:latest"
  ├── (2) ReadFlyToken()                          → token (from config-store)
  ├── (3) ensureLocalImage(runner, tag)           → error if unknown to docker
  ├── (4) dockerLogin(runner, token)              → once per process (see ADR 4)
  └── (5) runDockerPush(runner, tag)              → streams stdout/stderr; exit code

aa docker image ls
  ├── (1) ReadFlyToken()
  ├── (2) listRepositories(token)                 → []repository, HTTP
  ├── (3) for each: listTags(token, repo)         → []tag, HTTP
  ├── (4) filterToAAOwned(results)                → see ADR 2
  └── (5) printImageTable(stdout, results)

aa docker image rm <tag> [<tag>...]
  ├── (1) ReadFlyToken()
  ├── (2) for each tag:
  │     ├── parseFullyQualified(tag)              → repo, reference
  │     ├── resolveManifestDigest(token, ...)     → digest, HTTP
  │     └── deleteManifest(token, ...)            → HTTP
  └── (3) print "removed <tag>" per success
```

Every stage prints a one-line success marker (philosophy axis 3). Every stage that can fail returns an error with what / why / what-next; the outer `main` maps that to a non-zero exit.

---

## Failure modes

| Failure | Detected at | Behavior |
| --- | --- | --- |
| `docker` not on PATH | preflight | Exit 1 before any subcommand runs, with install hint. |
| Token missing from config | Seam 3 at subcommand entry | Exit 1 naming `token.flyio` and the `aa config` command to set it. |
| Dockerfile missing at build context | Seam 1 pre-flight (stat) | Exit 1 naming the path searched. Do not invoke `docker` at all. |
| `docker build` non-zero exit | Seam 1 | Exit with docker's exit code; `aa` prints a final line `docker build failed (exit N)` so the user knows which stage owns the failure. Output was already streamed to the terminal (ADR 5). |
| `docker login` rejects token (401/403) | Seam 1 (login subcommand) | Exit 1 naming `token.flyio` and "registry write access" — do not re-print docker's stderr verbatim; the user wants the interpretation, not the raw 401. |
| `docker push` fails mid-stream | Seam 1 | Propagate exit code; suggest retry in the final line. Do not auto-retry (YAGNI + reversibility — a partial push the user didn't see is worse than a visible failure). |
| Registry HTTP 401 / 403 | Seam 2 | Typed `errRegistryAuth` — message names the config key, not the status. |
| Registry HTTP 404 on `rm` | Seam 2 | Error naming the tag searched for. Fatal for that tag; in batch mode (ADR 3) the loop still exits non-zero at the end. |
| Registry HTTP 429 rate limit | Seam 2 | Fail loud with "registry rate-limited — retry in N seconds" using the `Retry-After` header if present. No in-process retry loop. |
| Network error (DNS, TCP, TLS) | Seam 2 | Surface the underlying error; suggest retry; exit 1. |
| `docker image inspect` says tag unknown on push | Seam 1 pre-push | Exit 1 with `no such local image: <tag> — run 'aa docker image build' first`. |

The pattern: catch at the narrowest boundary, translate to a message that names the failing component and the user's next action, never swallow. This is the existing convention in `v2/main.go`.

---

## Testing surface

Two externals, two fake strategies.

### The `docker` binary — fake via PATH-prepending

The harness (see `/workspaces/aa/v2/docs/test-harness.md`) already plants fake binaries in a temp `bin/` dir that is prepended to `$PATH` for the child `aa` process. This slug extends that: the fake `docker` binary records its argv, env (filtered), and stdin to a per-test log; emits declared stdout/stderr; exits with declared code.

- **Unit tests** (in-process, no subprocess): `resolveTag`, `parseFullyQualified`, `filterToAAOwned`, `dockerLoginArgv(token)` — all pure functions of inputs. These are the highest-leverage tests because tag derivation is the most error-prone surface.
- **Integration tests** (subprocess, harness fake): one test per verb, asserting on the exact argv sequence the fake observed. Example: `build ./myapi --tag v1` must invoke `docker build -t registry.fly.io/aa-apps/myapi:v1 ./myapi` and nothing else.

### The registry HTTP API — fake via `httptest.Server` + recordings

Same pattern as the existing Fly Machines API tests. `FLY_API_BASE` points the `ls`/`rm` paths at the harness's local server. Recordings are human-readable text, per the harness spec.

- **Integration tests**: `ls` against a canned two-repo / five-tag recording; `rm` against a canned "manifest exists, delete returns 202" recording; each auth-failure path has its own small recording.

### Hard-to-test things

- `docker build` output streaming verbatim to a real terminal is tested by asserting the fake's stdout bytes reach the test process's captured stdout. We cannot meaningfully test TTY interactivity — that's a design limit, not a bug.

---

## ADRs

### ADR 1: Default tag convention

**Date:** 2026-04-23
**Status:** accepted

#### Context

Intent flags this as open. `aa docker image build ./myapi` needs a default registry-qualified tag when `--tag` is not given. Options considered:

1. `registry.fly.io/aa-apps/<basename>:latest` — fixed single namespace.
2. `registry.fly.io/aa-apps-<user>/<basename>:latest` — per-user namespace derived from `$USER` or the Fly org.
3. Path-hash-based (`aa-apps/<sha256-of-abspath[:8]>-<basename>:latest`) — collision-free across projects.
4. No default — require `--tag`.

#### Decision

**Option 1.** Default tag is `registry.fly.io/aa-apps/<basename-of-path>:latest`, where basename is the final path segment of the build context (with `.` resolved to `$CWD`'s basename), lowercased and sanitized to `[a-z0-9-]`.

#### Justification

- **Axis 1 (clarity):** users read `registry.fly.io/aa-apps/myapi:latest` and know exactly what it is. Option 3 produces unreadable tags. Option 2 adds a user segment nobody asked to look up.
- **Axis 4 (low ceremony):** option 4 forces `--tag` on every invocation — hostile to the solo-dev quickstart loop in the docs.
- **YAGNI on collisions:** single-user tool, single backend, single org (intent: "One backend, one registry"). Two projects on the same account called `myapi` is a real risk; the user can pass `--tag` when they care. We don't optimize a tool for a problem its stated persona doesn't have yet.
- `aa-apps` already exists as the default Fly app name in `v2/main.go` (`defaultApp = "aa-apps"`). Reusing it keeps the registry namespace and the app namespace symmetric — one mental model, not two.

#### Consequences

- Two `myapi` directories pushed from the same token will overwrite each other's `:latest`. Users who hit this pass `--tag`. We document this in the tag-convention section of the user doc once this ADR lands.
- If a future multi-user feature appears, `aa-apps/` becomes the shared namespace and we revisit. This is a known future-change point, not a current bug.

---

### ADR 2: `ls` scope

**Date:** 2026-04-23
**Status:** accepted

#### Context

Intent flags this as open. Options:

1. Show only images under the `aa-apps/*` prefix (tool-owned).
2. Show every repository the token can see across the registry.

#### Decision

**Option 1 with a `--all` escape hatch: show `aa-apps/*` by default; `aa docker image ls --all` shows everything the token can see.**

#### Justification

- **Axis 1 (clarity):** default output matches the user's mental model of "what `aa` put there." Surprising inclusions (a teammate's `other-project/*`) would be worse than a small omission.
- **Axis 3 (observability):** default is the focused list; `--all` is the honest-about-the-token escape hatch when something is wrong. Together these satisfy both the focused-output and the honest-about-scope arguments in the intent.
- **Axis 4 (YAGNI):** one flag, no config knob, no labels to invent. We rely on the tag-prefix convention from ADR 1.

#### Consequences

- The tool defines `aa-apps/*` as its canonical prefix. If ADR 1 ever changes, ADR 2's default filter changes with it.
- An image pushed outside `aa` (e.g. manually via `docker push`) into some other namespace is invisible until the user passes `--all`. This is documented.

---

### ADR 3: `rm` semantics

**Date:** 2026-04-23
**Status:** accepted

#### Context

Intent flags batching and `--force` as open. Options:

1. Single-tag, no force.
2. Multi-tag, per-item failure reporting, no force (matches `aa rm` for machines).
3. Multi-tag, per-item, with `--force` required if any target is referenced by a running machine.

#### Decision

**Option 2.** Accept one or more tags. Each is processed independently; failure on one does not abort the others, but the overall exit code is non-zero if any failed. No `--force` flag in this slug.

#### Justification

- **Consistency with existing CLI:** `v2/machines.go:runLifecycle` already implements the "multi-arg, per-item, final exit reflects any failure" pattern. Users do not learn a second convention.
- **Axis 2 (evolvability):** adding `--force` later is trivial and non-breaking. Adding it now would require an HTTP probe that joins the machine-lifecycle slug to this one — a coupling we have no current evidence we need.
- **Reversibility:** `rm` is irreversible registry-side. We accept that risk in exchange for matching the existing verb's ergonomics; the user controls destruction with their own argv.

#### Consequences

- A user who deletes a tag that a paused machine references will break that machine's next start. This is the same risk the existing `docker push` + manual cleanup workflow already has; we document it.
- The `rm` failure loop behaves exactly like `machines.go`'s. The implementation of this loop is candidate for DRY-extraction, but only once a third instance appears (three-strike rule). Two is leave-alone.

---

### ADR 4: Registry login lifecycle

**Date:** 2026-04-23
**Status:** accepted

#### Context

When does `aa` call `docker login registry.fly.io -u x -p $FLY_API_TOKEN`? Options:

1. Before every `docker push`.
2. Once per process, cached in a process-local sentinel.
3. Once per shell session, cached on disk.
4. Never — write creds directly into `~/.docker/config.json`.

#### Decision

**Option 2: login once per `aa` process, before the first `push` that needs it, and skip on subsequent pushes within the same invocation.** The login is a transient in-process fact; no disk caching of our own.

#### Justification

- **Axis 4 (low ceremony):** option 1 issues redundant logins; option 3 invents a cross-invocation state file we don't need; option 4 persists credentials in a location the user doesn't control and couples us to Docker's config format (axis 2: evolvability).
- **Axis 5 (safety at boundary):** the boundary where token material crosses is the one `docker login` call; every other `docker` invocation runs without the token in env or argv.
- Real-world: today every `aa docker image push` is a single push per process. The "once per process" caching is free insurance if a future slug (`aa docker up`) does multiple pushes.

#### Consequences

- `~/.docker/config.json` is mutated as a side effect of `docker login` — we do not clean it up. The token it stores there is the Fly token; if the user runs `docker push registry.fly.io/...` manually later it will work. This is an acceptable leakage of state onto the user's machine (the token is theirs, the file is theirs) and is documented in the user doc.
- If we later decide to clean up after ourselves, we can add a deferred `docker logout registry.fly.io` at process end. Not in this slug.

---

### ADR 5: Build and push output handling

**Date:** 2026-04-23
**Status:** accepted

#### Context

Philosophy axis 3 (observability) wants the user to see what happened. Options for `docker build` / `docker push` output:

1. Stream verbatim to the user's stdout/stderr.
2. Filter and prefix every line with `[docker]`.
3. Capture silently; show only on failure or under `-v`.

#### Decision

**Option 1.** Forward `docker`'s stdout and stderr unchanged to the `aa` process's stdout and stderr. After `docker` exits, `aa` prints its own one-line summary (`built <tag>` or `pushed <tag>`) on a trailing line. `aa -v` adds pre/post context lines but never rewrites `docker`'s output.

#### Justification

- **Axis 1 (clarity):** docker's output is already the lingua franca developers recognize. Rewriting it forces every reader to learn a second format and obscures real errors.
- **Axis 3 (observability):** long builds must not go silent. Option 3 violates the "no silent 20-second blocks" rule outright.
- **Axis 4 (low ceremony):** zero code to filter or prefix. No parser to maintain when docker changes its output.
- Verbose mode "adds, never changes" (philosophy explicit): satisfied by having `aa`'s own lines appear before/after docker's, never interleaved.

#### Consequences

- `aa`'s stdout is **not** machine-parseable during the build/push phase — it contains docker's raw output. The one-line summary (`built <tag>`) is the only stable parser surface. This matches what the user doc already promises.
- Colors and progress bars from docker reach the terminal as-is. If stdout is a pipe, docker decides whether to suppress them; we don't interfere.

---

## Workstreams

The `docker-images` slug decomposes into three parallelizable workstreams plus one small cross-cutting task. Contract files are written first and locked; everything else codes against those signatures.

### Contract files (authored first, locked before any workstream starts)

- `v2/docker_image.go` — **stub only** initially: declares the package-level interface `dockerRunner` and the struct types used across stages (`imageRef`, `registryClient` shape). Locked before Wave 1.
- `v2/docker_image_runner.go` — the `execDockerRunner` implementation of `dockerRunner`. Small; lives with the interface.

Consuming seams already exist elsewhere:

- `ReadFlyToken()` — config-store slug provides. Depends-on, not owned-here.
- Harness fake `docker` binary — test-harness slug provides.

### Wave 1 — pure logic, no I/O (fully parallel)

These run in parallel; none depend on each other.

- **`tag-derivation`**
  - **Owns:** `v2/docker_image_tag.go`, `v2/docker_image_tag_test.go`.
  - **Consumes:** nothing.
  - **Produces:** `resolveTag(path string, explicit string) (string, error)`, `parseFullyQualified(s string) (imageRef, error)`, `sanitizeBasename(s string) string`.
  - **Fakes needed:** none.
  - **Tests:** unit tests + fuzz on `sanitizeBasename` and `parseFullyQualified` (they parse user/path input).

- **`argv-construction`**
  - **Owns:** `v2/docker_image_argv.go`, `v2/docker_image_argv_test.go`.
  - **Consumes:** `imageRef` from contract.
  - **Produces:** `buildArgv(ref, contextPath) []string`, `pushArgv(ref) []string`, `loginArgv(token) []string`, `inspectArgv(ref) []string`.
  - **Fakes needed:** none.
  - **Tests:** unit — every verb's argv asserted as an exact slice, including the case where a tag contains characters that must not become shell metacharacters (they never do because we don't use a shell — this is what argv construction buys us; the test pins the invariant).

- **`registry-http-client`**
  - **Owns:** `v2/docker_image_registry.go`, `v2/docker_image_registry_test.go`.
  - **Consumes:** `FLY_API_BASE` env for the base URL.
  - **Produces:** `listRepositories(ctx, token) ([]repository, error)`, `listTags(ctx, token, repo) ([]tagInfo, error)`, `resolveManifestDigest(ctx, token, repo, ref) (string, error)`, `deleteManifest(ctx, token, repo, digest) error`. Plus typed errors `errRegistryAuth`, `errRegistryNotFound`, `errRegistryRateLimited`.
  - **Fakes needed:** `httptest.Server` driven by recordings (harness provides the recording-playback helper).
  - **Tests:** integration against canned recordings for each endpoint + each typed-error path.

### Wave 2 — subprocess integration (parallel, starts once Wave 1 interfaces are frozen)

- **`docker-runner`**
  - **Owns:** `v2/docker_image_runner.go`, `v2/docker_image_runner_test.go`.
  - **Consumes:** argv-construction outputs; nothing else.
  - **Produces:** `dockerRunner` interface (contract file already declares the signature; this workstream lands the body) and `execDockerRunner` implementation with stdout/stderr passthrough per ADR 5.
  - **Fakes needed:** the harness's fake `docker` binary.
  - **Tests:** integration — one test per verb asserting the fake observed the exact argv slice, exit-code propagation, stdout pass-through.

### Wave 3 — CLI wiring (single writer per file, starts once Waves 1 and 2 are green)

- **`cli-verbs`**
  - **Owns:** `v2/docker_image_cmd.go` (the `runDockerImage(args []string)` dispatcher and the four verb handlers).
  - **Consumes:** everything above, plus `ReadFlyToken` from config-store.
  - **Produces:** `runDockerImage([]string)` — the entry point that `main.go` calls.
  - **Fakes needed:** none directly; integration tests here are e2e-harness flavored.
  - **Tests:** integration — one per verb, end-to-end through the harness (real compiled `aa` + fake `docker` + httptest registry). E2E coverage for the happy path of each verb belongs here; cross-verb stories (`build → push → ls` sequencing) belong to the e2e integration workstream described below.

### Shared / single-writer files

- `v2/main.go` — **owner:** `cli-verbs` workstream, **scheduled:** Wave 3. Only two edits:
  1. Add the `docker` case to the top-level `switch os.Args[1]`, dispatching to `runDockerImage`.
  2. Extend the usage string by one line.
  No other workstream touches `main.go` in this slug.

- `v2/preflight.go` (or wherever `preflight()` currently lives in `main.go`) — **owner:** `cli-verbs` workstream, **scheduled:** Wave 3. Append `docker` to the binary-check list. Append-only; no restructuring.

### Cross-workstream e2e integration

- **`e2e-docker-image`** — owner of `v2/test/e2e/docker_image_test.go` (or the harness's chosen path; test-harness slug decides layout). Lands **after** Wave 3. Exercises `build → push → ls → rm` end-to-end against the harness, including one auth-failure path and one rate-limit path.

### Known unavoidable conflict points

- `v2/main.go` — single writer (`cli-verbs`), late wave. No merge conflict if respected.
- Preflight binary list — same owner, append-only.

There is no file in this slug that two workstreams legitimately need to co-edit. If any workstream discovers it wants to touch a file outside its `Owns:` list, it raises and the breakdown is revised (per plan-implementation rules).

### LOC ceiling

The target per the philosophy is ~400 LOC / file, 800 ceiling. Splitting across `_tag.go`, `_argv.go`, `_registry.go`, `_runner.go`, `_cmd.go` keeps every file well under the comfort zone and lets each be rewritten from scratch in one Claude session (axis 2). If any file starts pushing past 400 LOC during implementation, we split it there — not preemptively.

---

## Assumptions baked in (call-outs for user review)

- The registry is the Fly machine registry, which speaks the Docker Registry HTTP API v2. The `ls` and `rm` HTTP shapes in this doc assume that; if the Fly registry uses a bespoke JSON shape instead, Wave 1's `registry-http-client` workstream needs a recording-discovery pass first.
- `FLY_API_BASE` today points at `https://api.machines.dev/v1` (see `v2/main.go:flyAPIBase`). The registry lives at `registry.fly.io` — a **different** host. Treating `FLY_API_BASE` as a single base URL for both Machines API and registry is wrong. **This slug introduces a second env var `AA_REGISTRY_BASE` (default `https://registry.fly.io`) for test-override of the registry host.** Called out explicitly because the task brief mentioned `FLY_API_BASE`; implementation follows the seam that actually exists.
- Config-store's `ReadFlyToken()` is assumed to exist with that exact signature. If the config-store slug lands a different signature, the contract file in this slug updates in one place.
