# Storage Package Reference

> `pkg/storage/` — Volume Manager, Secret Manager, and DebugFS Injection.

---

## Overview

The `pkg/storage` package provides storage abstractions for Firecracker VMs, including volume management (directory and block drivers), secret/config injection, and debugfs operations for ext4 rootfs modification.

**Package Structure:**

```
pkg/storage/
├── volume.go           # VolumeManager (dispatcher)
├── volume_dir.go       # DirectoryDriver (host directory volumes)
├── volume_block.go     # BlockDriver (block device volumes)
├── volume_meta.go      # MetaStore (volume metadata)
├── volume_quota.go     # Quota management
├── credential_store.go # SecretManager (secrets/configs)
├── driver.go           # VolumeDriver interface
└───────────────────────┴────────────────────────────────────────────────────
```

---

## VolumeManager

**File:** `volume.go`

The `VolumeManager` dispatches volume operations to appropriate drivers.

### Type Definition

```go
type VolumeManager struct {
    drivers     map[VolumeType]VolumeDriver
    defaultType VolumeType
    mu          sync.RWMutex
}
```

### Volume Types

```go
type VolumeType string

const (
    VolumeTypeDir   VolumeType = "dir"   // Host directory
    VolumeTypeBlock VolumeType = "block" // Block device
)
```

### Volume Struct

```go
type Volume struct {
    ID     string `json:"id"`     // Sanitized name
    Name   string `json:"name"`   // Original name
    Path   string `json:"path"`   // Volume path
    TaskID string `json:"task_id"` // Associated task
}
```

### Constructor

```go
func NewVolumeManager(volumesDir string) (*VolumeManager, error)
```

**Parameters:**
- `volumesDir` — Base directory for volumes (default: `/var/lib/swarmcracker/volumes`)

**Example:**

```go
vm, err := storage.NewVolumeManager("/var/lib/swarmcracker/volumes")
if err != nil {
    log.Fatal(err)
}
```

---

### Methods

#### CreateVolume (Legacy)

```go
func (vm *VolumeManager) CreateVolume(ctx context.Context, name, taskID string, sizeMB int) (*Volume, error)
```

**Purpose:** Create volume with default type (dir).

---

#### CreateVolumeWithOptions

```go
func (vm *VolumeManager) CreateVolumeWithOptions(ctx context.Context, name, taskID string, opts CreateOptions) (*Volume, error)
```

**Purpose:** Create volume with full options.

```go
type CreateOptions struct {
    Type   VolumeType
    SizeMB  int
    Quota   QuotaConfig
    Labels  map[string]string
}
```

**Example:**

```go
vol, err := vm.CreateVolumeWithOptions(ctx, "data-vol", "task-123", storage.CreateOptions{
    Type:   storage.VolumeTypeDir,
    SizeMB: 1024,
})
```

---

#### GetVolume

```go
func (vm *VolumeManager) GetVolume(name string) (*Volume, error)
```

**Purpose:** Retrieve volume handle by name.

---

#### GetVolumeInfo

```go
func (vm *VolumeManager) GetVolumeInfo(ctx context.Context, name string, volType VolumeType) (*VolumeInfo, error)
```

**Purpose:** Get detailed volume info.

```go
type VolumeInfo struct {
    Name       string
    Type       VolumeType
    SizeMB     int
    UsedMB     int64
    CreatedAt  time.Time
    LastUsedAt time.Time
    TaskID     string
}
```

---

#### MountVolume

```go
func (vm *VolumeManager) MountVolume(ctx context.Context, vol *Volume, rootfsPath, target string) error
```

**Purpose:** Copy volume data into rootfs at target path.

**Implementation (DirectoryDriver):**
1. Copy directory contents to rootfs subdirectory
2. Preserve permissions and ownership

---

#### UnmountVolume

```go
func (vm *VolumeManager) UnmountVolume(ctx context.Context, vol *Volume, rootfsPath, target string, readOnly bool) error
```

**Purpose:** Sync data back from rootfs to volume.

---

#### DeleteVolume

```go
func (vm *VolumeManager) DeleteVolume(ctx context.Context, name string) error
```

**Purpose:** Remove volume and its data.

---

#### ListVolumes

```go
func (vm *VolumeManager) ListVolumes(ctx context.Context) ([]*Volume, error)
```

**Purpose:** List all volumes.

---

#### ListVolumeInfos

```go
func (vm *VolumeManager) ListVolumeInfos(ctx context.Context) ([]*VolumeInfo, error)
```

**Purpose:** List detailed volume info.

---

#### SnapshotVolume

```go
func (vm *VolumeManager) SnapshotVolume(ctx context.Context, name string) (*Snapshot, error)
```

**Purpose:** Create point-in-time snapshot.

---

#### RestoreVolume

```go
func (vm *VolumeManager) RestoreVolume(ctx context.Context, name string, snap *Snapshot) error
```

**Purpose:** Restore from snapshot.

---

## DirectoryDriver

**File:** `volume_dir.go`

Host directory-backed volumes.

### Type Definition

```go
type DirectoryDriver struct {
    baseDir  string
    meta     *MetaStore
    quotaMgr *QuotaManager
}
```

### Constructor

```go
func NewDirectoryDriver(baseDir string) (*DirectoryDriver, error)
```

---

### Methods

#### Create

```go
func (d *DirectoryDriver) Create(ctx context.Context, name string, opts CreateOptions) (string, error)
```

**Purpose:** Create directory volume.

**Implementation:**
```go
volPath := filepath.Join(d.baseDir, sanitizeVolumeName(name))
os.MkdirAll(volPath, 0755)

// Apply quota if specified
if opts.Quota.Enabled {
    d.quotaMgr.SetQuota(volPath, opts.Quota.MaxBytes)
}
```

---

#### Mount

```go
func (d *DirectoryDriver) Mount(ctx context.Context, name, rootfsPath, target string) error
```

**Purpose:** Copy volume contents into rootfs using debugfs.

**Implementation:**
```go
// For each file in volume:
debugfs -w rootfsPath -R "write <src> <target>"
```

---

#### Unmount

```go
func (d *DirectoryDriver) Unmount(ctx context.Context, name, rootfsPath, target string, readOnly bool) error
```

**Purpose:** Copy rootfs data back to volume.

---

#### Delete

```go
func (d *DirectoryDriver) Delete(ctx context.Context, name string) error
```

---

#### Stat

```go
func (d *DirectoryDriver) Stat(ctx context.Context, name string) (*VolumeInfo, error)
```

---

## BlockDriver

**File:** `volume_block.go`

Block device-backed volumes for high-performance I/O.

### Type Definition

```go
type BlockDriver struct {
    baseDir string
    meta    *MetaStore
}
```

### Constructor

```go
func NewBlockDriver(baseDir string) (*BlockDriver, error)
```

---

### Methods

#### Create

```go
func (b *BlockDriver) Create(ctx context.Context, name string, opts CreateOptions) (string, error)
```

**Purpose:** Create block device volume.

**Implementation:**
1. Create qcow2 or raw image file
2. Setup loop device
3. Format with ext4

---

#### Mount

```go
func (b *BlockDriver) Mount(ctx context.Context, name, rootfsPath, target string) error
```

**Purpose:** Mount block volume and copy to rootfs.

---

#### Snapshot

```go
func (b *BlockDriver) Snapshot(ctx context.Context, name string) (*Snapshot, error)
```

**Purpose:** Create qcow2 snapshot.

---

## MetaStore

**File:** `volume_meta.go`

JSON-based volume metadata storage.

### Type Definition

```go
type MetaStore struct {
    metaDir string
    mu      sync.RWMutex
}

type VolumeMeta struct {
    Name       string     `json:"name"`
    Type       VolumeType `json:"type"`
    SizeMB     int        `json:"size_mb"`
    CreatedAt  time.Time  `json:"created_at"`
    LastUsedAt time.Time  `json:"last_used_at"`
    TaskID     string     `json:"task_id"`
    Quota      QuotaInfo  `json:"quota"`
}
```

### Methods

```go
func (m *MetaStore) Save(ctx context.Context, meta *VolumeMeta) error
func (m *MetaStore) Load(ctx context.Context, name string) (*VolumeMeta, error)
func (m *MetaStore) List(ctx context.Context) ([]*VolumeMeta, error)
func (m *MetaStore) Delete(ctx context.Context, name string) error
func (m *MetaStore) UpdateTaskID(ctx context.Context, name, taskID string) error
```

---

## Quota Management

**File:** `volume_quota.go`

### Type Definition

```go
type QuotaManager struct {
    enabled bool
}

type QuotaConfig struct {
    Enabled  bool
    MaxBytes int64
}
```

### Methods

```go
func (q *QuotaManager) SetQuota(path string, maxBytes int64) error
func (q *QuotaManager) GetUsage(path string) (int64, error)
```

**Implementation (Linux):**
```bash
# Set project quota
setquota -P <project-id> <soft> <hard> 0 0 <path>
```

---

## SecretManager

**File:** `credential_store.go`

Manages secrets and configs injection into VM rootfs.

### Type Definition

```go
type SecretManager struct {
    secretsDir string
    configsDir string
}
```

### Constructor

```go
func NewSecretManager(secretsDir, configsDir string) *SecretManager
```

---

### Methods

#### InjectSecret

```go
func (sm *SecretManager) InjectSecret(ctx context.Context, rootfsPath, targetPath string, secretData []byte) error
```

**Purpose:** Inject secret file into rootfs.

**Implementation:**
```go
// Use debugfs to write file
debugfs -w rootfsPath -R "write <temp-file> <target-path>"
```

---

#### InjectConfig

```go
func (sm *SecretManager) InjectConfig(ctx context.Context, rootfsPath, targetPath string, configData []byte) error
```

**Purpose:** Inject config file into rootfs.

---

#### ListSecrets

```go
func (sm *SecretManager) ListSecrets(ctx context.Context) ([]string, error)
```

---

#### DeleteSecret

```go
func (sm *SecretManager) DeleteSecret(ctx context.Context, name string) error
```

---

## Volume Reference Helpers

**File:** `volume.go`

### IsVolumeReference

```go
func IsVolumeReference(source string) bool
```

**Purpose:** Check if mount source is a volume reference.

**Rules:**
- `volume://name` → Volume reference
- `name` (no slashes) → Volume reference
- `/path/to/dir` → Host path (not volume)

---

### ExtractVolumeName

```go
func ExtractVolumeName(source string) string
```

**Purpose:** Extract volume name from reference.

```go
IsVolumeReference("volume://data")  // true → "data"
IsVolumeReference("data")           // true → "data"
IsVolumeReference("/var/data")      // false → ""
```

---

## Testing

### Mock VolumeManager

```go
type MockVolumeManager struct {
    Volumes map[string]*Volume
}

func (m *MockVolumeManager) CreateVolume(ctx context.Context, name, taskID string, sizeMB int) (*Volume, error) {
    vol := &Volume{
        ID:   name,
        Name: name,
        Path: "/mock/" + name,
    }
    m.Volumes[name] = vol
    return vol, nil
}
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"volume name cannot be empty"` | Empty name | Provide valid name |
| `"volume not found"` | Invalid name | Check volume exists |
| `"no driver registered for type"` | Unknown type | Use `dir` or `block` |
| `"block driver unavailable"` | Not root | Run with privileges |
| `"quota not supported"` | No quota support | Enable project quotas |

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit executor | [SwarmKit Reference](swarmkit.md) |
| Image preparation | [Image Reference](image.md) |
| Operations guide | [Operations Guide](../../user/guides/operations.md) |