# SwarmCracker CLI Quick Reference

## Installation

```bash
# Build
make all

# The binary will be at: ./build/swarmcracker-kit
```

## Commands Overview

### 1. Help
```bash
swarmcracker-kit --help
swarmcracker-kit run --help
swarmcracker-kit validate --help
```

### 2. Version
```bash
swarmcracker-kit version
```

### 3. Validate Configuration
```bash
# Validate default config
swarmcracker-kit validate

# Validate specific config
swarmcracker-kit validate --config /path/to/config.yaml
```

### 4. Run Containers

#### Basic Usage
```bash
# Run with defaults (1 vCPU, 512MB RAM)
swarmcracker-kit run nginx:latest

# Run with custom resources
swarmcracker-kit run --vcpus 2 --memory 1024 nginx:latest

# Run in background (detached)
swarmcracker-kit run --detach nginx:latest

# Run with environment variables
swarmcracker-kit run -e APP_ENV=prod -e DEBUG=false nginx:latest

# Test mode (validate without executing)
swarmcracker-kit run --test nginx:latest
```

#### With Custom Config
```bash
# Use specific config file
swarmcracker-kit run --config /etc/swarmcracker/config.yaml nginx:latest

# Override kernel path
swarmcracker-kit run --kernel /custom/path/vmlinux nginx:latest

# Override rootfs directory
swarmcracker-kit run --rootfs-dir /custom/rootfs nginx:latest
```

#### Debug Mode
```bash
# Enable debug logging
swarmcracker-kit --log-level debug run nginx:latest
```

## Global Flags

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--config` | `-c` | Path to config file | `/etc/swarmcracker/config.yaml` |
| `--log-level` | | Log level | `info` |
| `--kernel` | | Override kernel path | (from config) |
| `--rootfs-dir` | | Override rootfs dir | (from config) |

## Run Command Flags

| Flag | Short | Description | Default |
|------|-------|-------------|---------|
| `--detach` | `-d` | Run in detached mode | `false` |
| `--vcpus` | | Number of vCPUs | `1` |
| `--memory` | | Memory in MB | `512` |
| `--env` | `-e` | Environment variables | `[]` |
| `--test` | | Test mode only | `false` |

## Configuration File

Example at `config.example.yaml`:

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 1
  default_memory_mb: 512

network:
  bridge_name: "swarm-br0"

logging:
  level: "info"
  format: "text"
```

## Common Workflows

### 1. First Time Setup
```bash
# 1. Create config from example
cp config.example.yaml /etc/swarmcracker/config.yaml

# 2. Edit with your paths
vim /etc/swarmcracker/config.yaml

# 3. Validate configuration
swarmcracker-kit validate

# 4. Test with a simple image
swarmcracker-kit run --test nginx:latest
```

### 2. Development Workflow
```bash
# Build after changes
make all

# Run tests
./test-cli.sh

# Test new image
swarmcracker-kit run --test alpine:latest
```

### 3. Production Workflow
```bash
# Validate config
swarmcracker-kit validate --config /etc/swarmcracker/config.yaml

# Run service with resources
swarmcracker-kit run \
  --vcpus 2 \
  --memory 1024 \
  -e APP_ENV=production \
  --detach \
  myapp:latest
```

## Troubleshooting

### Config File Not Found
```bash
# Error: failed to read config file: no such file or directory
# Solution: Specify config file path or create default config
swarmcracker-kit --config /path/to/config.yaml run nginx:latest
```

### Kernel Not Found
```bash
# Error: kernel_path is required
# Solution: Override kernel path or fix config
swarmcracker-kit run --kernel /usr/share/firecracker/vmlinux nginx:latest
```

### Test Mode Recommended
```bash
# Use --test flag to validate without running actual VMs
swarmcracker-kit run --test nginx:latest
```

## Exit Codes

- `0`: Success
- `1`: Error (configuration, validation, or execution failure)

## Signal Handling

The CLI handles `SIGINT` (Ctrl+C) and `SIGTERM` gracefully:
- Stops running VMs
- Cleans up resources
- Exits with proper code

## Examples by Use Case

### Web Server
```bash
swarmcracker-kit run \
  --vcpus 2 \
  --memory 512 \
  -e PORT=8080 \
  nginx:latest
```

### Database
```bash
swarmcracker-kit run \
  --vcpus 4 \
  --memory 2048 \
  -e POSTGRES_PASSWORD=secret \
  postgres:15
```

### Development
```bash
swarmcracker-kit run \
  --vcpus 1 \
  --memory 256 \
  -e NODE_ENV=development \
  node:18
```

## Additional Resources

- Full Documentation: `docs/INSTALL.md`
- Configuration: `docs/CONFIG.md`
- Testing Guide: `docs/TESTING.md`
- Implementation: `CLI_IMPLEMENTATION_SUMMARY.md`
