---
title: "Image Package Reference"
description: "`pkg/image/` — Image Preparer, OCI Conversion, Init Injection, and Caching."
---

> `pkg/image/` — Image Preparer, OCI Conversion, Init Injection, and Caching.

---

## Overview

The `pkg/image` package prepares OCI container images as Firecracker VM root filesystems. It handles pulling, extraction, init system injection, and ext4 image creation.

**Package Structure:**

```
pkg/image/
├── preparer.go          # ImagePreparer (orchestration)
├── init.go              # InitInjector (tini/dumb-init injection)
├── busybox.go           # Busybox static binary embedding
├── detector.go          # Init system detection
├── oci_info.go          # OCI image configuration parsing
├── validator.go         # Image validation
├── auth.go              # Registry authentication
├── verify.go            # Image verification
├── wrapper.go           # go-containerregistry wrapper
└── binaries/            # Embedded init binaries
```

---

## ImagePreparer

**File:** `preparer.go`

The `ImagePreparer` orchestrates the full image preparation pipeline.

### Type Definition

```go
type ImagePreparer struct {
    config        *PreparerConfig
    cacheDir      string
    rootfsDir     string
    initInjector  *InitInjector
    volumeManager *storage.VolumeManager
    secretManager *storage.SecretManager
    ociInfo       *OCIImageInfo
}
```

### Configuration

```go
type PreparerConfig struct {
    KernelPath      string        `yaml:"kernel_path"`
    RootfsDir       string        `yaml:"rootfs_dir"`
    SocketDir       string        `yaml:"socket_dir"`
    DefaultVCPUs    int           `yaml:"default_vcpus"`
    DefaultMemoryMB int           `yaml:"default_memory_mb"`
    InitSystem      string        `yaml:"init_system"`        // "none", "tini", "dumb-init"
    InitGracePeriod int           `yaml:"init_grace_period"`  // Grace period in seconds
    MaxImageAgeDays int           `yaml:"max_image_age_days"` // Cache cleanup age
    RegistryAuth    *RegistryAuth `yaml:"registry_auth"`      // Registry credentials
}
```

### Constructor

```go
func NewImagePreparer(config interface{}) types.ImagePreparer
```

**Defaults Applied:**

| Field | Default Value |
|-------|---------------|
| `RootfsDir` | `"/var/lib/firecracker/rootfs"` |
| `InitSystem` | `"tini"` |
| `InitGracePeriod` | `10` |
| `MaxImageAgeDays` | `7` |

**Example:**

```go
config := &image.PreparerConfig{
    RootfsDir:       "/var/lib/firecracker/rootfs",
    InitSystem:      "tini",
    InitGracePeriod: 10,
    MaxImageAgeDays: 7,
}

preparer := image.NewImagePreparer(config)
```

---

### Methods

#### Prepare

```go
func (ip *ImagePreparer) Prepare(ctx context.Context, task *types.Task) error
```

**Purpose:** Prepare OCI image for task execution.

**Pipeline:**

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Image Preparation Pipeline                           │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   1. Validate Architecture                                                  │
│      • Check host supports x86_64                                           │
│      • Verify OCI image architecture                                        │
│                                                                             │
│   2. Check Cache                                                            │
│      • Generate image ID (hash of reference)                                │
│      • Check /var/lib/firecracker/rootfs/<id>.ext4 exists                   │
│      • Verify /init symlink valid                                           │
│      • Return cached if valid                                               │
│                                                                             │
│   3. Pull OCI Image                                                         │
│      • remote.Get(imageRef, options)                                        │
│      • Apply registry auth if configured                                    │
│                                                                             │
│   4. Extract to Temp Directory                                              │
│      • mutate.Extract(image)                                                │
│      • Create /tmp/swarmcracker-<id>/                                       │
│                                                                             │
│   5. Detect Init System                                                     │
│      • DetectInitType(tempDir)                                              │
│      → tini / dumb-init / openrc / sysvinit / none                          │
│                                                                             │
│   6. Inject Init System                                                     │
│      • InjectIntoDir(tempDir, ociInfo)                                      │
│      • Create /sbin/init symlink                                            │
│      • Inject busybox for scratch images                                    │
│                                                                             │
│   7. Create ext4 Rootfs                                                     │
│      • mke2fs -t ext4 -d tempDir rootfs.ext4                                │
│      • Move to /var/lib/firecracker/rootfs/<id>.ext4                        │
│                                                                             │
│   8. Cleanup Temp                                                           │
│      • Remove temp directory                                                │
│                                                                             │
│   9. Set Task Annotation                                                    │
│      • task.Annotations["rootfs"] = rootfsPath                             │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**Example:**

```go
task := &types.Task{
    ID: "task-abc123",
    Spec: types.TaskSpec{
        Runtime: &types.Container{
            Image: "nginx:alpine",
        },
    },
    Annotations: make(map[string]string),
}

if err := preparer.Prepare(ctx, task); err != nil {
    log.Fatal(err)
}

rootfsPath := task.Annotations["rootfs"]
fmt.Println("Rootfs:", rootfsPath)
// Output: Rootfs: /var/lib/firecracker/rootfs/nginx-alpine.ext4
```

---

## Init Injection

**File:** `init.go`

### InitInjector

```go
type InitInjector struct {
    config *InitSystemConfig
}

type InitSystemConfig struct {
    Type           InitSystemType
    GracePeriodSec int
}

type InitSystemType string

const (
    InitNone     InitSystemType = "none"
    InitTini     InitSystemType = "tini"
    InitDumbInit InitSystemType = "dumb-init"
)
```

### Constructor

```go
func NewInitInjector(config *InitSystemConfig) *InitInjector
```

---

### Methods

#### InjectIntoDir

```go
func (ii *InitInjector) InjectIntoDir(tmpDir string, ociInfo *OCIImageInfo) error
```

**Purpose:** Inject init system into extracted directory.

**This is the primary method** — called BEFORE ext4 creation.

**Steps:**

1. **Detect init type**
   ```go
   initType := ii.detectInitType(ociInfo)
   ```

2. **Handle by type:**

   | Type | Action |
   |------|--------|
   | `scratch` | Inject busybox + tini |
   | `tini` | Create `/init → /sbin/init` symlink |
   | `dumb-init` | Create `/init → /usr/bin/dumb-init` symlink |
   | `systemd` | **Reject** (incompatible with microVMs) |
   | `none` | Inject tini from embedded binary |

3. **Create /sbin/init symlink**

   ```go
   // Ensure /sbin directory exists
   os.MkdirAll(filepath.Join(tmpDir, "sbin"), 0755)
   
   // Create symlink
   os.Symlink(initPath, filepath.Join(tmpDir, "sbin", "init"))
   os.Symlink(initPath, filepath.Join(tmpDir, "init"))
   ```

---

#### Inject

```go
func (ii *InitInjector) Inject(rootfsPath string) error
```

**Status:** **DEPRECATED** — No-op stub.

The old `Inject(rootfsPath)` method never actually mounted the ext4 image. Use `InjectIntoDir()` instead.

---

## Init Detection

**File:** `detector.go`

### Function

```go
func DetectInitType(tmpDir string) InitTypeResult
```

**Purpose:** Detect init system type from an extracted rootfs. Detection is
stateless (free function, not a method).

```go
type InitTypeResult struct {
    Type    InitType
    Message string // Human-readable explanation
}

type InitType string

const (
    InitTypeNone         InitType = "none"
    InitTypeSystemd      InitType = "systemd"
    InitTypeOpenRC       InitType = "openrc"
    InitTypeSysvinit     InitType = "sysvinit"
    InitTypeTini         InitType = "tini"
    InitTypeDumbInit     InitType = "dumb-init"
    InitTypeScratch      InitType = "scratch"
    InitTypeUnknown      InitType = "unknown"
    InitTypeIncompatible InitType = "incompatible"
)
```

**Detection Rules:**

| Check | Condition | Result |
|-------|-----------|--------|
| Empty/scratch dir | → | `InitTypeScratch` |
| systemd | → | `InitTypeIncompatible` |
| tini present | → | `InitTypeTini` |
| dumb-init present | → | `InitTypeDumbInit` |
| OpenRC present | → | `InitTypeOpenRC` |
| sysvinit present | → | `InitTypeSysvinit` |
| Otherwise | → | `InitTypeNone` |

---

## OCI Image Info

**File:** `oci_info.go`

### OCIImageInfo

```go
type OCIImageInfo struct {
    Entrypoint   []string // OCI ENTRYPOINT (exec form)
    Cmd          []string // OCI CMD (exec form)
    Env          []string // KEY=VALUE pairs
    User         string   // OCI USER (e.g. "nginx", "1000:1000")
    WorkDir      string   // OCI WORKDIR
    StopSignal   string   // OCI STOPSIGNAL (default "SIGTERM")
    OS           string   // Image OS (e.g. "linux")
    Architecture string   // Image architecture (e.g. "amd64")
    ImageRef     string   // Original image reference
}
```

### Parse

```go
func ParseOCIImageConfig(cfg *v1.ConfigFile, imageRef string) *OCIImageInfo
func FullCommand(info *OCIImageInfo) []string
```

**Purpose:** Parse a go-containerregistry config file (and combine
ENTRYPOINT/CMD) into an `OCIImageInfo`.

**Example:**

```go
info := ParseOCIImageConfig(cfg, "nginx:latest")
fmt.Printf("Arch: %s, Command: %v\n", info.Architecture, FullCommand(info))
```

---

## Busybox Injection

**File:** `busybox.go`

For scratch images or images without init:

```go
func injectBusybox(tmpDir string) error
```

**Purpose:** Inject the embedded busybox static binary (unexported helper).

**Implementation:**
- Extract embedded busybox binary
- Create symlinks for common commands

---

## Registry Authentication

**File:** `auth.go`

### RegistryAuth

```go
type RegistryAuth struct {
    Username      string `yaml:"username"`
    Password      string `yaml:"password"`
    Auth          string `yaml:"auth"`      // Base64 encoded
    IdentityToken string `yaml:"identity_token"`
    RegistryToken string `yaml:"registry_token"`
}
```

### Keychain

```go
func NewKeychain(auth *RegistryAuth) authn.Keychain
```

**Purpose:** Create authn.Keychain for go-containerregistry.

---

## Image Verification

**File:** `verify.go`

```go
func VerifyBootable(rootfsPath string) error
```

**Purpose:** Check an ext4 rootfs has the required files for booting (`/init`,
`/sbin/init`, `/sbin/tini`, `/bin/sh`, `/etc/resolv.conf`) using `debugfs`.
Returns `nil` (with a debug log) when `debugfs` is not available.

> There is no cosign / signed-image verification in this package.

---

## Validation

**File:** `validator.go`

```go
func validateImageManifest(ctx context.Context, imageRef string, opts ...remote.Option) error
```

**Purpose:** Validate that an image is Firecracker-compatible: the OS must be
`linux` and the architecture must match the host (with alias normalization).
Unreadable manifests are accepted with a warning (graceful degradation). This
function is unexported; it is called during image preparation.

---

## Cache Management

### Image ID Generation

```go
func generateImageID(imageRef string) string
```

**Algorithm:**
```go
// Normalize reference
normalized := strings.ToLower(imageRef)
normalized = strings.ReplaceAll(normalized, "/", "-")
normalized = strings.ReplaceAll(normalized, ":", "-")

return normalized
```

### Cache Cleanup

```go
func (ip *ImagePreparer) Cleanup(ctx context.Context, keepDays int) (filesRemoved int, bytesFreed int64, err error)
```

**Purpose:** Remove rootfs images older than `keepDays` and report the number
of files removed and bytes freed.

---

## Testing

### Mock ImagePreparer

```go
type MockImagePreparer struct {
    RootfsPath string
    PrepareErr error
}

func (m *MockImagePreparer) Prepare(ctx context.Context, task *types.Task) error {
    if m.PrepareErr != nil {
        return m.PrepareErr
    }
    task.Annotations["rootfs"] = m.RootfsPath
    return nil
}
```

---

## Error Handling

### Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `"task cannot be nil"` | Nil task | Provide valid task |
| `"architecture validation failed"` | Wrong arch | Use amd64 images |
| `"invalid task runtime"` | Not container | Use container runtime |
| `"systemd images rejected"` | systemd init | Use tini-based images |
| `"failed to create ext4"` | Disk error | Check disk space |
| `"registry auth failed"` | Bad credentials | Verify auth config |

---

## Init Troubleshooting

### VM Fails to Boot: "No init found"

**Symptoms:**
```
Kernel panic - not syncing: No working init found.
```

**Root Cause:** Init not injected before ext4 creation.

**Solution:**

```go
// Correct order:
preparer.Prepare(ctx, task)
// → Calls InjectIntoDir(tmpDir, ociInfo)
// → THEN creates ext4

// NOT:
// Create ext4 first
// Then call Inject(rootfsPath) // DEPRECATED, no-op
```

---

## Related Documentation

| Topic | Document |
|-------|----------|
| SwarmKit executor | [SwarmKit Reference](swarmkit.md) |
| Storage volumes | [Storage Reference](storage.md) |
| Init troubleshooting | [Security Guide](/contributing/security/#init-injection-troubleshooting) |