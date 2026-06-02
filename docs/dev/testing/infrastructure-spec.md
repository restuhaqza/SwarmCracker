# SwarmCracker Test Infrastructure & E2E — Specification

> Version: 1.0 | Date: 2026-05-16 | Status: Proposal  
> Replaces ad-hoc testing with structured, reproducible, CI-runnable test infrastructure.

---

## 1. Current State Assessment

### 1.1 What We Have

```
test/
├── e2e/
│   ├── run.sh                          # Shell-based E2E runner
│   ├── swarmkit_test.go               # Prerequisites + planned scenarios
│   ├── swarmkit_api_test.go           # API struct validation (no runtime)
│   ├── swarmkit_comprehensive_test.go  # SwarmKit spec validation (no runtime)
│   ├── full_workflow_test.go          # 7 workflow tests using swarmctl
│   ├── cluster/                        # SwarmKit manager/agent lifecycle
│   │   ├── manager.go                 # SwarmKitManager (start/stop/wait)
│   │   ├── agent.go                   # SwarmKitAgent
│   │   └── cleanup.go                 # Resource cleanup
│   ├── firecracker/                    # Firecracker VM E2E (build tag: e2e)
│   │   ├── 01_prerequisites_test.go
│   │   ├── 02_vm_launch_test.go
│   │   ├── 02_vm_launch_kvm_test.go
│   │   ├── 03_image_preparation_test.go
│   │   ├── 04_real_image_test.go
│   │   └── 05_vxlan_overlay_test.go
│   ├── scenarios/
│   │   └── basic_deploy.go            # Scenario framework (unused)
│   └── fixtures/
│       └── tasks.go                   # Task builders
├── integration/
│   ├── init_test.go                   # Init system integration (real pulls)
│   ├── integration_test.go            # Translator + network integration
│   └── snapshot_integration_test.go   # Snapshot lifecycle (build tag)
├── testinfra/
│   ├── testinfra_test.go              # 10 prerequisite checks
│   ├── helpers.go
│   ├── helpers/vxlan.go
│   └── checks/
│       ├── firecracker.go
│       ├── kernel.go
│       ├── network.go
│       └── vxlan.go
└── mocks/
    └── mocks.go                       # MockContainerRuntime (unused)
```

### 1.2 CI Pipeline (`.github/workflows/ci.yml`)

| Job | Runs | Executes |
|-----|------|----------|
| `test` | Push + PR | `go test -short -race ./pkg/...` (unit only) |
| `build` | Push + PR | `make all` |
| `lint` | Non-tag push + PR | golangci-lint |
| `benchmark` | Push to main | `go test -bench=.` |

**No integration, E2E, or testinfra in CI.**

---

## 2. Problems Identified

### 🔴 Critical

| # | Problem | Impact | Where |
|---|---------|--------|-------|
| C1 | **No E2E/integration in CI** | Regressions only caught by developer manually running `make test-e2e` | `.github/workflows/ci.yml` |
| C2 | **Network-dependent tests timeout** | `pkg/image` integration tests pull from Docker Hub; slow, flaky, auth-dependent | `test/integration/init_test.go`, `pkg/image/*_test.go` |
| C3 | **No containerized test environment** | Tests depend on host binaries (Firecracker, swarmd, kernel, docker); can't run on GitHub runners | `run.sh`, all e2e tests |

### 🟠 High

| # | Problem | Impact | Where |
|---|---------|--------|-------|
| H1 | **Duplicate prerequisite checks** | `TestE2E_Prerequisites`, `TestInfra_Prerequisites`, `run.sh:check_prerequisites` — three implementations of the same thing | 3 files |
| H2 | **Hardcoded paths everywhere** | `/tmp/swarmkit-e2e`, `/usr/share/firecracker/vmlinux`, `/home/kali/.local/share/firecracker/vmlinux` | 8+ files |
| H3 | **No test isolation** | Parallel `run.sh` invocations conflict on same socket `/tmp/swarmkit-e2e/swarm.sock` | `run.sh` |
| H4 | **No image caching** | Integration tests pull nginx:alpine, redis:alpine, alpine:latest from scratch every run | `test/integration/` |
| H5 | **Snapshotted VM test requires bootable rootfs** | Test skips silently if `ROOTFS_PATH` not set — never runs in CI | `test/integration/snapshot_integration_test.go` |

### 🟡 Medium

| # | Problem | Impact | Where |
|---|---------|--------|-------|
| M1 | **`pkill -9 firecracker` in teardown** | Kills ALL Firecracker processes on host, not just test VMs | `run.sh:119` |
| M2 | **`run.sh` asserts container runtime** | E2E doesn't actually need docker/podman (uses swarmctl + swarmd-firecracker) | `run.sh:48-55` |
| M3 | **VXLAN tests require root** | `testinfra/checks/vxlan.go` uses `sudo ip` — breaks on rootless CI | `checks/vxlan.go` |
| M4 | **No structured test output** | Can't parse results programmatically; just grep for PASS/FAIL | `run.sh:generate_report()` |
| M5 | **firecracker/ package not integrated** | E2E tests under `test/e2e/firecracker/` have `//go:build e2e` tag, not run by `run.sh` | `firecracker/*.go` |
| M6 | **No test timeout per test** | One slow test blocks entire suite; `timeout 10m` applies globally | `run.sh` |

### 🟢 Low

| # | Problem | Impact | Where |
|---|---------|--------|-------|
| L1 | **Unused `scenarios/` package** | `basic_deploy.go` exists but never imported or run | `scenarios/basic_deploy.go` |
| L2 | **Unused `fixtures/` package** | `tasks.go` has task builders not used by any test | `fixtures/tasks.go` |
| L3 | **Unused `mocks/` package** | `MockContainerRuntime` defined but no test imports it | `test/mocks/mocks.go` |
| L4 | **`strings` imported but not used** | Linter should catch | `preparer_extended_test.go` (already fixed) |

---

## 3. Target Architecture

### 3.1 Test Pyramid

```
            ┌──────────┐
            │   E2E    │  1-2 tests: full cluster lifecycle
            │ (remote) │  Runs on bare-metal/Vagrant only
            ├──────────┤
            │ Integration│ 10-15 tests: VM launch, snapshot, network
            │ (local VM) │  Runs in CI via nested KVM or QEMU
            ├──────────┤
            │ Testinfra │  10 prerequisite checks
            │           │  Runs first in CI (gates everything)
            ├──────────┤
            │   Unit    │  100+ tests: all packages
            │           │  Runs on every push, <60s
            └──────────┘
```

### 3.2 CI Pipeline (Proposed)

```
PR / Push to main:
  ┌─────────────────────────────────────────────────────┐
  │ 1. testinfra (30s)                                   │
  │    └─ go test -v ./test/testinfra/...                │
  │                                                      │
  │ 2. lint (60s)                                        │
  │    └─ golangci-lint run                              │
  │                                                      │
  │ 3. unit-test (90s)                                   │
  │    └─ go test -short -race -coverprofile=coverage.out│
  │       -covermode=atomic ./pkg/...                    │
  │                                                      │
  │ 4. build (60s)                                       │
  │    └─ make all                                       │
  │                                                      │
  │ 5. integration (5min)          ┌─ conditional ─┐     │
  │    └─ go test -tags=integration│ on self-hosted │     │
  │       -v ./test/integration/...│   runner only  │     │
  └─────────────────────────────────────────────────────┘

Push to main only:
  ┌─────────────────────────────────────────────────────┐
  │ 6. benchmark                                         │
  │    └─ go test -bench=. -benchmem ./pkg/...           │
  │                                                      │
  │ 7. coverage-upload                                   │
  │    └─ codecov/codecov-action                         │
  └─────────────────────────────────────────────────────┘

Nightly (cron) on self-hosted runner:
  ┌─────────────────────────────────────────────────────┐
  │ 8. E2E-full (15min)                                  │
  │    └─ make test-e2e                                  │
  │    └─ go test -tags=e2e ./test/e2e/firecracker/...   │
  └─────────────────────────────────────────────────────┘
```

---

## 4. Detailed Specification

### 4.1 Unified Prerequisite Checker (`testinfra`)

**Goal:** Single source of truth for all prerequisite checks, used by CI, `run.sh`, and developers.

**Implementation:**
```go
// test/testinfra/runner.go (NEW)
type CheckResult struct {
    Name    string `json:"name"`
    Status  string `json:"status"` // "pass", "fail", "skip"
    Message string `json:"message"`
    Detail  string `json:"detail,omitempty"`
}

type InfraReport struct {
    Timestamp string        `json:"timestamp"`
    Results   []CheckResult `json:"results"`
    Passed    int          `json:"passed"`
    Failed    int          `json:"failed"`
    Skipped   int          `json:"skipped"`
    Ready     bool         `json:"ready"` // true if all required checks pass
}

// Required checks (block CI):
//   - Go version >= 1.26
//   - OS == linux
//   - Arch in ["amd64", "arm64"]
// Optional checks (skip if unavailable):
//   - KVM (/dev/kvm)
//   - Firecracker binary + kernel
//   - Container runtime
//   - Network creation (needs root)
//   - Disk >= 5GB, Memory >= 4GB
//   - VXLAN kernel module
```

**Output:** JSON report to `test-reports/infra-{timestamp}.json` + human-readable stdout.

### 4.2 Containerized Test Runner

**Goal:** Run integration/E2E tests without host dependency on specific binary versions.

**Dockerfile:** `test/e2e/Dockerfile`
```dockerfile
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y \
    firecracker go1.26 docker.io iproute2 curl
COPY --from=swarmkit /swarmd /usr/local/bin/swarmd
COPY --from=swarmkit /swarmctl /usr/local/bin/swarmctl
COPY vmlinux /usr/share/firecracker/vmlinux
WORKDIR /swarmcracker
ENTRYPOINT ["go", "test", "-v", "./test/e2e/..."]
```

**Trade-off:** KVM passthrough requires `--device /dev/kvm` + `--privileged` or rootful Docker. Not all CI providers support this. Use **self-hosted GitHub Actions runner** on bare metal for E2E.

### 4.3 Image Caching for Integration Tests

**Goal:** Eliminate Docker Hub pulls during integration tests.

**Implementation:**
```go
// test/testinfra/helpers/imagecache.go (NEW)
type ImageCache struct {
    dir string // ~/.cache/swarmcracker/images/
}

func (c *ImageCache) Has(image string) bool {
    // Check if ext4 rootfs for image exists
}

func (c *ImageCache) Get(image string) string {
    // Return path to cached rootfs
}
```

**Cache Strategy:**
- `make test-cache-warm`: Pre-pull nginx:alpine, redis:alpine, alpine:latest → ext4
- Integration tests check cache first; fall back to real pull only if env `SWARMCRACKER_ALLOW_PULL=true`
- CI: use `test-cache-warm` as a separate job with artifact upload/download
- Cache key: `{image}:{digest}`

### 4.4 New `run.sh` v2

**Goal:** Idempotent, isolated, structured output, configurable.

**Key changes from current:**

| Aspect | Current | Proposed |
|--------|---------|----------|
| Socket path | Hardcoded `/tmp/swarmkit-e2e/swarm.sock` | `$SWARMCRACKER_HOME/e2e-{run-id}/swarm.sock` |
| swarmd lifecycle | Manual `kill $PID` | `start`/`stop` subcommands with grace period |
| Teardown | `pkill -9 firecracker` | Only kill VMs started by this test run (track PIDs) |
| Container runtime | Required (`exit 1` if missing) | Optional (only if using container-based images) |
| Output | grep-based summary | JSON + JUnit XML |
| Parallel runs | Conflict on socket | Isolated by run-id |
| Config | Hardcoded env vars | `test/e2e/config.yaml` |

**New CLI:**
```bash
./test/e2e/run.sh [command]

Commands:
  check         Run prerequisite checks only (delegates to testinfra)
  setup         Start swarmd + ensure environment
  test          Run E2E tests (setup + test + report)
  teardown      Stop swarmd + cleanup
  all           check + setup + test + teardown + report (default)
  
Options:
  --parallel N   Run N test groups in parallel (default: 1)
  --timeout T    Per-test timeout (default: 5m)
  --output FMT   Output format: text|json|junit (default: text)
  --skip-pull    Skip Docker Hub image pulls (use cache only)
  --config FILE  Config file path (default: test/e2e/config.yaml)
```

### 4.5 Test Environment Configuration

**New file:** `test/e2e/config.yaml`
```yaml
# SwarmCracker E2E Configuration
# All values can be overridden via environment variables

swarmkit:
  state_dir: /tmp/swarmcracker-e2e-{{run_id}}
  listen_addr: 127.0.0.1:4242
  control_socket: /tmp/swarmcracker-e2e-{{run_id}}/swarm.sock
  election_tick: 5
  heartbeat_tick: 1

firecracker:
  kernel_path: /usr/share/firecracker/vmlinux
  binary_path: /usr/local/bin/firecracker
  jailer_path: /usr/local/bin/jailer
  rootfs_dir: /tmp/swarmcracker-e2e-{{run_id}}/rootfs
  machine_pool_size: 5

image_cache:
  enabled: true
  dir: ~/.cache/swarmcracker/images
  allow_pull: false  # Set SWARMCRACKER_ALLOW_PULL=true to override

network:
  bridge_prefix: swarm-br
  vxlan_id_start: 100

timeouts:
  swarmd_ready: 30s
  vm_ready: 60s
  service_deploy: 120s
  per_test: 300s
  suite: 30m

resources:
  min_disk_gb: 5
  min_memory_gb: 4
  min_vcpus: 2
```

### 4.6 Test Isolation Key

**Goal:** Multiple developers can run E2E simultaneously.

**Implementation:**
- **Run ID:** UUID v4 generated at test start, used in all paths (`/tmp/swarmcracker-e2e-{run-id}/`)
- **Bridge names:** Include run-id: `swarm-br-{short-run-id}`
- **VXLAN IDs:** Allocated from range `[vxlan_id_start, vxlan_id_start + parallel_runs)` per run
- **Socket paths:** All under run-id directory
- **Cleanup guard:** `trap cleanup_{run-id} EXIT`

### 4.7 Structured Test Output

**Goal:** Machine-parseable results for CI dashboards.

**JUnit XML output:**
```xml
<testsuites>
  <testsuite name="e2e" tests="14" failures="1" time="120.5">
    <testcase name="TestFullWorkflow_DeployVerifyExecuteCleanup" time="45.3">
      <testcase name="Step1_DeployService" time="3.2"/>
      <testcase name="Step2_VerifyVMsRunning" time="8.1"/>
      <failure message="VM never reached Running state"/>
    </testcase>
  </testsuite>
</testsuites>
```

**Implementation:** Use `gotestsum` or `go-junit-report`:
```bash
go test -v ./test/e2e/... 2>&1 | go-junit-report > test-reports/e2e-{run-id}.xml
```

### 4.8 Integration Test Organization

**Proposed structure (replaces current):**

```
test/
├── integration/
│   ├── README.md                       # How to run
│   ├── config.yaml                     # Integration-specific config
│   ├── fixtures/                       # Shared test data
│   │   ├── rootfs_minimal.ext4         # Pre-built minimal ext4 (1MB)
│   │   └── tasks.go                    # Task builders (move from e2e/fixtures/)
│   ├── vm_test.go                      # VM launch + lifecycle
│   ├── translation_test.go             # Task→Firecracker config translation
│   ├── image_test.go                   # Image preparation (init systems)
│   ├── snapshot_test.go                # Snapshot create/restore (move from snapshot_integration_test.go)
│   ├── network_test.go                 # Network interface creation
│   ├── vxlan_test.go                   # VXLAN overlay setup
│   └── helpers.go                      # Shared helpers
│
├── e2e/
│   ├── README.md
│   ├── run.sh                          # E2E runner v2 (see §4.4)
│   ├── config.yaml                     # E2E config (see §4.5)
│   ├── docker-compose.yaml             # Containerized test environment
│   ├── full_workflow_test.go           # Full cluster lifecycle (existing, use swarmctl)
│   ├── multi_node_test.go              # Multi-node cluster (NEW — uses cluster/ package)
│   ├── failover_test.go                # Manager failover (NEW)
│   ├── upgrade_test.go                 # Rolling upgrade (NEW)
│   ├── firecracker/                    # Move to integration if uses KVM directly
│   │   └── ... (existing tests)
│   └── cluster/                        # Keep: swarmd manager/agent lifecycle
│       ├── manager.go
│       ├── agent.go
│       └── cleanup.go
```

### 4.9 Remove Dead Code

| File | Action | Reason |
|------|--------|--------|
| `test/e2e/scenarios/basic_deploy.go` | Delete | Unused, never imported |
| `test/e2e/fixtures/tasks.go` | Move to `test/integration/fixtures/` | Used by integration tests |
| `test/mocks/mocks.go` | Delete or move to `pkg/image/testing/` | Only used by image package tests |
| Duplicate prerequisite checks | Merge into `testinfra` | `TestE2E_Prerequisites`, `run.sh:check_prerequisites`, `TestInfra_Prerequisites` |

---

## 5. CI Workflow (Proposed)

### 5.1 `ci.yml` — Every PR/Push

```yaml
jobs:
  testinfra:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.26' }
      - run: go test -v -json ./test/testinfra/... | tee test-reports/infra.json
      - uses: actions/upload-artifact@v4
        with: { name: testinfra-results, path: test-reports/ }

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: golangci/golangci-lint-action@v6

  unit-test:
    needs: [testinfra, lint]
    strategy:
      matrix:
        go: ['1.25', '1.26']       # Multi-version testing
    runs-on: ubuntu-latest
    steps:
      - run: go test -short -race -coverprofile=coverage.out -covermode=atomic ./pkg/...
      - run: go tool cover -html=coverage.out -o coverage.html

  build:
    needs: [unit-test]
    strategy:
      matrix:
        goos: [linux]
        goarch: [amd64, arm64]
    runs-on: ubuntu-latest
    steps:
      - run: GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} make all

  integration:
    needs: [build]
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: [self-hosted, linux, kvm]      # Self-hosted runner with KVM
    steps:
      - run: go test -tags=integration -v -timeout=15m ./test/integration/...
```

### 5.2 `e2e.yml` — Nightly / On-demand (NEW)

```yaml
name: E2E

on:
  schedule: [{ cron: '0 2 * * *' }]    # 2 AM daily
  workflow_dispatch:                     # Manual trigger

jobs:
  e2e:
    runs-on: [self-hosted, linux, kvm, bare-metal]
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@v4
      - run: make test-e2e
      - uses: actions/upload-artifact@v4
        with: { name: e2e-results, path: test-reports/ }
```

---

## 6. Implementation Plan

### Phase 1: Foundation (2-3 days)

| Task | Pri | Est. | Description |
|------|-----|------|-------------|
| 1.1 | 🔴 | 1h | Merge prerequisite checks into single `testinfra` package |
| 1.2 | 🔴 | 2h | Add JSON output to testinfra runner |
| 1.3 | 🟠 | 1h | Create `test/e2e/config.yaml` |
| 1.4 | 🟠 | 2h | Add image cache system (`test/testinfra/helpers/imagecache.go`) |
| 1.5 | 🟡 | 0.5h | Delete `test/e2e/scenarios/` |

### Phase 2: Runner v2 (3-4 days)

| Task | Pri | Est. | Description |
|------|-----|------|-------------|
| 2.1 | 🔴 | 4h | Rewrite `run.sh` v2 with run-id isolation |
| 2.2 | 🟠 | 2h | Add JUnit XML output via `gotestsum` |
| 2.3 | 🟠 | 2h | Make container runtime optional |
| 2.4 | 🟡 | 1h | Add `--parallel` support |
| 2.5 | 🟡 | 1h | Move firecracker tests out of build-tag ghetto |

### Phase 3: CI Integration (2-3 days)

| Task | Pri | Est. | Description |
|------|-----|------|-------------|
| 3.1 | 🔴 | 2h | Update `ci.yml` with testinfra job |
| 3.2 | 🔴 | 2h | Add multi-version Go matrix |
| 3.3 | 🟠 | 3h | Set up self-hosted runner for integration tests |
| 3.4 | 🟠 | 1h | Create `e2e.yml` nightly workflow |
| 3.5 | 🟡 | 1h | Add coverage artifact upload for integration tests |

### Phase 4: Polish (1-2 days)

| Task | Pri | Est. | Description |
|------|-----|------|-------------|
| 4.1 | 🟡 | 2h | Create `test/e2e/Dockerfile` for containerized runner |
| 4.2 | 🟡 | 1h | Add `test/e2e/docker-compose.yaml` |
| 4.3 | 🟢 | 1h | Move `fixtures/tasks.go` to `integration/fixtures/` |
| 4.4 | 🟢 | 0.5h | Delete unused `test/mocks/` |
| 4.5 | 🟢 | 1h | Write `test/README.md` with test pyramid diagram |

---

## 7. Success Criteria

| Metric | Current | Target |
|--------|---------|--------|
| Unit test coverage | — | ≥85% |
| Integration tests in CI | 0 | ≥10 tests |
| E2E tests runnable nightly | Manual only | Automated (self-hosted) |
| Test isolation (parallel runs) | ❌ Conflict | ✅ UUID-isolated |
| Prerequisite check implementations | 3 duplicates | 1 (testinfra) |
| Test output machine-parseable | ❌ grep only | ✅ JUnit XML |
| Image pulls per CI run | 3-5 (real) | 0 (cached) |
| CI end-to-end (push→results) | ~3 min | ~5 min (unit) + ~8 min (integration on main) |

---

## Appendix A: Key Dependencies

| Tool | Purpose | Install |
|------|---------|---------|
| `gotestsum` | Structured test output | `go install gotest.tools/gotestsum@latest` |
| `go-junit-report` | JUnit XML conversion | `go install github.com/jstemmer/go-junit-report/v2@latest` |
| `firecracker` | VMM for E2E | GitHub releases (v1.14+) |
| `swarmd` / `swarmctl` | SwarmKit daemon + CLI | SwarmKit project (Moby) |
| Self-hosted GitHub runner | KVM-required tests | GitHub Actions docs |

## Appendix B: File Changes Summary

```
Files to CREATE:
  test/e2e/config.yaml
  test/e2e/Dockerfile
  test/e2e/docker-compose.yaml
  test/e2e/multi_node_test.go
  test/e2e/failover_test.go
  test/e2e/upgrade_test.go
  test/integration/fixtures/tasks.go
  test/testinfra/helpers/imagecache.go
  test/testinfra/runner.go
  test/README.md
  .github/workflows/e2e.yml
  docs/dev/testing/README.md

Files to MODIFY:
  test/e2e/run.sh                    → Complete rewrite (v2)
  test/e2e/swarmkit_test.go          → Remove duplicate prerequisite checks
  .github/workflows/ci.yml           → Add testinfra, matrix, integration jobs
  Makefile                           → Add test-cache-warm, test-report targets

Files to DELETE:
  test/e2e/scenarios/basic_deploy.go → Unused
  test/mocks/mocks.go                → Unused (or move to pkg/image/testing/)

Files to MOVE:
  test/e2e/fixtures/tasks.go         → test/integration/fixtures/tasks.go
```
