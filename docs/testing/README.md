# Testing Overview

SwarmCracker has a comprehensive testing suite to ensure code quality, integration with external components, and end-to-end functionality.

## Test Pyramid

```
            ┌─────────┐
            │  E2E    │  ← Full stack validation
            │  Tests  │     (Docker Swarm)
         ┌──┴─────────┴──┐
         │  Integration │  ← Component integration
         │    Tests     │     (Firecracker, containerd)
      ┌───┴─────────────┴───┐
      │    Unit Tests      │  ← Function/method testing
      │                     │     (Fast, isolated)
      └─────────────────────┘
```

## Quick Reference

| Test Type | Command | Duration | Purpose |
|-----------|---------|----------|---------|
| **Unit** | `make test` | ~10s | Test functions/methods |
| **Integration** | `make integration-test` | ~5min | Test with Firecracker |
| **E2E (Docker)** | `make e2e-test` | ~1min | Validate Docker Swarm |
| **E2E (SwarmKit)** | `go test -v ./test/e2e/ -run TestE2E_SwarmKit` | ~10-30min | Test SwarmKit integration |
| **Testinfra** | `make testinfra` | ~2s | Validate environment |

## Running Tests

### Run All Tests
```bash
# Run everything (unit, integration, E2E, testinfra)
make test-all

# Or individually
make test              # Unit tests
make integration-test  # Integration tests
make e2e-test         # E2E tests
make testinfra        # Infrastructure checks
```

### Quick Development Cycle
```bash
# Run only unit tests (fast)
make test-quick

# Check infrastructure
make testinfra
```

### With Coverage
```bash
# Generate coverage report
make test

# View coverage
go tool cover -html=coverage.out
```

## Test Types

### 1. Unit Tests
**Location**: `pkg/*/*_test.go`

**Purpose**: Test individual functions and methods

**Dependencies**: Mocked

**Speed**: Fast (< 1 minute)

**Example**:
```bash
go test -v ./pkg/executor/...
```

**Documentation**: [Unit Testing Guide](unit.md)

---

### 2. Integration Tests
**Location**: `test/integration/`

**Purpose**: Test component integration with Firecracker

**Dependencies**: Firecracker, kernel, KVM, container runtime

**Speed**: Medium (2-5 minutes)

**Example**:
```bash
go test -v -tags=integration ./test/integration/...
```

**Documentation**: [Integration Testing Guide](integration.md)

---

### 3. E2E Tests

We have **two types** of E2E tests:

#### Docker Swarm E2E Tests
**Location**: `test/e2e/docker_swarm_test.go`

**Purpose**: Validate Docker Swarm environment

**Dependencies**: Docker Swarm only

**Speed**: Medium (1-2 minutes)

**Example**:
```bash
go test -v ./test/e2e/ -run TestE2E_DockerSwarmBasic
```

**Documentation**: [E2E Testing with Docker Swarm](e2e.md)

#### SwarmKit E2E Tests
**Location**: `test/e2e/swarmkit_test.go`, `test/e2e/scenarios/`

**Purpose**: Test SwarmCracker as SwarmKit executor

**Dependencies**: SwarmKit (swarmd), Firecracker, KVM

**Speed**: Slow (5-30 minutes)

**Example**:
```bash
go test -v ./test/e2e/ -run TestE2E_SwarmKit
```

**Documentation**: [E2E Testing with SwarmKit](e2e_swarmkit.md) ← **Use this for SwarmCracker**

---

### 4. Test Infrastructure
**Location**: `test/testinfra/`

**Purpose**: Validate test environment

**Dependencies**: None

**Speed**: Fast (< 5 seconds)

**Example**:
```bash
go test -v ./test/testinfra/...
```

**Documentation**: [Test Infrastructure Guide](testinfra.md)

## Test Workflow

### Before Running Tests
1. **Check Infrastructure**
   ```bash
   make testinfra
   ```
   Ensure all prerequisites are met

2. **Fix Any Issues**
   Address failed checks before proceeding

### During Development
1. **Unit Tests**
   ```bash
   make test-quick
   ```
   Fast feedback during development

2. **Lint**
   ```bash
   make fmt
   make lint
   ```

### Before Committing
1. **All Tests**
   ```bash
   make test-all
   ```

2. **Build**
   ```bash
   make all
   ```

### CI/CD Pipeline
1. **testinfra** - Validate runner environment
2. **Unit Tests** - Quick smoke test
3. **Integration Tests** - Full integration check
4. **E2E Tests** - Production-like validation

## Directory Structure

```
test/
├── e2e/                      # End-to-end tests
│   ├── docker_swarm_test.go  # Docker Swarm E2E
│   ├── swarmkit_test.go      # SwarmKit tests
│   ├── cluster/              # Cluster management
│   ├── scenarios/            # Test scenarios
│   └── fixtures/             # Test fixtures
├── integration/              # Integration tests
│   ├── integration_test.go   # Main tests
│   └── FIRECRACKER_SETUP.md  # Setup guide
├── testinfra/                # Infrastructure validation
│   ├── testinfra_test.go     # Main checks
│   └── checks/               # Individual checkers
└── mocks/                    # Mock implementations
    └── mocks.go
```

## Coverage Goals

| Package | Target | Current | Status |
|---------|--------|---------|--------|
| `pkg/executor` | 90% | 95.2% | ✅ |
| `pkg/translator` | 95% | 98.1% | ✅ |
| `pkg/config` | 85% | 87.3% | ✅ |
| `pkg/lifecycle` | 75% | 74.7% | ⚠️ |
| `pkg/image` | 80% | 60.7% | ⚠️ |
| `pkg/network` | 75% | 59.5% | ⚠️ |

**Overall**: 79.1% average coverage

## Writing Tests

### Unit Test Template
```go
func TestFeature(t *testing.T) {
    tests := []struct {
        name string
        input string
        want  string
        err   bool
    }{
        {"success", "input", "output", false},
        {"failure", "", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Feature(tt.input)
            if (err != nil) != tt.err {
                t.Errorf("Feature() error = %v, wantErr %v", err, tt.err)
            }
            if got != tt.want {
                t.Errorf("Feature() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Test Template
```go
//go:build integration
// +build integration

func TestIntegration_Feature(t *testing.T) {
    if !hasFirecracker() {
        t.Skip("Firecracker not found")
    }

    // Test with real Firecracker
}
```

### E2E Test Template
```go
func TestE2E_Feature(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping in short mode")
    }

    // Create service
    // Verify deployment
    // Cleanup
    t.Cleanup(func() {
        // Cleanup code
    })
}
```

## Common Commands

### Run Specific Test
```bash
# By name
go test -v -run TestExecutor_Prepare ./pkg/executor/

# By pattern
go test -v -run "TestExecutor_.*" ./pkg/executor/
```

### With Timeout
```bash
# Prevent hanging tests
go test -v -timeout 5m ./test/integration/...
```

### Verbose Output
```bash
# See all test output
go test -v ./pkg/...
```

### Race Detector
```bash
# Find race conditions
go test -race ./pkg/...
```

## Troubleshooting

### Tests Fail Randomly
- May be timing issues
- Add proper waits/polling
- Use `t.Parallel()` carefully

### Tests Timeout
- Increase timeout: `-timeout 30m`
- Check for infinite loops
- Verify external dependencies

### "No test files"
- Ensure files end in `_test.go`
- Check you're in the right directory

### Coverage Low
- Use `go tool cover -html=coverage.out` to see what's not covered
- Add tests for missing code paths

## Best Practices

1. **Test Early, Test Often**
   - Write tests alongside code
   - Run tests frequently during development

2. **Keep Tests Fast**
   - Unit tests should be fast
   - Use mocks for external dependencies
   - Move slow tests to integration/e2e

3. **Test Isolation**
   - Tests should not depend on each other
   - Use unique names for resources
   - Clean up after tests

4. **Clear Names**
   - Use descriptive test names
   - Follow `Test<Function>_<Scenario>` pattern

5. **Table-Driven Tests**
   - Use for multiple scenarios
   - Easier to add new cases

6. **Use Testify**
   - `assert.NoError()` for errors
   - `require.NoError()` for fatal errors
   - Table-driven test helpers

## CI/CD Integration

### GitHub Actions Example
```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Check Infrastructure
        run: go test -v ./test/testinfra/...

      - name: Unit Tests
        run: go test -v ./pkg/...

      - name: Integration Tests
        run: go test -v -tags=integration ./test/integration/...

      - name: E2E Tests
        run: go test -v ./test/e2e/...
```

## Resources

### Documentation
- [Unit Testing](unit.md) - Detailed unit testing guide
- [Integration Testing](integration.md) - Integration test setup
- [E2E Testing](e2e.md) - End-to-end testing guide
- [Test Infrastructure](testinfra.md) - Environment validation

### External
- [Go Testing Guide](https://go.dev/doc/tutorial/add-a-test)
- [Testify Assertions](https://github.com/stretchr/testify)
- [Table Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)

## Getting Help

1. Check existing documentation
2. Review test examples in the codebase
3. Search GitHub Issues
4. Ask on Discord

---

**Next Steps**:
- New to testing? Start with [Unit Tests](unit.md)
- Setting up environment? See [Test Infrastructure](testinfra.md)
- Ready for real testing? Try [Integration Tests](integration.md)
- Validate full stack? Run [E2E Tests](e2e.md)
