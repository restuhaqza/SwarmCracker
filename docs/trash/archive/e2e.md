# E2E Testing with Docker Swarm

**Note**: These tests validate the **Docker Swarm environment**, not SwarmKit integration.

For **SwarmKit E2E tests**, see [E2E Testing with SwarmKit](e2e_swarmkit.md).

## Purpose

Docker Swarm E2E tests validate:
- ✅ Docker Swarm is properly initialized
- ✅ Service deployment works
- ✅ Service scaling functions
- ✅ Task scheduling operates

These are **environment validation tests**, not SwarmCracker executor tests.

## When to Use

### Use Docker Swarm Tests For:
- Validating Docker Swarm installation
- Testing Docker Swarm functionality
- Environment smoke tests
- CI/CD pre-flight checks

### Use SwarmKit Tests For:
- Testing SwarmCracker executor
- Validating SwarmKit integration
- End-to-end executor workflow
- Real cluster testing

## Running Docker Swarm E2E Tests

### All Tests
```bash
go test -v ./test/e2e/ -run TestE2E_DockerSwarm -timeout 10m
```

### Specific Tests
```bash
# Basic test
go test -v ./test/e2e/ -run TestE2E_DockerSwarmBasic

# Scaling test
go test -v ./test/e2e/ -run TestE2E_DockerSwarmScale
```

## Test Coverage

### TestE2E_DockerSwarmBasic
- Swarm initialization check
- Node listing
- Service creation
- Service inspection
- Task listing
- Replica verification

### TestE2E_DockerSwarmScale
- Scale up (1 → 3 replicas)
- Scale down (3 → 1 replica)
- Task stability verification

## Prerequisites

### Docker Swarm
```bash
# Initialize Swarm
docker swarm init --advertise-addr <ip>

# Verify
docker info | grep Swarm
```

### No Other Requirements
- No Firecracker needed
- No KVM needed
- No swarmd needed

## Quick Reference

| Test | Duration | Purpose |
|------|----------|---------|
| `TestE2E_DockerSwarmBasic` | ~17s | Validate Swarm works |
| `TestE2E_DockerSwarmScale` | ~33s | Test scaling |

## Documentation

**For SwarmKit E2E testing** (the real executor tests), see:
- **[E2E Testing with SwarmKit](e2e_swarmkit.md)** ← Use this for SwarmCracker testing

---

**Related**: [E2E Testing with SwarmKit](e2e_swarmkit.md) | [Unit Tests](unit.md) | [Integration Tests](integration.md)
