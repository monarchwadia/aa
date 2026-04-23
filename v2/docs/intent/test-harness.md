# Intent: shared end-to-end test harness for `aa`

## Problem

`aa` is a CLI that talks to a paid cloud backend's HTTP API and shells out to two external binaries (a container build tool and a cloud-backend CLI used for shell attach). Exercising the real compiled binary end-to-end today would require a funded cloud account, live network access, and real external binaries installed on the developer's machine — none of which are acceptable for a routine test run. Without a shared harness, every feature either skips e2e coverage or invents its own ad-hoc mocks, drift goes undetected, and tests risk mutating the developer's real config or leaving cloud resources behind. The project needs one shared harness that makes it trivial to write e2e tests against the real compiled `aa` binary while staying fully offline, reproducible, and hermetic.

## Personas

- **Solo maintainer of `aa` (LLM-assisted).** Currently writes features and either skips e2e coverage or writes brittle one-off fakes. What breaks for them: they cannot confidently refactor the CLI because no automated test exercises the full binary; they occasionally want to test a feature that touches both the HTTP API and an external binary, and there is no shared scaffolding for that; they worry a test will accidentally mutate their real config file or burn real cloud credit.

## Success criteria

- Running the full test suite with no network access and no external-binary dependencies passes.
- A test can invoke the real compiled `aa` binary and assert on its stdout, stderr, exit code, and any files it wrote.
- A developer can enter a one-time "record" mode against a live account, run a flow once, and the harness produces recorded-exchange artifacts that live inside the repository tree and can be committed as-is.
- In all non-record runs, the harness replays recorded exchanges; an unmatched request causes the test to fail with a message that names the test, names the unmatched request, and shows the closest recorded exchange.
- Sensitive fields (tokens, account identifiers, and any field the developer has flagged as sensitive) are absent from every recorded artifact committed to the repo — verifiable by grepping the recorded tree.
- Test runs do not read from or write to the developer's real home directory, real config directory, or any path outside the repository's test workspace — verifiable by running the suite under a process with those paths made unreadable.
- Code paths that shell out to an external binary are exercised by the harness without that binary being installed on the machine.
- The harness is usable by a new test with only a handful of lines of setup — no feature-specific boilerplate.

## Non-goals

- Performance or load testing of `aa` or the backend.
- Integration and unit tests for individual features — those live with each feature and are out of scope for this slug.
- Mutation testing or coverage-threshold enforcement.
- Continuous-integration pipeline wiring (GitHub Actions, etc.) — the suite must pass locally; CI is a separate decision.
- Real-cloud smoke tests in the default test run — a manual, opt-in smoke path may exist later but is not part of routine `go test`.
- A general-purpose mocking framework usable outside `aa`.
- Test data management across multiple contributors — single-maintainer assumption holds.

## Constraints

- Go standard library only. No third-party testing libraries, no recording/replay libraries, no assertion libraries, no fake-process libraries.
- The harness must leave no trace on the developer's machine outside the repository working tree: no files in `$HOME`, no files in the OS config/cache directories, no environment mutations that persist past the test process.
- The harness must work against the real compiled `aa` binary, not an in-process re-invocation of `main`, so that flag parsing, exit codes, and process boundaries are covered.
- Recorded artifacts must be human-readable diff-friendly text so that a reviewer can inspect a recording in a pull request.
- Replay failures must be loud and specific — silent fallbacks or best-effort matches are forbidden, because drift between code and recording is the main failure mode the harness exists to catch.
- The harness code itself must remain small enough that a fresh LLM session can read and modify it in one pass (per `PHILOSOPHY.md`: evolvability, low ceremony, no frameworks inside the tool).

## Open questions

- **Record-mode trigger.** How does a developer switch into record mode? Options include a dedicated environment variable (name TBD), a separate test target/tag, or a standalone recording command. Which one wins on clarity and on "hard to fire accidentally"?
- **Snapshot format.** What shape does a recorded exchange take on disk? One file per exchange, one file per test, one file per flow, or a directory tree mirroring the test name? Text format details deferred to the documentation step, but the grain-of-recording decision blocks that step.
- **Per-test vs. per-flow recordings.** Should each test own its own isolated recording, or can multiple tests share a recording of a longer flow? Tradeoff: isolation and clarity versus duplication of large exchanges.
- **Updating a recording when the API shape changes.** When the backend changes a response field, what is the developer's workflow? Re-record the whole flow? Hand-edit the artifact? Some diff-and-approve step? This is the most likely future pain point and needs an answer before the harness ships.
- **Scrubbing sensitive fields.** Is scrubbing driven by a generic rule (e.g., known header names, known URL path segments) or by per-recording explicit declarations, or both? How does the harness prevent a newly introduced sensitive field from slipping into a recording unnoticed?
- **Fake external-binary behavior.** How much behavior does a fake external binary need to simulate — just exit code and output streams, or also interactive stdin? The shell-attach path is interactive in production; decide now whether e2e coverage of interactivity is in scope for this harness.
- **Matching strictness.** On replay, which parts of a request must match exactly (method, path, body, headers, query order) and which are allowed to vary? A too-strict matcher creates flaky tests on benign changes; a too-loose matcher defeats the point.
