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
  `-short`: zero failures in all four on the lab.
- **A clean host is the real test**, and the lab is not one: it carries the
  leftovers (which is exactly how the original failures became invisible). The
  suite is run in a throwaway privileged container built from `golang:1.26` plus
  `iproute2`, `iptables`, `procps` and `kmod` — no leftover devices, and the
  tooling the tests shell out to — as root and as uid 65534. Both PASS. Without
  the tooling the container is not comparable (every test that reaches the
  platform fails to exec `ip`), which is worth knowing before trusting such a
  run.
- `verify.sh <branch> main`: head-only empty, base-only = the tests this fixes.
- CI, `gofmt`, `vet`, `golangci-lint`.

## Review round

An independent review reproduced the environment properly (clean privileged
container, both users) and found that the branch did **not** yet meet its own
acceptance: a clean root host still failed.

- **BLOCKER** `TestDefaultNetlinkExecutor_LinkAdd_Fail` created `test-br-xyz` and
  asserted the creation failed. As root it succeeds, the assertion fails, and the
  device is left behind — which is why the *next* root run passed and the lab
  looked green. Fixed with a name longer than IFNAMSIZ (rejected for every user,
  leaves nothing behind), plus an assertion that the name still does not exist.
- The same dead-`NeighAdd` shape existed in
  `TestVXLANManager_AddPeerForwarding_Errors/neigh add fails` (passes or fails
  depending on a leftover `test-br-vxlan`), and
  `.../peer forwarding fails for some peers` kept writing real FDB entries: both
  now use the fake `bridge`, the latter with a per-peer script, and it asserts
  which peers were recorded instead of only "no error".
- `TestNetworkManager_CreateTapDevice_FullCreation` still asserted the retired
  `tap-eth` name and skipped every assertion whenever creation failed — both
  host-dependent and unable to catch a regression. It stubs the platform, takes
  the subtest's `*testing.T` in its validators and asserts the naming contract.
- `TestNetworkManager_CreateTapDevice_Comprehensive` never used its
  `expectError` field and guarded every check behind `err == nil`, so it passed
  with `createTapDevice` broken. Success is asserted explicitly now.
- The first version of the CNI-path fix widened the accepted error to any of
  three platform stages, which is close to accepting anything. With the bridge
  stubbed the call deterministically reaches the CNI stage, so the assertion now
  names that stage.
- The positive cases added for the mock-injected tests only asserted `err == nil`,
  which a regression that skips `LinkAdd`/`RouteAdd` also produces. They assert
  the link and the route handed to the kernel instead.

The review also confirmed what is *not* wrong: no test in the package calls
`t.Parallel()`, so swapping the package-level `execCommand` and using `t.Setenv`
are not races and cannot leak across tests.

## Acceptance

`go test ./pkg/network/` passes for any user on any host — verified on the lab and
in a **clean** privileged container as root and as an unprivileged uid — the tests
this issue covers no longer create real devices, routes or firewall rules, and
their assertions describe what the code does today.

## Notes

- No production code changes.
- **Residual pollution, measured and deliberately not fixed here.** Tests that
  inject only `LinkByName` still let the real `bridge fdb append` run, and it adds
  an entry whenever a leftover `br0-vxlan` happens to exist: a full root run of the
  suite took `bridge fdb show dev br0-vxlan` from 1 entry to 4. Those tests do not
  assert on the result, so they do not fail, and they are out of this issue's
  reach (they would each need the `execCommand` seam that `addPeerForwarding` does
  not yet use, or a per-test stub). The integration tests in
  `integration_real_test.go` also create real bridges and TAPs on purpose; they are
  root-gated, skipped in short mode, and clean up after themselves.
- The test server carries real devices left by earlier root runs (`test-br0`,
  `br0-vxlan`, `test-br-multi-*`, a pile of `tap-*`, ...). Cleaning it is
  worthwhile host maintenance, but it is not the fix — the suite is what creates
  them, which is why the tests had to stop depending on that state.
