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
- Two backends in v1: `local` (laptop Docker) and `fly` (ephemeral Firecracker VMs).

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
- Config is JSON.

## Open questions
- Exact JSON shape for the ephemeral-LLM-key config block (TTL and spend-cap concepts decided; field names TBD).
- Exact JSON shape for backend provisioning fields (Fly API-token reference, VM size, VM TTL — TBD).
- Whether `aa list`, `aa sweep`, and the `script` backend are v1 or deferred to v2.

## History
- 2026-04-23 — initial intent, extracted from `plans/plan-1.md` and `README.md`.
