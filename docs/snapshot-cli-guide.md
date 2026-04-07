# SwarmCracker Snapshot CLI Guide

Complete guide to using the snapshot feature in SwarmCracker CLI.

## Prerequisites

- SwarmCracker CLI installed
- Firecracker v1.14.0+ installed
- Running microVMs managed by SwarmCracker

## Quick Start

```bash
# List all snapshots
swarmcracker snapshot list

# Create a snapshot of a running VM
swarmcracker snapshot create task-123

# Restore a VM from snapshot
swarmcracker snapshot restore snap-abc123def4567890

# Delete a snapshot
swarmcracker snapshot delete snap-abc123def4567890

# Cleanup old snapshots
swarmcracker snapshot cleanup --max-age 24h
```

## Commands

### `swarmcracker snapshot create`

Create a snapshot of a running microVM.

**Usage:**
```bash
swarmcracker snapshot create <task-id> [flags]
```

**Flags:**
- `--socket string` - Path to Firecracker API socket (default: `<socket-dir>/<task-id>.sock`)
- `--service string` - SwarmKit service ID for metadata
- `--node string` - Node ID for metadata
- `--vcpus int` - vCPU count (for metadata)
- `--memory int` - Memory in MB (for metadata)
- `--rootfs string` - Rootfs path (for metadata)

**Example:**
```bash
# Basic usage
swarmcracker snapshot create task-123

# With custom socket path
swarmcracker snapshot create task-123 --socket /var/run/firecracker/task-123.sock

# With metadata
swarmcracker snapshot create task-123 \
  --service nginx \
  --node worker-1 \
  --vcpus 2 \
  --memory 512
```

**Output:**
```
Snapshot created successfully
  ID:        snap-a1b2c3d4e5f67890
  Task ID:   task-123
  Created:   2026-04-07T17:30:00Z
  Size:      256.0 MB
  Checksum:  abc123def456...
```

### `swarmcracker snapshot restore`

Restore a microVM from a previously created snapshot.

**Usage:**
```bash
swarmcracker snapshot restore <snapshot-id> [flags]
```

**Flags:**
- `--socket string` - Path for new Firecracker API socket

**Example:**
```bash
# Basic restore
swarmcracker snapshot restore snap-a1b2c3d4e5f67890

# With custom socket path
swarmcracker snapshot restore snap-a1b2c3d4e5f67890 \
  --socket /var/run/firecracker/restored-vm.sock
```

**Output:**
```
VM restored from snapshot
  Snapshot: snap-a1b2c3d4e5f67890
  Task ID:  task-123
  Socket:   /var/run/firecracker/task-123-restored.sock
```

### `swarmcracker snapshot list`

List all VM snapshots, optionally filtered.

**Usage:**
```bash
swarmcracker snapshot list [flags]
```

**Flags:**
- `--service string` - Filter by service ID
- `--task string` - Filter by task ID
- `--node string` - Filter by node ID

**Example:**
```bash
# List all snapshots
swarmcracker snapshot list

# Filter by service
swarmcracker snapshot list --service nginx

# Filter by task
swarmcracker snapshot list --task task-123

# Filter by node
swarmcracker snapshot list --node worker-1
```

**Output:**
```
ID                  TASK            SERVICE       NODE          CREATED             SIZE      CHECKSUM
snap-a1b2c3d4e5f6   task-123        nginx         worker-1      2026-04-07 17:30    256.0 MB  abc123def456...
snap-b2c3d4e5f678   task-456        redis         worker-2      2026-04-07 16:45    128.0 MB  def456abc789...

2 snapshot(s)
```

### `swarmcracker snapshot delete`

Delete a VM snapshot and free disk space.

**Usage:**
```bash
swarmcracker snapshot delete <snapshot-id>
```

**Example:**
```bash
# Delete a specific snapshot
swarmcracker snapshot delete snap-a1b2c3d4e5f67890
```

**Output:**
```
Deleting snapshot:
  ID:       snap-a1b2c3d4e5f67890
  Task ID:  task-123
  Created:  2026-04-07T17:30:00Z
  Size:     256.0 MB
Snapshot deleted.
```

### `swarmcracker snapshot cleanup`

Remove snapshots older than the specified age.

**Usage:**
```bash
swarmcracker snapshot cleanup [flags]
```

**Flags:**
- `--max-age string` - Maximum snapshot age (Go duration: 24h, 48h, 168h, etc.)

**Example:**
```bash
# Cleanup snapshots older than 24 hours
swarmcracker snapshot cleanup --max-age 24h

# Cleanup snapshots older than 3 days
swarmcracker snapshot cleanup --max-age 72h

# Cleanup snapshots older than 1 week
swarmcracker snapshot cleanup --max-age 168h

# Use default (from config or 7 days)
swarmcracker snapshot cleanup
```

**Output:**
```
Cleaned up 5 snapshot(s), freed 1.3 GB
```

## Configuration

Snapshots can be configured in the SwarmCracker config file:

```yaml
# /etc/swarmcracker/config.yaml
snapshot:
  enabled: true
  snapshot_dir: /var/lib/firecracker/snapshots
  max_snapshots: 3        # Per service
  max_age: 168h           # 7 days
  auto_snapshot: false    # Auto-snapshot on successful start
  compress: false         # Gzip compress memory snapshots
```

## Use Cases

### 1. Fast VM Restore

Snapshot a VM before making changes, restore if something goes wrong:

```bash
# Create snapshot before update
swarmcracker snapshot create task-nginx --service nginx

# Perform update...
# If update fails, restore:
swarmcracker snapshot restore snap-a1b2c3d4e5f67890
```

### 2. VM Migration (Future)

Copy snapshot files to another host and restore:

```bash
# On source host
swarmcracker snapshot create task-123
scp /var/lib/firecracker/snapshots/snap-* user@target:/tmp/snapshots/

# On target host
swarmcracker snapshot restore snap-a1b2c3d4e5f67890
```

### 3. Automated Backups

Use cleanup to maintain snapshot retention:

```bash
# Daily cleanup in cron
0 2 * * * swarmcracker snapshot cleanup --max-age 24h
```

### 4. Development Workflow

Snapshot a working state for quick iteration:

```bash
# Snapshot clean development environment
swarmcracker snapshot create dev-vm --service dev

# Make changes, test, break things...

# Restore to clean state
swarmcracker snapshot restore snap-dev123
```

## Troubleshooting

### "VM not paused" Error

**Problem:** Snapshot creation fails with VM state error  
**Solution:** The CLI automatically pauses the VM before snapshot (v1.14.x). If this fails, ensure the VM is running.

### "Snapshot not found" Error

**Problem:** Cannot find snapshot by ID  
**Solution:** List snapshots to verify ID:
```bash
swarmcracker snapshot list
```

### "Restore failed" Error

**Problem:** VM restore fails  
**Solution:** Check that:
1. Snapshot files exist in snapshot directory
2. Rootfs path is accessible
3. No other VM is using the same socket path

### Checksum Mismatch

**Problem:** State file checksum doesn't match  
**Solution:** Snapshot files may be corrupted. Delete and recreate:
```bash
swarmcracker snapshot delete snap-abc123
swarmcracker snapshot create task-123
```

## API Reference

The CLI uses the Firecracker v1.14.x API:

| Operation | Endpoint | Method |
|-----------|----------|--------|
| Pause VM | `/vm` | PATCH |
| Resume VM | `/vm` | PATCH |
| Create Snapshot | `/snapshot/create` | PUT |
| Load Snapshot | `/snapshot/load` | PUT |

## Files and Directories

- **Snapshot Directory:** `/var/lib/firecracker/snapshots/` (configurable)
- **State File:** `vm.state` (~15 KB)
- **Memory File:** `vm.mem` (size = VM memory)
- **Metadata:** `metadata.json` (auto-generated)

## Performance

- **Snapshot Creation:** ~1-2 seconds (includes VM pause)
- **Snapshot Restore:** ~2-3 seconds (with resume_vm=true)
- **Cold Boot:** ~5-10 seconds
- **Space Savings:** Snapshot restore is 2-3x faster than cold boot

## Security Notes

1. **Snapshot files are trusted** - Firecracker trusts snapshot files
2. **Secure snapshot storage** - Encrypt snapshots at rest if they contain sensitive data
3. **Access control** - Restrict access to snapshot directory
4. **Network state** - Network connections may not survive snapshot restore

## See Also

- `docs/snapshot-complete.md` - Complete snapshot workflow
- `docs/snapshot-test-report.md` - Test results
- `test/integration/SNAPSHOT_TESTS.md` - Integration tests
- Firecracker Documentation: https://github.com/firecracker-microvm/firecracker/tree/main/docs/snapshotting

---

**Last Updated:** 2026-04-07  
**SwarmCracker Version:** v0.2.1+  
**Firecracker Version:** v1.14.0+
