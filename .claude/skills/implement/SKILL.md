---
name: implement
description: Write the production code that turns the red test suite green, guided by intent, documentation, and the tests themselves. Use after the `integration-unit-tests` skill has produced a full red suite, or whenever the user says "implement this", "write the code", "make the tests pass", "turn red to green", "build the feature". Architectural decisions made during implementation are recorded in `docs/architecture/`. This is step 6 of the code-write workflow.
---

# Implement

**Step 6 of 7. Lowest-precedence layer: code.** The code's only job is to satisfy the tests, which satisfy the docs, which satisfy the intent. If any higher layer contradicts your code, your code is wrong.

## Purpose

Write the production code. Drive every change from a red test going green. Keep the code clean, consistent, and verbose enough that another engineer (or an LLM reading it cold) can understand each piece without external context.

## Inputs (read in this order)

1. **`docs/PHILOSOPHY.md`** — the lens for every tradeoff you make while writing code. The anti-patterns list is a block-list — violations of any item there should not enter the tree.
2. `docs/intent/<feature-slug>.md`
3. Feature documentation
4. `docs/architecture/<feature-slug>.md` (existing notes)
5. E2E tests
6. Integration tests
7. Unit tests
8. The existing codebase — patterns, style, idioms, dependency conventions

## How to run this step

1. **Read the `## Workstreams` section of `docs/architecture/<slug>.md`.** This is your schedule.
2. **Write the contract files first, in a single pass.** These are the types/interfaces/signatures every workstream depends on. Nothing else begins until they exist and compile.
3. **Execute workstreams wave by wave.**
   - Within a wave, workstreams are independent by construction — dispatch them in parallel. If multiple agents are available (via the Agent tool), spawn one per workstream and let them run concurrently; each agent edits only its own `Owns` files.
   - If only a single agent is available, run waves' workstreams sequentially but in any order within the wave — order doesn't affect correctness because ownership doesn't overlap.
   - Wave N+1 starts only when wave N's workstreams are all green.
4. **Single-writer / shared files are done by their named owner** at the scheduled wave, never opportunistically.
5. **Within each workstream:**
   - Start at its lowest layer with a red test. Usually: pick a unit test, make it pass, move to the next.
   - Write the minimum code needed to turn each red test green. Resist the urge to generalize. YAGNI.
   - After green, refactor: rename for clarity, extract when duplication is real (three hits, not two), unify only what's actually the same thing. Tests stay green throughout.
6. **Watch for drift continuously.** At every change, compare what you're writing to docs and intent. If you notice drift, stop. Decide:
   - If intent was wrong → back to `intent`.
   - If docs were wrong → back to `document`.
   - If the workstream breakdown was wrong → back to `plan-implementation` to revise the `## Workstreams` section.
   - If a test was wrong → amend with user approval.
   - If your code is wrong → fix the code.
7. **Record architectural decisions as you make them**, in `docs/architecture/<feature-slug>.md`. Any choice future-you would want to know the *why* of — library choice, boundary placement, error-handling strategy, concurrency model.
8. **Run the full suite frequently.** After each workstream lands. After each wave closes. Before every handoff.
9. **When all tests are green and no drift remains, hand off to `review-stack`.**

## Conflict-minimization rules (enforced by the workstream plan)

- **Touch only what you own.** Each workstream has an `Owns` list in `docs/architecture/<slug>.md`. Editing files outside that list is a rule violation; raise it rather than doing it.
- **If you need something from another workstream**, check whether its interface is already locked in a contract file. If yes, code against it (use the specified fake while its implementation lands). If no, stop — the workstream plan is under-specified; escalate.
- **No cross-workstream refactors.** A refactor that touches multiple workstreams' files waits until all workstreams in the affected waves are green, then happens as its own dedicated workstream.
- **Shared files merge with intent.** If the plan names a shared file (e.g. `main.go`) with a single owner and a wave number, only that owner edits it at that wave. Nobody sneaks changes in early.

## Wave review — required at the end of every wave

When a wave's workstreams finish (all workstreams in that wave's implementation are green), **invoke the `wave-review` skill** before moving to the next wave. The review is non-optional; a wave without a PASS'd review has not closed. See `.claude/skills/wave-review/SKILL.md` for the checklist — it covers scope conformance, build state, green/regression correctness, philosophy adherence, strict-mode discipline, and cross-workstream consistency.

If wave-review returns NEEDS-REWORK, fix the items listed in its report (re-dispatch the guilty subagent if scope-level, hand-edit if local), then re-run wave-review. Do not move to the next wave until it PASSes.

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
