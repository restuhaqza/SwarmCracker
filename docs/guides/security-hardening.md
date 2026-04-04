# Security Hardening Guide

**SwarmCracker Security Best Practices for Production Deployment**

## Table of Contents

1. [Overview](#overview)
2. [Security Features](#security-features)
3. [Configuration](#configuration)
4. [Best Practices](#best-practices)
5. [Hardening Checklist](#hardening-checklist)
6. [Troubleshooting](#troubleshooting)

---

## Overview

SwarmCracker provides multiple security layers to isolate Firecracker VMs and protect the host system:

### Security Layers

```
┌─────────────────────────────────────────────────┐
│           Application Layer                     │
│       (SwarmCracker, SwarmKit)                 │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│         Security Manager                        │
│  • Resource Limits                              │
│  • Secure Permissions                           │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│            Jailer Layer                         │
│  • Chroot Jail                                  │
│  • User/Group Isolation                         │
│  • Network Namespace Isolation                  │
└─────────────────┬───────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────┐
│         Firecracker VM                          │
│  • KVM Hardware Virtualization                  │
│  • seccomp Filters                              │
└─────────────────────────────────────────────────┘
```

---

## Security Features

### 1. Jailer (Chroot Isolation)

**Purpose:** Isolate each VM in its own chroot jail

**Features:**
- Each VM runs in isolated directory
- Privilege dropping (runs as non-root user)
- Separate /dev, /proc, /run filesystems
- Prevents escape from jail

**Configuration:**
```yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: /srv/jailer
    netns: ""  # Optional: network namespace name
```

**How It Works:**
1. Creates `/srv/jailer/<vm-id>/` directory
2. Sets up minimal filesystem structure
3. Changes root to jail directory
4. Drops privileges to specified UID/GID
5. VM runs isolated from host filesystem

### 2. Resource Limits

**Purpose:** Prevent VMs from consuming excessive resources

**Limits Applied:**
- **File Descriptors:** Max 4096 FDs per VM
- **Processes:** Max 1024 processes per VM
- **Memory:** Managed by Firecracker (configured per VM)
- **CPU:** Can be limited via cgroups

**Configuration:**
```yaml
# Applied automatically when jailer is enabled
# Limits can be customized in security manager
```

**How It Works:**
1. Creates cgroup for VM process
2. Sets `memory.max` limit
3. Sets `cpu.max` quota
4. Adds process to cgroup
5. Enforces limits automatically

### 3. seccomp Filters

**Purpose:** Restrict system calls available to VMs

**Default Profile:**
- Allows: Basic I/O, networking, file operations
- Blocks: Dangerous operations (mount, module loading)
- Applied via Firecracker configuration

**Configuration:**
```yaml
# Generated automatically at /srv/jailer/<vm-id>/seccomp.json
# Can be customized for different security levels
```

**Restrictive Mode:**
- Blocks all privileged operations
- Suitable for untrusted workloads
- May break some applications

### 4. Network Namespace Isolation

**Purpose:** Isolate VM network from host

**Configuration:**
```yaml
executor:
  enable_jailer: true
  jailer:
    netns: vmnet0  # Network namespace name
```

**How It Works:**
1. Creates separate network namespace
2. VM runs in isolated network stack
3. Prevents VM from accessing host network
4. Requires veth pairs for connectivity

### 5. Secure File Permissions

**Purpose:** Protect sensitive files

**Applied To:**
- VM root filesystems (0600)
- Socket directories (0700)
- Configuration files (0600)
- Jailer directories (0700)

**Automatic:**
- Applied on VM creation
- Verified on startup
- Logged for audit

---

## Configuration

### Minimal Security (Development)

```yaml
executor:
  enable_jailer: false  # Disable jailer for dev
  kernel_path: /usr/share/firecracker/vmlinux
  rootfs_dir: /var/lib/firecracker/rootfs
```

**Use Case:** Local development, testing

### Standard Security (Production)

```yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: /srv/jailer
    netns: ""  # No network namespace
```

**Use Case:** Production with trusted workloads

### High Security (Untrusted Workloads)

```yaml
executor:
  enable_jailer: true
  jailer:
    uid: 1000
    gid: 1000
    chroot_base_dir: /srv/jailer
    netns: vmnet0  # Network isolation

network:
  enable_rate_limit: true
  max_packets_per_sec: 10000
```

**Use Case:** Multi-tenant, untrusted code

---

## Best Practices

### 1. Always Run Jailer in Production

**Why:** Prevents VM escape, isolates filesystem

```bash
# Verify jailer is enabled
swarmctl node inspect self --pretty | grep jailer
```

### 2. Use Non-Root User

**Why:** Limits damage if VM is compromised

```yaml
jailer:
  uid: 1000  # Never use 0
  gid: 1000  # Never use 0
```

### 3. Isolate Network

**Why:** Prevents VM from accessing host network

```yaml
jailer:
  netns: vmnet0
```

### 4. Apply Resource Limits

**Why:** Prevents resource exhaustion

```bash
# Monitor cgroup usage
cat /sys/fs/cgroup/swarmcracker/<vm-id>/memory.current
```

### 5. Regular Security Audits

**Why:** Detect misconfigurations

```bash
# Check file permissions
find /srv/jailer -perm /o+w -ls

# Check for root-owned files
find /srv/jailer -user root -ls
```

### 6. Monitor Security Events

**Why:** Detect attacks early

```bash
# Monitor jailer logs
journalctl -u swarmcracker -f | grep -i jail

# Monitor seccomp violations
dmesg | grep seccomp
```

---

## Hardening Checklist

### Pre-Deployment

- [ ] Jailers enabled in configuration
- [ ] Non-root UID/GID configured
- [ ] chroot directory exists and has correct permissions
- [ ] Resource limits configured
- [ ] seccomp filters enabled
- [ ] Network isolation configured (if needed)
- [ ] File permissions verified
- [ ] Security manager tested

### Post-Deployment

- [ ] Verify all VMs run in jail
- [ ] Check resource limits applied
- [ ] Test network isolation
- [ ] Verify seccomp profiles loaded
- [ ] Monitor for security events
- [ ] Run security audit

### Ongoing

- [ ] Regular permission checks (weekly)
- [ ] Monitor resource usage (daily)
- [ ] Review security logs (daily)
- [ ] Update seccomp profiles (as needed)
- [ ] Test jail escape attempts (quarterly)

---

## Troubleshooting

### VM Won't Start

**Symptom:** VM fails to start with permission error

**Check:**
```bash
# Check jail directory permissions
ls -la /srv/jailer/<vm-id>/

# Should be owned by jailer UID/GID
# Should have 0700 permissions
```

**Fix:**
```bash
# Fix permissions
sudo chown -R 1000:1000 /srv/jailer/<vm-id>/
sudo chmod -R 0700 /srv/jailer/<vm-id>/
```

### Resource Limits Not Applied

**Symptom:** VM uses more resources than configured

**Check:**
```bash
# Check cgroup
cat /sys/fs/cgroup/swarmcracker/<vm-id>/memory.max
cat /sys/fs/cgroup/swarmcracker/<vm-id>/cpu.max
```

**Fix:**
```bash
# Manually apply limits
echo 2097152 > /sys/fs/cgroup/swarmcracker/<vm-id>/memory.max
```

### Network Namespace Issues

**Symptom:** VM has no network access

**Check:**
```bash
# Check network namespace exists
ip netns list

# Check namespace has interfaces
ip netns exec vmnet0 ip link show
```

**Fix:**
```bash
# Create network namespace
sudo ip netns add vmnet0

# Setup veth pair
sudo ip link add veth0 type veth peer name veth1
sudo ip link set veth1 netns vmnet0
```

### seccomp Violations

**Symptom:** VM crashes with seccomp error

**Check:**
```bash
# Check dmesg for violations
dmesg | grep seccomp

# Check seccomp profile
cat /srv/jailer/<vm-id>/seccomp.json
```

**Fix:**
```bash
# Use default seccomp profile
# Or customize to allow required syscalls
```

---

## Security Audit

### Manual Audit

```bash
# Check all jails
ls -la /srv/jailer/

# Check file ownership
find /srv/jailer ! -user 1000 -ls

# Check permissions
find /srv/jailer -perm /o+w -ls

# Check cgroups
ls -la /sys/fs/cgroup/swarmcracker/

# Check seccomp profiles
find /srv/jailer -name seccomp.json -exec cat {} \;
```

### Automated Audit

```bash
# Run security check
swarmcracker security audit

# Expected output:
# ✅ Jailer enabled
# ✅ All VMs jailed
# ✅ Resource limits applied
# ✅ seccomp filters active
# ✅ File permissions secure
```

---

## Performance Impact

### Overhead

- **Jailer:** ~5% CPU overhead
- **seccomp:** <1% overhead
- **Resource Limits:** Negligible
- **Network Namespace:** ~2% network overhead

### Optimization

1. **Reuse Jailer Directories:** Don't cleanup between VM restarts
2. **Cache seccomp Profiles:** Generate once, reuse
3. **Tune Resource Limits:** Match actual workload needs
4. **Monitor Performance:** Adjust as needed

---

## References

- Firecracker Security Documentation
- seccomp Documentation (kernel.org)
- Linux Capabilities (man pages: capabilities.7)
- cgroups v2 Documentation (kernel.org)

---

**Last Updated:** 2026-04-04
**Version:** 1.0
**Maintained By:** SwarmCracker Security Team
