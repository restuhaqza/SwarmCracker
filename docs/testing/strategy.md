# E2E Testing Strategy Clarification

## Overview

SwarmCracker has **two types** of E2E tests with different purposes:

## 1. Docker Swarm E2E Tests

**File**: `test/e2e/docker_swarm_test.go`
**Documentation**: [docs/testing/e2e.md](e2e.md)

### Purpose
Validate **Docker Swarm environment** is working

### What It Tests
- ✅ Docker Swarm initialization
- ✅ Service creation via Docker CLI
- ✅ Service scaling
- ✅ Task listing

### Use For
- Environment validation
- CI/CD smoke tests
- Verifying Docker Swarm installation

### Does NOT Test
- ❌ SwarmCracker executor
- ❌ SwarmKit integration
- ❌ Firecracker VMs
- ❌ Real orchestration

### Example
```bash
# Quick smoke test
go test -v ./test/e2e/ -run TestE2E_DockerSwarmBasic
```

---

## 2. SwarmKit E2E Tests ⭐

**Files**: `test/e2e/swarmkit_test.go`, `test/e2e/scenarios/`
**Documentation**: [docs/testing/e2e_swarmkit.md](e2e_swarmkit.md)

### Purpose
Test **SwarmCracker as SwarmKit executor**

### What It Tests
- ✅ SwarmKit manager/agent cluster
- ✅ SwarmCracker executor integration
- ✅ Task assignment and execution
- ✅ Firecracker VM lifecycle
- ✅ Real orchestration scenarios

### Use For
- Testing SwarmCracker executor
- Validating SwarmKit integration
- End-to-end workflow validation
- Real cluster testing

### Architecture
```
SwarmKit Manager → SwarmKit Agent (SwarmCracker) → Firecracker VM
```

### Example
```bash
# Real E2E test
go test -v ./test/e2e/ -run TestE2E_SwarmKit -timeout 30m
```

---

## Key Difference

| Aspect | Docker Swarm Tests | SwarmKit Tests |
|--------|-------------------|----------------|
| **Target** | Docker Swarm CLI | SwarmKit agents |
| **Integration** | None | SwarmCracker executor |
| **Workload** | Docker containers | Firecracker VMs |
| **Purpose** | Environment validation | Executor testing |
| **Real?** | No (Docker orchestrates) | Yes (SwarmKit orchestrates) |

## Which Should You Use?

### For Environment Validation
→ Use **Docker Swarm E2E tests** (`TestE2E_DockerSwarm*`)

### For Testing SwarmCracker
→ Use **SwarmKit E2E tests** (`TestE2E_SwarmKit*`)

### For CI/CD
1. Run **testinfra** first (validate environment)
2. Run **unit tests** (quick smoke test)
3. Run **Docker Swarm E2E** (validate Swarm)
4. Run **SwarmKit E2E** (test executor) ← **Important**

## Current Status

### Docker Swarm E2E Tests
✅ **Implemented and passing**
- Basic deployment: PASS
- Scaling: PASS
- Service lifecycle: PASS

### SwarmKit E2E Tests
⏸️ **Framework ready, implementation pending**
- Cluster management: ✅ Code complete
- Test scenarios: ✅ Framework ready
- SwarmKit integration: ⏸️ Needs swarmd installation
- Real tests: ⏸️ Need implementation

## Next Steps for SwarmKit E2E

### Immediate
1. **Install swarmd**
   ```bash
   go install github.com/moby/swarmkit/cmd/swarmd@latest
   ```

2. **Implement real SwarmKit tests**
   - Connect to SwarmKit API
   - Deploy services via SwarmKit
   - Verify SwarmCracker receives tasks
   - Validate Firecracker VMs start

3. **Add to CI/CD**
   - Run SwarmKit E2E in tests
   - Validate executor integration

### Example SwarmKit E2E Test
```go
func TestE2E_SwarmKitRealDeployment(t *testing.T) {
    // Start manager
    manager := NewSwarmKitManager(...)
    manager.Start()
    defer manager.Stop()

    // Start agent with SwarmCracker executor
    agent := NewSwarmKitAgent(...)
    agent.SetExecutor("/path/to/swarmcracker")
    agent.Start()
    defer agent.Stop()

    // Create service via SwarmKit API
    client := NewSwarmKitClient(manager.Addr())
    service := CreateTestService("nginx:alpine", 1)
    client.CreateService(service)

    // Wait for SwarmCracker to receive task
    // Verify Firecracker VM starts
    // Check task status
}
```

## Summary

- **Docker Swarm E2E**: Environment validation ✅
- **SwarmKit E2E**: Real executor testing ⏸️ (ready for implementation)

**For testing SwarmCracker, use SwarmKit E2E tests.**

---

**Documentation**:
- [E2E with Docker Swarm](e2e.md)
- [E2E with SwarmKit](e2e_swarmkit.md) ← **Main focus**
- [Testing Overview](README.md)
