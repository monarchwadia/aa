# Intent: One-shot build-and-enter a Dockerfile on a cloud backend

## Problem

Getting a local container workload running on a remote cloud backend involves multiple discrete steps: building the image, making it retrievable by the remote, provisioning a host, pulling the image there, starting the container, and opening an interactive shell into it. Each step is individually understandable, but chaining them by hand is slow, error-prone, and breaks the user's flow. When something goes wrong mid-chain, the user is left to untangle which step failed and what partial state exists.

What the user actually wants is: "I have a Dockerfile here. Put it on a cloud machine and drop me into a shell inside it — now." Everything in between is plumbing they should not have to think about.

## Personas

- **Alex, the tinkerer.** Edits a Dockerfile locally, wants to run it on real cloud hardware (more RAM, a GPU, a different architecture) without manually driving each stage. Current workflow: build locally, push to a registry, provision a machine, SSH in, pull, run, exec. Five tools, five places to make a mistake. What breaks: every context switch costs focus; any failure mid-way means manual cleanup.
- **Priya, the short-lived experiment runner.** Uses ephemeral cloud shells to try things that would take hours on her laptop. Current workflow: copies snippets from a runbook each time. What breaks: the runbook goes stale, friction discourages experimentation, and she often forgets to tear down what she stood up.

## Success criteria

- Running a single command from a directory that contains a Dockerfile results in the user being dropped into an interactive shell running inside that image on a cloud backend, with no other commands required in between.
- For a trivial image, the time from running the command to landing in the remote shell is under one minute in the common case.
- If any intermediate step fails, the command exits non-zero and prints a message that names which stage failed (build, publish, provision, or attach) and what to do next.
- After a failure, the user can determine from the command's output whether a cloud resource was left running and, if so, how to identify it.
- Exiting the remote shell returns the user to their local terminal cleanly, with no orphaned local processes.
- The command refuses to proceed (with a clear message) when run in a directory that has no Dockerfile.

## Non-goals

- **Hot-reload / watch-mode development.** This is not a file-sync or live-rebuild loop. Each invocation is a fresh build-and-enter; editing files after entering the shell does not trigger a rebuild.
- **Multi-service orchestration.** No compose-style multi-container graphs, no service dependencies, no networking between multiple user-defined services. One Dockerfile, one container. Multi-service is a separate future feature.
- **Production deployment semantics.** No zero-downtime rollout, no health checks, no rollback, no blue/green, no traffic shifting. This is for interactive work, not for running anything a real user depends on.
- **Cost optimization / idle auto-stop.** The command does not decide when to stop or reclaim the instance based on idleness, budget, or schedules. Lifecycle after the shell exits is handled elsewhere.
- **Building for architectures the user did not ask for.** No implicit cross-compilation or multi-arch fan-out.

## Constraints

- This feature is a composition of lower-level primitives. It only makes sense if a machine-lifecycle capability (provision, attach, tear down cloud hosts) and a docker-images capability (build locally, publish so a remote host can retrieve) both exist. This feature does not reimplement either — it depends on them. If either is absent or incomplete, this feature cannot be delivered.
- The user experience must be a single command invocation. Anything that requires the user to run a second command before the shell appears violates the intent.
- Failure messages must point at a single named stage. A generic "something went wrong" is not acceptable.
- The command runs in a terminal and must behave correctly as an interactive foreground process (signals, terminal resize, clean exit).

## Open questions

- **Naming / identity:** how are the resulting image and machine named so that a subsequent run can locate, replace, or ignore them? Derived from the directory, a user-supplied label, a content hash, or ephemeral random? This shapes how `--force` finds what to replace.
- **Scope of "Dockerfile here":** is the build context strictly the given directory, or is there an implicit notion of a project root / ignore file? Affects what gets shipped and how fast "trivial image" actually is.

## Resolved intent

- **Instance identity is detached; attach is immediate.** The instance created by this command is conceptually independent of the command invocation — it continues to exist after the shell exits. But the default UX is: the command provisions the instance and then immediately attaches the user's terminal to a shell inside it. When the user exits the shell, the instance stays running; they can reattach or tear it down with the machine-lifecycle commands.
- **Re-run refuses by default.** Running the command a second time from the same directory while an instance tied to that directory still exists fails with a clear message pointing at the existing instance and the two resolutions: pass `--force` to replace it in place, or tear down the old instance manually with the machine-lifecycle commands.
- **Partial-failure cleanup is asymmetric.** A published image that failed to provision is left in place — images are cheap, cacheable, and likely useful next time. An instance that was created but failed to reach shell-ready is destroyed automatically before the command exits with the failure.

## History

- 2026-04-23 — resolved attach behavior (detached identity, immediate attach), re-run behavior (`--force` or manual removal), and partial-failure cleanup policy (keep pushed image, destroy failed machine).
