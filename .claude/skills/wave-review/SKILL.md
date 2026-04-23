---
name: wave-review
description: Review the output of a single wave of parallel workstreams — scope conformance, build state, red/green correctness, philosophy adherence, strict-mode discipline, and cross-workstream consistency. Use at the end of every wave during `integration-unit-tests` (step 5) and `implement` (step 6), before moving to the next wave. Also use when the user says "review the wave", "wave-review", "check wave N", "did the subagents stay in scope", or when spot-checking parallel work. A wave that has not been wave-reviewed has not landed.
---

# Wave Review

**The gate between one wave and the next.** Parallel dispatch is fast but unsupervised. Every wave closes with this review; no wave is considered done until it passes.

Pairs with `plan-implementation`'s workstream plan (defines what each workstream owns) and `docs/PHILOSOPHY.md` (defines what "good" looks like). This skill is the checklist that confirms both were honored.

## When to invoke

- At the end of every wave dispatch in `integration-unit-tests` (step 5) and `implement` (step 6). Not optional — it's part of closing the wave.
- Whenever the user asks for a wave check, a spot-check, or a consistency audit scoped to parallel work.

## Inputs

1. `docs/PHILOSOPHY.md` — the anti-patterns list and the Strict-mode path list. The review is largely mechanical: grep the new code for anti-patterns; verify strict-mode files carry strict-mode discipline.
2. `docs/architecture/<slug>.md` § "Workstreams" — the `Owns` list for each workstream in this wave. Scope conformance is verified against it.
3. The wave's actual output — the files the subagents (or single agent) produced. Identify via `git status` / `git diff` against the pre-wave tree.
4. The full build + test run output for the wave.

## The checklist

Run every item. A wave passes only if every item passes.

### 1. Scope conformance

- For each file touched in the wave, map it to a workstream in the plan.
- Every file must be in some workstream's `Owns` list, OR be a single-writer file (like `fakes_test.go`) documented as append-only.
- A file outside any workstream's scope is a violation. Revert the out-of-scope edits and re-dispatch the subagent with a corrected prompt.

Tooling: `git status --porcelain` then cross-reference with the Workstreams section.

### 2. Build state

- `go vet ./...` — clean (or the language's equivalent)
- `go build ./...` — clean
- `go test -count=1 -list '.*' ./...` — compiles and enumerates; no "cannot compile" errors

Any warning is treated as an error during review. Fix before closing the wave.

### 3. Red-state correctness (for test-writing waves)

Each new test file, run in isolation with `go test -count=1 -run <TestName> ./...`, must:

- **FAIL** (not pass).
- Fail for the right reason: the implementation being tested panics `"unimplemented"` or otherwise signals "not yet built".
- Fail **for each test function**, not just the whole file. Run a sample from each test file individually.

If a test passes unexpectedly, the test is probably asserting something trivial (tautology) or the behavior already exists. Investigate and fix.

If a test fails with a compile error or fake-framework error (not a stub-panic), the test has a bug. Fix before closing.

### 4. Green-state correctness (for implementation waves)

- Every previously-red test in the wave's workstreams is now green.
- No previously-green test regressed.
- Race detector: `go test -race ./...` still clean.
- No test had to be weakened, skipped, or marked `t.Skip` to pass — if it did, the implementation is incomplete.

### 5. Philosophy adherence

Grep the new code for the anti-patterns listed in `docs/PHILOSOPHY.md` § "Anti-patterns worth grepping for". A hit is a block:

- Abbreviated identifiers across package boundaries.
- Comments that restate code.
- Placeholder examples (`foo`, `bar`) in doc comments.
- `context.Background()` inside a request path.
- Goroutines without a documented exit path.
- "Temporary" code without a delete date.
- `time.Sleep` as a synchronization primitive in tests.
- Test code that modifies the real `$HOME`.

Also verify the five axes are visible:

- **Clarity for the LLM** — names are verbose; every file has a doc comment; every exported symbol has a realistic example (not `foo`/`bar`).
- **Evolvability** — no speculative interfaces; no framework-ish patterns; files are rewriteable in one session.
- **Observability** — errors name what failed and what to try; success paths are not silent.
- **Low ceremony** — no tests for code that doesn't exist; no ADR for trivial choices; no unused files.
- **Safety-at-boundary** — applied only in `PHILOSOPHY.md` Strict-mode paths; not sprayed everywhere.

### 6. Strict-mode discipline

For every file in the wave that falls under `PHILOSOPHY.md` § Strict mode:

- Test cases at boundaries carry comments citing the specific strict-mode invariant they enforce.
- Input validation is visible: explicit type narrowing, explicit field-level errors, `DisallowUnknownFields` on JSON where applicable.
- Shell command composition never interpolates user-provided strings into a shell line — argv construction only.
- Error paths never swallow. Every branch returns an actionable error or a validated value.
- Timeouts on every I/O; no `context.Background()` in request paths.

If a strict-mode file is missing this discipline, the wave does NOT close until it's added.

### 7. Contract match

For each test file, verify it calls the contract file signatures verbatim — same function names, same argument types, same return shapes. No test invents a function that doesn't exist in a contract file (this causes silent drift where the implementation has to match invented surface later).

Quick check: `grep -E 'func (Test|Fuzz)' <test-file>` then verify every non-helper function in that test file that isn't a Test/Fuzz is either a helper within the file or references an exported symbol from a contract file.

### 8. Cross-workstream consistency

The wave was written by multiple agents. Before closing, verify the suite feels like one team wrote it:

- **Naming** — same concept has same name across files (e.g., "hostile hostname" test should use consistent examples across ssh-runner and egress-controller; a test named `TestX_BehaviorName` in one file uses the same convention in another).
- **Helpers** — duplicate helpers across files should be consolidated into `fakes_test.go` or a shared helpers file (append-only). If two workstreams invented the same helper with different names, flag it.
- **Test shape** — docstring style, subtest convention, assertion helper usage should match across the wave.
- **Panic-recovery convention** — if some workstreams added a recover helper and others didn't, that's inconsistency; either the helper is standard (add everywhere) or not needed (remove). **Decide and apply uniformly.**

## Output

A short report, at the end of the review, in this format:

```
WAVE <N> REVIEW: <feature-slug>
DATE: <YYYY-MM-DD>
RESULT: pass | needs-rework

WORKSTREAMS REVIEWED
  - <name> (owns: <paths>)
  - <name> (owns: <paths>)

SCOPE         ✓ / ✗   <notes if any violations>
BUILD         ✓ / ✗
RED/GREEN     ✓ / ✗
PHILOSOPHY    ✓ / ✗   <anti-patterns found, if any>
STRICT MODE   ✓ / ✗   <applicable files and whether discipline is visible>
CONTRACTS     ✓ / ✗
CONSISTENCY   ✓ / ✗   <drift items, if any>

REMEDIATION (if needs-rework)
  - <action> (workstream: <name>)
```

Emit this report to the user. If PASS, proceed to the next wave. If NEEDS-REWORK, stop the orchestrator, fix the listed items (re-dispatch the guilty subagent if scope-level, hand-edit if local), then re-run this review.

## Rules

- **A wave does not close until the review passes.** The orchestrator stops between waves until this skill returns PASS.
- **Do not accumulate drift across waves.** A consistency issue in wave 1 that gets copied to wave 2 is now twice as expensive to fix. Catch it at wave-1 close.
- **Be specific in remediation.** "Fix the lint" is not specific enough; "fix the `slices.Contains` modernization in ssh_runner_test.go:286" is.
- **The review itself is cheap.** A wave of 4 workstreams takes under five minutes to review — far less than debugging a cross-wave drift weeks later.
- **If you find yourself repeatedly catching the same issue across waves**, promote it: add an explicit instruction to the next wave's subagent prompts, or add a new anti-pattern to `PHILOSOPHY.md`.

## Handoff

- PASS → the orchestrator moves to the next wave (or the next step, if this was the last wave).
- NEEDS-REWORK → the orchestrator stays on the current wave until the remediation items are resolved and the review re-runs as PASS.
