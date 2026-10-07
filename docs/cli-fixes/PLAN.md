# Plan: SwarmCracker CLI Fixes

Companion to `docs/cli-fixes/SPEC.md`. Implementation order for one PR on
`fix/cli-audit-fixes`.

## Component graph

```
                 ┌──────────────┐
                 │ M1 vm-lifecycle│──┐
                 └──────────────┘  │ PID by id/socket
                        │          ▼
                        │   ┌──────────────────┐
                        └──▶│ M3 snapshot-restore│
                            └──────────────────┘
   ┌────────────────┐     ┌──────────────────┐
   │ M2 service-scale│     │ M4 id-resolution │──▶ M5 observability
   └────────────────┘     └──────────────────┘
   ┌────────────────┐     ┌──────────────────────┐
   │ M6 setup-config │     │ M7 network-introspect │
   └────────────────┘     └──────────────────────┘
                 └──────────────┬──────────────┘
                                ▼
                        M8 ux-polish (last)
```

## Build order (phases)

1. **Foundations** — extend `pkg/runtime` PID/discovery (socket-based ID) +
   shared reference resolvers. Unblocks M1/M3/M4/M5.
2. **Core fixes** — M1 vm-lifecycle, M2 service-scaling, M3 snapshot-restore.
3. **Resolution + observability** — M4 id-resolution, M5 observability.
4. **Setup + network** — M6 setup-config, M7 network-introspection.
5. **Polish** — M8 ux-polish.
6. **Docs + verification + PR.**

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| Snapshot restore semantics on v1.15 (block devices/rootfs from state) need live iteration | Prototype on the test node first; keep changes in `pkg/snapshot`; add a focused integration check; error clearly if rootfs missing |
| Adding `--id` to CLI Firecracker changes its log-line prefix | Low risk; verify logs still readable; keep the change minimal |
| Resolvers do `List*` calls; large clusters | Fine for CLI; match exact ID first, then prefix, then name |
| `cluster status` summary needs the control API; may be down | Degrade gracefully to an error or "cluster unreachable" without `Usage` spam |
| Un-deprecating `metrics` could double-register | Remove the deprecated wrapper; assert single registration in tests |
| `network` introspection is Linux-only | Guard by `runtime.GOOS`; return structured empty results elsewhere |
| BUG-21 daemon socket cleanup may alter task teardown | Scope daemon change narrowly; treat as "ask first" if it changes observable behavior |

## Verification checkpoints

After each phase: `make fmt`, `go test` on affected packages, `GOOS=linux go
build ./...`. After core (phase 2), resolution (phase 3), setup/network (phase 4)
and polish (phase 5): run the relevant commands on `192.168.18.25` and clean up.

## Definition of done

- All module acceptance criteria in the spec pass (live + unit).
- `go build ./...` and `GOOS=linux go build ./...` pass; affected tests pass.
- `docs-site` updated for behavior/flag changes and `npm run verify` passes.
- Server left healthy; one PR opened against `main`.
