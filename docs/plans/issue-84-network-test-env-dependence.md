# Plan — #84: the `pkg/network` test failures the truncations were hiding

Branch: `test/network-env-independent`
Issue: [#84](https://github.com/restuhaqza/SwarmCracker/issues/84)

## Problem

Fixing #83 let the `./pkg/network/` test binary run past the panic that had been
hiding failures, and four tests appeared. They are pre-existing and
environment-dependent: tests that assert a privileged operation fails, which
stops being true when the suite runs as root.

## Diagnosis (measured on the test server, `main` = `cef15e3`)

The issue lists four tests. Measuring the whole package showed the number is
larger, and why: **a second truncation**.
`TestVXLANManager_AddPeerForwarding_NeighAddError` ends with
`assert.Contains(t, err.Error(), ...)` where `err` is nil once its mock is dead,
so the failing assertion is followed by a nil-pointer dereference **inside the
test**:

```
panic: runtime error: invalid memory address or nil pointer dereference
	github.com/restuhaqza/swarmcracker/pkg/network.TestVXLANManager_AddPeerForwarding_NeighAddError(...)
		manager_coverage_v2_test.go:685
```

That aborts the test binary, so `main` reports 15 failures and never runs the rest
of the suite. Behind it are 16 more (four more top-level tests), the same kind.
Ten more (six top-level tests) fail on **both** sides, so the head-vs-base
comparison never flagged them either.

Three groups, all root-only — every one of them passes as `nobody`:

| Group | Tests | Why it fails as root |
|-------|-------|----------------------|
| The issue's four | `UpdatePeers_AddPeerFails`, `AddPeerForwarding_NeighAddError`, `PrepareNetwork_ErrorScenarios/{IPv6,multiple_addresses}` | dead `NeighAdd` mock; assertion allows only two of the three platform stages |
| Behind the second panic (16 names, 4 tests) | `SetupVXLAN`, `createVXLANInterface`, `AddRouteToSubnet`, `SetupNAT_DuplicateSetup` | real executor plus a leftover `br0-vxlan`/`br0` on the host; real iptables succeeding |
| Visible on both sides (10 names, 6 tests) | `CreateTAPDevice_Wrapper_Fails`, `Integration_NetworkManager_SetupNAT`, `CreateTapDevice_Comprehensive`, `createTapDevice_InterfaceName`, `PrepareNetwork_{Empty,Nil}Networks` | real TAP/IP/NAT calls succeeding; stale expectations (`tap-eth<index>`, unguarded masquerade rule, "0 TAP devices") |

### The issue's pair 1 — the dead `NeighAdd` mock

`TestVXLANManager_UpdatePeers_AddPeerFails` and
`TestVXLANManager_AddPeerForwarding_NeighAddError` inject a failure through
`MockNetlinkExecutor.NeighAddFunc`. But `addPeerForwarding` no longer uses netlink
`NeighAdd` — it shells out to `bridge fdb append` (vxlan.go:264 says exactly that,
for correct VXLAN FDB semantics). **The mock is dead: nothing under test ever
calls `NeighAdd`.**

So the tests only pass while the real `bridge fdb append` fails. On the test server
it does not fail as root, because a **stale `br0-vxlan` device is left on the
host** (link 1968) and it already carries the FDB entry an earlier root run of
these very tests created:

```
# CMD [bridge fdb show dev br0-vxlan]
00:00:00:00:00:00 dst 10.0.0.2 self permanent
```

`bridge fdb append` on an existing entry fails with output containing "exists",
which `addPeerForwarding` deliberately tolerates (vxlan.go:281-285) → it reports
success → the peer is added → both assertions fail. Unprivileged, the same command
fails with a permission error (no "exists") → success is not reported → both pass.
Hence "FAIL as root, pass as `nobody`", and the test poisons its own environment
for the next run.

### The issue's pair 2 — the assertion allows two of three stages

Both fixtures carry addresses, so they take the CNI path. The assertion allows
only two platform errors, assuming `ensureBridge` fails without root. It does not
fail on the test server either, because a **stale `test-br0` bridge is left
there**:

```
CASE ipv6: ERR=CNI ADD failed for network network-: CNI ADD failed: fork/exec /opt/cni/bin/swarmcracker-cni: no such file or directory
```

That is the third platform stage (the CNI plugin is not installed), which the
assertion rejects. It fails for every user once `test-br0` exists, so it is not a
privilege effect — matching the issue.

### Behind the panic

- `SetupVXLAN` / `createVXLANInterface`: `createVXLANInterface` first asks whether
  the named VXLAN already exists. It does (`br0-vxlan`, matching id/port), so the
  function logs "reusing", brings it up and returns **success before validating
  the local IP at all** — every "invalid local IP" case expects an error and gets
  nil.
- `AddRouteToSubnet`: `LinkByName("br0")` succeeds on the lab (leftover `br0`), so
  the route is really added as root and the call returns nil where the test
  expects "bridge doesn't exist".
- `SetupNAT_DuplicateSetup`: asserts `setupNAT` fails twice — it documents "no
  privileges" rather than behaviour; as root it really installs the rules.
- `PrepareNetwork_{Empty,Nil}Networks`: asserts zero TAP devices, but
  `PrepareNetwork` with no attachment is *supposed* to create a default TAP; it
  only stayed at zero while creating the bridge failed.
- `createTapDevice_InterfaceName` / `CreateTapDevice_Comprehensive`: assert the
  long-gone `tap-eth<index>` naming; the code produces `tap-<hash8>-<index>`.
- `Integration_NetworkManager_SetupNAT`: checks for the **unguarded** masquerade
  rule that #70 replaced with a guarded one; CI skips it (`RequireRoot`), so nobody
  noticed. Its cleanup deleted a rule that was not there, leaving the real one
  behind.
- `CreateTAPDevice_Wrapper_Fails`: "no root → tuntap add fails" is false as root;
  the TAP gets created and the *bridge attach* fails, so the expected message never
  matches — and the device is left behind.

## Fix (tests only; no production change)

### T1 — the dead mock becomes a real injected failure

`vxlan_test.go` already solved this for `removePeerForwarding` ("missing bridge
executable does not panic", "stale FDB entry ... is tolerated"): point `PATH` at a
temp dir holding a fake `bridge`, so the command fails deterministically and the
host is never touched. Reuse it:

- Extract `fakeBridgeOnPath(t, output, code)` and use it from the refactored
  `removePeerForwarding` subtest too.
- `TestVXLANManager_AddPeerForwarding_NeighAddError` → rename to
  `..._FDBAddError` (the old name referenced a call the code no longer makes), fake
  `bridge` fails with a non-"exists" message → assert `failed to add FDB entry`.
  This is also what removes the panic: `err` is no longer nil.
- `TestVXLANManager_UpdatePeers_AddPeerFails` → same fake → assert the peer is
  **not** recorded and `UpdatePeers` still returns no error (it logs and
  continues by design).
- New `TestVXLANManager_UpdatePeers_ExistingFDBEntryIsTolerated`: `File exists` →
  tolerated → the peer **is** recorded. That branch is what made these flake.
- Drop the dead `NeighAddFunc` from both fixtures.

### T2 — assert the platform stage, not one error string

The CNI fixtures are about ID/address handling (the area #83 fixed), not about
which platform call fails; their stated intent is "the logic should still
execute". One `isPlatformStageError` helper accepting the three stages
`PrepareNetwork` can reach — `failed to ensure bridge`, `failed to create TAP
device`, `CNI ADD failed` — while still tolerating success.

### T3 — the sixteen behind the second panic

- `SetupVXLAN` / `createVXLANInterface` / `AddRouteToSubnet`: inject
  `MockNetlinkExecutor` (`missingLinksExecutor`, `LinkByName` reports the device
  absent) so each case reaches the validation it is about; add the positive cases
  the real-executor runs were accidentally covering (`LinkAdd` fails; bridge
  present → route added). The route no longer leaks into the host table.
- `SetupNAT_DuplicateSetup`: use the existing `stubExec(t)` recording stub, assert
  both calls succeed, and assert each call checks the *guarded* masquerade rule
  before adding it — the idempotency the test claims to cover.

### T4 — the ten that fail on both sides

- `PrepareNetwork_{Empty,Nil}Networks`: `stubExec` + assert exactly one default TAP
  on the configured bridge (the documented behaviour).
- `createTapDevice_InterfaceName` / `CreateTapDevice_Comprehensive`: `stubExec` +
  `assertTapName` helper asserting `^tap-[0-9a-f]{8}-<index>$`, plus stability for
  the same task+index.
- `CreateTAPDevice_Wrapper_Fails`: fail on a name longer than IFNAMSIZ, which the
  kernel rejects for every user, so nothing is created.
- `Integration_NetworkManager_SetupNAT`: build the expectation *and* the cleanup
  from `masqueradeSpec(subnet, "", "", true)` — the helper production uses — so it
  checks the guarded rule and removes it.

### T5 — document the convention

`docs-site/.../contributing/testing/unit-tests.md`, new "Host-Independent Tests"
section: inject the seam (`stubExec`, `MockNetlinkExecutor`,
`CreateTAPDeviceWithExecutor`, or a fake binary on `PATH`), never create real
devices/routes/rules from a test, and remember the suite runs as root on some hosts
and unprivileged in CI.

### T6 — verification

- `go test ./pkg/network/` as **root** and as **`nobody`**, plus `-race` and
  `-short`: zero failures in all four. Done: all four green.
- `verify.sh <branch> main`: head-only empty, base-only = the tests this fixes.
- CI, `gofmt`, `vet`, `golangci-lint`.
- The lab is already on `main`, so the lab check *is* running the package there as
  root and as `nobody`; nothing to deploy for a tests-and-docs branch.

## Acceptance

`go test ./pkg/network/` passes for any user on any host, no test creates real
devices, routes or firewall rules (so the suite no longer depends on its own
history), and the assertions describe what the code does today.

## Notes

- No production code changes.
- The test server still carries real devices left by earlier root runs
  (`test-br0`, `br0-vxlan`, `test-br-multi-*`, a pile of `tap-*`, ...). Cleaning
  it is worthwhile host maintenance, but it is not the fix — the suite is what
  creates them, which is why the tests had to stop depending on that state.
