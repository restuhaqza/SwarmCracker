---
title: "Advanced Topics"
description: "Rolling updates, memory balloon, multi-arch, init systems, build from source."
---

> Rolling updates, memory balloon, multi-arch, init systems, build from source.

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

## Memory Balloon

A service and a `vm create` VM can declare a **virtio-balloon** target: how much
guest memory the node is allowed to reclaim while the VM runs. It trims a VM's
resident memory under host pressure; it does not let a VM grow beyond its memory
limit.

```bash
# A 1 GiB VM that can give back up to 256 MiB
swarmcracker service create --name cache --image redis:7 \
    --memory 1G --balloon-mib 256

# Direct VMs take the same flag
swarmcracker vm create --memory 1024 --balloon-mib 256 alpine:latest

# Change the target on a running service (rolls the task), or remove it
swarmcracker service update cache --balloon-mib 384
swarmcracker service update cache --balloon-mib 0
```

### Units and validation

`--balloon-mib` is an integer count of **MiB**, not a size string (`256`, not
`256m`). It must be a **positive integer smaller than the VM's memory limit**: a
target at or above the memory can never be honoured, so the request is rejected
with an error naming both numbers — before the service is created or the VM is
prepared. A service's memory limit is floored at the platform minimum of 512 MiB
when the target is validated, matching the size the node will actually boot.

On `service create` and `vm create` the flag is optional; an omitted flag means
no balloon device. Because the flag exists only to configure one, an explicit
`--balloon-mib 0` is rejected as invalid rather than read as "no balloon". On
`service update` the flag is **tri-state**: omitting it keeps the service's
current target, a value replaces it, and `--balloon-mib 0` removes it. An update
validates a new target against the memory the task will get — the
`--memory-limit` given on the same command, else the limit already on the
service.

### The device, and when it takes effect

The balloon is created **before the VM boots** (`PUT /balloon` runs before
`InstanceStart`), and Firecracker rejects the request once a VM is running. It
therefore **cannot be added to a running VM**: enable it when the VM is created,
or let `service update` roll the task with the new target.

The device is configured with **`deflate_on_oom: true`**, so when the guest comes
under memory pressure it can take the ballooned pages back instead of being
OOM-killed — reclamation is cooperative, not a hard cap. Statistics polling is
left off.

The balloon only takes effect if the **guest kernel has the virtio-balloon
driver**. If it does not, the device still exists on the host side, but the
guest's memory use is unchanged — the guest has no way to hand pages back. Check
the image's kernel before relying on the flag.

A service that does not declare a balloon gets **no balloon key at all**, so its
VM config and the requests sent to Firecracker are byte-identical to a build
without the feature.

### How the target reaches the node

For a service the target travels as the **`swarmcracker.balloon` service label**,
which SwarmKit copies onto each task and the node reads when it translates the
task into a VM config (a `vm create` VM carries it as a task annotation instead).
You can set that label directly with `--label swarmcracker.balloon=<n>`, but
`--balloon-mib` and a `swarmcracker.balloon` label that **disagree** are refused
rather than resolved silently; a label whose value agrees is accepted. A hand-set
label bypasses the CLI's check and is validated by the node at translation time,
where an invalid value fails the task loudly.

---

## Multi-Architecture Support

SwarmCracker supports multiple CPU architectures. Pick the variant that
matches your nodes through the image you deploy.

### Supported Architectures

| Arch | Firecracker Support | Notes |
|------|---------------------|-------|
| x86_64 | ✅ Full support | Primary target |
| arm64 | ✅ Experimental | AWS Graviton, Ampere |

### Architecture Constraints

`swarmctl create-service` is a minimal debug client and parses only
`--network`, `--name`, and `--replicas` — it does **not** accept
`--constraint`. Use `swarmcracker service` for deployment: `--constraint` and
`--placement-pref` are enforced by the SwarmKit scheduler (`node.hostname`,
`node.role`, `node.labels.*`, …), and the architecture itself is selected
through the image you deploy.

A `node.labels.<key>` constraint is satisfiable only once a matching label
exists on a node, and labels are set through `swarmcracker node update`:

```bash
# Label a node, then place work on it
swarmcracker node update <node-id> --label-add disk=ssd
swarmcracker service create --name web --image nginx:alpine \
    --constraint node.labels.disk==ssd
```

`--label-add KEY=VALUE` and `--label-rm KEY` are both repeatable and at least one
is required; an existing key is overwritten, and removing a key the node does not
have is a no-op. See [`node update`](/reference/cli/#swarmcracker-node).

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
  init_system: tini        # none | tini | dumb-init
  init_grace_period: 10    # seconds before SIGKILL during shutdown
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

- Go 1.26+
- Git
- Make

### Clone and Build

```bash
# Clone repository
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker

# Build binaries
make all

# Output:
# build/swarmcracker
# build/swarmd-firecracker
# build/swarmcracker-agent

# Install
sudo make install
```

### Build Targets

```bash
make all           # Build all binaries
make swarmcracker  # Build the main CLI
make test          # Run unit tests
make lint          # Run linters
make fmt           # Format code
make clean         # Clean build artifacts
make install       # Install binaries to $GOPATH/bin
```

### Development Build

```bash
# Build a debug/dev binary
go build -o build/swarmcracker ./cmd/swarmcracker

# Run the CLI
./build/swarmcracker --help
```

---

## systemd Services

`swarmcracker cluster init` (manager) and `swarmcracker cluster join` (worker)
generate and enable the systemd units for you:

- `swarmcracker-manager.service` — runs `swarmd-firecracker --manager` on the manager
- `swarmcracker-worker.service` — runs `swarmd-firecracker` on workers

Inspect and control them with systemd:

```bash
sudo systemctl status swarmcracker-manager
sudo systemctl restart swarmcracker-worker

# Logs
sudo journalctl -u swarmcracker-manager -f
sudo journalctl -u swarmcracker-worker -f
```

There is no standalone `swarmcracker` service.

---

## File Management

Manage rootfs and kernel images.

### Rootfs Directory

```yaml
executor:
  rootfs_dir: "/var/lib/firecracker/rootfs"
```

### Kernel Management

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
```

### Image Storage

```bash
/var/lib/firecracker/
├── rootfs/                 # executor.rootfs_dir
│   ├── nginx-rootfs.ext4
│   └── redis-rootfs.ext4
└── golden/                 # prebuilt golden image artifacts

/usr/share/firecracker/
└── vmlinux                 # executor.kernel_path (default kernel)
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
swarmctl update <service-id> --image nginx:1.25-alpine
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
go version  # Must be 1.26+

# Check dependencies
go mod download

# Run lint for errors
make lint
```

---

**See Also:** [Configuration](/guides/configuration/) | [Contributing Guide](/contributing/guidelines/)