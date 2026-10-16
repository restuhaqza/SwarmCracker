---
title: "Operations Guide — SwarmCracker"
description: "How to operate, monitor, troubleshoot, and maintain a SwarmCracker cluster in production."
---

> How to operate, monitor, troubleshoot, and maintain a SwarmCracker cluster in production.

---

## Health Checks

### Node-Level Health

Run the built-in doctor to verify node health:

```bash
swarmcracker doctor
```

**Passing checks:**

| Check | What it verifies | Healthy Output |
|-------|-----------------|----------------|
| CPU virtualization (KVM) | `/dev/kvm` exists and the KVM module is loaded | `✓ CPU virtualization (KVM) — KVM available` |
| Firecracker binary | `firecracker` is in PATH | `✓ Firecracker binary — found at /usr/local/bin/firecracker` |
| Firecracker version | `firecracker --version` runs | `✓ Firecracker version — v1.15.1` |
| Kernel image | Kernel present at the configured path | `✓ Kernel image — /usr/share/firecracker/vmlinux (25.0 MB)` |
| TUN/TAP device | `/dev/net/tun` is available | `✓ TUN/TAP device — /dev/net/tun available` |
| Available memory | Free memory from `/proc/meminfo` | `✓ Available memory — 16.0 GB total, 7.5 GB available` |
| CPU cores | Processor count | `✓ CPU cores — 8 CPU core(s)` |
| Bridge interface | `swarm-br0` exists | `✓ Bridge interface (swarm-br0) — swarm-br0 exists` |
| Port 4242 | SwarmKit API port availability | `✓ Port 4242 — port 4242 available` |
| Manager/Worker service | systemd unit is active | `✓ Worker service — worker service is active` |
| Join tokens | `/var/lib/swarmkit/join-tokens.txt` present | `✓ Join tokens — found at /var/lib/swarmkit/join-tokens.txt` |
| Firecracker processes | Running VM process count | `✓ Firecracker processes — 3 Firecracker process(es) running` |

### API Health Endpoint

The daemon exposes a health check endpoint on its health address
(`--health-addr`, default `127.0.0.1:8080`):

```bash
# Local
curl -s http://127.0.0.1:8080/healthz

# Response
{
  "healthy": true,
  "checks": {
    "kvm": { "status": "ok", "message": "..." },
    "bridge": { "status": "ok", "message": "..." },
    "firecracker": { "status": "ok", "message": "..." }
  }
}
```

The same HTTP server exposes Prometheus metrics at
`http://127.0.0.1:8080/metrics`.

### Cluster Health

```bash
# List all nodes
swarmcracker node ls

# Expected output:
# ID            STATUS  HOSTNAME    AVAILABILITY
# abc123def456  Ready   manager-1   Active
# def456abc789  Ready   worker-1    Active
# ghi789def012  Ready   worker-2    Active
```

### VM Health

```bash
# Check a specific VM
swarmcracker vm status <vm-id>

# Watch mode
watch -n2 'swarmcracker vm status <vm-id>'

# Cluster overview (nodes / services / tasks)
swarmcracker cluster status
```

### Container Healthchecks

A service can declare a **container healthcheck**: a command that runs inside the
microVM on a schedule. A task with a healthcheck is not considered running until
the check passes, so a rolling update only moves on once the new task is actually
serving.

```bash
swarmcracker service create --name api --image myapi:v2 --replicas 3 \
    --health-cmd 'curl -fsS http://localhost:8080/healthz' \
    --health-interval 10s \
    --health-timeout 3s \
    --health-retries 3 \
    --health-start-period 20s
```

The command runs **inside the guest** through the guest agent over vsock — the
same mechanism as [`vm exec`](/guides/guest-access/) — using the guest shell
(`/bin/sh -c`), and the check passes when the command exits `0`. It runs **as
root** in the guest, so treat the check like any other guest command. The check
therefore needs a reachable guest agent; see
[Guest Access → Limitations](/guides/guest-access/#limitations) for the images
and modes where it is unavailable.

| Flag | Default | Meaning |
|------|---------|---------|
| `--health-cmd` | — | Check command; enables the healthcheck |
| `--health-interval` | `30s` | Time between checks |
| `--health-timeout` | `30s` | Per-check timeout |
| `--health-retries` | `3` | Consecutive failures before the task is unhealthy |
| `--health-start-period` | `0` | Grace period during which failures do not count |

The four tuning flags apply **only** when `--health-cmd` is set. Without it there
is no healthcheck and a task starts exactly as before. An invalid value is
rejected (`--health-retries 0`, `--health-interval 0s`), and passing any other
`--health-*` flag without `--health-cmd` is an error — no flag is silently
ignored.

#### Health-gated rollouts

The healthcheck **gates the task's start**: the executor holds the task in
`STARTING` until a check passes, and only then reports it `RUNNING`. That is what
makes `--update-order start-first` wait for real readiness:

```bash
swarmcracker service update api \
    --image myapi:v3 \
    --update-order start-first \
    --update-failure-action rollback \
    --health-cmd 'curl -fsS http://localhost:8080/healthz'
```

With `start-first`, SwarmKit does not stop the old task until the new one is up —
and because a healthchecked task is not `RUNNING` until its check passes, the
rollout does not proceed until the new task is healthy. If the check never
passes, the retries are exhausted, the task fails, and
`--update-failure-action rollback` rolls the service back.

A task that becomes unhealthy **while running** is stopped, and the service's
restart policy (`--restart-condition`) decides what happens next. Each probe
(dial plus exec) is bounded by `--health-timeout`, and the health wait does not
hold the executor's lock, so `service rm` and stop are never stalled behind it.

#### Seeing health

`service ps` and `task ls` show a `HEALTH` column. When the CLI can reach the
node owning a task, the column carries that node's **live** verdict; otherwise
it falls back to the value derived from the health-gated task state:

```bash
swarmcracker service ps api
# ID            STATUS    HEALTH     NODE          IMAGE
# 5f3a1b2c9d0e  RUNNING   failing    abc123def456  myapi:v3   # live: probe failing, retries not exhausted
# 9c8d7e6f1a2b  STARTING  STARTING   def456abc789  myapi:v3   # live health not published yet
# 1a2b3c4d5e6f  RUNNING   RUNNING    ghi789def012  myapi:v3   # node not exposed: derived value
# 7e6f1a2b9c8d  RUNNING   -          jkl012mno345  myapi:v3   # owning node unreachable
```

| Value | Source | Meaning |
|-------|--------|---------|
| `starting` | live | The task has not passed a check yet, so it is not reported `RUNNING` |
| `healthy` | live | The most recent probe passed |
| `failing` | live | The most recent probe failed, but the retries threshold is not reached — the task is still `RUNNING` and no restart has fired |
| `unhealthy` | live | The retries are exhausted; the VM is being stopped so SwarmKit reschedules the task |
| `STARTING` / `RUNNING` / `FAILED` | derived | Live health was not available (an older daemon, or a task that has not published yet), so the health-gated task state is shown |
| `-` | — | No healthcheck, the task has completed or stopped, or the owning node could not be reached |

`failing` is the one worth watching: it is the window between "a check started
failing" and "the task is restarted", up to `(retries - 1) × interval` (60s with
the defaults). It is the value the derived column cannot show.

`--format json` emits the display value as `health` and, when it was fetched,
the raw live observation as `live_health` (`state`, `failing_streak`, `output`,
`command`, `updated_at`).

`task inspect` shows the declared healthcheck and, when a check failed, the
reason the executor recorded:

```bash
swarmcracker task inspect --format table 5f3a1b2c
# Healthcheck:
#   Test: CMD-SHELL curl -fsS http://localhost:8080/healthz
#   Interval: 10s
#   Timeout: 3s
#   Retries: 3
#   Start Period: 20s
# Error: healthcheck failed: /bin/sh -c curl -fsS http://localhost:8080/healthz: ...
```

The default `--format json` output carries the same information in the task's
`Spec.Container.Healthcheck` and `Status.Err` fields.

#### Live health, cluster-wide

The live verdict comes from the node that owns the task. The executor publishes
every probe result into the per-task metadata file next to the VM socket, and
the daemon serves it on its health server:

```
GET http://<node-address>:8080/v1/tasks/health
{"tasks":[{"task_id":"5f3a1b2c…","state":"failing","failing_streak":2,"command":"…","output":"…","updated_at":"…"}]}
```

`service ps` / `task ls` resolve each task's node from SwarmKit, send **one**
request per node (each bounded by a 3s timeout), and overlay the result. Tasks
with no healthcheck are absent from the response and keep their derived `-`.

The daemon's health server listens on `--health-addr`, which defaults to
`127.0.0.1:8080` — node-local. To get a cluster-wide `HEALTH` column, bind it on
the cluster network when starting the daemon:

```bash
# In each node's systemd unit (or on the command line)
swarmd-firecracker --health-addr 0.0.0.0:8080 …
```

and, if the daemons bind a port other than 8080, tell the CLI:

```bash
swarmcracker service ps api --health-port 9090
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--health-port` | `8080` | Port of each node's health server |
| `--no-health` | `false` | Skip the fan-out; derive `HEALTH` from the task state only |

`--no-health` also skips the `ListNodes` call, so it is the escape hatch when the
endpoint is not exposed and you do not want any extra requests. An invalid
`--health-port` is rejected; it is never silently ignored.

**Trust model.** The endpoint returns health only — state, failing streak, the
probe command and its last (bounded) output. There is no authentication and no
secret. The bind address *is* the boundary: the default `127.0.0.1:8080` exposes
it to nothing, and binding `0.0.0.0` exposes it to the cluster network exactly
like the existing `/healthz` and `/metrics` (which are on the same server).

A node running an older daemon, or one whose health server is not reachable,
simply contributes no live values: that node's tasks degrade to the derived
value, or to `-` if the node is unreachable entirely. The command still succeeds.

#### Live health on a single node

The same data is available locally, without the fan-out. `vm status` / `vm list`
read the metadata file directly:

```bash
# Run this on the node hosting the task.
swarmcracker vm status 5f3a1b2c
# [Status]
#   State:        Running ✓
# [Health]
#   State:        Failing ✗ (retries not exhausted; task still running)
#   Failing:      2 consecutive probe(s)
#   Check:        /bin/sh -c curl -fsS http://localhost:8080/healthz
#   Last output:  curl: (7) Failed to connect to localhost port 8080
#   Observed:     2026-10-13 15:04:07
```

This is **node-local**: run the command on the node hosting the task. VMs started
in the foreground by `vm create` have no daemon and no monitor, so they never
carry health.

#### The API limitation

SwarmKit's API has no health field: `api.ContainerStatus` carries only
`ContainerID`, `PID` and `ExitCode`, and `api.TaskStatus` has none — unchanged in
SwarmKit v2.1.2 and upstream `master`. A new field alone would not fix it: while
a task is `RUNNING` the agent blocks in `Wait` and never polls its status, so any
value would stay frozen until the task exits. SwarmCracker therefore **derives**
the fallback `HEALTH` value from the health-gated task state, and gets the live
value — including `failing`, which the derived value cannot express — from each
node's health endpoint as described above.


---

## Monitoring

### Metrics

The node's health server exposes Prometheus metrics at
`http://127.0.0.1:8080/metrics`:

```bash
# Scrape this node's metrics
curl -s http://127.0.0.1:8080/metrics

# Per-task resource usage
swarmctl metrics <task-id>
```

> `swarmcracker metrics` shows live per-VM resource usage, including VMs started
> by the daemon for services. Use the Prometheus endpoint above for scraping.

**Key metrics** (all prefixed `swarmcracker_`):
- `swarmcracker_vms_running` — VMs currently running on this node
- `swarmcracker_vms_total` — VM lifecycle transitions by status
- `swarmcracker_vm_cpu_seconds` — CPU time consumed per VM (`task_id`, `service` labels)
- `swarmcracker_vm_memory_bytes` — Memory used per VM
- `swarmcracker_vm_net_rx_bytes` / `swarmcracker_vm_net_tx_bytes` — Network I/O per VM
- `swarmcracker_vm_boot_duration_seconds` — VM boot time
- `swarmcracker_vxlan_peers` / `swarmcracker_vxlan_expected_peers` — Overlay peers
- `swarmcracker_manager_health` / `swarmcracker_raft_health` — Manager/Raft health
- `swarmcracker_disk_usage_bytes` — Disk usage of SwarmCracker directories

### Logging

**Log locations:**

| Log | Path | Rotation |
|-----|------|----------|
| swarmd-firecracker (daemon) | `/var/log/swarmcracker/daemon.log` | systemd journal |
| VM console logs | `<state-dir>/<vm-id>.log` (`/var/run/swarmcracker` as root) | Truncated on VM start |
| dnsmasq (DHCP) | `/var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log` | Manual |
| Firecracker stderr | captured by daemon | — |

dnsmasq's per-bridge files — the log, the pid file, the DHCP lease file and the
service-name records — live in `/var/lib/swarmcracker/dnsmasq/`, a root-owned
directory that only root and dnsmasq's own user can write (when that user cannot
be resolved, the directory stays root-owned and only root can). They are
deliberately not in `/tmp`, where a predictable name would let a local user plant a
file the daemon would write through or signal as root.

That path is under the project's state directory because the shipped systemd units
run the daemon with `ProtectSystem=strict`: only the paths those units list in
`ReadWritePaths` are writable, and `/var/lib/swarmcracker` is one of them while
`/run` is not.

Both CLI-created VMs and daemon-managed service tasks mirror their serial
console to `<state-dir>/<vm-id>.log`, so `swarmcracker vm logs` works for either.
The CLI state directory is `/var/run/swarmcracker` when running as root and
`~/.swarmcracker` otherwise.

**View logs:**
```bash
# Daemon logs
sudo journalctl -u swarmcracker-worker -f

# Specific VM console (CLI-created VM or service task)
swarmcracker vm logs --follow <vm-id>

# Logs for each task in a service (find the task IDs first)
swarmcracker service ps <service-name>
swarmcracker vm logs --follow <task-id>

# dnsmasq DHCP logs
tail -f /var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log
```

**Attach to a VM console:**

`swarmcracker vm attach` connects your terminal to the microVM's serial console
(the same channel the guest kernel and `ttyS0` use). For an image whose command
is an interactive shell (for example `CMD ["/bin/bash"]`), this gives you a shell
inside the VM.

```bash
# Find the task ID (service ps shows a 12-character prefix)
swarmcracker task ls

# Attach using the full ID or any unique prefix
swarmcracker vm attach 5f3a1b2c9d

# Non-default socket directory (must match the daemon's --socket-dir)
swarmcracker vm attach --socket-dir /var/run/firecracker 5f3a1b2c9d
```

Press **Ctrl-P Ctrl-Q** to detach; the VM keeps running. The console socket is
owned by the daemon (`root`, mode `0600`), so run the command as root or with
`sudo`.

> **Note:** console attach is available for VMs managed by the daemon (service
> tasks). VMs started in the foreground by `swarmcracker vm create` are not
> attachable, because the CLI process that owns their console exits with the VM.

To run a command or copy a file instead of driving the console, use
[`swarmcracker vm exec` and `swarmcracker vm cp`](/guides/guest-access/).

**Log levels:** Set via `--log-level` flag or `logging.level` in config.yaml.
Available: `debug`, `info`, `warn`, `error`.

Set to `debug` for troubleshooting (verbose, includes token operations at debug level only):
```bash
swarmd-firecracker --debug
```

---

## Common Troubleshooting

### VM Won't Start

**Symptoms:** `vm create` or `service create` returns error.

**Checklist:**

1. **KVM available?**
   ```bash
   ls -la /dev/kvm
   # If missing: modprobe kvm && modprobe kvm-intel (or kvm-amd)
   ```

2. **Firecracker binary?**
   ```bash
   which firecracker
   firecracker --version  # Should be v1.15.1+
   ```

3. **Kernel image?**
   ```bash
   ls -la /usr/share/firecracker/vmlinux
   # Expected: ~25MB ELF kernel
   ```

4. **Rootfs exists?**
   ```bash
   ls -la /var/lib/firecracker/rootfs/
   # Should show .ext4 files for each pulled image
   ```

5. **Bridge exists?**
   ```bash
   ip link show swarm-br0
   # If missing: swarmcracker cluster init (recreates infrastructure)
   ```

6. **Socket directory writable?**
   ```bash
   ls -la /var/run/firecracker/
   # Permissions should be 0755, owned by the daemon user
   ```

7. **Sufficient resources?**
   ```bash
   swarmcracker doctor  # Check memory/CPU available
   ```

### VM Crashes / Exits Immediately

**Symptoms:** VM starts then stops within seconds.

**Checklist:**

1. **Check VM console log:**
   ```bash
   swarmcracker vm logs <vm-id>
   ```

2. **Common causes:**
   - **Kernel panic:** Wrong kernel or missing modules. Check boot args.
   - **Rootfs not found:** Verify path in config.
   - **Init system failure:** Check if tini/dumb-init is in rootfs.
   - **OOM:** VM has insufficient memory. Increase `--memory` / `-m`.
   - **Missing command:** Container image doesn't have the specified command.

3. **Check Firecracker output:**
   ```bash
   journalctl -u swarmcracker-worker | grep <vm-id>
   ```

### Network Issues

#### VMs Can't Reach Internet

1. **NAT enabled?**
   ```bash
   iptables -t nat -L POSTROUTING | grep MASQUERADE
   # Should show rule for 192.168.127.0/24
   ```

2. **IP forwarding?**
   ```bash
   sysctl net.ipv4.ip_forward
   # Should be 1
   ```

3. **DHCP working?**
   ```bash
   cat /var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log | tail -20
   # Should show DHCPOFFER/DHCPACK
   ```

#### Cross-Node VM Communication Fails

1. **VXLAN enabled on both nodes?**
   ```bash
   ip link show | grep vxlan
   # Should show swarm-br0-vxlan
   ```

2. **VXLAN peers correct?**
   ```bash
   swarmcracker network vxlan ls
   # Should list all worker IPs
   ```

3. **UDP 4789 open between nodes?**
   ```bash
   nc -zvu <other-node-ip> 4789
   ```

4. **Bridge FDB entries correct?**
   ```bash
   bridge fdb show dev swarm-br0-vxlan
   ```

5. **Consul registration?**
   ```bash
   consul catalog services
   # Should show swarmcracker-worker
   ```

#### VM Has No IP Address

1. **Check TAP device:**
   ```bash
   ip link show | grep tap-
   ```

2. **Check IP allocator:**
   ```bash
   swarmcracker doctor  # Checks IPAM state
   ```

3. **Static IP with no DHCP fallback?**
   If using static IP mode and the VM expects DHCP, add `ip=dhcp` to kernel args.

### Cluster Issues

#### Worker Can't Join

1. **Connectivity to manager:**
   ```bash
   nc -zv <manager-ip> 4242
   ```

2. **Valid join token?**
   ```bash
   # On manager (prints the worker and manager join tokens)
   swarmcracker cluster token
   ```

3. **Firewall?**
   Ports needed:
   - `4242` (SwarmKit gRPC API)
   - `4789` UDP (VXLAN overlay)
   - `8500` (Consul, if enabled)

4. **Time sync?**
   ```bash
   timedatectl status
   # Clocks must be within a few seconds
   ```

#### Manager Lost Quorum

If you lose 2 of 3 managers:

1. On the remaining manager:
   ```bash
   swarmd-firecracker --manager --force-new-cluster
   ```

2. Rejoin workers:
   ```bash
   # On each worker
   swarmcracker cluster join --token <new-token> <manager-ip>:4242
   ```

### Performance Issues

#### VMs Are Slow

1. **Check CPU steal:**
   ```bash
   curl -s http://127.0.0.1:8080/metrics | grep swarmcracker_vm_cpu
   ```

2. **Check memory pressure:**
   ```bash
   free -h
   swarmcracker doctor  # Shows available memory
   ```

3. **Disk I/O bottleneck?**
   - Use `block` driver instead of `dir` for database workloads
   - Check rootfs is on fast storage (SSD/NVMe)

4. **Network throughput?**
   - VXLAN adds ~50 bytes overhead per packet
   - For overlay networks, MTU is set to 1450 automatically

---

## Backup and Restore

### VM Snapshots

```bash
# Create snapshot of running VM
swarmcracker vm snapshot create <task-id>

# List snapshots
swarmcracker vm snapshot list --task <task-id>

# Restore from snapshot
swarmcracker vm snapshot restore <snapshot-id>
```

### Configuration Backup

```bash
# Backup config directory
tar czf swarmcracker-config-$(date +%Y%m%d).tar.gz /etc/swarmcracker/

# Backup state (includes certs, tokens, task state)
tar czf swarmcracker-state-$(date +%Y%m%d).tar.gz /var/lib/swarmcracker/
```

### Volume Backup

```bash
# Backup a volume
tar czf volume-<name>-$(date +%Y%m%d).tar.gz /var/lib/swarmcracker/volumes/<name>/
```

### Full Node Backup

```bash
#!/bin/bash
# Full backup script
BACKUP_DIR="/backup/swarmcracker/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# Config
cp -r /etc/swarmcracker "$BACKUP_DIR/config"

# State (stop the daemon first if possible)
systemctl stop swarmcracker-worker   # or swarmcracker-manager on a manager node
cp -r /var/lib/swarmcracker "$BACKUP_DIR/state"
systemctl start swarmcracker-worker

# Rootfs images
cp -r /var/lib/firecracker/rootfs "$BACKUP_DIR/rootfs"

# Volumes
cp -r /var/lib/swarmcracker/volumes "$BACKUP_DIR/volumes"

# Compress
tar czf "$BACKUP_DIR.tar.gz" -C "$(dirname "$BACKUP_DIR")" "$(basename "$BACKUP_DIR")"
rm -rf "$BACKUP_DIR"
```

---

## Cluster Upgrade

### Rolling Upgrade (Zero Downtime)

1. **Drain a worker:**
   ```bash
   swarmcracker node drain worker-1
   ```

2. **Wait for all VMs to move:**
   ```bash
   swarmcracker task ls --node worker-1
   # Should show no running tasks
   ```

3. **Upgrade the worker:**
   ```bash
   # On worker-1
   systemctl stop swarmcracker-worker
   # Deploy new binary
   cp new-swarmd-firecracker /usr/local/bin/
   systemctl start swarmcracker-worker
   ```

4. **Activate the worker:**
   ```bash
   swarmcracker node activate worker-1
   ```

5. **Verify health:**
   ```bash
   swarmcracker node ls | grep worker-1
   # Should show Ready, Active
   ```

6. **Repeat for remaining workers, then managers.**

### Manager Upgrade

Managers must be upgraded one at a time:

1. **Verify quorum:**
   ```bash
   swarmcracker node ls | grep manager
   # Need 2+ managers healthy for quorum
   ```

2. **Drain and upgrade:**
   ```bash
   # Same as worker upgrade, but re-initialize if needed
   swarmcracker cluster leave
   # Upgrade binary, then re-join
   swarmcracker cluster join --token <token> <leader-ip>:4242
   ```

---

## Resource Management

### Capacity Planning

| Per-VM Overhead | Value |
|----------------|-------|
| Firecracker process | ~50 MB RSS |
| TAP device | Negligible |
| Bridge + VXLAN | ~5 MB kernel memory |
| dnsmasq (shared) | ~10 MB RSS |
| State tracking | ~5 MB per VM |

**Formula:**
```
Available VMs = (Total_RAM - System_Reserved - 200MB) / (VM_Size + 50MB)
```

Example for a 16GB worker with 512MB VMs:
```
(16GB - 2GB - 0.2GB) / (0.512GB + 0.05GB) = 24.5 → 24 VMs max
```

### Scaling Up

```bash
# Add workers
# 1. Provision new node with Firecracker + kernel
# 2. Get join token from manager
swarmcracker cluster token worker

# 3. On new worker
swarmcracker cluster join <manager-ip>:4242 --token SWMTKN-1-xxx

# 4. Verify
swarmcracker node ls
# Should show new node as Ready, Active
```

### Scaling Down

```bash
# Drain worker
swarmcracker node drain worker-3

# Wait for VMs to reschedule
swarmcracker task ls --node worker-3

# Leave cluster
swarmcracker cluster leave

# Clean up
swarmcracker cluster reset
```

---

## Security Operations

### Certificate Rotation

SwarmCracker uses SwarmKit's built-in mutual TLS, and node certificates are
rotated automatically by SwarmKit. There is no CLI command to force a
rotation: `swarmcracker cluster token` only displays join tokens.

### Secret Management

SwarmCracker does not currently expose secret management through its CLI:
neither `swarmcracker` nor `swarmctl` has a `secret` command, and
`swarmcracker service create` has no `--secret` flag. Distribute sensitive
data through your image build or a mounted volume until secret support lands.

### Firewall Rules

Minimum required ports:

| Port | Protocol | Source | Destination | Purpose |
|------|----------|--------|-------------|---------|
| 4242 | TCP | All nodes | Managers | SwarmKit gRPC |
| 4789 | UDP | All workers | All workers | VXLAN overlay |
| 8500 | TCP | All nodes | Consul nodes | Service discovery |
| 4242/tcp | TCP | Admin | Managers | CLI access |

```bash
# Example iptables rules
iptables -A INPUT -p tcp --dport 4242 -s 192.168.1.0/24 -j ACCEPT
iptables -A INPUT -p udp --dport 4789 -s 192.168.1.0/24 -j ACCEPT
```

---

## Routine Maintenance

### Daily
- [ ] Check node health: `swarmcracker doctor`
- [ ] Check running VMs: `swarmcracker vm list`
- [ ] Check disk space: `df -h /var/lib/firecracker/rootfs`

### Weekly
- [ ] Run image cleanup: check `/var/log/swarmcracker/daemon.log` for "Periodic cleanup completed"
- [ ] Check for orphaned VMs: review daemon logs for "Found orphaned VM"
- [ ] Review metrics trends
- [ ] Check available disk for snapshots: `du -sh /var/lib/firecracker/snapshots/`

### Monthly
- [ ] Test backup restoration
- [ ] Review and update firewall rules
- [ ] Check for SwarmCracker updates
- [ ] Rotate Consul tokens (if using ACLs)
- [ ] Verify VXLAN cross-node connectivity

---

## Emergency Procedures

### Node Failure

**Worker failure:**
1. Drain the failed node: `swarmcracker node drain <worker>`
2. SwarmKit reschedules VMs to other workers
3. Replace hardware, reprovision, rejoin

**Manager failure (1 of 3):**
- Cluster continues operating. Replace the failed manager.

**Manager failure (2 of 3 — loss of quorum):**
1. On the surviving manager: `swarmd-firecracker --manager --force-new-cluster`
2. Replace failed managers, join as followers
3. Rejoin workers

### Full Cluster Recovery

1. Stop all `swarmd-firecracker` processes
2. Restore from backup on the designated manager node
3. Start with `--force-new-cluster`
4. Restore workers from backups, rejoin one at a time
5. Verify: `swarmcracker node ls` should show all nodes Ready

### Disk Full

1. **Immediate:** Delete old snapshots
   ```bash
   swarmcracker vm snapshot cleanup --max-age 168h
   ```

2. **Short-term:** Remove unused rootfs images
   ```bash
   # The rootfs directory holds the per-image cache (<image>-<tag>.ext4, never
   # booted); each task's private copy lives in its tasks/ subdirectory and is
   # deleted when the task or VM goes away.
   ls -lh /var/lib/firecracker/rootfs/ /var/lib/firecracker/rootfs/tasks/
   ```
   The periodic cleanup only reaps a copy whose **access time** is old, so a
   running VM's rootfs is normally left alone — but on a filesystem mounted
   `noatime` (or for a task running longer than the retention window) an old copy
   can be reaped while its VM still runs. The VM keeps working from the open file;
   a restart or a snapshot of it then fails.

3. **Long-term:** Configure snapshot retention. (Image cleanup already runs
   automatically every 24 hours, removing rootfs images older than 7 days.)
   ```yaml
   snapshot:
     max_snapshots: 10
     max_age: 168h  # 7 days
   ```

---

## Configuration Reference

See [Configuration Guide](/guides/configuration/) for all config keys and defaults.

---

## Quick Reference Card

```bash
# Health
swarmcracker doctor                     # Node health check
curl 127.0.0.1:8080/healthz             # API health check

# Cluster
swarmcracker node ls                    # List nodes
swarmcracker node inspect <node>        # Node details
swarmcracker cluster token worker       # Get a worker join token

# VMs
swarmcracker vm list                    # List VMs (CLI-created + service tasks)
swarmcracker vm status <vm-id>          # VM details (service task IDs work too)
swarmcracker vm logs -f <vm-id>         # Follow VM logs (CLI-created VMs and service tasks)
swarmcracker vm attach <vm-id>          # Attach to the VM serial console
swarmcracker vm exec <vm-id> -- <cmd>   # Run a command in the guest (see Guest Access guide)
swarmcracker vm cp <file> <vm-id>:<dst> # Copy a file in/out of the guest
swarmcracker vm stop <vm-id>            # Stop a CLI-created VM
swarmcracker cluster status             # Cluster overview (nodes/services/tasks)
swarmcracker service scale <service> 0  # Stop a service's tasks (scale to zero)

# Services
swarmcracker service ls                 # List services
swarmcracker service ps <service>       # Service tasks

# Snapshots
swarmcracker vm snapshot create <vm-id> # Snapshot VM
swarmcracker vm snapshot list           # List snapshots
swarmcracker vm snapshot restore <snap> # Restore from snapshot

# Network
swarmcracker network vxlan ls           # VXLAN peers

# Recovery
swarmcracker cluster reset --hard       # Reset node
swarmcracker cluster leave              # Leave cluster
```

---

**See Also:** [Architecture Overview](/architecture/) | [Configuration Guide](/guides/configuration/) | [Networking Guide](/guides/networking/) | [Security Guide](/guides/security/)
