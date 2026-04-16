# Research: OCI Image Extraction SDKs for Go

## Summary

**Recommended: `github.com/google/go-containerregistry` (go-containerregistry / ggcr)**

- Pure Go (NO C dependencies)
- NO daemon required
- Direct registry pull
- Built-in layer extraction with `mutate.Extract()`
- Handles whiteouts automatically
- Well-maintained by Google
- Works with all registries (docker.io, quay.io, ghcr.io, etc.)

---

## Tested Results

```bash
$ go run extract.go
✅ SUCCESS! Image pulled without daemon
Layers count: 7

Extracting nginx:latest to ./rootfs...
✅ Done! Files extracted to ./rootfs
Top-level dirs: [bin boot dev docker-entrypoint.d docker-entrypoint.sh etc ...]
```

---

## SDK Comparison

| Library | Pure Go | No Daemon | Layer Extract | Whiteouts | C Deps | Recommendation |
|---------|---------|-----------|---------------|-----------|--------|----------------|
| **go-containerregistry** | ✅ | ✅ | ✅ `mutate.Extract()` | ✅ Auto | ❌ None | **BEST CHOICE** |
| containers/image/v5 | ❌ CGO | ✅ | ✅ Manual | ✅ Manual | ✅ gpgme,btrfs | Good but C deps |
| containers/storage | ❌ CGO | ✅ | ✅ | ✅ | ✅ Many | For storage only |
| regclient/regclient | ✅ | ✅ | ✅ | ✅ Manual | ❌ None | Good alternative |
| docker/docker | ❌ CGO | ❌ Needs daemon | ✅ | ✅ | ✅ Many | Daemon required |

---

## go-containerregistry Key Packages

### Pull Image from Registry
```go
import (
    "github.com/google/go-containerregistry/pkg/name"
    "github.com/google/go-containerregistry/pkg/v1/remote"
)

ref, _ := name.ParseReference("nginx:latest")
img, _ := remote.Image(ref)
```

### Extract Flattened Rootfs
```go
import "github.com/google/go-containerregistry/pkg/v1/mutate"

// Flattens all layers into single tar stream
fs := mutate.Extract(img)
defer fs.Close()

// Read as tar and extract files
tr := tar.NewReader(fs)
for {
    header, err := tr.Next()
    if err == io.EOF { break }
    // Extract to filesystem...
}
```

### Write to Tarball
```go
import "github.com/google/go-containerregistry/pkg/v1/tarball"

tarball.Write(ref, img, "/tmp/nginx.tar")
```

### Get Layer Info
```go
layers, _ := img.Layers()
for _, layer := range layers {
    digest, _ := layer.Digest()
    size, _ := layer.Size()
    fmt.Println(digest, size)
}
```

### Get Manifest/Config
```go
manifest, _ := img.Manifest()
config, _ := img.ConfigFile()

fmt.Println("Arch:", config.Architecture)
fmt.Println("Entrypoint:", config.Config.Entrypoint)
fmt.Println("Env:", config.Config.Env)
```

---

## Implementation Plan for SwarmCracker

### Step 1: Add Dependency
```bash
go get github.com/google/go-containerregistry/pkg/name@latest
go get github.com/google/go-containerregistry/pkg/v1/remote@latest
go get github.com/google/go-containerregistry/pkg/v1/mutate@latest
go get github.com/google/go-containerregistry/pkg/v1@latest
```

### Step 2: Implement Extraction Function
```go
func (ip *ImagePreparer) extractWithGGCR(ctx context.Context, imageRef, destPath string) error {
    // Parse reference
    ref, err := name.ParseReference(imageRef)
    if err != nil {
        return fmt.Errorf("failed to parse reference: %w", err)
    }
    
    // Pull image from registry (no daemon!)
    img, err := remote.Image(ref, remote.WithContext(ctx))
    if err != nil {
        return fmt.Errorf("failed to pull image: %w", err)
    }
    
    // Extract flattened filesystem
    fs := mutate.Extract(img)
    defer fs.Close()
    
    // Extract tar stream to destPath
    return extractTarStream(fs, destPath)
}

func extractTarStream(r io.Reader, dest string) error {
    tr := tar.NewReader(r)
    for {
        header, err := tr.Next()
        if err == io.EOF { break }
        if err != nil { return err }
        
        target := filepath.Join(dest, header.Name)
        
        // Handle whiteouts (already done by mutate.Extract!)
        // But still need to handle regular files
        
        switch header.Typeflag {
        case tar.TypeDir:
            os.MkdirAll(target, os.FileMode(header.Mode))
        case tar.TypeReg:
            os.MkdirAll(filepath.Dir(target), 0755)
            f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
            if err != nil { return err }
            io.Copy(f, tr)
            f.Close()
        case tar.TypeSymlink:
            os.Symlink(header.Linkname, target)
        case tar.TypeLink:
            // Hard link - copy content
            src := filepath.Join(dest, header.Linkname)
            os.Link(src, target)
        }
    }
    return nil
}
```

### Step 3: Update Priority Order
```go
methods := []struct{...}{
    {name: "ggcr", fn: ip.extractWithGGCR},      // Primary: Pure Go, no deps
    {name: "skopeo", fn: ip.extractWithSkopeo},  // Fallback: CLI
    {name: "buildah", fn: ip.extractWithBuildah},// Fallback: CLI
    {name: "docker", fn: ip.extractWithDocker},  // Legacy: daemon
    {name: "podman", fn: ip.extractWithDocker},  // Legacy: daemon
}
```

---

## Advantages of go-containerregistry

1. **Self-contained**: No external tools or daemons needed
2. **Pure Go**: No C dependencies, builds anywhere
3. **Efficient**: Streams data, doesn't require temp storage
4. **Complete**: Handles all OCI features (manifests, configs, layers)
5. **Whiteout handling**: `mutate.Extract()` handles `.wh.*` markers automatically
6. **Multi-arch**: Supports platform selection
7. **Auth**: Supports registry authentication (docker config, bearer tokens)
8. **Well-documented**: Active development, good docs

---

## Go Version Requirement

go-containerregistry v0.21.5 requires **Go 1.25+** (currently using Go 1.24)

Need to upgrade or use older version:
```bash
# Use older version for Go 1.24 compatibility
go get github.com/google/go-containerregistry@v0.19.0  # Compatible with Go 1.22+
```

---

## Alternative: regclient

If Go version issue persists, regclient is another pure Go option:

```go
import "github.com/regclient/regclient"

rc := regclient.NewRegClient()
ref, _ := ref.NewRef("docker://nginx:latest")
img, _ := rc.ImageGet(ctx, ref)

// Similar extraction workflow
```

---

## Decision

**Use go-containerregistry** for SwarmCracker image preparation:

1. Remove docker/podman dependency completely
2. Remove containers/image C dependency 
3. Pure Go, cross-platform builds
4. Single binary deployment

---

*Research completed 2026-04-15*