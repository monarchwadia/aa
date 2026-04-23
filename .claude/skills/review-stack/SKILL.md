---
name: review-stack
description: Final alignment review across the full stack — intent, documentation, e2e tests, integration tests, unit tests, and code. Verifies every test is green and every layer agrees with the layers above it. Use after the `implement` skill finishes, or whenever the user says "review the stack", "verify alignment", "check everything agrees", "final review", "does the code match intent". This is step 7 of the code-write workflow; it is the last gate before the work is considered done.
---

# Review Stack

**Step 7 of 7. The alignment gate.** The feature is not done until this skill confirms every layer agrees with every layer above it, and every test is green.

This skill is distinct from a generic PR code review. It reviews **the stack of artifacts** — intent → docs → e2e → integration → unit → code — and confirms they are consistent.

## Purpose

A feature passes this step only if all of the following are true:

1. **All tests green.** Unit, integration, and e2e.
2. **Code matches the tests.** No untested public surface. No tests skipped or disabled.
3. **Tests match the documentation.** Every journey in docs has an e2e test. Every documented behavior has a functional test.
4. **Documentation matches intent.** No documented feature outside the intent's scope. No intent goal undocumented.
5. **Code reflects architectural notes.** Decisions recorded in `docs/architecture/` are the decisions the code actually embodies.
6. **No dead artifacts.** No orphaned helper, dead config field, unused test fixture, or stale doc section.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md`
2. Feature documentation
3. `docs/architecture/<feature-slug>.md`
4. E2E tests
5. Integration tests
6. Unit tests
7. Production code
8. The test run output (all runs, full output)

## How to run this step

1. **Run the full test suite.** If any test is red, skipped, or flaky, the review stops here — hand back to `implement`.
2. **Build a manifest.** Extract from each layer a list of its claims:
   - Intent → list of success criteria and non-goals.
   - Docs → list of documented behaviors, config fields, commands/APIs, failure modes.
   - E2E → list of journey names with their steps.
   - Integration & unit → list of tested behaviors and covered edge cases.
   - Code → list of public functions, commands, fields, and modules.
3. **Cross-reference the manifest, top-down.**
   - Does every intent success criterion map to at least one e2e journey?
   - Does every documented behavior map to at least one test?
   - Does every test map to some documented behavior (or a sub-behavior that serves a documented behavior)?
   - Does every public code surface map to at least one test?
   - Does every config field named in docs actually exist in the code, and vice versa?
4. **Record each mismatch** in a review report with precedence-based resolution:
   - Intent missing coverage → docs/tests/code need to add it.
   - Docs describe something with no test → write the test (back to `e2e-tests` or `integration-unit-tests`).
   - Test covers something not in docs → either document it or delete the test.
   - Code surface with no test → write the test or delete the surface.
   - Code does something not documented → document it or remove it.
5. **Read the architecture notes.** Every architectural decision recorded should be reflected in the code. Flag stale or fantasy decisions.
6. **Check honesty of the docs.** Any claim the code can't back — a performance number, a guarantee, a "never happens" — flag for correction.
7. **Summarize** in a short report to the user:
   - Tests: counts by type, all green (required to proceed).
   - Drift items: prioritized by precedence. Each has a proposed resolution.
   - Honesty issues: any doc claims the code doesn't support.
   - Non-goals tripwire: did implementation quietly cross a non-goal?
   - Overall: pass / needs-rework.

## The precedence rule (critical)

When any two layers disagree, **the higher-precedence layer is right by default**.

1. Intent
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

Examples:
- **Docs say a command exists; no test and no code for it.** → write them. Docs win.
- **E2E test passes but contradicts a doc table.** → the doc is the spec; either the doc was wrong (update it) or the e2e test is wrong (amend it). Go up to `document` first; if the doc reflects intent correctly, the test is wrong.
- **Unit test asserts behavior the docs don't mention.** → if the behavior is a real sub-requirement, document it. Otherwise delete the unit test.
- **Code has a public field not in docs.** → either document it (if legitimate) or make it private / delete it.

The *only* exception: if higher-precedence layer is itself wrong (intent was not what the user actually wanted), you go *up* to fix it. You never silently let a lower layer override a higher one.

## Rules

- **Green tests are a precondition, not a conclusion.** You don't start reviewing until the suite is green.
- **Read the actual test output**, not a summary. Flaky tests that happen to be green today still fail the review.
- **Do not rewrite code in this step.** This skill identifies drift; fixing drift is the job of the appropriate earlier skill.
- **Make every drift explicit.** A review report with no drift is rare and worth double-checking.
- **Record the outcome.** When a review passes, note the pass in the architecture doc (date, summary, any caveats).

## What a good review report looks like

```
REVIEW: <feature-slug>
DATE:   <YYYY-MM-DD>
RESULT: needs-rework

TESTS
  Unit:        214 green, 0 red, 0 skipped
  Integration:  47 green, 0 red, 0 skipped
  E2E:           8 green, 0 red, 0 skipped
  (all suites green — required to proceed)

DRIFT (in precedence order)

  [intent ↔ docs]
    • Intent lists "ephemeral API key with spend cap" as a success
      criterion. Docs mention TTL but not spend cap. Fix: update
      docs/<path>.md § Credentials.

  [docs ↔ code]
    • Docs describe `aa sweep` command; no subcommand wired in
      cmd/aa/main.go. Fix: either implement or remove from docs.

  [tests ↔ code]
    • cmd/aa/state.go has a public func `MarkInconsistent` with no
      test. Fix: add unit test for each state-transition input it
      accepts; fuzz the exit-code field.

HONESTY
  • README claims "1–3 second cold start on Fly". No benchmark in
    repo backs this. Either add a benchmark or soften the claim.

NON-GOALS TRIPWIRE
  • None crossed.

NEXT STEPS
  • Back to `document` to fix docs ↔ intent mismatch.
  • Back to `integration-unit-tests` to cover MarkInconsistent.
  • Back to `implement` to wire or remove `aa sweep`.
```

## Handoff

- **Pass**: the feature is done. Note the pass in `docs/architecture/<feature-slug>.md`. Hand back to the user.
- **Needs rework**: hand back to the appropriate earlier skill per the drift list. The user decides whether to address items now or defer.

## Precedence hierarchy (reminder)

1. Intent
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

This skill is the only one that sees the whole stack at once. Use that vantage point.
