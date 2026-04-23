# Technical philosophy

The principles used to resolve technical ambiguity in this project. When multiple approaches look reasonable, we pick the one that aligns with these. **Intent** always wins on goals; philosophy is the tiebreaker for everything else.

**Context this philosophy is tuned for:** a brand-new, innovative, never-been-done-before tool, authored primarily by an LLM, for personal consumption, with strong DX, evolving as the LLM ecosystem evolves. Different context → different philosophy.

**Read this at the start of every coding session.** Every skill in `.claude/skills/` that touches code references this document.

---

## The five axes, in precedence order

1. **Clarity for the LLM** — the reader is a fresh Claude session, every time. Comprehensibility is the enabler of everything else.
2. **Evolvability over stability** — every component is rewriteable. This is the actual goal.
3. **Observability for the solo developer** — when something's wrong, a 30-second glance shows why.
4. **Low ceremony, ruthless YAGNI** — the shortest honest path from idea to experiment.
5. **Safety at the boundary, not everywhere** — defensive coding is scoped to the paths where the threat model demands it. See "Strict mode" at the bottom.

When two options tie, lower number wins. Clarity is first because without clarity the LLM can't evolve, debug, or refactor — every other axis dies downstream.

---

## 1. Clarity for the LLM

The reader is not you. The reader is a fresh Claude session that has never seen this codebase. Everything is optimized for that reader.

- **Verbose, complete-word names.** `resolveEphemeralAnthropicKey`, not `getKey`. `ContainerLifecycleController`, not `Ctrl`. Abbreviations only for industry standards (`HTTP`, `URL`, `JSON`, `ID`). The extra keystrokes are paid once by the author and saved every time the code is read.
- **Every file has a package- or file-level doc comment** stating what the file contains and why it exists as its own file.
- **Every exported symbol has a doc comment with a realistic example.** Never `foo`/`bar`. Show a real invocation with real values.
- **One responsibility per file, one file per responsibility.** If an LLM has to read three files to understand one function, the design is wrong. Split.
- **No implicit state, no import-time side effects.** A function's behaviour is determined by its arguments and receiver, nothing else. Config is passed in, not read from a global.
- **No reflection, no code generation, no runtime dispatch** where a switch statement works. Magic is invisible to the LLM; it makes subsequent edits ten times harder.
- **Type signatures tell the story.** Prefer rich, specific types over `string` and `[]byte`. A parameter named `host Host` is better than `addr string`.
- **Readable beats concise.** If a chained one-liner is harder to understand than four plain lines, write the four lines. Every "clever" idiom costs future-Claude a context window.

## 2. Evolvability over stability

The tool is still being discovered. Assume every component will be rewritten.

- **Thin seams.** An interface has the minimum methods that work today. Expand deliberately when a real need appears. Never add a method "in case."
- **Local state over shared state.** Pass structs explicitly. No singletons, no package-level mutable state, no init() magic. Every dependency is visible at the call site.
- **Data over behaviour.** Plain structs beat classes-with-methods-that-do-things. A function that takes data and returns data is always more reusable than a method on a big object.
- **Composition over inheritance.** Embedding is fine for sharing fields. Deep chains of "extends" are not.
- **Three-strike rule for abstraction.** Three copies that really do the same thing = extract. Two copies = leave. One copy = inline it. Premature abstraction is harder to undo than duplication.
- **Every file rewriteable in one session.** If a file is too big to rewrite from scratch in one Claude session, split it. ~400 LOC is the comfort zone; 800 is the ceiling.
- **Delete unused code immediately.** Don't "keep it just in case." YAGNI applies to the code you wrote last week too.
- **No frameworks inside the tool.** Config file, yes. Plugin interface, yes. "Everything must register through this subsystem I built" — no.

## 3. Observability for the solo developer

You are also the user. When something breaks at 11pm, you need to know why in 30 seconds.

- **Every operation prints what it did.** Not just errors — successes too. `aa push` says what was pushed. `aa kill` lists what was torn down.
- **Errors include what, why, and what-next.** `loading config at ~/.aa/config.json: missing required field "default_backend" — run 'aa init --global' to scaffold one`. Not `invalid config`.
- **State on disk, not hidden in memory.** Session records in `~/.aa/sessions/*.json` are plain JSON a human can `cat`. Agent state file is a text file. Nothing lives only in process memory where you can't see it with `ls`.
- **Structured logs, not prose.** Key=value or JSON lines. `grep` is the debugger.
- **No silent successes for interesting events.** Provisioning finished → print a line. Key revoked → print a line. Teardown complete → print a line. Users (= you) infer progress from these.
- **Verbose mode adds, never changes.** `aa -v` prints additional lines; it does not rewrite or re-format the normal output. Parsers (including future-you) can rely on normal output.
- **Long operations show progress.** No 20-second silent blocks. Even a simple `⚡ Provisioning...` dot-dot-dot is enough.

## 4. Low ceremony, ruthless YAGNI

Ceremony is the enemy of iteration. Every hoop between "I have an idea" and "I can try it" is a hoop you will eventually stop jumping through.

- **Shortest honest path from idea to experiment.** No required ADR for trivial choices. No pre-built abstraction "for future flexibility." No forced test for a 20-line spike.
- **No speculative interfaces.** If there's one implementation today, there's no interface today. An interface comes into existence the moment there's a second implementation OR a test fake needs to substitute — not before.
- **No "temporary" code without a delete date.** If something is "just for now," put a `// DELETE BY 2026-MM-DD` comment. If that date passes, delete it or renew it.
- **No CI until there's something to protect.** The test suite on your laptop is enough until the tool has real users. Adding GitHub Actions is its own decision, later.
- **No configuration knob without a user who has asked for it.** Every flag is a maintenance surface. Defaults until someone (you counts) needs to change.
- **Vertical slices over horizontal layers.** Build the thinnest end-to-end path that proves a feature works. Widen only after the thin path is solid.
- **Delete > comment out.** If code is not running, it's not protecting anything; it's noise.

## 5. Safety at the boundary, not everywhere

Defensive coding has a cost: it slows evolution and clutters code. Pay that cost only where the threat model demands it.

**Inside the tool, trust the tool.** Functions trust their callers. Non-nil Go types aren't re-checked for nil. Outputs from your own code aren't re-validated.

**At the boundary, trust nothing.** The specific boundary paths that get defensive treatment are listed in "Strict mode" below. In those paths: validate at entry, fail loud on anything unexpected, assume input is hostile until proven otherwise.

Do not apply defensive patterns to pure internal functions. Re-validating data that already passed a boundary check is a ceremony without a benefit; it just makes the code harder for the LLM to evolve.

---

## How to apply these when you hit an ambiguity

1. State the options. Two or three, concretely.
2. For each axis (1–5 in order): which option wins?
3. Pick the option that wins the highest-numbered axis first. If tied at the top, pick the one winning the most axes overall.
4. If the decision is load-bearing, record it:
   - **Architectural** → new ADR in `docs/architecture/<slug>.md`.
   - **New principle** → new entry in this document's History.
   - **Small local choice** → in the commit message, naming the axis used: `"use process backend default: clarity (explicit default) over ceremony (env var)"`.

Most decisions don't need to be recorded. Only the ones you'd want to re-derive later.

---

## Strict mode — paths where defensive coding applies

These are the paths where the agent is potentially hostile, user input is untrusted, or a bug becomes a security incident. In these files, apply full defensive discipline: fail loud, validate every field, disallow unknown input, timeout every I/O, never swallow an error.

- `cmd/aa/egress.go` — egress controller. Firewall rule construction must reject unknown inputs; allowlist parsing must be strict.
- `cmd/aa-proxy/**` — the forward proxy itself. Every code path must refuse on ambiguity.
- `cmd/aa/keys_*.go` — ephemeral key minting and revocation. TTL, spend cap, key ID handling must never silently default.
- `cmd/aa/patch.go` + `cmd/aa/patch_parser.go` — agent-produced patch input, parsed on the laptop. Patch is hostile bytes.
- `cmd/aa/config.go` + `cmd/aa/config_loader.go` — user config parsing at the laptop's boundary. `json.Decoder.DisallowUnknownFields()` required. Missing-required-field errors must name the field.
- `cmd/aa/ssh.go` + `cmd/aa/ssh_runner.go` — any command composition that incorporates user-provided strings. No unescaped interpolation into shell.
- Any future code that reads `$AA_WORKSPACE/.aa/*` produced by the agent.

If you're writing code outside this list, you are *not* in strict mode. Apply axes 1–4 and keep moving.

---

## Anti-patterns worth grepping for

Short list; violation in any of these paths (inside or outside strict mode) is a block, not a style suggestion.

- **Abbreviated identifiers across package boundaries** — `cfg`, `res`, `tmp` hide meaning. `ctx`, `err` are fine (universal).
- **Comments that restate code** — `// increment counter` on `counter++` must go.
- **Placeholder examples in doc comments** — `foo`, `bar`, `baz` signal the example was never thought about.
- **`context.Background()` inside a request path** — deadline and cancellation matter; propagate.
- **Goroutines without a documented exit path** — every `go func()` says how it terminates, in a comment.
- **Clever one-liners** — if a chained expression needs a mental stack depth > 2, split it.
- **"Temporary" without a delete date.**
- **"Just in case" code** — if no current code path exercises it, it doesn't exist.
- **Test code that modifies real `$HOME`** — isolation is non-negotiable.
- **`time.Sleep` as a synchronization primitive in tests** — use channels or explicit state polling.

---

## History

- 2026-04-23 — initial draft attempted team-production-grade axes (Safety → Explicitness → Consistency → Defensive → Reversibility). Rejected by the user: wrong fit for this project's context (LLM-authored, solo, exploration-phase, infinitely evolving).
- 2026-04-23 — **replaced with the current axes** (Clarity → Evolvability → Observability → Low ceremony → Safety-at-boundary). Motivated by the observation that "tight defensive coding everywhere" directly conflicts with "evolve infinitely as the LLM ecosystem evolves" — the latter is the intent-level goal, so it wins. Defensive discipline is kept but scoped to a Strict-mode path list.
