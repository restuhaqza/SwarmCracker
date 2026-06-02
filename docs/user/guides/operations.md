# Operations Guide — SwarmCracker

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
| KVM | `/dev/kvm` exists and is accessible | `✅ KVM available` |
| Firecracker | Binary exists at PATH | `✅ firecracker v1.15.1` |
| Kernel | Kernel image present | `✅ /usr/share/firecracker/vmlinux` |
| Bridge | swarm-br0 exists and is UP | `✅ Bridge swarm-br0 up` |
| Socket dir | `/var/run/firecracker` exists | `✅ Socket directory` |
| dnsmasq | DHCP server running | `✅ dnsmasq PID xxxx` |
| CPU | Virtualization flags | `✅ CPU supports VMX/SVM` |
| Memory | Available memory | `✅ 7.5 GB available for VMs` |

### API Health Endpoint

The daemon exposes a health check endpoint:

```bash
# Local
curl -s http://127.0.0.1:4242/healthz

# Response
{
  "status": "healthy",
  "kvm": true,
  "bridge": "swarm-br0",
  "firecracker": "/usr/bin/firecracker",
  "vms_running": 3,
  "uptime_seconds": 86400
}
```

### Cluster Health

```bash
# Check all nodes
swarmcracker node list

# Expected output:
# ID              HOSTNAME    STATUS  AVAILABILITY  MANAGER
# abc123...       manager-1   Ready   Active        Leader
# def456...       worker-1    Ready   Active
# ghi789...       worker-2    Ready   Active
```

### VM Health

```bash
# Check specific VM
swarmcracker status <vm-id>

# Watch mode
swarmcracker status --watch
```

---

## Monitoring

### Metrics

```bash
# One-time metrics
swarmcracker metrics

# Watch mode (refreshes every 2s)
swarmcracker metrics --watch --interval 5s

# JSON for scraping
swarmcracker metrics --json
```

**Key metrics:**
- `vm_count_total` — Total VMs running on this node
- `vm_cpu_usage_percent` — CPU utilization per VM
- `vm_memory_usage_bytes` — Memory usage per VM
- `vm_disk_read_bytes` / `vm_disk_write_bytes` — Disk I/O
- `vm_network_rx_bytes` / `vm_network_tx_bytes` — Network I/O
- `node_cpu_available` — Available CPU (nanocpus)
- `node_memory_available` — Available memory
- `bridge_packets_total` — Bridge packet count

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
journalctl -u swarmd-firecracker -f

# Specific VM console
swarmcracker vm logs <vm-id> --follow

# All VMs in a service
swarmcracker service logs --follow <service-name>

# dnsmasq DHCP logs
tail -f /tmp/dnsmasq.log
```

**Log levels:** Set via `--log-level` flag or `logging.level` in config.yaml.
Available: `debug`, `info`, `warn`, `error`.

Set to `debug` for troubleshooting (verbose, includes token operations at debug level only):
```bash
swarmd-firecracker --log-level debug
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
   # If missing: swarmcracker init (recreates infrastructure)
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
   journalctl -u swarmd-firecracker | grep <vm-id>
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
   # Should show vxlan100
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
   bridge fdb show dev vxlan100
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
   # On manager:
   swarmcracker cluster token list
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
   swarmcracker metrics | grep cpu
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
swarmcracker snapshot create --name pre-upgrade <vm-id>

# List snapshots
swarmcracker snapshot list --vm <vm-id>

# Restore from snapshot
swarmcracker snapshot restore <snapshot-id>
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

# State (stop daemon first if possible)
systemctl stop swarmd-firecracker
cp -r /var/lib/swarmcracker "$BACKUP_DIR/state"
systemctl start swarmd-firecracker

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
   swarmcracker task list --node worker-1
   # Should show no running tasks
   ```

3. **Upgrade the worker:**
   ```bash
   # On worker-1
   systemctl stop swarmd-firecracker
   # Deploy new binary
   cp new-swarmd-firecracker /usr/local/bin/
   systemctl start swarmd-firecracker
   ```

4. **Activate the worker:**
   ```bash
   swarmcracker node activate worker-1
   ```

5. **Verify health:**
   ```bash
   swarmcracker node list | grep worker-1
   # Should show Ready, Active
   ```

6. **Repeat for remaining workers, then managers.**

### Manager Upgrade

Managers must be upgraded one at a time:

1. **Verify quorum:**
   ```bash
   swarmcracker node list | grep manager
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
swarmcracker cluster token create --role worker

# 3. On new worker
swarmcracker cluster join --token SWMTKN-1-xxx <manager-ip>:4242

# 4. Verify
swarmcracker node list
# Should show new node as Ready, Active
```

### Scaling Down

```bash
# Drain worker
swarmcracker node drain worker-3

# Wait for VMs to reschedule
swarmcracker task list --node worker-3

# Leave cluster
swarmcracker cluster leave

# Clean up
swarmcracker reset
```

---

## Security Operations

### Certificate Rotation

SwarmCracker uses SwarmKit's built-in TLS with auto-rotation. To force rotation:

```bash
swarmcracker cluster token rotate
```

### Secret Management

```bash
# Create a secret (via SwarmKit API)
swarmctl secret create db_password - <<< "s3cr3t"

# Use in service
swarmcracker service create --secret db_password myapp

# Secret is injected at /run/secrets/db_password inside VM
```

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
- [ ] Check available disk for snapshots: `du -sh /var/lib/swarmcracker/snapshots/`

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
5. Verify: `swarmcracker node list` should show all nodes Ready

### Disk Full

1. **Immediate:** Delete old snapshots
   ```bash
   swarmcracker snapshot delete --all
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

See [Configuration Guide](../guides/configuration.md) for all config keys and defaults.

---

## Quick Reference Card

```bash
# Health
swarmcracker doctor                     # Node health check
curl 127.0.0.1:4242/healthz             # API health check

# Cluster
swarmcracker node list                  # List nodes
swarmcracker node inspect <node>        # Node details
swarmcracker cluster token create       # Create join token

# VMs
swarmcracker vm list                    # List VMs
swarmcracker vm logs -f <vm-id>         # Follow VM logs
swarmcracker vm stop <vm-id>            # Stop VM
swarmcracker status <vm-id>             # VM status

# Services
swarmcracker service list               # List services
swarmcracker service ps <service>       # Service tasks
swarmcracker service logs -f <service>  # Service logs

# Snapshots
swarmcracker snapshot create <vm-id>    # Snapshot VM
swarmcracker snapshot list              # List snapshots
swarmcracker snapshot restore <snap>    # Restore from snapshot

# Network
swarmcracker network vxlan list         # VXLAN peers

# Metrics
swarmcracker metrics --watch            # Watch metrics

# Recovery
swarmcracker reset --force              # Reset node
swarmcracker cluster leave              # Leave cluster
```

---

**See Also:** [Architecture Overview](../../architecture/overview.md) | [Configuration Guide](../guides/configuration.md) | [Networking Guide](../guides/networking.md) | [Security Guide](../guides/security.md)
