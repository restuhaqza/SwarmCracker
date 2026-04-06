# Vagrant Test Automation - Fixes Applied

## Issues Fixed

### 1. **SSH Host Key Verification Failure**
**Problem:** The provisioning scripts were trying to clone the SwarmCracker repository from GitHub using SSH, which failed because the VMs didn't have SSH keys configured for GitHub access.

**Root Cause:** 
- Scripts checked for `/swarmcracker` directory first (which didn't exist)
- Fell back to `git clone` from GitHub
- The `git pull` command used SSH authentication which wasn't configured

**Solution:**
- Updated `setup-manager.sh` and `setup-worker.sh` to prioritize `/tmp/swarmcracker` (where Vagrant copies the local source)
- Added fallback checks in proper order: `/tmp/swarmcracker` → `/swarmcracker` → GitHub clone
- This ensures the provisioning uses your local code instead of trying to fetch from GitHub

### 2. **Binary Name Conflict**
**Problem:** The build command `go build -o /tmp/swarmcracker` was trying to write a binary file to a path that was already a directory.

**Root Cause:**
- Vagrant provisions the source code to `/tmp/swarmcracker/` (directory)
- The build script tried to create a binary at `/tmp/swarmcracker` (file)
- This caused a conflict: "cannot copy directory"

**Solution:**
- Changed output binary names to avoid conflicts:
  - `/tmp/swarmcracker-binary` for the CLI tool
  - `/tmp/swarmd-firecracker-binary` for the agent
- These are then copied to `/usr/local/bin/` with the correct names

### 3. **SSH Key Management in DigitalOcean**
**Problem:** Vagrant was trying to create new SSH keys for each VM, but the key names already existed in the DigitalOcean account.

**Root Cause:**
- Each `vagrant up` command tried to create a new SSH key
- DigitalOcean API rejected duplicate key names with HTTP 422 error

**Solution:**
- Updated Vagrantfile to use existing SSH key: `id_ed25519_do`
- Both manager and worker now use the same SSH key
- Added fallback to Vagrant's insecure private key for initial setup

### 4. **Dynamic IP Address Resolution**
**Problem:** The worker setup script used hardcoded IP address `192.168.56.10` for the manager, but DigitalOcean assigns dynamic private IPs.

**Root Cause:**
- VirtualBox uses predictable IPs (192.168.56.x)
- DigitalOcean uses dynamic private networking (10.x.x.x)
- Worker couldn't connect to manager at hardcoded IP

**Solution:**
- Added dynamic IP detection in `setup-worker.sh`:
  1. Try DNS resolution for `swarmcracker-manager`
  2. Probe common private IP ranges to find the manager's token server
  3. Use the discovered IP for both token fetching and cluster joining
- Manager IP is now determined at runtime: `10.104.0.6:4242`

### 5. **Hostname Pattern Mismatch**
**Problem:** The worker script expected hostname format `worker-1` but DigitalOcean used `worker1`.

**Root Cause:**
- Script regex pattern: `worker-\K\d+` (expects hyphen)
- Actual hostname: `worker1` (no hyphen)
- Worker index extraction failed

**Solution:**
- Updated regex pattern to: `worker\K\d+` (no hyphen required)
- Script now correctly extracts worker number from both formats

## Files Modified

1. **test-automation/scripts/setup-manager.sh**
   - Fixed source code path priority
   - Fixed binary name conflict
   
2. **test-automation/scripts/setup-worker.sh**
   - Fixed source code path priority
   - Fixed binary name conflict
   - Added dynamic manager IP detection
   - Fixed hostname pattern regex
   - Updated all hardcoded IPs to use `$MANAGER_PRIVATE_IP` variable

3. **test-automation/Vagrantfile**
   - Changed SSH key to use existing `id_ed25519_do`
   - Added fallback to Vagrant's insecure private key

## Current Cluster Status

```
ID                         Name     Membership  Status  Availability  Manager Status
--                         ----     ----------  ------  ------------  --------------
3x3b4o7pkc69l4jct9y4j3u8p  worker1  ACCEPTED    READY   ACTIVE        
lvpnbplti84a40car64hfrrdh           ACCEPTED    READY   ACTIVE        REACHABLE *
```

✅ **Manager:** Active and running (10.104.0.6)
✅ **Worker1:** Active and connected to cluster

## How to Use

### Check Cluster Status
```bash
cd test-automation
export DIGITAL_OCEAN_TOKEN="your_token_here"

# Check VM status
vagrant status

# Check cluster nodes
vagrant ssh manager -c "sudo swarmctl -s /var/run/swarmkit/swarm.sock node ls"
```

### Deploy a Test Service
```bash
vagrant ssh manager -c "
  sudo swarmctl -s /var/run/swarmkit/swarm.sock service create \\
    --name nginx \\
    --image nginx:alpine \\
    --replicas 2
"
```

### Check Service Status
```bash
# List services
vagrant ssh manager -c "sudo swarmctl -s /var/run/swarmkit/swarm.sock service ls"

# List tasks
vagrant ssh manager -c "sudo swarmctl -s /var/run/swarmkit/swarm.sock task ls"
```

### Monitor Worker Logs
```bash
vagrant ssh worker1 -c "sudo journalctl -u swarmd -f"
```

### Cleanup (Stop Billing!)
```bash
# Destroy all droplets
vagrant destroy -f
```

## Cost Information

- **Manager (s-4vcpu-8gb):** ~$0.07/hour ($48/month)
- **Worker1 (s-2vcpu-4gb):** ~$0.036/hour ($24/month)
- **Total:** ~$0.10/hour ($72/month)

**Important:** Remember to run `vagrant destroy -f` when done testing to stop billing!

## Next Steps

1. **Test Firecracker Integration:**
   ```bash
   vagrant ssh worker1 -c "sudo swarmcracker list"
   ```

2. **Deploy Real Workloads:**
   - Create services with multiple replicas
   - Test networking between microVMs
   - Verify resource isolation

3. **Scale the Cluster:**
   - Uncomment worker2 in Vagrantfile
   - Run `vagrant up worker2`
   - Verify it joins the cluster automatically

## Troubleshooting

### Worker Not Joining
```bash
# Check worker logs
vagrant ssh worker1 -c "sudo journalctl -u swarmd -n 100"

# Check manager accessibility
vagrant ssh worker1 -c "curl -v http://10.104.0.6:8000/tokens.json"
```

### Manager IP Changed
If you recreate the manager, the private IP may change. Worker scripts will auto-detect the new IP.

### SSH Connection Issues
```bash
# Reload SSH configuration
vagrant ssh-config manager
vagrant ssh-config worker1
```

---

**Fixed on:** 2026-02-04
**Tested with:** DigitalOcean Ubuntu 22.04, Firecracker v1.14.1, Go 1.24.0
