# aa v2 — cross-slug architecture overview

The five slugs (`config-store`, `machine-lifecycle`, `docker-images`, `docker-up`, `test-harness`) each have their own architecture doc. This file is the thin layer above them: the cross-slug contracts, wave plan, and shared invariants. Read each slug's own doc for detail; read this one for how they fit together.

## Shared invariants

1. **Go stdlib only.** No third-party runtime or test dependencies anywhere in `v2/`.
2. **Config precedence is uniform.** Every user-supplied value resolves as: per-command flag > environment variable > config file > built-in default. The only place this resolution logic lives is `config-store`.
3. **Labels are `aa.*`-prefixed.** Any machine label the tool itself reads or writes has the `aa.` prefix. User-supplied labels are not a surface in v1.
4. **Test isolation is non-negotiable.** No test writes to the real `HOME`, touches the real network, or invokes a real external binary.

## Cross-slug contracts (Wave 0, single-author, locked before Wave 1)

These contract files are written by one author (me) and frozen before any workstream begins.

| Contract file | Owner slug | Exposes |
|---|---|---|
| `v2/configstore/reader.go` | config-store | `ResolveFlyToken()`, `ResolveAPIBase()`, `ResolveRegistryBase()`, `ResolveDefaultApp()`, `ResolveDefaultImage()` |
| `v2/flyclient/iface.go` | machine-lifecycle | `SpawnSpec` (with `Labels`), `Machine` (with `Labels`), `Client` interface with `Create`/`Get`/`List`/`Start`/`Stop`/`Destroy`/`FindByLabel` |
| `v2/extbin/iface.go` | machine-lifecycle | `Runner` interface for shelling out to external binaries (`flyctl`, `docker`) in a test-injectable way |
| `v2/registry/iface.go` | docker-images | `Registry` interface: `Login`, `Push`, `List`, `Delete` |
| `v2/testhelpers/sandbox.go` | test-harness | `Sandbox`, `NewSandbox(t, snapshotName)`, `RunAA`, `ExpectBinary`, `BinaryInvocations` |
| `v2/testhelpers/fakes.go` | test-harness | `FakeBinary`, `Invocation`, matcher helpers |

**Rule:** until a contract file is landed, no slug whose workstreams consume that file begins. Contract files may have stub implementations returning zero values/errors; that's fine for unblocking.

## Wave plan

```
Wave 0 ─ contract files                                         [1 author, serial]
         configstore/reader.go · flyclient/iface.go · extbin/iface.go
         registry/iface.go · testhelpers/{sandbox,fakes}.go
           │
Wave 1 ─┬─ test-harness body                (v2/testhelpers/*.go)   [parallel]
        └─ config-store body                (v2/configstore/*, cmd)
           │
Wave 2 ─┬─ machine-lifecycle body           (fly-client, extbin-runner,
        │                                    spawn-state, handlers)   [parallel]
        └─ docker-images body               (tag-derivation, argv,
                                             registry-http, docker-runner, cli)
           │
Wave 3 ─── docker-up body                   (v2/docker_up.go)      [single]
           │
Wave 4 ─── wiring + e2e                     (main.go dispatch,
                                             tests/e2e/*_test.go)  [single]
```

A wave starts only when every workstream in the previous wave is landed *and* its exports are stable. If a later wave discovers it needs an interface change in an earlier wave's contract file, the flow stops, the contract is amended, and affected waves are rerun to the amended contract. This is the "stop-the-world" escalation path per the plan-implementation skill.

## Shared / single-writer files

Some files are co-written by multiple slugs in principle; in practice a single owner writes them in the wave where the last contributor lands.

| File | Single writer | Wave | Collaborators |
|---|---|---|---|
| `v2/main.go` | test-harness (one-line env-var read change) in Wave 1; final dispatch by Wave 4 owner | 1 + 4 | every slug contributes a `case` in the dispatch |
| `v2/preflight.go` | docker-images adds `docker` check; Wave 2 | 2 | machine-lifecycle already owns the `flyctl` check |
| `v2/go.mod` | unchanged across all waves | — | (no new deps; stdlib only) |

## Scheduling rules

1. **Contract freeze is the gate.** Wave N+1 does not read Wave N's bodies — only Wave N's contracts.
2. **Fakes unblock.** Every slug consuming another slug's interface develops against the `fakes.go` stub, not the real implementation. A Wave 2 slug can write its tests and bodies before Wave 1's real bodies ship.
3. **Wave reviews are mandatory.** After each wave, the `wave-review` skill runs before the next wave starts. A wave that has not been wave-reviewed has not landed.
4. **Conflicts escalate, don't hide.** If a Wave N workstream discovers it needs to write into another workstream's `Owns` list, it stops and raises; the breakdown is revised.

## Known risks carried forward

- **Registry protocol.** `registry.fly.io` is assumed to speak standard Docker Registry v2 HTTP. If it doesn't, `docker-images` Wave 2 adds a discovery pass. Confirmed before commit.
- **Label uniqueness.** Fly Machine metadata doesn't enforce uniqueness on our labels. `docker-up` tolerates and collapses multi-match on `--force`; this behavior is tested explicitly.
- **External-binary absence.** Preflight checks guard `flyctl` and `docker`; any new shell-out added in future work must extend preflight in the same wave.
