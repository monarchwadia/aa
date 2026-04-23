# Intent: Manage container images in the cloud backend's private registry from `aa`

## Problem

The tool already owns the runtime half of the workflow — spawning, listing, and tearing down cloud instances. But to run anything other than a stock base image, the user has to context-switch to a separate container-build tool and a separate registry CLI: build the image locally, log in to the cloud backend's private registry with a second credential, push, then remember the resulting tag to pass back into `aa spawn`. That's two tools, two credential stores, and two mental models for what should be one workflow ("I have a Dockerfile, I want to run it in the cloud"). The seam is where things go wrong: stale logins, mismatched tags, images pushed to the wrong namespace, no single place to see what's been published.

## Personas

- **Solo developer running personal workloads.** Current workflow: writes a Dockerfile locally, runs the build tool by hand, separately authenticates to the private registry, pushes, then copies the tag into an `aa spawn --image ...` invocation. What breaks: the registry credential drifts out of sync with the tool's configured credential; the user forgets which tag they pushed last; there is no single command to list what's already in the registry without opening a browser or a second CLI.
- **LLM-authored agent session acting on the user's behalf.** Current workflow: has to be taught two tools and two auth flows to ship an image. What breaks: every extra tool and credential is another surface where the agent gets stuck, asks the human to intervene, or silently picks the wrong namespace.

## Success criteria

- With only the credential already configured in the tool, a user in a directory containing a Dockerfile can run a single `aa` subcommand and end up with a built, pushed image in the cloud backend's private registry, without performing any separate registry authentication step.
- Immediately after a successful push, `aa machine spawn` can launch an instance from that same image tag without the user having to type or copy the tag from another tool's output.
- `aa docker image ls` prints the images in the registry that are reachable with the tool's configured credential, one per line, with enough information (tag, size or digest, age) to identify which image is which.
- `aa docker image rm <tag>` removes the named image from the registry; a follow-up `aa docker image ls` no longer shows it.
- If the local container-build tool is missing, `aa docker image build` fails with an error that names the missing prerequisite and tells the user what to install, rather than a generic "command not found".
- If the configured cloud-backend credential lacks permission to write to the private registry, the build/push flow fails with an error that identifies the credential and the missing permission, not a raw HTTP status.

## Non-goals

- **Remote / cloud-native builds without a local build tool.** The feature assumes the user has a local container-build tool installed. Building on the cloud backend's infrastructure is out of scope.
- **Multi-architecture build fan-out.** Producing one image per target architecture from a single invocation is not part of this feature. Whatever architecture the local build tool produces is what gets pushed.
- **Supply-chain signing and attestation.** Producing, attaching, or verifying image signatures or build provenance attestations is out of scope.
- **Image vulnerability scanning.** Reporting CVEs on pushed or listed images is out of scope.
- **Registry mirror fallback.** Configuring alternate/mirror registries, failing over to a public mirror, or pulling through a cache is out of scope. One backend, one registry.
- **Wrapping the full build-tool flag surface.** `aa docker image build` is not a drop-in shell for every flag the underlying build tool accepts. Only the flags needed for the success criteria above are supported.

## Constraints

- Single cloud backend. The private registry is whichever one that backend exposes; there is no abstraction over multiple registries.
- No separate registry credential. The tool must derive whatever it needs to talk to the private registry from the cloud-backend credential already stored in the tool's config.
- The tool shells out to an external, locally-installed container-build tool to perform the actual image build. The user must have that tool installed; the feature does not vendor or re-implement it.
- Solo-dev, LLM-authored, stdlib-only Go codebase. No new third-party dependencies introduced by this feature.
- Observability axis: every subcommand prints what it did (what was built, what tag was pushed, what was deleted) in the same plain-text style as the existing machine-lifecycle commands.
- Errors must follow the existing "what, why, what-next" convention used by the rest of the tool.

## Open questions

- **Image-tag naming convention.** What is the default tag an `aa docker image build` produces when the user doesn't specify one? Options include: derived from the current directory name, derived from a per-user namespace, derived from a per-project identifier stored in config, or required-explicit (no default, fail if missing). This choice determines whether two different projects on the same user account can collide, and whether two different users in the same organisation can collide.
- **Scope of `aa docker image ls`.** Should it list only images this tool has published (filtered by some tool-owned label or naming prefix), or every image visible to the configured credential in the private registry? The first keeps output focused; the second is honest about what the credential can actually see and delete.
- **Batching on `rm`.** Should `aa docker image rm` accept multiple tags in one invocation (matching the machine-lifecycle `rm` that takes multiple IDs), and if so, is partial failure fatal or per-item? Consistency with the existing `rm` argues for multi-arg with per-item reporting; the image case may want stricter behaviour because deletes are not reversible.
