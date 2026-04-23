---
name: intent
description: Capture and solidify intent before any design, documentation, or code is written. Use this at the start of every new feature, bug-fix with non-obvious scope, refactor, or architectural change — any time the user says "let's build X", "I want to add Y", "we need to solve Z", "plan this feature", or anything that starts a unit of work. Also use when the user explicitly asks to "capture intent", "write an intent doc", or "clarify what we're doing". This is step 1 of the code-write workflow; nothing else in that workflow runs until intent is solidified.
---

# Intent

**Step 1 of 7. Highest precedence in the hierarchy.** All later artifacts (docs, tests, code) must serve this intent. If any later layer disagrees with intent, intent wins and the lower layer is wrong.

## Purpose

Before writing a single line of docs, test, or code, get aligned on **what we are trying to do and why**. Most failed features are failures of intent, not execution. This skill produces one short, durable document that the rest of the workflow answers to.

## What you produce

A single markdown file at `docs/intent/<feature-slug>.md` containing:

```markdown
# Intent: <one-line feature name>

## Problem
<what user/business pain are we addressing? In plain language,
no jargon, no implementation.>

## Personas
<who experiences this problem? 1-3 named personas. Include
their current workflow and what breaks for them.>

## Success criteria
<observable outcomes that prove the feature works. Each must
be verifiable by a human or an E2E test. No fuzzy language
like "better" or "faster" without a metric.>

## Non-goals
<explicit scope cuts. What this feature deliberately does NOT do.
Lists what future-us might be tempted to add and why we're not.>

## Constraints
<hard constraints: tech stack, compatibility, security, deadline,
team capacity. Anything that bounds the solution space.>

## Open questions
<things that need human judgment before we can proceed.
Each is a blocker for the next step.>
```

## How to run this step

1. **Read the conversation first.** The user has almost certainly already said what they want in natural language. Extract as much as you can before asking.
2. **Read any existing intent docs in `docs/intent/`** to check whether this is a new feature or an amendment.
3. **Ask the user the smallest number of questions needed** to fill gaps. Lead with what's missing, not a checklist. Batch related questions together.
4. **Draft the document.** Use the template. Be terse and concrete. No prose paragraphs where a bullet will do.
5. **Show the document to the user and iterate.** Keep going until the user says it captures what they want. Do not proceed without explicit confirmation — intent is the foundation.
6. **Commit the document** (or ask the user to) before the next step begins.

## Rules

- **No solution language in intent.** If a sentence names a library, API, file path, class, or data structure, it belongs in documentation or architecture, not intent.
- **No implementation details.** Not even "we'll use a database" — that's already a decision.
- **Success criteria must be observable.** "Users are happier" is not a criterion. "Users can complete the OAuth flow without contacting support" is.
- **Non-goals are load-bearing.** They prevent scope creep in later steps. Take time to list what you're not doing.
- **Open questions block progress.** If an open question has no answer yet, stop and ask. Don't guess.

## When to re-run this skill

- User changes their mind about the problem.
- A later step uncovers an assumption that invalidates a success criterion.
- A non-goal becomes a goal (or vice versa).

Each re-run produces a new version of the same document. Track the change in the document itself under a `## History` section — intent drift is the most dangerous kind.

## Handoff

When complete, the next skill in the workflow is `document`. It reads this intent file and extends it into practical documentation that tells users/developers how to use the feature. Invoke `document` or hand control back to the user for them to invoke it.

## Precedence hierarchy (reminder)

1. **Intent** ← you are here
2. Documentation
3. E2E tests
4. Integration tests
5. Unit tests
6. Code

Intent outranks all of them. Write as if future-you will read this doc and follow it literally.
