# Disaster Recovery

> How to recover SwarmCracker from node failures, Raft corruption, and cluster outages.

---

## Overview

SwarmCracker cluster state is stored in multiple locations:

| Component | Location | Backup Strategy |
|-----------|----------|----------------|
| Raft log (cluster state) | `/var/lib/swarmkit/` | `swarmctl cluster snapshot` |
| VM state | `/var/lib/firecracker/` | VM snapshots |
| VM snapshots | `/var/lib/swarmcracker/snapshots/` | Copy to remote storage |
| Config | `/etc/swarmcracker/config.yaml` | Auto-generated on join |
| Join tokens | `/var/lib/swarmkit/certificates/` | Backed up with Raft |

---

## Scenario 1: Worker Node Failure

### Detection
```bash
swarmcracker cluster health
# Will show healthz endpoint unreachable for the failed node
swarmctl --socket /var/lib/swarmkit/swarm.sock node ls
# Status will show "down" for the failed worker
```

### Recovery Steps

1. **Verify the node is truly dead:**
   ```bash
   ssh dead-worker -- swarmcracker cluster health
   ```

2. **Remove the dead node from the cluster:**
   ```bash
   swarmctl --socket /var/lib/swarmkit/swarm.sock node rm <NODE_ID>
   ```

3. **VMs on the dead node are lost.** SwarmKit will reschedule services with desired replicas. Verify:
   ```bash
   swarmcracker service ps <service-name>
   ```

4. **Replace the worker:**
   ```bash
   # Get a new join token on the manager
   swarmcracker cluster token worker

   # On the replacement node
   curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash
   sudo swarmcracker setup install
   sudo swarmcracker setup network
   sudo swarmcracker cluster join --token <TOKEN> <MANAGER_IP>:4242
   ```

---

## Scenario 2: Manager Node Failure

⚠️ **Manager failure requires immediate action** — the Raft consensus log is on the manager.

### If you have >1 manager (recommended for production)

SwarmKit's Raft consensus handles this automatically. The remaining managers elect a new leader.

```bash
# Check which manager is now leader
swarmctl --socket /var/lib/swarmkit/swarm.sock node ls
# Look for "Leader" in the MANAGER STATUS column
```

### If you have only 1 manager (typical for small clusters)

You need to promote a worker or set up a new manager.

#### Option A: Promote a worker to manager

1. **On a healthy worker, stop the swarmd service:**
   ```bash
   sudo systemctl stop swarmd-firecracker
   ```

2. **Clear old worker state (prevents CA conflicts):**
   ```bash
   sudo rm -rf /var/lib/swarmkit/certificates
   sudo rm -rf /var/lib/swarmkit/worker
   ```

3. **Start as manager with force-new-cluster:**
   ```bash
   sudo swarmd-firecracker \
     --manager \
     --force-new-cluster \
     --state-dir /var/lib/swarmkit \
     --listen-remote-api 0.0.0.0:4242 \
     --advertise-remote-api <THIS_NODE_IP>:4242 \
     --bridge-name swarm-br0 \
     --enable-cni
   ```

4. **Re-join remaining workers:**
   ```bash
   # Get new join token
   sudo cat /var/lib/swarmkit/join-tokens.txt

   # On each worker
   sudo swarmcracker cluster join --token <TOKEN> <NEW_MANAGER_IP>:4242
   ```

#### Option B: Restore from Raft backup (if you have one)

```bash
# Restore the Raft snapshot on a new node
sudo swarmd-firecracker \
  --manager \
  --state-dir /var/lib/swarmkit \
  --raft-snapshot /backup/raft-snapshot.db \
  --listen-remote-api 0.0.0.0:4242
```

---

## Scenario 3: Raft Log Corruption

**Symptoms:** Manager won't start, or swarmctl commands return inconsistent results.

### Recovery

1. **Stop the manager:**
   ```bash
   sudo systemctl stop swarmd-firecracker
   ```

2. **Try Raft recovery:**
   ```bash
   # SwarmKit has a built-in recovery mechanism
   sudo swarmd-firecracker \
     --manager \
     --force-new-cluster \
     --state-dir /var/lib/swarmkit \
     --listen-remote-api 0.0.0.0:4242 \
     --advertise-remote-api <IP>:4242
   ```

   This creates a new Raft log from the most recent consistent state.

3. **If recovery fails, reset and rebuild:**
   ```bash
   sudo rm -rf /var/lib/swarmkit/raft
   sudo swarmcracker cluster reset --force
   sudo swarmcracker cluster init --advertise-addr <IP>:4242
   # Re-join all workers
   ```

⚠️ **Resetting loses all running VM state.** Use this only as a last resort.

---

## Scenario 4: VM Snapshot Recovery

If a VM's state is lost but you have snapshots:

```bash
# List available snapshots
swarmcracker vm snapshot list <VM_ID>

# Restore from snapshot
swarmcracker vm snapshot restore <VM_ID> --snapshot <SNAPSHOT_ID>

# Verify
swarmcracker vm inspect <VM_ID>
```

### Automated snapshot backup

Set up a cron job to copy snapshots to remote storage:

```bash
# /etc/cron.d/swarmcracker-backup
0 2 * * * root rsync -avz /var/lib/swarmcracker/snapshots/ backup-server:/backups/swarmcracker/
```

---

## Scenario 5: Full Cluster Outage

All nodes lose power or network.

### Recovery Steps

1. **Power on the manager node first.**

2. **Wait for it to start:**
   ```bash
   # Manager should auto-start via systemd
   sudo systemctl status swarmd-firecracker
   swarmcracker cluster health
   ```

3. **Power on worker nodes one at a time:**
   ```bash
   # On each worker
   sudo systemctl start swarmd-firecracker
   sudo journalctl -u swarmd-firecracker -f
   # Look for "Node joined" in logs
   ```

4. **Verify cluster:**
   ```bash
   swarmcracker cluster health --format json | jq .
   swarmctl --socket /var/lib/swarmkit/swarm.sock node ls
   ```

5. **Verify VMs:**
   ```bash
   swarmcracker vm list
   swarmcracker service ps --all
   ```

6. **VMs that were running before the outage will need to be recreated:**
   ```bash
   # If using services (recommended), SwarmKit handles this automatically
   swarmcracker service update <service> --force
   ```

---

## Preventive Measures

### 1. Regular Raft backups

```bash
# Run daily via cron on the manager node
#!/bin/bash
BACKUP_DIR="/backup/swarmcracker/$(date +%Y-%m-%d)"
mkdir -p "$BACKUP_DIR"

# Backup Raft state
swarmctl --socket /var/lib/swarmkit/swarm.sock cluster snapshot \
  > "$BACKUP_DIR/raft-snapshot-$(date +%H%M).db"

# Backup config
cp /etc/swarmcracker/config.yaml "$BACKUP_DIR/"

# Backup join tokens
cp /var/lib/swarmkit/join-tokens.txt "$BACKUP_DIR/"
```

### 2. Multiple managers (production)

For production clusters with >3 nodes, run 3 managers. SwarmKit's Raft consensus requires odd numbers.

```bash
# Add a second manager
swarmcracker cluster token manager | grep TOKEN
# On new node: use the manager token
swarmcracker cluster join --role manager --token <TOKEN> <LEADER_IP>:4242
```

### 3. VM state redundancy

Use SwarmKit **services** instead of raw VMs for stateless workloads. Services automatically reschedule tasks when a worker fails.

```bash
# Good: service with replicas
swarmcracker service create --name web --replicas 3 nginx:alpine

# Instead of: single VM
swarmcracker vm create web-vm --image nginx:alpine
```

### 4. Health monitoring

```bash
# Nagios/Icinga compatible exit code
swarmcracker cluster health --format nagios
# CRITICAL: 2 checks failed | kvm=pass firecracker=pass ...
# OK: all checks passed | kvm=pass ...

# JSON for scripting/monitoring
swarmcracker cluster health --format json | jq '.healthy'
```

---

## Quick Reference

| Situation | Command |
|-----------|---------|
| Check cluster health | `swarmcracker cluster health` |
| List nodes | `swarmctl --socket /var/lib/swarmkit/swarm.sock node ls` |
| Remove dead node | `swarmctl node rm <NODE_ID>` |
| Get join token | `swarmcracker cluster token worker` |
| Force new cluster | `swarmd-firecracker --manager --force-new-cluster ...` |
| Backup Raft | `swarmctl cluster snapshot > backup.db` |
| Restore VM snapshot | `swarmcracker vm snapshot restore <ID>` |
| Service reschedule | `swarmcracker service update <name> --force` |
