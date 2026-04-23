# Remaining work — pickup brief for the next session

You are continuing the `aa` project mid-flight. The previous session built the v1 binary, ran an end-to-end smoke test successfully, and was partway through Step 7 (`review-stack`) when the e2e suite surfaced real integration gaps that need cleanup. This document gives you everything you need to finish without prior context.

---

## TL;DR

- The product **works** — manual smoke test on the `process` backend passes end to end.
- The unit + integration test suite (166 tests in `cmd/aa/` and `cmd/aa-proxy/`) is **green and race-clean**.
- The e2e suite (`tests/e2e/`) is **partially broken**: 1 hang fixed (kill-test), 7 others are unverified after the binary became real.
- Two real product fixes landed but are **uncommitted**.
- Step 7 (`review-stack`) is queued; the e2e fix-up is the work blocking it.
- Goal: close Step 7 with a working binary, all tests green, and a clean alignment audit.

---

## Read these first, in this order

1. `INTENT.md` — the v1 contract. Three backends (`local` / `fly` / `process`); agent-env contract uses `AA_WORKSPACE` + `AA_SESSION_ID`.
2. `docs/PHILOSOPHY.md` — the lens for every decision. Five axes: Clarity → Evolvability → Observability → Low ceremony → Safety-at-boundary. Includes a Strict-mode path list.
3. `docs/architecture/aa.md` — ADRs (6 of them) and the workstream plan that drove waves of parallel work.
4. `README.md` — user-facing spec. Especially § Concepts, § Command reference, § Session states, § Egress allowlisting.
5. `.claude/skills/` — the skills you'll use. Particularly: `code-write` (orchestrator), `wave-review` (gate between waves), `review-stack` (Step 7 — the step you are in).
6. `~/.claude/projects/-workspaces-aa/memory/MEMORY.md` — user preferences. **Two are load-bearing**:
    - **Decision framing**: never present technical option menus to the user; ask business/UX questions, then make the technical call yourself.
    - **Commit before forward motion**: commit at every natural checkpoint (passing wave-review, after a fix lands, before dispatching new parallel work).

---

## What just happened (uncommitted state)

Run `git status` first to see the diff. Two files have unstaged changes that need committing before you do anything else:

### Fix 1 (real product bug): `cmd/aa/backend_process.go`

The previous session discovered that `ProcessBackend` tracked the agent's PID only in an in-memory map. Across `aa` invocations (e.g., `aa` to start, then `aa kill` later in a fresh process), the map is empty — so `aa kill` couldn't find the detached agent process to terminate, and orphaned `sh -c 'while true; ...'` processes accumulated forever.

The fix writes the agent PID to `<workspace>/.aa/pid` at `RunContainer` time, and `Teardown` falls back to reading that file when the in-memory map is empty. Imports for `strconv` and `strings` were added.

### Fix 2 (test fix to match real product behavior): `tests/e2e/kill_tears_down_test.go`

The test expected an ephemeral key to have been minted, but `aa`'s `buildKeyProvider` returns a `noopKeyProvider` when `ANTHROPIC_ADMIN_KEY` is empty. The test now passes `ANTHROPIC_ADMIN_KEY=test-admin-key` via `ExtraEnv` on both the start and kill invocations so the `AnthropicKeyProvider` is wired up against the fake server.

`TestKillTearsDownEverything` now passes (~31s).

**FIRST ACTION:** run `git status`, then `git add -A` and commit these two fixes with a faithful message before doing anything else. Use the existing commit-message style (multi-section, "Co-Authored-By: Claude Opus 4.7 (1M context)" trailer).

---

## Remaining work, in priority order

### 1. Sweep the e2e suite (highest priority)

The e2e suite has 8 journey files in `tests/e2e/`. Of these:
- `first_time_setup_test.go` — verified passing (~0.06s).
- `egress_allowlist_test.go` — skips when Docker is unavailable. Leave it alone unless you have Docker + Linux.
- `kill_tears_down_test.go` — just fixed; passes (~31s).
- `detach_reattach_test.go` — verified passing but takes ~40s (suspiciously slow; probably long polling). Investigate whether the runtime can be reduced without weakening the test.
- `uncommitted_changes_test.go`, `push_with_rule_violation_test.go`, `push_reads_patch_from_laptop_test.go`, `session_states_test.go` — **status unknown**. Run each individually with a 60s timeout.

For each failing or hanging test:

- Read the `PERSONA / JOURNEY / BUSINESS IMPACT` block at the top to understand the spec.
- Diagnose: is it a real product bug, or a test/code mismatch (test expected something the product can't do without specific env or config)?
- Apply the cheapest correct fix — patch the test to reflect reality OR patch the product to match the spec. The Strict-mode path list in `PHILOSOPHY.md` decides which side wins by default for security claims; for everything else, use the philosophy axes.
- After each fix: rebuild (`go build -o /workspaces/aa/aa ./cmd/aa`) and re-run the specific test before moving on.
- **Commit after each test goes green** per the user-preference memory.

Tooling tips:
- E2E tests **invoke the real `aa` binary**. Build it before running them: `go build -o /workspaces/aa/aa ./cmd/aa`.
- If a test hangs, an orphaned `sh -c 'while true; ...'` agent is probably running. Reap them: `pgrep -f "while true" | xargs -r kill -9`.
- All e2e tests need `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1` in the environment — `tests/e2e/helpers.go` injects this automatically.
- For tests that need an ephemeral-key flow, set `ANTHROPIC_ADMIN_KEY` via `aaInvocation.ExtraEnv` (see the fix in `kill_tears_down_test.go`).

### 2. Run the full test suite green

After every e2e test passes individually, confirm the whole suite is green:

```bash
go vet ./...                                # clean
go build ./...                              # clean
go test -count=1 -timeout 120s ./...        # all green
go test -count=1 -race -timeout 180s ./...  # race-clean
```

If any test now fails for a reason that didn't exist when it was written, fix the test or fix the product per philosophy.

### 3. Run `review-stack` (Step 7)

Invoke the `review-stack` skill (or follow its protocol manually). It walks every layer top-down:

```
intent → documentation → e2e tests → integration tests → unit tests → code
```

For each pair of adjacent layers, look for drift:

- **Intent ↔ README**: every success criterion in INTENT.md has matching documentation in README.md? Every product claim in README is in intent's scope?
- **README ↔ tests**: every command/state/feature in README has a test (e2e for journeys, integration/unit for behaviors)? Every test corresponds to something documented?
- **Tests ↔ code**: every test targets a real public symbol with the right signature? Every public function/type/constant is referenced by at least one test?
- **Cross-cutting**: any references to old field names, removed verbs, or stale paths in docs? Any code paths exercising features not in intent?

Produce a written report (template in `.claude/skills/review-stack/SKILL.md`):

```
REVIEW: aa
DATE: <YYYY-MM-DD>
RESULT: pass | needs-rework

LAYER PAIRS REVIEWED
  intent ↔ README           ✓/✗  <notes>
  README ↔ tests            ✓/✗  <notes>
  tests ↔ code              ✓/✗  <notes>

DRIFT ITEMS
  - <item>  →  <resolution>

REMEDIATION
  <fixes applied or recommended>
```

Apply fixes in precedence order (intent wins over docs, docs win over tests, tests win over code). Commit after each batch of fixes per the commit-before-forward-motion preference.

### 4. Final demonstration + done

Once everything is green and `review-stack` returns PASS:

- Re-run the manual smoke test against the `process` backend to confirm end-to-end behavior. The fixture pattern is documented in the prior commit messages and was used during Step 6:

  ```bash
  rm -rf /tmp/aa-smoke && mkdir -p /tmp/aa-smoke/{home,repo}
  cd /tmp/aa-smoke/repo && git init -q -b main && git config user.email a@b.c && \
    git config user.name a && echo "# smoke" > README.md && \
    cat > aa.json <<'EOF'
  {"image":"n/a","agent":"deterministic"}
  EOF
    git add -A && git commit -qm initial
  mkdir -p /tmp/aa-smoke/home/.aa
  cat > /tmp/aa-smoke/home/.aa/config.json <<'EOF'
  {
    "default_backend": "process",
    "backends": { "process": { "type": "process", "egress_enforcement": "none" } },
    "agents": {
      "deterministic": {
        "run": "sh -c 'mkdir -p $AA_WORKSPACE/.aa && printf \"From abc Mon Sep 17 00:00:00 2001\\nFrom: agent <a@b.c>\\nSubject: [PATCH] smoke\\n---\\n hello.txt | 1 +\\n\\ndiff --git a/hello.txt b/hello.txt\\nnew file mode 100644\\n--- /dev/null\\n+++ b/hello.txt\\n@@ -0,0 +1 @@\\n+hello\\n--\\n2.40.0\\n\" > $AA_WORKSPACE/.aa/result.patch && echo DONE > $AA_WORKSPACE/.aa/state'",
        "env": {}, "egress_allowlist": ["*"]
      }
    },
    "rules": []
  }
  EOF
  HOME=/tmp/aa-smoke/home AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 /workspaces/aa/aa -v
  HOME=/tmp/aa-smoke/home AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 /workspaces/aa/aa status
  HOME=/tmp/aa-smoke/home AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 /workspaces/aa/aa diff
  HOME=/tmp/aa-smoke/home AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 /workspaces/aa/aa kill
  ```

  Expected: session-start banner, DONE state, patch printed, kill tears down everything.

- Final commit summarizing what shipped.
- Tell the user: v1 done, give them the test count, the commit hash, and a one-line description of what they can now do with the binary.

---

## Constraints to honor

### Zero Go module dependencies

`go.mod` is two lines:

```
module aa

go 1.22
```

If you find yourself wanting to `go get` something, stop and find a stdlib equivalent. The intent forbids module deps.

### Strict-mode path list (PHILOSOPHY.md)

These files apply defensive coding (fail loud, validate every field, no shell interpolation, etc.):

- `cmd/aa/egress.go`
- `cmd/aa-proxy/**`
- `cmd/aa/keys_*.go`
- `cmd/aa/patch.go` + `cmd/aa/patch_parser.go`
- `cmd/aa/config.go` + `cmd/aa/config_loader.go`
- `cmd/aa/ssh.go` + `cmd/aa/ssh_runner.go`
- Any code that parses `$AA_WORKSPACE/.aa/*` produced by the agent

Outside these paths, low-ceremony rules apply — internal code trusts internal code.

### Process-backend specifics

- `RunContainer` requires `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1` in the environment of the `aa` invocation. Tests inject this automatically via `tests/e2e/helpers.go`.
- `egress_enforcement` for the process backend MUST be `"none"`. Any other value is a config error.
- The process backend is for dev/test only; never recommend it for real agent work.

### No silent test/prod divergence

If a test affordance ends up in production code (e.g., `AdminAPIBaseURL` field), document it as a real config field with a legitimate use case OR move it behind an explicit env-var gate. Do not let test-only behavior be triggered by the same code path users hit.

### Repo working-tree sync

`SessionManager.StartSession` runs `cp -a <repo>/. <workspace>/` between Mint and InstallEgress, but **only when `host.Address == ""`** (the process backend). This is by design — Docker uses bind-mount and Fly uses scp. If you find yourself wanting to add a `SyncRepo` method to the `Backend` interface, push back: it's not justified by current backends.

---

## User preferences (from memory)

Two preferences are load-bearing for how you communicate:

### Decision framing — no technical menus

When you need user input, do NOT present "Option A: X. Option B: Y. Option C: Z." Instead:

1. First try to decide yourself (intent + philosophy + conversation context).
2. If genuinely under-specified, ask a user-facing business/UX question that pulls the answer out of them. Example: instead of "Should `aa kill` show a per-orphan prompt or destroy silently?" ask "When you `aa sweep`, do you want to see what will be destroyed before it goes?"
3. Code-level option menus only when the user is in "architect mode" — actively reading code or has explicitly asked for the options.

### Commit before significant forward motion

Commit at every natural checkpoint:

- After a `wave-review` returns PASS.
- After a multi-file fix lands and tests pass.
- Before dispatching a new parallel block of work.
- Before applying a new skill or philosophy update.

Use multi-section commit messages with the existing style. Never commit while tests are red for a non-stub reason.

---

## Definition of done

v1 is done when ALL of the following hold:

1. `go vet ./...` clean.
2. `go build ./...` clean.
3. `go test -count=1 -race -timeout 180s ./...` returns `ok` for every package.
4. The manual smoke test (above) succeeds end to end on the `process` backend.
5. `review-stack` PASS report exists, with no remaining drift items.
6. All work is committed; `git status` shows a clean working tree.
7. A final summary message to the user names: total tests, total LOC, commit hash, and what they can now do with the binary.

---

## Quick orientation map

```
/workspaces/aa/
├── INTENT.md               — v1 contract (read first)
├── README.md               — user-facing spec
├── REMAINING_WORK.md       — this file
├── docs/
│   ├── PHILOSOPHY.md       — the lens
│   └── architecture/aa.md  — ADRs + workstream plan
├── plans/plan-1.md         — design conversation that drove intent
├── .claude/skills/         — workflow skills
├── cmd/
│   ├── aa/                 — main binary (~25 .go files; main.go + verb_*.go + session.go + backend_*.go + everything else)
│   └── aa-proxy/           — small forward-proxy binary
└── tests/
    └── e2e/                — 8 journey tests (the ones still needing sweep)
```

If lost: re-read `INTENT.md` and `docs/PHILOSOPHY.md` first, then `docs/architecture/aa.md` § Workstreams to understand the layout, then come back here.
