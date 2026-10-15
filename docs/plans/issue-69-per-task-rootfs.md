# Plan — #69: a task must never share the image cache read-write

Branch: `fix/per-task-rootfs`
Issue: [#69](https://github.com/restuhaqza/SwarmCracker/issues/69)

## Problem

A task with no mounts, secrets or configs boots **directly from the shared image
cache** (`<rootfs_dir>/<image>.ext4`) with a writable drive. Every write the
guest makes — a workload's, or `vm exec`'s — lands in the cache and is visible to
every later task and service on that node using the same image.

The code states the opposite invariant (`pkg/image/preparer.go`): "the shared
cache is used directly as the VM drive, so it must never be modified" — but the
private copy is conditional:

```go
needsPrivateRootfs := len(container.Mounts) > 0 || len(container.Secrets) > 0 || len(container.Configs) > 0
```

Observed on the 2-node lab: `vm exec <task> -- touch /tmp/break` persisted into
the cache, every later `alpine` task on that node failed its gate immediately,
and clearing the cache file was the only way back.

## What the recon established

| Fact | Evidence |
|------|----------|
| The private copy already exists and is cheap to make: `cp --reflink=auto --sparse=always` | `pkg/image/preparer.go:878-891` |
| The repo already decided this for golden images — "Replicas must not share a read/write rootfs" | `pkg/swarmkit/executor.go:748-760` (`applyGoldenImage` materializes a per-task copy) |
| A per-task copy is cleaned up on teardown when it is marked ephemeral | `pkg/swarmkit/executor.go:1167` reads `rootfs_ephemeral`; `pkg/swarmkit/remove_rootfs_test.go` |
| The Firecracker drive is hardcoded writable (`is_read_only: false`) | `pkg/swarmkit/vmm.go:434`, `pkg/swarmkit/translator.go:102` |
| Measured cost on the lab (ext4, **no** reflink): a 10 GiB-apparent image is 92 MiB of real data and copies in **0.355 s** | `cp --reflink=auto --sparse=always` on `ubuntu-24.04.ext4` |
| The daemon's orphan sweep removes sockets and metadata for orphaned VMs but **not** rootfs copies | `pkg/swarmkit/executor.go:407-458` |
| The CLI executor's `Remove` deletes no rootfs at all | `pkg/executor/executor.go:240-265` |
| The CLI's start rollback deletes `Annotations["rootfs"]` on a later failure — today that is the **shared cache** for a plain task | `pkg/executor/executor.go:167-175` |

## Design

Make the OCI path match the golden path: **every task gets its own writable copy**
of the image rootfs.

- `pkg/image/preparer.go`: drop the `needsPrivateRootfs` condition. Always copy to
  `tasks/<task-id>.ext4` — a directory of its own, because task IDs and image IDs
  would otherwise share one namespace and the CLI lets a VM be named after an image
  (`vm create --name alpine-latest alpine`), which would have let a task boot and
  then delete the cache. Always mark `rootfs_ephemeral=true`, and keep applying
  mounts/secrets/configs to the copy. The base stays the cache and is never handed
  to a guest. The copy is staged in a unique temp file and renamed into place, so a
  concurrent prepare (or a retry while a running VM holds the old file open) never
  sees a missing or half-written rootfs.
- `pkg/swarmkit/executor.go`: the orphan sweep also removes the orphaned task's
  copy, so a daemon crash (or a task killed while the daemon was down) does not
  leak disk now that every task has one. Golden images get their copy in the same
  directory and are marked ephemeral too, so teardown deletes the copy and never
  the artifact.
- `pkg/executor/executor.go` + `cmd/swarmcracker`: the CLI's teardown frees the
  copy — `vm stop` through `cleanupVM`, which reads the new `VMState.RootfsEphemeral`
  — and `FirecrackerExecutor.Remove` does the same for programmatic callers. The
  CLI's VM ID is also unique now (it used to be per-second, which made two VMs
  share one copy).
- No configuration knob: a knob that re-enables sharing is a knob that re-enables
  the leak, and the golden path already hard-codes the correct behaviour.

### Cost, and why it is acceptable

The copy is sparse and reflink-accelerated where the filesystem supports it; on
the lab's ext4 it is a 0.355 s, 92 MiB copy for a 10 GiB-apparent image. The
trade-off is per-task disk equal to the image's real data instead of zero, and
that cost is already paid by every mount-bearing task and every golden-image task
today. A zero-copy future (read-only base + a writable overlay or tmpfs for the
writable paths, which is what containers do) is a separate change: it needs the
guest init wrapper to assemble the overlay and a guest kernel with overlayfs, and
it is recorded as a follow-up rather than smuggled in here.

## Explicitly out of scope

- **Read-only base + overlay/tmpfs.** The real zero-copy answer; needs guest-side
  work and a kernel profile check.
- **The CLI's golden path.** `pkg/executor` still hands a prebuilt golden artifact
  to the VM directly ("shared between tasks"). The daemon path already copies.
  Filed as part of the same follow-up if it matters.

## Tasks

| # | Task | Files | Verify |
|---|------|-------|--------|
| T1 | Always per-task copy + ephemeral marker; comment states the invariant | `pkg/image/preparer.go` | `go test ./pkg/image/` |
| T2 | Orphan sweep removes the orphaned task's copy | `pkg/swarmkit/executor.go` | `go test ./pkg/swarmkit/` |
| T3 | CLI teardown frees the ephemeral copy: `VMState.RootfsEphemeral` + `cleanupVM` (`vm stop`), and `FirecrackerExecutor.Remove`; unique CLI VM IDs | `pkg/runtime/state.go`, `cmd/swarmcracker/cmd_vm.go`, `cmd_stop.go`, `pkg/executor/executor.go`, `cmd/swarmcracker/helpers.go` | `go test ./pkg/executor/ ./cmd/swarmcracker/` |
| T4 | Update tests that pinned the shared-cache behaviour, and add the invariant test | `pkg/image/*_test.go` | `go test ./pkg/image/` |
| T5 | Docs: the cache is a template, never a drive; the copy layout and the cleanup caveat | `guides/configuration.md`, `guides/operations.md`, `reference/cli.md` | `npm run verify` |
| T6 | Independent whole-branch review at the frozen tip | — | — |
| T7 | Lab e2e (2 nodes) | — | see below |

## Verification

- Unit: a plain task (no mounts/secrets/configs) resolves its rootfs to the
  per-task copy, not the cache, and is marked ephemeral; the cache file is not
  written; the orphan sweep removes an orphaned copy but never a cache file; the
  CLI `Remove` deletes the copy.
- Lab e2e: `vm exec <task> -- touch /tmp/break` in one task, then boot a second
  task from the same image on the same node and prove the marker is **absent**;
  prove the cache file's size/mtime did not change; two replicas of a service with
  no mounts cannot see each other's writes; per-task copies are removed on
  teardown; disk does not grow without bound.
