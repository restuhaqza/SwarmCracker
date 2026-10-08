# AGENTS.md - Project Guide for Agents

This file helps AI agents (and humans) understand the SwarmCracker project setup, architecture, and workflows.

## 🎯 Project Overview

**SwarmCracker** is a Firecracker microVM executor for SwarmKit orchestration.

**What it does:** Runs Docker containers as hardware-isolated Firecracker microVMs instead of traditional containers, using the familiar Docker Swarm interface.

**Key Value:** Strong KVM-based isolation without Kubernetes complexity.

**Repo:** github.com/restuhaqza/swarmcracker
**Language:** Go 1.26+
**Status:** v0.10.0 — actively developed

---

## 🏗️ Architecture Overview

```
SwarmKit Manager (orchestration)
    ↓ gRPC
SwarmKit Agent (task distribution)
    ↓ Executor API
SwarmCracker Executor (orchestrates VM lifecycle)
    ├─→ Task Translator (SwarmKit → Firecracker config)
    ├─→ Image Preparer (OCI image → root filesystem)
    ├─→ Network Manager (TAP devices, bridges)
    └─→ VMM Manager (Firecracker API, lifecycle)
    ↓ REST API
Firecracker VMM (microVM process)
    ↓ KVM
MicroVM (isolated kernel + workload)
```

### Key Components

| Component | Package | Purpose | Status |
|-----------|---------|---------|--------|
| **Executor** | `pkg/executor` | Main executor implementing SwarmKit interface | 90.7% coverage |
| **Translator** | `pkg/translator` | Converts SwarmKit tasks to Firecracker config | 94.9% coverage |
| **Config** | `pkg/config` | Configuration management with validation | 97.6% coverage |
| **Lifecycle** | `pkg/lifecycle` | VM start/stop/monitor via Firecracker API | 82.3% coverage |
| **Runtime** | `pkg/runtime` | Runtime utilities and helpers | 86.0% coverage |
| **Discovery** | `pkg/discovery` | Service discovery mechanisms | 80.6% coverage |
| **Metrics** | `pkg/metrics` | Prometheus metrics collection | 84.4% coverage |
| **Jailer** | `pkg/jailer` | Security sandboxing via jailer | 92.6% coverage |
| **Image** | `pkg/image` | OCI image → root filesystem conversion | 85.8% coverage |
| **Network** | `pkg/network` | TAP device & bridge management | 86.7% coverage |
| **Storage** | `pkg/storage` | Volume driver system | 87.3% coverage |
| **Snapshot** | `pkg/snapshot` | VM snapshot/restore lifecycle | 87.9% coverage |
| **SwarmKit** | `pkg/swarmkit` | SwarmKit API integration | 87.0% coverage |
| **Types** | `pkg/types` | Shared interfaces and data structures | 100.0% coverage |

> Coverage measured 2026-09-29 (total `./pkg/...` 87.6%); see
> `docs-site/src/content/docs/contributing/testing/unit-tests.md` for the full
> table and methodology.

### Data Flow

1. **SwarmKit** assigns task to agent
2. **Executor** receives task via SwarmKit executor API
3. **Translator** converts task spec to Firecracker JSON config
4. **Image Preparer** pulls OCI image, extracts rootfs
5. **Network Manager** creates TAP device, attaches to bridge
6. **VMM Manager** creates Firecracker socket, configures VM
7. **Firecracker** launches microVM via KVM
8. **Executor** monitors VM status, reports back to SwarmKit
9. **Discovery** registers service instances for discovery
10. **Metrics** exposes Prometheus metrics for monitoring
11. **Snapshot** captures VM state for fast restore (optional)

---

## 📁 Project Structure

```
swarmcracker/
├── cmd/
│   ├── swarmcracker/               # Main orchestration CLI
│   ├── swarmd-firecracker/         # SwarmKit agent with FC executor
│   └── swarmcracker-agent/         # Agent daemon
├── pkg/                            # Core packages
│   ├── executor/                   # Main executor
│   ├── translator/                 # Task → VM config
│   ├── config/                     # Configuration
│   ├── lifecycle/                  # VM lifecycle
│   ├── runtime/                    # Runtime utilities
│   ├── discovery/                  # Service discovery
│   ├── metrics/                    # Prometheus metrics
│   ├── image/                      # Image preparation
│   ├── network/                    # Network management
│   ├── jailer/                     # Jailer sandboxing
│   ├── storage/                    # Volume driver system
│   ├── snapshot/                   # VM snapshots
│   ├── swarmkit/                   # SwarmKit API integration
│   └── types/                      # Shared types
├── test/
│   ├── mocks/                      # Mock implementations
│   ├── integration/                # Integration tests
│   └── e2e/                        # End-to-end tests
├── infrastructure/                 # Ansible playbooks, Terraform
├── docs-site/                      # Astro Starlight docs site (docs.swarmcracker.com)
│   ├── src/content/docs/           # Published docs (guides, reference, architecture…)
│   └── src/styles/custom.css       # Brand theme
├── docs/                           # Internal material (reports, superpowers, design assets)
├── build/                          # Build output (gitignored)
├── README.md                       # Main overview
├── CONTRIBUTING.md                 # Contribution guidelines
├── Makefile                        # Build system
├── go.mod                          # Go module definition
└── go.sum                          # Dependency lock
```

### Key Files

| File | Purpose |
|------|---------|
| `README.md` | Main overview, features, quick start |
| `Makefile` | Build, test, install targets |
| `go.mod` | Go dependencies (requires 1.26+) |
| `cmd/swarmcracker/main.go` | CLI tool entry point |
| `pkg/executor/executor.go` | Main executor logic |
| `pkg/config/config.go` | Configuration structures |
| `docs-site/src/content/docs/` | Published documentation (Astro Starlight) |

---

## 🔨 Build & Development

### Building

```bash
# Build the main CLI
make swarmcracker
# Output: build/swarmcracker

# Build all binaries
make all
# Output: build/swarmcracker, build/swarmd-firecracker, build/swarmcracker-agent

# Install to $GOPATH/bin
make install

# Build release binaries
make release
```

### Testing

```bash
# Run all tests
make test

# Run specific package
go test -v ./pkg/executor/

# Run with coverage
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out -o coverage.html

# Run integration tests
make integration-test

# Run with race detector
make race
```

### Development Workflow

```bash
# Format code
make fmt

# Run linters
make lint

# Clean build artifacts
make clean

# Development with hot reload
make dev
```

---

## 🔧 Configuration

### Default Config Location

`/etc/swarmcracker/config.yaml`

### Key Config Sections

```yaml
executor:
  kernel_path: "/usr/share/firecracker/vmlinux"
  rootfs_dir: "/var/lib/firecracker/rootfs"
  default_vcpus: 1
  default_memory_mb: 512

network:
  bridge_name: "swarm-br0"
  enable_rate_limit: false
  max_packets_per_sec: 10000

images:
  cache_dir: "/var/cache/swarmcracker"
  max_cache_size_mb: 1024
```

### CLI Overrides

```bash
swarmcracker --kernel /path/to/vmlinux run nginx:latest
swarmcracker --rootfs-dir /custom/rootfs run nginx:latest
swarmcracker --config /custom/config.yaml run nginx:latest
```

---

## 🧪 Testing Strategy

### Test Organization

- **Unit tests**: Package-specific (`*_test.go`)
- **Mock objects**: `test/mocks/` for external dependencies
- **Integration tests**: `test/integration/` (requires Firecracker)

### Current Coverage

`./pkg/...` coverage was **87.6%** across 19 packages, measured 2026-09-29. The
full per-package table, the 85% target/threshold rationale, and the measurement
methodology live in
[`docs-site/src/content/docs/contributing/testing/unit-tests.md`](docs-site/src/content/docs/contributing/testing/unit-tests.md).

### Running Specific Tests

```bash
# Executor tests
go test -v ./pkg/executor/

# Network tests (may require root)
sudo go test -v ./pkg/network/

# With verbose output
go test -v -race ./pkg/...
```

---

## 🚀 CLI Usage

### Basic Commands

```bash
# Show help
swarmcracker --help

# Show version
swarmcracker version

# Validate config
swarmcracker config validate

# Initialize a cluster (manager)
sudo swarmcracker cluster init --advertise-addr 192.168.1.10:4242

# Join a cluster (worker)
sudo swarmcracker cluster join 192.168.1.10:4242 --token SWMTKN-1-xxx

# Create a microVM directly
sudo swarmcracker vm create --cpu 2 --memory 1024 -e APP=prod nginx:latest

# Deploy a service
swarmcracker service create --name web --image nginx:alpine --replicas 3
```

### Snapshot Commands

```bash
# List all snapshots
swarmcracker vm snapshot list

# Create a snapshot of a running VM
swarmcracker vm snapshot create task-123

# Restore a VM from a snapshot
swarmcracker vm snapshot restore snap-a1b2c3d4e5f67890

# Delete a snapshot
swarmcracker vm snapshot delete snap-a1b2c3d4e5f67890

# Clean up old snapshots
swarmcracker vm snapshot cleanup --max-age 24h
```

### Attach to a VM Console

```bash
# Find a task ID (service ps shows a 12-char prefix)
swarmcracker task ls

# Attach to the guest ttyS0 console (task ID or unique prefix). Ctrl-P Ctrl-Q detaches.
swarmcracker vm attach 5f3a1b2c9d
```

The console socket is `<socket-dir>/<task-id>.console.sock` (default socket dir
`/var/run/firecracker`, owned by the daemon, mode `0600`). Attach works for VMs
managed by the daemon (service tasks); VMs started in the foreground by
`swarmcracker vm create` are not attachable because the CLI owns their console.

### Global Flags

- `--config, -c` - Config file path
- `--log-level` - debug, info, warn, error
- `--kernel` - Override kernel path
- `--rootfs-dir` - Override rootfs directory
- `--ssh-key` - SSH key for remote deployment

### `vm snapshot create` Flags

- `--socket` - Firecracker API socket path
- `--service` - SwarmKit service ID (metadata)
- `--node` - Node ID (metadata)
- `--rootfs` - Rootfs path (metadata)
- `--vcpus` - vCPU count (metadata)
- `--memory` - Memory in MB (metadata)
- `--max-age` - Snapshot age for cleanup (e.g., 24h, 7d)

---

## 🔍 Common Tasks for Agents

### When Adding a New Feature

1. **Update relevant package** in `pkg/`
2. **Add tests** in `*_test.go` files
3. **Update documentation** under `docs-site/src/content/docs/`
4. **Run tests**: `make test`
5. **Format code**: `make fmt`
6. **Verify docs**: `cd docs-site && npm run verify` (if docs changed)

### When Debugging Issues

1. **Check logs** with `--log-level debug`
2. **Verify config** with `swarmcracker config validate`
3. **Test in isolation**: `swarmcracker vm create --detach alpine:latest`
4. **Check Firecracker**: Verify `/dev/kvm` exists

### When Working with Tests

1. **Privilege-aware**: Many network tests require root
2. **Use mocks**: External deps in `test/mocks/`
3. **Coverage**: Run `make test` and check `coverage.html`
4. **Race detector**: Use `make race` for concurrency bugs

### When Updating Documentation

Published docs live in `docs-site/src/content/docs/` (Astro Starlight) and map
1:1 to URLs at docs.swarmcracker.com. Update the page there, then run
`cd docs-site && npm run verify`. See `docs-site/README.md`.

1. **`docs-site/src/content/docs/guides/configuration.md`**: Configuration options (authoritative)
2. **`docs-site/src/content/docs/reference/cli.md`**: CLI reference
3. **`docs-site/src/content/docs/architecture/`**: System design, components
4. **`docs-site/src/content/docs/getting-started/`**: Setup instructions
5. **`docs-site/src/content/docs/contributing/`**: Contributor docs
6. **`docs/`**: Internal only (reports, agent plans) — not published

---

## 📸 Snapshot Feature

### Overview

SwarmCracker supports full VM snapshot/restore functionality for Firecracker v1.14.0+ (v1.15.1 is the version installed by `setup install`).

**Use Cases:**
- Fast VM restore (2-3x faster than cold boot)
- VM state preservation before updates
- Development workflow (save/restore clean states)
- Future: Live migration support

### How It Works

1. **Pause VM** - `PATCH /vm {"state": "Paused"}`
2. **Create Snapshot** - `PUT /snapshot/create` (saves memory + state)
3. **Store Files** - `vm.state` (~15KB) + `vm.mem` (VM memory size)
4. **Restore** - `PUT /snapshot/load` with `resume_vm: true`
5. **Auto-Resume** - VM continues from exact state

### Firecracker v1.14.0+ API Changes

| Operation | Old API (< v1.10) | New API (v1.14.0+) |
|-----------|------------------|-------------------|
| Pause VM | `PUT /vm/pause` | `PATCH /vm {"state": "Paused"}` |
| Resume VM | `PUT /vm/resume` | `PATCH /vm {"state": "Resumed"}` |
| Create Snapshot | `mem_backend` object | `snapshot_type` + `mem_file_path` |
| Load Snapshot | Basic format | Added `resume_vm` flag |

### Implementation Details

**Package:** `pkg/snapshot/snapshot.go`

**Key Functions:**
- `pauseVM()` - Pause VM before snapshot
- `resumeVM()` - Resume VM after restore
- `CreateSnapshot()` - Create full snapshot (auto-pauses VM)
- `RestoreFromSnapshot()` - Restore and auto-resume
- `ListSnapshots()` - List with filters (service, task, node)
- `DeleteSnapshot()` - Delete and free disk space
- `CleanupOldSnapshots()` - Remove snapshots older than max age

**Configuration:**
```yaml
snapshot:
  enabled: true
  snapshot_dir: /var/lib/firecracker/snapshots
  max_snapshots: 3        # Per service
  max_age: 168h           # 7 days
  auto_snapshot: false    # Auto-snapshot on start
```

### Testing

**Unit Tests:** `pkg/snapshot/snapshot_test.go` (20+ tests)

**Integration Tests:** `test/integration/snapshot_integration_test.go`
- TestIntegration_Snapshot_CreateAndRestore
- TestIntegration_Snapshot_CleanupOldSnapshots
- TestIntegration_Snapshot_MaxSnapshotsEnforcement
- TestIntegration_Snapshot_ChecksumVerification

**Real Cluster Test:** `infrastructure/ansible/playbooks/test-snapshot.yml`

**Test Results:**
```bash
# All tests pass
✅ VM Started: True
✅ Snapshot Created: True (15KB state, 256MB memory)
✅ VM Restored: True
✅ Restored VM Running: True
```

### Documentation

- `docs-site/src/content/docs/guides/snapshots.md` - Snapshot CLI usage and workflows

### Known Limitations

1. **VM must be paused** - Required before snapshot (handled automatically)
2. **Network state** - Network connections may not survive restore
3. **Rootfs path** - Must be accessible at same path on restore
4. **Firecracker version** - Requires v1.14.0+ for current API

### Security Notes

- Snapshot files are trusted by Firecracker
- Encrypt snapshots at rest if containing sensitive data
- Restrict access to snapshot directory
- Verify checksums on restore (automatic)

---

## 📚 Dependencies

### Go Dependencies

```go
require (
    github.com/rs/zerolog v1.35.1        // Logging
    gopkg.in/yaml.v3 v3.0.1              // Config parsing
    github.com/spf13/cobra v1.10.2       // CLI framework
    github.com/google/go-containerregistry v0.21.5 // OCI image handling
)
```

### System Dependencies

- **Go 1.26+** - Language runtime
- **Firecracker v1.14.0+** - MicroVM VMM (v1.15.1 recommended; installed by `setup install`)
- **KVM** - Hardware virtualization (`/dev/kvm`)
- **Linux** - Required OS (KVM is Linux-only)

### Development Tools

- **golangci-lint** - Linting
- **staticcheck** - Static analysis
- **air** - Hot reload for development
- **mockgen** - Mock generation

---

## 🔐 Security Considerations

### Privilege Model

- **SwarmCracker Executor**: Runs as root (needs KVM, TAP, bridge access)
- **Firecracker VMM**: Runs as unprivileged user
- **MicroVM**: Isolated via KVM (no host access)

### Security Boundaries

1. **Host → VMM**: Systemd service limits, cgroups
2. **VMM → MicroVM**: KVM hardware virtualization
3. **MicroVM → Workload**: Kernel namespaces

### Best Practices

- Never run workload containers as root
- Use resource limits (vCPUs, memory)
- Isolate networks (TAP devices per VM)
- Validate all configs before execution
- Clean up resources on shutdown

---

## 🎯 Development Priorities

### Current Focus

1. **Test coverage maintenance** - keep `./pkg/...` above the 85% target (measured 87.6%)
2. **CI/CD enhancement** - GitHub Actions workflows for testing and releases
3. **Documentation updates** - Keeping docs in sync with code

---

## 🤝 Contributing

### Before Contributing

1. Read `CONTRIBUTING.md`
2. Check open issues and `CHANGELOG.md` for context
3. Discuss significant changes first

### Code Standards

- **Format**: `make fmt` (goimports)
- **Lint**: `make lint` (golangci-lint)
- **Test**: `make test` (all tests must pass)
- **Docs**: Update relevant docs

### Pull Request Process

1. Fork and branch from `main`
2. Make changes with tests
3. Update documentation
4. Run `make test lint`
5. Submit PR with description

---

## 📞 Getting Help

### Documentation

- **Quick start**: `README.md`
- **Published docs**: `docs-site/src/content/docs/` (live at docs.swarmcracker.com)
- **Architecture**: `docs-site/src/content/docs/architecture/`
- **Configuration**: `docs-site/src/content/docs/guides/configuration.md`
- **Testing**: `docs-site/src/content/docs/contributing/testing/`
- **Development**: `docs-site/src/content/docs/contributing/`
- **Internal docs**: `docs/` (reports, agent plans — not published)

### Test Reports

- Coverage reports generated via `go test -coverprofile=coverage.out ./pkg/...`

### External Resources

- [SwarmKit](https://github.com/moby/swarmkit) - Orchestration engine
- [Firecracker](https://github.com/firecracker-microvm/firecracker) - MicroVM technology
- [firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd) - Container integration reference

---

## 📝 Notes

- This project is actively developed - v0.10.0
- Test coverage is 87.6% across 19 packages, above the 85% target
- Documentation is actively maintained
- Contributions welcome - see CONTRIBUTING.md

**Last Updated:** 2026-10-07
**Project Lead:** Restu Muzakir
**License:** Apache 2.0
