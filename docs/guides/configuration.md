# Configuration Reference

Complete reference for SwarmCracker configuration options.

## Configuration File Location

SwarmCracker looks for configuration in this order:

1. `--config` flag value
2. `SWARMCRACKER_CONFIG` environment variable
3. `/etc/swarmcracker/config.yaml` (default)

## Configuration Structure

```yaml
# Executor configuration
executor:
  name: string              # Executor name (default: "firecracker")
  kernel_path: string       # Path to Firecracker kernel (required)
  initrd_path: string       # Path to initrd (optional)
  rootfs_dir: string        # Directory for root filesystems (required)
  socket_dir: string        # Directory for API sockets (default: "/var/run/firecracker")
  default_vcpus: int        # Default vCPUs per VM (default: 1)
  default_memory_mb: int    # Default memory in MB (default: 512)
  enable_jailer: bool       # Enable jailer for isolation (default: false)
  jailer:                   # Jailer configuration (if enabled)
    uid: int               # UID to run Firecracker as (required if jailer enabled)
    gid: int               # GID to run Firecracker as (required if jailer enabled)
    chroot_base_dir: string # Jailer chroot directory (required if jailer enabled)
    netns: string          # Network namespace path (optional)

# Network configuration
network:
  bridge_name: string         # Bridge name for VM networking (default: "swarm-br0")
  enable_rate_limit: bool     # Enable rate limiting (default: false)
  max_packets_per_sec: int    # Max packets per second (default: 10000)

# Logging configuration
logging:
  level: string    # Log level: debug, info, warn, error (default: "info")
  format: string   # Log format: json, text (default: "json")
  output: string   # Log output: stdout, stderr, /path/to/file (default: "stdout")

# Image preparation configuration
images:
  cache_dir: string          # Directory for image cache (default: "/var/cache/swarmcracker")
  max_cache_size_mb: int     # Max cache size in MB (default: 10240)
  enable_layer_cache: bool   # Enable layer caching (default: true)

# Metrics configuration
metrics:
  enabled: bool    # Enable metrics endpoint (default: false)
  address: string  # Metrics server address (default: "0.0.0.0:9090")
  format: string   # Metrics format: prometheus, json (default: "prometheus")
```

## Executor Configuration

### executor.name

Executor identifier for logging and metrics.

```yaml
executor:
  name: "firecracker"
```

**Type:** string
**Default:** `firecracker`
**Required:** No

### executor.kernel_path

Path to the Firecracker kernel image (vmlinux).

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
```

**Type:** string
**Default:** none
**Required:** Yes

**Notes:**
- Must be an uncompressed Linux kernel in ELF format
- Kernel must be configured for Firecracker
- See [Firecracker kernel guide](https://github.com/firecracker-microvm/firecracker/blob/main/docs/kernel-policy.md) for requirements

### executor.initrd_path

Optional initrd image for early boot.

```yaml
executor:
  initrd_path: "/usr/share/firecracker/initrd.img"
```

**Type:** string
**Default:** none
**Required:** No

**Notes:**
- Optional for most use cases
- Useful for custom init systems or debugging

### executor.rootfs_dir

Directory where converted container root filesystems are stored.

```yaml
executor:
  rootfs_dir: "/var/lib/firecracker/rootfs"
```

**Type:** string
**Default:** none
**Required:** Yes

**Notes:**
- Must exist and be writable
- Contains ext4 images: `<image-name>.ext4`
- Can be on fast storage (SSD) for better performance

### executor.socket_dir

Directory for Firecracker API Unix sockets.

```yaml
executor:
  socket_dir: "/var/run/firecracker"
```

**Type:** string
**Default:** `/var/run/firecracker`
**Required:** No

**Notes:**
- One socket per VM: `<task-id>.sock`
- Cleaned up on VM removal

### executor.default_vcpus

Default number of vCPUs per VM if not specified in task.

```yaml
executor:
  default_vcpus: 2
```

**Type:** integer
**Default:** `1`
**Required:** No
**Valid range:** 1 - 32

**Notes:**
- Can be overridden per task via resource limits
- Firecracker supports up to 32 vCPUs

### executor.default_memory_mb

Default memory per VM in MB if not specified in task.

```yaml
executor:
  default_memory_mb: 1024
```

**Type:** integer
**Default:** `512`
**Required:** No
**Valid range:** 128 - 8192

**Notes:**
- Can be overridden per task via resource limits
- Min 128MB, max depends on host memory

### executor.enable_jailer

Enable Firecracker jailer for additional security isolation.

```yaml
executor:
  enable_jailer: true
```

**Type:** boolean
**Default:** `false`
**Required:** No

**Notes:**
- Runs Firecracker as unprivileged user
- Adds chroot isolation
- Recommended for production

### executor.jailer

Jailer-specific configuration when `enable_jailer: true`.

#### executor.jailer.uid

UID to run Firecracker process as.

```yaml
executor:
  jailer:
    uid: 1000
```

**Type:** integer
**Default:** none
**Required:** Yes (if jailer enabled)

#### executor.jailer.gid

GID to run Firecracker process as.

```yaml
executor:
  jailer:
    gid: 1000
```

**Type:** integer
**Default:** none
**Required:** Yes (if jailer enabled)

#### executor.jailer.chroot_base_dir

Base directory for jailer chroot environments.

```yaml
executor:
  jailer:
    chroot_base_dir: "/srv/jailer"
```

**Type:** string
**Default:** none
**Required:** Yes (if jailer enabled)

**Notes:**
- Creates subdirectory per VM: `/srv/jailer/firecracker/<vm-id>/`
- Must be writable by the configured UID/GID

#### executor.jailer.netns

Network namespace path to join.

```yaml
executor:
  jailer:
    netns: "/var/run/netns/firecracker"
```

**Type:** string
**Default:** none
**Required:** No

**Notes:**
- Optional, for advanced networking
- See `man ip-netns` for details

## Network Configuration

### network.bridge_name

Name of the Linux bridge for VM networking.

```yaml
network:
  bridge_name: "swarm-br0"
```

**Type:** string
**Default:** `swarm-br0`
**Required:** Yes

**Notes:**
- Bridge must exist before starting VMs
- See [installation.md](installation.md) for bridge setup

### network.enable_rate_limit

Enable packet rate limiting on TAP devices.

```yaml
network:
  enable_rate_limit: true
```

**Type:** boolean
**Default:** `false`
**Required:** No

**Notes:**
- Prevents VM flood attacks
- Uses Linux traffic control (tc)

### network.max_packets_per_sec

Maximum packets per second per TAP device.

```yaml
network:
  max_packets_per_sec: 10000
```

**Type:** integer
**Default:** `10000`
**Required:** No

**Notes:**
- Only applies if `enable_rate_limit: true`
- Typical values: 1000 - 10000

## Logging Configuration

### logging.level

Log verbosity level.

```yaml
logging:
  level: "info"
```

**Type:** string
**Default:** `info`
**Valid values:** `debug`, `info`, `warn`, `error`

### logging.format

Log output format.

```yaml
logging:
  format: "json"
```

**Type:** string
**Default:** `json`
**Valid values:** `json`, `text`

**Notes:**
- `json` for structured logging
- `text` for human-readable logs

### logging.output

Log output destination.

```yaml
logging:
  output: "stdout"
```

**Type:** string
**Default:** `stdout`
**Valid values:** `stdout`, `stderr`, or file path

## Images Configuration

### images.cache_dir

Directory for caching prepared images.

```yaml
images:
  cache_dir: "/var/cache/swarmcracker"
```

**Type:** string
**Default:** `/var/cache/swarmcracker`
**Required:** No

**Notes:**
- Stores extracted OCI filesystems
- Can be cleaned with `swarmcracker cache cleanup`

### images.max_cache_size_mb

Maximum cache size in megabytes.

```yaml
images:
  max_cache_size_mb: 10240
```

**Type:** integer
**Default:** `10240` (10GB)
**Required:** No

**Notes:**
- LRU eviction when limit exceeded
- Set to 0 for unlimited

### images.enable_layer_cache

Enable OCI layer caching.

```yaml
images:
  enable_layer_cache: true
```

**Type:** boolean
**Default:** `true`
**Required:** No

**Notes:**
- Speeds up image preparation
- Uses more disk space

## Metrics Configuration

### metrics.enabled

Enable Prometheus metrics endpoint.

```yaml
metrics:
  enabled: true
```

**Type:** boolean
**Default:** `false`
**Required:** No

### metrics.address

Metrics server listen address.

```yaml
metrics:
  address: "0.0.0.0:9090"
```

**Type:** string
**Default:** `0.0.0.0:9090`
**Required:** No

**Notes:**
- Format: `host:port`
- Use `127.0.0.1` for local only

### metrics.format

Metrics export format.

```yaml
metrics:
  format: "prometheus"
```

**Type:** string
**Default:** `prometheus`
**Valid values:** `prometheus`, `json`

## Environment Variables

Configuration can be overridden via environment variables:

```bash
# Override config file location
export SWARMCRACKER_CONFIG=/custom/path/config.yaml

# Override kernel path
export SWARMCRACKER_KERNEL_PATH=/custom/kernel

# Override log level
export SWARMCRACKER_LOG_LEVEL=debug
```

## Task Overrides

Task specifications can override executor defaults:

```yaml
# In SwarmKit service spec
resources:
  limits:
    nano_cpus: 2000000000    # 2 vCPUs
    memory_bytes: 1073741824 # 1GB
```

## Examples

### Minimal Configuration

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"

network:
  bridge_name: "swarm-br0"
```

### Production Configuration

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  socket_dir: "/var/run/firecracker"
  default_vcpus: 2
  default_memory_mb: 2048
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: "/srv/jailer"

network:
  bridge_name: "swarm-br0"
  enable_rate_limit: true
  max_packets_per_sec: 10000

logging:
  level: "info"
  format: "json"

images:
  cache_dir: "/var/cache/swarmcracker"
  max_cache_size_mb: 10240
  enable_layer_cache: true

metrics:
  enabled: true
  address: "0.0.0.0:9090"
```

### Development Configuration

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/tmp/firecracker/rootfs"
  socket_dir: "/tmp/firecracker/sockets"
  default_vcpus: 1
  default_memory_mb: 512
  enable_jailer: false

network:
  bridge_name: "test-br0"

logging:
  level: "debug"
  format: "text"

images:
  cache_dir: "/tmp/firecracker/cache"
  enable_layer_cache: false

metrics:
  enabled: false
```

## Validation

Configuration is validated on startup:

```bash
# Test configuration
swarmcracker --config /etc/swarmcracker/config.yaml --validate

# Expected output if valid:
# ✓ Configuration is valid

# Expected output if invalid:
# ✗ Configuration validation failed:
#   - executor.kernel_path is required
#   - network.bridge_name is required
```

## Migration from Legacy Config

Old flat config structure is automatically migrated:

```yaml
# Old format (still supported)
kernel_path: "/kernel"
rootfs_dir: "/rootfs"

# Automatically migrated to:
executor:
  kernel_path: "/kernel"
  rootfs_dir: "/rootfs"
```

**Recommendation:** Update to nested structure for new features.
