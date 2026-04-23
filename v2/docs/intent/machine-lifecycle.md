# Intent: Machine lifecycle — spawn, inspect, control, and tear down cloud instances from the CLI

## Problem

- Provisioning an ephemeral cloud instance by hand requires multiple steps: creating a namespace on the backend, picking an image, waiting for the backend to report readiness, then waiting again for a shell surface to come up.
- A fresh user with nothing set up has no frictionless path from "I want a box" to "I am typing in a shell inside that box."
- Backend-reports-ready and shell-is-reachable are different events; the gap between them surfaces as confusing errors when the user tries to attach too soon.
- Existing verbs are scattered as flat top-level aliases, making the mental model inconsistent as the tool grows.

## Personas

- **Solo developer spinning up a scratch box.** Current workflow: opens the backend's web console, clicks through creation, copies an identifier, shells in separately. Breaks when they want to do this five times a day from the terminal.
- **LLM agent driving the CLI on the user's behalf.** Current workflow: needs a single deterministic command per verb with predictable output. Breaks when it has to orchestrate multi-step provisioning and guess when shell is ready.
- **Returning user cleaning up.** Current workflow: lists what they have running, stops or destroys the ones they no longer need. Breaks when listing and teardown live under inconsistent command shapes.

## Success criteria

- A user with no prior setup can run a single command and, when it returns, be sitting at an interactive shell inside a fresh instance.
- Running the spawn command prints a line when the backend reports the instance ready, and another line when the shell surface is confirmed reachable; the command does not return success until the second event.
- If the user interrupts after backend-ready but before shell-ready, the instance is discoverable via the list command and attachable later without re-provisioning.
- The list command shows every instance the tool has provisioned on the user's behalf, with enough identifying information to target it in subsequent commands.
- Start, stop, and destroy commands each accept an instance identifier, perform the action, and print a line stating what happened.
- The attach command opens an interactive shell on a running instance and returns the user to their local shell cleanly on exit.
- Attempting to attach to an instance whose shell surface is not yet reachable produces a clear message naming the condition and what to do next, not a raw transport error.
- All six verbs are invoked as `aa machine <verb>`; no flat top-level aliases exist for these verbs.

## Non-goals

- Portability across cloud backends. v1 targets a single backend; a second backend is a future concern.
- Zero-downtime rolling restarts, live migration, or any form of hitless state transfer.
- Autoscaling, fleet management, or policy-driven instance counts.
- Multi-region replication or geo-aware placement.
- A separate non-interactive remote-exec command. Non-interactive execution is not in scope for this feature; attach is interactive-only.
- Custom image building, image registries, or per-user image catalogs beyond selecting a base image by name.
- Instance resource sizing controls (CPU, memory, disk) beyond whatever default the backend applies.
- Networking configuration (ports, firewalls, private networks) beyond what the backend gives by default.
- Persistence of instance state across destroy; destroyed means gone.

## Constraints

- Language and runtime: Go, standard library only. Solo dev, LLM-authored.
- Command surface is grouped: `aa machine spawn | ls | start | stop | rm | attach`. No flat top-level aliases.
- The tool provisions any backend-side scaffolding (namespace, grouping object) the user needs on first run, without requiring a separate init step.
- The tool ships with a single default base image; users may override per-command by name, and may change the persistent default via the config store.
- The tool ships with a single default namespace/grouping name used when the user does not specify one; the persistent default is also overridable via the config store.
- The base image is never required to be passed at the command line — if neither flag nor config provides one, the built-in default is used.
- Spawn is synchronous: it does not return until shell surface is confirmed reachable, or it fails with a diagnosable error.
- Every operation prints a human-readable line describing what happened (philosophy axis 3: observability).
- Errors include what failed, why, and what the user can try next.
- Safety-at-boundary applies to any user-supplied string that becomes part of a shell invocation during attach.

## Open questions

- What identifier does the user pass to start/stop/rm/attach — backend-assigned ID, a human name, or both acceptable?
- What is the timeout for "shell surface reachable" before spawn gives up, and what does it leave behind on timeout?
- When spawn is interrupted between backend-ready and shell-ready, is the partial instance left running or torn down?
- Does `ls` show only instances provisioned by this tool, or every instance in the namespace regardless of origin?
- Does `rm` require confirmation, accept a force flag, or neither?
- Is there a way to spawn without attaching (detached spawn), or is attach-on-success the only mode?

## History

- 2026-04-23 — resolved: default base image and default namespace are both configurable via the config store with built-in fallbacks. Base image is never required at the command line.
