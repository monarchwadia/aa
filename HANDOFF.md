# HANDOFF — aa repo

**Last touched:** 2026-04-23
**Active workspace:** `v2/`
**Current state:** `/code-write` workflow complete end-to-end through phase 7 (review-stack). All tests green. Committed at `dbb8668`.

## Where we are

The v2 rewrite is **shippable** but has a list of documented drift between docs/tests/code that we chose to defer rather than block on. The full per-package picture lives in `v2/STATUS.readme` — read that first.

`go test -count=1 ./...` from `v2/` is green across every package. Three e2e
journey tests are explicitly `t.Skip()`-ed with TODOs (they need hand-crafted
HTTP snapshots).

## What we just did

Worked through `/code-write` resumption:

1. Fixed an import-cycle leftover (`v2/dockerimage/argv.go` had been left with duplicate concatenated content from a prior edit; truncated to first 29 valid lines).
2. **Wave 3 implement** (delegated to a sub-agent, then verified):
   - `dockerimage.Run` dispatcher (build/push/ls/rm), login memoized once-per-process via `sync.Map`+`sync.Once` per ADR-4.
   - `dockerup.Label` (sha256(lower(absPath))[:12], key `aa.up-id`) and `dockerup.Run` four-stage orchestrator (build→push→spawn→attach) with `--force` destroy ordered after push success per ADR-4 and best-effort attach cleanup per ADR-5.
   - `main.go` `aa docker {image,up}` dispatch wired to real configstore/registry/flyclient/extbin.
   - `v2/imageref/` extracted as the leaf package that breaks the original `registry ↔ dockerimage` cycle.
   - e2e harness scaffolding (`tests/e2e/main_test.go`, empty config-store snapshots).
3. **Phase 7 review-stack** (delegated to code-review sub-agent). Review surfaced ~10 drift items between layers. Fixed the high-leverage ones inline:
   - `main.go` now calls `cfg.ResolveAPIBase()` (was bypassing the Reader and reading `FLY_API_BASE` env directly — file-layer `endpoints.api` was being silently ignored, a real bug).
   - `dockerup` re-run-refusal and no-Dockerfile errors reworded to docs-mandated form.
   - `dockerimage` build/push emit docs-mandated `built <tag>` / `pushed <tag>` success lines.

## What to do next (ordered by leverage)

### Trivial cleanup (knock out as one batch)

1. **`v2/docs/config-store.md` sync** — strip "proposed, not yet specified" from `--remove`; document `--show-secrets`, `<set>` masking, key-naming convention. Code already implements all three; this is pure doc.
2. **`v2/README.md` perf claim** — soften "within a minute" / "sub-minute for a trivial image" to honest wording, or land a benchmark.
3. **`v2/dockerimage/cmd.go` `runLs`** — print docs-mandated columns (tag, size or digest, age) using `registry.Image.{Tag,Digest,SizeB,PushedAt}` instead of just `img.Tag`.
4. **`v2/docs/docker-images.md` `rm` paragraph** — strike the "until intent resolves, single-tag, fail-loud" placeholder; integration test ADR-3 enforces multi-tag continue-on-error and code matches. Doc is the lone outlier.
5. **`v2/dockerup/dockerup.go` attach error** — wrap the raw `flyctl ssh` transport error with the docs-mandated "shell not yet reachable" diagnostic when applicable.

### Moderate (one focused session)

6. **dockerup stage observability** — wrap each stage's stdout/stderr with a prefixing writer (`[build]`, `[push]`, `[spawn]`, `[attach]`) and emit `error: <stage> stage failed — …` summary on each return path. Mechanical but touches all four stages in `dockerup.Run`.

### Bigger (half-day-ish each)

7. **dockerup spawn/attach parity** — currently `dockerup.Run` calls `Fly.Create` directly without `WaitStarted` and runs `flyctl ssh` once with no retry. Docs claim parity with `aa machine spawn`/`aa machine attach`. Refactor so dockerup shares the spawn+wait+attach helpers in `v2/machine.go` (extract a helper). Add unit tests for the wait/retry behavior under dockerup.
8. **e2e journey snapshots** — three `t.Skip`s in `v2/tests/e2e/`:
   - `TestDockerImagesJourney`
   - `TestDockerUpJourney`
   - `TestMachineLifecycleJourney`
   Two routes:
   - (a) hand-write `tests/testdata/snapshots/*.json` fixtures (tedious; exact paths/headers/bodies).
   - (b) implement `AA_TEST_RECORD=1` record mode in `v2/testhelpers/sandbox.go` (the error message already hints at it; the impl is missing), then record against a real backend once and commit the JSON.
   Route (b) pays for itself if more journeys are added later.

### Workflow housekeeping

- Once items 1–6 land, re-run `review-stack` to confirm no new drift snuck in. The skill spec lives at `.claude/skills/review-stack/SKILL.md`.
- After items 7–8 land, the v2 slug set is "fully done" by the workflow's own definition — note the pass in each `v2/docs/architecture/<slug>.md`.

## Important context to keep in mind

- **Stdlib only.** No third-party Go deps anywhere in `v2/`. Enforced.
- **Philosophy:** `v1/docs/PHILOSOPHY.md` — Clarity > Evolvability > Observability > Low-ceremony > Safety-at-boundary.
- **Label contract pinned (2026-04-23 amendment):** key `aa.up-id`, value `hex(sha256(lower(absPath)))[:12]`. Constant lives in `v2/dockerup/dockerup.go` as `LabelKey`.
- **`testhelpers/sandbox.go` is strict:** unconsumed snapshot entries AND drifted requests both fail in `t.Cleanup`. Empty `[]` snapshots are only valid for tests that make zero HTTP calls.
- **Login lifecycle (ADR-4 docker-images):** `dockerimage.Run` logins once per `(DockerRunner pointer, token, host)` triple via package-level `sync.Map` of `*sync.Once`. Failed logins drop from cache so retries can re-attempt.
- **--force timing (ADR-4 docker-up):** destroy old machine AFTER push success, BEFORE Create. Build/push failures preserve the user's working machine. Don't reorder.
- **Attach failure cleanup (ADR-5 docker-up):** best-effort destroy of the new machine; image is RETAINED in the registry. Asymmetric on purpose.
- **argv.go gotcha:** a previous session left duplicated old+new content concatenated in `dockerimage/argv.go`. After any partial edit in a long-running session, view the file end to confirm it terminates cleanly.

## Quick commands

```sh
cd v2
go build ./...
go test -count=1 ./...
go test ./dockerup/...   # one package
```

## Files most likely to need edits next

- `v2/dockerup/dockerup.go` (items 5, 6, 7)
- `v2/dockerimage/cmd.go` (item 3)
- `v2/docs/config-store.md` (item 1)
- `v2/docs/docker-images.md` (item 4)
- `v2/README.md` (item 2)
- `v2/testhelpers/sandbox.go` (item 8b)
- `v2/tests/testdata/snapshots/` (item 8a)
