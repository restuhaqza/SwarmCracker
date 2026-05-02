# Snapshots Guide

> Save and restore VM state — crash recovery, fast boot, debugging.

---

## Overview

SwarmCracker supports Firecracker snapshots:

- **Full snapshots** — Complete VM state (memory + disk)
- **Partial snapshots** — Memory only
- **Fast restore** — Resume VM in milliseconds

---

## Use Cases

| Use Case | Benefit |
|----------|---------|
| **Crash recovery** | Restore VM to known state |
| **Fast boot** | Resume from snapshot ~50ms vs 1s cold boot |
| **Debugging** | Capture VM state at specific point |
| **Testing** | Reproduce exact conditions |

---

## CLI Commands

```bash
# Create snapshot
swarmctl snapshot create <vm-id> --name backup-1

# List snapshots
swarmctl snapshot list <vm-id>

# Restore snapshot
swarmctl snapshot restore <vm-id> --name backup-1

# Delete snapshot
swarmctl snapshot delete <vm-id> --name backup-1
```

---

## Snapshot Types

### Full Snapshot

Saves memory + disk state:

```bash
swarmctl snapshot create <vm-id> \
  --name full-backup \
  --type full
```

**Created files:**
- `snapshots/<vm-id>/backup-1.mem` — Memory state (~VM RAM size)
- `snapshots/<vm-id>/backup-1.vmstate` — VM metadata
- `snapshots/<vm-id>/backup-1.disk` — Disk state (if configured)

### Partial Snapshot

Memory only (faster):

```bash
swarmctl snapshot create <vm-id> \
  --name quick-save \
  --type partial
```

**Created files:**
- `snapshots/<vm-id>/quick-save.mem` — Memory state
- `snapshots/<vm-id>/quick-save.vmstate` — VM metadata

---

## Configuration

```yaml
snapshot:
  enabled: true
  storage_path: "/var/lib/swarmcracker/snapshots"
  max_snapshots: 10          # per VM
  auto_cleanup: true         # delete oldest when limit reached
```

| Option | Default | Description |
|--------|---------|-------------|
| `enabled` | `true` | Enable snapshot feature |
| `storage_path` | `/var/lib/swarmcracker/snapshots` | Snapshot directory |
| `max_snapshots` | `10` | Max snapshots per VM |
| `auto_cleanup` | `true` | Auto-delete old snapshots |

---

## Snapshot Storage

```
/var/lib/swarmcracker/snapshots/
├── svc-nginx-abc123/
│   ├── backup-1.mem        (512 MB)
│   ├── backup-1.vmstate    (1 KB)
│   ├── backup-1.disk       (1 GB)
│   ├── backup-2.mem
│   └── backup-2.vmstate
└── svc-redis-def456/
│   ├── quick-save.mem
│   └── quick-save.vmstate
```

---

## Performance

| Operation | Time |
|-----------|------|
| Create full snapshot | ~500ms (memory size dependent) |
| Create partial snapshot | ~100ms |
| Restore full snapshot | ~50ms |
| Restore partial snapshot | ~30ms |
| Cold boot | ~1s |

---

## Workflow Examples

### Pre-Update Snapshot

```bash
# Before updating service
swarmctl snapshot create svc-nginx --name pre-update

# Update service
swarmctl update svc-nginx --image nginx:1.25

# If issue detected, restore
swarmctl snapshot restore svc-nginx --name pre-update
```

### Crash Recovery

```bash
# Create snapshot before risky operation
swarmctl snapshot create svc-db --name before-transaction

# If VM crashes, restore
swarmctl snapshot restore svc-db --name before-transaction
```

### Debug Workflow

```bash
# Capture state at bug point
swarmctl snapshot create svc-app --name bug-state

# Restore for analysis
swarmctl snapshot restore svc-app --name bug-state

# Inspect VM
swarmctl inspect svc-app
```

---

## Limitations

- **VM must be paused** before snapshot (Firecracker requirement)
- **Snapshots are node-local** — Not replicated across cluster
- **Memory size** — Snapshot size equals VM RAM
- **Disk required** — Full snapshots need persistent disk

---

## Troubleshooting

### Snapshot Fails

```bash
# Check VM is running
swarmctl ls-tasks

# Check snapshot directory writable
ls -la /var/lib/swarmcracker/snapshots

# Check disk space
df -h /var/lib/swarmcracker/snapshots
```

### Restore Fails

```bash
# Verify snapshot exists
swarmctl snapshot list <vm-id>

# Check snapshot files present
ls /var/lib/swarmcracker/snapshots/<vm-id>/
```

### Snapshots Too Large

```bash
# Reduce VM memory in config
memory_mb: 256  # instead of 1024

# Use partial snapshots (no disk)
swarmctl snapshot create <vm-id> --type partial
```

---

**See Also:** [Configuration](configuration.md) | [CLI Reference](../reference/cli.md)