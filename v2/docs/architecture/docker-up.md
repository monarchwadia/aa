# `aa docker up` — Architecture Notes

**Slug:** `docker-up`
**Status:** proposed
**Date:** 2026-04-23
**Composes:** `machine-lifecycle`, `docker-images`, `config-store`

This slug is glue. The hard work lives in the two sibling primitive slugs; `docker-up` only sequences their exported verbs and handles the cross-cutting concerns that only the composer can see: identity, `--force` replacement, and the asymmetric cleanup contract resolved in intent.

---

## Seams and boundaries

`docker-up` consumes three interfaces, all owned by other slugs. Nothing in this slug reaches below these seams — we do not shell out to `docker` or `flyctl` here, we call the sibling packages which do.

### From `docker-images` (owned by that slug)

```go
// DockerImages is the build+push surface this slug depends on.
// The real implementation lives in v2/docker_images.go (docker-images slug).
type DockerImages interface {
    // Build runs the local container-build tool against the given build
    // context directory and produces a locally-tagged image. The returned
    // tag is fully qualified for the configured backend registry.
    Build(ctx context.Context, buildContextPath string, tag string) (builtImageTag string, err error)

    // Push publishes a locally-built tag to the backend's private registry.
    // On success the tag is retrievable by any machine the backend spawns.
    Push(ctx context.Context, tag string) error
}
```

### From `machine-lifecycle` (owned by that slug)

```go
// MachineLifecycle is the spawn+attach+destroy surface this slug depends on.
// The real implementation lives in v2/machines.go (machine-lifecycle slug).
type MachineLifecycle interface {
    // Spawn provisions a cloud instance running the given image tag, waits
    // for backend-ready, then waits for shell-surface-ready. The returned
    // MachineID is the opaque handle used by Attach/Destroy/Find.
    Spawn(ctx context.Context, spec SpawnSpec) (MachineID, error)

    // Attach opens an interactive shell inside the running container on the
    // given machine and blocks until the user exits. Returns nil on clean
    // exit; returns non-nil if the shell surface was never reachable after
    // the retry policy documented in machine-lifecycle.
    Attach(ctx context.Context, id MachineID) error

    // Destroy tears down a machine. Used by docker-up on attach failure and
    // on --force replacement. Best-effort; see Failure modes.
    Destroy(ctx context.Context, id MachineID) error

    // FindByLabel looks up a machine tagged with the given docker-up identity
    // label. Returns (zero, false, nil) if none exists. Used for re-run
    // refusal and for --force replacement.
    FindByLabel(ctx context.Context, label string) (MachineID, bool, error)
}

type SpawnSpec struct {
    ImageTag string            // required, fully qualified
    Labels   map[string]string // docker-up sets the identity label here
}
```

### From `config-store` (reader only)

```go
// ConfigReader is the read-only surface this slug uses. docker-up never writes
// config; writes are the config-store slug's responsibility.
type ConfigReader interface {
    // BackendToken returns the stored backend credential used for both push
    // (registry) and spawn (backend API).
    BackendToken() (string, error)
    // RegistryNamespace returns the backend-registry namespace to prefix tags with.
    RegistryNamespace() (string, error)
}
```

**What must not leak across these seams:**
- `docker-up` never knows about `docker` or `flyctl` directly — only through `DockerImages` / `MachineLifecycle`.
- The sibling slugs never know about directory paths, `--force`, or the four-stage chain.
- The identity label is defined *in this slug* and passed to `machine-lifecycle` as an opaque string; `machine-lifecycle` stores it and queries it but does not interpret it.

---

## Data flow — the four stages

The pipeline is strictly linear. Each stage consumes the previous stage's output and produces the next stage's input. No backtracking, no retries at this layer (per-stage retries, where they exist, live in the sibling slugs — e.g. attach retry).

```
 args(path, --force) ──► resolve ──► build ──► push ──► spawn ──► attach
                           │          │         │         │         │
                         Identity   Image    Published  Machine   Interactive
                         + preflight tag      tag        id       shell session
```

### Stage 0: resolve (in-process, no side effects beyond reads)

- **Input:** CLI args (`<path>`, `--force`), `ConfigReader`.
- **Work:**
  1. Verify `<path>` exists and contains a `Dockerfile` (exit 2 with the message from `docs/docker-up.md` if not).
  2. Compute the identity label from `<path>` (see ADR-1).
  3. Call `MachineLifecycle.FindByLabel(label)`. If found and `!--force`, refuse (exit 2). If found and `--force`, record the old machine id for destruction in stage 3 (see ADR-4).
  4. Compose the fully-qualified image tag from `ConfigReader.RegistryNamespace()` + the identity label.
- **Output:** `resolved{ label, imageTag, buildContextPath, oldMachineID (optional) }`.
- **Failure:** refused re-run → exit 2, no side effects.

### Stage 1: build

- **Input:** `resolved.buildContextPath`, `resolved.imageTag`.
- **Work:** `DockerImages.Build(ctx, buildContextPath, imageTag)`.
- **Output:** built image present in the local build-tool's cache, tagged `imageTag`.
- **Observability:** `[build]  building image from <path>/Dockerfile … done (Ns)`.
- **Failure:** print `[build] FAILED: …`, exit 1. Nothing to clean up.

### Stage 2: push

- **Input:** `resolved.imageTag` (now built).
- **Work:** `DockerImages.Push(ctx, imageTag)`.
- **Output:** image retrievable from the backend's private registry at `imageTag`.
- **Observability:** `[push]   pushing <imageTag> … done (Ns)`.
- **Failure:** print `[push] FAILED: …`, exit 1. Nothing to clean up (local built image stays in build-tool cache; that's normal).

### Stage 3: spawn

- **Input:** `resolved.imageTag`, `resolved.label`, `resolved.oldMachineID (optional)`.
- **Work:**
  1. If `resolved.oldMachineID` is set (from `--force`): `MachineLifecycle.Destroy(ctx, oldMachineID)` (see ADR-4 for the timing rationale).
  2. `MachineLifecycle.Spawn(ctx, SpawnSpec{ImageTag: imageTag, Labels: {identityLabelKey: label}})`.
- **Output:** `machineID`.
- **Observability:** `[spawn]  provisioning instance … backend-ready` then `[spawn]  waiting for shell … shell-ready (machine id: <id>)`. (Both lines are actually emitted *by* `MachineLifecycle.Spawn` — per docs; we just don't swallow them.)
- **Failure:** print `[spawn] FAILED: …`, exit 1. **Image is retained** in the registry (resolved intent). **No machine cleanup needed** — if Spawn failed, by its contract it either returns a usable machine or no machine at all. If `--force` destroy already ran and Spawn then failed, the user is left with neither old nor new, which is correct behavior for an explicit replacement.

### Stage 4: attach

- **Input:** `machineID` from stage 3.
- **Work:** `MachineLifecycle.Attach(ctx, machineID)`.
- **Output:** user is in a shell; on clean exit, control returns with the machine still running (detached identity per resolved intent).
- **Observability:** `[attach] attaching to <id> …` then the live shell.
- **Failure:** attach (after its own retry budget) returned non-nil. **Destroy the machine** before exiting (resolved intent); see Failure modes for the contract.

---

## Failure modes (per stage, with cleanup contract)

| Stage   | On failure, clean up what?                                 | Contract                                                                                                                                           |
|---------|------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------|
| resolve | Nothing (no side effects yet).                             | Hard-fail with a message naming the cause (no Dockerfile / refused re-run).                                                                         |
| build   | Nothing.                                                   | Hard-fail. Print build tool's error under `[build] FAILED`.                                                                                          |
| push    | Nothing remote. (Local build-tool cache retained — normal.) | Hard-fail. Image tag is unreachable from the backend, so next stage cannot run.                                                                     |
| spawn   | **Keep the pushed image** (resolved intent, asymmetric).   | Hard-fail. Print the retained tag in the error so the next attempt's user knows caching will help.                                                  |
| attach  | **Destroy the machine** before exiting (resolved intent).  | Best-effort destroy (see ADR-5). On destroy failure, print the machine id so the user can `aa machine rm` it. Attach error itself is still returned. |

**--force cascade (see ADR-4):** the pre-existing machine is destroyed **between push success and spawn start**, not before build. Rationale in the ADR.

---

## State and lifecycle

- The pipeline has no backtracking. Each stage either produces its output and transitions forward, or fails and terminates the command.
- There is **no persistent in-process state** beyond the `resolved` struct plus the current `machineID`. Nothing is written to disk by this slug (see ADR-1 for why identity does not need a local lock file).
- The post-attach state (machine still running after shell exit) is entirely owned by `machine-lifecycle`. This slug does not track it.
- Signals during attach (Ctrl-C, terminal resize) are handled by `MachineLifecycle.Attach` per its own contract; `docker-up` does not install signal handlers.
- Concurrency: none. The four stages run sequentially in a single goroutine. There is one `context.Context` threaded through all stages; cancellation of it aborts the current stage via the sibling contracts.

---

## Testing surface

This slug is almost pure composition, so the test pyramid is inverted compared to a leaf slug.

- **Unit tests — small.** The only genuine unit here is the resolver: identity-label derivation from path, image-tag composition, and the re-run refusal logic. Target file: `v2/docker_up_test.go` (table-driven).
- **Integration tests — most of the surface.** Drive `docker_up.go` against **fakes** of `DockerImages`, `MachineLifecycle`, and `ConfigReader`. Cover:
  - Happy path (all four stages succeed).
  - Each stage's failure, asserting the cleanup contract (image kept vs. machine destroyed).
  - `--force` with pre-existing machine: assert destroy ordering (after push, before spawn — ADR-4).
  - `--force` absent with pre-existing machine: assert refusal and exit code 2.
  - Attach failure with destroy-failure: assert the machine id is surfaced to the user and exit is still non-zero.
- **E2E — a single happy path.** One real test that runs `aa docker up <fixture-dir>` against a stub backend and asserts the user lands in a shell. Owned by the e2e workstream; not duplicated at integration level.

The sibling slugs carry their own unit/integration coverage of `docker`/`flyctl` invocations. We do not re-test those here; our fakes assume the sibling interfaces behave as documented.

---

## ADRs

### ADR-1: Identity from path — canonical absolute directory path, hashed

**Status:** proposed. **Date:** 2026-04-23.

**Context.** We need a scheme by which `aa docker up ./myapi` can, on a second run, locate the image tag and machine it produced the first time so `--force` can replace them. Intent lists: basename, content hash, per-directory metadata file, random ULID in a lock file.

**Options considered.**

1. **Directory basename (`myapi`).** Pro: human-readable, short tags. Con: two projects named `myapi` in different parent directories collide silently. Fails clarity (axis 1): the identity is lying about what it identifies.
2. **Content hash of Dockerfile + context.** Pro: truly content-addressed. Con: every source edit changes identity, which breaks `--force` (the "same directory" no longer resolves to the same image/machine). Fails intent directly — re-run refusal is supposed to key on *directory*, not content.
3. **Per-directory metadata file (`.aa/up.json`) storing a ULID.** Pro: stable, user can delete the file to reset. Con: writes to the user's working tree. Every invocation from a clean checkout (CI, fresh clone) creates a new identity, which again breaks `--force`. Also adds disk-write ceremony (axis 4).
4. **Hash of the canonical absolute path of `<path>`.** Pro: stable across invocations from the same directory; different for different directories; no disk writes; no collision between two `myapi` directories at different paths; reader can reproduce it from the CLI args alone. Con: not human-readable in the tag, but intent and docs already tell users to treat the tag as opaque.

**Decision.** Option 4. The identity label is `aa-up-<first-12-hex-of-sha256(absPath(<path>))>`. Machine identity label and image tag both derive from this. No local state file.

**Axes walked.** Clarity (1): option 4 is the only one whose identity honestly names what we said we'd key on ("directory"). Evolvability (2): no on-disk scheme to migrate. Low ceremony (4): zero files written. Wins on 1, 2, and 4; ties elsewhere.

**Consequences.** Two users on two laptops with the same path (`/home/alice/myapi` vs `/home/bob/myapi`) hashing the same under the same backend namespace would collide — mitigated by including the backend-registry namespace (per-user) in the full image tag, so the hash collision zone is within one user's account. Moving the project directory on disk *is* a new identity; documented behavior, matches the mental model ("it's tied to *this* directory").

---

### ADR-2: Build context is `<path>` verbatim

**Status:** proposed. **Date:** 2026-04-23.

**Context.** Intent open question: is the context strictly `<path>`, or is there an implicit project-root / ignore-file notion?

**Options considered.**

1. **`<path>` verbatim, pass to `docker build <path>`.** The underlying build tool honors `.dockerignore` if the user placed one at `<path>`. No additional filtering by `aa`.
2. **Walk upward from `<path>` to find a project root marker (`.git`, `go.mod`, etc.) and use that as the context.** Ships more content, potentially much more, implicitly.
3. **Introduce an `aa`-level ignore file (`.aaignore`).** A second ignore mechanism alongside `.dockerignore`.

**Decision.** Option 1. Build context is exactly the resolved absolute `<path>`.

**Axes walked.** Clarity (1): what-you-see-is-what-gets-shipped; the path in the command IS the context. Low ceremony (4): zero new concepts introduced by `aa`. YAGNI: no user has asked for project-root inference.

**Consequences.** Users who want to ship additional files place them under `<path>` or use Docker's existing `.dockerignore` mechanism. If real workflows demand project-root inference later, it can be added as a flag without breaking the default.

---

### ADR-3: Linear orchestration, no stage engine

**Status:** proposed. **Date:** 2026-04-23.

**Context.** The chain is four stages. We could build a small `Stage` interface with `Run/Cleanup` methods and a runner that iterates, or we could write four sequential function calls.

**Decision.** Four sequential function calls inside one `runDockerUp` function. No stage engine, no `Stage` interface.

**Axes walked.** Clarity (1): a straight-line function is the most obvious possible code for a straight-line pipeline. Low ceremony (4): explicitly pushes toward this. YAGNI: no second consumer of a `Stage` abstraction exists or is planned. Three-strike rule (evolvability): we have one chain; inline it.

**Consequences.** If a second pipeline-shaped feature emerges later, we extract then, with the benefit of two concrete examples. Not now.

---

### ADR-4: `--force` destroys the old machine AFTER push succeeds, BEFORE spawn starts

**Status:** proposed. **Date:** 2026-04-23.

**Context.** On `--force`, we know there's an existing machine to replace. When do we destroy it?

**Options considered.**

1. **Before stage 1 (build).** Pro: simplest. Con: if build or push fails, the user has lost their previous working machine and gained nothing. Maximum downtime; bad for interactive-work personas.
2. **After stage 4 (attach) succeeds.** Pro: zero downtime. Con: requires running two machines simultaneously under the same identity label, which means the identity invariant ("one machine per directory") is violated during the overlap window — breaking the guarantee `FindByLabel` relies on. Also doubles cost during the overlap.
3. **Between push success and spawn start.** Build can fail harmlessly (old machine still running, user re-runs without ever noticing). Push can fail harmlessly. Only once we have a good image do we commit to replacement. Spawn failure does leave the user with no machine, but that's the correct tradeoff for an explicit `--force` replacement.

**Decision.** Option 3.

**Axes walked.** Observability (3): the user explicitly typed `--force`, so the "nothing running" state after a spawn failure is expected, not surprising. Evolvability (2): doesn't require changing the one-machine-per-label invariant. Ceremony (4): still a single sequential flow.

**Consequences.** Documented behavior: `--force` preserves the old machine through build and push; destroys it just before attempting the new spawn. If the new spawn fails, the user has no machine under that identity and the retained image in the registry; re-running `aa docker up` (without `--force`, since no machine exists under the label anymore) will proceed.

---

### ADR-5: Attach-failure destroy is best-effort

**Status:** proposed. **Date:** 2026-04-23.

**Context.** Intent says destroy the machine on attach failure. What if the destroy itself fails?

**Options considered.**

1. **Hard-fail the destroy.** Pro: cleanest contract. Con: if both attach *and* destroy fail, the user's terminal sees a destroy error, which hides the original attach error — the actually-useful information.
2. **Best-effort destroy with a prominent warning.** Pro: the user gets the original attach error (what they wanted to know) *and* the machine id they need to clean up by hand. Observability axis wins cleanly.

**Decision.** Option 2. Attach-failure triggers a destroy attempt; if destroy fails, the error message includes the machine id and an instruction to run `aa machine rm <id>`. The original attach failure is the primary error returned; the destroy failure is a nested detail.

**Axes walked.** Observability (3): the original error is preserved; the user knows what to do next. Safety-at-boundary (5): we're not in strict mode here — this is user-facing command output, not hostile-input parsing. Low ceremony (4): one extra warning line, no transactional bookkeeping.

**Consequences.** Documented in the failure matrix in `docs/docker-up.md`. Integration tests assert this exact shape.

---

## Workstreams

### Contract files (locked before any workstream starts)

These are the files that pin the interfaces `docker-up` consumes. They are authored by the sibling slugs' architecture waves (wave 2), but `docker-up`'s workstream schedules against their signatures — not their bodies.

- `v2/docker_images.go` — exports `Build`, `Push`. Owned by `docker-images` slug.
- `v2/machines.go` — exports `Spawn`, `Attach`, `Destroy`, `FindByLabel`, plus `MachineID`, `SpawnSpec`. Owned by `machine-lifecycle` slug.
- `v2/config.go` (or similar) — exports `BackendToken`, `RegistryNamespace`. Owned by `config-store` slug.

`docker-up` does **not** define its own contract file. It consumes the sibling-slug exports directly — no intermediate interface package. Per philosophy axis 2 (thin seams) and axis 4 (no speculative interfaces): with one consumer and one implementation each, we inline.

### Wave placement: **Wave 3** (not 2)

Rationale: this slug is composition-only. Its tests are overwhelmingly integration tests that drive the real sibling interfaces (with fakes injected at the seam). Those tests cannot be meaningfully written or made green until the sibling *interfaces are frozen* — and in practice, writing confident integration fakes needs the sibling doc comments, error shapes, and label semantics all settled, which only wave-2 bodies (not just stubs) reliably produce.

Choosing wave 3 over "wave 2 with frozen interfaces" also reflects ADR-3: because we have no stage engine, there's almost no work in this slug that is independent of the sibling behavior. Starting later costs us little calendar time and saves rework.

### Workstreams in this wave

**`docker-up`** (this slug, the only workstream in wave 3 from this doc)

- **Owns:** `v2/docker_up.go`, `v2/docker_up_test.go`.
- **Consumes:**
  - `DockerImages` (from `v2/docker_images.go`, wave 2).
  - `MachineLifecycle` (from `v2/machines.go`, wave 2).
  - `ConfigReader` (from `v2/config.go`, wave 2).
- **Produces:** the `aa docker up` command handler wired into `v2/main.go` (see shared file note below). No new exported types beyond what `main.go` needs to call in.
- **Fakes needed:**
  - `fakeDockerImages` — records Build/Push calls, injectable failure per method.
  - `fakeMachineLifecycle` — records Spawn/Attach/Destroy/FindByLabel, injectable failure per method, fake label-store for `FindByLabel` round-trips.
  - `fakeConfigReader` — returns canned token + namespace.
  - All three fakes live in `v2/docker_up_test.go` (no separate `testing/` package; axis 4).
- **Tests:**
  - `v2/docker_up_test.go` — unit (resolver) + integration (all four-stage paths, `--force` ordering, attach-destroy contract).
  - E2E lives in the integration workstream, not here.

### Shared / single-writer files

- **`v2/main.go`** — the CLI entry point. Every slug in the tool adds its command wiring here. Single owner: whoever merges the wave 3 integration, appending the `docker up` subcommand registration. Scheduled at the tail of wave 3. Merge strategy: append-only per-slug blocks separated by a comment header (`// --- docker-up ---`), so parallel additions by other slugs in the same wave conflict on whitespace only.
- **`v2/preflight.go`** — already contains `docker` and `flyctl` PATH checks per the task note. `docker-up` needs to call the existing preflight check before stage 0; no new preflight entries required, so no edit needed here. If an edit becomes required, it is also single-writer (append-only).

### Known unavoidable conflict points

- `v2/main.go` — covered above with append-only merge strategy.
- No others identified. `v2/docker_up.go` is a single new file owned by this workstream end to end.

---

## Summary (for handoff)

1. **Naming scheme:** identity label is `aa-up-<first-12-hex-of-sha256(absPath(<path>))>`; image tag and machine-identity label both derive from it; no local state file.
2. **Build-context decision:** `<path>` verbatim, no project-root inference, no `aa`-level ignore file.
3. **`--force` cascade order:** destroy old machine **between push success and spawn start**, so a failed build or push never costs the user their working machine.
4. **Workstream wave:** wave 3. Depends on `machine-lifecycle` and `docker-images` wave-2 bodies being in place, not just frozen interfaces, because this slug is almost entirely integration-tested against those siblings.
5. **Biggest risk:** the sibling slugs drift on label/identity semantics — specifically, whether `MachineLifecycle.FindByLabel` is guaranteed to round-trip the label set in `SpawnSpec.Labels`. If that invariant wobbles, `--force` detection silently breaks. Pin this in `machine-lifecycle`'s architecture doc and integration-test it from both sides.

---

## Amendments — 2026-04-23

### Label contract pinned: `aa.up-id`

The machine's identity label is now a concrete contract:

- **Key**: `aa.up-id` (literal; not user-configurable).
- **Value**: `sha256(absolutePath(<path>))[:12]` — first 12 hex chars of the lowercase absolute path's SHA-256.
- **Written** by `docker-up` in the `SpawnSpec.Labels` map (see `machine-lifecycle` arch amendment).
- **Read** by `docker-up` via `MachineLifecycle.FindByLabel("aa.up-id", <value>)` when deciding whether to refuse, replace (`--force`), or spawn fresh.

If `FindByLabel` returns more than one machine for this label, `docker-up` destroys all matches before proceeding (under `--force`) or refuses and lists all matches (without `--force`). The multi-match case should not happen under normal operation — it indicates a prior failed replace — and treating it deterministically prevents orphaned instances.

### No local state file

The sha256 identity is computed from `<path>` alone on every invocation. There is no local file, lock, or metadata written outside the Fly backend. Removing an instance via `aa machine rm <id>` (or manually in the Fly dashboard) is sufficient to "reset" a directory's binding — the next `aa docker up <path>` will find zero matches and spawn fresh.

### Base-URL seams

`docker-up` consumes two base-URL seams (API and registry) via the `config-store` resolver helpers added in its amendment. It does not read env vars directly.
