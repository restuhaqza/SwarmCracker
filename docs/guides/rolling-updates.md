# Rolling Updates with SwarmCracker

SwarmCracker supports **zero-downtime rolling updates** for services running as Firecracker microVMs, using SwarmKit's native orchestration capabilities.

## How It Works

Rolling updates are orchestrated by the **SwarmKit manager**, not the executor. The process:

1. **Manager creates new task** with updated service specification
2. **SwarmCracker starts new Firecracker VM** with the new image/configuration
3. **VM reports RUNNING status** via ContainerStatus API
4. **Manager waits for Monitor period** (default: 5 minutes) to ensure stability
5. **Manager shuts down old task** using graceful shutdown (30s timeout)
6. **Process repeats** until all replicas are updated

## Update Configuration

Configure rolling updates in your service spec:

```go
service.Spec.Update = &api.UpdateConfig{
    Parallelism:     1,              // Update 1 task at a time
    Delay:           30 * time.Second, // Wait 30s between updates
    FailureAction:   api.UpdateConfig_PAUSE, // Pause on failure
    Monitor:         &types.Duration{Nanos: 300000000000}, // 5 minutes
    MaxFailureRatio: 0.1,            // Allow 10% failures
    Order:           api.UpdateConfig_STOP_FIRST, // or START_FIRST
}
```

### Configuration Options

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Parallelism` | uint64 | 1 | Number of tasks to update simultaneously. `0` = unlimited |
| `Delay` | duration | 0s | Time to wait between updating individual tasks |
| `FailureAction` | enum | `PAUSE` | Action on failure: `CONTINUE`, `PAUSE`, `ROLLBACK` |
| `Monitor` | duration | 5m | How long to monitor new tasks for failure |
| `MaxFailureRatio` | float32 | 0.0 | Fraction of failures before triggering failure action |
| `Order` | enum | `STOP_FIRST` | Update order: `STOP_FIRST` or `START_FIRST` |

## Update Order Strategies

### STOP_FIRST (Default)
- Stops old task before starting new one
- Ensures no overlap (useful for stateful services)
- Brief downtime during transition

### START_FIRST
- Starts new task before stopping old one
- True zero-downtime (both run briefly in parallel)
- Requires sufficient cluster resources

## Health Checks

SwarmCracker reports VM health through the **ContainerStatus** API:

```go
func (c *Controller) ContainerStatus(ctx context.Context) (*api.ContainerStatus, error) {
    return &api.ContainerStatus{
        ContainerID: c.task.ID,
        PID:         pid,      // Firecracker process PID
        ExitCode:    0,        // 0 = running, non-zero = exited
    }, nil
}
```

### What Gets Reported

| Field | Value | Description |
|-------|-------|-------------|
| `ContainerID` | Task ID | Unique identifier for the Firecracker VM |
| `PID` | Process ID | Firecracker VMM process ID |
| `ExitCode` | 0 or 1 | 0 = running, 1 = exited/failed |

### VM Readiness

The SwarmKit manager considers a task **ready** when:
1. VM process is running (`IsRunning()` returns true)
2. Firecracker API is responsive (`CheckVMAPIHealth()` returns true)
3. Task state is `RUNNING`

## Graceful Shutdown

During rolling updates, old tasks are shut down gracefully:

- **Timeout**: 30 seconds
- **Process**: 
  1. Send SIGTERM to Firecracker process
  2. Wait for VM to exit cleanly
  3. Cleanup network resources (TAP devices, dnsmasq entries)
  4. Remove socket files and rootfs images

If graceful shutdown fails, SwarmKit can be configured to force-terminate after the timeout.

## Example: Rolling Update Flow

```bash
# Initial service: nginx:1.24, 3 replicas
$ swarmctl service ls
ID                          NAME        REPLICAS
abc123                      web-app     3/3

# Update to nginx:1.25 with rolling update config
$ swarmctl service update web-app \
    --image nginx:1.25 \
    --update-parallelism 1 \
    --update-delay 30s \
    --update-failure-action pause \
    --update-monitor 5m

# Watch update progress
$ swarmctl service tasks web-app
ID                          NAME            DESIRED STATE   CURRENT STATE
def456                      web-app.1       RUNNING         RUNNING (updated)
ghi789                      web-app.2       RUNNING         RUNNING (old)
jkl012                      web-app.3       RUNNING         RUNNING (old)

# After 30s delay, next replica updates...
$ swarmctl service tasks web-app
ID                          NAME            DESIRED STATE   CURRENT STATE
def456                      web-app.1       RUNNING         RUNNING (updated)
mno345                      web-app.2       RUNNING         RUNNING (updated)
jkl012                      web-app.3       RUNNING         RUNNING (old)

# Update complete
$ swarmctl service tasks web-app
ID                          NAME            DESIRED STATE   CURRENT STATE
def456                      web-app.1       RUNNING         RUNNING (updated)
mno345                      web-app.2       RUNNING         RUNNING (updated)
pqr678                      web-app.3       RUNNING         RUNNING (updated)
```

## Failure Handling

### Update Pauses

If `FailureAction` is set to `PAUSE` and failures exceed `MaxFailureRatio`:

```
Update paused: failure ratio 0.33 exceeds max 0.1

To continue:   swarmctl service update web-app --update-continue
To rollback:   swarmctl service update web-app --rollback
```

### Common Failure Scenarios

| Failure | Cause | Resolution |
|---------|-------|------------|
| VM fails to start | Insufficient resources, invalid image | Check logs, verify image, add resources |
| VM crashes immediately | Application error, missing config | Check application logs, verify configs/secrets |
| VM starts but unhealthy | Application not responding | Check health check configuration, application logs |
| Network isolation | VXLAN/bridge misconfiguration | Verify network setup, check worker connectivity |

## Monitoring Updates

### Check Service Status

```bash
# Service update state
$ swarmctl service inspect web-app
...
UpdateStatus:
  State: updating
  Parallelism: 1
  Completed: 2/3
```

### View Task Logs

```bash
# Get task ID
$ swarmctl service tasks web-app

# View task events
$ swarmctl task inspect <task-id>

# View Firecracker VM console (if configured)
$ swarmcracker logs <task-id>
```

### Resource Monitoring

```bash
# Monitor cluster resources during update
$ swarmcracker metrics --format table

# Check individual VM metrics
$ swarmcracker metrics <task-id> --format json
```

## Best Practices

### 1. Set Appropriate Parallelism

```go
// Conservative (production)
Parallelism: 1

// Aggressive (staging)
Parallelism: 0  // Unlimited
```

### 2. Configure Monitor Period

Allow enough time for your application to initialize:

```go
// Quick-starting apps
Monitor: 2 * time.Minute

// Apps with slow startup
Monitor: 10 * time.Minute
```

### 3. Use Failure Ratios

```go
// Strict (critical services)
MaxFailureRatio: 0.0  // Any failure pauses update

// Lenient (non-critical services)
MaxFailureRatio: 0.3  // Allow 30% failures
```

### 4. Test Updates in Staging

Before updating production:
1. Deploy to staging cluster
2. Verify rolling update completes successfully
3. Test application functionality
4. Monitor resource usage
5. Then update production

### 5. Plan for Rollback

Always have a rollback plan:

```bash
# Quick rollback command
swarmctl service update web-app --rollback

# Or manually specify previous image
swarmctl service update web-app --image nginx:1.24
```

## Implementation Details

### Executor Support

SwarmCracker's executor implements the following for rolling update support:

- **`Update()`**: Updates task spec (no-op for running tasks)
- **`ContainerStatus()`**: Reports VM PID and exit code
- **`Shutdown()`**: Graceful 30s timeout shutdown
- **`Terminate()`**: Force kill if graceful fails

### Task Lifecycle

```
PENDING → ASSIGNED → READY → PREPARED → STARTING → RUNNING → (exited/failed)
                                    ↑
                              Rolling update happens here
```

### Resource Cleanup

After old task is shut down:
1. Firecracker VM process terminated
2. TAP device detached from bridge
3. dnsmasq DHCP entry removed
4. Socket file deleted
5. Rootfs image deleted (or kept in cache)

## Troubleshooting

### Update Stuck on "Starting"

**Symptom**: Task stays in `STARTING` state

**Causes**:
- Firecracker binary not found
- KVM not available (`/dev/kvm` missing)
- Insufficient resources

**Fix**:
```bash
# Check Firecracker installation
which firecracker
firecracker --version

# Check KVM
ls -la /dev/kvm
kvm-ok  # From cpu-checker package

# Check available resources
swarmcracker metrics --format table
```

### Update Fails Immediately

**Symptom**: Task transitions to `FAILED` immediately

**Causes**:
- Invalid container image
- Missing kernel/rootfs
- Network configuration error

**Fix**:
```bash
# Check executor logs
journalctl -u swarmd-firecracker -f

# Verify image can be pulled
swarmcracker run --test nginx:latest

# Check network setup
ip link show swarm-br0
brctl show swarm-br0
```

### Rolling Update Too Slow

**Symptom**: Update takes longer than expected

**Causes**:
- Low parallelism setting
- Long delay between updates
- VM startup time

**Fix**:
```bash
# Increase parallelism
swarmctl service update web-app --update-parallelism 3

# Reduce delay
swarmctl service update web-app --update-delay 10s

# Check VM startup time
time swarmcracker run --test nginx:latest
```

## See Also

- [SwarmKit Orchestration Guide](../architecture/swarmkit-integration.md)
- [Service Deployment Guide](./service-deployment.md)
- [Health Checks Configuration](./health-checks.md)
- [Troubleshooting Guide](./troubleshooting.md)
