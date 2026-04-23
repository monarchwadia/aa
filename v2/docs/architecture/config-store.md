# config-store — Architecture Notes

**Status:** proposed
**Date:** 2026-04-23
**Slug:** `config-store`
**Inputs read:** `.claude/skills/plan-implementation/SKILL.md`, `v1/docs/PHILOSOPHY.md`, `v2/docs/intent/config-store.md`, `v2/docs/config-store.md`, `v2/main.go`.

This doc covers how the persistent `aa config` store is shaped, where its seams live, and how the work is parallelized across workstreams. No code yet.

---

## Seams & boundaries

The config store has two external-facing surfaces and one internal one:

1. **CLI surface** — `aa config` subcommand. Humans and scripts type this; it must be stable, greppable, and print one line per event.
2. **Reader surface (shared interface to other slugs)** — a tiny set of exported functions other slugs (e.g. `machine-lifecycle`, `docker-up`) call to resolve a value with the documented precedence. Other slugs never touch the file directly and never know the on-disk format.
3. **On-disk file** — single `key=value` text file under `os.UserConfigDir()/aa/config`, mode `0600`, parent `0700`. File format is internal; `aa config` is the supported inspection path (philosophy axis 3: state on disk a human can `cat`, but the spec is `aa config`).

**What stays internal to the store:**
- Parse/serialize of the `key=value` file format.
- The choice of separator, comment prefix, trimming rules.
- Whether the file exists or not (a missing file is an empty config).
- Atomic-write details (write to temp, rename) if we add them.

**What leaks intentionally:**
- The key names themselves (`token.flyio`, `defaults.app`) — those are a user-facing contract documented in `docs/config-store.md`.
- The precedence rule (flag > env > config) for consumers resolving `token.flyio`. This is a published contract; each consumer implements it the same way by calling the shared helper.

**What must not leak:**
- The map type returned by `readConfig`. Consumers should go through a named helper (`ConfigGet(key) (string, bool)`) rather than iterating the map. This keeps the door open to change storage later without touching consumers — philosophy axis 2 (evolvability).

---

## Data flow

Four named stages for a `set` path, three for a `list`, three for a `remove`, and three for a read-by-consumer:

**Set (`aa config k=v ...`):**
1. **Parse args** — split each arg on first `=`. Reject args with no `=` with a clear message naming the bad arg.
2. **Load current** — read existing file into a `map[string]string`. Missing file is an empty map, not an error.
3. **Mutate in memory** — assign each `k=v` pair, overwriting existing keys.
4. **Persist + acknowledge** — write file with `0600`, print `saved <k>` once per key in input order.

**List (`aa config`):**
1. **Load current** — same reader as above.
2. **Render** — if empty, print `(no config set)`. Otherwise one `key=value` per line, keys sorted lexicographically for stable output.
3. **Mask** — apply masking policy to secret-shaped keys before printing (see ADR 2).

**Remove (`aa config --remove <key>`):**
1. **Load current.**
2. **Delete in memory** — if the key isn't present, still exit 0 and print `removed <key>` (idempotent — philosophy axis 4, low-ceremony; the user's goal state is reached).
3. **Persist + acknowledge** — rewrite file (or delete it if now empty; see ADR open-question note below), print `removed <key>`.

**Consumer read (from another slug):**
1. **Check flag** (e.g. `--token`) — if non-empty, use it.
2. **Check env** (e.g. `FLY_API_TOKEN`) — if non-empty, use it.
3. **Check config** via `ConfigGet("token.flyio")` — if present and non-empty, use it; else return the documented "not set" error that names the key.

Key-sorting in list output is the only non-obvious choice; it makes `aa config | diff` useful across runs. Without it, Go map iteration would jitter.

---

## Failure modes

| Failure | Behavior |
| --- | --- |
| Config file does not exist on read | Treat as empty map. Not an error. |
| Config file unreadable (permissions, I/O) | Fail loud with full path and underlying error; exit non-zero. |
| Config file malformed line (no `=`) | Skip the line silently. The file format is forgiving by design; the supported writer (`aa config`) never produces malformed lines. Comment lines (`#`) are explicitly skipped. |
| `aa config bareword` (no `=`) | Abort before any write; print `invalid config argument "bareword" — expected key=value`; exit non-zero. Do not partially apply earlier args in the same invocation (all-or-nothing validation happens before mutation). |
| `aa config --remove` with no key arg | Print usage, exit non-zero. |
| `aa config --remove missing.key` | Print `removed missing.key`, exit 0 (idempotent). |
| `os.UserConfigDir()` fails (no `$HOME` at all) | Fail loud with the underlying error; exit non-zero. This is unusual enough to name explicitly in the error. |
| `MkdirAll` on config parent fails | Fail loud with the path and error. |
| `WriteFile` fails after mutation | Fail loud. Nothing was persisted; in-memory state is discarded on process exit. We accept this risk rather than adding a journal (philosophy axis 4). |
| Setting the same key twice in one invocation | Last write wins, both `saved <k>` lines print. YAGNI on dedup. |
| Concurrent `aa config` invocations | Last writer wins. No file locking. A solo operator is not expected to run two `aa config` commands in the same millisecond (philosophy axis 4). Documented non-goal. |
| Key with `=` in value (`token=fo1=abc`) | Handled — `strings.Cut` splits on first `=` only. Value may contain `=`. |
| Empty key (`=value`) | Treated as invalid; reject at parse stage. |
| Value with embedded newline | Reject at set time with a clear message. The line-based file format cannot represent it. This is an explicit boundary check (the user-input parser is the relevant boundary, philosophy axis 5 — but it is *not* in strict mode's list, so the validation is minimal and focused). |

Validation-before-mutation for the multi-arg set case matters: if the user runs `aa config a=1 bareword c=3`, none of the three should take effect. That's the principle of least surprise and costs one extra loop.

---

## Testing surface

**Easy (unit, pure):**
- Parsing `key=value` lines, including comments, blanks, embedded `=`, leading/trailing whitespace.
- Serializing a map to the file format with sorted keys.
- Arg parser for `aa config` (list / set / remove / invalid).
- Masking predicate: "is this key secret-shaped?"

**Medium (integration, needs fake `$HOME`):**
- End-to-end of `runConfig` round-trip: set → read → list → remove → list.
- File permissions: assert `0600` on file, `0700` on directory, after every write.
- Missing file behavior: list on a fresh `$HOME` prints `(no config set)` and exits 0.
- Corrupt file: malformed lines skipped, valid lines returned.

**Hard (or deferred to e2e):**
- Actual OS-enforced permission isolation between users. We don't simulate a second user in integration tests; this is asserted at the e2e layer only via `stat` output.
- `os.UserConfigDir()` semantics across OSes. We override via `$HOME` / `$XDG_CONFIG_HOME` in tests rather than stubbing the function.

**Fakes needed:**
- None at the package level. The store is a leaf — it talks only to the filesystem.
- Consumers of the store (other slugs) will want a fake `ConfigReader` interface so they can test without touching disk. That fake lives with the consumer, not with the store. We expose the interface from the store package so both sides agree on its shape (philosophy axis 2: thin seam).

**Design smells to watch:**
- If a test needs to mock the filesystem, we've over-abstracted. The real filesystem under an isolated `$HOME` is cheap and honest.
- If the store grows a background goroutine or a cache, we have a problem. It shouldn't need either.

---

## ADRs

Each open question from the intent and docs resolved here.

### ADR 1 — Remove-key syntax

**Date:** 2026-04-23
**Status:** proposed

#### Context
The docs show `aa config --remove <key>` as placeholder syntax. We need to commit. Options are (a) `--remove <key>` flag, (b) `--unset <key>` flag, (c) `aa config remove <key>` subverb, (d) deletion by setting empty value `aa config key=`.

#### Options considered
1. **`aa config --remove <key>`** — matches current placeholder; flag-ish; reads naturally. Cost: mixes flag and positional modes in one command, which is already slightly awkward for `aa config`. Pro: zero surprise from docs.
2. **`aa config --unset <key>`** — common in other CLIs (git). Slightly more precise verb. Same flag/positional awkwardness. No clear win over `--remove`.
3. **`aa config remove <key>`** — subverb. Cleanly separates remove from set. Cost: introduces a second parsing mode inside `aa config`; arguably grows the command surface.
4. **`aa config key=`** (empty value deletes) — zero new surface. Cost: overloads the set verb with a magic value; hurts clarity; `key=` as "value is empty string" is a legitimate future need and this steals that meaning. Violates philosophy axis 1 (clarity — magic is invisible to the LLM).

#### Decision
**`aa config --remove <key>`**, supporting one or more keys: `aa config --remove token.flyio defaults.app`.

Picked by walking the axes:
- **Clarity (1):** `--remove` is an explicit, named operation. Beats the magic-empty-value option decisively.
- **Evolvability (2):** Flag form keeps the command surface tight — we don't commit to a subverb pattern we'd have to honor for future operations (`list`, `get`, etc.). If we later discover we need subverbs, migrating `--remove` to a subverb is mechanical.
- **Observability (3):** One `removed <key>` line per key removed, consistent with `saved <key>` on set.
- **Low ceremony (4):** Matches what the docs already showed as placeholder — no docs churn beyond striking "proposed".
- **Safety-at-boundary (5):** Not applicable; config is not in strict-mode's path list.

#### Consequences
- Locks `--remove` as the spelling. Docs update from "proposed" to live.
- `aa config` grows a flag parser for the first time. Today `runConfig` does manual arg iteration. We will use `flag.FlagSet` scoped to `config`, which is how `spawn` already does it.
- Setting `key=` (empty value) remains available and stores the empty string — it does not delete. If we later want `key=` to be meaningful (unlikely for current keys) the door is open.
- Removing a non-existent key is idempotent (see failure modes).

---

### ADR 2 — Masking policy when listing

**Date:** 2026-04-23
**Status:** proposed

#### Context
`aa config` lists every stored value. Some keys (`token.*`) are secrets that end up on screen, in terminal scrollback, and in shell recording tools. Intent open question: show in full, mask, or mask-by-default with opt-in reveal?

#### Options considered
1. **Show in full, always.** Simplest. Matches current code. Cost: casual `aa config` in a pair-programming screenshare leaks the Fly token. The whole point of this store is that the user stops typing the token — they will run `aa config` to confirm it's set, not to copy it.
2. **Mask always.** `token.flyio=fo1_abc123...` rendered as `token.flyio=<set>` or `token.flyio=fo1_****`. Cost: if the user *does* want to copy the token out, they have to `cat` the file directly. Not a blocker — the file is documented as `cat`-able.
3. **Mask by default, `--show-secrets` flag to reveal.** Best of both. Cost: one new flag to maintain. One more thing to document.

#### Decision
**Mask by default, `--show-secrets` reveals.** Keys matching the prefix `token.` are masked. Masking renders as `token.flyio=<set>` (literal string `<set>`, no prefix or suffix of the real value). Non-token keys (`defaults.*` and any unknown key) are never masked.

Axes:
- **Clarity (1):** `<set>` is unambiguous — the user can't mistake it for a real value (real values never contain `<` or `>`). Beats partial-prefix masking which invites "is that the real prefix?" confusion.
- **Evolvability (2):** The mask predicate is `strings.HasPrefix(key, "token.")` — one function, easy to extend if we add `secret.*` or similar. Docs name the prefix so users know what's masked.
- **Observability (3):** `<set>` still tells the user "yes, this is stored"; they can verify the token is configured without revealing it. `--show-secrets` is the escape hatch for the rare copy-out case.
- **Low ceremony (4):** One flag, no config knob. No "mask these keys too" configurability — YAGNI.
- **Safety-at-boundary (5):** Not strict-mode, but defaulting-to-safe on secret display is cheap and correct.

#### Consequences
- Documented masking rule: any key whose name starts with `token.` is masked in `aa config` output. Users naming a secret key outside that prefix get no masking — we tell them to use the prefix.
- `--show-secrets` is accepted on the list form only. On the set form it is an unknown flag and rejected.
- Locks the rendered mask string as `<set>`. If we change it later, scripts parsing output will break; scripts should use `--show-secrets` if they need the real value.

---

### ADR 3 — Source precedence for token resolution

**Date:** 2026-04-23
**Status:** proposed (confirming existing behavior)

#### Context
Current code in `main.go` resolves the Fly.io token in order: `--token` flag → `FLY_API_TOKEN` env → config `token.flyio`. Docs already document this order. Question is whether to keep it.

#### Options considered
1. **Keep flag > env > config.** Standard CLI convention (git, kubectl, aws-cli). Matches current code and docs.
2. **Invert to config > env > flag.** "Persistent wins." Would defeat the purpose of the flag and env as overrides.
3. **Config > flag > env** or any other permutation. No motivating scenario.

#### Decision
**Keep flag > env > config.** This is not a new decision; it is a pinning of current behavior so future readers don't re-open it.

Axes:
- **Clarity (1):** Matches the convention every CLI user already knows. No surprise.
- **Evolvability (2):** The precedence chain is implemented in one helper (the shared reader exposed by this slug). Changing order later is a one-file edit.
- **Observability (3):** On the "not set" error, we name the key and the config-file remedy. We could also print which source *would* have been used — deferred; YAGNI until someone hits confusion.
- **Low ceremony (4):** No new surface.
- **Safety (5):** N/A.

#### Consequences
- The store exposes a shared helper consumers use — see contract file below. Inventing a second precedence order in another consumer is a bug.
- Error message shape is pinned: `no Fly.io token found — run: aa config token.flyio=<token>` (already in `main.go`).

---

### ADR 4 — Echo-on-set

**Date:** 2026-04-23
**Status:** proposed

#### Context
`aa config token.flyio=xxx` — should we echo the value back, echo only `saved token.flyio`, or stay silent?

#### Options considered
1. **Silent.** Hostile to observability — the user can't tell if anything happened without a subsequent `aa config`.
2. **Echo `saved <key>` per key.** Matches current code. One line per input.
3. **Echo `saved <key>=<value>` per key.** Helpful for non-secrets; terrible for secrets (leaks to scrollback). Even if we mask here, now two places enforce masking.

#### Decision
**`saved <key>` per key, in input order. Value is never echoed.** Matches current code and docs.

Axes:
- **Clarity (1):** The output line tells the LLM/user exactly what happened in a parseable format.
- **Observability (3):** Every operation prints what it did (philosophy explicit rule). One line per key.
- **Low ceremony (4):** No flags, no modes.
- **Safety (5):** Never echoing the value means we don't need a second masking enforcement point.

#### Consequences
- Locks output format: `saved <key>\n` per `=`-pair, in input order. Scripts can rely on this.
- Remove mirrors this: `removed <key>\n` per removed key.

---

### ADR 5 — Key naming convention

**Date:** 2026-04-23
**Status:** proposed

#### Context
Intent open question: is there a reserved namespace for keys, and is it user-visible? We already have `token.flyio`, `defaults.app`, `defaults.image` in the docs.

#### Options considered
1. **Free-form, no convention.** Maximum flexibility. Cost: no way to automate masking (ADR 2); no way for a new agent reading the codebase to predict the shape of future keys.
2. **Strictly enforced schema** (reject unknown prefixes). Cost: closes the "future settings work with no new code" property the docs explicitly promise. Rejected.
3. **Documented convention, not enforced.** Keys follow `<category>.<name>`. Reserved top-level prefixes: `token.<service>` for secrets, `defaults.<name>` for unflagged-command defaults. Unknown prefixes are allowed and stored verbatim — but the masking rule only fires on `token.`.

#### Decision
**Documented convention, not enforced.** Two reserved prefixes today:
- `token.<service>` — a credential for a specific upstream service. Always masked in list output.
- `defaults.<name>` — a value consumed as a default by some `aa` subcommand.

Unknown prefixes are accepted and stored; users take responsibility for not naming a secret outside `token.*`.

Axes:
- **Clarity (1):** Two named prefixes give the LLM predictable slots. A fresh Claude session reading the store code can guess that `token.openai` would be a credential.
- **Evolvability (2):** New categories (`endpoint.<service>`?) can be added by documenting them — no code change. Masking stays tied to the `token.` prefix only; we don't gold-plate it.
- **Low ceremony (4):** No validation to write, no reject path for "unknown prefix". The store accepts anything; the convention is docs-only.

#### Consequences
- The `docs/config-store.md` key table is the source of truth for currently-known keys. Agents adding new config keys in future slugs pick a prefix from the convention and add a row.
- No code path validates the prefix. The user who names their secret `flyio_token=...` instead of `token.flyio=...` loses the masking benefit — documented trade-off.

---

## Workstreams

### Contract files

Exactly one contract file, written first, locked before any workstream begins:

- **`v2/configstore.go`** — package-level doc comment, exported types and function signatures only (bodies may be stubs that panic `"not implemented"` during wave 1). Specifically:
  - `type Store interface { Get(key string) (string, bool); Set(pairs map[string]string) error; Remove(keys []string) error; All() map[string]string }` — the reader/writer surface other slugs call.
  - `func Open() (Store, error)` — opens the default-location store (`os.UserConfigDir()/aa/config`).
  - `func ResolveFlyToken(flag string) (string, error)` — the shared precedence helper implementing flag > env > config. Returns the "no Fly.io token found — run: aa config token.flyio=<token>" error when no source has a value.
  - `const KeyFlyToken = "token.flyio"` — single source of truth for the key name.
  - `func IsSecretKey(key string) bool` — masking predicate (`strings.HasPrefix(key, "token.")`).

This file is owned by the plan author (writes the stubs) and is not edited by any workstream other than `config-core` once locked.

### Workstreams, by wave

**Wave 0 — contract lock.** Single author writes `v2/configstore.go` stubs per the contract file spec above. Once merged, wave 1 begins.

**Wave 1 — parallel (three workstreams):**

- **`config-core`**
  - **Owns:** `v2/configstore.go` (fills in bodies of the contract file).
  - **Consumes:** nothing — leaf module, only the Go stdlib.
  - **Produces:** all exported symbols from the contract file, with working bodies. File I/O, parse/serialize, sorted rendering, `0600`/`0700` permissions, `--remove` semantics at the library level (the `Remove` method).
  - **Fakes needed:** none. Tests run against real filesystem under an isolated `$HOME`.
  - **Tests owned:**
    - `v2/configstore_test.go` — unit tests for parse/serialize, masking predicate, sorted render, round-trip, missing file, malformed lines, embedded `=`, empty key rejection, newline-in-value rejection.
    - `v2/configstore_integration_test.go` — integration tests using a `t.TempDir()` + `t.Setenv("HOME", ...)` + `t.Setenv("XDG_CONFIG_HOME", ...)` pattern. Covers file permissions assertion, directory permissions, full round-trip via `Open`/`Set`/`Get`/`Remove`/`All`.

- **`config-cli`**
  - **Owns:** `v2/config_cmd.go` — `runConfig(args []string)` handler; arg parsing (list / set-pairs / `--remove <key>...` / `--show-secrets`); output formatting (`saved <k>`, `removed <k>`, `(no config set)`, `key=value` or `key=<set>`); exit codes.
  - **Consumes:** `Store`, `Open`, `IsSecretKey` from `v2/configstore.go`.
  - **Produces:** `func runConfig(args []string)` — the single entry point `main.go` already calls.
  - **Fakes needed:** an in-memory `Store` fake for CLI tests so they don't touch disk. This fake lives in `v2/config_cmd_test.go` as an unexported test helper (three-strike rule — only one consumer for now).
  - **Tests owned:**
    - `v2/config_cmd_test.go` — unit tests for arg parsing, output shape, validation-before-mutation (the `a=1 bareword c=3` case), masking-on-list, `--show-secrets`-bypass, `--remove` idempotence, `(no config set)` rendering.

- **`docs-sync`**
  - **Owns:** `v2/docs/config-store.md` (strike "proposed" from the remove section; document `--show-secrets`; document key naming convention; document masking rendering as `<set>`).
  - **Consumes:** this architecture doc only.
  - **Produces:** an updated user-facing doc matching the ADR decisions.
  - **Fakes needed:** none.
  - **Tests owned:** none (docs).

**Wave 2 — single-writer wiring:**

- **`main-wiring`**
  - **Owns:** `v2/main.go`.
  - **Consumes:** `runConfig` from `v2/config_cmd.go` (already called by name; change is removing the inlined `runConfig` + `readConfig` + `writeConfig` + `configPath` from `main.go`), and `ResolveFlyToken` + `KeyFlyToken` from `v2/configstore.go` (to replace the inlined token-resolution block in `runSpawn`).
  - **Produces:** slimmer `main.go`; the same `aa` CLI, now delegating.
  - **Fakes needed:** none.
  - **Tests owned:** no new tests — e2e coverage of `aa config` / `aa spawn` lives outside this workstream.

### Shared / single-writer files

- **`v2/main.go`** — single owner: `main-wiring` (wave 2). Scheduled after `config-core` and `config-cli` interfaces are stable. The only required edits are (a) delete the inlined config helpers, (b) import-and-call `configstore.ResolveFlyToken` and `runConfig`. No other workstream touches this file.
- **`v2/configstore.go`** — single owner: `config-core` (wave 1), after the wave-0 contract author finishes the stub. Authorship transitions cleanly: the contract author does not continue editing in wave 1.

### Known unavoidable conflict points

None. The file layout was chosen explicitly so each workstream owns disjoint files. `main.go` edits are deferred to wave 2 specifically to avoid conflicts with wave-1 parallelism.

If a future workstream (outside this slug) wants to add a second consumer of `Store` (e.g. `defaults.app` in `machine-lifecycle`), it imports from `v2/configstore.go` and does not edit it. The only file that ever accumulates edits over time is `v2/configstore.go`, which is the central store implementation — philosophy axis 2 says this is fine as long as it stays under 800 LOC; current target is well under 400.

---

## File layout summary

- `v2/configstore.go` — store + shared reader helpers (one responsibility: persistent config).
- `v2/config_cmd.go` — the `aa config` CLI handler (one responsibility: turn argv into store operations and print output).
- `v2/configstore_test.go`, `v2/configstore_integration_test.go`, `v2/config_cmd_test.go` — tests owned by their respective workstreams.
- `v2/main.go` — unchanged in role (dispatcher), slimmer in body.

Two production files for the slug, matching the "≤2 files" constraint and staying well inside the 400-LOC comfort band (current inlined code is ~80 LOC; the final split should land around 150 LOC for `configstore.go` and 120 LOC for `config_cmd.go`).

---

## Assumptions surfaced

These are baked in by the plan; flag any the user disagrees with before wave 0 starts:

1. `os.UserConfigDir()` is the right location on all supported OSes. (Already in code; docs list Linux and macOS paths explicitly.)
2. Tests override `HOME` and `XDG_CONFIG_HOME` (Linux) and `HOME` (macOS-style resolution via `os.UserConfigDir`) to isolate from the real user config. No test ever writes to the real `$HOME/.config/aa/config`.
3. The file format stays `key=value\n` text, not JSON. The store is small enough that human-readability wins over schema rigor (philosophy axis 3 — state on disk you can `cat`).
4. No file locking. A solo operator running two concurrent `aa config` invocations is an out-of-scope scenario (philosophy axis 4 — YAGNI until someone hits it).
5. Masking is a list-time rendering concern only. The file always stores the real value; readers always see the real value. Nothing on disk is masked.
6. Config is not in strict mode's path list (per `PHILOSOPHY.md`) — **but** the set-args parser does one thing strict-mode-ish: it validates all args before mutating. This is a single cheap check, justified by "principle of least surprise" for multi-arg set.

---

## Amendments — 2026-04-23

### New config keys: `endpoints.api`, `endpoints.registry`

Two new keys are added to the store with the same precedence as `token.flyio`:

| Key | Purpose | Built-in default | Env var override |
|---|---|---|---|
| `endpoints.api` | Fly Machines API base URL | `https://api.machines.dev/v1` | `FLY_API_BASE` |
| `endpoints.registry` | Container registry host | `registry.fly.io` | `AA_REGISTRY_BASE` |

**Resolution order (unchanged pattern):** per-command flag > env var > config file > built-in default.

Rationale: docker-images discovered the registry lives on a distinct host from the Machines API, so the original single-`FLY_API_BASE` seam is insufficient. Promoting these to first-class config keys (with env-var overrides for tests and per-invocation flags for debugging) matches how `token.flyio` already resolves, and avoids ceremony-only-for-tests.

**Masking policy:** `endpoints.*` keys are **not** masked in listings. They're URLs, not secrets.

**Impact on workstreams:** `configstore.go` gains two more `Resolve*()` helpers (`ResolveAPIBase()`, `ResolveRegistryBase()`) alongside the existing `ResolveFlyToken()`. Wave assignments unchanged.
