# config-store

Store tokens and preferences for the `aa` CLI once, reuse them on every subsequent command. Solves the "paste the Fly.io token on every invocation" problem and gives you a single place to see and clear what `aa` knows about you.

## Quickstart

```sh
# 1. Save your Fly.io token so every command that needs it picks it up automatically
aa config token.flyio=fo1_abc123yourrealtokenhere

# 2. List everything aa has stored for you
aa config

# 3. Remove a stored value (proposed syntax — see command reference)
aa config --remove token.flyio
```

After step 1, commands like `aa machine spawn` (see [`./machine-lifecycle.md`](./machine-lifecycle.md)) work with no `--token` flag and no `FLY_API_TOKEN` in your environment.

## Command reference

### `aa config <key>=<value> [<key>=<value> ...]`

Set or update one or more values. Existing keys are replaced. On success, prints one `saved <key>` line per key; the value is not echoed.

```sh
aa config token.flyio=fo1_abc123yourrealtokenhere
# saved token.flyio

aa config token.flyio=fo1_newrotatedtoken defaults.app=my-aa-apps
# saved token.flyio
# saved defaults.app
```

### `aa config`

With no arguments, lists every stored key and its value, one `key=value` per line. Output is stable and greppable.

```sh
aa config
# token.flyio=fo1_abc123yourrealtokenhere
# defaults.app=my-aa-apps
```

If nothing is stored, prints `(no config set)` and exits 0.

### `aa config --remove <key>` *(proposed, not yet specified)*

Remove a single stored value. After removal, the key no longer appears in `aa config` output, and any command that required it will fail with a message naming the key to re-set.

```sh
aa config --remove token.flyio
# removed token.flyio
```

The exact flag spelling (`--remove` vs `--unset` vs `aa config rm <key>`) is not yet settled in intent. Treat the above as placeholder syntax until the remove path is specified.

## Key reference

| Key              | Purpose                                                | Format                 | Example                                   | Status                              |
| ---------------- | ------------------------------------------------------ | ---------------------- | ----------------------------------------- | ----------------------------------- |
| `token.flyio`    | Fly.io API token used by every command that talks to the Fly Machines API. | Opaque string, no spaces. | `token.flyio=fo1_abc123yourrealtokenhere` | Live.                               |
| `defaults.app`   | Fly.io app name to use when no `--app` flag is given. See [`./machine-lifecycle.md`](./machine-lifecycle.md). | App slug.              | `defaults.app=my-aa-apps`                 | **Proposed, not yet specified.**    |
| `defaults.image` | Container image to use when no `--image` flag is given. See [`./docker-up.md`](./docker-up.md). | Docker image ref.      | `defaults.image=ubuntu:22.04`             | **Proposed, not yet specified.**    |

Unknown keys are accepted and stored verbatim — the set surface is deliberately open so future settings need no new command. A stored key only has an effect once some command reads it.

### Precedence when a command reads a value

For `token.flyio`, commands check sources in this order and use the first one that is non-empty:

1. Explicit flag (e.g. `--token`)
2. Environment variable (e.g. `FLY_API_TOKEN`)
3. Stored config (`token.flyio`)

Setting it in config is the "set once and forget" path; the flag and env var remain available for one-off overrides.

## Storage and security model

- Values live in a single file under the current user's OS-standard config directory, at `aa/config` within it. On Linux that is `~/.config/aa/config`; on macOS `~/Library/Application Support/aa/config`.
- The file is a plain `key=value` text file, one entry per line. Lines starting with `#` are ignored. You can `cat` it to inspect; `aa config` is the supported way.
- The file is created with mode `0600` and its parent directory with mode `0700`. Other local user accounts cannot read it under default OS permissions.
- Values are stored in the clear. There is no passphrase, no OS-keychain integration, and no encryption-at-rest. Anyone with read access to your user account (including processes running as you) can read the token.
- No sync across machines. No per-project overrides. One config file per user per machine.

## Troubleshooting

### `no Fly.io token found — run: aa config token.flyio=<token>`

A command that needed a Fly.io token could not find one in any source (flag, env, config). Fix by either supplying `--token`, exporting `FLY_API_TOKEN`, or — for the persistent fix — running the command the error printed:

```sh
aa config token.flyio=fo1_abc123yourrealtokenhere
```

Then re-run the original command.

### `invalid config argument "token.flyio" — expected key=value`

You passed a bare key to `aa config` without an `=value`. `aa config` is either zero arguments (list) or one-or-more `key=value` arguments (set). To read a single key, use `aa config | grep '^token.flyio='`.

### `aa config` shows nothing after a set

Most commonly means the set ran as a different user than the read (for example, one under `sudo`, one without). Config is per-user; `sudo aa config token.flyio=...` writes to root's config dir, not yours. Re-run without `sudo`.

## Non-goals

- No syncing across machines, users, or teams.
- No encryption-at-rest, user passphrase, or OS keychain integration.
- No per-project or per-directory config layering.
- No import/export, backup, or migration tooling.
- No auditing or change history.
- No interactive prompt-and-confirm UX.
- No validation that a stored credential is accepted by the upstream service at set time — you find out when you next run a command that uses it.
