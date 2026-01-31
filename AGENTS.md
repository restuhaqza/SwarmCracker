# AGENTS.md - Project Guide for Agents

This file helps AI agents (and humans) understand the SwarmCracker project setup, architecture, and workflows.

## 🎯 Project Overview

**SwarmCracker** is a Firecracker microVM executor for SwarmKit orchestration.

**What it does:** Runs Docker containers as hardware-isolated Firecracker microVMs instead of traditional containers, using the familiar Docker Swarm interface.

**Key Value:** Strong KVM-based isolation without Kubernetes complexity.

**Repo:** github.com/restuhaqza/swarmcracker
**Language:** Go 1.21+
**Status:** v0.1.0-alpha (scaffolded, functional)

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
| **Executor** | `pkg/executor` | Main executor implementing SwarmKit interface | 95.2% coverage |
| **Translator** | `pkg/translator` | Converts SwarmKit tasks to Firecracker config | 98.1% coverage |
| **Config** | `pkg/config` | Configuration management with validation | 87.3% coverage |
| **Lifecycle** | `pkg/lifecycle` | VM start/stop/monitor via Firecracker API | 54.4% coverage |
| **Image** | `pkg/image` | OCI image → root filesystem conversion | 61.2% coverage |
| **Network** | `pkg/network` | TAP device & bridge management | 35.1% coverage |
| **Types** | `pkg/types` | Shared interfaces and data structures | Complete |

### Data Flow

1. **SwarmKit** assigns task to agent
2. **Executor** receives task via SwarmKit executor API
3. **Translator** converts task spec to Firecracker JSON config
4. **Image Preparer** pulls OCI image, extracts rootfs
5. **Network Manager** creates TAP device, attaches to bridge
6. **VMM Manager** creates Firecracker socket, configures VM
7. **Firecracker** launches microVM via KVM
8. **Executor** monitors VM status, reports back to SwarmKit

---

## 📁 Project Structure

```
swarmcracker/
├── cmd/
│   └── swarmcracker/
│       └── main.go                 # CLI tool (cobra-based)
├── pkg/                            # Core packages
│   ├── executor/                   # Main executor
│   ├── translator/                 # Task → VM config
│   ├── config/                     # Configuration
│   ├── lifecycle/                  # VM lifecycle
│   ├── image/                      # Image preparation
│   ├── network/                    # Network management
│   └── types/                      # Shared types
├── test/
│   └── mocks/                      # Mock implementations
├── docs/                           # Documentation
│   ├── ARCHITECTURE.md             # Detailed architecture
│   ├── INSTALL.md                  # Installation guide
│   ├── CONFIG.md                   # Configuration reference
│   ├── TESTING.md                  # Testing guide
│   ├── DEVELOPMENT.md              # Development workflow
│   └── reports/                    # Test coverage reports
├── build/                          # Build output (gitignored)
├── README.md                       # Main overview
├── PROJECT.md                      # Status & roadmap
├── CONTRIBUTING.md                 # Contribution guidelines
├── Makefile                        # Build system
├── go.mod                          # Go module definition
└── go.sum                          # Dependency lock
```

### Key Files

| File | Purpose |
|------|---------|
| `README.md` | Main overview, features, quick start |
| `PROJECT.md` | Project status, roadmap, progress |
| `Makefile` | Build, test, install targets |
| `go.mod` | Go dependencies (requires 1.21+) |
| `cmd/swarmcracker/main.go` | CLI tool entry point |
| `pkg/executor/executor.go` | Main executor logic |
| `pkg/config/config.go` | Configuration structures |
| `docs/ARCHITECTURE.md` | System design & components |

---

## 🔨 Build & Development

### Building

```bash
# Build main binary
make build
# Output: build/swarmcracker

# Build all
make all

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
  default_vcpus: 2
  default_memory_mb: 1024

network:
  bridge_name: "swarm-br0"
  default_rate_limit: "10G"

image:
  cache_dir: "/var/cache/swarmcracker"
  max_cache_size_mb: 10240
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

| Package | Coverage | Status |
|---------|----------|--------|
| translator | 98.1% | ✅ Excellent |
| executor | 95.2% | ✅ Excellent |
| config | 87.3% | ✅ Good |
| lifecycle | 54.4% | ⚠️ Needs work |
| image | 61.2% | ⚠️ Needs work |
| network | 35.1% | ⚠️ Needs work |

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
swarmcracker validate --config /etc/swarmcracker/config.yaml

# Run container as microVM (test mode)
swarmcracker run --test nginx:latest

# Run with custom resources
swarmcracker run --vcpus 2 --memory 1024 nginx:latest

# Run with environment variables
swarmcracker run -e APP=prod -e DEBUG=false nginx:latest

# Deploy to remote hosts via SSH
swarmcracker deploy --hosts host1,host2 nginx:latest
```

### Global Flags

- `--config, -c` - Config file path
- `--log-level` - debug, info, warn, error
- `--kernel` - Override kernel path
- `--rootfs-dir` - Override rootfs directory

---

## 🔍 Common Tasks for Agents

### When Adding a New Feature

1. **Update relevant package** in `pkg/`
2. **Add tests** in `*_test.go` files
3. **Update documentation** in `docs/`
4. **Update PROJECT.md** if changing roadmap
5. **Run tests**: `make test`
6. **Format code**: `make fmt`

### When Debugging Issues

1. **Check logs** with `--log-level debug`
2. **Verify config** with `swarmcracker validate`
3. **Test in isolation**: `swarmcracker run --test`
4. **Check Firecracker**: Verify `/dev/kvm` exists
5. **Review test reports** in `docs/reports/`

### When Working with Tests

1. **Privilege-aware**: Many network tests require root
2. **Use mocks**: External deps in `test/mocks/`
3. **Coverage**: Run `make test` and check `coverage.html`
4. **Race detector**: Use `make race` for concurrency bugs

### When Updating Documentation

1. **README.md**: Main overview, features, CLI reference
2. **docs/ARCHITECTURE.md**: System design, components
3. **docs/CONFIG.md**: Configuration options
4. **docs/INSTALL.md**: Setup instructions
5. **PROJECT.md**: Status and roadmap updates
6. **docs/ORGANIZATION.md**: Docs structure

---

## 📚 Dependencies

### Go Dependencies

```go
require (
    github.com/rs/zerolog v1.33.0        // Logging
    gopkg.in/yaml.v3 v3.0.1              // Config parsing
    github.com/spf13/cobra v1.10.2       // CLI framework
)
```

### System Dependencies

- **Go 1.21+** - Language runtime
- **Firecracker v1.0.0+** - MicroVM VMM
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

1. **CLI completion** - Full `swarmcracker` CLI implementation
2. **Test coverage** - Improve lifecycle, image, network packages
3. **Integration tests** - Real Firecracker testing

### Next Steps

1. End-to-end Firecracker integration
2. SwarmKit agent integration
3. Security hardening (jailer integration)
4. Performance optimization
5. Alpha release (v0.2.0)

---

## 🤝 Contributing

### Before Contributing

1. Read `CONTRIBUTING.md`
2. Check `PROJECT.md` for roadmap alignment
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
- **Architecture**: `docs/ARCHITECTURE.md`
- **Configuration**: `docs/CONFIG.md`
- **Testing**: `docs/TESTING.md`
- **Development**: `docs/DEVELOPMENT.md`

### Test Reports

- Image preparer: `docs/reports/IMAGE_PREPARER_TESTS_REPORT.md`
- Network manager: `docs/reports/NETWORK_MANAGER_TESTS_REPORT.md`

### External Resources

- [SwarmKit](https://github.com/moby/swarmkit) - Orchestration engine
- [Firecracker](https://github.com/firecracker-microvm/firecracker) - MicroVM technology
- [firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd) - Container integration reference

---

## 📝 Notes

- This project is alpha quality - expect breaking changes
- Test coverage is good but not complete
- Documentation is actively maintained
- Contributions welcome - see CONTRIBUTING.md

**Last Updated:** 2026-01-31
**Project Lead:** Restu Muzakir
**License:** Apache 2.0
