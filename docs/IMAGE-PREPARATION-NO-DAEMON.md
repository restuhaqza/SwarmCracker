# Plan: Remove Docker/Podman Dependency for Image Preparation

## Current State

**File:** `pkg/image/preparer.go`

Current implementation uses:
- `docker create` + `docker export` → extract filesystem
- `podman create` + `podman export` → extract filesystem

Both require:
- Container daemon running
- Container storage configured
- Root privileges for some operations

---

## Proposed Alternatives

### Option 1: Skopeo + umoci (Recommended)

**Skopeo** - Copy images without daemon
```bash
# Download image to OCI format locally
skopeo copy docker://nginx:latest oci:/tmp/nginx-oci

# Result: OCI image layout with layers in /tmp/nginx-oci/blobs/sha256/
```

**umoci** - Unpack OCI images
```bash
# Extract to rootfs directory
umoci unpack --image oci:/tmp/nginx-oci:latest /tmp/rootfs

# Result: Extracted filesystem in /tmp/rootfs/rootfs/
```

**Pros:**
- No daemon required
- Works rootless
- Pure OCI compliance
- Small dependency footprint
- Can extract specific image variants

**Cons:**
- Additional tools to install
- umoci less common than skopeo

---

### Option 2: Buildah

```bash
# Create container from image (no daemon)
buildah from docker.io/nginx:latest

# Mount and copy filesystem
buildah mount nginx-working-container
# Mounted at: /var/lib/containers/storage/overlay/.../merged

# Copy files directly
cp -r /var/lib/containers/storage/overlay/.../merged /tmp/rootfs

# Cleanup
buildah umount nginx-working-container
buildah rm nginx-working-container
```

**Pros:**
- Single tool
- No daemon
- Supports rootless mode
- Well documented

**Cons:**
- Requires containers/storage setup
- More complex cleanup
- Storage conflicts if user has podman

---

### Option 3: Buildctl (Buildkit)

```bash
# Buildkit client - download and extract
buildctl build \
  --local dockerfile=. \
  --output type=local,dest=/tmp/rootfs
```

**Pros:**
- Part of moby/buildkit ecosystem
- Supports advanced build features
- Can work standalone

**Cons:**
- Requires buildkitd running
- More oriented toward building, not extracting
- Less straightforward for simple extraction

---

### Option 4: Pure Go Implementation

Use Go libraries directly:
- `github.com/containers/image` - Skopeo's library
- `github.com/opencontainers/image-spec` - OCI spec

```go
import (
    "github.com/containers/image/copy"
    "github.com/containers/image/oci/layout"
    "github.com/containers/image/docker"
)

// Copy image to local OCI layout
src, _ := docker.NewReference("docker://nginx:latest")
dest, _ := layout.NewReference("/tmp/nginx-oci", "latest")
copy.Image(ctx, policy, dest, src, opts)

// Then use umoci or custom layer extraction
```

**Pros:**
- No external tools
- Full control
- Can bundle in binary

**Cons:**
- Complex implementation
- Need to handle layer extraction manually
- More code to maintain

---

## Recommended Approach: Skopeo + Custom Layer Extractor

### Phase 1: Add Skopeo Backend

```go
// pkg/image/preparer.go

func (ip *ImagePreparer) extractWithSkopeo(ctx context.Context, imageRef, destPath string) error {
    // 1. Download image to OCI layout
    ociDir := filepath.Join(destPath, "oci-image")
    cmd := exec.CommandContext(ctx, "skopeo", "copy",
        "--quiet",
        "docker://"+imageRef,
        "oci:"+ociDir)
    
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("skopeo copy failed: %w", err)
    }
    
    // 2. Extract layers from OCI layout
    return ip.extractOCILayers(ociDir, destPath)
}

func (ip *ImagePreparer) extractOCILayers(ociDir, destPath string) error {
    // Read manifest to get layer order
    manifestPath := filepath.Join(ociDir, "index.json")
    // Parse and extract each layer tar.gz
    // Apply whiteouts (delete markers)
    // Result: merged rootfs
}
```

### Phase 2: Add umoci Backend (Optional)

```go
func (ip *ImagePreparer) extractWithUmoci(ctx context.Context, imageRef, destPath string) error {
    // 1. Download with skopeo
    ociDir := filepath.Join(destPath, "oci-image")
    // ... skopeo copy ...
    
    // 2. Unpack with umoci
    cmd := exec.CommandContext(ctx, "umoci", "unpack",
        "--image", "oci:"+ociDir+":latest",
        destPath)
    
    return cmd.Run()
}
```

### Phase 3: Add Buildah Backend

```go
func (ip *ImagePreparer) extractWithBuildah(ctx context.Context, imageRef, destPath string) error {
    // 1. Create working container
    output, err := exec.CommandContext(ctx, "buildah", "from", "--quiet", imageRef).Output()
    if err != nil {
        return err
    }
    containerName := strings.TrimSpace(string(output))
    
    // 2. Mount container
    mountOutput, err := exec.CommandContext(ctx, "buildah", "mount", containerName).Output()
    if err != nil {
        return err
    }
    mountPath := strings.TrimSpace(string(output))
    
    // 3. Copy filesystem
    // cp -r mountPath/* destPath
    
    // 4. Cleanup
    exec.Command("buildah", "umount", containerName).Run()
    exec.Command("buildah", "rm", containerName).Run()
    
    return nil
}
```

---

## Updated Priority Order

| Tool | Priority | Reason |
|------|----------|--------|
| **skopeo** | Primary | Most portable, no daemon, widely available |
| **umoci** | Secondary | Pure OCI, clean extraction |
| **buildah** | Tertiary | Works well on RHEL/Fedora systems |
| **docker/podman** | Fallback | Keep for compatibility |

---

## Implementation Steps

1. **Add skopeo detection and backend**
   - Check `skopeo` availability
   - Implement `extractWithSkopeo()`
   - Implement `extractOCILayers()` for layer merging

2. **Add buildah backend**
   - Check `buildah` availability  
   - Implement `extractWithBuildah()`

3. **Update method priority**
   - Try: skopeo → buildah → docker → podman
   - First available wins

4. **Add layer extraction logic**
   - Parse OCI manifest
   - Extract tar.gz layers
   - Apply whiteout markers
   - Merge into single rootfs

---

## File Changes

```
pkg/image/preparer.go
  - Add extractWithSkopeo()
  - Add extractWithBuildah()
  - Add extractOCILayers() - handle layer merging
  - Update methods[] priority order
  - Add OCI manifest parsing helpers
```

---

## Dependencies to Install

```bash
# Debian/Ubuntu
apt install skopeo

# RHEL/Fedora  
dnf install skopeo buildah

# Arch
pacman -S skopeo buildah

# Go library (optional, for pure Go)
go get github.com/containers/image/v5
```

---

## Testing Plan

1. Test with skopeo only
2. Test with buildah only
3. Test fallback to docker
4. Verify layer extraction handles:
   - Whiteout files (.wh.*)
   - Multiple layers
   - Different base images (Alpine, Debian, Ubuntu)

---

## Timeline Estimate

| Step | Effort |
|------|--------|
| Skopeo backend | 2 hours |
| Layer extraction logic | 3 hours |
| Buildah backend | 1 hour |
| Testing | 2 hours |
| **Total** | ~8 hours |

---

## Next Action

1. Install skopeo: `sudo apt install skopeo`
2. Test manual extraction: `skopeo copy docker://nginx:latest oci:/tmp/test`
3. Implement `extractWithSkopeo()` function

Want me to start implementation?