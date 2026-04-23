# `aa docker image` — build, push, list, and remove container images

One tool owns both halves of the workflow. The image (artifact) and the instance (runtime) live under the same CLI, use the same stored credential, and share the same mental model. You write a Dockerfile, you run one `aa` command, and a minute later `aa machine spawn` can launch an instance from the tag you just pushed. No separate `docker login`, no second credential store, no copy-pasting tags between tools.

See also:

- [`./machine-lifecycle.md`](./machine-lifecycle.md) — `aa machine spawn` consumes the tags this feature produces.
- [`./docker-up.md`](./docker-up.md) — the end-to-end "Dockerfile to running instance" wrapper.
- [`./config-store.md`](./config-store.md) — where the cloud-backend token is stored and read from.

## Mental model

You never authenticate to the registry yourself. The cloud-backend token you stored once with the config store ([`./config-store.md`](./config-store.md)) is the only credential in the picture. When `aa docker image push` runs, the tool translates that token into whatever the private registry needs (username/password header, bearer token, whatever the backend expects) and hands it to the local build tool as ambient credentials for the duration of the push. Nothing is persisted into `~/.docker/config.json`. Nothing is logged. When the command returns, there is no lingering login.

This means:

- You do not run `docker login` before using this feature. If you did, the tool ignores it.
- Rotating the token in the config store is the only thing required to rotate registry access. There is no second place to update.
- A registry-side permission error is reported against the configured token, naming the key, not as a raw HTTP 401.

## Prerequisites

- A local container-build tool on `PATH`, invokable as `docker`. The feature shells out to it for `build` and `push`. This feature does not vendor or re-implement a builder.
- A cloud-backend token stored in the config store under the documented key (see [`./config-store.md`](./config-store.md)). Without this, every subcommand in this group fails early with a message naming the missing key.

## Quickstart

```
# 1. In a directory containing a Dockerfile, build with the default tag.
aa docker image build .

# 2. Push the tag that step 1 printed.
aa docker image push my-project

# 3. Verify it is in the registry.
aa docker image ls

# 4. Launch an instance from the image you just pushed.
aa machine spawn --image my-project
```

Every command prints a line describing what it did. `build` prints the final tag. `push` prints the tag and the registry path. `ls` prints one image per line. `rm` prints the tag it deleted.

## Command reference

### `aa docker image build <path> [--tag <name>]`

Builds an image from the Dockerfile found at `<path>` using the local `docker` binary.

| Aspect | Behavior |
| --- | --- |
| `<path>` | Required. Directory containing a `Dockerfile`, or a path to a specific Dockerfile. Passed through to the build tool as the build context. |
| `--tag <name>` | Optional. The tag to apply to the built image. If omitted, the tool derives a default tag from the basename of `<path>` (see "Tag convention" below). |
| Output on success | Prints `built <fully-qualified-tag>` on the final line. |
| Build tool output | Forwarded to stderr verbatim. Verbose mode (`aa -v`) adds lines from `aa`; it never rewrites the build tool's own output. |

Failure modes:

- **Local build tool missing.** Exits non-zero with `aa docker image build: 'docker' not found on PATH — install Docker (or a compatible builder) and ensure it is invokable as 'docker'`.
- **No Dockerfile at `<path>`.** Exits non-zero, naming the path that was searched.
- **Build tool returned non-zero.** The tool's own exit code and stderr are surfaced; `aa` adds a final line naming which stage failed.

### `aa docker image push <tag>`

Pushes a locally-built tag to the cloud backend's private registry, using registry credentials derived from the stored cloud-backend token.

| Aspect | Behavior |
| --- | --- |
| `<tag>` | Required. The tag produced by an earlier `aa docker image build`, or any tag the local build tool has. |
| Output on success | Prints `pushed <fully-qualified-tag>` on the final line. The printed tag is exactly the one a subsequent `aa machine spawn --image <tag>` should consume. |
| Authentication | The stored cloud-backend token is translated to registry credentials for the duration of the push. No persistent login is written. |

Failure modes:

- **Token missing from config.** Exits non-zero with a message naming the exact config key that must be set and how to set it (see [`./config-store.md`](./config-store.md)).
- **Token rejected by registry.** Exits non-zero with a message naming the configured token key and the missing permission (e.g. "write access to private registry"), not a raw HTTP status.
- **Unknown local tag.** Exits non-zero with `no such local image: <tag> — run 'aa docker image build' first`.
- **Network / transport failure.** Exits non-zero with the underlying error and a suggestion to retry.

### `aa docker image ls`

Lists images in the backend's private registry that the configured token can see.

| Aspect | Behavior |
| --- | --- |
| Arguments | None. |
| Output | One image per line: tag, size or digest, age. Stable, greppable format — parsers may rely on it. |
| Empty result | Exits zero, prints a single line noting there are no images. |

**Scope of listing: proposed, not yet specified.** Whether `ls` shows only images produced by `aa` (filtered by a tool-owned label or tag-prefix) or every image the configured token can see in the registry is an open question flagged by the intent document. Both options have honest tradeoffs and the decision has not been made. This section will be tightened once it is.

Failure modes:

- **Token missing from config.** Same message shape as `push`.
- **Token rejected by registry.** Same message shape as `push`.

### `aa docker image rm <tag>`

Removes an image from the backend's private registry.

| Aspect | Behavior |
| --- | --- |
| `<tag>` | Required. The tag to delete from the registry. |
| Output on success | Prints `removed <fully-qualified-tag>` on the final line. |
| Post-condition | A subsequent `aa docker image ls` no longer shows the removed tag. |

**Batching and safety: proposed, not yet specified.** The intent flagged two open questions here:

- Whether `rm` accepts multiple tags in one invocation (for consistency with the machine-lifecycle `rm`, which takes multiple IDs), and if so whether partial failures are fatal or per-item.
- Whether `rm` requires a `--force` flag when the image is still referenced by a machine that could be restarted from it.

Until those are resolved, assume single-tag, no-force-flag, fail-loud-on-any-problem semantics. This section will be tightened once the intent is updated.

Failure modes:

- **Tag not found in registry.** Exits non-zero, naming the tag that was searched for.
- **Token missing from config.** Same message shape as `push`.
- **Token lacks delete permission.** Exits non-zero with a message naming the configured token key and the missing permission.

## Tag convention

When `--tag` is omitted, `aa docker image build` derives a default tag from the basename of the supplied build-context path. Running `aa docker image build ./my-project` produces a local tag of `my-project`; running `aa docker image build .` from `/home/alice/hello` produces `hello`.

**Full registry-path template: proposed, not yet specified.** The intent flagged the collision-safety question as open: should the published registry path include a per-user namespace, a per-project identifier pulled from config, the directory basename alone, or require an explicit tag with no default at all? Each option has different collision properties when two projects or two users share an account. Until the intent document resolves this, this doc shows tags as bare names (`my-project`) and references the fully-qualified registry path abstractly as "the registry path printed by `aa docker image push`." Expect the published form to look roughly like `registry.<backend>/aa-apps/<name>` — this is illustrative, not normative.

## Troubleshooting

**`'docker' not found on PATH`**
The feature shells out to a local container build tool named `docker`. Install Docker Desktop, Docker Engine, or a compatible drop-in (anything that provides `docker build` and `docker push`) and confirm it runs with `docker --version`. Then retry.

**`missing required config key: token.<backend>`**
You have not stored the cloud-backend token yet. Set it through the config store (see [`./config-store.md`](./config-store.md)) and retry. The error names the exact key the tool expected.

**`push failed: registry rejected image (403)` or similar**
The stored token reached the registry but was refused. The most common causes, in order: the token does not have write scope for the private registry; the token has been revoked or rotated on the backend side; the image's destination namespace is not one this token can publish to. The error message names the configured token key so you know which entry to replace in the config store.

**`no such local image: <tag>`**
`aa docker image push` was handed a tag the local build tool does not know about. Run `aa docker image build <path> --tag <tag>` first, then push.

**`ls` prints nothing after a successful push**
If the listing scope question above is resolved in the "only images produced by this tool" direction, an image pushed by another client to the same registry will not appear. This is expected once that decision lands. Until the decision lands, treat this as a bug to report.

## Non-goals

Lifted from the intent document and restated so there is no ambiguity about what this feature will not do:

- **No remote / cloud-native builds.** The feature requires a local build tool. Building on the cloud backend's infrastructure is out of scope.
- **No multi-architecture fan-out.** Whatever architecture the local builder produces is what gets pushed. One invocation, one image.
- **No signing or attestation.** The feature does not produce, attach, or verify image signatures or build-provenance attestations.
- **No vulnerability scanning.** CVE reporting on built or listed images is out of scope.
- **No registry mirroring or fallback.** One backend, one registry. The feature does not configure alternate registries, pull-through caches, or failover.
- **Not a full shell for the build tool's flags.** `aa docker image build` is not a drop-in for every flag the underlying builder accepts. Only the flags documented above are supported.
