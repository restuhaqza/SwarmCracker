# Testing Overview

> Test strategy, coverage, and results.

---

## Test Levels

| Level | Scope | Run Command |
|-------|-------|-------------|
| **Unit** | Individual functions | `go test ./pkg/...` |
| **Integration** | Component interactions | `go test -run Integration` |
| **E2E** | Full cluster | `./test-automation/e2e.sh` |

---

## Coverage

| Package | Coverage | Status |
|---------|----------|--------|
| `pkg/config` | ~85% | ✅ |
| `pkg/executor` | ~70% | ✅ |
| `pkg/network` | ~75% | ✅ |
| `pkg/jailer` | ~65% | ✅ |
| `pkg/storage` | ~80% | ✅ |
| `pkg/snapshot` | ~60% | ⚠️ Needs more tests |
| `pkg/swarmkit` | ~50% | ⚠️ Needs more tests |

---

## Test Summary

### Passing Tests

| Package | Tests | Status |
|---------|-------|--------|
| `pkg/jailer` | 24 | ✅ All passing |
| `pkg/swarmkit` | 10 | ✅ Passing (2 skipped) |
| `pkg/network` | 15 | ✅ All passing |
| `pkg/storage` | 18 | ✅ All passing |

### Known Gaps

- Jailer integration tests skipped in CI (needs root)
- Swarmkit tests skip jailer-dependent tests
- E2E tests require live VM cluster

---

## Running Tests

### Unit Tests

```bash
make test

# Or directly
go test ./pkg/...
```

### Integration Tests

```bash
go test -run Integration ./pkg/executor/...
go test -run Integration ./pkg/network/...
```

### E2E Tests

```bash
# Requires running cluster
./test-automation/e2e-test.sh
```

---

## Test Infrastructure

### Test Fixtures

Located in `test-automation/fixtures/`:

- Sample rootfs images
- Test configurations
- Mock Firecracker binaries

### Mock Components

- Mock executor for unit tests
- Mock network for integration tests
- Test sockets for SwarmKit

---

## Related Docs

| Topic | Document |
|-------|----------|
| Development guide | [Development Guide](../development/) |
| Architecture | [Architecture Overview](../architecture/) |

---

**See Also:** [Development Guide](../development/) | [Architecture](../architecture/)