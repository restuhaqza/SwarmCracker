# Advanced Topics

> Rolling updates, multi-arch, init systems, build from source.

---

## Rolling Updates

SwarmCracker supports zero-downtime rolling updates via SwarmKit orchestration.

### How It Works

1. Manager creates new task with updated spec
2. SwarmCracker starts new Firecracker VM
3. VM reports RUNNING status
4. Manager waits for Monitor period (default: 5s)
5. Manager stops old task
6. Executor removes old VM

### Update a Service

```bash
# Update image (triggers rolling update)
swarmctl update svc-nginx --image nginx:1.25

# Update with environment
swarmctl update svc-app --env LOG_LEVEL=debug
```

### Configuration

SwarmKit controls update behavior (not SwarmCracker):

| Parameter | Default | Description |
|-----------|---------|-------------|
| Parallelism | 1 | Tasks updated simultaneously |
| Delay | 0s | Wait between batches |
| Monitor | 5s | Verify task stability |
| Failure Action | pause | On failure: pause/continue/rollback |

---

## Multi-Architecture Support

SwarmCracker supports multiple CPU architectures via placement constraints.

### Supported Architectures

| Arch | Firecracker Support | Notes |
|------|---------------------|-------|
| x86_64 | ✅ Full support | Primary target |
| arm64 | ✅ Experimental | AWS Graviton, Ampere |

### Architecture Constraints

```bash
# Create service constrained to x86_64 nodes
swarmctl create-service nginx:latest --constraint arch==x86_64

# Create service constrained to arm64 nodes
swarmctl create-service arm-app:latest --constraint arch==arm64
```

### Multi-Arch Images

Use OCI image indexes for cross-arch compatibility:

```bash
# Build multi-arch image
docker buildx build --platform linux/amd64,linux/arm64 -t myapp:latest .

# SwarmCracker pulls correct variant based on node arch
```

---

## Init Systems

MicroVMs need an init system for proper process management.

### Why Init Matters

- **Zombie reaping** — Orphan processes must be reaped
- **Signal handling** — Forward SIGTERM to children
- **Process supervision** — Restart failed processes

### Supported Init Systems

| Init | Size | Features |
|------|------|----------|
| **tini** | ~20KB | Minimal, Docker default |
| **dumb-init** | ~30KB | Signal proxy, lightweight |
| **s6** | ~100KB | Process supervision |
| **systemd** | Large | Full service management |

### Configure Init

```yaml
executor:
  init_process: "/sbin/tini"
  init_process_args: ["--", "/bin/sh"]
```

### Rootfs with Init

```bash
# Install tini in rootfs
curl -fsSL https://github.com/krallin/tini/releases/download/v0.19.0/tini-static -o rootfs/sbin/tini
chmod +x rootfs/sbin/tini
```

---

## Build from Source

### Prerequisites

- Go 1.21+
- Git
- Make

### Clone and Build

```bash
# Clone repository
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker

# Build binaries
make build

# Output:
# bin/swarmcracker
# bin/swarmctl

# Install
sudo make install
```

### Build Targets

```bash
make build         # Build all binaries
make test          # Run tests
make lint          # Run linter
make clean         # Clean build artifacts
make install       # Install to /usr/local/bin
make uninstall     # Remove binaries
```

### Development Build

```bash
# Build with debug symbols
make build-debug

# Run locally
./bin/swarmcracker run --config config.yaml
```

---

## systemd Service

Run SwarmCracker as a systemd service.

### Service File

```bash
sudo tee /etc/systemd/system/swarmcracker.service <<EOF
[Unit]
Description=SwarmCracker Firecracker Executor
After=network.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/swarmcracker run --config /etc/swarmcracker/config.yaml
Restart=always
RestartSec=5
TimeoutStartSec=180

[Install]
WantedBy=multi-user.target
EOF
```

### Enable Service

```bash
sudo systemctl daemon-reload
sudo systemctl enable swarmcracker
sudo systemctl start swarmcracker

# Check status
sudo systemctl status swarmcracker
```

### Logs

```bash
journalctl -u swarmcracker -f
```

---

## File Management

Manage rootfs and kernel images.

### Rootfs Directory

```yaml
executor:
  rootfs_dir: "/var/lib/swarmcracker/rootfs"
```

### Kernel Management

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
```

### Image Storage

```bash
/var/lib/swarmcracker/
├── rootfs/
│   ├── nginx-rootfs.ext4
│   ├── redis-rootfs.ext4
├── kernels/
│   ├── vmlinux-6.1.155
│   ├── vmlinux-5.10
├── config.yaml
```

---

## Troubleshooting

### Rolling Update Stuck

```bash
# Check task status
swarmctl ls-tasks

# Check node availability
swarmctl ls-nodes

# Force rollback if needed
swarmctl update svc-nginx --rollback
```

### Init Process Missing

```bash
# Check init in rootfs
ls rootfs/sbin/tini

# Verify init binary
file rootfs/sbin/tini
```

### Build Fails

```bash
# Check Go version
go version  # Must be 1.21+

# Check dependencies
go mod download

# Run lint for errors
make lint
```

---

**See Also:** [Configuration](configuration.md) | [Development Guide](../development/)