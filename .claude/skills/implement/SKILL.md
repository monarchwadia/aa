---
name: implement
description: Write the production code that turns the red test suite green, guided by intent, documentation, and the tests themselves. Use after the `integration-unit-tests` skill has produced a full red suite, or whenever the user says "implement this", "write the code", "make the tests pass", "turn red to green", "build the feature". Architectural decisions made during implementation are recorded in `docs/architecture/`. This is step 6 of the code-write workflow.
---

# Implement

**Step 6 of 7. Lowest-precedence layer: code.** The code's only job is to satisfy the tests, which satisfy the docs, which satisfy the intent. If any higher layer contradicts your code, your code is wrong.

## Purpose

Write the production code. Drive every change from a red test going green. Keep the code clean, consistent, and verbose enough that another engineer (or an LLM reading it cold) can understand each piece without external context.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md`
2. Feature documentation
3. `docs/architecture/<feature-slug>.md` (existing notes)
4. E2E tests
5. Integration tests
6. Unit tests
7. The existing codebase — patterns, style, idioms, dependency conventions

## How to run this step

1. **Start at the lowest layer that has a red test.** Usually: pick an individual unit test, make it pass, move to the next. Work bottom-up through unit → integration → e2e.
2. **Write the minimum code needed to turn each red test green.** Resist the urge to generalize. YAGNI.
3. **After green, refactor.** Rename for clarity, extract when duplication is real (three hits, not two), unify only what's actually the same thing. Tests stay green throughout.
4. **Watch for drift continuously.** At every step, compare what you're writing to docs and intent. If you notice drift, stop. Decide:
   - If intent was wrong → back to `intent`.
   - If docs were wrong → back to `document`.
   - If a test was wrong → amend with user approval.
   - If your code is wrong → fix the code.
5. **Record architectural decisions** as you make them, in `docs/architecture/<feature-slug>.md`. Any choice future-you would want to know the *why* of — library choice, boundary placement, error-handling strategy, concurrency model.
6. **Run the full suite frequently.** Every meaningful change.
7. **When all tests are green and no drift remains, hand off to `review-stack`.**

## Code quality rules

### Naming — verbose and unambiguous
- Functions, classes, and variables use complete words and say what they do. `resolveEphemeralApiKeyForSession` is better than `resolveKey`. `ContainerLifecycleController` is better than `Ctrl`.
- Avoid abbreviations except for industry-standard ones (`HTTP`, `URL`, `JSON`, `ID`).
- Types describe the thing, not the representation. `EgressAllowlist` beats `StringSlice`.
- Predicates are questions. `isContainerRunning` beats `containerStatus` for a boolean.

### Comments — the *why* and realistic examples
- Default: no comment. Well-named code explains itself.
- Comment when **why** is non-obvious: a hidden constraint, a workaround for a specific bug, a subtle invariant, an ordering dependency.
- For every public function/class, write a doc comment that includes:
  - One sentence describing what it does.
  - A realistic example showing a concrete call and its result, using realistic values — never `foo`/`bar`.
- Do not write comments that restate the code. `// increment counter` on `counter++` must go.
- Do not write comments that reference the current task/PR (`// added for issue #123`). Those rot.

### Consistency
- Match the codebase's existing style before introducing your own.
- One way to do each thing. If there are two HTTP clients in the repo, don't introduce a third.
- If a pattern appears three times, consider extracting. If twice, don't.

### Structure
- One responsibility per file / module / class where it costs nothing.
- Public surface small and intentional; internal surface can be larger but should not leak.
- Errors typed where the language supports it. Errors carry context useful to the caller.
- No silent failures. No catch-and-ignore. No default values that paper over missing config.

## What goes in `docs/architecture/<feature-slug>.md`

Append an entry for each decision that shapes future code:

```markdown
## Decision: <title>
**Date:** <YYYY-MM-DD>
**Status:** accepted

### Context
<what the code needed to decide, why now>

### Options considered
1. <option> — <pros/cons>
2. <option> — <pros/cons>

### Decision
<chosen option>

### Consequences
<what this locks in, what it leaves open, what it will cost to reverse>
```

You do **not** document every small choice this way. Only architectural ones — things that shape how later code is written. A choice of variable name is not architectural. A choice of concurrency primitive is.

## Drift handling (critical)

Every time you change or add code, hold it up against each higher layer:

- **Does this code make a test pass?** If not, why are you writing it? (YAGNI violation.)
- **Does this code contradict a doc example?** If yes, fix one — usually the code, occasionally the doc.
- **Does this code contradict an intent statement?** If yes, stop. Escalate to intent revision.

When you catch drift, record it in a short note to the user *before* resolving it, so the decision is visible.

## Rules

- **Write the minimum code to turn red to green, then refactor.**
- **Run tests continuously.** Green is the only acceptable state between meaningful edits.
- **No new public surface without a test covering it.** If you find yourself adding a public method no test calls, either write a test or delete the method.
- **No pre-optimization.** Measure first. Optimize only when a test or constraint requires it.
- **No silent scope expansion.** If you notice "while I'm here" temptation, write it down and raise it — then return to the task.
- **No skipped hooks, no `--no-verify`, no commenting-out failing tests.** If something's broken, fix it.

## Handoff

When all tests are green, no drift remains, and architectural notes are up to date, the next skill is `review-stack`.

## Precedence hierarchy (reminder)

1. Intent
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. **Code** ← you are here

You are at the bottom. Everything above is more authoritative than what you wrote. If there's disagreement, the code loses by default.
