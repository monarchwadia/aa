---
name: document
description: Write or modify extensive, accurate developer-facing documentation based on solidified intent. Use this after the `intent` skill produces an intent document, or whenever the user says "write the docs", "update the README", "document this", "documentation-driven design", or asks for user-facing / developer-facing documentation of a feature. This is step 2 of the code-write workflow; it reads intent as the source of truth and produces the spec that tests and code will follow.
---

# Document

**Step 2 of 7. Second-highest precedence.** Documentation is the practical spec. It tells a developer using this feature exactly how to use it, what it does, and what it does not do. Tests and code must make the documentation true.

## Purpose

Turn a confirmed intent document into **accurate, extensive, practical documentation** that a developer could use to operate the feature. Documentation is not marketing copy. It is a contract.

## Inputs (read in this order)

1. `docs/intent/<feature-slug>.md` (highest; nothing you write may contradict it)
2. Existing READMEs, CHANGELOGs, `docs/` content (for style and scope of overlap)
3. The current codebase (for any pre-existing interfaces to be consistent with)
4. The `plans/` directory if it exists (design provenance, not a spec — treat as supplementary)

## What you produce

**Primary**: Update or create the developer-facing README (or the appropriate subsection of it). Typical contents:

- One-paragraph pitch: what the feature is and the core problem it solves.
- Mental model: how the pieces fit together. A diagram when it helps.
- Quickstart: the smallest possible end-to-end example.
- Configuration reference: every config field, with type, default, constraints, example.
- Command / API reference: every public command or API, with inputs, outputs, failure modes.
- Lifecycle / state model where relevant (state machines shown as diagrams).
- Security model (what the feature does and does not protect against).
- Troubleshooting section covering the realistic failure modes users will hit.
- Non-goals (copied/adapted from intent — reinforces scope).

**Secondary (when applicable)**: Module-level docs, API reference pages, migration guides.

## How to run this step

1. **Re-read the intent doc in full.** Every claim you make must trace back to something in intent, or be a faithful practical elaboration of it.
2. **Survey the existing docs** so your additions match the codebase's voice and don't duplicate.
3. **Draft the documentation.** Lead with the developer's job. Include concrete examples — real code blocks, real file paths, real config snippets, real command invocations. Use tables for schemas. Use diagrams for architectures and state machines.
4. **Mark proposed vs decided.** If a field name, path, or API detail is not yet specified in the intent or prior design artifacts, label it **proposed, not yet specified** with a short note on what was and wasn't decided. Do not invent details and present them as settled.
5. **Show the user the documentation and iterate.** Ask specifically: "does this accurately describe what you want built?" Drift detected here is cheap; drift detected in code is expensive.
6. **Call out deliberate omissions.** If intent includes something the docs don't cover yet, say so and flag whether it's deferred or forgotten.

## Rules

- **Documentation is the spec.** Write it as if no code existed yet. Don't document what the code happens to do — document what the feature is supposed to do.
- **Be honest.** If a tradeoff is real, say it. If a protection has a residual risk, say it. Glossy docs become broken docs.
- **Use verbose, explicit names in examples.** A developer should understand a snippet without reading the rest of the doc.
- **No promises you can't keep.** If intent says "TBD during implementation", your doc says the same. Don't fill gaps by guessing.
- **One claim per line in reference tables.** No prose paragraphs in a field reference.
- **Every example should actually work** once the feature is built. No fake commands.

## Drift handling

If while drafting you notice something in intent that no longer makes sense, **stop**. Either:
- Go back to `intent` and amend it (preferred if the user agrees the intent was wrong), or
- Document the current intent faithfully and raise the concern with the user for a later intent revision.

Never silently correct intent by writing contradictory docs.

## Handoff

When the user confirms the documentation is accurate and extensive enough, the next skill in the workflow is `plan-implementation`. Invoke it or hand control back.

## Precedence hierarchy (reminder)

1. Intent
2. **Documentation** ← you are here
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

Tests and code will be held accountable to what you wrote here. Write accordingly.
