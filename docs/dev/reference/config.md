# Configuration Package Reference

> `pkg/config/` — YAML Configuration Loading, Validation, and Defaults.

---

## Overview

The `pkg/config` package handles SwarmCracker YAML configuration file parsing, validation, default value application, and environment variable integration.

**Package Structure:**

```
pkg/config/
├── config.go           # Config struct, Load, Validate, Defaults
└───────────────────────┴────────────────────────────────────────────────────
```

---

## Config Struct

**File:** `config.go`

### Top-Level Configuration

```go
type Config struct {
    Executor ExecutorConfig `yaml:"executor"`
    Network  NetworkConfig  `yaml:"network"`
    Logging  LoggingConfig  `yaml:"logging"`
    Images   ImagesConfig   `yaml:"images"`
    Metrics  MetricsConfig  `yaml:"metrics"`
    Snapshot SnapshotConfig `yaml:"snapshot"`

    // Legacy fields for backward compatibility
    KernelPath      string       `yaml:"kernel_path"`
    InitrdPath      string       `yaml:"initrd_path"`
    RootfsDir       string       `yaml:"rootfs_dir"`
    SocketDir       string       `yaml:"socket_dir"`
    DefaultVCPUs    int          `yaml:"default_vcpus"`
    DefaultMemoryMB int          `yaml:"default_memory_mb"`
    EnableJailer    bool         `yaml:"enable_jailer"`
    Jailer          JailerConfig `yaml:"jailer"`
}
```

---

## ExecutorConfig

```go
type ExecutorConfig struct {
    Name            string       `yaml:"name"`
    KernelPath      string       `yaml:"kernel_path"`
    InitrdPath      string       `yaml:"initrd_path"`
    RootfsDir       string       `yaml:"rootfs_dir"`
    SocketDir       string       `yaml:"socket_dir"`
    DefaultVCPUs    int          `yaml:"default_vcpus"`
    DefaultMemoryMB int          `yaml:"default_memory_mb"`
    EnableJailer    bool         `yaml:"enable_jailer"`
    Jailer          JailerConfig `yaml:"jailer"`
    InitSystem      string       `yaml:"init_system"`
    InitGracePeriod int          `yaml:"init_grace_period"`
}
```

### Field Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `name` | string | `"firecracker"` | No | Executor identifier |
| `kernel_path` | string | — | **Yes** | vmlinux path (e.g., `/usr/share/firecracker/vmlinux`) |
| `initrd_path` | string | — | No | Optional initrd image |
| `rootfs_dir` | string | — | **Yes** | Rootfs storage directory |
| `socket_dir` | string | `/var/run/firecracker` | No | API socket directory |
| `default_vcpus` | int | `1` | No | Default VM vCPU count (1-32) |
| `default_memory_mb` | int | `512` | No | Default VM memory (128-8192 MB) |
| `enable_jailer` | bool | `false` | No | Enable jailer isolation |
| `init_system` | string | `"tini"` | No | Init type (`none`, `tini`, `dumb-init`) |
| `init_grace_period` | int | `10` | No | Shutdown grace period (seconds) |

---

## JailerConfig

```go
type JailerConfig struct {
    UID           int    `yaml:"uid"`
    GID           int    `yaml:"gid"`
    ChrootBaseDir string `yaml:"chroot_base_dir"`
    NetNS         string `yaml:"netns"`
}
```

### Field Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `uid` | int | — | **Yes** (if jailer enabled) | UID for Firecracker process |
| `gid` | int | — | **Yes** (if jailer enabled) | GID for Firecracker process |
| `chroot_base_dir` | string | — | **Yes** (if jailer enabled) | Base directory for chroot |
| `netns` | string | — | No | Network namespace path |

---

## NetworkConfig

```go
type NetworkConfig struct {
    BridgeName       string `yaml:"bridge_name"`
    EnableRateLimit  bool   `yaml:"enable_rate_limit"`
    MaxPacketsPerSec int    `yaml:"max_packets_per_sec"`
    Subnet           string `yaml:"subnet"`
    BridgeIP         string `yaml:"bridge_ip"`
    IPMode           string `yaml:"ip_mode"`
    NATEnabled       *bool  `yaml:"nat_enabled"`
}
```

### Field Reference

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `bridge_name` | string | `"swarm-br0"` | **Yes** | Linux bridge name |
| `subnet` | string | `"192.168.127.0/24"` | No | Overlay subnet CIDR |
| `bridge_ip` | string | `"192.168.127.1/24"` | No | Bridge gateway IP |
| `ip_mode` | string | `"static"` | No | IP allocation mode (`static`, `dhcp`) |
| `nat_enabled` | *bool | `true` | No | Enable NAT/masquerading |
| `enable_rate_limit` | bool | `false` | No | Enable packet rate limiting |
| `max_packets_per_sec` | int | `10000` | No | Rate limit threshold |

---

## LoggingConfig

```go
type LoggingConfig struct {
    Level  string `yaml:"level"`
    Format string `yaml:"format"`
    Output string `yaml:"output"`
}
```

### Field Reference

| Field | Type | Default | Valid Values | Description |
|-------|------|---------|--------------|-------------|
| `level` | string | `"info"` | `debug`, `info`, `warn`, `error` | Log verbosity |
| `format` | string | `"json"` | `json`, `text` | Log format |
| `output` | string | `"stdout"` | `stdout`, `stderr`, `<path>` | Output destination |

---

## ImagesConfig

```go
type ImagesConfig struct {
    CacheDir         string `yaml:"cache_dir"`
    MaxCacheSizeMB   int    `yaml:"max_cache_size_mb"`
    EnableLayerCache bool   `yaml:"enable_layer_cache"`
}
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cache_dir` | string | `/var/cache/swarmcracker` | OCI layer cache directory |
| `max_cache_size_mb` | int | `10240` (10GB) | Maximum cache size |
| `enable_layer_cache` | bool | `true` | Enable OCI layer caching |

---

## MetricsConfig

```go
type MetricsConfig struct {
    Enabled bool   `yaml:"enabled"`
    Address string `yaml:"address"`
    Format  string `yaml:"format"`
}
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable Prometheus metrics |
| `address` | string | `"0.0.0.0:9090"` | Metrics server address |
| `format` | string | `"prometheus"` | Export format (`prometheus`, `json`) |

---

## SnapshotConfig

```go
type SnapshotConfig struct {
    Enabled      bool     `yaml:"enabled"`
    SnapshotDir  string   `yaml:"snapshot_dir"`
    MaxSnapshots int      `yaml:"max_snapshots"`
    MaxAge       Duration `yaml:"max_age"`
    AutoSnapshot bool     `yaml:"auto_snapshot"`
    Compress     bool     `yaml:"compress"`
}
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable snapshot support |
| `snapshot_dir` | string | `/var/lib/firecracker/snapshots` | Snapshot storage |
| `max_snapshots` | int | `3` | Max snapshots per VM |
| `max_age` | Duration | `168h` (7 days) | Snapshot retention |
| `auto_snapshot` | bool | `false` | Automatic periodic snapshots |
| `compress` | bool | `false` | Compress snapshots |

---

## Duration Type

Custom YAML duration parser supporting string formats.

```go
type Duration time.Duration
```

### UnmarshalYAML

Accepts:
- String duration: `"24h"`, `"7d"`, `"30m"`
- Numeric (seconds): `3600`

```yaml
max_age: "7d"      # 7 days
max_age: "168h"    # 168 hours
max_age: 604800    # 604800 seconds
```

---

## Loading Functions

### LoadConfig

```go
func LoadConfig(path string) (*Config, error)
```

**Purpose:** Load configuration from YAML file.

**Example:**

```go
cfg, err := config.LoadConfig("/etc/swarmcracker/config.yaml")
if err != nil {
    log.Fatal(err)
}
```

---

### LoadConfigFromEnv

```go
func LoadConfigFromEnv() (*Config, error)
```

**Purpose:** Load from `SWARMCRACKER_CONFIG` env or default path.

**Default Path:** `/etc/swarmcracker/config.yaml`

---

### GetDefaultConfigPath

```go
func GetDefaultConfigPath() string
```

**Purpose:** Get default config path.

---

## Validation

### Validate

```go
func (c *Config) Validate() error
```

**Purpose:** Validate configuration.

**Checks:**
- `executor.kernel_path` required
- `executor.rootfs_dir` required
- `executor.default_vcpus > 0`
- `executor.default_memory_mb > 0`
- `network.bridge_name` required
- `network.max_packets_per_sec > 0` (if rate limiting enabled)
- `jailer` config valid (if jailer enabled)

---

## Defaults

### SetDefaults

```go
func (c *Config) SetDefaults()
```

**Purpose:** Apply default values for empty fields.

**Called automatically by executor on startup.**

---

## Legacy Migration

Legacy flat fields are automatically migrated to nested structure:

```yaml
# Old format (still supported)
kernel_path: "/usr/share/firecracker/vmlinux"
rootfs_dir: "/var/lib/firecracker/rootfs"

# Automatically migrated to:
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SWARMCRACKER_CONFIG` | Config file path override |
| `SWARMCRACKER_KERNEL_PATH` | Kernel path override |
| `SWARMCRACKER_LOG_LEVEL` | Log level override |

---

## Example Configurations

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
  init_system: "tini"
  init_grace_period: 30

network:
  bridge_name: "swarm-br0"
  subnet: "192.168.127.0/24"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"
  nat_enabled: true
  enable_rate_limit: true
  max_packets_per_sec: 10000

logging:
  level: "info"
  format: "json"
  output: "/var/log/swarmcracker/swarmd.log"

images:
  cache_dir: "/var/cache/swarmcracker"
  max_cache_size_mb: 10240
  enable_layer_cache: true

metrics:
  enabled: true
  address: "127.0.0.1:9090"

snapshot:
  enabled: true
  snapshot_dir: "/var/lib/firecracker/snapshots"
  max_snapshots: 3
  max_age: "168h"
  compress: true
```

---

## Testing

```go
func TestConfigValidation() {
    cfg := &Config{
        Executor: ExecutorConfig{
            KernelPath: "/usr/share/firecracker/vmlinux",
            RootfsDir:  "/var/lib/firecracker/rootfs",
        },
        Network: NetworkConfig{
            BridgeName: "swarm-br0",
        },
    }
    
    if err := cfg.Validate(); err != nil {
        t.Errorf("Validation failed: %v", err)
    }
}
```

---

## Related Documentation

| Topic | Document |
|-------|----------|
| User configuration guide | [Configuration Guide](../../user/guides/configuration.md) |
| CLI commands | [CLI Reference](cli.md) |
| Executor config | [SwarmKit Reference](swarmkit.md) |