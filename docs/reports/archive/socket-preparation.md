# Socket Preparation for SwarmKit

## Problem

By default, `swarmd` does **not** automatically create the unix socket directory or file. The socket directory `/var/run/swarmkit` must exist before `swarmd` starts, otherwise the control API socket won't be available.

## Solutions Implemented

### 1. **prepare-socket.sh** Script
Location: `scripts/prepare-socket.sh`

This script:
- ✅ Creates `/var/run/swarmkit` directory with proper permissions (755)
- ✅ Removes stale socket files from previous runs
- ✅ Installs tmpfiles.d configuration for persistence across reboots
- ✅ Can be run on both manager and worker nodes

**Usage:**
```bash
sudo bash scripts/prepare-socket.sh
```

**What it does:**
```bash
# Create directory
sudo mkdir -p /var/run/swarmkit
sudo chmod 755 /var/run/swarmkit

# Install tmpfiles.d config
sudo tee /etc/tmpfiles.d/swarmkit.conf > /dev/null <<EOF
d /var/run/swarmkit 0755 root root -
EOF
```

### 2. **tmpfiles.d Configuration**
Location: `scripts/swarmkit-tmpfiles.conf`

Systemd-tmpfiles ensures the directory is recreated at boot time.

**Installation:**
```bash
sudo cp scripts/swarmkit-tmpfiles.conf /etc/tmpfiles.d/swarmkit.conf
sudo systemd-tmpfiles --create
```

### 3. **Integration with Setup Scripts**

Both `setup-manager.sh` and `setup-worker.sh` now automatically call `prepare-socket.sh` before starting `swarmd`.

**Manager Setup Flow:**
1. Build SwarmKit binaries
2. Build SwarmCracker
3. **Prepare socket directory** ← NEW
4. Create systemd service
5. Start swarmd
6. Wait for socket file to be created

**Worker Setup Flow:**
1. Build SwarmKit binaries
2. Build SwarmCracker
3. Validate config
4. **Prepare socket directory** ← NEW (for consistency)
5. Join cluster
6. Start swarmd

## Verification

### Check Socket Directory Exists
```bash
ls -ld /var/run/swarmkit
# Expected: drwxr-xr-x 2 root root 4096 ... /var/run/swarmkit
```

### Check Socket File (Manager Only)
```bash
ls -l /var/run/swarmkit/swarm.sock
# Expected: srwxr-xr-x 1 root root ... /var/run/swarmkit/swarm.sock
```

### Test Socket Connection
```bash
# From manager node
export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
swarmctl node ls

# If this works, the socket is functioning correctly
```

### Check tmpfiles.d Configuration
```bash
cat /etc/tmpfiles.d/swarmkit.conf
# Expected: d /var/run/swarmkit 0755 root root -
```

## Troubleshooting

### Socket Not Created After Starting swarmd

**Symptoms:**
- `ls -l /var/run/swarmkit/swarm.sock` → No such file or directory
- `swarmctl` commands fail with connection refused

**Diagnosis:**
```bash
# Check if swarmd is running
sudo systemctl status swarmd

# Check swarmd logs for socket creation errors
sudo journalctl -u swarmd -n 50

# Check if directory exists
ls -ld /var/run/swarmkit
```

**Fix:**
```bash
# 1. Ensure directory exists
sudo mkdir -p /var/run/swarmkit
sudo chmod 755 /var/run/swarmkit

# 2. Restart swarmd
sudo systemctl restart swarmd

# 3. Wait and verify
sleep 5
ls -l /var/run/swarmkit/swarm.sock
```

### Socket Directory Removed After Reboot

**Symptoms:**
- `/var/run/swarmkit` doesn't exist after VM reboot
- swarmd fails to start

**Fix:**
```bash
# Install tmpfiles.d configuration
sudo tee /etc/tmpfiles.d/swarmkit.conf > /dev/null <<EOF
d /var/run/swarmkit 0755 root root -
EOF

# Create directory immediately
sudo systemd-tmpfiles --create

# Restart swarmd
sudo systemctl restart swarmd
```

### Permission Denied

**Symptoms:**
- `swarmctl` fails with permission denied
- Socket exists but can't be accessed

**Fix:**
```bash
# Fix permissions
sudo chmod 755 /var/run/swarmkit
sudo chmod 666 /var/run/swarmkit/swarm.sock

# Or add your user to the appropriate group
sudo usermod -aG root $(whoami)  # Not recommended for production
```

## Best Practices

1. **Always run `prepare-socket.sh` before starting swarmd** (setup scripts do this automatically)
2. **Use tmpfiles.d for persistence** - ensures directory survives reboots
3. **Check socket file existence** after starting swarmd
4. **Monitor swarmd logs** if socket creation fails

## Summary

The socket preparation is now **automated** in the setup scripts. You shouldn't need to manually prepare the socket directory unless:

- You're doing manual testing outside the Vagrant environment
- You've modified the setup scripts
- You're troubleshooting socket-related issues

**Key Files:**
- `scripts/prepare-socket.sh` - Automated preparation script
- `scripts/swarmkit-tmpfiles.conf` - Persistent directory configuration
- `scripts/setup-manager.sh` - Calls prepare-socket.sh automatically
- `scripts/setup-worker.sh` - Calls prepare-socket.sh automatically

---

**Last Updated:** 2026-02-01  
**Status:** ✅ Automated and integrated
