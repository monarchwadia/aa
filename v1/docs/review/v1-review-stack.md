# Review Stack — aa v1

**Gate:** final alignment review across intent → docs → tests → code.
**Result:** **PASS.**
**Commit at review:** `fd301fa`.

## Test run (authoritative)

```
go vet ./...              → clean
go test -count=1 -race -timeout 180s ./...

ok  aa/cmd/aa        28.5s
ok  aa/cmd/aa-proxy   1.4s
ok  aa/tests/e2e     94.3s
```

- Unit + integration (`cmd/aa`, `cmd/aa-proxy`): 155 tests, all green, 1 skipped (`TestSSH_RealAgainstLoopback` — guarded by `AA_E2E_LOOPBACK=1`, documented).
- E2E (`tests/e2e`): 8 files, 12 top-level tests, all green under `-race`.
- No flakes observed across two back-to-back runs.

## Layer-by-layer alignment

### Intent (`INTENT.md`) ↔ Documentation (`README.md`)

All five intent pillars are covered in the README:

| Intent pillar | README section |
|---|---|
| One-command session start | `aa` with no args, first-time setup flow |
| Detach / reattach is cheap | `aa` status table, `aa attach` |
| The laptop is the trust boundary | Security model table, `aa diff` / `aa push` |
| Rules are guardrails, not gates | `Rules` section, severity table |
| Backends are swappable | `Backends` section, config schema |

No documented feature is outside the intent. No intent goal is silent.

### Documentation ↔ E2E tests

| Documented journey | E2E test |
|---|---|
| First-time setup with missing key | `first_time_setup_test.go` |
| Detach and reattach | `detach_reattach_test.go` |
| Egress allow-list enforcement | `egress_allowlist_test.go` |
| Kill tears down compute | `kill_tears_down_test.go` |
| Push reads patch from laptop (trust boundary) | `push_reads_patch_from_laptop_test.go` |
| Rule violation aborts by default | `push_with_rule_violation_test.go` |
| Session-state table display | `session_states_test.go` |
| Uncommitted-changes warning | `uncommitted_changes_test.go` |

Every README journey has a test. No orphan e2e tests.

### Code ↔ Tests

- Every public verb in `main.go`'s switch (`start`, `attach`, `diff`, `push`, `kill`, `status`, `retry`, `fixture`) has at least one unit or e2e test exercising its happy path.
- `fixture` is the sole hidden verb; refuses without `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1`, and that refusal is covered by the production-safety guard already exercised by every e2e run that sets the var.
- No untested public surface in `session.go`, `state.go`, `rules.go`, `patch.go`, `ssh_runner.go`, `egress.go`, `keys_anthropic.go`, `sessions_file.go`, `config_loader.go`, or any `backend_*.go`.

### Architecture notes (`docs/architecture/aa.md`) ↔ code

- Backend interface split (local / fly / process) — matches.
- SessionManager as the single orchestrator between Backend + Store + KeyProvider + SSH — matches.
- Patch-file handoff as the trust boundary — matches, reinforced by the new laptop cache.
- Egress is enforced inside the sandbox by `aa-proxy`, not by the backend — matches.
- `.aa/state` and `.aa/exit` as the only signals the agent has to communicate — matches; the symmetry between DONE and FAILED prefix handling is now honoured (`session.go` Status + `state.go` ComputeSessionState).

## Drift and honesty

None found. Specifically:

- **Security-model claim** "agent host is not in the review trust path" — backed by `TestDiffAndPushTrustOnlyTheLaptop`, which severs the host between review and push and asserts push either fails loudly or applies the cached patch without re-fetching.
- **"Bare Enter defaults to safe"** — backed by severity-aware `defaultYes` in `Push` and tested by `TestPushWithCIRuleViolationDefaultsToAbort` (error severity → default no → bare Enter aborts).
- **"Kill tears down compute"** — backed by `TestKillTearsDownCompute`; `--host-only` documented in README and tested as part of `TestDiffAndPushTrustOnlyTheLaptop`.

## Non-goals tripwire

The intent's non-goals are: no in-process git implementation, no custom rule DSL, no agent-side policy engine. None were crossed: git remains shelled out, rules are a fixed set of types, and the agent still only writes `.aa/state` / `.aa/exit` / `.aa/result.patch`.

## Philosophy check (`docs/PHILOSOPHY.md`)

- No new defensive code outside the strict-mode path list.
- No premature abstractions introduced in this cleanup.
- New commits shell out to git (`git am`, `git push`) rather than reimplementing.
- Error messages are actionable: every new failure tells the user the exact path to recover (cached patch location, `git am` command).

## Verdict

**PASS.** The v1 stack is coherent top-to-bottom. The binary at `fd301fa` is the shippable artifact.
