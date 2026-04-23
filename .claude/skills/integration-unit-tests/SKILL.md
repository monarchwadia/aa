---
name: integration-unit-tests
description: Write integration and unit tests that describe what each unit does, cover all edge cases, and fuzz inputs where possible. Use after the `e2e-tests` skill, or whenever the user says "write integration tests", "write unit tests", "cover edge cases", "add fuzz tests", or "get red tests in place below the e2e layer". All tests written here must be red before moving on. Each test must be runnable independently and atomically. This is step 5 of the code-write workflow.
---

# Integration & Unit Tests

**Step 5 of 7. Fourth-highest (integration) and fifth-highest (unit) precedence.** These tests are the functional specification of each piece — the building blocks that make the e2e journeys work.

## Purpose

Below the e2e journeys, every unit and every integration point needs its own failing tests before code is written. These are **functional** tests: they describe what the unit *does*, not how it does it. They cover the happy path, every edge case, and fuzzed inputs.

Red/green discipline still applies: all tests are written first, all are red, then the `implement` step drives them to green.

## Integration vs unit — the distinction

- **Integration tests** cover the seams between units: a command talking to a config loader, a config loader talking to a secret resolver, a backend talking to `ssh`. They use real collaborators where possible; they fake only things that cross a true external boundary (cloud APIs, filesystem, network).
- **Unit tests** cover one unit in isolation: a pure function, a parser, a state-transition function, a validator. No I/O. No collaborators that do real work.

Both live under `tests/` in the repo's conventional locations (`tests/integration/...`, `tests/unit/...`). Both follow the same rules below unless noted.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md`
2. Feature documentation
3. `docs/architecture/<feature-slug>.md` (if it exists)
4. The e2e tests written in the previous step (authoritative on external behavior)
5. The repo's existing test conventions, helpers, and fakes

## What you produce

Test files at `tests/integration/<area>/<file>.<ext>` and `tests/unit/<area>/<file>.<ext>`. The structure inside each file:

1. **A docstring/comment per test** stating what the unit *does* in plain terms. Example: *"parses a config with a missing `agent` field and returns a ConfigError whose message names the missing field"* — not *"tests parse_config with bad input"*.
2. **Happy path test first.** Then edge cases. Then error cases. Then fuzzed inputs.
3. **Explicit edge-case tests for every boundary** the docs or architecture notes imply: empty input, single-element input, maximum-size input, input at each state boundary, each failure mode of every collaborator.
4. **Fuzzing for every function that takes structured input.** Use the repo's fuzz/property testing library (or stdlib equivalents like Go's `testing.F`, Python's `hypothesis`, etc.). Fuzz the types the function actually accepts. Pin invariants: "no input causes a panic", "idempotent under repeated application", "output satisfies schema X".

## How to run this step

1. **Read the `## Workstreams` section of `docs/architecture/<slug>.md`.** Every workstream owns a `Tests` entry naming its test files. You'll write those files, grouped by workstream.
2. **Walk the feature top-down per workstream.** For each public unit in the workstream's `Owns` list (as described in docs), identify its direct collaborators. That gives you the integration surface for this workstream.
3. **For each unit, list what it does** as one-line claims. Each claim becomes one test.
4. **For each claim, list the edge cases.** Empties, nulls, extremes, repeated calls, unusual orderings, concurrent calls where concurrency is a thing. Each edge case becomes one test.
5. **Write the test file.** Docstring-per-test stating behavior. Arrange/act/assert clearly separated. Use the fakes named in the workstream's `Fakes needed` entry — if a fake doesn't exist yet, add it to a shared fakes file specified in the workstream plan.
6. **Add a fuzz/property test per function with structured input.** Even a trivial fuzz harness catches things exhaustive cases miss.
7. **Run the full suite.** All should be red for the right reason (unit doesn't exist yet, or is incomplete). None should be red for the wrong reason (import error, broken fake, bad assertion).
8. **Show the user a summary of the test matrix** — workstreams × units × cases — and confirm coverage is complete before handing off.

## Parallelization

Because each workstream owns its own test files and depends only on the locked contract files, workstream test suites can be written concurrently. If multiple agents are available, dispatch one per workstream (each edits only that workstream's `Tests` paths). If working sequentially, order within a wave doesn't matter.

## Rules

- **Independent and atomic.** Any one test can be run in isolation and produce the same result as running it in the full suite. No shared mutable fixtures. No test depends on another test's side effects.
- **No ordering assumptions.** Tests can run in parallel and in any order.
- **One behavior per test.** If a test has two asserts of two different facts, split it.
- **Describe behavior, not implementation.** A test named `test_parse_config_uses_json_decoder` is a trap. Name it `test_parse_config_accepts_trailing_newline`.
- **Fuzz where plausible.** If a function takes strings, bytes, numbers, or structured data — fuzz it. Pin the invariants you care about.
- **Fakes, not mocks.** A fake behaves like the real thing (in-memory filesystem, in-process HTTP server). A mock just records calls. Prefer fakes; only use mocks when the real collaborator is impossible to run (e.g., a paid external API with no fake available).
- **No mocking the unit under test.** Ever.
- **No snapshot tests for structured data.** Assert the specific properties that matter; snapshots rot and hide real changes.
- **Red before green.** Confirm every test fails, for the right reason, before handing off.

## Coverage you must achieve

- Every public function/method/command described in the documentation has at least one test.
- Every documented failure mode has at least one test.
- Every state transition in any state machine has a transition-specific test.
- Every config field has: valid-value test, invalid-value test, missing-field test, default-value test.
- Every external boundary (cloud API, filesystem, process spawn, network) has: success test, timeout test, malformed-response test, authentication-failure test.

## Drift handling

- **A test you want to write contradicts an e2e test** → the lower-precedence test loses. Either the e2e test is wrong (amend it, with user approval) or your unit test is wrong.
- **A test reveals the docs are ambiguous** → go back to `document`. Fix docs. Then return.
- **A test needs to touch private internals to pass** → the unit's public surface is too small. Go to `plan-implementation`, refine the boundary, then resume.

## Handoff

When the full red suite is in place and the user has reviewed coverage, the next skill is `implement`.

## Precedence hierarchy (reminder)

1. Intent
2. Documentation
3. E2E tests
4. **Integration tests** ← you are here
5. **Unit tests** ← you are here
6. Code

These tests are the contract the code will be written to satisfy.
