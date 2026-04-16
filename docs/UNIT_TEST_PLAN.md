# SwarmCracker — Unit Test Plan

> Comprehensive testing strategy for all packages, prioritized by risk and coverage gaps.

---

## Current State

### Coverage Overview

| Package | Source Files | Test Files | Exported Funcs | Tests | Status |
|---------|-------------|------------|---------------|-------|--------|
| `pkg/config` | 1 | 2 | 3 | 12 | ✅ Good |
| `pkg/translator` | 1 | 2 | 1 | 6 | ✅ Good |
| `pkg/jailer` | 2 | 2 | 4 | 22 | ✅ Good |
| `pkg/snapshot` | 1 | 1 | 2 | 23 | ✅ Good |
| `pkg/security` | 3 | 3 | 7 | 28 | ✅ Good |
| `pkg/metrics` | 1 | 1 | 1 | 7 | ✅ Good |
| `pkg/storage` | 6 | 4 | 5 | 28 | ⚠️ Gaps |
| `pkg/network` | 3 | 10 | 3 | 8 | ⚠️ Gaps |
| `pkg/lifecycle` | 1 | 7 | 1 | 14 | ✅ Good |
| `pkg/image` | 2 | 7 | 1 | 13 | ✅ Good |
| `pkg/swarmkit` | 3 | 5 | 5 | 1 | 🔴 Critical |
| `pkg/executor` | 1 | 1 | 1 | 10 | ✅ Good |
| `pkg/runtime` | 1 | 0 | 1 | 0 | 🔴 Critical |

### Priority Classification

| Priority | Packages | Reason |
|----------|----------|--------|
| **P0 — Critical** | `swarmkit/vmm`, `swarmkit/translator`, `runtime/state` | Core orchestration, zero tests, highest complexity |
| **P1 — High** | `network/vxlan`, `storage/volume_block`, `storage/credential_store` | Cross-node networking, persistent storage, zero tests |
| **P2 — Medium** | `storage/driver`, `storage/volume_meta`, `storage/volume_quota` | Infrastructure, partial coverage |
| **P3 — Low** | Existing well-tested packages | Add edge cases, error paths, fuzz targets |

---

## Phase 1: Critical (P0) — Core Orchestration

### 1.1 `pkg/swarmkit/vmm` — VMM Manager (831 LOC, 2 exported funcs, 0 tests)

**Why critical:** Manages Firecracker VM processes. Start/stop/configure are the most impactful operations. If VMMManager fails, nothing runs.

**Test file:** `pkg/swarmkit/vmm_test.go` (already exists but empty/unused)

```
Test Cases:
├── NewVMMManager
│   ├── Default config — valid paths → no error
│   ├── Invalid firecracker path → error
│   └── Empty socket dir → creates it
│
├── NewVMMManagerWithConfig
│   ├── Full config → fields populated correctly
│   ├── Jailer enabled → UseJailer = true
│   └── CgroupVersion "v2" → accepted
│
├── StartVM
│   ├── Valid config → VM starts, process tracked
│   ├── Invalid kernel path → error, no process created
│   ├── Socket already in use → error
│   ├── Jailer enabled → wraps with jailer
│   └── Context cancelled → VM not started
│
├── StopVM
│   ├── Running VM → stops cleanly, process removed
│   ├── Non-existent VM → error
│   ├── Already stopped VM → idempotent
│   └── Force stop → SIGKILL after timeout
│
├── GetVMStatus
│   ├── Running VM → returns RUNNING
│   ├── Stopped VM → returns STOPPED
│   └── Unknown VM → returns NOT_FOUND
│
├── ListVMs
│   ├── Empty → empty list
│   ├── 3 running VMs → all listed
│   └── Mixed running/stopped → only running
│
├── ConfigureVM (Firecracker PUT API)
│   ├── Valid boot-source config → 200 OK
│   ├── Invalid JSON → error
│   └── Socket not responding → connection refused error
│
├── Cleanup (deferred teardown)
│   ├── Remove socket files
│   ├── Remove jailer chroot dirs
│   └── Clean cgroup entries
│
└── Concurrent Operations
    ├── Parallel StartVM → no race conditions
    ├── Parallel StopVM → no race conditions
    └── Start + Stop same VM → deterministic result
```

**Mocking strategy:**
- Mock `os/exec.Command` via `CommandExecutor` interface (already exists in `pkg/network/mocks.go`)
- Mock HTTP calls to Firecracker API socket
- Use `testing/fstest` for filesystem operations
- Use temp directories for socket paths

---

### 1.2 `pkg/swarmkit/translator` — Task Translator (163 LOC, 1 exported func, 0 tests)

**Why critical:** Converts SwarmKit tasks to Firecracker VM configs. Wrong translation = broken VMs.

**Test file:** `pkg/swarmkit/translator_test.go` (needs creation)

```
Test Cases:
├── NewTaskTranslator
│   ├── Valid kernel + bridge IP → no error
│   ├── Empty kernel path → error
│   └── Invalid bridge IP → error
│
├── Translate (task → VM config)
│   ├── Basic task → valid VM config with defaults
│   ├── Task with env vars → env passed to rootfs
│   ├── Task with port bindings → network config correct
│   ├── Task with memory limit → machine config MemSizeMB set
│   ├── Task with CPU limit → machine config VcpuCount set
│   ├── Task with volume mounts → drive config includes mount
│   ├── Task with labels → propagated to VM metadata
│   └── Task with restart policy → reflected in config
│
├── Edge Cases
│   ├── Task with 0 replicas → empty config list
│   ├── Task with very large memory → capped at host limit
│   ├── Task with invalid image → error returned
│   └── Task with special chars in name → sanitized
│
└── Error Paths
    ├── Nil task → panic or error
    └── Missing required fields → descriptive error
```

---

### 1.3 `pkg/runtime/state` — State Manager (273 LOC, 1 exported func, 0 tests)

**Why critical:** Tracks VM lifecycle state across the system. State bugs cause ghost VMs or lost tasks.

**Test file:** `pkg/runtime/state_test.go` (needs creation)

```
Test Cases:
├── NewStateManager
│   ├── Valid data dir → created, no error
│   ├── Invalid permissions → error
│   └── Already exists → no error (idempotent)
│
├── VM State Transitions
│   ├── Created → Running → OK
│   ├── Running → Stopped → OK
│   ├── Stopped → Running → OK (restart)
│   ├── Created → Stopped → OK (pre-start cancel)
│   ├── Running → Failed → OK
│   └── Failed → Running → OK (retry)
│
├── Invalid Transitions
│   ├── Stopped → Created → error (no going back)
│   └── Same state → idempotent, no error
│
├── Persistence
│   ├── Save state → file written to disk
│   ├── Load state after restart → correct state restored
│   └── Corrupt state file → recovers gracefully
│
├── Concurrency
│   ├── Parallel state updates → no data race
│   └── Read during write → consistent snapshot
│
└── Cleanup
    ├── Remove dead entries (GC)
    └── Remove entries older than TTL
```

---

## Phase 2: High Priority (P1) — Networking & Storage

### 2.1 `pkg/network/vxlan` — VXLAN Overlay (522 LOC, 2 exported funcs, 0 tests)

**Why critical:** Cross-node VM communication depends on VXLAN. Broken overlay = isolated VMs.

**Test file:** `pkg/network/vxlan_test.go` (needs creation)

```
Test Cases:
├── StaticPeerStore
│   ├── New → empty peers
│   ├── AddPeer → appears in GetPeers
│   ├── AddPeer duplicate → no duplicate entries
│   ├── RemovePeer → removed from list
│   ├── RemovePeer nonexistent → no error
│   ├── GetPeers concurrent → safe
│   └── Initial peers → populated on creation
│
├── VXLANManager (mock netlink)
│   ├── Create VXLAN interface → link created
│   ├── Create with existing name → error
│   ├── Add FDB entry → peer reachable
│   ├── Remove FDB entry → peer removed
│   └── List peers → correct set
│
├── VXLAN Configuration
│   ├── Valid VXLAN ID (1-16777215) → OK
│   ├── VXLAN ID 0 → error
│   ├── Valid overlay IP → configured
│   ├── Invalid overlay IP → error
│   └── Custom port → configured
│
├── Peer Discovery
│   ├── Add remote node → FDB entry added
│   ├── Remove remote node → FDB entry removed
│   ├── Node rejoins → entry updated
│   └── Multiple nodes → all entries present
│
└── Error Handling
    ├── netlink operation fails → wrapped error
    ├── Permission denied → clear error message
    └── Interface exists with different config → error
```

**Mocking strategy:**
- Mock `netlink` via interface wrapper (already has `CommandExecutor`)
- Or use `netlink.Handle` with fake netns (needs interface extraction)

---

### 2.2 `pkg/storage/volume_block` — Block Storage Driver (410 LOC, 1 exported func, 0 tests)

**Test file:** `pkg/storage/volume_block_test.go` (needs creation)

```
Test Cases:
├── NewBlockDriver
│   ├── Valid base dir → driver created
│   ├── Nonexistent base dir → auto-created
│   └── Read-only base dir → error
│
├── Create Volume
│   ├── Valid name + size → file created
│   ├── Size rounded to block boundary
│   ├── Duplicate name → error
│   ├── Invalid name (spaces, special chars) → error
│   └── Max size exceeded → error
│
├── Delete Volume
│   ├── Existing volume → removed
│   ├── Non-existent volume → error
│   ├── Volume in use → error
│   └── Force delete → removes even if in use
│
├── Attach Volume
│   ├── Available volume → attached to VM
│   ├── Already attached → error
│   └── VM not found → error
│
├── Detach Volume
│   ├── Attached volume → detached
│   ├── Not attached → error
│   └── VM shutdown during detach → force cleanup
│
├── List Volumes
│   ├── Empty → empty list
│   ├── Multiple volumes → all listed with status
│   └── Filter by status → correct subset
│
└── Persistence
    ├── Metadata saved after create
    ├── Metadata saved after attach/detach
    └── Corrupt metadata → recovery attempt
```

**Mocking strategy:**
- Use `testing/fstest` for filesystem
- Temp directories for real I/O tests

---

### 2.3 `pkg/storage/credential_store` — Secrets Manager (227 LOC, 1 exported func, 0 tests)

**Test file:** `pkg/storage/credential_store_test.go` (needs creation)

```
Test Cases:
├── NewSecretManager
│   ├── Valid dirs → manager created
│   ├── Nonexistent dirs → auto-created
│   └── Readonly dirs → error
│
├── Store Secret
│   ├── String secret → stored as file
│   ├── Binary secret → stored correctly
│   ├── Large secret (>1MB) → handled
│   ├── Secret with special chars → encoded
│   └── Nil value → error
│
├── Retrieve Secret
│   ├── Existing secret → returned
│   ├── Non-existent secret → error
│   └── Corrupt secret file → error with details
│
├── List Secrets
│   ├── Empty → empty list
│   ├── Multiple secrets → all listed
│   └── Filter by prefix → correct subset
│
├── Delete Secret
│   ├── Existing → removed
│   ├── Non-existent → error
│   └── Directory cleanup when empty
│
├── Config Management
│   ├── Store config file → file written
│   ├── Retrieve config → content matches
│   └── Delete config → removed
│
└── Permissions
    ├── Secret file mode → 0600
    ├── Dir mode → 0700
    └── Ownership preserved
```

---

## Phase 3: Medium Priority (P2) — Infrastructure

### 3.1 `pkg/storage/driver` — Storage Interface (101 LOC, 0 tests)

```
Test Cases:
├── Interface Compliance
│   ├── BlockDriver implements StorageDriver
│   └── All methods have correct signatures
│
├── Driver Registry
│   ├── Register driver → available
│   ├── Duplicate name → error
│   └── Get driver → correct instance
```

### 3.2 `pkg/storage/volume_meta` — Volume Metadata (already has tests, expand)

```
Additional Test Cases:
├── JSON serialization roundtrip
├── Invalid JSON → error
├── Version mismatch → migration needed
├── Concurrent read/write → safe
└── Empty metadata → defaults
```

### 3.3 `pkg/storage/volume_quota` — Quota Management (partial coverage)

```
Additional Test Cases:
├── Quota update on running volume
├── Quota exceeded → write denied
├── Negative quota → error
└── Quota persistence across restart
```

---

## Phase 4: Low Priority (P3) — Existing Packages (Edge Cases)

### 4.1 `pkg/config` — Add tests for

```
├── Env var overrides (SWARMCRACKER_*)
├── Config file + env var merge (env wins)
├── Invalid YAML → descriptive errors
├── Missing required fields → validation errors
└── Config migration (old → new format)
```

### 4.2 `pkg/jailer` — Add tests for

```
├── Jailer + cgroup integration
├── Resource limit edge cases (0, negative, very large)
├── Chroot escape prevention
└── Cleanup on crash (orphaned chroots)
```

### 4.3 `pkg/network/manager` — Add tests for

```
├── Bridge + VXLAN interaction
├── Concurrent VM network setup
├── Network namespace isolation
└── IPAM exhaustion handling
```

### 4.4 `pkg/snapshot` — Add tests for

```
├── Snapshot of running vs stopped VM
├── Concurrent snapshot + restore
├── Disk full during snapshot → partial cleanup
├── Snapshot metadata integrity
└── Large VM snapshot performance
```

### 4.5 Fuzz Targets (new)

```
Create fuzz targets in test/fuzz/:

├── config_fuzz.go        — Fuzz YAML config parsing
├── translator_fuzz.go    — Fuzz task → VM config translation
├── snapshot_meta_fuzz.go — Fuzz snapshot metadata JSON
└── vxlan_config_fuzz.go  — Fuzz VXLAN configuration
```

---

## Implementation Order

### Sprint 1 (P0 — ~3-4 days)
| Day | Task | Files |
|-----|------|-------|
| 1 | `swarmkit/vmm_test.go` — constructor + StartVM/StopVM | New tests |
| 2 | `swarmkit/vmm_test.go` — GetVMStatus, ListVMs, ConfigureVM, Cleanup | New tests |
| 3 | `swarmkit/translator_test.go` — full coverage | New file |
| 3 | `runtime/state_test.go` — constructor + transitions | New file |
| 4 | `runtime/state_test.go` — persistence + concurrency | New tests |

### Sprint 2 (P1 — ~3-4 days)
| Day | Task | Files |
|-----|------|-------|
| 1 | `network/vxlan_test.go` — StaticPeerStore | New file |
| 2 | `network/vxlan_test.go` — VXLANManager (mocked) | New tests |
| 3 | `storage/volume_block_test.go` — CRUD operations | New file |
| 3 | `storage/credential_store_test.go` — secrets CRUD | New file |
| 4 | `storage/volume_block_test.go` — attach/detach/persistence | New tests |

### Sprint 3 (P2 + P3 — ~2-3 days)
| Day | Task | Files |
|-----|------|-------|
| 1 | `storage/driver_test.go` + expand meta/quota tests | New + extend |
| 2 | Expand config, jailer, network/manager tests | Extend |
| 3 | Expand snapshot tests + create fuzz targets | Extend + new |

---

## Testing Conventions

### File Naming
```
pkg/<package>/<name>_test.go           # Main test file
pkg/<package>/<name>_integration_test.go # Integration tests (build tag)
pkg/<package>/<name>_mock_test.go       # Tests using mocks
```

### Build Tags
```go
//go:build unit           // Fast, no external deps
//go:build integration    // May need Docker, network
//go:build e2e           // Full cluster required
```

### Test Structure
```go
func TestFunctionName_Scenario_ExpectedBehavior(t *testing.T) {
    // Arrange
    cfg := NewDefaultConfig()
    
    // Act
    result, err := DoThing(cfg)
    
    // Assert
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if result != expected {
        t.Errorf("got %v, want %v", result, expected)
    }
}
```

### Table-Driven Tests
```go
func TestParseSize(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    int64
        wantErr bool
    }{
        {"gigabytes", "1G", 1073741824, false},
        {"megabytes", "512M", 536870912, false},
        {"invalid", "abc", 0, true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParseSize(tt.input)
            // assertions...
        })
    }
}
```

---

## Mocking Strategy

### Interfaces to Extract/Mock

| Package | What to Mock | How |
|---------|-------------|-----|
| `swarmkit/vmm` | `exec.Command`, HTTP client | `CommandExecutor` interface |
| `network/vxlan` | `netlink` operations | `NetlinkHandle` interface |
| `storage/volume_block` | Filesystem I/O | `testing/fstest` + temp dirs |
| `runtime/state` | File persistence | `testing/fstest` |

### Mock Generation
```bash
# Using go generate
go generate ./pkg/...

# Or manually with mockgen
mockgen -source=pkg/swarmkit/vmm.go -destination=pkg/swarmkit/vmm_mock.go
```

### Test Helpers
```
Create test/helpers/helpers.go:
├── NewTempDir(t)           — temp directory, cleaned up after test
├── NewTestConfig(t)        — valid config with temp paths
├── MockFirecrackerSocket(t) — fake Firecracker API socket
├── MockNetlinkHandle(t)    — fake netlink operations
└── AssertEqualJSON(t, a, b) — compare JSON structs
```

---

## Coverage Targets

| Metric | Current | Target |
|--------|---------|--------|
| Overall pkg coverage | ~60% (est.) | **80%+** |
| P0 packages | ~5% | **90%+** |
| P1 packages | 0% | **80%+** |
| P2 packages | ~40% | **70%+** |
| Critical paths (VM start, network, storage) | ~40% | **95%+** |

### Measuring Coverage
```bash
# All unit tests with coverage
go test -short -coverprofile=coverage.out ./pkg/...
go tool cover -func=coverage.out | grep -v "_test.go"

# HTML report
go tool cover -html=coverage.out -o coverage.html

# Per-package coverage
go test -short -cover ./pkg/config/
go test -short -cover ./pkg/swarmkit/
go test -short -cover ./pkg/runtime/
```

---

## CI Integration

### GitHub Actions (update existing `.github/workflows/ci.yml`)

```yaml
test-unit:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.24'
    - run: make test-quick
    - run: make lint
    
test-unit-coverage:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
    - with:
        go-version: '1.24'
    - run: go test -short -coverprofile=coverage.out ./pkg/...
    - name: Check coverage threshold
      run: |
        COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print $3}' | tr -d '%')
        if (( $(echo "$COVERAGE < 70" | bc -l) )); then
          echo "Coverage $COVERAGE% is below 70% threshold"
          exit 1
        fi
```

---

## Summary

| Priority | Packages | New Tests (est.) | LOC (est.) | Days |
|----------|----------|-------------------|------------|------|
| **P0 Critical** | vmm, translator, state | ~45 | ~1800 | 3-4 |
| **P1 High** | vxlan, volume_block, credential_store | ~35 | ~1400 | 3-4 |
| **P2 Medium** | driver, meta, quota | ~15 | ~600 | 1-2 |
| **P3 Low** | config, jailer, network, snapshot, fuzz | ~20 | ~800 | 2-3 |
| **Total** | | **~115** | **~4600** | **9-13** |

---

*This plan is a living document. Update as tests are implemented and coverage gaps shift.*
