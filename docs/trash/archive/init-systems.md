# Init System Support

SwarmCracker supports init systems for proper container lifecycle management in Firecracker microVMs. This ensures reliable process management, signal handling, and zombie process reaping.

## Why Init Systems Matter

In containers and microVMs, the first process (PID 1) has special responsibilities:

- **Reaping zombies**: Orphaned child processes must be reaped to prevent resource leaks
- **Signal handling**: Properly forwarding SIGTERM/SIGKILL to child processes
- **Process supervision**: Monitoring and restarting failed processes
- **Graceful shutdown**: Allowing processes time to clean up before termination

Without an init system, containers may:
- Leave zombie processes consuming resources
- Not respond properly to shutdown signals
- Experience abrupt terminations without cleanup
- Fail to reap child processes

## Supported Init Systems

### Tini (Default)

**Tini** is a minimal init system used by Docker. It's lightweight, well-tested, and designed specifically for containers.

- **Size**: ~500KB
- **Features**: Zombie reaping, signal forwarding, subreaper
- **Use case**: General-purpose containers, production workloads
- **Installation**: `apt-get install tini` (Debian/Ubuntu)

**Example boot args:**
```
console=ttyS0 reboot=k panic=1 -- /sbin/tini -- /usr/bin/nginx -g daemon off;
```

### Dumb-Init

**Dumb-init** is a simpler init focused on signal forwarding. It's even smaller than tini.

- **Size**: ~100KB
- **Features**: Signal forwarding, process tree management
- **Use case**: Resource-constrained environments, simple workloads
- **Installation**: `apt-get install dumb-init` (Debian/Ubuntu)

**Example boot args:**
```
console=ttyS0 reboot=k panic=1 -- /sbin/dumb-init /usr/bin/nginx -g daemon off;
```

### None

Disables init system injection. Container process runs directly as PID 1.

- **Use case**: Debugging, containers that manage their own init
- **Warning**: May cause zombie processes and improper signal handling

## Configuration

### Global Configuration

Configure init system in `/etc/swarmcracker/config.yaml`:

```yaml
executor:
  # Init system type: "none", "tini", or "dumb-init"
  init_system: "tini"

  # Grace period in seconds before SIGKILL (default: 10)
  init_grace_period: 10
```

### Task-Level Configuration

Init system settings can be overridden per task using annotations:

```yaml
annotations:
  init_system: "dumb-init"
  init_grace_period: 5
```

## How It Works

### 1. Image Preparation

When preparing a container image, SwarmCracker injects the init binary into the rootfs:

```
[Image Pull] → [Extract to /tmp] → [Inject Init Binary] → [Create ext4 Image]
```

The init binary is copied to:
- `/sbin/tini` (for tini)
- `/sbin/dumb-init` (for dumb-init)

### 2. Boot Configuration

The container command is wrapped with the init system:

```
Without init:
  -- /usr/bin/nginx -g daemon off;

With tini:
  -- /sbin/tini -- /usr/bin/nginx -g daemon off;
```

The init system becomes PID 1, and your container process becomes PID 2.

### 3. Graceful Shutdown

When SwarmKit sends a `StopTask`:

1. SwarmCracker sends SIGTERM to the Firecracker process
2. Firecracker forwards SIGTERM to the VM
3. Init (PID 1) receives SIGTERM and forwards to child processes
4. Container process handles graceful shutdown
5. After grace period (default 10s), SIGKILL is sent if still running

## Examples

### Nginx with Tini

```yaml
task:
  spec:
    runtime:
      image: "nginx:alpine"
      command: ["nginx"]
      args: ["-g", "daemon off;"]
```

**Process tree in VM:**
```
PID 1: /sbin/tini -- nginx -g daemon off;
  └─ PID 2: nginx -g daemon off;
       ├─ worker process
       └─ worker process
```

### Redis with Dumb-Init

```yaml
executor:
  init_system: "dumb-init"
  init_grace_period: 15

task:
  spec:
    runtime:
      image: "redis:alpine"
      command: ["redis-server"]
```

**Process tree in VM:**
```
PID 1: /sbin/dumb-init redis-server
  └─ PID 2: redis-server
       ├─ background save process
       └─ other children
```

## Graceful Shutdown Flow

### With Init System (Recommended)

```
SwarmKit: StopTask
    ↓
SwarmCracker: SIGTERM → Firecracker
    ↓
Init (PID 1): Receives SIGTERM
    ↓
Init: Forwards SIGTERM to container process (PID 2)
    ↓
Container: Graceful shutdown (close connections, save state)
    ↓
Container: Exits
    ↓
Init: Exits
    ↓
Firecracker: VM stops
```

### Without Init System (Not Recommended)

```
SwarmKit: StopTask
    ↓
SwarmCracker: SIGTERM → Firecracker
    ↓
Container (PID 1): May not handle signals properly
    ↓
[After grace period]
    ↓
SwarmCracker: SIGKILL → Firecracker
    ↓
Abrupt termination (potential data loss)
```

## Installing Init Binaries

### Debian/Ubuntu

```bash
# Install tini
sudo apt-get update
sudo apt-get install -y tini

# Or install dumb-init
sudo apt-get install -y dumb-init
```

### From Source

**Tini:**
```bash
wget https://github.com/krallin/tini/releases/download/v0.19.0/tini
chmod +x tini
sudo mv tini /usr/bin/tini
```

**Dumb-init:**
```bash
wget https://github.com/Yelp/dumb-init/releases/download/v1.2.5/dumb-init_1.2.5_amd64.deb
sudo dpkg -i dumb-init_1.2.5_amd64.deb
```

### Verify Installation

```bash
# Check for tini
which tini
# Should output: /usr/bin/tini

# Check for dumb-init
which dumb-init
# Should output: /usr/bin/dumb-init
```

## Troubleshooting

### Init Binary Not Found

**Error:** `init injection failed: init binary not found`

**Solution:** Install the init binary on your host system:
```bash
sudo apt-get install tini  # or dumb-init
```

### Container Not Responding to Shutdown

**Symptoms:** Container doesn't shut down gracefully, processes are killed abruptly.

**Solutions:**

1. Check init system is enabled:
```yaml
executor:
  init_system: "tini"
```

2. Increase grace period:
```yaml
executor:
  init_grace_period: 30  # seconds
```

3. Verify container handles SIGTERM:
```bash
# Test in container
kill -SIGTERM $(pidof nginx)
```

### Zombie Processes

**Symptoms:** Zombie processes accumulate in the VM.

**Solution:** Ensure init system is enabled:
```yaml
executor:
  init_system: "tini"  # or "dumb-init"
```

### Process Exits Immediately

**Symptoms:** Container starts but exits immediately.

**Possible causes:**

1. Command not wrapped correctly by init
2. Init binary not executable
3. Missing dependencies in rootfs

**Debug:**
```bash
# Check init binary in rootfs
sudo debugfs -R "ls -l /sbin/tini" /var/lib/firecracker/rootfs/<image>.ext4
```

## Best Practices

### 1. Use Tini for Production

Tini is battle-tested and handles edge cases well:
```yaml
executor:
  init_system: "tini"
  init_grace_period: 10
```

### 2. Set Appropriate Grace Periods

Different workloads need different shutdown times:
- **Fast workloads** (nginx): 5-10 seconds
- **Slow workloads** (database): 30-60 seconds

```yaml
executor:
  init_grace_period: 30  # for databases
```

### 3. Test Signal Handling

Verify your container handles signals properly:
```dockerfile
# In your Dockerfile
STOPSIGNAL SIGTERM
```

```go
// In your application
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
```

### 4. Monitor for Zombies

Check for zombie processes in your VMs:
```bash
# In the VM
ps aux | grep Z
```

### 5. Use Proper Shell Scripts

If using shell scripts as entrypoints, use `exec` to replace the shell:
```bash
#!/bin/sh
# Good: exec replaces shell, init can supervise
exec nginx -g "daemon off;"

# Bad: shell stays as parent
nginx -g "daemon off;"
```

## Performance Impact

Init systems add minimal overhead:

| Init System | Binary Size | Memory Overhead | CPU Overhead |
|-------------|-------------|-----------------|--------------|
| None        | 0 bytes     | 0 bytes         | 0%           |
| dumb-init   | ~100 KB     | ~1 MB           | <0.1%        |
| tini        | ~500 KB     | ~1.5 MB         | <0.1%        |

The tradeoff is worth it for production reliability.

## Comparison

| Feature          | None     | Tini     | Dumb-init |
|------------------|----------|----------|-----------|
| Zombie reaping   | ❌       | ✅       | ✅         |
| Signal forwarding| ❌       | ✅       | ✅         |
| Subreaper        | ❌       | ✅       | ❌         |
| Size             | 0 bytes  | ~500 KB  | ~100 KB   |
| Production ready | ❌       | ✅       | ✅         |
| Default          | No       | **Yes**  | No        |

## References

- [Tini GitHub](https://github.com/krallin/tini)
- [Dumb-init GitHub](https://github.com/Yelp/dumb-init)
- [Docker and init systems](https://docs.docker.com/engine/using/init/)
- [PID 1 in containers](https://blog.phusion.nl/2015/01/20/docker-and-the-pid-1-zombie-reaping-problem/)
