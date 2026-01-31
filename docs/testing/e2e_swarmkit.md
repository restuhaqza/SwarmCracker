# E2E Testing with SwarmKit

End-to-end tests validate SwarmCracker as a SwarmKit executor using real SwarmKit manager and agents.

## What is SwarmKit?

**SwarmKit** is the orchestration engine behind Docker Swarm. It's a standalone toolkit for building distributed systems using container orchestration.

**Key Points**:
- SwarmKit is the library/protocol
- Docker Swarm is the CLI/product that uses SwarmKit
- SwarmCracker integrates with SwarmKit **agents**, not Docker

## Architecture

```
┌─────────────────────────────────────────┐
│         SwarmKit Manager                │
│     (swarmd --listen-remote-api)        │
└────────────┬────────────────────────────┘
             │ gRPC
             ▼
┌─────────────────────────────────────────┐
│      SwarmKit Agent (SwarmCracker)      │
│  - Receives tasks from manager          │
│  - Runs tasks as Firecracker VMs        │
│  - Reports status back to manager       │
└─────────────────────────────────────────┘
```

## Prerequisites

### 1. Install SwarmKit (swarmd)

```bash
# From source
go install github.com/moby/swarmkit/cmd/swarmd@latest

# Or download binary
wget https://github.com/moby/swarmkit/releases/download/v1.12.0/swarmd-v1.12.0-x86_64
sudo mv swarmd-v1.12.0-x86_64 /usr/local/bin/swarmd
sudo chmod +x /usr/local/bin/swarmd

# Verify
swarmd --version
```

### 2. Other Requirements

- **Firecracker** - MicroVM runtime
- **Kernel** - vmlinux for Firecracker
- **KVM** - Hardware virtualization
- **Container Runtime** - Docker or Podman (for image prep)

## Running E2E Tests

### All SwarmKit E2E Tests
```bash
make e2e-test

# Or
go test -v ./test/e2e/ -run TestE2E_SwarmKit -timeout 30m
```

### Specific Tests
```bash
# Manager and agent setup
go test -v ./test/e2e/ -run TestE2E_SwarmKitClusterFormation

# Service deployment
go test -v ./test/e2e/ -run TestE2E_SwarmKitServiceDeployment

# Scaling
go test -v ./test/e2e/ -run TestE2E_SwarmKitServiceScaling
```

## Test Framework

### Cluster Management

Located in `test/e2e/cluster/`:

#### Manager (`manager.go`)
```go
manager, err := cluster.NewSwarmKitManager("/tmp/manager", "127.0.0.1:4242")
if err != nil {
    t.Fatal(err)
}

if err := manager.Start(); err != nil {
    t.Fatal(err)
}

defer manager.Stop()

// Manager is now ready
addr := manager.GetAddr()
token := manager.GetJoinToken()
```

#### Agent (`agent.go`)
```go
agent, err := cluster.NewSwarmKitAgent(
    "/tmp/agent-1",
    manager.GetAddr(),
    manager.GetJoinToken(),
)
if err != nil {
    t.Fatal(err)
}

if err := agent.Start(); err != nil {
    t.Fatal(err)
}

defer agent.Stop()

// Agent is connected to manager
```

#### Cleanup (`cleanup.go`)
```go
cleanup := cluster.NewCleanupManager()
cleanup.TrackProcess(manager.GetProcess())
cleanup.TrackProcess(agent.GetProcess())
cleanup.TrackStateDir("/tmp/manager")

defer cleanup.Cleanup(ctx)
```

### Test Scenarios

Located in `test/e2e/scenarios/`:

#### Basic Deployment (`basic_deploy.go`)
```go
scenario := scenarios.NewBasicDeployScenario(
    "/tmp/test-dir",
    "test-service",
    "nginx:alpine",
    1, // replicas
)

if err := scenario.Setup(ctx, t); err != nil {
    t.Fatal(err)
}
defer scenario.Teardown(ctx, t)

if err := scenario.Run(ctx, t); err != nil {
    t.Fatal(err)
}
```

## Test Categories

### 1. Cluster Formation Tests

**Purpose**: Verify manager and agent can form a cluster

**What it tests**:
- ✅ Manager starts and listens
- ✅ Agent connects to manager
- ✅ Agent authenticates with token
- ✅ Cluster state is healthy

**Duration**: ~30 seconds

**Example**:
```go
func TestE2E_ClusterFormation(t *testing.T) {
    manager := NewSwarmKitManager(...)
    agent := NewSwarmKitAgent(...)

    // Verify connection
    assert.True(t, manager.IsRunning())
    assert.True(t, agent.IsRunning())
}
```

---

### 2. Service Deployment Tests

**Purpose**: Deploy services through SwarmKit

**What it tests**:
- ✅ Service creation via SwarmKit API
- ✅ Task assignment to agent
- ✅ Task execution in Firecracker VM
- ✅ Status reporting

**Duration**: ~2-5 minutes

**Example**:
```go
func TestE2E_ServiceDeployment(t *testing.T) {
    // Create service
    service := CreateTestService("nginx", 1)

    // Deploy through SwarmKit
    err := swarmClient.CreateService(service)
    require.NoError(t, err)

    // Verify task is running
    tasks := ListTasks(service.ID)
    assert.Equal(t, "RUNNING", tasks[0].Status.State)
}
```

---

### 3. Scaling Tests

**Purpose**: Test service scaling

**What it tests**:
- ✅ Scale up (1 → 3 replicas)
- ✅ Scale down (3 → 1 replicas)
- ✅ Multiple agents distribute tasks
- ✅ Task rebalancing

**Duration**: ~3-5 minutes

---

### 4. Failure Recovery Tests

**Purpose**: Test failure scenarios

**What it tests**:
- ✅ Agent failure detection
- ✅ Task rescheduling
- ✅ Manager failover
- ✅ Network partition recovery

**Duration**: ~5-10 minutes

---

### 5. Update/Rollback Tests

**Purpose**: Test service updates

**What it tests**:
- ✅ Rolling image updates
- ✅ Rollback on failure
- ✅ Update pausing
- ✅ Parallel updates

**Duration**: ~5 minutes

## Difference from Docker Swarm Tests

We have **two types** of E2E tests:

### SwarmKit E2E Tests (`test/e2e/swarmkit_test.go`)
- **Target**: SwarmKit agents and managers
- **Purpose**: Test SwarmCracker as SwarmKit executor
- **Direct**: Uses swarmd binary directly
- **Real**: Tests actual SwarmKit integration
- **Use**: Validates executor implementation

### Docker Swarm Tests (`test/e2e/docker_swarm_test.go`)
- **Target**: Docker Swarm CLI
- **Purpose**: Validate Docker Swarm environment
- **Indirect**: Uses docker CLI
- **Validation**: Ensures Docker Swarm works
- **Use**: Environment validation only

**Key**: SwarmCracker integrates with **SwarmKit agents**, not Docker Swarm directly.

## Example E2E Test

```go
func TestE2E_SwarmKitEndToEnd(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E in short mode")
    }

    // Check prerequisites
    if !hasSwarmd() {
        t.Skip("swarmd not found")
    }

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
    defer cancel()

    // Setup cluster
    manager, _ := cluster.NewSwarmKitManager(stateDir, "127.0.0.1:4242")
    manager.Start()
    defer manager.Stop()

    agent, _ := cluster.NewSwarmKitAgent(
        agentStateDir,
        manager.GetAddr(),
        manager.GetJoinToken(),
    )
    agent.Start()
    defer agent.Stop()

    // Create service
    service := CreateTestService("nginx:alpine", 1)

    // Deploy service through SwarmKit API
    client := NewSwarmKitClient(manager.GetAddr())
    err := client.CreateService(ctx, service)
    require.NoError(t, err)

    // Wait for task to be assigned
    var task *api.Task
    require.Eventually(t, func() bool {
        tasks, _ := client.ListTasks(ctx)
        for _, t := range tasks {
            if t.ServiceID == service.ID {
                task = t
                return true
            }
        }
        return false
    }, 2*time.Minute, 5*time.Second)

    // Verify task status
    assert.Equal(t, api.TaskState_RUNNING, task.Status.State)

    // Cleanup
    client.RemoveService(ctx, service.ID)
}
```

## Troubleshooting

### "swarmd not found"
```bash
# Install swarmd
go install github.com/moby/swarmkit/cmd/swarmd@latest

# Verify
which swarmd
swarmd --version
```

### "Agent cannot connect to manager"
```bash
# Check manager is listening
netstat -tlnp | grep 4242

# Check firewall
sudo ufw status
sudo ufw allow 4242/tcp
```

### "Manager not ready"
```bash
# Check manager logs
# Manager logs to stdout/stderr in tests

# Check socket exists
ls -la /tmp/manager*/swarmd.sock
```

### "Tasks not assigned"
```bash
# Check agent is connected
# In agent logs, look for "registered" message

# Check manager sees agent
swarmctl -m 127.0.0.1:4242 nodes ls
```

## Architecture Diagram

```
┌─────────────────────────────────────────────────────┐
│                  Test Suite                        │
│  (test/e2e/swarmkit_test.go)                      │
└────────────┬────────────────────────────────────────┘
             │
             │ Starts
             ▼
┌─────────────────────────────────────────────────────┐
│         SwarmKit Manager (swarmd)                   │
│  - Listens on 127.0.0.1:4242                       │
│  - Manages cluster state                          │
│  - Assigns tasks to agents                        │
└────────────┬────────────────────────────────────────┘
             │
             │ gRPC (tasks)
             ▼
┌─────────────────────────────────────────────────────┐
│      SwarmKit Agent + SwarmCracker Executor         │
│  - Receives tasks from manager                     │
│  - SwarmCracker executor runs tasks as VMs         │
│  - Reports VM status back to manager               │
└─────────────────────────────────────────────────────┘
             │
             │ Firecracker API
             ▼
┌─────────────────────────────────────────────────────┐
│          Firecracker MicroVM                        │
│  - Runs container workloads                       │
│  - Hardware isolation                             │
└─────────────────────────────────────────────────────┘
```

## Best Practices

1. **Use CleanupManager**: Always track and cleanup resources
2. **Set Timeouts**: SwarmKit operations can hang
3. **Check Prerequisites**: Skip tests if swarmd not available
4. **Unique Names**: Use random strings for service/node IDs
5. **Log Everything**: E2E tests run infrequently, log verbosely
6. **Test Real Scenarios**: Use actual images, not mocks
7. **Verify Cleanup**: Ensure no VMs or sockets remain

## CI/CD Integration

```yaml
name: E2E Tests

on: [push, pull_request]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Install swarmd
        run: |
          go install github.com/moby/swarmkit/cmd/swarmd@latest

      - name: Install Firecracker
        run: |
          wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.8.0/firecracker-v1.8.0-x86_64
          sudo mv firecracker-v1.8.0-x86_64 /usr/local/bin/firecracker
          sudo chmod +x /usr/local/bin/firecracker

      - name: Run E2E Tests
        run: |
          go test -v ./test/e2e/ -run TestE2E_SwarmKit -timeout 30m
```

## Next Steps

### Immediate
- [ ] Implement real SwarmKit API calls in tests
- [ ] Add service deployment test
- [ ] Add scaling test
- [ ] Add failure recovery test

### Future
- [ ] Multi-agent tests
- [ ] Network isolation tests
- [ ] Performance benchmarks
- [ ] Chaos engineering tests

## Resources

- [SwarmKit GitHub](https://github.com/moby/swarmkit)
- [SwarmKit Documentation](https://github.com/moby/swarmkit/tree/master/docs)
- [SwarmKit API](https://github.com/moby/swarmkit/blob/master/api/swagger.yaml)
- [Docker Swarm vs SwarmKit](https://docs.docker.com/engine/swarm/)

---

**Related**: [Unit Tests](unit.md) | [Integration Tests](integration.md) | [Test Infrastructure](testinfra.md)
