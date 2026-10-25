---
title: "Snapshots Guide"
description: "Save and restore VM state — crash recovery, fast boot, debugging."
---

> Save and restore VM state — crash recovery, fast boot, debugging.

---

## Overview

SwarmCracker can snapshot a running Firecracker microVM and restore it later.
A snapshot is a **self-contained restore point**: guest memory plus CPU/device
state (Firecracker `snapshot_type: Full`) **and** a copy of the task's disk.

`create` pauses the VM, asks Firecracker to write state and memory, copies the
task disk into the snapshot directory, and only then resumes the VM. The pause
matters: pausing for the whole sequence is what makes the memory image and the
disk copy describe the same instant, so a restore cannot replay stale
page-cache data from the memory image over newer disk state.

This requires **Firecracker v1.14.0+** — the version installed by
`swarmcracker setup install`.

---

## Use Cases

| Use Case | Benefit |
|----------|---------|
| **Crash recovery** | Restore a VM to a known-good state |
| **Fast boot** | Resume from a snapshot faster than a cold boot |
| **Debugging** | Capture exact VM state at a point in time |
| **Pre-update safety** | Roll back a workload after a bad update |

---

## CLI Commands

Snapshots live under `swarmcracker vm snapshot`:

```bash
# Create a snapshot of a running VM (task)
swarmcracker vm snapshot create <task-id>

# With metadata (all optional)
swarmcracker vm snapshot create <task-id> \
  --service <service-id> \
  --node <node-id> \
  --vcpus 2 \
  --memory 512

# List snapshots (optionally filtered)
swarmcracker vm snapshot list
swarmcracker vm snapshot list --task <task-id>
swarmcracker vm snapshot list --service <service-id>
swarmcracker vm snapshot list --node <node-id>

# Restore a VM from a snapshot
swarmcracker vm snapshot restore <snapshot-id>

# Delete a snapshot
swarmcracker vm snapshot delete <snapshot-id>

# Remove snapshots older than a duration
swarmcracker vm snapshot cleanup --max-age 168h
```

`create` determines the Firecracker API socket from `--socket` (default:
`<socket-dir>/<task-id>.sock`). `restore` can set a new socket with `--socket`.

`create` copies the task's disk, so it needs to know where that disk is. The
executor keeps one disk per task at `<rootfs-dir>/tasks/<task-id>.ext4`, and the
CLI derives the task ID from the socket's file name, so no flag is needed for a
normal task. Pass `--rootfs` when the VM's disk is somewhere else (for example a
VM started by hand with `vm create --rootfs`): a file that does not exist is
never recorded, and the snapshot then behaves as it did before disk copies
existed — a restore needs the original disk in place.

---

## HTTP API (experimental)

The manager exposes the same snapshots through API v1, but the surface is
**experimental and disabled by default**. Start `swarmd-firecracker` with
`--snapshots-enable` on the node (and keep its broker enabled) to turn it on.
While it is off, every snapshot route answers `501 not_implemented` with
`details.capability: "snapshots"`; `/v1/version` does not advertise the
capability, so a client can feature-detect it.

| Method | Route | Purpose |
|--------|-------|---------|
| `POST` | `/v1/tasks/{id}/snapshots` | create a snapshot of the running task |
| `GET` | `/v1/tasks/{id}/snapshots` | list the task's snapshots |
| `POST` | `/v1/tasks/{id}/snapshots/{sid}/restore` | restore one snapshot |
| `DELETE` | `/v1/tasks/{id}/snapshots/{sid}` | delete one snapshot |
| `GET` | `/v1/snapshots?task={id}` | list (flat address) |
| `POST` | `/v1/snapshots/{sid}/restore?task={id}` | restore (flat address) |
| `DELETE` | `/v1/snapshots/{sid}?task={id}` | delete (flat address) |

All seven routes require `tasks:snapshots` (a `runner` or `admin` token). The
task-scoped forms are the primary address. The flat forms need `?task=` because a
snapshot lives on the node that ran the task, and a snapshot id alone does not
say which node to ask; a flat restore or delete without `task` is `400 invalid`.

```bash
# Create, list, restore and delete through the task-scoped routes
curl -sS -X POST \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/vnd.swarmcracker.v1+json' \
  "$API/v1/tasks/$TASK/snapshots" -d '{"vcpus": 1, "memoryMB": 256}'

curl -sS -H "Authorization: Bearer $TOKEN" "$API/v1/tasks/$TASK/snapshots"

curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  "$API/v1/tasks/$TASK/snapshots/$SNAPSHOT/restore"

curl -sS -X DELETE -H "Authorization: Bearer $TOKEN" \
  "$API/v1/tasks/$TASK/snapshots/$SNAPSHOT"

# The same operations through the flat address
curl -sS -H "Authorization: Bearer $TOKEN" "$API/v1/snapshots?task=$TASK"
```

Three behaviours are worth knowing before you script this:

- **A create needs a running task** (404 `task_not_running` otherwise) and pauses
  the VM for the whole snapshot: state, memory and the task disk. The manager's
  default deadline is 30 s; a create past it is `504 timeout`. The node resumes
  the VM even when the deadline fires or the copy fails, so a timed-out create
  does not leave it paused. A large disk therefore wants a deadline above the
  copy time (`ManagerConfig.SnapshotTimeout`).
- **A restore is refused while the task is running**: `409 conflict` with
  `details.reason: "restore_task_running"`. The live VM still owns its rootfs,
  and a second Firecracker on the same `rootfs.ext4` would corrupt both. Stop the
  task first.
- **A restore puts the disk back if it is gone.** Firecracker snapshots capture
  memory and device state, not the disk bytes, so `create` copies the task disk
  into the snapshot directory. If the executor already removed
  `<rootfs-dir>/tasks/<taskID>.ext4`, `restore` copies the snapshot's copy back to
  that path before the VM starts. An existing task disk is never overwritten —
  it is the live VM's disk. A snapshot whose copy and whose task disk are both
  missing is `409 conflict` with `details.reason: "snapshot_incomplete"`; it
  never starts a VM with a missing disk.

Restore is experimental for a second reason: it starts a Firecracker process
outside SwarmKit's task tracker. The restored VM is not scheduler-managed, so if
you do not stop it through the node, an operator must stop it manually. A crash
during restore can leave the process behind too. See
[Limitations](#limitations) and the API reference's
[Snapshots section](/reference/api/#snapshots-experimental).

---

## Configuration

```yaml
snapshot:
  enabled: false
  snapshot_dir: "/var/lib/firecracker/snapshots"
  max_snapshots: 3        # per service (0 = unlimited)
  max_age: 168h           # cleanup threshold (0 = unlimited)
  auto_snapshot: false    # snapshot automatically on start
  compress: false
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `false` | Enable the snapshot feature |
| `snapshot_dir` | `/var/lib/firecracker/snapshots` | Snapshot storage directory |
| `max_snapshots` | `3` | Max snapshots per service |
| `max_age` | `168h` (7 days) | Age threshold used by `cleanup` |
| `auto_snapshot` | `false` | Snapshot automatically on VM start |
| `compress` | `false` | Compress snapshot files |

---

## Snapshot Storage

Each snapshot gets its own ID and directory, published in one step once it is
complete:

```
/var/lib/firecracker/snapshots/
└── snap-a1b2c3d4e5f67890/
    ├── vm.state      # VM state (~15 KB)
    ├── vm.mem        # Memory image (≈ VM RAM size)
    ├── rootfs.ext4   # Copy of the task disk (≈ disk size)
    └── metadata.json # Snapshot record
```

The metadata records the snapshot ID, task/service/node IDs, creation time, vCPU
count, memory size, rootfs path, the snapshot-owned disk copy, and a SHA-256
checksum of the state file.

**Disk cost.** Memory plus disk, per snapshot: a task with a 4 GiB disk and 1 GiB
of RAM costs about 5 GiB per snapshot, and `max_snapshots`/`max_age` decide how
many are kept. Where the filesystem supports reflinks the copy is initially free
(blocks are shared until one side changes); elsewhere it is a full copy. There is
no separate space pre-flight: a create that runs out of space (or cannot
otherwise copy) fails and deletes the partial snapshot rather than keeping one
that cannot be restored. Deleting a snapshot removes the whole directory, disk
copy included, and `snapshot list`/`size_bytes` include it.

A create leaves nothing half-written: it builds the snapshot in a
`snapshot-staging-*` directory and renames it into place after the metadata is
written. A directory with that prefix is an interrupted create, is ignored by
`list`, and is swept by the next manager start once it is an hour old.

---

## Workflow Examples

### Pre-Update Snapshot

```bash
# Find the task behind the service
swarmcracker service ps <service>

# Snapshot before updating
swarmcracker vm snapshot create <task-id>

# Update the service
swarmcracker service update <service> --image nginx:1.25-alpine

# If something breaks, restore
swarmcracker vm snapshot restore <snapshot-id>
```

### Crash Recovery

```bash
# Snapshot before a risky operation
swarmcracker vm snapshot create <task-id>

# If the VM dies, restore it
swarmcracker vm snapshot restore <snapshot-id>
```

---

## swarmctl Alternative

The lightweight `swarmctl` debug client (manager node only) can also manage
snapshots. Note the name is a positional argument:

```bash
swarmctl snapshot create <task-id> <snapshot-name>
swarmctl snapshot list
swarmctl snapshot restore <snapshot-name>
swarmctl snapshot rm <snapshot-name>
```

---

## Limitations

- **VM must be paused** before snapshot (handled automatically by `create`).
- **Snapshots are node-local** — they are not replicated across the cluster.
- **Size** — a snapshot is roughly the VM's RAM plus its disk; see
  [Snapshot Storage](#snapshot-storage).
- **Rootfs path** — a restore puts the disk copy back at the recorded path, so
  the parent directory must be writable by the daemon.
- **Firecracker version** — requires v1.14.0+ for the current snapshot API.
- **Network state** — active network connections may not survive a restore.
- **Hugepage-backed VMs cannot be restored through this path.** Creating a
  snapshot of a VM started with `--hugepages` works, but Firecracker can restore
  hugepage-backed memory only through a userfaultfd (UFFD) backend. The
  file-backed `restore` in SwarmCracker cannot, and Firecracker rejects the load.
  See [Hugepages → Known limitations](/guides/advanced/#known-limitations).

---

## Troubleshooting

### Snapshot Fails

```bash
# Check the VM/task is running
swarmcracker task ls
swarmcracker vm list

# Check the snapshot directory is writable and has space
ls -la /var/lib/firecracker/snapshots
df -h /var/lib/firecracker/snapshots
```

### Restore Fails

```bash
# Verify the snapshot exists
swarmcracker vm snapshot list

# Confirm the files are present
ls /var/lib/firecracker/snapshots/<snapshot-id>/

# Restoring after the task was cleaned up needs rootfs.ext4 in the snapshot
# and a writable <rootfs-dir>/tasks directory to put it back
df -h /var/lib/firecracker/rootfs
```

### Snapshots Too Large

```bash
# Create VMs with less memory
swarmcracker vm create --memory 256 alpine:latest

# Or reclaim space
swarmcracker vm snapshot cleanup --max-age 24h
```

---

**See Also:** [Configuration](/guides/configuration/) | [CLI Reference](/reference/cli/) | [Operations](/guides/operations/)
