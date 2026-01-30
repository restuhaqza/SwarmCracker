# Development Guide

Guide for contributing to and developing SwarmCracker.

## Getting Started

### Prerequisites

- Go 1.21+
- Docker or Podman (for testing)
- make
- git
- Access to KVM (/dev/kvm)

### Setup Development Environment

```bash
# Clone repository
git clone https://github.com/restuhaqza/swarmcracker.git
cd swarmcracker

# Install dependencies
go mod download

# Install development tools
make install-tools

# Run tests to verify setup
make test
```

## Project Structure

```
swarmcracker/
├── cmd/                    # CLI applications
│   └── swarmcracker-kit/   # Main CLI tool
├── pkg/                    # Public packages
│   ├── executor/          # Executor implementation
│   ├── translator/        # Task → VM config
│   ├── image/             # Image preparation
│   ├── network/           # Network management
│   ├── lifecycle/         # VM lifecycle
│   ├── config/            # Configuration
│   └── types/             # Shared types
├── test/                  # Test utilities
│   └── mocks/             # Mock implementations
├── docs/                  # Documentation
├── examples/              # Example configurations
└── Makefile              # Build automation
```

## Development Workflow

### 1. Create a Branch

```bash
git checkout -b feature/your-feature-name
# or
git checkout -b fix/your-bug-fix
```

### 2. Make Changes

- Write code following [Code Style](#code-style)
- Add/update tests
- Update documentation as needed

### 3. Run Tests

```bash
# Run all tests
make test

# Run specific package tests
go test ./pkg/executor -v

# Run with race detector
make race

# Run with coverage
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out
```

### 4. Lint Code

```bash
# Run linters
make lint

# Format code
make fmt
```

### 5. Build

```bash
# Build binaries
make build

# Test build
./build/swarmcracker-kit --help
```

### 6. Commit Changes

```bash
git add .
git commit -m "feat: add your feature description"
```

#### Commit Message Format

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

**Types:**
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `test`: Test changes
- `refactor`: Code refactoring
- `perf`: Performance improvement
- `chore`: Maintenance tasks

**Examples:**

```bash
feat(executor): add support for volume mounts
fix(network): handle bridge creation failure gracefully
docs(config): add jailer configuration examples
test(translator): add tests for resource limits
```

### 7. Push and Create PR

```bash
git push origin feature/your-feature-name
# Then create PR on GitHub
```

## Code Style

### Go Conventions

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` for formatting
- Run `golangci-lint` before committing

### Naming

```go
// Good - clear, descriptive
func (e *FirecrackerExecutor) Prepare(ctx context.Context, task *Task) error

// Bad - unclear abbreviations
func (e *FCExec) Prep(ctx context.Context, t *Task) error
```

### Error Handling

```go
// Good - wrap errors with context
if err := vmmManager.Start(ctx, task, config); err != nil {
    return fmt.Errorf("failed to start VM: %w", err)
}

// Good - use custom errors for expected failures
var ErrVMNotFound = errors.New("VM not found")

// Bad - bare returns
if err != nil {
    return
}
```

### Logging

```go
// Good - structured logging
log.Info().
    Str("task_id", task.ID).
    Str("service_id", task.ServiceID).
    Msg("Starting task")

// Good - error with fields
log.Error().
    Err(err).
    Str("vm_id", vmID).
    Msg("Failed to start VM")
```

### Context Usage

```go
// Always accept context as first parameter
func (e *Executor) Prepare(ctx context.Context, task *Task) error {
    // Check for cancellation
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }
    // ... work ...
}
```

## Testing

### Test Organization

```
pkg/executor/
├── executor.go          # Implementation
└── executor_test.go     # Tests
```

### Writing Tests

```go
package executor

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestExecutor_Prepare(t *testing.T) {
    tests := []struct {
        name    string
        task    *types.Task
        wantErr bool
    }{
        {
            name: "successful prepare",
            task: mocks.NewTestTask("task-1", "nginx:latest"),
            wantErr: false,
        },
        {
            name: "missing image",
            task: &types.Task{},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup
            exec := NewTestExecutor()

            // Execute
            err := exec.Prepare(context.Background(), tt.task)

            // Assert
            if tt.wantErr {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

### Using Mocks

```go
import "github.com/restuhaqza/swarmcracker/test/mocks"

func TestMyFunction(t *testing.T) {
    // Create mocks
    vmm := mocks.NewMockVMMManager()
    img := mocks.NewMockImagePreparer()

    // Configure mock behavior
    vmm.ShouldFail = true

    // Test
    err := MyFunction(vmm, img)
    assert.Error(t, err)
}
```

### Test Coverage

Target coverage:
- Core packages: 80%+
- Translator: 95%+
- Executor: 90%+

Check coverage:
```bash
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out
```

### Integration Tests

Mark integration tests with build tag:

```go
//go:build integration
// +build integration

package integration_test

func TestRealFirecracker(t *testing.T) {
    // Requires actual Firecracker
}
```

Run integration tests:
```bash
go test -tags=integration ./test/integration/...
```

## Adding New Features

### 1. Design First

- Update ARCHITECTURE.md if needed
- Document the approach
- Get feedback if it's a major change

### 2. Implement Interfaces

```go
// Define interface in pkg/types/
type NewFeature interface {
    DoSomething(ctx context.Context) error
}

// Implement in pkg/newfeature/
type NewFeatureImpl struct {
    config *Config
}
```

### 3. Add Tests

- Unit tests for all public methods
- Integration tests if needed
- Benchmark tests for performance-critical code

### 4. Update Documentation

- Update relevant docs in `docs/`
- Add examples to `examples/`
- Update README if user-facing

### 5. Update Configuration

If feature needs config:

```go
// Add to pkg/config/config.go
type Config struct {
    // ... existing fields ...
    NewFeature NewFeatureConfig `yaml:"new_feature"`
}

type NewFeatureConfig struct {
    Enabled bool `yaml:"enabled"`
    Option  string `yaml:"option"`
}
```

## Debugging

### Enable Debug Logging

```bash
# In config.yaml
logging:
  level: "debug"
  format: "text"

# Or via environment
export SWARMCRACKER_LOG_LEVEL=debug
```

### Attach Debugger

```bash
# Delve debugger
dlv debug ./cmd/swarmcracker-kit/main.go -- --config config.yaml

# Set breakpoints
(dlv) break main.main
(dlv) break executor.(*FirecrackerExecutor).Prepare
```

### Debug Tests

```bash
# Verbose test output
go test -v ./pkg/executor

# Run specific test
go test -v -run TestExecutor_Prepare ./pkg/executor

# Print test coverage
go test -cover ./pkg/...
```

### Common Issues

**VM fails to start:**
- Check kernel path exists
- Verify KVM is available: `ls -la /dev/kvm`
- Enable debug logging for Firecracker API errors

**Network issues:**
- Verify bridge exists: `ip link show swarm-br0`
- Check TAP device creation: `ip link show tap-*`
- Review iptables rules if using NAT

**Permission denied:**
- Add user to kvm group: `sudo usermod -a -G kvm $USER`
- Check directory permissions
- Review jailer configuration

## Performance

### Benchmarking

```go
func BenchmarkExecutor_Start(b *testing.B) {
    exec := NewTestExecutor()
    task := mocks.NewTestTask("bench", "nginx")

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _ = exec.Start(context.Background(), task)
    }
}
```

Run benchmarks:
```bash
go test -bench=. -benchmem ./pkg/executor
```

### Profiling

```bash
# CPU profile
go test -cpuprofile=cpu.prof ./pkg/executor
go tool pprof cpu.prof

# Memory profile
go test -memprofile=mem.prof ./pkg/executor
go tool pprof mem.prof
```

### Optimization Tips

- Reuse connections where possible
- Pool resources (buffers, connections)
- Use sync.Pool for frequently allocated objects
- Profile before optimizing
- Measure impact of optimizations

## Documentation

### Code Comments

```go
// FirecrackerExecutor implements the SwarmKit executor interface
// to run tasks as Firecracker microVMs.
//
// The executor manages the full lifecycle of microVMs including:
// - Image preparation (OCI → rootfs)
// - Network setup (TAP devices, bridges)
// - VM lifecycle (start/stop/monitor)
// - Resource cleanup
type FirecrackerExecutor struct {
    // config holds the executor configuration
    config *Config

    // vmmManager manages Firecracker VM lifecycle
    vmmManager types.VMMManager
}
```

### Package Documentation

```go
// Package executor provides a Firecracker-based executor for SwarmKit.
//
// The executor translates SwarmKit tasks into Firecracker microVMs,
// providing hardware isolation for container workloads.
//
// Basic usage:
//
//	exec, err := executor.NewFirecrackerExecutor(config, ...)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	err = exec.Prepare(ctx, task)
//	err = exec.Start(ctx, task)
package executor
```

## Release Process

### Versioning

SwarmCracker follows [Semantic Versioning](https://semver.org/):
- MAJOR: Breaking changes
- MINOR: New features (backwards compatible)
- PATCH: Bug fixes

### Creating a Release

```bash
# Update version
git tag -a v0.2.0 -m "Release v0.2.0"

# Build release binaries
make release

# Push tag
git push origin v0.2.0
```

### Release Checklist

- [ ] All tests passing
- [ ] Documentation updated
- [ ] CHANGELOG.md updated
- [ ] Version bumped
- [ ] Release notes prepared
- [ ] Binaries tested

## Getting Help

### Resources

- [SwarmKit Documentation](https://github.com/moby/swarmkit)
- [Firecracker Documentation](https://github.com/firecracker-microvm/firecracker)
- [Go Documentation](https://go.dev/doc/)

### Community

- GitHub Issues: Bug reports and feature requests
- GitHub Discussions: Questions and ideas
- Discord: Chat with other developers

## Code of Conduct

Be respectful, inclusive, and constructive. We're all here to build something cool.

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.
