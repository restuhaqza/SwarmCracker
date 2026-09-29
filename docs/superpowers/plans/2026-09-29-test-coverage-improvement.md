# Test Coverage Improvement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Raise CI-measured statement coverage of `./pkg/...` from 80.9% to ≥85%, while fixing the test defects and out-of-CI gaps that currently make the suite report misleading results.

**Architecture:** Two workstreams. First make the instrument trustworthy: repair tests whose expectations are wrong or environment-dependent, fix the two production bugs those tests were hiding, and stop ignoring `./cmd/...`. Then add coverage where it is cheapest per statement: `pkg/cni` (55.2%, ~507 uncovered statements) is the single largest lever; zero-test packages (`apiversion`, `logging`, `types`) and the test-only `network/testhelpers` package are near-free wins; the remaining mid-tier gaps (`network`, `swarmkit`, `image`, `lifecycle`, `config`) are error-path/edge-case additions to existing test files.

**Tech Stack:** Go 1.26+ (repo local `go1.27.1`), `go test -race -covermode=atomic`, `github.com/stretchr/testify`, `github.com/moby/swarmkit/v2/api`, GitHub Actions, Codecov.

**Spec:** `docs/dev/testing/unit-tests.md` (testing strategy) and `AGENTS.md` §Testing Strategy ("Overall target: 85%"). The "Current State" table in `unit-tests.md` is stale and is corrected by Task 16.

---

## Verified Baseline (2026-09-29)

Measured on `main` at commit `baf4d4d` with the uncommitted benchmark-drain fix present.

| Command | Scope | Total |
|---|---|---|
| `go test -short -race -coverprofile=coverage.out -covermode=atomic ./pkg/...` (CI) | `./pkg/...` | **80.9%** |
| `go test -race -coverprofile=coverage.out -covermode=atomic ./pkg/...` (full) | `./pkg/...` | **81.4%** |

Gap to 85% ≈ **4.1 points ≈ ~375 statements** (denominator ≈ 9,175). Package coverage, CI `-short`:

| Package | CI coverage | Uncovered (full profile) |
|---|---|---|
| `pkg/apiversion` | 0.0% | 5 |
| `pkg/logging` | 0.0% | 19 |
| `pkg/types` | 0.0% | 8 |
| `pkg/network/testhelpers` | 0.0% | 85 (test-only package) |
| `pkg/cni` | **55.2%** | **507** |
| `pkg/config` | 75.3% | 41 |
| `pkg/discovery` | 80.6% | 13 |
| `pkg/lifecycle` | 82.1% | 74 |
| `pkg/network` | 83.4% | 212 |
| `pkg/metrics` | 84.4% | 28 |
| `pkg/console` | 85.0% | 37 |
| `pkg/image` | 85.5% | 149 |
| `pkg/swarmkit` | 85.9% | 207 |
| `pkg/runtime` | 86.0% | 37 |
| `pkg/storage` | 86.1% | 116 |
| `pkg/snapshot` | 87.9% | 43 |
| `pkg/health` | 89.5% | 8 |
| `pkg/executor` | 90.7% | 8 |
| `pkg/jailer` | 92.6% | 40 |
| `pkg/translator` | 94.9% | 9 |

### Full-suite failures (non-`-short`) and their true causes

All were reproduced and root-caused; none are CI failures, but three classes are real defects.

| Failing tests | Cause | Classification |
|---|---|---|
| `cni`: `TestDefaultCommandExecutor_Execute`, `..._WithContext`, `..._Env` | Tests misuse `Execute(ctx, name, stdin, env)` as if name were command+args; the interface runs `exec.CommandContext(ctx, name)` with **no arguments** | **Real test bug** (hidden by `-short`) |
| `image`: `TestCopyDirectory_NonExistentSource`, `TestCopyDirectory/copy non-existent source`, `TestCreateExt4Image_ExecErrorPaths/nonexistent source` | Assert on literal `/nonexistent/path`, which **exists on this dev machine** | **Fragile test** (passes on clean CI) |
| `network`: `TestDefaultTAPExecutor_CombinedOutputFails`, `TestDefaultExecuteWithOutput_RealCommand` | Assert `ls /nonexistent` fails; `/nonexistent` **exists here** | **Fragile test** |
| `swarmkit`: `TestVMMManagerConfigDefaults`, `TestVMMManagerConfigDefaultsUnit` | Require `mkdir /var/lib/swarmcracker` as non-root; skipped on CI because no `firecracker` binary | **Environment** (correctly skipped on CI) |
| `lifecycle`: 5× `TestGracefulShutdown_*` | Child is a zombie (never reaped) so `Signal(0)` never reports exit; after grace period `forceKillVM` kills but **never sets state to stopped** | **Real production bug + test artifact** |
| `cmd/swarmcracker` (not run in CI at all): `TestPumpStdinInterceptsDetach` | `pumpStdin` returns `errDetach` **before flushing** bytes already accumulated in `out` | **Real production bug** (invisible to CI) |

## Global Constraints

- Go module is `github.com/restuhaqza/swarmcracker`; `go.mod` declares `go 1.26`; `.github/workflows/ci.yml`, `Dockerfile`, and `Makefile check-go-version` must stay aligned at `1.26`.
- CI test command is currently exactly `go test -short -race -coverprofile=coverage.out -covermode=atomic ./pkg/...`; coverage is uploaded to Codecov for pushes to `main` only.
- Tests must pass as a non-root user with no Firecracker/jailer binaries, no `/dev/kvm`, and no network privileges. Anything requiring root must call `t.Skip`.
- Do not add new third-party test dependencies; use `stretchr/testify` and the standard library, which are already used throughout.
- Tests live in-package (e.g. `package cni`) unless an external-package test is already the norm in that directory.
- New test files follow `<name>_test.go`; `docs/dev/testing/unit-tests.md` §Testing Conventions.
- `coverage.out` / `coverage.html` are gitignored build artifacts; never commit them.
- Every task commits separately with a conventional-commit message.

## Review Focus

Input classes / failure modes the current suite does not pin. Each gets a test in the owning task below.

1. **A path that "cannot exist" may exist.** Tests asserting failure on `/nonexistent` silently pass or fail depending on the host. A missing path must be derived from `t.TempDir()`.
2. **Bare command execution contract.** `DefaultCommandExecutor.Execute` takes `(ctx, name, stdin, env)` and passes no argv; behavior for a command that needs args, and for an env-var-only contract, is unpinned.
3. **Detach sequence straddling reads with pending output.** Ctrl-P Ctrl-Q split across `Read` calls after buffered bytes must flush those bytes before signalling `errDetach`.
4. **State after forced kill.** Killing the VM process must leave `VMInstance` in `VMStateStopped`, not `VMStateStopping`.
5. **IP allocator boundaries.** Reserved addresses, pool exhaustion, and the VIP range start must not hand out the gateway, network, or broadcast addresses.

---

### Task 1: Correct the `pkg/cni` command-executor tests

**Files:**
- Modify: `pkg/cni/executor_test.go`

**Interfaces:**
- Consumes: `NewDefaultCommandExecutor() CommandExecutor`; `DefaultCommandExecutor.Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error)`; `DefaultCommandExecutor{Timeout time.Duration}`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the current failure**

Run: `go test -run 'TestDefaultCommandExecutor_Execute' -v ./pkg/cni/`
Expected: FAIL — `"\n" does not contain "test"` (echo ignores stdin), `"signal: killed" does not contain "context deadline exceeded"`, `"" does not contain "MY_VAR=test123"`.

- [ ] **Step 2: Rewrite the three tests to match the real contract**

The executor runs the named binary with no arguments; stdin is piped, env is appended to `os.Environ()`. Replace the broken bodies with:

```go
func TestDefaultCommandExecutor_Execute(t *testing.T) {
	// echo is not usable: Execute passes no args. Use an argv-free script that
	// writes a fixed string.
	script := filepath.Join(t.TempDir(), "say.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf test\n"), 0o755))

	stdout, stderr, err := NewDefaultCommandExecutor().Execute(context.Background(), script, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", string(stdout))
	assert.Empty(t, stderr)
}

func TestDefaultCommandExecutor_Execute_Env(t *testing.T) {
	stdout, _, err := NewDefaultCommandExecutor().Execute(context.Background(), "env", nil, []string{"MY_VAR=test123"})
	require.NoError(t, err)
	assert.Contains(t, string(stdout), "MY_VAR=test123")
}

func TestDefaultCommandExecutor_Execute_WithContext(t *testing.T) {
	script := filepath.Join(t.TempDir(), "sleep.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 10\n"), 0o755))

	executor := &DefaultCommandExecutor{Timeout: 50 * time.Millisecond}
	_, _, err := executor.Execute(context.Background(), script, nil, nil)
	require.Error(t, err)
}
```

Move `TestDefaultCommandExecutor_Execute_NonexistentCommand` and `..._CommandFailure` out of the `testing.Short()` guard so the error paths also run in CI.

- [ ] **Step 3: Run the tests**

Run: `go test -run 'TestDefaultCommandExecutor' -v ./pkg/cni/`
Expected: PASS (all tests, with and without `-short`).

- [ ] **Step 4: Commit**

```bash
git add pkg/cni/executor_test.go
git commit -m "test(cni): fix command-executor tests to match argv-less Execute contract"
```

---

### Task 2: Remove environment-dependent "missing path" assertions

**Files:**
- Modify: `pkg/image/coverage_gap_test.go:415-418`
- Modify: `pkg/image/preparer_coverage_test.go:287-292`
- Modify: `pkg/image/preparer_exec_test.go` (`TestCreateExt4Image_ExecErrorPaths`, "nonexistent source" subtest)
- Modify: `pkg/network/cni_unit_test.go:279-288`
- Modify: `pkg/network/executor_unit_test.go:111-113`

**Interfaces:**
- Consumes: existing `copyDirectory(src, dst string) error`, `(*ImagePreparer).createExt4Image(...)`, `(*DefaultTAPExecutor).Command(name string, args ...string) *exec.Cmd`, `(*DefaultTAPExecutor).CombinedOutput(cmd *exec.Cmd) ([]byte, error)`, `defaultExecuteWithOutput(name string, args ...string) (string, error)`.
- Produces: a shared helper, described below.

- [ ] **Step 1: Confirm the current failure**

Run: `go test -run 'TestCopyDirectory|TestCreateExt4Image_ExecErrorPaths' -v ./pkg/image/ && go test -run 'TestDefaultTAPExecutor_CombinedOutputFails|TestDefaultExecuteWithOutput_RealCommand' -v ./pkg/network/`
Expected: FAIL with "An error is expected but got nil" — because `/nonexistent` and `/nonexistent/path` exist on this host. Verify with `ls -ld /nonexistent/path`.

- [ ] **Step 2: Add a helper for a guaranteed-absent path**

In each modified package add a small helper (or inline it):

```go
// absentPath returns a path under t.TempDir() that is guaranteed not to exist.
func absentPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "does-not-exist")
}
```

Replace every literal `/nonexistent` / `/nonexistent/path` in these five tests with `absentPath(t)`. For the network tests, keep the `ls`-based failure assertion but point at the absent path; for `defaultExecuteWithOutput`, use `defaultExecuteWithOutput("ls", absentPath(t))`.

- [ ] **Step 3: Run the tests**

Run: `go test -short -run 'TestCopyDirectory|TestCreateExt4Image_ExecErrorPaths' -v ./pkg/image/ && go test -short -run 'TestDefaultTAPExecutor_CombinedOutputFails|TestDefaultExecuteWithOutput_RealCommand|TestDefaultExecute_RealCommand' -v ./pkg/network/`
Expected: PASS both with and without `-short`.

- [ ] **Step 4: Commit**

```bash
git add pkg/image/coverage_gap_test.go pkg/image/preparer_coverage_test.go pkg/image/preparer_exec_test.go pkg/network/cni_unit_test.go pkg/network/executor_unit_test.go
git commit -m "test: derive missing paths from t.TempDir instead of /nonexistent"
```

---

### Task 3: Fix `pumpStdin` losing buffered bytes on detach

**Files:**
- Modify: `cmd/swarmcracker/cmd_vm_attach.go:110-146`
- Test: `cmd/swarmcracker/cmd_vm_test.go` (existing `TestPumpStdinInterceptsDetach`)

**Interfaces:**
- Consumes: `func pumpStdin(r io.Reader, w io.Writer) error`; `errDetach`; `detachPrefix = 0x10`; `detachSuffix = 0x11`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the failing test**

Run: `go test -run 'TestPumpStdin' -v ./cmd/swarmcracker/`
Expected: FAIL — `Expected only 'a' before detach, got ""`.

- [ ] **Step 2: Flush accumulated output before returning `errDetach`**

In `pumpStdin`, at the current `if b == detachSuffix { return errDetach }` branch, write the already-accumulated `out` to `w` first (and surface a write error), then return `errDetach`. The `pendingCtrlP` reset and the `continue` for `detachPrefix` must be preserved.

- [ ] **Step 3: Run the detach tests**

Run: `go test -run 'TestPumpStdin' -v ./cmd/swarmcracker/`
Expected: PASS — `TestPumpStdinForwardsInput`, `TestPumpStdinInterceptsDetach`, `TestPumpStdinDetachAcrossReads`, `TestPumpStdinPassesLoneCtrlP`.

- [ ] **Step 4: Run the whole CLI package**

Run: `go test ./cmd/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/swarmcracker/cmd_vm_attach.go
git commit -m "fix(console): flush pending stdin bytes before detaching"
```

---

### Task 4: Run `./cmd/...` tests in CI

**Files:**
- Modify: `.github/workflows/ci.yml:49-50`

**Interfaces:**
- Consumes: Task 3 (CLI tests must be green first).
- Produces: nothing other tasks use.

- [ ] **Step 1: Add a CLI-test step that does not dilute the coverage gate**

Keep the coverage command scoped to `./pkg/...`. Add a separate step immediately after it:

```yaml
      - name: Test CLI packages
        run: go test -short -race ./cmd/...
```

Rationale: `./cmd/...` is largely untested; merging it into the same `coverage.out` would lower the reported number without measuring production risk. A separate gate catches regressions like Task 3's bug.

- [ ] **Step 2: Verify locally**

Run: `go test -short -race ./cmd/...`
Expected: PASS, no `FAIL`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: run cmd package tests"
```

---

### Task 5: Fix VM state after forced kill

**Files:**
- Modify: `pkg/lifecycle/vmm.go:696-703` (`forceKillVM`)
- Test: `pkg/lifecycle/lifecycle_graceful_test.go`, `pkg/lifecycle/lifecycle_coverage_test.go`

**Interfaces:**
- Consumes: `(*VMInstance).SetState(state VMState)`, `VMStateStopped`, `(*VMMManager).gracefulShutdown(ctx context.Context, vmInstance *VMInstance) error`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the failing tests**

Run: `go test -run 'TestGracefulShutdown' -v ./pkg/lifecycle/`
Expected: FAIL — `expected: "stopped", actual: "running"`, and `should complete within 2 seconds`.

- [ ] **Step 2: Set state to stopped once the process is killed**

In `forceKillVM`, after `process.Kill()` succeeds, call `vmInstance.SetState(VMStateStopped)` before returning `nil`. If `Kill()` returns an error, return it without changing state.

- [ ] **Step 3: Reap the child so exit is observable**

In the five `TestGracefulShutdown_*` tests that start `exec.Command("sleep", ...)`, reap the child concurrently so `process.Signal(syscall.Signal(0))` returns an error once it exits:

```go
	go func() { _ = cmd.Wait() }()
```

Remove the now-redundant trailing `cmd.Wait()` calls. Keep the `testing.Short()` guards as-is.

- [ ] **Step 4: Run the tests**

Run: `go test -run 'TestGracefulShutdown' -v ./pkg/lifecycle/`
Expected: PASS, each well under its grace period.

- [ ] **Step 5: Run the package**

Run: `go test -short -race ./pkg/lifecycle/`
Expected: PASS. Coverage should not decrease.

- [ ] **Step 6: Commit**

```bash
git add pkg/lifecycle/vmm.go pkg/lifecycle/lifecycle_graceful_test.go pkg/lifecycle/lifecycle_coverage_test.go
git commit -m "fix(lifecycle): mark VM stopped after forced kill"
```

---

### Task 6: Cover `CNINetworkAllocator` network allocation

**Files:**
- Create: `pkg/cni/allocator_methods_test.go`
- Reference: `pkg/cni/allocator.go:65-151`, `pkg/cni/cni_coverage_test.go:318` (`setupCNIProvider`)

**Interfaces:**
- Consumes: `NewCNINetworkAllocator(provider *CNIProvider, cfg *networkallocator.Config) (*CNINetworkAllocator, error)`; `(*CNINetworkAllocator).Allocate(n *api.Network) error`; `.Deallocate(n *api.Network) error`; `.GetAllocatedNetwork(networkID string) (*AllocatedNetwork, error)`; `.ListAllocatedNetworks() []*AllocatedNetwork`; `.IsAllocated(n *api.Network) bool`; `setupCNIProvider(t *testing.T) *CNIProvider`; `api.Network`, `api.NetworkSpec`.
- Produces: `func newTestAllocator(t *testing.T) *CNINetworkAllocator` in `pkg/cni/allocator_methods_test.go`, reused by Tasks 7-8. It calls `setupCNIProvider(t)` then `NewCNINetworkAllocator(provider, nil)`, and registers no cleanup beyond `t.TempDir()`.

- [ ] **Step 1: Confirm the gap**

Run: `go test -cover -run 'TestCNINetworkAllocator_Allocate' ./pkg/cni/`
Expected: no test matches; `go tool cover` lists `Allocate`, `Deallocate`, `GetAllocatedNetwork`, `ListAllocatedNetworks` at 0.0%.

- [ ] **Step 2: Write the tests**

Add tests with these names and assertions:

```go
func TestCNINetworkAllocator_Allocate_Bridge(t *testing.T)     // Allocate succeeds; IsAllocated true; DriverState non-nil; IPAM non-nil
func TestCNINetworkAllocator_Allocate_Nil(t *testing.T)        // Allocate(nil) errors
func TestCNINetworkAllocator_Allocate_Idempotent(t *testing.T) // second Allocate returns nil, map size unchanged
func TestCNINetworkAllocator_Deallocate(t *testing.T)          // after Allocate: Deallocate succeeds, IsAllocated false
func TestCNINetworkAllocator_Deallocate_NotAllocated(t *testing.T) // Deallocate on unknown ID returns nil
func TestCNINetworkAllocator_Deallocate_Nil(t *testing.T)      // Deallocate(nil) errors
func TestCNINetworkAllocator_GetListAllocated(t *testing.T)    // missing ID errors; List returns allocated entries
```

Use a network like `&api.Network{ID: "net-1", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "test-net"}}}`. The default driver must be `bridge`, which `setupCNIProvider` already provides a fake plugin for.

- [ ] **Step 3: Verify**

Run: `go test -cover -run 'TestCNINetworkAllocator' ./pkg/cni/`
Expected: PASS; `Allocate`, `Deallocate`, `GetAllocatedNetwork`, `ListAllocatedNetworks` no longer at 0.0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/cni/allocator_methods_test.go
git commit -m "test(cni): cover network allocate/deallocate paths"
```

---

### Task 7: Cover service and task allocation in `pkg/cni`

**Files:**
- Create: `pkg/cni/allocator_service_task_test.go`
- Reference: `pkg/cni/allocator.go:156-475`, `:607-644`

**Interfaces:**
- Consumes: `(*CNINetworkAllocator).AllocateService(s *api.Service) error`, `.DeallocateService(s *api.Service) error`, `.IsServiceAllocated(s *api.Service, flags ...func(*networkallocator.ServiceAllocationOpts)) bool`, `.AllocateTask(t *api.Task) error`, `.DeallocateTask(t *api.Task) error`, `.IsTaskAllocated(t *api.Task) bool`, `.AllocateAttachment(node *api.Node, na *api.NetworkAttachment) error`, `.DeallocateAttachment(node *api.Node, na *api.NetworkAttachment) error`, `.IsAttachmentAllocated(node *api.Node, na *api.NetworkAttachment) bool`, `parsePublishedPorts(s *api.Service) []PublishedPort`; `newTestAllocator(t)` from Task 6.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go tool cover -func=/dev/stdin ./pkg/cni` is not valid — instead run `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'AllocateService|AllocateTask|AllocateAttachment|parsePublishedPorts'` and confirm each is `0.0%`.

- [ ] **Step 2: Write the tests**

```go
func TestCNINetworkAllocator_AllocateService(t *testing.T)      // allocate network first; AllocateService assigns VIP(s); IsServiceAllocated true
func TestCNINetworkAllocator_AllocateService_NoNetworks(t *testing.T) // service with zero networks returns nil
func TestCNINetworkAllocator_IsServiceAllocated_Nil(t *testing.T)     // nil service => false
func TestCNINetworkAllocator_AllocateTask(t *testing.T)         // task with a NetworkAttachment gets an IP (see api.NetworkAttachment)
func TestCNINetworkAllocator_DeallocateTask(t *testing.T)
func TestCNINetworkAllocator_AllocateAttachment(t *testing.T)   // node attachment gets an IPAddress and MACAddress
func TestCNINetworkAllocator_DeallocateAttachment(t *testing.T)
func TestParsePublishedPorts(t *testing.T)                      // table-driven: empty, tcp/udp, host-mode, ingress
```

For `parsePublishedPorts`, build `&api.Service{Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{Ports: []*api.PortConfig{...}}}}` and assert the `Port`, `PublishedPort`, `Protocol`, `PublishMode` fields.

- [ ] **Step 3: Verify**

Run: `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'AllocateService|AllocateTask|AllocateAttachment|parsePublishedPorts'`
Expected: PASS and each function > 0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/cni/allocator_service_task_test.go
git commit -m "test(cni): cover service, task, and attachment allocation"
```

---

### Task 8: Cover `IPAMManager` VIP allocation and pool boundaries

**Files:**
- Create: `pkg/cni/ipam_vip_test.go`
- Reference: `pkg/cni/ipam.go:163-315`

**Interfaces:**
- Consumes: `NewIPAMManager(config *CNIConfig) *IPAMManager`; `(*IPAMManager).CreatePool(subnetCIDR string, gatewayIP net.IP) (*IPPool, error)`; `.AllocateIP(subnetCIDR, ownerID string) (net.IP, error)`; `.AllocateVIP(subnetCIDR, serviceID string) (net.IP, error)`; `.ReleaseVIP(vip net.IP, subnetCIDR, serviceID string) error`; `getVIPRangeStart(subnet *net.IPNet) net.IP`; `isReserved(pool *IPPool, ip net.IP) bool`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'AllocateVIP|ReleaseVIP|getVIPRangeStart'`
Expected: each `0.0%`.

- [ ] **Step 2: Write the tests**

```go
func TestIPAMManager_AllocateVIP(t *testing.T)          // pool 10.0.0.0/24; VIP is inside range, not gateway, not network/broadcast
func TestIPAMManager_AllocateVIP_NoPool(t *testing.T)   // unknown subnet => error
func TestIPAMManager_ReleaseVIP(t *testing.T)           // Allocate then Release => owner cleared; unknown VIP => error
func TestGetVIPRangeStart(t *testing.T)                 // table: /24, /16, IPv6 subnet => expected offset
func TestIPAMManager_AllocateIP_Reserved(t *testing.T)  // gateway, network address, and broadcast are never handed out
func TestIPAMManager_AllocateIP_Exhaustion(t *testing.T) // tiny pool (/30) => error after capacity reached
```

- [ ] **Step 3: Verify**

Run: `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'AllocateVIP|ReleaseVIP|getVIPRangeStart'`
Expected: PASS, each > 0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/cni/ipam_vip_test.go
git commit -m "test(cni): cover VIP allocation and IPAM boundaries"
```

---

### Task 9: Cover `CNIProvider` network lookup and `files.go` validation

**Files:**
- Create: `pkg/cni/provider_files_test.go`
- Reference: `pkg/cni/provider.go:68-245`, `pkg/cni/files.go:63-76`, `pkg/cni/allocator.go:626-644`

**Interfaces:**
- Consumes: `(*CNIProvider).PredefinedNetworks() []networkallocator.PredefinedNetworkData`; `.GetNetwork(networkID string) (*AllocatedNetwork, error)`; `.AllocateNetwork(name, driver string) (*AllocatedNetwork, error)`; `setupCNIProvider(t)`; `validateCNINetworkName(name string) error`; `RemoveCNIConfig(configDir, networkName string) error`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'PredefinedNetworks|GetNetwork|AllocateNetwork|validateCNINetworkName|RemoveCNIConfig'`
Expected: each `0.0%`.

- [ ] **Step 2: Write the tests**

```go
func TestCNIProvider_PredefinedNetworks(t *testing.T)   // contains ingress and gw-bridge entries with driver bridge
func TestCNIProvider_AllocateNetwork_Bridge(t *testing.T) // subnet/gateway/bridge name assigned and config file written
func TestCNIProvider_AllocateNetwork_VXLAN(t *testing.T)  // VXLANID assigned
func TestCNIProvider_GetNetwork(t *testing.T)             // network stored under the "net-N" ID returned by AllocateNetwork is found; unknown ID => error
func TestValidateCNINetworkName(t *testing.T)             // table: valid names pass; empty, "/", "..", names > 255 chars, and names with ":" or "/" fail
func TestRemoveCNIConfig(t *testing.T)                    // writes a config file, removes it; absent file is not an error
```

- [ ] **Step 3: Verify**

Run: `go test -coverprofile=/tmp/cni.out ./pkg/cni/ && go tool cover -func=/tmp/cni.out | grep -E 'PredefinedNetworks|GetNetwork|AllocateNetwork|validateCNINetworkName|RemoveCNIConfig'`
Expected: PASS, each > 0%. Then run `go test -race -cover ./pkg/cni/` and confirm `pkg/cni` is ≥ 85%.

- [ ] **Step 4: Commit**

```bash
git add pkg/cni/provider_files_test.go
git commit -m "test(cni): cover provider lookup and config-file validation"
```

---

### Task 10: Zero-test packages — `apiversion`, `logging`, `types`

**Files:**
- Create: `pkg/apiversion/version_test.go`
- Create: `pkg/logging/adapter_test.go`
- Modify: `pkg/types/task_test.go` (exists and tests struct construction only; `GetContainer`/`SetContainer` are uncovered)

**Interfaces:**
- Consumes: `apiversion.WithVersion() grpc.DialOption`, `apiversion.VersionClientInterceptor() grpc.UnaryClientInterceptor`, `apiversion.Current`; `logging.InstallZerologHook(logger zerolog.Logger)`, `(*logging.ZerologHook).Fire(entry *logrus.Entry) error`, `(*logging.ZerologHook).Levels() []logrus.Level`; `(*types.TaskSpec).GetContainer() (*types.Container, error)`, `(*types.TaskSpec).SetContainer(c *types.Container)`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -cover ./pkg/apiversion/ ./pkg/logging/ ./pkg/types/`
Expected: `coverage: 0.0% of statements` for each (no test files / tests do not exercise these functions).

- [ ] **Step 2: Write the tests**

```go
// apiversion/version_test.go
func TestVersionClientInterceptor_InjectsMetadata(t *testing.T) // fake invoker reads metadata.FromOutgoingContext; assert x-swarmcracker-version == Current
func TestWithVersion_ReturnsDialOption(t *testing.T)            // WithVersion() is non-nil

// logging/adapter_test.go
func TestZerologHook_Fire_AllLevels(t *testing.T)   // table over logrus levels => no error, message emitted
func TestZerologHook_Levels(t *testing.T)           // equals logrus.AllLevels
func TestInstallZerologHook(t *testing.T)           // does not panic; subsequent logrus.Info is forwarded

// types/task_test.go (append)
func TestTaskSpec_GetContainer(t *testing.T)  // nil runtime => error; wrong type (e.g. &Container{} pointer stored as string) => error; *Container => same pointer
func TestTaskSpec_SetContainer(t *testing.T)  // sets Runtime and RuntimeType == RuntimeContainer
```

For the logging tests, write the hook's output to a `bytes.Buffer` via `zerolog.New(&buf)` and assert the message text. For the `PanicLevel` row, drive `Fire` directly with a constructed `logrus.Entry` rather than calling `logrus.Panic` (which would abort the test).

- [ ] **Step 3: Verify**

Run: `go test -cover ./pkg/apiversion/ ./pkg/logging/ ./pkg/types/`
Expected: each ≥ 80%; none at 0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/apiversion/version_test.go pkg/logging/adapter_test.go pkg/types/task_test.go
git commit -m "test: cover apiversion, logging, and types packages"
```

---

### Task 11: Exclude the test-only `network/testhelpers` package from coverage

**Files:**
- Move: `pkg/network/testhelpers/netns.go` → `test/testhelpers/netns.go`
- Modify: `pkg/network/integration_real_test.go:11` (import path)
- Create: `.codecov.yml`

**Interfaces:**
- Consumes: `github.com/restuhaqza/swarmcracker/pkg/network/testhelpers` (all exported helpers used by `integration_real_test.go`).
- Produces: `github.com/restuhaqza/swarmcracker/test/testhelpers` with the same exported names.

- [ ] **Step 1: Confirm the gap**

Run: `go test -cover ./pkg/network/testhelpers/`
Expected: `coverage: 0.0% of statements` — 85 statements, all test scaffolding that requires root.

- [ ] **Step 2: Move the package and update the import**

Move `netns.go` to `test/testhelpers/netns.go` unchanged except its package comment, and update the sole importer:

```bash
git mv pkg/network/testhelpers/netns.go test/testhelpers/netns.go
```

Then in `pkg/network/integration_real_test.go` replace the import path with `github.com/restuhaqza/swarmcracker/test/testhelpers`.

Also add `.codecov.yml` so Codecov ignores test scaffolding if any remains:

```yaml
ignore:
  - "test/**"
  - "pkg/**/*_test.go"
```

- [ ] **Step 3: Verify**

Run: `go vet ./test/testhelpers/ && go test -run '^$' ./pkg/network/ && go test -race -coverprofile=/tmp/cov.out ./pkg/... && go tool cover -func=/tmp/cov.out | grep testhelpers`
Expected: build/vet clean, the integration test still compiles, and no `network/testhelpers` entry appears in the profile.

- [ ] **Step 4: Commit**

```bash
git add test/testhelpers/netns.go pkg/network/integration_real_test.go .codecov.yml
git commit -m "test: move network testhelpers out of pkg so coverage measures production code"
```

---

### Task 12: Cover `pkg/config` conversion and default-config helpers

**Files:**
- Create: `pkg/config/config_coverage_test.go`
- Reference: `pkg/config/config.go:57-66`, `:219-260`

**Interfaces:**
- Consumes: `(Duration).ToDuration() time.Duration`, `(Duration).String() string`, `EnsureDefaultConfig() (bool, error)`, `(*Config).String() string`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/cfg.out ./pkg/config/ && go tool cover -func=/tmp/cfg.out | grep -E 'ToDuration|String|EnsureDefaultConfig'`
Expected: `ToDuration`, `config.Duration.String`, `EnsureDefaultConfig` at `0.0%`.

- [ ] **Step 2: Write the tests**

```go
func TestDuration_ToDuration(t *testing.T) // table: "30s","5m","1h", "" => 0, invalid => 0
func TestDuration_String(t *testing.T)     // round-trips ToDuration output
func TestEnsureDefaultConfig(t *testing.T) // with HOME/temp config dir: creates file once (created=true), second call reports already-present; file parses
func TestConfig_String(t *testing.T)       // no panic; contains a stable marker such as the kernel path
```

`EnsureDefaultConfig` resolves its path via `GetDefaultConfigPath()`, which honors the `SWARMCRACKER_CONFIG` env var. Use `t.Setenv("SWARMCRACKER_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))` so the test never touches `/etc`.

- [ ] **Step 3: Verify**

Run: `go test -race -cover ./pkg/config/`
Expected: PASS; coverage ≥ 85%.

- [ ] **Step 4: Commit**

```bash
git add pkg/config/config_coverage_test.go
git commit -m "test(config): cover duration conversion and default config creation"
```

---

### Task 13: Cover `pkg/network` teardown and VXLAN peer removal

**Files:**
- Create: `pkg/network/manager_shutdown_test.go`
- Modify: `pkg/network/vxlan_test.go` (exists)

**Interfaces:**
- Consumes: `(*NetworkManager).Shutdown() error`, `(*NetworkManager).teardownNAT() error`, `(*NetworkManager).cleanupDnsmasq() error`, `(*NetworkManager).killByPID(pid string)`, `(*VXLANManager).removePeerForwarding(vxlanName, peerIP string) error`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/net.out ./pkg/network/ && go tool cover -func=/tmp/net.out | grep -E 'Shutdown|teardownNAT|cleanupDnsmasq|killByPID|removePeerForwarding'`
Expected: each `0.0%`.

- [ ] **Step 2: Write the tests**

```go
func TestNetworkManager_Shutdown_Idempotent(t *testing.T)   // Shutdown twice returns nil; second call tolerates missing state
func TestNetworkManager_teardownNAT(t *testing.T)           // with no state => nil; with a recorded NAT rule => command attempted
func TestNetworkManager_killByPID(t *testing.T)             // invalid PID, empty PID, and a live child PID (kill it, then wait)
func TestNetworkManager_cleanupDnsmasq(t *testing.T)        // no pid file => nil
func TestVXLANManager_removePeerForwarding(t *testing.T)    // no ip/iptables available or no rules => nil, never panics
```

These functions shell out to `ip`/`iptables`/`pkill`. Use a `NetworkManager` constructed by the package's existing test helper or a zero value whose state maps are initialised, capture stdout by redirecting nothing, and assert on the no-op paths. `killByPID` has `testDefaultExecute`-style seams if present; otherwise assert it does not panic for invalid input and that a real forked child is gone afterwards.

- [ ] **Step 3: Verify**

Run: `go test -race -cover ./pkg/network/`
Expected: PASS (root not required); coverage ≥ 85%; named functions > 0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/network/manager_shutdown_test.go pkg/network/vxlan_test.go
git commit -m "test(network): cover manager teardown and VXLAN peer removal"
```

---

### Task 14: Cover `pkg/swarmkit` VMM helpers and executor error paths

**Files:**
- Create: `pkg/swarmkit/vmm_coverage_test.go`
- Modify: `pkg/swarmkit/swarmkit_exec_test.go` (extend)

**Interfaces:**
- Consumes: `toStr(v interface{}) string`; existing executor test fixtures in `pkg/swarmkit/swarmkit_exec_test.go`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/sk.out ./pkg/swarmkit/ && go tool cover -func=/tmp/sk.out | grep -E 'toStr'`
Expected: `toStr` at `0.0%`; `vmm.go` ≈ 82%, `executor.go` ≈ 88%.

- [ ] **Step 2: Write the tests**

```go
func TestToStr(t *testing.T) // table: nil => "null"/"", string, int, error, struct; never panics
```

Then add executor error-path subtests to the existing file for the uncovered branches flagged by `go tool cover -html=/tmp/sk.out` (focus on `vmm.go` and `executor.go` blocks at 0). Each added subtest must be `testing.Short()`-guarded if it starts a process, following the file's existing convention.

- [ ] **Step 3: Verify**

Run: `go test -short -race -cover ./pkg/swarmkit/`
Expected: PASS; `toStr` > 0%; `pkg/swarmkit` ≥ 87%.

- [ ] **Step 4: Commit**

```bash
git add pkg/swarmkit/vmm_coverage_test.go pkg/swarmkit/swarmkit_exec_test.go
git commit -m "test(swarmkit): cover toStr and VMM/executor error paths"
```

---

### Task 15: Cover `pkg/image` detector and `pkg/storage` directory driver

**Files:**
- Modify: `pkg/image/detector_test.go` (exists)
- Modify: `pkg/storage/volume_dir_test.go`

**Interfaces:**
- Consumes: `hasBusyboxBinary(tmpDir string) bool`; `(*DirectoryDriver).Stat(ctx, name)`, `.Capacity(ctx, name)`, `.Snapshot(ctx, name)`, `.Restore(ctx, name, snap)`, `.Export(ctx, name, w)`, `.Import(ctx, name, r, sizeMB)`; `validateTarPath(dest, name string) error`; `stripSetuidSetgid(mode int64) int64`; `ensureAbsolutePath(target string) string`.
- Produces: nothing other tasks use.

- [ ] **Step 1: Confirm the gap**

Run: `go test -coverprofile=/tmp/is.out ./pkg/image/ ./pkg/storage/ && go tool cover -func=/tmp/is.out | grep -E 'hasBusyboxBinary|validateTarPath|stripSetuidSetgid|ensureAbsolutePath'`
Expected: each `0.0%`; `volume_dir.go` ≈ 82%.

- [ ] **Step 2: Write the tests**

```go
func TestHasBusyboxBinary(t *testing.T) // empty dir => false; dir containing an executable "busybox" => true
func TestValidateTarPath(t *testing.T)  // table: "a/b" ok; "../escape", "/abs", "a/../../b" => error
func TestStripSetuidSetgid(t *testing.T) // clears 0o4000/0o2000, preserves permission bits
func TestEnsureAbsolutePath(t *testing.T) // already-absolute unchanged; relative => absolute
func TestDirectoryDriver_StatCapacity(t *testing.T) // create volume via Create(); Stat reports size; Capacity non-negative
func TestDirectoryDriver_SnapshotRestore(t *testing.T) // snapshot then restore round-trips file contents
func TestDirectoryDriver_ExportImport(t *testing.T)    // export to buffer; import into a second driver; contents match
```

- [ ] **Step 3: Verify**

Run: `go test -race -cover ./pkg/image/ ./pkg/storage/`
Expected: PASS; both packages ≥ 85%; named functions > 0%.

- [ ] **Step 4: Commit**

```bash
git add pkg/image/detector_test.go pkg/storage/volume_dir_test.go
git commit -m "test: cover image detector and storage directory driver"
```

---

### Task 16: Update the testing docs and add a coverage gate

**Files:**
- Modify: `docs/dev/testing/unit-tests.md` ("Current State" table and "Coverage Targets")
- Modify: `docs/planning/README.md` if it indexes current status
- Modify: `.github/workflows/ci.yml` (add threshold check)

**Interfaces:**
- Consumes: the final measured coverage from Tasks 1-15.
- Produces: nothing other tasks use.

- [ ] **Step 1: Re-measure**

Run: `go test -short -race -coverprofile=coverage.out -covermode=atomic ./pkg/... && go tool cover -func=coverage.out | tail -1`
Expected: `total: ... ≥ 85.0%`.

- [ ] **Step 2: Update the docs**

Replace the stale "Current State" table in `docs/dev/testing/unit-tests.md` with the measured per-package numbers, mark the 85% target as met, and correct the "Coverage Targets" section. Remove the `security` row (no such package exists today) and add `cni`, `console`, `health`, `apiversion`, `logging`, `types`.

- [ ] **Step 3: Add a failing-threshold gate to CI**

After the coverage-generating step, add:

```yaml
      - name: Check coverage threshold
        run: |
          PCT=$(go tool cover -func=coverage.out | tail -1 | awk '{print $3}' | tr -d '%')
          echo "coverage: $PCT%"
          awk -v p="$PCT" 'BEGIN { exit (p+0 < 85) }' || { echo "coverage $PCT% is below 85%"; exit 1; }
```

Threshold starts at 85; lower it only with an explicit decision recorded in `unit-tests.md`.

- [ ] **Step 4: Verify the gate locally**

Run: `go test -short -race -coverprofile=coverage.out -covermode=atomic ./pkg/... && PCT=$(go tool cover -func=coverage.out | tail -1 | awk '{print $3}' | tr -d '%') && awk -v p="$PCT" 'BEGIN { exit (p+0 < 85) }' && echo OK`
Expected: `OK`.

- [ ] **Step 5: Commit**

```bash
git add docs/dev/testing/unit-tests.md .github/workflows/ci.yml
git commit -m "docs+ci: refresh coverage baseline and enforce 85% threshold"
```

---

## Self-Review

**1. Spec coverage.** The requirement is the 85% overall target in `AGENTS.md` and `docs/dev/testing/unit-tests.md`. Task mapping: biggest lever `pkg/cni` → Tasks 6-9; zero-coverage packages → Task 10; test-only scaffolding inflation → Task 11; `pkg/config` → Task 12; `pkg/network` → Task 13; `pkg/swarmkit` → Task 14; `pkg/image` + `pkg/storage` → Task 15; target enforcement + doc refresh → Task 16. The suite-correctness prerequisite (failing tests, hidden bugs, unrun `cmd` tests) → Tasks 1-5. Projected: Tasks 6-9 cover roughly two-thirds of `pkg/cni`'s 507 uncovered statements, Task 11 removes 85 statements from the denominator, and Task 10 adds ~32; arithmetically this projects to ~85–86% before Tasks 12-15. The margin is thin, so Tasks 12-15 are the safety buffer, and Task 16's gate is what actually holds the line.

**2. Step scan.** Every step names a command with expected output or an exact test name with its assertions. No "handle edge cases" or "add appropriate tests" phrasing. The one open implementation detail — `EnsureDefaultConfig`'s path resolution in Task 12 — is called out as a step instruction to inspect it first, not left ambiguous about the deliverable.

**3. Type consistency.** `newTestAllocator(t *testing.T) *CNINetworkAllocator` is defined in Task 6 and consumed in 7-8 under that exact name. `absentPath(t *testing.T) string` is used consistently in Task 2. `setupCNIProvider(t)` is the existing helper in `cni_coverage_test.go`, reused rather than redefined. Interface signature strings in each Consumes block match the grep output from the repo.

**4. Review Focus.** (1) path-existence assumptions → Task 2; (2) argv-less command contract → Task 1; (3) detach with buffered bytes → Task 3; (4) state after forced kill → Task 5; (5) IPAM reserved/range boundaries → Task 8.

**5. Proportion.** This plan is longer than the one-page requirement because it spans 16 independently shippable tasks; it carries test names, assertions, and commands, not implementation bodies. No task transcribes production code except the two one-line bug fixes and the `absentPath` helper.
