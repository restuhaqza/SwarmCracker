# Cluster Initialization Test - v0.4.0

## Test Status: ✅ Pre-flight Checks Verified

Local testing completed successfully. All pre-flight checks working as expected.

---

## Test Results (Local)

```
🔥 Initializing SwarmCracker Cluster
──────────────────────────────────────────────────

Running pre-flight checks...

📋 Pre-flight Checks
──────────────────────────────────────────────────
  ✓ KVM available (5ms)
  ✓ Firecracker installed (9ms)
  ✓ Kernel image exists (0ms)
  ✓ Bridge can be created (2ms)
  ✓ Sufficient memory (0ms)
  ✓ Port 4242 available (19ms)
──────────────────────────────────────────────────
✓ All checks passed (6 passed, 0 warnings)

[1/5] ✓ Directories created
[2/5] ✓ Configuration generated
[3/5] ⠦ Starting manager service...
```

**Note**: Full test requires sudo privileges for system directories.

---

## Testing on KVM Cluster

### Option 1: Test with Vagrant (If Fixed)

```bash
cd test-automation

# Fix Vagrant issue first (libvirt_ip_command warning)
# Then start cluster
vagrant up

# Copy new binary to VMs
vagrant scp ../bin/swarmcracker manager:/tmp/swarmcracker
vagrant scp ../bin/swarmd-firecracker manager:/tmp/swarmd-firecracker

# SSH to manager and test
vagrant ssh manager
sudo mv /tmp/swarmcracker /tmp/swarmd-firecracker /usr/local/bin/

# Test init command
sudo swarmcracker init --debug
```

### Option 2: Manual Test on Running VMs

If you have running VMs from previous setup:

**On Manager (192.168.121.17):**

```bash
# Stop existing services
sudo systemctl stop swarmcracker-manager swarmd-firecracker 2>/dev/null || true

# Copy new binary
scp bin/swarmcracker kali@192.168.121.17:/tmp/
scp bin/swarmd-firecracker kali@192.168.121.17:/tmp/

# SSH and install
ssh kali@192.168.121.17
sudo mv /tmp/swarmcracker /tmp/swarmd-firecracker /usr/local/bin/

# Test new init command
sudo swarmcracker init --vxlan-enabled --vxlan-peers 192.168.121.24,192.168.121.143
```

**On Worker-1 (192.168.121.24):**

```bash
# Get token from manager
ssh kali@192.168.121.17 "sudo cat /var/lib/swarmkit/join-tokens.txt"

# Copy binary
scp bin/swarmcracker kali@192.168.121.24:/tmp/
scp bin/swarmd-firecracker kali@192.168.121.24:/tmp/

# SSH and install
ssh kali@192.168.121.24
sudo mv /tmp/swarmcracker /tmp/swarmd-firecracker /usr/local/bin/

# Join cluster
sudo swarmcracker join 192.168.121.17:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.121.17,192.168.121.143
```

**On Worker-2 (192.168.121.143):**

```bash
# Same as worker-1
scp bin/swarmcracker kali@192.168.121.143:/tmp/
scp bin/swarmd-firecracker kali@192.168.121.143:/tmp/

ssh kali@192.168.121.143
sudo mv /tmp/swarmcracker /tmp/swarmd-firecracker /usr/local/bin/

sudo swarmcracker join 192.168.121.17:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.121.17,192.168.121.24
```

---

## Testing via Install Script

**On Manager:**

```bash
# Test new install.sh init mode
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- init \
  --vxlan-enabled \
  --vxlan-peers 192.168.121.24,192.168.121.143 \
  --debug
```

**On Workers:**

```bash
# Test new install.sh join mode
curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
  --manager 192.168.121.17:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled \
  --vxlan-peers 192.168.121.17,192.168.121.24 \
  --debug
```

---

## Verification Steps

After cluster initialization:

**1. Check cluster status:**
```bash
swarmcracker status
```

**2. List nodes:**
```bash
swarmcracker list nodes
```

**3. Check systemd services:**
```bash
# On manager
sudo systemctl status swarmcracker-manager

# On workers
sudo systemctl status swarmcracker-worker
```

**4. View logs:**
```bash
# Manager logs
sudo journalctl -u swarmcracker-manager -f

# Worker logs
sudo journalctl -u swarmcracker-worker -f
```

**5. Deploy test service:**
```bash
swarmcracker run nginx:alpine --detach
```

**6. Test VXLAN networking:**
```bash
# From MicroVM on worker-1, ping MicroVM on worker-2
ping 172.20.0.21
```

---

## Expected Output

### Init Command
```
🔥 Initializing SwarmCracker Cluster
──────────────────────────────────────────────────

Running pre-flight checks...

📋 Pre-flight Checks
──────────────────────────────────────────────────
  ✓ KVM available (5ms)
  ✓ Firecracker installed (9ms)
  ✓ Kernel image exists (0ms)
  ✓ Bridge can be created (2ms)
  ✓ Sufficient memory (0ms)
  ✓ Port 4242 available (19ms)
──────────────────────────────────────────────────
✓ All checks passed (6 passed, 0 warnings)

[1/5] ✓ Directories created
[2/5] ✓ Configuration generated
[3/5] ✓ Manager service started
[4/5] ✓ Manager ready
[5/5] ✓ Join tokens generated

✅ SwarmCracker cluster initialized!

Manager: swarm-manager (192.168.121.17:4242)

Next steps:
  1. Get join token: sudo cat /var/lib/swarmkit/join-tokens.txt
  2. On workers, run:
     curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | sudo bash -s -- join \
       --manager 192.168.121.17:4242 --token <TOKEN>
```

### Join Command
```
🔗 Joining SwarmCracker Cluster
──────────────────────────────────────────────────

Running pre-flight checks...

📋 Pre-flight Checks
──────────────────────────────────────────────────
  ✓ KVM available (4ms)
  ✓ Firecracker installed (8ms)
  ✓ Kernel image exists (0ms)
  ✓ Bridge can be created (2ms)
  ✓ Sufficient memory (0ms)
  ✓ Manager connectivity (15ms)
──────────────────────────────────────────────────
✓ All checks passed (6 passed, 0 warnings)

[1/5] ✓ Manager connectivity validated
[2/5] ✓ Directories created
[3/5] ✓ Configuration generated
[4/5] ✓ Worker service started
[5/5] ✓ Cluster join verified

✅ Installation & Join Complete

Node joined the cluster successfully!
```

---

## Rollback Plan

If something goes wrong:

```bash
# Stop new services
sudo systemctl stop swarmcracker-manager swarmcracker-worker

# Restore old binaries (if backed up)
sudo mv /usr/local/bin/swarmcracker.backup /usr/local/bin/swarmcracker
sudo mv /usr/local/bin/swarmd-firecracker.backup /usr/local/bin/swarmd-firecracker

# Or reinstall from previous release
wget https://github.com/restuhaqza/SwarmCracker/releases/download/v0.3.0/swarmcracker-v0.3.0-linux-amd64.tar.gz
tar -xzf swarmcracker-v0.3.0-linux-amd64.tar.gz
sudo cp swarmcracker-v0.3.0-linux-amd64/* /usr/local/bin/
```

---

## Success Criteria

- [ ] Pre-flight checks run and display correctly
- [ ] Progress indicators show step-by-step
- [ ] Manager initializes successfully
- [ ] Join tokens generated and displayed
- [ ] Workers can join with token
- [ ] VXLAN networking works across nodes
- [ ] Services start via systemd
- [ ] Logs are accessible via journalctl
- [ ] No errors in pre-flight or initialization

---

## Test Report Template

```markdown
## Test Report - v0.4.0 Cluster Init

**Date:** 2026-04-07
**Tester:** [Name]
**Environment:** KVM/libvirt (3 nodes)

### Results

| Test | Status | Notes |
|------|--------|-------|
| Pre-flight checks | ✅ PASS | All 6 checks passed |
| Manager init | ✅/❌ | |
| Worker join (1) | ✅/❌ | |
| Worker join (2) | ✅/❌ | |
| VXLAN networking | ✅/❌ | |
| Service deployment | ✅/❌ | |

### Issues Found

[List any bugs or issues]

### Recommendations

[Any improvements or fixes needed]
```
