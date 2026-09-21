# Snapshots Guide

> Save and restore VM state — crash recovery, fast boot, debugging.

---

## Overview

SwarmCracker can snapshot a running Firecracker microVM and restore it later.
A snapshot captures the **full VM state**: guest memory plus CPU/device state
(Firecracker `snapshot_type: Full`). This requires **Firecracker v1.14.0+** — the
version installed by `swarmcracker setup install`.

Calling `create` pauses the VM, writes the snapshot, and the VM can then resume.

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
  --rootfs /var/lib/firecracker/rootfs/<image>.ext4 \
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

---

## Configuration

```yaml
snapshot:
  enabled: true
  snapshot_dir: "/var/lib/firecracker/snapshots"
  max_snapshots: 3        # per service (0 = unlimited)
  max_age: 168h           # cleanup threshold (0 = unlimited)
  auto_snapshot: false    # snapshot automatically on start
  compress: false
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `true` | Enable the snapshot feature |
| `snapshot_dir` | `/var/lib/firecracker/snapshots` | Snapshot storage directory |
| `max_snapshots` | `3` | Max snapshots per service |
| `max_age` | `168h` (7 days) | Age threshold used by `cleanup` |
| `auto_snapshot` | `false` | Snapshot automatically on VM start |
| `compress` | `false` | Compress snapshot files |

---

## Snapshot Storage

Each snapshot gets its own ID and directory:

```
/var/lib/firecracker/snapshots/
└── snap-a1b2c3d4e5f67890/
    ├── vm.state      # VM state (~15 KB)
    ├── vm.mem        # Memory image (≈ VM RAM size)
    └── …             # metadata (JSON)
```

The metadata records the snapshot ID, task/service/node IDs, creation time, vCPU
count, memory size, rootfs path, and a SHA-256 checksum of the state file.

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
- **Size** — the memory file is roughly the VM's RAM size.
- **Rootfs path** — the rootfs must be accessible at the same path on restore.
- **Firecracker version** — requires v1.14.0+ for the current snapshot API.
- **Network state** — active network connections may not survive a restore.

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
```

### Snapshots Too Large

```bash
# Create VMs with less memory
swarmcracker vm create --memory 256 alpine:latest

# Or reclaim space
swarmcracker vm snapshot cleanup --max-age 24h
```

---

**See Also:** [Configuration](configuration.md) | [CLI Reference](../reference/cli.md) | [Operations](operations.md)
