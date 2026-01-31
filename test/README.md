# Test Directory

This directory contains the test suite for SwarmCracker.

**Documentation has moved to** `docs/testing/`

## Quick Links

- [Testing Overview](../docs/testing/README.md) - Complete testing guide
- [Unit Tests](../docs/testing/unit.md) - Unit testing documentation
- [Integration Tests](../docs/testing/integration.md) - Integration testing with Firecracker
- [E2E Tests](../docs/testing/e2e.md) - End-to-end testing with Docker Swarm
- [Test Infrastructure](../docs/testing/testinfra.md) - Environment validation

## Running Tests

```bash
# From project root
make test              # Unit tests
make integration-test  # Integration tests
make e2e-test         # E2E tests
make testinfra        # Infrastructure checks
make test-all         # All tests
```

## Directory Structure

```
test/
├── e2e/                      # End-to-end tests
│   ├── docker_swarm_test.go  # Docker Swarm E2E tests
│   ├── swarmkit_test.go      # SwarmKit tests
│   ├── cluster/              # Cluster management helpers
│   ├── scenarios/            # Test scenarios
│   └── fixtures/             # Test fixtures
├── integration/              # Integration tests
│   ├── integration_test.go   # Main integration tests
│   ├── README.md             # Integration test guide
│   └── FIRECRACKER_SETUP.md  # Firecracker setup
├── testinfra/                # Infrastructure validation
│   ├── testinfra_test.go     # Main infrastructure tests
│   ├── checks/               # Individual checkers
│   │   ├── firecracker.go
│   │   ├── kernel.go
│   │   └── network.go
│   └── helpers.go            # Test helper utilities
└── mocks/                    # Mock implementations
    └── mocks.go
```

## Test Categories

### Unit Tests (`pkg/*/*_test.go`)
- Fast, isolated tests
- Mock external dependencies
- Run frequently during development

### Integration Tests (`test/integration/`)
- Test with real Firecracker
- Require KVM, kernel, container runtime
- Validate component integration

### E2E Tests (`test/e2e/`)
- Full-stack testing with Docker Swarm
- Real service deployment
- Production-like validation

### Test Infrastructure (`test/testinfra/`)
- Validate test environment
- Check prerequisites
- Diagnose setup issues

## Documentation

For detailed testing documentation, see:
- **`docs/testing/`** - Complete testing documentation
- **`docs/testing/README.md`** - Testing overview
- **`docs/index.md`** - Main documentation index

## Quick Start

1. **Check your environment**
   ```bash
   make testinfra
   ```

2. **Run unit tests** (fast)
   ```bash
   make test-quick
   ```

3. **Run integration tests** (requires Firecracker)
   ```bash
   make integration-test
   ```

4. **Run E2E tests** (requires Docker Swarm)
   ```bash
   make e2e-test
   ```

## Contribute Tests

When adding new features:
1. Add unit tests for new code
2. Add integration tests if needed
3. Update documentation
4. Ensure all tests pass

See [Contributing Guide](../CONTRIBUTING.md) for details.

---

**Documentation**: See `docs/testing/` for complete testing documentation
