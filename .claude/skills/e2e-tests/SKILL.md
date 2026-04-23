---
name: e2e-tests
description: Write end-to-end tests that describe full user journeys, including persona, the why behind each step, and business impact if the journey breaks. Use after the `plan-implementation` step, or whenever the user says "write e2e tests", "write user journey tests", "write acceptance tests", or "red/green a user flow". Tests written by this skill must be red before moving on — they describe unbuilt behavior. This is step 4 of the code-write workflow.
---

# E2E Tests

**Step 4 of 7. Third-highest precedence.** E2E tests are the authoritative check that intent and documentation are actually satisfied by the running system. They describe what a real user does and what breaks if it doesn't work.

## Purpose

Translate the documented user-facing behavior into executable tests that simulate a real user's full journey — end to end, through real interfaces, against real (or realistically-faked) infrastructure.

We always do **red/green**: the test is written first, it fails first, and then the implementation in a later step makes it pass. If a test is green before implementation, the test is wrong or the feature already exists.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md`
2. The feature's documentation (user-facing sections and personas)
3. Any architecture notes
4. The testing frameworks and conventions already in the repo

## What you produce

One or more test files at `tests/e2e/<journey-slug>.<ext>` (use the repo's existing test conventions). Each file contains **one user journey** and is runnable independently.

### Structure of every e2e test file

At the top, a comment block (or docstring — whatever the test runner supports) that states, in this order:

```
PERSONA
  <name + role>. What they know, what they don't, what tool they reach
  for first. Copied / adapted from intent.

JOURNEY
  Step by step. Each step includes:
    - what the user does
    - the WHY (what they're trying to achieve with this step — not just
      the mechanical action)
    - what they observe (the observable outcome)

BUSINESS IMPACT IF BROKEN
  What concretely goes wrong if this test starts failing. Who complains.
  What revenue, trust, or workflow is lost. Be specific.
```

Then the test itself. Every step in the JOURNEY maps to an assertion or an action in the test. No step in the test that isn't in the JOURNEY. No step in the JOURNEY that isn't in the test.

### Example skeleton

```python
"""
PERSONA
  Maya, senior backend engineer. New to this repo, familiar with
  similar internal tools. Reads the README first, then runs commands.

JOURNEY
  1. Maya runs `aa init` inside a fresh clone.
     WHY: she wants the minimum config the tool needs to work here.
     OBSERVES: a two-line aa.json appears at repo root with image and
     agent fields; stdout says "wrote aa.json".

  2. Maya runs `aa` with no arguments.
     WHY: this is the only verb she remembers, she's testing if the
     defaults Just Work.
     OBSERVES: a session-start banner shows egress allowlist and
     backend mode; then the container attaches her into the agent.

  3. Maya types a prompt, detaches with Ctrl-b d, closes laptop.
     WHY: the whole point of the product is to walk away.
     OBSERVES: terminal returns control to her shell; session is
     still listed under `aa list` on reattach hours later.

BUSINESS IMPACT IF BROKEN
  Entire onboarding flow is dead. Any new user abandons the product
  on step 1 or 2. Existing users hit this on every fresh repo.
  The "I walked away and came back" promise is the product's
  differentiator; breaking step 3 removes the reason to use aa over
  running an agent locally.
"""
```

## How to run this step

1. **Read the user-facing documentation** and identify the distinct journeys. Usually one journey per persona-goal pair.
2. **For each journey, write the comment block first.** Persona, steps, business impact. Get the `why` right before coding the assertions.
3. **Write the test body.** Prefer real interfaces (CLI invocations, HTTP calls, process spawns) over calling internal functions. E2E means *end to end*.
4. **Run the tests. They must all fail.** If any passes, investigate — usually the test is asserting something trivial, or the feature is partially built and the test doesn't actually check the hard part.
5. **Show the failing test output to the user** and confirm the red state is meaningful (the right reason for failure, not a syntax error).
6. **Commit the tests as red** (in a branch, or behind a `skip` with a TODO that the next step removes — decide with the user).

## Rules

- **One journey per file.** Combining journeys makes failures ambiguous.
- **Each test runs independently and atomically.** No ordering between e2e tests. Each sets up and tears down its own state.
- **No mocks at the e2e layer.** Fakes that behave like the real thing (an in-process fake Fly API, a test SSH daemon) are fine. Mocks that just record calls are not.
- **Test through public interfaces only.** If a journey can't be tested without reaching into internals, the design is wrong — go back to `plan-implementation`.
- **Realistic inputs.** Use the file paths, config values, and command sequences the docs actually show.
- **No flaky tolerance.** If a test is non-deterministic, fix the determinism (inject clocks, fix ordering) before proceeding.
- **Red before green, always.** The point of red/green is that a passing test means *your code* made it pass.

## Drift handling

If, while writing an e2e test, you find:
- Documentation is ambiguous about the expected behavior → go to `document` and fix it. Then resume.
- Documentation contradicts intent → go to `intent` and resolve. Then cascade down.
- Documentation is internally consistent but you disagree with it → raise with the user; do not silently write a test that matches your preference.

## Handoff

When all journeys have failing e2e tests confirmed by the user, the next skill is `integration-unit-tests`.

## Precedence hierarchy (reminder)

1. Intent
2. Documentation
3. **E2E tests** ← you are here
4. Integration tests
5. Unit tests
6. Code

Integration and unit tests below must not contradict the journeys you wrote here. If they do, those lower layers are wrong.
