# E2E Test Audit — 2026-05-15

> SwarmCracker uses SwarmKit (Project Moby), NOT Docker Swarm. E2E tests must use `swarmd` + `swarmctl`, never `docker service`.

---

## Invalid Tests (Docker Swarm — must be removed)

### `test/e2e/docker_swarm_test.go` — DELETE ENTIRE FILE

| Test | Problem |
|------|---------|
| `TestE2E_DockerSwarmBasic` | Uses `docker swarm`, `docker service create`, `docker node ls` — pure Docker Swarm, not SwarmKit |
| `TestE2E_DockerSwarmScale` | Uses `docker service scale` — Docker Swarm scaling, irrelevant |
| `TestE2E_SwarmCrackerExecutor` | Checks `swarmcracker --version` then skips with "to be implemented" — should be in full_workflow_test.go as real test |

## Invalid Tests (Docker Swarm via SwarmKit API)

### `test/e2e/swarmkit_api_test.go` — PARTIAL DELETE

| Test | Problem |
|------|---------|
| `TestE2E_SwarmKitWithDocker` ❌ | Uses `docker service create/inspect/ps` — tests Docker Swarm backend, not SwarmCracker |
| `TestE2E_SwarmKitAPI` ✅ | Valid — pure SwarmKit API struct validation, no Docker dependency |
| `TestE2E_SwarmKitExecutorPlaceholder` ⚠️ | Placeholder stubs, both sub-tests skip. OK as-is for now. |

### `test/e2e/swarmkit_comprehensive_test.go` — REWRITE

| Test | Problem |
|------|---------|
| `TestE2E_SwarmKit_Comprehensive` | Phase 1-2: ✅ SwarmKit API (valid). Phase 3-7: ❌ All use `docker service` |
| `TestE2E_SwarmKit_TaskLifecycle` ❌ | Entire test uses `docker service create/ps` — must be rewritten to use `swarmctl` |

## Placeholder Stubs (empty bodies — no real assertions)

### `test/e2e/swarmkit_test.go` — REWORK

| Test | Problem |
|------|---------|
| `TestE2E_BasicDeployment` ⚠️ | Both sub-tests `t.Skip(...)` — empty stubs |
| `TestE2E_SwarmKitManagerOnly` ⚠️ | Logs "completed" with no assertions — zero-value test |
| `TestE2E_ClusterFormation` ⚠️ | Logs "completed" with no assertions |
| `TestE2E_ServiceScaling` ⚠️ | Logs "completed" with no assertions |
| `TestE2E_FailureRecovery` ⚠️ | Logs "completed" with no assertions |
| `TestE2E_NetworkIsolation` ⚠️ | Logs "completed" with no assertions |

## Valid Tests (keep)

| File | Test | Status |
|------|------|--------|
| `swarmkit_test.go` | `TestE2E_Prerequisites` | ✅ Checks infrastructure (KVM, swarmd, Firecracker, kernel) |
| `swarmkit_api_test.go` | `TestE2E_SwarmKitAPI` | ✅ Pure SwarmKit API struct validation |
| `full_workflow_test.go` | All 7 tests | ✅ Use `swarmctl` (correct!), need swarmd-firecracker executor to run |

---

## Summary

| Category | Count | Action |
|----------|-------|--------|
| **Docker Swarm (delete)** | 6 | Remove `docker_swarm_test.go`, `TestE2E_SwarmKitWithDocker`, `TestE2E_SwarmKit_TaskLifecycle` |
| **Docker dependency (rewrite)** | 1 | `TestE2E_SwarmKit_Comprehensive` → use `swarmctl` |
| **Empty stubs (rework)** | 6 | Replace or add `t.Skip("TODO: implement")` with clear plan |
| **Valid tests (keep)** | 9 | Prerequisites, API validation, full workflow tests |

**After cleanup: 0 tests depend on Docker Swarm. All integration tests use `swarmctl` + `swarmd`.**
