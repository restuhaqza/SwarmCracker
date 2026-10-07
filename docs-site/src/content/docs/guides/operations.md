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
# Check specific VM
swarmcracker cluster status <vm-id>

# Watch mode
watch -n2 'swarmcracker cluster status <vm-id>'
```

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

> `swarmcracker metrics` still runs but is deprecated. Use the Prometheus
> endpoint above (or `swarmctl metrics`) instead.

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
| VM console logs | `/var/log/firecracker/<vm-id>.log` | Per-VM |
| dnsmasq (DHCP) | `/tmp/dnsmasq.log` | Manual |
| Firecracker stderr | captured by daemon | — |

**View logs:**
```bash
# Daemon logs
sudo journalctl -u swarmcracker-worker -f

# Specific VM console
swarmcracker vm logs --follow <vm-id>

# Logs for each task in a service (find the task IDs first)
swarmcracker service ps <service-name>
swarmcracker vm logs --follow <task-id>

# dnsmasq DHCP logs
tail -f /tmp/dnsmasq.log
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
   cat /tmp/dnsmasq.log | tail -20
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
   swarmcracker network vxlan list
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

2. **Short-term:** Manually trigger image cleanup
   ```bash
   # Set max_image_age_days to 1 in config, restart daemon
   ```

3. **Long-term:** Configure auto-cleanup in config:
   ```yaml
   images:
     max_cache_size_mb: 10240
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
swarmcracker vm logs -f <vm-id>         # Follow VM logs (CLI-created VMs)
swarmcracker vm attach <vm-id>          # Attach to the VM serial console
swarmcracker vm stop <vm-id>            # Stop a CLI-created VM
swarmcracker cluster status <vm-id>     # VM status

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
