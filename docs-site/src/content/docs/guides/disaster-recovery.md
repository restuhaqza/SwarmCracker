---
title: "Disaster Recovery"
description: "How to recover SwarmCracker from node failures, Raft corruption, and cluster outages."
---

> How to recover SwarmCracker from node failures, Raft corruption, and cluster outages.

---

## Overview

SwarmCracker cluster state is stored in multiple locations:

| Component | Location | Backup Strategy |
|-----------|----------|----------------|
| Raft log (cluster state) | `/var/lib/swarmkit/` | Stop the manager and copy the directory |
| VM state | `/var/lib/firecracker/` | VM snapshots |
| VM snapshots | `/var/lib/firecracker/snapshots/` | Copy to remote storage |
| Config | `/etc/swarmcracker/config.yaml` | Auto-generated on join |
| Join tokens | `/var/lib/swarmkit/join-tokens.txt` | Backed up with the state directory |

---

## Scenario 1: Worker Node Failure

### Detection
```bash
# Check the suspect node (run locally there, or over ssh)
swarmcracker cluster health

# List cluster nodes (run on a manager)
swarmcracker node ls
# The failed worker's STATUS is shown as "Down"
```

### Recovery Steps

1. **Verify the node is truly dead:**
   ```bash
   ssh dead-worker -- swarmcracker cluster health
   ```

2. **Remove the dead node from the cluster:**
   ```bash
   swarmcracker node rm <NODE_ID>
   ```
   (Add `--force` if the node is still reported as `Ready`.)

3. **VMs on the dead node are lost.** SwarmKit will reschedule services with desired replicas. Verify:
   ```bash
   swarmcracker service ps <service-name>
   ```

4. **Replace the worker:**
   ```bash
   # Get a new join token on the manager
   swarmcracker cluster token worker

   # On the replacement node: install binary + setup deps, then join
   curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash
   sudo swarmcracker setup install --download-kernel --download-rootfs
   sudo swarmcracker setup network
   sudo swarmcracker setup config --non-interactive
   sudo swarmcracker cluster join <MANAGER_IP>:4242 --token <TOKEN>
   ```

---

## Scenario 2: Manager Node Failure

⚠️ **Manager failure requires immediate action** — the Raft consensus log is on the manager.

### If you have >1 manager (recommended for production)

SwarmKit's Raft consensus handles this automatically. The remaining managers elect a new leader.

```bash
# Check node roles and health
swarmcracker node ls
swarmcracker node inspect <node-id>   # shows the node's role
```

### If you have only 1 manager (typical for small clusters)

You need to promote a worker or set up a new manager.

#### Option A: Promote a worker to manager

1. **On a healthy worker, stop the swarmd service:**
   ```bash
   sudo systemctl stop swarmcracker-worker
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

#### Option B: Restore from a state-directory backup (if you have one)

SwarmCracker does not expose a Raft snapshot command; back up and restore the
manager state as files (see [Preventive Measures](#preventive-measures)).

```bash
# Stop the manager, then restore the backed-up state directory
sudo systemctl stop swarmcracker-manager
sudo rm -rf /var/lib/swarmkit
sudo cp -a /backup/swarmkit /var/lib/swarmkit

sudo swarmd-firecracker \
  --manager \
  --force-new-cluster \
  --state-dir /var/lib/swarmkit \
  --listen-remote-api 0.0.0.0:4242
```

---

## Scenario 3: Raft Log Corruption

**Symptoms:** Manager won't start, or swarmctl commands return inconsistent results.

### Recovery

1. **Stop the manager:**
   ```bash
   sudo systemctl stop swarmcracker-manager
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
   sudo swarmcracker cluster reset
   sudo swarmcracker cluster init --advertise-addr <IP>:4242
   # Re-join all workers
   ```

⚠️ **Resetting loses all running VM state.** Use this only as a last resort.

---

## Scenario 4: VM Snapshot Recovery

If a VM's state is lost but you have snapshots (see the
[Snapshots guide](/guides/snapshots/) for the full workflow):

```bash
# List available snapshots
swarmcracker vm snapshot list --task <VM_ID>

# Restore from snapshot
swarmcracker vm snapshot restore <SNAPSHOT_ID>

# Verify
swarmcracker vm status <VM_ID>
```

### Automated snapshot backup

Set up a cron job to copy snapshots to remote storage:

```bash
# /etc/cron.d/swarmcracker-backup
0 2 * * * root rsync -avz /var/lib/firecracker/snapshots/ backup-server:/backups/swarmcracker/
```

---

## Scenario 5: Full Cluster Outage

All nodes lose power or network.

### Recovery Steps

1. **Power on the manager node first.**

2. **Wait for it to start:**
   ```bash
   # Manager should auto-start via systemd
   sudo systemctl status swarmcracker-manager
   swarmcracker cluster health
   ```

3. **Power on worker nodes one at a time:**
   ```bash
   # On each worker
   sudo systemctl start swarmcracker-worker
   sudo journalctl -u swarmcracker-worker -f
   # Look for "Node joined" in logs
   ```

4. **Verify cluster:**
   ```bash
   swarmcracker cluster health --format json | jq .
   swarmcracker node ls
   ```

5. **Verify VMs:**
   ```bash
   swarmcracker vm list
   swarmcracker task ls --all
   ```

6. **VMs that were running before the outage will need to be recreated:**
   ```bash
   # If using services (recommended), SwarmKit handles this automatically
   swarmcracker service update <service> --force
   ```

---

## Preventive Measures

### 1. Regular manager state backups

SwarmCracker has no Raft snapshot command. Back up the manager's state
directory as files while the manager is stopped:

```bash
# Run daily via cron on the manager node
#!/bin/bash
BACKUP_DIR="/backup/swarmcracker/$(date +%Y-%m-%d)"
mkdir -p "$BACKUP_DIR"

# Backup manager state (Raft log, certificates, join tokens)
systemctl stop swarmcracker-manager
cp -a /var/lib/swarmkit "$BACKUP_DIR/swarmkit"
systemctl start swarmcracker-manager

# Backup config
cp /etc/swarmcracker/config.yaml "$BACKUP_DIR/"
```

### 2. Multiple managers (production)

For production clusters with >3 nodes, run 3 managers. SwarmKit's Raft consensus requires odd numbers.

```bash
# Add a second manager
swarmcracker cluster token manager

# On the new node: join with the manager token
swarmcracker cluster join <LEADER_IP>:4242 --manager --token <TOKEN>
```

### 3. VM state redundancy

Use SwarmKit **services** instead of raw VMs for stateless workloads. Services automatically reschedule tasks when a worker fails.

```bash
# Good: service with replicas
swarmcracker service create --name web --image nginx:alpine --replicas 3

# Instead of: single VM
swarmcracker vm create --name web-vm nginx:alpine
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
| List nodes | `swarmcracker node ls` |
| Remove dead node | `swarmcracker node rm <NODE_ID>` |
| Get join token | `swarmcracker cluster token worker` |
| Force new cluster | `swarmd-firecracker --manager --force-new-cluster ...` |
| Back up manager state | stop the manager, then `cp -a /var/lib/swarmkit <dest>` |
| Restore VM snapshot | `swarmcracker vm snapshot restore <ID>` |
| Service reschedule | `swarmcracker service update <name> --force` |
