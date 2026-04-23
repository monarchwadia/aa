---
name: plan-implementation
description: Think through how to build the feature after documentation is written but before any tests or code. Produces no code; produces a required parallelization plan (workstreams designed to minimize merge conflicts) appended to docs/architecture/<slug>.md, plus optional architectural decision records. Use after the `document` skill completes, or whenever the user says "let's think about how to build this", "plan the implementation", "what's the approach", "think before you code", "design the implementation", "plan the workstreams", "how should we parallelize this". This is step 3 of the code-write workflow; its output is alignment with the user on the shape of the solution AND a workstream breakdown the later steps schedule against.
---

# Plan Implementation

**Step 3 of 7. No artifacts required.** This skill is about disciplined thinking, not output. The goal is alignment between you and the user on how the feature will be built, before any test or line of code is written.

## Purpose

Between documentation (what the feature *is*) and tests (how we verify it), there is a thinking step: **how should we build it?** This skill forces that thinking to happen deliberately, in the open, with principles applied — so later steps don't hit obvious dead ends.

## Inputs (read in this order)

1. **`docs/PHILOSOPHY.md`** — the lens. Every tradeoff you surface is resolved by walking its axes. Read first, every time; it's short.
2. `docs/intent/<feature-slug>.md`
3. The documentation written in the previous step
4. The existing codebase — patterns, abstractions, boundaries already in place
5. Any `docs/architecture/` or `docs/adr/` contents

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
10. **Parallelization of the work itself.** See next section — this is the final output of plan-implementation, and becomes the scheduling input for every step after.

## Parallelization plan (required output)

Before handing off to `e2e-tests`, you must produce a **workstream breakdown** designed to keep merge conflicts at or near zero while the later steps are worked — ideally by multiple agents in parallel.

### What a workstream is

A workstream is a unit of work that:
- Owns a specific, named set of files. Nobody else writes to those files.
- Depends on other workstreams **only through interfaces/contracts** that are agreed upfront — not through shared implementation files.
- Can have its tests written and code drafted independently once its interface is pinned.

### How to design them

1. **Identify the seams.** The interfaces you listed in bullet 1 above are the natural cut lines. Each seam becomes a "contract file" — where the types/signatures are pinned early and everyone else codes against that file.
2. **Group code into workstreams along those seams.** Each workstream owns one file or a small set of files that together implement one responsibility (e.g., "rules engine", "local backend", "ephemeral key provider").
3. **Identify single-writer files.** Some files are inherently shared — e.g., the top-level CLI entry point where every subsystem gets wired in, or a central types file. Pick a single owner for each and schedule them late (after their collaborators' interfaces are stable).
4. **Sort workstreams into waves.** A wave is a set of workstreams that can all run in parallel because none of them depends on another in the same wave. Wave N+1 starts only when wave N's interfaces are frozen (the files exist with the agreed types/signatures, even if bodies are stubs).
5. **Pin fakes.** For any interface a workstream depends on, specify the fake/stub it should develop against so it isn't blocked on the real implementation.

### What you record

Append a `## Workstreams` section to `docs/architecture/<slug>.md` (creating it if needed) containing, in this order:

1. **Contract files** — the short list of files where shared types/interfaces live. These are written *first*, by a single author (usually you), and locked before any workstream starts.
2. **Workstreams, grouped by wave.** For each workstream, a one-line block:
   - **Name** (short, imperative, e.g. `rules-engine`, `local-backend`, `proxy-binary`).
   - **Owns:** exact file paths.
   - **Consumes:** the contract files or interfaces it depends on.
   - **Produces:** the interfaces/types/exported functions it exposes.
   - **Fakes needed:** fakes for any collaborator interface it consumes.
   - **Tests:** which test files it owns (unit + integration; e2e covered separately).
3. **Shared / single-writer files.** Files that multiple workstreams *would* edit naïvely — enumerate them with the single owner and the wave they're scheduled in.
4. **Known unavoidable conflict points.** If there is any file that must be co-edited, name it and specify the merge strategy (append-only, sectioned by comment, etc.).

### Rules

- **Interfaces before bodies.** The contract files must exist and be locked before any workstream is assigned.
- **One workstream = one file (or a tight group).** If a workstream wants to touch files outside its `Owns` list, it raises and the breakdown is revised.
- **Tests follow the workstream.** Unit and integration tests for a workstream's files live in the workstream. Cross-workstream tests are e2e, and belong to a single "integration" workstream with its own owner.
- **Keep waves honest.** If wave 2 turns out to depend on a file wave 1 didn't produce, go back — the interface was under-specified.
- **Small waves beat big ones.** Three workstreams of four files each is far better than one workstream of twelve files, even if the total work is the same.

## What you output

- **A short summary to the user**, in conversation: 5–15 bullets covering the above, tailored to the feature. Not a template dump — actual thinking on this specific thing.
- **A parallelization plan** appended to `docs/architecture/<feature-slug>.md` under a `## Workstreams` section, per the format above. This is required — `e2e-tests`, `integration-unit-tests`, and `implement` all schedule against it.
- **If architectural decisions are being made now** (things that shape future code and need to be durable): draft entries in `docs/architecture/<feature-slug>.md` that the `implement` step will extend. Typical format:

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
