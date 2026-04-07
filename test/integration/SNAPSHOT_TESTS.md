# Snapshot Integration Tests

This document describes the snapshot integration tests for SwarmCracker.

## Test Files

- `snapshot_integration_test.go` - Integration tests for snapshot functionality

## Running Tests

### Unit Tests (no special requirements)
```bash
go test -v ./pkg/snapshot/...
```

### Integration Tests (requires KVM + Firecracker)
```bash
# Basic tests (no bootable rootfs required)
go test -v -tags=integration ./test/integration/... -run Snapshot

# Full VM snapshot test (requires bootable rootfs)
ROOTFS_PATH=/path/to/bootable-rootfs.ext4 \
  go test -v -tags=integration ./test/integration/... -run TestIntegration_Snapshot_CreateAndRestore
```

## Test Coverage

### Unit Tests (`pkg/snapshot/snapshot_test.go`)
- ✅ Config defaults and validation
- ✅ Metadata save/load
- ✅ Filter matching (task, service, node, time ranges)
- ✅ List, delete, cleanup operations
- ✅ SHA-256 checksum calculation
- ✅ Max snapshots enforcement
- ✅ Snapshot ID generation

### Integration Tests (`test/integration/snapshot_integration_test.go`)

#### TestIntegration_Snapshot_CreateAndRestore
Tests the full snapshot lifecycle:
1. **Snapshot Manager Methods** (always runs):
   - List empty snapshots
   - Create mock snapshots
   - List and filter snapshots
   - Delete snapshots
   - Cleanup old snapshots

2. **Full VM Test** (requires `ROOTFS_PATH` env var):
   - Start Firecracker VM
   - Create snapshot via Firecracker API
   - List snapshots
   - Restore from snapshot
   - Delete snapshot

#### TestIntegration_Snapshot_CleanupOldSnapshots
Tests automatic cleanup of snapshots older than max age.

#### TestIntegration_Snapshot_MaxSnapshotsEnforcement
Tests that max snapshot limit per service is enforced.

#### TestIntegration_Snapshot_ChecksumVerification
Tests that corrupted state files are detected via checksum mismatch.

## Prerequisites

For basic tests:
- Go 1.21+
- Standard Go testing framework

For full VM snapshot tests:
- KVM support (`/dev/kvm`)
- Firecracker v1.14+ in PATH
- Bootable rootfs image (Alpine, Ubuntu, etc.)
- Firecracker kernel

## Creating a Bootable Rootfs

To run full VM snapshot tests, create a bootable rootfs:

```bash
# Download Alpine rootfs
wget https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz

# Create ext4 image
dd if=/dev/zero of=rootfs.ext4 bs=1M count=200
mkfs.ext4 -F rootfs.ext4

# Mount and extract
mkdir /mnt/rootfs
sudo mount -o loop rootfs.ext4 /mnt/rootfs
sudo tar -xzf alpine-minirootfs-3.19.0-x86_64.tar.gz -C /mnt/rootfs
sudo umount /mnt/rootfs

# Run test
ROOTFS_PATH=$(pwd)/rootfs.ext4 go test -v -tags=integration ./test/integration/... -run Snapshot
```

## Firecracker Snapshot API

The snapshot feature uses Firecracker's snapshot API:

1. **Create Snapshot**: `PUT /snapshot/create`
   - Pauses the VM
   - Saves memory and state to files
   - VM exits after snapshot

2. **Restore Snapshot**: 
   - Start Firecracker with `--snapshot <state-file>`
   - `PUT /snapshot/load` to load memory
   - `PUT /actions` with `InstanceStart` to resume

## Known Limitations

1. **Snapshot requires VM pause**: Creating a snapshot pauses the VM, so it's not suitable for production workloads without careful planning.

2. **Rootfs must be accessible**: The restored VM needs access to the same rootfs path.

3. **No live migration yet**: Current implementation supports snapshot/restore on the same host. Live migration (snapshot on host A, restore on host B) requires shared storage or snapshot transfer.

## Future Enhancements

- [ ] Compressed snapshots (gzip memory files)
- [ ] Incremental snapshots
- [ ] Live migration support
- [ ] Snapshot scheduling/automation
- [ ] Web dashboard integration
