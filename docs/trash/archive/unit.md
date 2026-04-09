# Unit Testing

Unit tests verify individual functions and methods in isolation. They are fast and provide quick feedback during development.

## Running Unit Tests

### Quick Test
```bash
# Run all unit tests
make test

# Or with go test
go test ./pkg/...
```

### Specific Package
```bash
# Test specific package
go test -v ./pkg/executor/...
go test -v ./pkg/translator/...
go test -v ./pkg/lifecycle/...
```

### With Coverage
```bash
# Generate coverage report
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out -o coverage.html
```

### Race Detector
```bash
# Run with race detector
go test -race ./pkg/...
```

## Writing Unit Tests

### Basic Structure
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

## Test Organization

```
pkg/
├── executor/
│   ├── executor.go
│   └── executor_test.go       # Unit tests
├── translator/
│   ├── translator.go
│   └── translator_test.go
├── lifecycle/
│   ├── vmm.go
│   ├── vmm_test.go
│   ├── vmm_advanced_test.go   # Advanced tests
│   └── vmm_boost_test.go      # Boost coverage
└── ...
```

## Coverage Goals

| Package | Target | Current |
|---------|--------|---------|
| `pkg/executor` | 90% | 95.2% ✅ |
| `pkg/translator` | 95% | 98.1% ✅ |
| `pkg/config` | 85% | 87.3% ✅ |
| `pkg/lifecycle` | 75% | 74.7% ⚠️ |
| `pkg/image` | 80% | 60.7% ⚠️ |
| `pkg/network` | 75% | 59.5% ⚠️ |

## Best Practices

### 1. Table-Driven Tests
Use table-driven tests for multiple scenarios:
```go
tests := []struct {
    name string
    input string
    want string
}{
    {"empty", "", ""},
    {"simple", "test", "test"},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // test logic
    })
}
```

### 2. Subtests
Use subtests for better organization:
```go
func TestExecutor(t *testing.T) {
    t.Run("Prepare", testPrepare)
    t.Run("Start", testStart)
    t.Run("Stop", testStop)
}
```

### 3. Cleanup
Use `t.Cleanup()` for resource cleanup:
```go
func TestSomething(t *testing.T) {
    tmpDir := t.TempDir()
    file := createFile(tmpDir)

    t.Cleanup(func() {
        os.RemoveAll(tmpDir)
    })

    // Test logic
}
```

### 4. Context
Always accept context as first parameter:
```go
func (e *Executor) Prepare(ctx context.Context, task *Task) error {
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }
    // ... work ...
}
```

## Common Patterns

### Test Helpers
```go
// Create test executor
func NewTestExecutor(t *testing.T) *Executor {
    t.Helper()
    cfg := &Config{}
    cfg.SetDefaults()
    return NewExecutor(cfg)
}

// Create test task
func NewTestTask(t *testing.T, id, image string) *Task {
    t.Helper()
    return &Task{
        ID: id,
        Spec: TaskSpec{
            Runtime: &Container{Image: image},
        },
    }
}
```

### Error Assertions
```go
// Expect error
assert.Error(t, err)
assert.ErrorIs(t, err, ExpectedError)
assert.ErrorContains(t, err, "expected message")

// Expect no error
assert.NoError(t, err)
require.NoError(t, err)  // Fatal if error
```

## Running Specific Tests

### By Name
```bash
# Run specific test
go test -v -run TestExecutor_Prepare ./pkg/executor/

# Run tests matching pattern
go test -v -run "TestExecutor_.*Prepare" ./pkg/executor/
```

### By Tag
```bash
# Skip long tests
go test -short ./...

# Run verbose
go test -v ./...
```

## Benchmarks

### Writing Benchmarks
```go
func BenchmarkExecutor_Start(b *testing.B) {
    exec := NewTestExecutor()
    task := NewTestTask("bench", "nginx")

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        exec.Start(context.Background(), task)
    }
}
```

### Running Benchmarks
```bash
go test -bench=. -benchmem ./pkg/executor/
```

## Examples

### Example 1: Simple Test
```go
func TestAdd(t *testing.T) {
    result := Add(2, 3)
    if result != 5 {
        t.Errorf("Add(2, 3) = %d; want 5", result)
    }
}
```

### Example 2: Table-Driven Test
```go
func TestTranslate(t *testing.T) {
    tests := []struct {
        task    *Task
        wantErr bool
    }{
        {ValidTask(), false},
        {InvalidTask(), true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            _, err := translator.Translate(tt.task)
            if (err != nil) != tt.wantErr {
                t.Errorf("Translate() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

## Troubleshooting

### "No test files"
```bash
# Ensure files are named *_test.go
ls pkg/*_test.go
```

### "Package not found"
```bash
# Run from project root
cd ~/path/to/swarmcracker
go test ./pkg/...
```

### Coverage too low
```bash
# See which lines aren't covered
go tool cover -html=coverage.out
```

## Resources

- [Go Testing Guide](https://go.dev/doc/tutorial/add-a-test)
- [Testify Documentation](https://github.com/stretchr/testify)
- [Table Driven Tests](https://dave.cheney.net/2019/05/07/prefer-table-driven-tests)

---

**Related**: [Integration Tests](integration.md) | [E2E Tests](e2e.md) | [Test Infrastructure](testinfra.md)
