# Multi-Architecture Support

SwarmCracker supports **multi-architecture deployments** on both **x86_64 (amd64)** and **ARM64 (aarch64)** platforms, enabling heterogeneous clusters with mixed hardware.

## Supported Architectures

| Architecture | Platform | Status |
|-------------|----------|--------|
| **amd64** | x86_64 (Intel/AMD) | ✅ Fully Supported |
| **arm64** | AArch64 (Apple Silicon, ARM servers) | ✅ Fully Supported |
| **others** | x86, ARMv7, etc. | ❌ Not Supported (Firecracker limitation) |

## How It Works

### 1. Platform Detection

When a SwarmCracker worker starts, it reports its platform to the SwarmKit manager:

```go
NodeDescription{
    Platform: &api.Platform{
        Architecture: runtime.GOARCH,  // "amd64" or "arm64"
        OS:           runtime.GOOS,    // "linux"
    },
    Resources: &api.Resources{...},
}
```

### 2. Image Pulling

SwarmCracker uses containerd/Docker to pull images. These tools automatically handle **multi-arch manifest lists**:

```bash
# Pull nginx (automatically selects correct arch)
docker pull nginx:latest

# On amd64 host → pulls nginx:latest@sha256:... (amd64 variant)
# On arm64 host → pulls nginx:latest@sha256:... (arm64 variant)
```

### 3. Placement Constraints

SwarmKit scheduler uses platform information to place tasks on compatible nodes:

```go
// Service spec with platform constraint
service.Spec.Task.Placement = &api.Placement{
    Constraints: []api.Constraint{
        {
            Field: api.ConstraintNodePlatform,
            Op:    api.ConstraintOpEq,
            Value: "linux/amd64",
        },
    },
}
```

## Architecture Validation

SwarmCracker validates architecture at multiple levels:

### Executor Level

```go
func (e *Executor) archSupported() bool {
    switch runtime.GOARCH {
    case "amd64", "arm64":
        return true
    default:
        return false
    }
}
```

If architecture is unsupported:
- Firecracker resource is **not reported** to the manager
- Node marked as **unavailable** for Firecracker workloads
- Warning logged with architecture details

### Image Preparation Level

```go
func (ip *ImagePreparer) validateArchitecture() error {
    switch runtime.GOARCH {
    case "amd64", "arm64":
        return nil
    default:
        return fmt.Errorf("unsupported architecture: %s", runtime.GOARCH)
    }
}
```

If validation fails:
- Image preparation **aborts**
- Task marked as **FAILED**
- Error message includes architecture info

## Mixed-Architecture Clusters

SwarmCracker supports clusters with **both amd64 and arm64 workers**:

```
SwarmKit Manager (amd64)
├── Worker-1 (amd64) — runs x86_64 VMs
├── Worker-2 (arm64) — runs ARM64 VMs
└── Worker-3 (amd64) — runs x86_64 VMs
```

### Service Placement

**Option 1: Architecture-agnostic (default)**
```bash
# Service runs on any available worker
swarmctl service create --name web --image nginx:latest --replicas 3
```

SwarmKit automatically places tasks on compatible nodes based on image architecture.

**Option 2: Architecture-specific**
```bash
# Force amd64 only
swarmctl service create --name web \
  --constraint "node.platform.architecture == amd64" \
  --image nginx:latest \
  --replicas 3

# Force arm64 only
swarmctl service create --name web \
  --constraint "node.platform.architecture == arm64" \
  --image nginx:latest \
  --replicas 3
```

## Building Multi-Arch Images

To create images that work on both architectures:

### Using Docker Buildx

```bash
# Enable buildx
docker buildx create --use

# Build and push multi-arch image
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t myapp:latest \
  --push \
  .
```

### Manifest List

Docker automatically creates a **manifest list** that references both architectures:

```bash
# Inspect manifest
docker manifest inspect myapp:latest

# Output shows both amd64 and arm64 variants
{
  "manifests": [
    {
      "platform": {"architecture": "amd64", "os": "linux"},
      "digest": "sha256:..."
    },
    {
      "platform": {"architecture": "arm64", "os": "linux"},
      "digest": "sha256:..."
    }
  ]
}
```

## Firecracker on ARM64

Firecracker supports ARM64 with some considerations:

### Requirements

1. **KVM support** — `/dev/kvm` must be available
2. **ARM64 kernel** — Use ARM64-specific kernel image
3. **ARM64 rootfs** — Root filesystem must be ARM64-compatible

### Kernel Configuration

ARM64 kernels need specific config options:

```
CONFIG_KVM=y
CONFIG_KVM_ARM_HOST=y
CONFIG_VIRTIO=y
CONFIG_VIRTIO_NET=y
CONFIG_VIRTIO_BLK=y
```

### Download ARM64 Kernel

```bash
# Official Firecracker ARM64 kernel
wget https://s3.amazonaws.com/spec.ccfc.min/img/arm64/kernel/vmlinux.bin

# Or build from source
git clone https://github.com/firecracker-microvm/firecracker
cd firecracker/tools/create_snapshot_artifact/
./create_artifacts.sh arm64
```

## Testing Multi-Arch Support

### Verify Node Platform

```bash
# Check node description
swarmctl node inspect <node-id>

# Look for Platform section
Platform:
  Architecture: arm64
  OS: linux
```

### Test Image Pull

```bash
# Pull multi-arch image
swarmcracker run --test nginx:latest

# Verify correct architecture pulled
docker inspect nginx:latest | grep Architecture
# Output: "Architecture": "arm64" (on ARM host)
```

### Deploy Service

```bash
# Create service
swarmctl service create \
  --name test-multi-arch \
  --image nginx:latest \
  --replicas 2

# Verify tasks scheduled correctly
swarmctl service tasks test-multi-arch

# Check which nodes are running tasks
swarmctl node ls
```

## Troubleshooting

### Issue: "unsupported architecture" Error

**Symptom**: Task fails immediately with architecture error

**Cause**: Running on unsupported architecture (e.g., x86, ARMv7)

**Fix**:
```bash
# Check current architecture
uname -m
# amd64 → x86_64
# aarch64 → ARM64

# Verify Firecracker support
firecracker --version
# Firecracker v1.x.x supports x86_64 and aarch64 only
```

### Issue: Wrong Image Architecture Pulled

**Symptom**: VM fails to start, binary format error

**Cause**: Image doesn't have multi-arch manifest, wrong variant pulled

**Fix**:
```bash
# Check image manifest
docker manifest inspect nginx:latest

# Force specific architecture
docker pull --platform linux/arm64 nginx:latest

# Or use architecture-specific tag
docker pull nginx:latest-arm64
```

### Issue: KVM Not Available on ARM

**Symptom**: Firecracker resource not reported

**Cause**: KVM not enabled or missing kernel modules

**Fix**:
```bash
# Check KVM availability
ls -la /dev/kvm
kvm-ok  # From cpu-checker package

# Load KVM module
sudo modprobe kvm
sudo modprobe kvm-arm  # On ARM systems

# Check kernel support
zcat /proc/config.gz | grep CONFIG_KVM
```

### Issue: Mixed-Arch Cluster Scheduling

**Symptom**: Tasks stuck in PENDING state

**Cause**: No compatible nodes available for image architecture

**Fix**:
```bash
# Check node platforms
swarmctl node ls --format json | jq '.[].Description.Platform'

# Check service constraints
swarmctl service inspect <service-id>

# Remove architecture constraints if not needed
swarmctl service update <service> --constraint-rm "node.platform.architecture == amd64"
```

## Implementation Details

### Platform Reporting Flow

```
swarmd-firecracker starts
    ↓
Executor.Describe() called
    ↓
NodeDescription.Platform set
    ↓
Reported to SwarmKit manager
    ↓
Manager stores in Node object
    ↓
Scheduler uses for placement decisions
```

### Image Pull Flow

```
Task received with image "nginx:latest"
    ↓
ImagePreparer.Prepare() called
    ↓
validateArchitecture() checks host arch
    ↓
extractOCIImage() pulls via containerd/Docker
    ↓
Manifest list resolved for correct arch
    ↓
Rootfs extracted (correct arch binaries)
    ↓
VM started with correct-arch rootfs
```

## Best Practices

### 1. Use Multi-Arch Images

Always prefer images with multi-arch manifests:
```bash
# Good: Official images with multi-arch support
nginx:latest
alpine:latest
golang:1.21

# Check if multi-arch
docker manifest inspect nginx:latest
```

### 2. Label Your Nodes

Add architecture labels for easier scheduling:
```bash
swarmctl node update <node-id> \
  --label-add "arch=$(uname -m)" \
  --label-add "platform=$(uname -s)/$(uname -m)"
```

### 3. Test on Both Architectures

Before deploying to production:
```bash
# Test on amd64
swarmcracker run --test nginx:latest

# Test on arm64
swarmcracker run --test nginx:latest

# Verify both work correctly
```

### 4. Monitor Architecture Distribution

```bash
# Count nodes by architecture
swarmctl node ls --format json | \
  jq 'group_by(.Description.Platform.Architecture) | 
      map({arch: .[0].Description.Platform.Architecture, count: length})'
```

### 5. Document Architecture Requirements

In your service specs:
```go
service.Annotations["architecture"] = "amd64"  // or "arm64" or "any"
service.Annotations["multi-arch-ready"] = "true"
```

## See Also

- [SwarmKit Placement Constraints](../architecture/swarmkit-integration.md#placement)
- [Firecracker ARM64 Support](https://github.com/firecracker-microvm/firecracker/blob/main/docs/arm64-support.md)
- [Docker Multi-Arch Builds](https://docs.docker.com/build/building/multi-platform/)
- [Container Image Manifests](https://github.com/opencontainers/image-spec/blob/main/image-index.md)
