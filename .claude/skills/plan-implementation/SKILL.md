---
name: plan-implementation
description: Think through how to build the feature after documentation is written but before any tests or code. Produces no code and writes no files by default — this is a structured thinking step. Use after the `document` skill completes, or whenever the user says "let's think about how to build this", "plan the implementation", "what's the approach", "think before you code", "design the implementation". This is step 3 of the code-write workflow; its output is alignment with the user on the shape of the solution, not artifacts.
---

# Plan Implementation

**Step 3 of 7. No artifacts required.** This skill is about disciplined thinking, not output. The goal is alignment between you and the user on how the feature will be built, before any test or line of code is written.

## Purpose

Between documentation (what the feature *is*) and tests (how we verify it), there is a thinking step: **how should we build it?** This skill forces that thinking to happen deliberately, in the open, with principles applied — so later steps don't hit obvious dead ends.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md`
2. The documentation written in the previous step
3. The existing codebase — patterns, abstractions, boundaries already in place
4. Any `docs/architecture/` or `docs/adr/` contents

## Principles to apply — explicitly

- **DRY (Don't Repeat Yourself).** Identify the one place each fact should live. But don't pre-unify things that only *look* similar — three similar sites is better than a premature abstraction.
- **SOLID.** In particular: single responsibility per unit, dependency inversion at module boundaries, open to extension at the seams you *know* need it.
- **YAGNI.** If a requirement isn't in intent or docs, don't build for it. No features "while we're here". No configurability no one asked for. No hooks for hypothetical plugins.
- **Avoid premature optimization.** Correct first, simple second, fast third. Do not add caches, pools, concurrency, or clever data structures unless a measurement or a documented constraint demands it.

## What you think about

Work through each of these, briefly, and surface your reasoning to the user:

1. **Seams and boundaries.** Where does this feature meet existing code? What's the contract at that boundary? What must not leak across?
2. **Data flow.** What data enters, what transforms happen, what exits. Name the stages.
3. **Failure modes.** What goes wrong? At each failure, what's the right behavior — retry, abort, warn, degrade?
4. **State and lifecycle.** If the feature has state, is it well-modeled? Are transitions exhaustive? Are states *terminal* or *transient* clearly labeled?
5. **Concurrency and ordering.** Does anything run in parallel? Are there race conditions? Do operations need to be idempotent?
6. **Side effects and reversibility.** What external systems are touched? What's hard to undo? What needs a confirmation step?
7. **Testing surface.** What's easy to test, what's hard, what will need fakes or mocks? If something is hard to test, is it a design smell?
8. **Naming.** Do the key concepts have verbose, unambiguous names that would survive being read by an LLM cold?
9. **Non-goals revisited.** Re-read the intent's non-goals. Are you about to quietly implement one of them? Stop.

## What you output

- **A short summary to the user**, in conversation: 5–15 bullets covering the above, tailored to the feature. Not a template dump — actual thinking on this specific thing.
- **If and only if an architectural decision is being made now** (something that shapes future code and needs to be durable): a draft entry in `docs/architecture/<feature-slug>.md` that the `implement` step will extend. Typical format:

```markdown
# <Feature> — Architecture Notes

## Decision: <short title>
**Date:** <YYYY-MM-DD>
**Status:** proposed

### Context
<why this decision is being made>

### Options considered
1. <option> — <pros/cons>
2. <option> — <pros/cons>

### Decision
<chosen option>

### Consequences
<what this locks us into, what it leaves open>
```

## Rules

- **No code in this step.** Not even pseudo-code in a file. Pseudo-code *in conversation* is fine if it clarifies a point.
- **No file changes except `docs/architecture/`** — and even that only if a real architectural choice is being made now. If everything is still in flux, don't write anything.
- **Surface every assumption you are about to bake in.** If you're about to assume "the agent host is always Linux" or "configs are small enough to read fully into memory", name it and check it with the user.
- **Disagreement with docs is a stop-the-world event.** If you can't figure out how to build what the docs describe, the docs are wrong or intent is wrong. Go back and fix that first.

## Handoff

When the user aligns on the approach, the next skill is `e2e-tests`. Invoke or hand back.

## Precedence hierarchy (reminder)

1. Intent
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

This step does not produce a layer in the hierarchy — it produces shared understanding. But the `docs/architecture/` notes (if any) are a persistent record of *why* the code looks the way it does, and survive the feature's lifetime.
