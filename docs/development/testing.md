# Testing Guide

Comprehensive guide for testing SwarmCracker.

## Test Overview

SwarmCracker uses a multi-layered testing approach:

- **Unit Tests**: Test individual functions and methods
- **Integration Tests**: Test component interactions
- **End-to-End Tests**: Test full workflows
- **Benchmarks**: Measure performance
- **Property Tests**: Test with randomized inputs (future)

## Running Tests

### Quick Start

```bash
# Run all tests
make test

# Run with coverage
make test

# Run specific package
go test ./pkg/executor

# Run with race detector
make race
```

### Test Commands

```bash
# Run all tests in pkg/
go test ./pkg/...

# Run tests verbosely
go test -v ./pkg/executor

# Run specific test
go test -v -run TestExecutor_Prepare ./pkg/executor

# Run tests with coverage
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out -o coverage.html

# Run tests with race detector
go test -race ./pkg/...

# Run benchmarks
go test -bench=. -benchmem ./pkg/executor

# Skip integration tests
go test -short ./pkg/...
```

## Test Structure

### Directory Layout

```
swarmcracker/
├── pkg/
│   ├── executor/
│   │   ├── executor.go         # Implementation
│   │   └── executor_test.go    # Tests
│   └── translator/
│       ├── translator.go
│       └── translator_test.go
├── test/
│   ├── integration/            # Integration tests
│   │   └── integration_test.go
│   └── mocks/                  # Mock implementations
│       └── mocks.go
```

### Test File Organization

```go
package translator

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// Test constructor
func TestNewTaskTranslator(t *testing.T) {
    // ... implementation
}

// Test main function
func TestTaskTranslator_Translate(t *testing.T) {
    // ... implementation
}

// Test helper functions
func TestTaskTranslator_buildBootArgs(t *testing.T) {
    // ... implementation
}

// Benchmark
func BenchmarkTaskTranslator_Translate(b *testing.B) {
    // ... implementation
}
```

## Writing Tests

### Basic Test Pattern

```go
func TestFunctionName(t *testing.T) {
    tests := []struct {
        name    string
        input   InputType
        want    OutputType
        wantErr bool
    }{
        {
            name:    "success case",
            input:   validInput,
            want:    expectedOutput,
            wantErr: false,
        },
        {
            name:    "error case",
            input:   invalidInput,
            want:    nil,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup
            sut := NewSystemUnderTest()

            // Execute
            got, err := sut.Function(tt.input)

            // Assert
            if tt.wantErr {
                assert.Error(t, err)
                assert.Nil(t, got)
            } else {
                require.NoError(t, err) // Fail fast on error
                assert.Equal(t, tt.want, got)
            }
        })
    }
}
```

### Using Mocks

```go
package executor_test

import (
    "context"
    "testing"
    "github.com/restuhaqza/swarmcracker/test/mocks"
    "github.com/stretchr/testify/assert"
)

func TestExecutor_Prepare(t *testing.T) {
    // Create mocks
    vmm := mocks.NewMockVMMManager()
    img := mocks.NewMockImagePreparer()
    net := mocks.NewMockNetworkManager()

    // Create executor with mocks
    exec, _ := NewExecutor(config, vmm, nil, img, net)

    // Create test task
    task := mocks.NewTestTask("task-1", "nginx:latest")

    // Test
    err := exec.Prepare(context.Background(), task)

    // Assert
    assert.NoError(t, err)
    assert.True(t, img.IsTaskPrepared("task-1"))
    assert.True(t, net.IsTaskPrepared("task-1"))
}
```

### Testing Error Handling

```go
func TestExecutor_Start_Error(t *testing.T) {
    tests := []struct {
        name      string
        setupMock func(*mocks.MockVMMManager)
        wantErr   string
    }{
        {
            name: "VMM start fails",
            setupMock: func(vmm *mocks.MockVMMManager) {
                vmm.ShouldFail = true
            },
            wantErr: "failed to start VM",
        },
        {
            name: "translation fails",
            setupMock: func(vmm *mocks.MockVMMManager) {
                // VMM is fine, but translator will fail
            },
            wantErr: "task translation failed",
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Setup
            vmm := mocks.NewMockVMMManager()
            if tt.setupMock != nil {
                tt.setupMock(vmm)
            }

            exec := NewTestExecutor()

            // Execute
            err := exec.Start(context.Background(), task)

            // Assert
            assert.Error(t, err)
            assert.Contains(t, err.Error(), tt.wantErr)
        })
    }
}
```

### Testing Concurrent Code

```go
func TestConcurrentStart(t *testing.T) {
    exec := NewTestExecutor()

    const goroutines = 10
    errors := make(chan error, goroutines)

    for i := 0; i < goroutines; i++ {
        go func(id int) {
            task := mocks.NewTestTask(fmt.Sprintf("task-%d", id), "nginx")
            errors <- exec.Start(context.Background(), task)
        }(i)
    }

    // Collect results
    for i := 0; i < goroutines; i++ {
        err := <-errors
        assert.NoError(t, err)
    }
}
```

## Test Categories

### Unit Tests

Test individual functions/methods in isolation.

**Example:** `pkg/translator/translator_test.go`

```go
func TestTaskTranslator_buildBootArgs(t *testing.T) {
    container := &types.Container{
        Command: []string{"/bin/sh"},
        Args:    []string{"-c", "echo hello"},
    }

    translator := NewTaskTranslator(nil)
    result := translator.buildBootArgs(container)

    assert.Contains(t, result, "console=ttyS0")
    assert.Contains(t, result, "/bin/sh")
    assert.Contains(t, result, "echo hello")
}
```

### Integration Tests

Test multiple components working together.

**Example:** `pkg/executor/executor_test.go`

```go
func TestFirecrackerExecutor_FullLifecycle(t *testing.T) {
    // Setup all components
    vmm := mocks.NewMockVMMManager()
    trans := mocks.NewMockTaskTranslator()
    img := mocks.NewMockImagePreparer()
    net := mocks.NewMockNetworkManager()

    exec, _ := NewExecutor(config, vmm, trans, img, net)
    task := mocks.NewTestTask("task-1", "nginx")

    // Test full lifecycle
    err := exec.Prepare(ctx, task)
    require.NoError(t, err)

    err = exec.Start(ctx, task)
    require.NoError(t, err)

    status, err := exec.Describe(ctx, task)
    require.NoError(t, err)
    assert.Equal(t, types.TaskState_RUNNING, status.State)

    err = exec.Stop(ctx, task)
    require.NoError(t, err)

    err = exec.Remove(ctx, task)
    require.NoError(t, err)
}
```

### Integration Tests (with Firecracker)

Mark with build tag to skip in normal runs.

```go
//go:build integration
// +build integration

package integration_test

import (
    "testing"
    "os/exec"
)

func TestRealFirecracker(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }

    // Check if Firecracker is available
    if _, err := exec.LookPath("firecracker"); err != nil {
        t.Skip("Firecracker not installed")
    }

    // Run real Firecracker VM
    // ...
}
```

### Benchmark Tests

Measure performance of critical paths.

```go
func BenchmarkTaskTranslator_Translate(b *testing.B) {
    task := &types.Task{
        ID:        "bench-task",
        Spec:      types.TaskSpec{
            Runtime: &types.Container{
                Image: "nginx:latest",
                Command: []string{"nginx"},
            },
        },
        Annotations: map[string]string{
            "rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
        },
    }

    translator := NewTaskTranslator(nil)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, _ = translator.Translate(task)
    }
}
```

Run benchmarks:
```bash
go test -bench=. -benchmem ./pkg/translator

# Output:
# BenchmarkTaskTranslator_Translate-8    50000    32000 ns/op    4096 B/op    12 allocs/op
```

## Test Coverage

### Current Coverage

```
pkg/translator     98.1%  (excellent)
pkg/executor       95.2%  (excellent)
pkg/config         87.3%  (good)
pkg/lifecycle      54.4%  (moderate)
pkg/network         9.1%  (needs work)
pkg/image           0%    (pending)
```

### Improving Coverage

1. **Add missing tests:**
   ```bash
   # Find untested code
   go test -coverprofile=coverage.out ./pkg/...
   go tool cover -html=coverage.out

   # Look for red lines (uncovered)
   ```

2. **Target thresholds:**
   - Core logic: 80%+
   - Public APIs: 90%+
   - Critical paths: 95%+

3. **Edge cases:**
   - Nil/empty inputs
   - Error conditions
   - Boundary values
   - Concurrent access

## Mocks

### Available Mocks

Located in `test/mocks/mocks.go`:

- `MockVMMManager` - VM lifecycle mock
- `MockTaskTranslator` - Translation mock
- `MockImagePreparer` - Image prep mock
- `MockNetworkManager` - Network mock

### Using Mocks

```go
import "github.com/restuhaqza/swarmcracker/test/mocks"

// Create mock
vmm := mocks.NewMockVMMManager()

// Configure behavior
vmm.ShouldFail = true
vmm.SetWaitStatus(&types.TaskStatus{
    State: types.TaskState_RUNNING,
})

// Use in tests
exec, _ := NewExecutor(config, vmm, ...)

// Verify calls
assert.True(t, vmm.StartCalled)
assert.True(t, vmm.IsTaskStarted("task-1"))
```

### Creating New Mocks

```go
// In test/mocks/mocks.go

type MockNewComponent struct {
    Called bool
    Input  interface{}
    Output interface{}
    Err    error
}

func NewMockNewComponent() *MockNewComponent {
    return &MockNewComponent{}
}

func (m *MockNewComponent) DoSomething(ctx context.Context, input interface{}) (interface{}, error) {
    m.Called = true
    m.Input = input
    return m.Output, m.Err
}
```

## CI/CD Integration

### GitHub Actions

Tests run automatically on PRs:

```yaml
name: Test
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - run: make test
      - run: make lint
```

### Running Locally Before Push

```bash
# Run full test suite
make test

# Run linters
make lint

# Format code
make fmt

# Build
make build
```

## Debugging Tests

### Verbose Output

```bash
go test -v ./pkg/executor
```

### Stop on First Error

```bash
go test -failfast ./pkg/...
```

### Test Specific Function

```bash
go test -v -run TestExecutor_Prepare ./pkg/executor
```

### Print Test Output

```go
func TestDebug(t *testing.T) {
    t.Log("Debug info:", someVariable)

    // Or use fmt.Printf in development
    // fmt.Printf("Debug: %+v\n", someVariable)
}
```

### Using Delve

```bash
# Install delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug test
dlv test ./pkg/executor -test.run TestExecutor_Prepare
```

## Best Practices

### DO ✅

- Write tests alongside code
- Use table-driven tests for multiple cases
- Test error paths
- Use descriptive test names
- Keep tests fast (use mocks)
- Test concurrent code with `go test -race`
- Clean up resources in `defer`

### DON'T ❌

- Don't skip tests without good reason
- Don't ignore test failures
- Don't test private functions (test public API)
- Don't make tests dependent on each other
- Don't use `time.Sleep` (use channels or timeouts)
- Don't hardcode paths (use `t.TempDir()`)

### Example: Good Test

```go
func TestExecutor_Prepare_ImageError(t *testing.T) {
    // Arrange
    img := mocks.NewMockImagePreparer()
    img.ShouldFail = true
    exec := NewTestExecutor(img)
    task := mocks.NewTestTask("task-1", "nginx")

    // Act
    err := exec.Prepare(context.Background(), task)

    // Assert
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "image preparation failed")
}
```

## Troubleshooting

### Tests Fail with "Permission Denied"

```bash
# Add user to kvm group
sudo usermod -a -G kvm $USER

# Re-login for changes to take effect
```

### Integration Tests Time Out

```bash
# Skip integration tests
go test -short ./pkg/...

# Or increase timeout
go test -timeout 10m ./test/integration/...
```

### Race Detector Errors

```bash
# Run with race detector
go test -race ./pkg/...

# Fix data races by:
# - Using mutexes
# - Using channels
# - Making copies instead of sharing
```

## Resources

- [Go Testing Guide](https://go.dev/doc/tutorial/add-a-test)
- [Testify Documentation](https://github.com/stretchr/testify)
- [Go Benchmarks](https://go.dev/pkg/testing/#hdr-Benchmarks)
- [Table Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)
