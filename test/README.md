# Test Directory

This directory contains the test suite for SwarmCracker.

**Documentation has moved to** `docs-site/src/content/docs/contributing/testing/`

## Quick Links

- [Testing Overview](../docs-site/src/content/docs/contributing/testing/) - Complete testing guide

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
├── e2e/                          # End-to-end tests
│   ├── firecracker/              # VM lifecycle tests
│   ├── cluster/                  # Cluster management helpers
│   ├── fixtures/                 # Test fixtures
│   ├── full_workflow_test.go     # Full workflow test
│   ├── swarmkit_test.go          # SwarmKit tests
│   ├── swarmkit_api_test.go      # SwarmKit API tests
│   ├── swarmkit_comprehensive_test.go
│   ├── config.yaml               # E2E test configuration
│   └── run.sh                    # E2E test runner
├── integration/                  # Integration tests
│   ├── integration_test.go       # Main integration tests
│   ├── snapshot_integration_test.go
│   ├── README.md                 # Integration test guide
│   └── SNAPSHOT_TESTS.md         # Snapshot integration guide
├── testinfra/                    # Infrastructure validation
│   ├── testinfra_test.go         # Main infrastructure tests
│   ├── checks/                   # Individual checkers
│   │   ├── firecracker.go
│   │   ├── kernel.go
│   │   └── network.go
│   └── helpers.go                # Test helper utilities
└── mocks/                        # Mock implementations
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
- **[Testing Overview](../docs-site/src/content/docs/contributing/testing/)** - Testing guide and strategy

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

**Documentation**: See [docs-site/src/content/docs/contributing/testing/](../docs-site/src/content/docs/contributing/testing/) for complete testing documentation