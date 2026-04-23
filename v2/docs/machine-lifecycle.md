# Machine lifecycle

Spawn, inspect, control, and tear down cloud instances from the `aa` CLI. One command surface, one mental model: `aa machine <verb>`. The tool handles the backend-side scaffolding on first run, waits until a real shell is reachable before returning from `spawn`, and keeps every instance it provisioned discoverable via `ls` so nothing gets orphaned.

Related docs: [`./config-store.md`](./config-store.md) (where defaults live), [`./docker-up.md`](./docker-up.md) (local container variant of the same mental model).

---

## Mental model: backend-ready vs shell-reachable

The single most important distinction in this feature.

- **Backend-ready** — the cloud backend reports the instance is allocated, booted, and in the `started` state. This happens first.
- **Shell-reachable** — an interactive shell surface on the instance actually accepts a connection. This happens strictly later, sometimes by several seconds.

Attempting to attach between these two events produces a transport error that looks scary but isn't. `spawn` bridges the gap for you: it does not return success until **shell-reachable** is confirmed. If you interrupt `spawn` after backend-ready but before shell-reachable, the instance still exists and is listed by `ls` — see Troubleshooting.

---

## Prerequisites

- A Fly.io API token stored in the config store under `token.flyio`. See [`./config-store.md`](./config-store.md).
- `flyctl` installed and on `$PATH`. The tool invokes it as a preflight dependency for attach.

Set the token once:

```
aa config token.flyio=<your-token>
```

---

## Quickstart

```
# create a fresh instance and drop into its shell
aa machine spawn

# in another terminal, see what's running
aa machine ls

# reconnect to an existing instance
aa machine attach <id>

# tear it down when you're done
aa machine rm <id>
```

> **Identifier format:** *proposed, not yet specified.* The intent doc leaves open whether `<id>` is a backend-assigned ID, a human-assigned name, or both. Until pinned, treat the `ID` column of `aa machine ls` as the canonical identifier to pass to other verbs.

---

## Command reference

All verbs live under `aa machine`. The flat aliases (`aa spawn`, `aa ls`, `aa start`, `aa stop`, `aa rm`) that exist in the current code **are being retired**; new scripts should use only the `aa machine <verb>` surface.

### `aa machine spawn`

Provision a new instance and attach to its shell. Synchronous: does not return until shell-reachable, or fails with a diagnosable error.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config `token.flyio`, then `FLY_API_TOKEN` | Override the stored token for this one call. |
| `--app` | string | from config `defaults.app`, then built-in fallback | Namespace/grouping on the backend. |
| `--image` | string | from config `defaults.image`, then built-in fallback | Base image. **Never required at the command line.** |
| `--region` | string | backend default | Optional placement hint. |

On success, prints (in order):
- `Creating machine in app "<app>" (image: <image>)...`
- `Machine <id> created (region: <region>), waiting to start...`
- `Machine <id> is running — waiting for SSH...` (backend-ready line)
- one or more `SSH not ready yet (attempt N/M), retrying in 3s...` lines while bridging the gap
- the interactive shell takes over; on exit, `spawn` returns.

Failure modes:
- **No token configured** → `no Fly.io token found — run: aa config token.flyio=<token>`.
- **App creation refused** → prints HTTP error from the backend and exits non-zero. The instance was not created.
- **Backend never reports `started` within the timeout** → `timed out after <duration> waiting for state "started"`. *Timeout value and partial-instance cleanup behavior: proposed, not yet specified.*
- **Shell surface never becomes reachable** → retries are exhausted, final transport error is printed. The instance still exists; use `aa machine ls` to find it and `aa machine rm` to destroy it.

### `aa machine ls`

List instances in the configured app.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config | Override stored token. |
| `--app` | string | from config `defaults.app` | Namespace to list. |

Output is a tab-aligned table:

```
ID              STATE    REGION
9080e6f3a12345  started  iad
4871aa09cd7890  stopped  iad
```

If no instances exist, prints `(no machines in "<app>")`.

> **Scope of `ls`:** *proposed, not yet specified.* Intent leaves open whether `ls` shows only instances this tool provisioned, or every instance in the namespace regardless of origin. Today it shows every instance in the namespace.

### `aa machine start <id> [<id>...]`

Start one or more stopped instances.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config | Override stored token. |
| `--app` | string | from config `defaults.app` | Namespace. |

On success, prints `start <id> ok` per instance.

Failure modes:
- **Unknown ID** → backend 404; error names the ID.
- **Already started** → backend accepts this as a no-op; exits zero.

### `aa machine stop <id> [<id>...]`

Stop one or more running instances. The instance remains provisioned and re-startable.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config | Override stored token. |
| `--app` | string | from config `defaults.app` | Namespace. |

On success, prints `stop <id> ok` per instance.

### `aa machine rm <id> [<id>...]`

Destroy one or more instances. Destroyed means gone; there is no undo.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config | Override stored token. |
| `--app` | string | from config `defaults.app` | Namespace. |
| `--force` | bool | `false` | Destroy even if the instance is still running. |

On success, prints `rm <id> ok` per instance.

Behavior against a running instance without `--force`: the backend refuses; the command surfaces the error and exits non-zero. Pass `--force` to destroy it in place.

> **Confirmation prompt:** *proposed, not yet specified.* Intent leaves open whether `rm` should prompt before destroying. Today it does not.

### `aa machine attach <id>`

Open an interactive shell on a running instance. Returns to your local shell cleanly when the remote shell exits.

| Flag | Type | Default | Purpose |
|---|---|---|---|
| `--token` | string | from config | Override stored token. |
| `--app` | string | from config `defaults.app` | Namespace. |

Failure modes:
- **Instance is stopped** → clear message naming the state; suggestion to run `aa machine start <id>` first.
- **Shell surface not yet reachable** → clear message naming the condition, not a raw transport error.

> **Detached-spawn mode (spawn without attach):** *proposed, not yet specified.* Intent leaves open whether a flag like `--detach` on `spawn` is in scope. Today `spawn` always attaches on success.

---

## Defaults

Defaults are read from the config store (see [`./config-store.md`](./config-store.md)) and fall back to built-in values only if the config store has nothing.

| Key | Purpose | Built-in fallback |
|---|---|---|
| `defaults.app` | Namespace/grouping on the backend used when `--app` is omitted. | *proposed, not yet specified* |
| `defaults.image` | Base image used when `--image` is omitted. | *proposed, not yet specified* |
| `token.flyio` | Credential for the cloud backend. | *(no fallback — required)* |

The **precedence** for each of these values is: explicit flag → environment variable (where one exists, e.g. `FLY_API_TOKEN` for the token) → config store → built-in fallback.

To change a persistent default:

```
aa config defaults.app=my-scratch-apps
aa config defaults.image=ubuntu:24.04
```

---

## Troubleshooting

**"SSH not ready yet" during spawn.** Expected. This is the bridge between backend-ready and shell-reachable. The tool retries automatically. If retries are exhausted, the instance still exists — `aa machine ls` will show it. You can `aa machine attach <id>` to try again, or `aa machine rm <id>` to tear it down.

**"no Fly.io token found".** The tool could not resolve a token from flag, environment, or config store. Fix:

```
aa config token.flyio=<your-token>
```

**Attaching to a stopped instance.** `aa machine attach` will fail with a clear message. Start the instance first:

```
aa machine start <id>
aa machine attach <id>
```

**`rm` on a running instance.** Refused without `--force`. Either stop it first, or pass `--force`:

```
aa machine stop <id> && aa machine rm <id>
# or
aa machine rm --force <id>
```

**Interrupted `spawn`.** If you Ctrl-C `spawn` after the `Machine <id> created` line, the instance exists and will show up in `aa machine ls`. Decide whether to `attach` it later or `rm` it. *Intent leaves open whether interrupted spawns should auto-tear-down; today they do not.*

---

## Non-goals

Lifted from the intent doc:

- No portability across cloud backends; v1 targets a single backend.
- No zero-downtime restarts, live migration, or hitless state transfer.
- No autoscaling, fleet management, or policy-driven instance counts.
- No multi-region replication or geo-aware placement.
- No non-interactive remote-exec command; `attach` is interactive-only.
- No custom image building or per-user image catalogs beyond selecting a base image by name.
- No CPU/memory/disk sizing controls beyond backend defaults.
- No networking configuration beyond what the backend provides by default.
- No state persistence across `rm`; destroyed means gone.

---

## Migration note: flat aliases are retiring

The current codebase still accepts `aa spawn`, `aa ls`, `aa start`, `aa stop`, and `aa rm` as flat top-level commands. These are being retired. Documentation, tests, and LLM-facing examples use only the grouped `aa machine <verb>` form. Scripts that rely on the flat aliases should migrate.
