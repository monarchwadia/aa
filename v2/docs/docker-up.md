# `aa docker up` — one-command Dockerfile-to-cloud-shell

`cd` into a directory with a `Dockerfile`, run `aa docker up .`, and land in an interactive shell running that image on a cloud backend. Sub-minute for a trivial image. No second command, no copy-pasted tags, no separate registry login. This is the flagship verb of the tool: every other feature (image management, machine lifecycle, config) exists so this one works.

See also: [machine-lifecycle.md](./machine-lifecycle.md) · [docker-images.md](./docker-images.md) · [config-store.md](./config-store.md)

---

## Mental model

### The stage chain

```
  build  ───►  push  ───►  spawn  ───►  attach
   (1)         (2)          (3)          (4)
```

Every invocation walks this chain in order. Each stage prints one line on success. If any stage fails, the chain stops, the command exits non-zero, and the error names the stage.

1. **build** — shells out to the local container-build tool (`docker`) to build the image from the `Dockerfile` in the target directory.
2. **push** — pushes the built image to the cloud backend's private registry, using the backend credential already stored in config. No second login.
3. **spawn** — provisions a cloud instance running that image, waits for backend-ready, then waits for shell-surface-ready. Identical to `aa machine spawn` semantics.
4. **attach** — opens an interactive shell inside the running container on the instance. Identical to `aa machine attach` semantics.

### Partial-failure cleanup is asymmetric (resolved intent)

- **Image is retained on failure-after-push.** Images are cheap, cacheable, and likely useful on the next attempt. A failed `spawn` does not roll back the published image.
- **Machine is destroyed on failure-after-spawn.** A machine that provisioned but never reached a usable shell is torn down automatically before the command exits. You will not find a half-dead instance in `aa machine ls` after a failed `up`.

The asymmetry is deliberate: images are artifacts, machines are state.

> **Note on cleanup differing from plain `aa machine spawn`.** [machine-lifecycle.md](./machine-lifecycle.md) specifies that `aa machine spawn` **retains** an instance whose shell surface never becomes reachable (the user must `aa machine rm` it manually). `aa docker up` deliberately overrides that: because `up` owns the whole pipeline, it cleans up its own half-dead instances. If you want the retention behavior, use the lower-level primitives directly.

### Instance identity is detached from the command (resolved intent)

The instance `aa docker up` creates is conceptually independent of the command invocation. The command provisions the instance and immediately attaches your terminal to a shell inside it. **When you exit the shell, the instance stays running.** You can:

- Reattach later with `aa machine attach <id>`
- Tear it down with `aa machine rm <id>`
- List it alongside anything else you've provisioned with `aa machine ls`

See [machine-lifecycle.md](./machine-lifecycle.md) for the full set of post-up operations.

### Re-run refuses by default (resolved intent)

Running `aa docker up` a second time from the same directory — while an instance tied to that directory still exists — fails with a clear message pointing at the existing instance. Two resolutions:

- **`--force`** — replace the existing instance in place. The old instance is torn down, a fresh build/push/spawn/attach cycle runs, and you land in the new one.
- **Manual teardown** — run `aa machine rm <id>` (printed in the refusal message), then re-run `aa docker up`.

---

## Quickstart

You have a directory `./myapi` with a `Dockerfile`. Your Fly.io token is already in config (see [config-store.md](./config-store.md)).

```
$ aa docker up ./myapi
[build]  building image from ./myapi/Dockerfile … done (12.4s)
[push]   pushing <image-tag> … done (8.1s)
[spawn]  provisioning instance … backend-ready
[spawn]  waiting for shell … shell-ready (machine id: 4d891a2b)
[attach] attaching to 4d891a2b …

root@4d891a2b:/app# _
```

> The literal form of `<image-tag>` in the `push` line is provisional. See § Identity and naming below; treat the tag printed by `aa docker up` as opaque.

You are now in a shell inside your container on a cloud machine. Type what you want. When you're done:

```
root@4d891a2b:/app# exit
$ _
```

The instance keeps running. To tear it down:

```
$ aa machine rm 4d891a2b
[rm]  destroyed 4d891a2b
```

---

## Command reference

### `aa docker up <path> [--force]`

| Input         | Required | Description                                                                                       |
|---------------|----------|---------------------------------------------------------------------------------------------------|
| `<path>`      | yes      | Path to a directory containing a `Dockerfile`. Relative or absolute. Must exist and be readable.  |
| `--force`     | no       | Replace an existing instance tied to this directory instead of refusing.                          |

**Per-stage output.** Each stage prints exactly one line on success. Verbose mode (`-v`) adds lines but does not re-format the normal lines (philosophy axis 3).

```
[build]  <what was built>
[push]   <tag that was pushed>
[spawn]  <progress lines, then the machine id>
[attach] <attaching line, then the live shell>
```

**Exit codes.**

| Code | Meaning                                                                     |
|------|-----------------------------------------------------------------------------|
| 0    | Attach succeeded and the user exited the shell cleanly.                     |
| 1    | A stage failed. The error message names the stage.                         |
| 2    | Misuse (no Dockerfile at path, bad flag, refused re-run without `--force`).|

### Failure matrix

Each stage fails loudly, names itself, and leaves predictable state behind.

| Stage   | Failure example              | What is left behind                                        | Error names                            | Recovery                                                              |
|---------|------------------------------|------------------------------------------------------------|----------------------------------------|-----------------------------------------------------------------------|
| build   | Dockerfile syntax error       | Nothing on the remote. Local build-tool cache unchanged.   | `build`                                | Fix the Dockerfile, re-run `aa docker up <path>`.                    |
| push    | Registry auth rejected        | Nothing on the remote. Local image exists in build-tool.   | `push`                                 | Check the configured token; see [config-store.md](./config-store.md). |
| spawn   | Backend quota exceeded        | **Image retained in registry.** No instance.               | `spawn`                                | Free a slot (or change region), re-run. Cached image re-used on push.|
| attach  | SSH surface never reachable   | **Instance destroyed.** Image retained in registry.        | `attach` (after automatic retry)       | Re-run `aa docker up <path>`. Error message may suggest a new region. |

Attach is retried automatically before giving up (see [machine-lifecycle.md](./machine-lifecycle.md) for the retry policy shared with `aa machine attach`). When attach ultimately fails, the command prints the instance id that was destroyed and — if the destruction itself failed — the id you need to `aa machine rm` manually.

---

## Identity and naming

> **Proposed, not yet specified.** The intent document lists naming as an open question: how the resulting image tag and machine name are derived so that a subsequent run can locate them (and so `--force` knows what to replace).
>
> Candidate schemes under consideration: derived from the directory basename, derived from a content hash of the build context, derived from a per-user namespace prefix, or an explicit `--name` flag. The final scheme determines whether two projects on the same account can collide, and how `--force` identifies the "existing instance tied to this directory."
>
> Until this is pinned, treat the image tag and machine name printed by `aa docker up` as opaque identifiers: copy them from the output, don't construct them by hand.

## Build context scope

> **Proposed, not yet specified.** The intent document lists this as an open question: whether the build context is strictly `<path>` or whether there is an implicit project-root / ignore-file notion.
>
> Until this is pinned, assume the build context is exactly `<path>` and that everything under it is shipped. Keep the directory small for fast builds. A `.dockerignore` at `<path>` is honored by the underlying build tool; whether `aa` adds additional filtering is TBD.

---

## Prerequisites

| Requirement                   | Why                                                                                           | How to verify                      |
|-------------------------------|-----------------------------------------------------------------------------------------------|------------------------------------|
| `docker` on `PATH`            | Used to build the image locally. `aa` shells out; it does not vendor a builder.               | `docker version`                   |
| `flyctl` on `PATH`            | Used to open the interactive shell surface on the cloud instance during attach.               | `flyctl version`                   |
| Fly.io token stored in config | Used for push (registry) and spawn (backend API). No separate registry login is performed.    | `aa config` shows `token.flyio`    |

To store the token:

```
$ aa config token.flyio=fo1_xxxxxxxxxxxxxxxxxxxx
```

See [config-store.md](./config-store.md) for the full config surface (including changing the default base image / namespace used during spawn).

---

## Troubleshooting

**No Dockerfile at path.**
```
$ aa docker up ./nope
error: no Dockerfile at ./nope — aa docker up requires a directory containing a Dockerfile
```
Point `aa docker up` at a directory that has a `Dockerfile` in it.

**Build fails.**
The `[build]` line is replaced with the build-tool's failure output and the stage name:
```
[build]  FAILED: Dockerfile:12 — unknown instruction: RUNN
error: build stage failed — fix the Dockerfile and re-run 'aa docker up ./myapi'
```
Nothing is pushed. Fix the Dockerfile, re-run.

**Push fails (auth).**
```
[push]   FAILED: 401 Unauthorized
error: push stage failed — registry rejected credential 'token.flyio'; run 'aa config token.flyio=<new-token>' and re-run
```

**Push fails (rate limit).**
```
[push]   FAILED: 429 Too Many Requests
error: push stage failed — registry rate-limited; wait a minute and re-run 'aa docker up ./myapi'
```

**Spawn fails (quota).**
```
[spawn]  FAILED: backend refused provision — machine quota exhausted in region 'iad'
error: spawn stage failed — free a machine with 'aa machine rm <id>' or try a different region; pushed image <image-tag> is retained
```
The retained image tag is re-used on the next attempt; push will short-circuit on an identical image.

**Spawn fails (region).**
```
[spawn]  FAILED: backend refused provision — region 'iad' unavailable
error: spawn stage failed — try again or switch region; pushed image <image-tag> is retained
```

**Attach fails.**
Attach is retried automatically according to the policy in [machine-lifecycle.md](./machine-lifecycle.md). If every retry fails, the instance is destroyed before the command exits:
```
[attach] retrying (1/3) … retrying (2/3) … retrying (3/3) … FAILED
error: attach stage failed — shell surface never became reachable on 4d891a2b; instance destroyed; pushed image retained; re-run 'aa docker up ./myapi' to try again
```

**Re-run without `--force`.**
```
$ aa docker up ./myapi
error: instance 4d891a2b is already tied to ./myapi — pass --force to replace it, or run 'aa machine rm 4d891a2b' first
```

---

## Non-goals

Lifted from the intent document. These are deliberately out of scope for `aa docker up`:

- **Hot-reload / watch-mode development.** Every invocation is a fresh build-and-enter. Editing files after entering the shell does not trigger a rebuild.
- **Multi-service orchestration.** One `Dockerfile`, one container. No compose-style service graphs, no inter-service networking.
- **Production deployment semantics.** No zero-downtime rollout, health checks, rollback, blue/green, or traffic shifting. This is for interactive work.
- **Cost optimization / idle auto-stop.** The command does not stop or reclaim instances based on idleness or budgets. Post-exit lifecycle is handled by [machine-lifecycle.md](./machine-lifecycle.md).
- **Cross-architecture fan-out.** No implicit multi-arch builds. Whatever your local `docker` produces is what gets pushed.
