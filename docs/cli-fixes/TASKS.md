# Tasks: SwarmCracker CLI Fixes

Each task is a single focused session. `Acceptance` = what must be true;
`Verify` = how to confirm; `Files` = expected touch set.

## Phase 1 — Foundations

- [ ] **T1 — Socket-aware Firecracker PID resolution** (M1/M3/M4)
  - Extend `runtime.firecrackerTaskID`/`FindFirecrackerPIDs` to match `--api-sock <dir>/<id>.sock` in addition to `--id`; add `FindFirecrackerPIDBySocket`.
  - Accept: PID map includes CLI VMs launched without `--id`.
  - Verify: `go test ./pkg/runtime/` with fixtures for both cmdline shapes.
  - Files: `pkg/runtime/discovery.go`, `pkg/runtime/*_test.go`.

- [ ] **T2 — Shared reference resolvers** (M4/M5)
  - Add helpers that resolve a ref to a full ID: exact id → unique prefix → name (services); ambiguous → error listing candidates.
  - Accept: one code path used by service/node/task commands.
  - Verify: unit tests for exact/prefix/ambiguous/missing.
  - Files: new `cmd/swarmcracker/resolve.go`, `cmd/swarmcracker/resolve_test.go`.

## Phase 2 — Core fixes

- [ ] **T3 — M1 record CLI VM PID + real `vm stop`** (BUG-01)
  - Add `--id` to lifecycle launch; set `vmState.PID` on create; re-resolve in stop; clean sockets on stop.
  - Accept: create -d shows PID; stop kills process + removes sockets; daemon VM still refused.
  - Verify: `go test ./cmd/... ./pkg/lifecycle/`; live create/stop on `192.168.18.25`.
  - Files: `pkg/lifecycle/vmm.go`, `cmd/swarmcracker/cmd_vm.go`, `cmd_stop.go`, `vm_lookup.go`, tests.

- [ ] **T4 — M2 scale-to-zero + explicit replicas** (BUG-02)
  - `changed("replicas")` semantics; `scaleService` always applies; clear negative error.
  - Accept: `scale <svc> 0` → 0; `update --replicas 0` → 0; omit → unchanged.
  - Verify: unit tests + live on a throwaway service.
  - Files: `cmd/swarmcracker/cmd_service.go`, `cmd_service_test.go`.

- [ ] **T5 — M3 snapshot restore via `/snapshot/load`** (BUG-15)
  - Start Firecracker without `--snapshot`; load state+mem; validate rootfs; register restored VM.
  - Accept: restore boots, API answers, `vm list`/`vm stop` manage it; missing rootfs → clear error.
  - Verify: unit tests for request building; live golden snapshot→restore.
  - Files: `pkg/snapshot/snapshot.go`, `cmd/swarmcracker/cmd_snapshot.go`, tests.

## Phase 3 — Resolution + observability

- [ ] **T6 — M4 wire resolvers into service/node/task** (BUG-03)
  - Use T2 in `service inspect|ps|scale|update|rm`, `node inspect|drain|activate|promote|rm`, `task inspect`; `service ps` errors on unknown.
  - Accept: 12-char IDs work; ambiguous errors; unknown ps errors.
  - Verify: unit tests + live with the server's 12-char IDs.
  - Files: `cmd_node.go`, `cmd_service.go`, `cmd_task.go`, tests.

- [ ] **T7 — M4 attach liveness** (BUG-16)
  - Resolve attach refs against discovered running VMs; build console socket path.
  - Accept: dead task ref → no match; live prefix works.
  - Verify: unit tests + live attach.
  - Files: `pkg/console/resolve.go` or `cmd_vm_attach.go`, `vm_lookup.go`, tests.

- [ ] **T8 — M5 metrics discovery + un-deprecate** (BUG-04)
  - Merge discovered VMs; register top-level `metrics`; remove deprecated wrapper/hint.
  - Accept: metrics shows service VM; no deprecation banner; JSON works.
  - Verify: unit tests + live.
  - Files: `cmd/swarmcracker/main.go`, `cmd_metrics.go`, `cmd_deprecated.go`, tests.

## Phase 4 — Setup + network

- [ ] **T9 — M6 config validate path + setup check rootfs** (BUG-06, BUG-07)
  - Optional path arg; rootfs `*.ext4` scan; real failure count.
  - Accept: `validate /nonexistent.yaml` errors; `setup check` passes with images present.
  - Verify: unit tests + live.
  - Files: `cmd_config.go`, `cmd_setup.go`, tests.

- [ ] **T10 — M7 network introspection** (BUG-05)
  - Real bridge/VXLAN output; honor `--format json`; label experimental where partial.
  - Accept: bridge/vxlan commands return structured data, not the doctor stub.
  - Verify: unit tests (parse fixtures) + live.
  - Files: `cmd/swarmcracker/cmd_network.go`, `cmd_network_test.go` (+ `pkg/network` if reused).

## Phase 5 — Polish

- [ ] **T11 — M8 CLI consistency fixes** (BUG-08–14, 17–20)
  - `vm ls` alias; real deprecation list; `cluster status` summary + `<vm-id>`; snapshot help; ID guard; `--format` validation; verify-dir error; drain warning; init/join hints; promote guard; negative scale message.
  - Accept: each bullet verified; `--help` matches reality.
  - Verify: unit tests + live spot checks.
  - Files: `cmd_list.go`, `cmd_deprecated.go`, `cmd_cluster.go`, `cmd_snapshot.go`, `cmd_node.go`, `cmd_asset.go`, `cmd_init.go`, `cmd_join.go`, `cmd_status.go`, `cmd_service.go`, tests.

- [ ] **T12 — M8 daemon stale-socket cleanup** (BUG-21)
  - Ensure API + console sockets are removed when a task stops/removes; verify daemon teardown path.
  - Accept: after task removal, no stale `.sock`/`.console.sock` remain.
  - Verify: live drain/remove; unit test where feasible.
  - Files: `pkg/swarmkit/vmm.go`, tests. *(ask first if it changes task semantics beyond cleanup)*

## Phase 6 — Docs + ship

- [ ] **T13 — Documentation**
  - Update `docs-site/.../reference/cli.md` and relevant guides for changed flags/behavior; fix snapshot examples.
  - Verify: `cd docs-site && npm run verify`.
  - Files: `docs-site/src/content/docs/**`.

- [ ] **T14 — Full live verification sweep**
  - Re-run the affected commands on `192.168.18.25` for every module; confirm acceptance; clean artifacts.
  - Verify: checklist in the spec; server healthy.

- [ ] **T15 — Finalize PR**
  - `make fmt && make lint`; `go test`; cross-compile; commit; push; open one PR against `main` referencing the spec.
