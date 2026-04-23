# aa

A command-line tool for putting containers onto cloud compute, fast.

You have a Dockerfile. You want it running on real cloud hardware. You want to be in an interactive shell inside it within a minute. `aa` is the single tool that takes you there.

## Features

- **[Config store](docs/config-store.md)** — store your cloud-backend credentials and preferences once; never paste them again.
- **[Machine lifecycle](docs/machine-lifecycle.md)** — spawn, list, start, stop, destroy, and attach to cloud instances with `aa machine <verb>`.
- **[Docker images](docs/docker-images.md)** — build, publish, list, and delete container images against a private registry. No separate `docker login` step.
- **[Docker up](docs/docker-up.md)** — one command: Dockerfile here → shell on the cloud.
- **[Test harness](docs/test-harness.md)** — shared e2e testing infrastructure used by every other feature. Offline by default.

## Prerequisites

- `flyctl` on `PATH` — used to open interactive shells inside running instances.
- `docker` on `PATH` — used to build images before publishing.

## Status

This is v2 of `aa`, built from scratch. v1 is archived in `v1/` for reference only.

Documentation is the spec; features are labeled **proposed, not yet specified** where details are deliberately left open for the implementation phase.
