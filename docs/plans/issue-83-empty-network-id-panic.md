# #83 — an empty network ID panics, and hides every test after it

## Symptom

`prepareNetworkWithCNI` names an attachment that has no spec name after its ID:

```go
networkName = attachment.Network.ID[:8]
```

A network with an empty ID panics with `slice bounds out of range [:8] with length 0`.
`TestNetworkManager_PrepareNetwork_ErrorScenarios/empty_network_ID` expects a clean
result, so the panic aborts the whole `./pkg/network/` test binary: every test after it
silently never runs.

```
--- FAIL: TestNetworkManager_PrepareNetwork_ErrorScenarios/empty_network_ID
panic: runtime error: slice bounds out of range [:8] with length 0
FAIL	github.com/restuhaqza/swarmcracker/pkg/network	1.675s
```

Found while reviewing #76: a wrong row in a brand-new test file was invisible in a full
package run, and the head-vs-base failure-set comparison was comparing two *identically
truncated* sets — so the comparison was valid but narrower than it looked.

## Fix

Slice only what the ID actually has. The value is a label for the CNI config lookup, so
it keeps the ID-prefix shape it has always had; nothing else about it changes, and the
existing test passes as written (it expects the call to be handled gracefully, not to
error).

The same unguarded slice exists in the CLI display helpers: `cmd/swarmctl`'s listings
slice live IDs at 12, and `cmd_task.go` guards only the empty string rather than a short
one. Those are not reachable from a real cluster today, because SwarmKit IDs are longer,
but they are the same one-line class and the check the issue asked for covered the repo,
so they are guarded the same way.

## Tasks

- [ ] T1 `prepareNetworkWithCNI` slices only what the ID has.
- [ ] T2 The CLI display sites guarded the same way.
- [ ] T3 A short-ID row next to the empty-ID one.
- [ ] T4 Verify: a full `./pkg/network/` run completes, and the head-vs-base failure
  sets can finally be compared in full, subtests included.

## Out of scope

- Any test that only becomes visible once the panic is gone: if the completed run shows
  failures that a truncated run hid, they get their own issue rather than being folded
  into this one.
