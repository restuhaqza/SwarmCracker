# Plan — #84: the four `pkg/network` test failures the #83 panic was hiding

Branch: `test/network-env-independent`
Issue: [#84](https://github.com/restuhaqza/SwarmCracker/issues/84)

## Problem

Fixing #83 let the `./pkg/network/` test binary run to the end, and four
pre-existing failures appeared. They are not caused by #83; they are tests whose
outcome depends on the machine they run on, not on the code.

## Diagnosis (measured on the test server, `main` = `cef15e3`)

### Pair 1 — `TestVXLANManager_UpdatePeers_AddPeerFails`,
`TestVXLANManager_AddPeerForwarding_NeighAddError`

Both inject a failure through `MockNetlinkExecutor.NeighAddFunc`. But
`addPeerForwarding` no longer uses netlink `NeighAdd` — it shells out to
`bridge fdb append` (vxlan.go:264 says exactly that, to get correct VXLAN FDB
semantics). **The mock is dead: nothing under test ever calls `NeighAdd`.**

So the tests only pass while the real `bridge fdb append` fails. On the test
server it does not fail as root, because a **stale `br0-vxlan` device is left on
the host** (line 1968) and it already carries the FDB entry
`00:00:00:00:00:00 dst 10.0.0.2 self permanent` — created by an earlier root run
of these very tests:

```
# CMD [bridge fdb show dev br0-vxlan]
00:00:00:00:00:00 dst 10.0.0.2 self permanent
```

`bridge fdb append` on an existing entry fails with output containing "exists",
which `addPeerForwarding` deliberately tolerates (vxlan.go:281-285) → it reports
success → the peer is added → both assertions fail. As an unprivileged user the
same command fails with a permission error (no "exists") → success is not
reported → both tests pass. Hence "FAIL as root, pass as `nobody`", and the test
poisons its own environment for the next run.

### Pair 2 — `.../ErrorScenarios/network_with_IPv6_addresses`,
`.../ErrorScenarios/multiple_addresses_for_single_network`

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

## Fix

### T1 — the two VXLAN tests: make the injected failure real

`pkg/network/vxlan_test.go` already solved this pattern for `removePeerForwarding`
("missing bridge executable does not panic", "stale FDB entry ... is tolerated"):
point `PATH` at a temp dir holding a fake `bridge` script, so the command fails
deterministically and the host is never touched. Reuse it instead of inventing a
new seam:

- `TestVXLANManager_AddPeerForwarding_NeighAddError` → rename to
  `TestVXLANManager_AddPeerForwarding_FDBAddError` (the name referenced a call the
  code no longer makes), fake `bridge` fails with a non-"exists" message → assert
  `failed to add FDB entry`.
- `TestVXLANManager_UpdatePeers_AddPeerFails` → same fake → assert the peer is
  **not** in the store and that `UpdatePeers` still returns no error (it logs and
  continues by design).
- New: a fake `bridge` reporting `File exists` → tolerated → the peer **is**
  added. That branch is what made these tests flake, so it gets its own case.
- Drop the now-dead `NeighAddFunc` from both fixtures.

No production code changes: the seam the tests need already exists.

### T2 — the two CNI-path subtests: assert the stage, not one error string

The fixtures are about ID/address handling (the area #83 fixed), not about which
platform call fails. Their stated intent is "the logic should still execute". So
allow the three platform stages that `PrepareNetwork` can actually reach, and say
why in a comment:

`failed to ensure bridge` (no privilege / no bridge) · `failed to create TAP
device` (no privilege) · `CNI ADD failed` (CNI plugin absent, stale bridge
present).

Keep tolerating success (a machine that can actually do this must not fail the
test).

### T3 — document the convention

`docs-site/src/content/docs/contributing/testing/unit-tests.md`: a short note that
`pkg/network` unit tests must not depend on the host's privileges or network
state — inject a fake `bridge` via `PATH` (or a mock executor), and remember the
suite is run as root on some machines. This is the failure mode #84 was.

### T4 — verification

- `go test ./pkg/network/` **as root and as `nobody`**: zero failures, both.
- `verify.sh <branch> main` on the test server: head-only empty, base-only = these
  four.
- CI (authoritative), plus `gofmt`/`vet`/`lint`.
- Lab: the four tests pass in place; then restore the lab to `main`.

## Acceptance

The four tests pass for every user on any host, without depending on leftover
devices or on being unprivileged, and the suite stops mutating the host's FDB for
these cases.

## Out of scope (found while diagnosing)

`pkg/network` tests that build a real `NetworkManager` or a real
`VXLANManager` still create **real** bridges, VXLAN devices, TAPs and FDB entries
when run as root (`test-br0`, `br0-vxlan`, `test-br-multi-*`, a pile of `tap-*`
are all left on the test server), which is what made these tests fail in the first
place. Cleaning that up is a larger, separate change; this issue makes the four
named failures independent of it and records the finding.
