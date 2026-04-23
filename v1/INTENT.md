# Intent: aa

## Problem
Running a coding agent on a real repo forces a bad tradeoff: give it your credentials and laptop filesystem (unsafe), or a half-sandbox that still holds push access (still unsafe). Nothing on the market offers all of: agent-agnostic, credentials never touch the agent, kernel-level egress allowlist, survives laptop close, single zero-dep binary.

## Personas
- **Solo senior engineer** running agents on client code. Walks away mid-task; cannot afford data leaks or a compromised agent pushing a backdoor.
- **Platform engineer** standardizing how a small team runs agents across repos, laptops, and providers.
- **Security-conscious OSS contributor** who will not adopt agentic tooling without hard structural guarantees against exfil and unreviewed push.

## Success criteria
- A user can run `aa` in a repo, prompt the agent, detach, close the laptop, reopen hours later, and reattach — same session, same container, full scrollback.
- Outbound network from the container is limited to a configured hostname allowlist; prohibited calls attempted by the agent fail at the host kernel, verified by a test that tries them.
- The agent has no git push credentials. The only push path is `aa push` from the user's laptop, using the user's credentials, after showing the patch for review, gated by configurable rules that flag sensitive files (git hooks, CI config, package manifests).
- Ships as one static Go binary, stdlib only.
- Repo config is a two-field `aa.json`. Global config is `~/.aa/config.json`.
- Agent-agnostic: any shell command works in the agent's `run` field. No agent-specific code paths in aa itself.
- Agent ↔ aa contract uses env vars, not fixed absolute paths. `aa` injects `AA_WORKSPACE` (absolute path to this agent's workspace root) and `AA_SESSION_ID` into the agent's environment. By convention, the agent writes `.aa/state` and `.aa/result.patch` under `$AA_WORKSPACE`. This contract is identical across all backends (including future "N agents on one backend" scenarios, where each agent's env carries its own workspace value).
- Three backends in v1: `local` (laptop Docker, container isolation), `fly` (ephemeral Firecracker VMs, VM isolation), and `process` (host child process, NO isolation — dev/test only).

## Non-goals
- IDE integration, hosted service, multi-user sessions, auto-push/auto-merge.
- Protection against an agent writing bad code on purpose or by accident. That is what human review exists for; aa makes review a mandatory step, not a replacement for it.
- Per-repo secrets. Secrets live only in global config.
- Being a general container orchestrator. One agent per session.

## Constraints
- Go, zero module dependencies.
- Shell out to `ssh`, `scp`, `docker`, `git`, `tmux`, `flyctl` — do not reimplement them.
- Provider-agnostic egress enforcement (iptables + forward proxy on the host). Works on any Linux VM, not tied to a single cloud.
- macOS local backend uses a privileged helper container to install iptables rules inside Docker Desktop's VM, or fails loud with `egress_enforcement: "none"` as an explicit opt-out. No silent downgrade.
- The `process` backend is opt-in only, gated by `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1` in the environment. It forces `egress_enforcement: "none"` (cannot enforce egress on a host-level process without interfering with the user's own networking) and prints a loud no-isolation warning at every session start. It exists so the dev loop and test suite can run without Docker; it is NOT intended for running real agents on untrusted code.
- Config is JSON.

## Command surface in v1
- Core verbs: `aa`, `aa status`, `aa attach`, `aa diff`, `aa push`, `aa kill`, `aa retry`.
- Housekeeping verbs: `aa list` (sessions across repos), `aa sweep` (orphaned resources), `aa init` / `aa init --global`, `aa version`.

## Open questions
- Exact JSON shape for the ephemeral-LLM-key config block (TTL and spend-cap concepts decided; field names TBD).
- Exact JSON shape for backend provisioning fields (Fly API-token reference, VM size, VM TTL — TBD).
- `script` backend (user-provided shell implementing the backend interface) is deferred to v2.

## History
- 2026-04-23 — initial intent, extracted from `plans/plan-1.md` and `README.md`.
- 2026-04-23 — closed open questions on command surface: `aa list`, `aa sweep`, `aa init`, `aa version` are v1; `script` backend is v2.
- 2026-04-23 — added `process` backend to v1 scope. Opt-in, no isolation, dev/test only. Motivated by unblocking integration tests on any laptop (Docker-free) and speeding the dev loop. Does NOT replace `local` or `fly` for real agent work.
- 2026-04-23 — locked agent-environment contract as `AA_WORKSPACE` + `AA_SESSION_ID` env vars, with state/patch/log as conventions under `$AA_WORKSPACE/.aa/`. Chosen for backend portability and forward-compat with multi-agent-per-backend.
