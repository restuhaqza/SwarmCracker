# Jailer Quick Start Guide

**Status:** ✅ Implementation Complete | 🧪 Testing Required

---

## What is Jailer?

Firecracker Jailer is a process isolation tool that provides:
- 🔒 **Chroot isolation** - Firecracker can't access host filesystem
- 👤 **Privilege dropping** - Runs as unprivileged user (not root)
- 📊 **Cgroup limits** - CPU, memory, I/O resource constraints
- 🛡️ **Seccomp filtering** - Restricts syscalls to minimal safe set

This is **production-grade security** for multi-tenant SwarmCracker deployments.

---

## Quick Start (5 minutes)

### 1. Install Firecracker + Jailer

```bash
# Download latest release
wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.0/firecracker-v1.14.0-x86_64.tgz
tar xzf firecracker-v1.14.0-x86_64.tgz

# Install binaries
sudo cp release-v1.14.0-x86_64/firecracker /usr/local/bin/
sudo cp release-v1.14.0-x86_64/jailer /usr/local/bin/

# Verify installation
firecracker --version  # v1.14.0
jailer --version       # v1.14.0
```

### 2. Create Firecracker User

```bash
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/swarmcracker firecracker
sudo chown -R firecracker:firecracker /var/lib/swarmcracker
```

### 3. Start SwarmCracker with Jailer

```bash
sudo swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token SWMTKN-1-... \
  --enable-jailer \
  --jailer-uid 1000 \
  --jailer-gid 1000 \
  --enable-cgroups
```

That's it! All MicroVMs will now run inside jailer with full isolation.

---

## Configuration File

Create `/etc/swarmcracker/executor-config.yaml`:

```yaml
# Basic settings
firecracker_path: /usr/local/bin/firecracker
kernel_path: /usr/share/firecracker/vmlinux
rootfs_dir: /var/lib/firecracker/rootfs
socket_dir: /var/run/firecracker

# Jailer settings
enable_jailer: true
jailer_path: /usr/local/bin/jailer
jailer_uid: 1000
jailer_gid: 1000
jailer_chroot_dir: /var/lib/swarmcracker/jailer
cgroup_version: v2
enable_cgroups: true

# Resource limits per VM
default_vcpus: 1
default_memory_mb: 512
```

Then start normally:
```bash
sudo swarmd-firecracker --config /etc/swarmcracker/executor-config.yaml
```

---

## Verification

### Check Jailer is Running

```bash
# Find Firecracker processes
ps aux | grep firecracker

# Should show UID 1000 (firecracker user), not root
firecracker+  12345  0.0  0.1  ...  /usr/local/bin/jailer --id vm-xxx ...
```

### Check Chroot Structure

```bash
ls -la /var/lib/swarmcracker/jailer/<task-id>/
# Expected:
# root/     - Chroot filesystem
# run/      - Runtime files (sockets)
# log/      - Logging FIFO
```

### Check Cgroup Limits

```bash
# CPU limit (should show quota/period)
cat /sys/fs/cgroup/swarmcracker/<task-id>/cpu.max
# Example: 1000000 1000000 (1 CPU core)

# Memory limit (in bytes)
cat /sys/fs/cgroup/swarmcracker/<task-id>/memory.max
# Example: 536870912 (512MB)

# Current memory usage
cat /sys/fs/cgroup/swarmcracker/<task-id>/memory.current
```

### Check Seccomp

```bash
# Find Firecracker PID
PID=$(pgrep -f "firecracker.*--id")

# Check seccomp status
cat /proc/$PID/status | grep Seccomp
# Should show: Seccomp: 2 (filter mode active)

# View allowed syscalls
cat /proc/$PID/seccomp_filters
```

### Test Resource Limits

```bash
# Deploy a VM and try to exceed memory limit
swarmcracker deploy stress-test --memory 1024

# Monitor cgroup stats
watch -n1 'cat /sys/fs/cgroup/swarmcracker/<task-id>/memory.current'

# Should see throttling or OOM when exceeding limit
```

---

## Troubleshooting

### Jailer fails to start

**Error:** `failed to start jailer: permission denied`

**Fix:** Ensure firecracker user exists and owns chroot directory:
```bash
sudo useradd -r -s /usr/sbin/nologin firecracker
sudo chown -R firecracker:firecracker /var/lib/swarmcracker/jailer
```

### Cgroup errors

**Error:** `failed to create cgroup: operation not permitted`

**Fix:** Verify cgroup v2 is enabled:
```bash
cat /sys/fs/cgroup/cgroup.controllers
# Should list controllers, not error
```

If using cgroup v1, set `--cgroup-version v1`.

### Seccomp kills Firecracker

**Error:** Firecracker exits immediately with SIGSYS

**Fix:** Seccomp policy is too restrictive. Either:
1. Disable seccomp temporarily: `--seccomp=false`
2. Add missing syscalls to policy
3. Check logs for which syscall was blocked

### Socket not created

**Error:** `socket not created: context deadline exceeded`

**Fix:** Check jailer logs:
```bash
journalctl -u swarmd-firecracker -f
# Or check syslog
tail -f /var/log/syslog | grep jailer
```

Common causes:
- Firecracker binary not found
- Kernel/rootfs paths incorrect
- Permission issues in chroot

---

## Migration from Legacy Mode

### Before (Legacy)
```bash
sudo swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token SWMTKN-1-...
```

### After (Jailer)
```bash
sudo swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token SWMTKN-1-... \
  --enable-jailer \
  --jailer-uid 1000 \
  --jailer-gid 1000
```

### Breaking Changes

1. **Socket path changes:**
   - Old: `/var/run/firecracker/<task-id>.sock`
   - New: `/var/lib/swarmcracker/jailer/<task-id>/run/firecracker/<task-id>.sock`
   
   **Impact:** External tools that access Firecracker API directly need path update.

2. **Process ownership:**
   - Old: Root
   - New: UID 1000 (firecracker user)
   
   **Impact:** Logs may show different UID; adjust monitoring/alerting.

3. **Resource usage:**
   - Old: No limits
   - New: Cgroup limits enforced
   
   **Impact:** VMs may be throttled if exceeding limits; adjust resource requests.

### Rollback Plan

If jailer causes issues, simply remove `--enable-jailer` flag:
```bash
sudo systemctl stop swarmd-firecracker
# Edit /etc/systemd/system/swarmd-firecracker.service
# Remove --enable-jailer and related flags
sudo systemctl daemon-reload
sudo systemctl start swarmd-firecracker
```

---

## Performance Overhead

Based on Firecracker benchmarks:

| Metric | Overhead |
|--------|----------|
| CPU | < 1% |
| Memory | +5-10MB per VM |
| Boot time | +50-100ms |
| Network I/O | Negligible |
| Disk I/O | Negligible |

**Conclusion:** Overhead is minimal for production security benefits.

---

## Security Best Practices

1. **Dedicated user:** Always run jailer as dedicated `firecracker` user
2. **Minimal privileges:** Don't run as root unless absolutely necessary
3. **Cgroup limits:** Set appropriate CPU/memory limits per VM
4. **Monitor stats:** Watch cgroup metrics for anomalies
5. **Update regularly:** Keep Firecracker/jailer binaries up to date
6. **Audit logs:** Enable host audit logging for security monitoring
7. **Network isolation:** Consider network namespaces for multi-tenant setups

---

## Next Steps

### Testing
1. Deploy test cluster with jailer enabled
2. Verify isolation (can't access host FS, processes, etc.)
3. Test resource limits (OOM, CPU throttling)
4. Test graceful shutdown
5. Verify cross-node networking still works

### Production Rollout
1. Start with staging environment
2. Monitor for 1 week
3. Gradually roll to production nodes
4. Keep rollback plan ready

### Future Enhancements
- [ ] Network namespace integration (CNI)
- [ ] Per-VM seccomp policies
- [ ] IO bandwidth limits (device discovery)
- [ ] Jailer health monitoring
- [ ] Automated security auditing

---

## References

- [Firecracker Jailer Documentation](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md)
- [Cgroup v2 Documentation](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html)
- [Seccomp Documentation](https://www.kernel.org/doc/html/latest/userspace-api/seccomp.html)
- [Security Guide](docs/guides/security.md) — Jailer configuration and hardening

---

**Questions?** Check the full implementation plan or reach out on GitHub Issues.
