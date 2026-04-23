---
name: code-write
description: End-to-end disciplined software development workflow that runs intent → document → plan-implementation → e2e-tests → integration-unit-tests → implement → review-stack in sequence, with user confirmation between every step. Use when the user says "build this feature", "implement X from scratch", "start a new feature properly", "do it the right way", "code-write this", "run the full workflow", or asks for any non-trivial piece of software to be built where discipline matters. Produces artifacts at every layer and leaves behind a fully reviewed, tested, documented implementation. Prefer this over jumping straight to code for any feature whose correctness or durability matters.
---

# Code Write

**The orchestrator.** Runs the full seven-step disciplined workflow. Use this when the cost of getting it wrong is higher than the cost of doing it properly.

## The workflow

```
   1. intent                  → docs/intent/<slug>.md
           │
           ▼
   2. document                → README / docs (updates or creates)
           │
           ▼
   3. plan-implementation     → docs/architecture/<slug>.md
                                 (architectural decisions + workstream breakdown
                                  for parallel work with minimal merge conflicts)
           │
           ▼
   4. e2e-tests               → tests/e2e/<journey>.<ext>   (all red, one owner)
           │
           ▼
   5. integration-unit-tests  → tests/integration/**  +  tests/unit/**   (all red,
                                 tests grouped by workstream so they can be
                                 written in parallel)
           │
           ▼
   6. implement               → production code   (dispatched by workstream;
                                 parallel where waves permit, sequential within
                                 each wave;  docs/architecture/<slug>.md updated
                                 as decisions land)
           │
           ▼
   7. review-stack            → pass / needs-rework report
```

**Parallelization is a first-class output of step 3.** The workstream breakdown produced there is the scheduling input for steps 4, 5, and 6. Any step past 3 that cannot map its work cleanly onto the workstream list is a signal that step 3 was under-specified — escalate and refine it.

**Wave review is required between every wave in steps 5 and 6.** After a wave's workstreams finish, invoke the `wave-review` skill. The orchestrator does not advance to the next wave until `wave-review` returns PASS. This is how drift is caught early, before it compounds across waves.

## Precedence hierarchy (reminder, enforced throughout)

1. Intent
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

When anything disagrees, the higher layer is right by default. Any deviation requires going *up* to fix the higher layer first.

## Philosophy — always in force, orthogonal to the hierarchy

`docs/PHILOSOPHY.md` is the lens used to resolve every technical ambiguity inside every step. It does not replace the precedence hierarchy — intent still wins goals — but when two options serve the same layer equally well, philosophy's axes break the tie. Philosophy is referenced from each sub-skill's Inputs; re-read it at the start of each step.

## How to run this orchestrator

### 1. Frame the request
Before invoking any sub-skill, confirm with the user:
- What feature / slug will this workflow run for?
- Is this a new feature or a modification to an existing one? (Existing features may already have intent/docs; you'll extend, not replace.)
- Any non-negotiable constraints the user wants to surface upfront (deadline, compatibility, stack)?

Then state the plan back: "I'll run the seven-step workflow. Between each step, I'll show you what was produced and wait for your confirmation before continuing."

### 2. Run each skill in order
For each step, in order:

1. Invoke the skill via the Skill tool.
2. Let the skill complete its work and produce its artifact.
3. Show the user the artifact (or, for `plan-implementation`, the summary of thinking).
4. **Wait for explicit user confirmation** before moving to the next step. Don't assume silence = approval.
5. If the user requests changes, stay on the current skill and iterate.
6. If the user wants to revisit a previous step, go back and re-run it — later artifacts may need re-derivation. Flag that to the user before proceeding.

### 3. Track where you are
At every message, state the current step: e.g. `[code-write 4/7 — e2e-tests]`. The user should always know what's being worked on.

### 4. Respect drift escalation
At any step, if a sub-skill detects that a higher-precedence layer is wrong, it will escalate. When that happens:
- Stop the current step.
- Go back up to the layer that needs fixing.
- Re-run from there, cascading changes down as needed.
- Explain the cascade to the user.

### 5. End the workflow
The workflow ends when `review-stack` returns **pass**. At that point:
- Summarize what was built: one paragraph of plain-English description.
- List the artifacts created, by path.
- Note any deferred items or caveats from the review.
- Hand control back to the user.

If `review-stack` returns **needs-rework**, the workflow **does not end**. Go back to the earliest-precedence layer that has drift, fix it, and cascade down. Re-run `review-stack` when complete.

## How to ask the user for input (load-bearing)

The user is the business owner, not the implementer. They have just asked you to generate a lot of code they haven't read. **Do not present technical option menus.**

When you need input to make a decision:

1. **First try to decide yourself.** Walk the lenses in order: intent, then PHILOSOPHY.md axes, then consistency with existing conversation. If the answer falls out, just decide — report what you chose and why in one sentence, and invite redirect.
2. **If the answer is genuinely under-specified, ask a user-facing question.** Frame it in terms the user already understands:
   - Who is the user of the tool?
   - What experience do they need?
   - What promises is the product making?
   - What tradeoff matters to the business?
   Extract the one piece of information you actually need, then make the technical decision yourself with it.
3. **Only show code-level options as a last resort**, and only when the user is in "architect mode" — actively reading code, or having explicitly asked for the options. When you do, flag explicitly: "this one needs you to look at X; here are the options."

Examples:
- ❌ "Option A: add recover helper. Option B: remove it. Pick one."
- ✅ "Do you want to see all failing tests at once while we develop, or is one-at-a-time fine? If no preference, I'll pick the lower-ceremony option."
- ❌ "Should `AdminAPIBaseURL` be a config field or env var?"
- ✅ "Should `aa` support enterprise Anthropic customers on self-hosted endpoints in v1, or is that a later concern?"

Minimize the user's decision load. Every question you ask is a tax.

## Rules

- **Never skip a step.** If the user asks to skip intent "because it's obvious", push back: the five minutes to write intent catches a month of rebuild. Skipping is allowed only if the user explicitly overrides and acknowledges the risk.
- **Never run two steps in parallel.** Each depends on the previous being settled.
- **Never let a step finish while red (where red should be green).** E2e and integration/unit tests end their step red — that's correct. Implement ends green. Review ends pass.
- **Stay honest with the user.** If a step is taking longer than expected, say so. If you catch yourself generalizing beyond intent, flag it and stop.
- **No scope creep.** If mid-workflow the user asks "can we also add X?", the answer is: yes, after we finish, as a new code-write run. Do not bolt features into the current run.
- **Artifacts are durable.** Every artifact produced (intent doc, docs, tests, architecture notes, code) survives the workflow and remains the reference for future work.

## When this workflow is overkill

- A one-line bug fix where intent is self-evident and the existing test suite already covers the area.
- Pure refactors where the existing test suite is comprehensive and intent is unchanged.
- Exploratory spikes where the goal is to learn, not to ship.

In those cases, use the individual skills selectively (`intent` alone to capture a bug report, `review-stack` alone to audit drift) or skip this skill entirely.

## When this workflow is the right tool

- New features of meaningful scope.
- Changes that cross module boundaries.
- Anything where "the code works but doesn't do what I wanted" is a realistic failure mode.
- Anything where three months from now, someone (including future-you) will need to know why the code looks the way it does.

## Handoff back to the user

When the workflow ends (pass), provide a short summary like:

```
[code-write complete]

Feature:   <slug>
Artifacts: docs/intent/<slug>.md
           <doc paths>
           docs/architecture/<slug>.md
           tests/e2e/<files>
           tests/integration/<files>
           tests/unit/<files>
           <code paths>
Tests:     <counts, all green>
Review:    pass (notes: <anything>)

Next:      <recommendation — commit, PR, deploy, or follow-up items>
```
