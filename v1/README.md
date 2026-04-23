# aa

**Run coding agents in an isolated remote sandbox. Orchestrated entirely from your laptop. Your git credentials never leave it.**

`aa` (short for "agent") starts a containerized coding agent (Claude Code, Aider, or anything else) on a remote or local Linux host, lets you prompt it, detach, walk away, come back hours later, review what it did as a plain-text patch, and push to origin from your own machine — using your own credentials, which the agent never sees.

It is not a framework, an IDE plugin, or a hosted service. It is a single static binary that wraps SSH, Docker, and standard Unix tools with a small state machine and an opinionated security posture.

---

## Table of contents

- [What problem does aa solve?](#what-problem-does-aa-solve)
- [Concepts](#concepts)
- [Mental model](#mental-model)
- [Installation](#installation)
- [Quickstart](#quickstart)
- [The two config files](#the-two-config-files)
- [Command reference](#command-reference)
- [Session lifecycle](#session-lifecycle)
- [Session states](#session-states)
- [Review and push flow](#review-and-push-flow)
- [Rules (patch safeguards)](#rules-patch-safeguards)
- [Egress allowlisting](#egress-allowlisting)
- [Backends](#backends)
- [Credentials and ephemeral API keys](#credentials-and-ephemeral-api-keys)
- [Security model: what aa does and does not protect against](#security-model)
- [Troubleshooting](#troubleshooting)
- [Non-goals](#non-goals)

---

## What problem does aa solve?

You want to hand a long-running task to a coding agent and walk away. When you come back, you want to review what it did and push. You do not want:

- The agent to be able to push to your repo directly (even accidentally).
- The agent to have access to your laptop's filesystem, SSH keys, cloud credentials, or browser sessions.
- A compromised or prompt-injected agent to exfiltrate your data to arbitrary internet hosts.
- Your laptop to have to stay online and attached for the agent to keep working.
- To learn a complicated tool.

`aa` is built for that.

**Key architectural decisions:**

| Decision | Consequence |
|---|---|
| The agent runs in a container on a **remote or local Linux host**, not on your laptop. | Agent cannot read your `~/.ssh`, `~/.aws`, browser cookies, or other repos. |
| The container has **no git push credentials**. | Even if the agent is fully compromised, it cannot push malicious code. |
| The agent writes its result as a **git patch file**. Your laptop reads it over SSH, applies it locally, and pushes using your real git credentials. | Credentials never leave your laptop. The patch is plain text — easy to review, easy to audit. |
| The container's **egress is locked down at the host kernel level** to a hostname allowlist (default: just the agent's API endpoint). | A prompt-injected agent cannot exfil to arbitrary hosts — only the hosts you explicitly allow. |
| Sessions **survive your laptop closing**. Reattach with `aa`. | You start the agent, detach, close your laptop, come back hours later, reattach. |
| `aa` is a **single static binary, zero Go dependencies**. Config is JSON. | Install with one download. Nothing to manage. |

---

## Concepts

`aa` has two primary concepts you will interact with constantly. Read these once and the rest of the documentation snaps into place.

### Agent

**An *agent* is the software that does the coding work inside the sandbox.** Claude Code, Aider, Cursor CLI, a custom Python script, a deterministic shell script — anything you can launch from a command line.

An agent is defined in your **global** config (`~/.aa/config.json`) as a named entry:

```json
"agents": {
  "claude-code": {
    "run":              "claude --dangerously-skip-permissions",
    "env":              { "ANTHROPIC_API_KEY": "keyring:anthropic" },
    "egress_allowlist": ["api.anthropic.com"]
  }
}
```

- `run` — the shell command that launches the agent. `aa` passes it to `bash -lc` inside the sandbox, verbatim.
- `env` — environment variables the agent needs, resolved on your laptop from keyring references before the agent starts.
- `egress_allowlist` — the hostnames the agent is permitted to reach (enforced at the host kernel; see [Egress allowlisting](#egress-allowlisting)).

`aa` does not know or care what the agent does internally. Adding a new agent is a new entry in the global config — **zero code changes to `aa`**. That is what "agent-agnostic" means.

Your repo's `aa.json` picks which agent to use by name: `"agent": "claude-code"`.

### Backend

**A *backend* is the infrastructure that provides the sandbox where the agent runs.** Docker on your laptop, a Fly.io microVM, a plain child process on your laptop.

A backend is defined in the same global config:

```json
"backends": {
  "local":   { "type": "local",   "egress_enforcement": "strict" },
  "fly":     { "type": "fly",     "egress_enforcement": "strict" },
  "process": { "type": "process", "egress_enforcement": "none"   }
}
```

- `type` — the backend implementation (see [Backend types shipped in v1](#backend-types-shipped-in-v1)).
- `egress_enforcement` — `"strict"` or `"none"`; controls whether the host-kernel egress rules are installed.

`aa` knows everything about the backend: it provisions it, installs egress controls, runs the agent inside it, tears it down. The `default_backend` field in global config picks which backend is used.

### Relationship: orthogonal

Any agent runs on any backend:

| | `local` | `fly` | `process` |
|---|---|---|---|
| `claude-code` | Claude in Docker, egress→anthropic | Claude in microVM, egress→anthropic | Claude as laptop process, no isolation |
| `aider` | Aider in Docker, egress→openai | Aider in microVM, egress→openai | Aider as laptop process, no isolation |

The combination — which agent on which backend — is what `aa` provisions at session start.

### The agent ↔ aa environment contract

When `aa` starts an agent, it injects two environment variables into the agent's shell:

| Variable | Value |
|---|---|
| `AA_WORKSPACE` | absolute path to **this** agent's workspace root. The repo's working tree is materialized here. |
| `AA_SESSION_ID` | opaque string identifying this session. Useful in agent logs for correlating with `aa list` output. |

**Conventions** (not env, documented): within `$AA_WORKSPACE`, the agent uses a reserved `.aa/` subdirectory to communicate with `aa`:

| Path | Purpose |
|---|---|
| `$AA_WORKSPACE/.aa/state` | State file the agent writes on exit: `DONE`, or `FAILED: <reason>`. |
| `$AA_WORKSPACE/.aa/result.patch` | The git-format patch the agent produces (`git format-patch origin/<branch>..HEAD --stdout`). |
| `$AA_WORKSPACE/.aa/agent.log` | Agent's own log output, if it writes one. Tailed by `aa` for display. |

The two env vars and the three file paths are the **entire** contract between `aa` and an agent. Everything else is the agent's business.

This contract is identical across all backends — the paths are meaningful to the agent regardless of whether its `$AA_WORKSPACE` resolves to `/workspace` inside a container, `/home/fly/ws` on a Firecracker VM, or `~/.aa/workspaces/<id>` on your laptop under the `process` backend. It also supports a future world where multiple agents share one backend: each agent gets its own `$AA_WORKSPACE` at spawn time.

---

## Mental model

```
  your laptop                        agent host (ephemeral VM or local)
 ┌────────────┐                     ┌──────────────────────────────────┐
 │            │                     │  ┌────────────────────────────┐  │
 │   aa CLI   │◀──── SSH / tmux ───▶│  │  container                 │  │
 │            │                     │  │    agent runs here         │  │
 │            │                     │  │    writes result.patch     │  │
 │  git       │                     │  │    egress: allowlist only  │  │
 │  creds     │                     │  └────────────────────────────┘  │
 │  (stay     │                     │  ▲                               │
 │   here)    │                     │  │ kernel iptables + forward proxy│
 │            │                     │  │ enforce hostname allowlist    │
 └────────────┘                     └──────────────────────────────────┘
       │                                           │
       │       1. aa start: provision + sync repo + attach
       │       2. you prompt the agent, detach (Ctrl-b d), close laptop
       │       3. agent keeps running on remote
       │       4. aa (later): reattach, see status, read log
       │       5. aa push: pull result.patch, review, apply locally, push to origin
       │       6. aa teardown: destroy VM, revoke API key
       ▼
   origin (GitHub, etc.)
```

**`aa` has three verbs you will use constantly:** `aa`, `aa push`, `aa kill`. Everything else is for recovery and scripting.

---

## Installation

```bash
# Linux / macOS
curl -LO https://github.com/<org>/aa/releases/latest/download/aa
chmod +x aa
sudo mv aa /usr/local/bin/

aa --version
```

`aa` is a single static binary. No runtime dependencies.

It **shells out** to these standard tools, which you probably already have:

| Tool | Used for | Required if |
|---|---|---|
| `ssh` | Remote attach, remote commands | Using a remote backend |
| `scp` | File transfer | Using a remote backend |
| `docker` | Local container runtime | Using `local` backend |
| `git` | Bundling repo, applying patches | Always |
| `tmux` | Detachable sessions (on agent host) | Always (installed by aa on agent host) |
| `flyctl` | Fly.io provisioning | Using `fly` backend |

### First-time setup

```bash
aa init --global
```

This writes a starter `~/.aa/config.json` with a `local` backend, a Claude Code agent entry, and the default rule set. See [The two config files](#the-two-config-files) for what lives in it.

Then, inside any repo you want to use `aa` in:

```bash
cd your-repo
aa init
```

This writes a two-line `aa.json` at the repo root, committable and shareable. See [`aa.json` schema](#repo-config-aajson).

---

## Quickstart

Assuming you've done the first-time setup and have `ANTHROPIC_API_KEY` in your keyring:

```bash
cd your-repo
git checkout -b feature/my-task

aa
```

If your working tree has uncommitted changes, `aa` warns before proceeding — those changes will be synced into the container:

```
  ⚠  Uncommitted changes in feature/my-task:
       M  src/auth/provider.ts
       M  src/auth/session.ts
       ?? src/auth/oauth.ts

     These will be included in the session. Continue? [y/N]
```

Once you confirm, `aa` provisions, starts the container, and prints a **session-start banner** that surfaces every security tradeoff that's in effect:

```
  ◆ starting session: myapp / feature/my-task
  ⚡ egress allowlist: api.anthropic.com
  ⚡ backend: fly (ephemeral)
  ⚡ ephemeral key: TTL 8h, spend cap $50
```

If a tradeoff is less secure than the recommended baseline, the ⚡ becomes a ⚠ — e.g. `⚠ egress: UNRESTRICTED` when the allowlist is `["*"]`, or `⚠ backend: persistent (not ephemeral)` when you're using a long-lived agent host. See [Egress allowlisting](#egress-allowlisting) and [Backends](#backends) for what each of those costs you.

Then `aa` attaches you into Claude Code, inside the container. You type a prompt at Claude:

> Implement OAuth with Google and GitHub. Add tests. Commit each provider separately.

Claude starts working. You watch for a minute. Then `Ctrl-b d` to detach. Close your laptop.

Two hours later:

```bash
cd your-repo
aa                      # reattaches; you see scrollback and current state
```

Claude says it's done. You detach again.

```bash
aa diff                 # pulls result.patch, shows you the diff locally
aa push                 # applies locally, pushes to origin, tears down remote VM
```

Done.

---

## The two config files

`aa` deliberately splits config into two files with different lifecycles:

| File | Scope | Committed? | Contains secrets? |
|---|---|---|---|
| `~/.aa/config.json` | Your laptop, all repos | No (stays on your laptop) | References to keyring entries, not secrets themselves |
| `aa.json` (or `.aa.json`) | This repo | **Yes — commit it** | No, ever |

### Repo config (`aa.json`)

Dead simple. Two fields:

```json
{
  "image": ".devcontainer/Dockerfile",
  "agent": "claude-code"
}
```

| Field | Meaning |
|---|---|
| `image` | Path (relative to repo root) to a `Dockerfile` or `docker-compose.yml`. Defines the agent's sandbox environment. |
| `agent` | Name of an agent defined in your global config. Picks the command and env vars to run inside the container. |

That's it. Everything else is either inferred (branch, session name from repo+branch) or defaulted (push to current branch, backend from global config).

The repo config contains **no secrets** and **no infrastructure details**. Anyone who clones the repo and has `aa` set up on their machine can just run `aa`.

### Global config (`~/.aa/config.json`)

This is where your infrastructure, credentials, agents, and rules live.

```json
{
  "default_backend": "local",

  "backends": {
    "local": {
      "type": "local",
      "egress_enforcement": "strict"
    },
    "fly": {
      "type": "fly",
      "region": "iad",
      "egress_enforcement": "strict"
    }
  },

  "agents": {
    "claude-code": {
      "run": "claude --dangerously-skip-permissions",
      "env": {
        "ANTHROPIC_API_KEY": "keyring:anthropic"
      },
      "egress_allowlist": ["api.anthropic.com"]
    },
    "aider": {
      "run": "aider --yes",
      "env": { "OPENAI_API_KEY": "keyring:openai" },
      "egress_allowlist": ["api.openai.com"]
    }
  },

  "rules": [
    { "type": "gitHooksChanged",        "severity": "error" },
    { "type": "ciConfigChanged",        "severity": "error" },
    { "type": "packageManifestChanged", "severity": "warn"  },
    { "type": "lockfileChanged",        "severity": "warn"  },
    { "type": "dockerfileChanged",      "severity": "warn"  },
    { "type": "buildScriptChanged",     "severity": "warn"  }
  ]
}
```

> **Fields marked proposed, not yet specified:**
>
> - Backend-specific provisioning fields beyond `type`, `region`, and `egress_enforcement` — e.g. Fly API-token reference, VM size, VM TTL. The plan establishes the concepts (ephemeral per-session VM, Fly.io as the v1 remote backend) but not the exact JSON shape. Settled during implementation.
> - The ephemeral-LLM-key config block. The plan establishes: the laptop holds an admin key; at session start `aa` mints a session-scoped key with an aggressive TTL (default 8h) and a spend cap (default $50); on teardown the laptop revokes it. The JSON shape for configuring this is TBD.
> - Per-repo override shape for allowlist/env. The plan establishes that per-repo override exists and that repo-level allowlist *adds to* (does not replace) the global allowlist, and repo wins on env conflicts. The exact field names in `aa.json` are TBD.

### Secret references

The plan establishes one reference syntax: `keyring:<name>`. `aa` resolves these on your laptop at session start and injects the resolved values into the container env. Secrets are never written to disk on the agent host.

Additional reference schemes (env-var, file-on-disk, password-manager CLIs) are plausible extensions but not part of the v1 spec.

---

## Command reference

`aa` has a small, deliberate verb surface. Run `aa help` for this list.

| Command | Purpose |
|---|---|
| `aa` | **Attach.** Start a new session (if none exists for this repo+branch) or reattach to a running one. If the session has already finished, shows status instead. |
| `aa status` | Print session status as a one-screen summary. Never attaches. Safe to pipe. |
| `aa attach` | Force attach to a container shell, regardless of state. Drops you into bash, not the agent, if the agent has exited. "I know what I'm doing." |
| `aa diff` | Pull the agent's `result.patch` and show it locally, piped through `$PAGER`. Always computed on your laptop — never trusts the agent host. |
| `aa push` | Finalize: pull patch, run rules, prompt for review, apply to local clone, `git push` using your laptop credentials, tear down remote. |
| `aa kill` | Abandon the session. Tear down the remote VM, revoke the ephemeral API key, delete local session state. Work is discarded. |
| `aa retry` | Only valid in `LIMBO` or `FAILED` states. Restart the agent inside the existing workspace, preserving files. |
| `aa sweep` | List and optionally clean up orphaned resources — VMs, relay hosts (legacy), or ephemeral API keys your laptop provisioned but lost track of. |
| `aa init` | Write a starter `aa.json` in the current repo. |
| `aa init --global` | Write a starter `~/.aa/config.json`. |
| `aa list` | List all active sessions across all repos on this laptop. |
| `aa version` | Print the binary version. |

**All commands are idempotent.** Running `aa` in the same repo always does the right thing for the current session state. You rarely need flags.

---

## Session lifecycle

```
   [no session]
        │
        │ aa
        ▼
   PROVISIONING ──(failure)──▶ FAILED_PROVISION
        │
        │ (host up, container started, you're attached)
        ▼
     RUNNING ◀───────┐
        │            │ aa (reattach)
        │            │
        │ (Ctrl-b d — detach; session keeps running)
        │
        │ (agent process exits)
        ▼
    ┌────────────────────────────────┐
    │ one of four terminal states:   │
    │                                │
    │   DONE         — clean exit    │
    │   FAILED       — clean fail    │
    │   LIMBO        — no state file │
    │   INCONSISTENT — contradictory │
    └────────────────────────────────┘
        │
        │ aa push     aa kill      aa retry
        ▼             ▼             │
     PUSHED       TORN_DOWN         │
        │             │             ▼
        └─────┬───────┘          RUNNING (again)
              ▼
         TORN_DOWN
```

`aa` tracks this state in two places:

- **On your laptop:** `~/.aa/sessions/<repo>-<branch>.json` — holds the host address, SSH key, ephemeral API key handle, backend name.
- **On the agent host:** `$AA_WORKSPACE/.aa/state` — a plain text file the agent writes when it finishes. `$AA_WORKSPACE` resolves differently per backend (see [Concepts](#concepts)) but the relative location under it is fixed.

Your laptop is always the source of truth for "does this session exist and where is it?" The agent host is the source of truth for "what did the agent finish with?"

---

## Session states

When the agent process inside the container exits, `aa` has to figure out what happened. There are exactly four possibilities.

### 1. `DONE` — clean success

The agent ran `aa-done` (a small helper script `aa` ships into the sandbox) before exiting, which writes `$AA_WORKSPACE/.aa/state` with `DONE`.

```
  ◆ myapp / feature/oauth — DONE

  agent reported success:
    "Implemented OAuth2 with Google and GitHub. 4 commits, all tests passing."

  commits ready to push:
    a1b2c3d  feat: add OAuth2 provider interface
    d4e5f6a  feat: implement Google OAuth flow
    b7c8d9e  feat: implement GitHub OAuth flow
    f1a2b3c  test: add OAuth integration tests

  next:
    aa diff   review changes
    aa push   ship to origin
    aa kill   discard and tear down
```

### 2. `FAILED` — clean failure

The agent ran `aa-fail "<message>"`, which writes `FAILED` plus a reason.

```
  ◆ myapp / feature/oauth — FAILED

  agent reported failure:
    "Unable to resolve dependency conflict in package.json.
     Left workspace in partial state on branch feature/oauth."

  exit code: 1

  next:
    aa attach   reattach to investigate (container still up)
    aa diff     review partial changes
    aa push     ship anyway (partial work)
    aa kill     discard and tear down
```

The container is kept alive so you can `aa attach` and poke around. You get a shell, not the agent.

### 3. `LIMBO` — process exited, no state file

The agent died without signaling. Could be OOM, a segfault, the user typed `exit` without running `aa-done`, or the container's PID 1 was killed.

```
  ◆ myapp / feature/oauth — LIMBO

  the agent process exited without reporting a result.
  no state file was written. cause is unknown.

  exit code: 137 (likely OOM or killed)
  last 20 lines of agent log:

    > Running tests on OAuth integration...
    > node: FATAL ERROR: CALL_AND_RETRY_LAST Allocation failed
    > (no further output)

  the workspace may or may not contain useful changes.

  next:
    aa attach   drop into shell to inspect workspace
    aa diff     review any changes made before exit
    aa push     ship what's there (risky — partial work)
    aa kill     discard and tear down
    aa retry    restart the agent in the same workspace
```

`aa retry` is only meaningful here — if it was a transient crash, you can resume.

### 4. `INCONSISTENT` — state file and exit code disagree

Rare but real. State file says `DONE` but the process exited nonzero (usually because a post-hook failed after the agent signaled completion).

```
  ◆ myapp / feature/oauth — INCONSISTENT

  the agent reported DONE but exited with code 2.
  this is unusual and may indicate a problem.

  agent message:  "Completed OAuth implementation."
  exit code:      2
  last 20 lines of agent log:
    > Committed feature/oauth
    > aa-done
    > $AA_WORKSPACE/.aa/post-hook.sh: line 4: git: command not found

  next:
    aa attach   reattach to investigate
    aa diff     review changes
    aa push     trust the DONE, ship anyway
    aa kill     discard and tear down
```

`aa` shows both signals and lets you decide. It never silently picks.

---

## Review and push flow

When you run `aa push`, here's what happens, in order, on **your laptop**:

1. Read the patch bytes from the backend's storage — SSH `cat` for a remote backend, `docker cp` for `local`, direct filesystem read for `process` — from `$AA_WORKSPACE/.aa/result.patch`.
2. Pipe the patch through `git apply --stat` and `git apply --numstat` to get the file list.
3. Match file list against configured [rules](#rules-patch-safeguards).
4. Display rule violations, patch summary, and prompt for confirmation.
5. If confirmed: `git checkout -b <branch>` in a clean local clone, `git am < result.patch`, `git push origin <branch>`.
6. On successful push: tear down the remote VM, revoke the ephemeral API key, delete local session state.

Steps 1–4 happen **before** any code is applied anywhere. You see the patch as text. You decide. The agent host has no role in review.

### Why a patch file, not a bundle?

Earlier versions of this design used a zipped working tree shipped via SFTP. Replacing that with a single text file from `git format-patch origin/<branch>..HEAD` collapses the architecture:

- No tar extraction, no zip-slip attacks, no symlink attacks.
- Patch is human-readable — you can `cat`, `grep`, review it with standard tools.
- No separate "relay" server is needed: your laptop is the relay.
- The agent is forced to commit before exit, which is a useful discipline.

Binary files use `git format-patch --binary`. Works fine for the small binary changes most agents produce.

---

## Rules (patch safeguards)

Before applying a patch, `aa` scans it for changes to files that warrant extra scrutiny — git hooks, CI config, package manifests, build scripts — and flags them.

Rules are **ESLint-style**: each has a `type` and a `severity`.

```json
"rules": [
  { "type": "gitHooksChanged",        "severity": "error" },
  { "type": "ciConfigChanged",        "severity": "error" },
  { "type": "packageManifestChanged", "severity": "warn"  },
  { "type": "fileChanged", "severity": "error", "include": ["infra/**", "terraform/**"] }
]
```

### Severities

| Severity | Behavior |
|---|---|
| `off`   | Rule is disabled. |
| `warn`  | Violation is shown but the push proceeds to the default `[a] accept` prompt. |
| `error` | Violation is shown and the prompt defaults to abort. User must actively type `y`. |

### Built-in rule types

| Type | Files it watches | Why |
|---|---|---|
| `gitHooksChanged` | `.githooks/**`, `.husky/**`, `.gitattributes` | Committed hooks run on contributor machines. Filter drivers in `.gitattributes` can execute arbitrary code on checkout. |
| `ciConfigChanged` | `.github/workflows/**`, `.gitlab-ci.yml`, `.circleci/**`, `azure-pipelines.yml`, `.drone.yml` | CI runs with deploy credentials. Modifying CI is a classic supply-chain pivot. |
| `packageManifestChanged` | `package.json`, `pyproject.toml`, `setup.py`, `Cargo.toml`, `Gemfile`, `go.mod` | `scripts` / install hooks run arbitrary code on `npm install` etc. |
| `lockfileChanged` | `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `poetry.lock`, `Cargo.lock`, `Gemfile.lock` | Usually legitimate. Default `warn`. |
| `dockerfileChanged` | `**/Dockerfile`, `docker-compose*.yml` | Build-time code execution. |
| `buildScriptChanged` | `Makefile`, `justfile`, `Taskfile.yml`, `scripts/**`, `*.sh` at repo root | Targets invoked by `make install` or docs are high-risk. |
| `fileChanged` | User-specified via `include` globs | Generic escape hatch for org-specific sensitive paths. |

### What a violation looks like at `aa push`

```
$ aa push

  Fetched patch from agent (2.3 KB, 4 commits).

  ⛔ Rule violation: gitHooksChanged (error)

  Committed git hooks and filters execute on contributor machines
  after pull. Attacks this rule guards against:
    • Credential theft via pre-commit reading ~/.ssh, ~/.aws
    • Supply-chain via gitattributes filter driver
    • Persistence via self-reinstalling hooks

  Files:
    M  .githooks/pre-commit
    A  .gitattributes

  ⚠  Rule violation: packageManifestChanged (warn)

  Files:
    M  package.json

  [r] view full diff   [s] view flagged files only
  [a] accept and push  [q] abort
  > _
```

`[s]` is often the right choice — skim the rest, read the flagged bits.

### `aa init` defaults

`aa init --global` writes this rule block, with comments explaining each. You can add `fileChanged` entries for your own sensitive paths (`infra/`, `terraform/`, `kubernetes/`, whatever your org cares about).

---

## Egress allowlisting

One of `aa`'s core features is locking down where the container can make network requests to. **This is enforced at the VM kernel level, not inside the container.** A prompt-injected or fully compromised agent cannot bypass it.

### How it works

Three layers of enforcement, all outside the container:

1. **VM kernel firewall (`iptables` on the agent host)** drops all outbound packets from the container bridge except to a single destination: the forward proxy running on the VM host.
2. **A forward proxy on the host** (tinyproxy or equivalent, in a separate process from the container) accepts only `CONNECT` requests to hostnames in the allowlist. Everything else gets a `403`.
3. **DNS is blocked outbound from the container.** The container's resolver points at `127.0.0.1`, which answers nothing. Hostname resolution happens inside the proxy, scoped to the allowlist. This closes DNS-tunnel exfil.

The container can change its own routing tables, unset `HTTPS_PROXY`, try direct connections — none of that matters. Every packet that leaves the container hits the host kernel firewall, which drops it.

### Config

```json
"agents": {
  "claude-code": {
    "egress_allowlist": ["api.anthropic.com"]
  }
}
```

Default for a new agent should be **the agent's API endpoint, and nothing else**. Expand deliberately:

```json
"egress_allowlist": [
  "api.anthropic.com",
  "registry.npmjs.org",
  "pypi.org",
  "github.com"
]
```

Every addition is an exfil channel. Add consciously.

### The `["*"]` escape hatch

```json
"egress_allowlist": ["*"]
```

`aa` skips the firewall and proxy entirely. Container gets normal network. At every session start:

```
  ◆ starting session: myapp / feature/oauth
  ⚠  egress: UNRESTRICTED (allowlist is "*")
     The agent can reach any host on the internet.
     This is your explicit choice; proceed accordingly.
```

You opted in; the consequence is surfaced every session.

### Residual risk

Even with a strict allowlist of just `api.anthropic.com`, that endpoint is an **outbound channel**. A malicious agent could encode data into prompts. You can't close this without cutting off the agent. The defense is: your ephemeral key has a tight TTL and spend cap, and the threat model is "prompt-injected agent," not "nation-state adversary with training data access." For most teams, this tradeoff is fine. For top-secret code, don't use an LLM agent at all.

---

## Backends

A **backend** tells `aa` where to run the container and how to install egress controls on the host.

### Backend types shipped in v1

| Type | Host | Isolation | Use case |
|---|---|---|---|
| `local` | Your laptop's Docker | Container + egress allowlist | Development with real isolation; offline work; fast iteration |
| `fly` | Fly.io Machines (Firecracker microVMs) | Hypervisor + egress allowlist | Ephemeral per-session VMs; 1-3s cold start; strongest isolation |
| `process` | Host child process on your laptop | **None** | Dev loop and test suites only. No Docker needed. **Not safe for real agents.** |

### The `process` backend

`process` runs the agent as an ordinary child process on your laptop — no container, no VM, no egress enforcement. It exists so the dev loop and the integration-test suite can run on any machine where Go runs, including CI runners without Docker.

**Three guardrails keep it from being a foot-gun:**

1. **Opt-in required.** `aa` refuses to use this backend unless `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1` is set in the environment of the `aa` invocation. Missing the flag fails loudly at session start — no silent fallback.
2. **`egress_enforcement` is forced to `"none"`.** Any other value in config is a configuration error. You cannot accidentally think egress is enforced under `process`.
3. **Loud no-isolation banner at every session start:**
   ```
   ⚠  backend: process — NO ISOLATION
      Agent has full access to your laptop: filesystem, env vars,
      SSH keys, network. Dev/test use only. For real agent work,
      use `local` or `fly`.
   ```

**What `process` is useful for:**
- Writing and running the e2e/integration tests for `aa` itself without needing Docker.
- Iterating on `aa`'s own CLI/verb behaviour where the security boundary isn't the thing being tested.
- A quick smoke check that a new agent's `run` command is wired correctly before committing a container image.

**What `process` is NOT for:**
- Running an agent on code you didn't write. It cannot protect you.
- Anything where the `aa` security story (isolation, egress allowlist) is load-bearing.

### Deferred to v2

| Type | Status |
|---|---|
| `ssh` | Bring-your-own VM. Easy to add — mostly shared with the remote path of `fly`. |
| Other clouds (AWS, GCP, Hetzner) | Each is ~150 LOC of API-talking code. |
| `script` | User-provided shell script implementing `provision`, `install-egress`, `run`, `teardown`. The official extension point for unsupported providers. |

### `egress_enforcement` field

Every backend supports one knob:

```json
"backends": {
  "local": {
    "type": "local",
    "egress_enforcement": "strict"   // or "none"
  }
}
```

- `strict` (default): `aa` installs firewall rules and the proxy. If enforcement cannot be installed (e.g. privileged containers are disabled on macOS Docker Desktop by corporate policy), **`aa` refuses to start the session**. No silent downgrade.
- `none`: `aa` skips enforcement entirely. Surfaced at every session start as a warning.

### macOS local backend

macOS runs Docker Desktop in a Linux VM. To install `iptables` rules in that VM, `aa` runs a **privileged helper container** with `--net=host --privileged` on session start. This installs the rules, then exits.

If privileged containers are blocked (many corporate machines do this), strict mode fails loud with three options:

```
✗ Cannot start session: egress enforcement failed

  macOS Docker Desktop requires a privileged helper container to
  install egress firewall rules. Starting that container failed:

    Error: privileged containers are disabled in Docker Desktop settings

  Options:
    1. Enable privileged containers in Docker Desktop
       Settings → Advanced → Allow privileged containers

    2. Switch to a remote backend (recommended for isolation):
       Edit ~/.aa/config.json, set "default_backend" to "fly"

    3. Disable egress enforcement (not recommended):
       Edit ~/.aa/config.json, backends.local.egress_enforcement = "none"
```

**Recommendation on security-sensitive work on macOS: use `fly`.** Real VM isolation, strong egress control, bounded cost, fast provisioning.

**For dev/test iteration on macOS where Docker Desktop privileged containers aren't available or desired**, the `process` backend is a no-isolation alternative. It requires `AA_ALLOW_UNSAFE_PROCESS_BACKEND=1` and runs the agent as a regular macOS process with full laptop access. Useful for quickly smoke-testing an agent's `run` command or iterating on `aa` itself; not for running real agents on code you didn't write. See [The `process` backend](#the-process-backend).

### Persistent vs ephemeral

A backend is **ephemeral** if it provisions a fresh VM per session and destroys it on teardown (`fly`, a future `aws-fargate`, etc.). `aa` **recommends ephemeral** for security-sensitive work, because:

- Compromise can't persist across sessions.
- No orphan state accumulates.
- Teardown is a hard boundary, not a best-effort cleanup.

If you use a persistent backend (e.g. `ssh` to your own always-on VM), `aa` warns at session start:

```
  ⚠  You are using a persistent agent host.
     Container isolation is strong, but a compromised container
     could affect future sessions on this host.

     Recommended: switch to an ephemeral backend (fly, etc.)
```

---

## Credentials and ephemeral API keys

### The credential flow

1. **Laptop holds the admin key** for your LLM provider (e.g. `ANTHROPIC_ADMIN_API_KEY`). Never leaves the laptop.
2. At `aa start`, the laptop calls the provider's Admin API to **create a fresh session-scoped key** named `aa-<repo>-<branch>-<timestamp>`, with:
   - **TTL** (default 8 hours — long enough for unattended work, short enough that an exfiltrated key expires soon)
   - **Spend cap** (default $50 — bounds damage if the agent goes haywire)
3. That session key is injected into the container as `ANTHROPIC_API_KEY` (or equivalent).
4. On `aa push` or `aa kill` — or on laptop-initiated crash recovery — the laptop calls DELETE on the session key.
5. If the laptop dies and never cleans up, the TTL expires the key anyway.

### `aa sweep`

If your laptop crashes, loses state, or is wiped before cleanup, you may have orphans:

- API keys that didn't get revoked (but will expire eventually).
- VMs that didn't get torn down (costing you money until you notice).

```bash
aa sweep
```

Queries each configured backend for resources tagged `aa-*`, cross-references against `~/.aa/sessions/`, lists orphans, and prompts to clean each one up.

### Supported key providers

| Provider | Ephemeral keys? | TTL support | Spend cap support |
|---|---|---|---|
| Anthropic Admin API | Yes | Yes | Yes |
| OpenAI | Project-scoped keys | Partial | Partial |
| (others) | Defaults to using the static key if ephemeral is not available; surfaced as a warning at session start |

### Overriding the admin API endpoint

Any agent entry can set an `admin_api_base_url` field to override the default Admin API endpoint used for ephemeral-key lifecycle:

```json
"agents": {
  "claude-code": {
    "run": "claude --dangerously-skip-permissions",
    "env": { "ANTHROPIC_API_KEY": "keyring:anthropic" },
    "egress_allowlist": ["api.anthropic.com"],
    "admin_api_base_url": "https://my-enterprise-anthropic.example.com"
  }
}
```

Two legitimate uses:

1. **Self-hosted / enterprise Anthropic deployments** with their own admin API endpoint.
2. **Tests**, which point this at a local `httptest.Server` to exercise the key lifecycle without touching production.

The field is not a secret — it's a URL. The admin key itself still resolves via `keyring:` or equivalent.

---

## Security model

`aa` is honest about what it defends and what it doesn't. **Architectural isolation is not a substitute for code review.**

### What aa protects against

The `Backends` column names which backends each protection applies to. A missing backend in that column means the protection is **not in force** on that backend.

| Threat | Mitigation | Backends |
|---|---|---|
| Agent exfiltrating your git credentials | Credentials never leave your laptop; agent has no push access. | `local`, `fly`, `process` |
| Agent trashing your laptop filesystem | Container/VM is a separate filesystem. Only the repo's working tree is synced. | `local`, `fly` |
| Agent persisting between sessions | Ephemeral backend: fresh VM per session, destroyed at teardown. | `fly` |
| Agent exfiltrating to arbitrary hosts | Egress allowlist enforced at host kernel. Container cannot bypass. | `local`, `fly` |
| Agent consuming unbounded resources on your laptop | Not on your laptop. | `fly` |
| Agent reading other projects on your laptop | Only the current repo is synced into the sandbox. | `local`, `fly` |
| Agent forging the diff you review | `aa` reads raw patch bytes from the backend's storage (SSH `cat` for remote backends, filesystem read for local/process). Diff rendering and rule evaluation happen on the laptop — the agent never formats what you see. | `local`, `fly`, `process` |
| Agent burning unbounded LLM spend | Ephemeral keys have a spend cap and TTL. | `local`, `fly`, `process` |

**If you are using `process`:** the only protections in force are the credential-isolation promise (the agent still doesn't get git push credentials; `aa push` still runs on your laptop with your creds after review), the review trust path (rendering happens on the laptop), and ephemeral-key controls. Everything else — filesystem isolation, egress allowlist, resource bounds, cross-project privacy — is OFF. This is deliberate: `process` is a dev/test backend, not a security posture.

### What aa does NOT protect against

| Threat | Why |
|---|---|
| **Agent writing malicious code you review and push anyway.** Prompt injection via a crafted README, dependency, or search result can make the agent write a backdoor. | Architectural isolation does nothing here. Only review does. This is why `aa push` always shows you the patch and runs [rules](#rules-patch-safeguards). |
| **Agent exfiltrating repo contents via an allowlisted endpoint.** `api.anthropic.com` is an outbound channel; a determined agent could encode data into prompts. | Allowlist raises the bar but doesn't close it. Ephemeral keys, aggressive TTLs, and not using `aa` for top-secret code are the compensating controls. |
| **Agent escaping the container via a kernel exploit.** | Use a backend with real VM isolation (`fly` uses Firecracker). Container namespace escapes are rare but real; prompt-injected agents will not chain 0-days, but this is still residual risk. |
| **Agent writing local (non-committed) git hooks.** | These affect only the agent's container. They do not get pushed to origin, so they don't affect you or collaborators. Accepted risk. |

### The single biggest real-world risk

The agent writes subtle malicious code, your rules don't trip, you skim and accept, and the push succeeds. Your architecture made you feel safe, which reduced your review vigilance. `aa` is designed to avoid this by making `aa diff` and `aa push` separate, explicit steps, with rule violations front-and-center. **Do not add an auto-push mode.**

---

## Troubleshooting

### Session is stuck in `PROVISIONING`

```bash
aa status
```

Shows the last provision log line. Common causes: Fly API token invalid, `docker` not running, image build failed.

```bash
aa kill
```

…if the VM actually started, tears it down. Safe to run even mid-provision.

### Agent died but container is still up

This is the expected behavior on `FAILED` / `LIMBO` / `INCONSISTENT` states. The container is deliberately kept alive so you can investigate.

```bash
aa attach
```

Drops you into a `bash` shell inside the container. Files at `/workspace` are the agent's working tree.

```bash
aa retry
```

Restarts the agent in place (only valid in `LIMBO` / `FAILED`).

### "SSH connection refused" when reattaching

The VM may have been reaped by the provider (TTL expired, billing issue) or the backend's network had a blip. Run `aa status` to see the last known host state. If the VM is gone, the session is unrecoverable; `aa kill` to clean local state.

### "Egress enforcement failed" at session start

See [macOS local backend](#macos-local-backend). Choose: enable privileged containers, switch to `fly`, or explicitly set `egress_enforcement: "none"`.

### I need to add a domain to the allowlist mid-session

You can't. Allowlist is set at container start and enforced at the kernel. Kill the session, edit config, start a new session.

### I ran `aa push` and the push failed

`aa push` does the following atomically as far as your remote state is concerned:

1. Pull patch → **succeeds or aborts** (no remote changes).
2. Apply patch to local clone → **succeeds or aborts** (no remote changes).
3. `git push` → if this fails, the local clone has the commits but origin doesn't. **Remote VM is still up.** Your session state stays `DONE`. You can inspect the local clone under `~/.aa/sessions/<id>/clone/` and push manually, then run `aa kill` to tear the remote down without re-pushing.

---

## Non-goals

`aa` deliberately does not:

- Provide an IDE integration. Attach is a terminal. If you want an IDE, use [devpod](https://devpod.sh/) or similar; they solve a different problem.
- Host a server. There is no `aa` daemon, no cloud service, no account.
- Provide collaborative multi-user sessions. One user per session.
- Auto-merge, auto-push, auto-rebase. Every state transition past `DONE` requires an explicit verb.
- Parse the agent's output for intent. The entire contract with the agent is two injected env vars (`AA_WORKSPACE`, `AA_SESSION_ID`) and three files by convention under `$AA_WORKSPACE/.aa/`: `state`, `result.patch`, `agent.log`. Everything else is the agent's business.
- Mirror your code to any third-party service. The patch goes agent-host → laptop → origin. Nothing else.
- Manage Anthropic/OpenAI/etc billing. It consumes those APIs on your credentials.
- Replace code review. If anything, it should make you review more carefully, because an autonomous agent ran unsupervised.

---

## Design provenance

The full design conversation is in [`plans/plan-1.md`](plans/plan-1.md). It walks through the tradeoffs that led to every decision in this README — the patch-file handoff, the kernel-level egress enforcement, the two-config-file split, the rules system, the session state machine, and the deliberate minimalism of the verb surface.

When in doubt about why a thing is the way it is, the plan is the source. When the plan and this README disagree, this README wins.
