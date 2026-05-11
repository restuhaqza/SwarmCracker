# Security Guide — SwarmCracker

> Comprehensive security documentation for SwarmCracker operators and developers.

---

## Table of Contents

- [Security Model](#security-model)
- [Hardening Checklist](#hardening-checklist)
- [Seccomp Profile](#seccomp-profile)
- [Secure Deployment Guide](#secure-deployment-guide)
- [Init Injection Troubleshooting](#init-injection-troubleshooting)
- [CVE / Advisory History](#cve--advisory-history)

---

## Security Model

### Architecture

```
┌─────────────────────────────────────────────┐
│                  SwarmKit                    │
│  ┌─────────┐  ┌─────────┐  ┌─────────┐     │
│  │ Manager │  │ Worker  │  │ Worker  │     │
│  └────┬────┘  └────┬────┘  └────┬────┘     │
│       │            │            │           │
│  ┌────▼────────────▼────────────▼────┐      │
│  │     swarmd-firecracker agent     │      │
│  │  ┌──────────┐  ┌──────────────┐  │      │
│  │  │ Executor │  │ VMM Manager  │  │      │
│  │  └────┬─────┘  └──────┬───────┘  │      │
│  │       │               │          │      │
│  │  ┌────▼───────────────▼────┐     │      │
│  │  │   Firecracker microVM   │     │      │
│  │  │  ┌───────────────────┐  │     │      │
│  │  │  │   Guest Kernel    │  │     │      │
│  │  │  │  ┌─────────────┐  │  │     │      │
│  │  │  │  │  Container  │  │  │     │      │
│  │  │  │  └─────────────┘  │  │     │      │
│  │  │  └───────────────────┘  │     │      │
│  │  └────────────────────────┘     │      │
│  └─────────────────────────────────┘      │
└─────────────────────────────────────────────┘
```

### Isolation Boundaries

| Layer | Technology | Protection |
|-------|-----------|------------|
| Hardware | KVM | Separate kernel per VM, hardware-enforced isolation |
| Process | Jailer chroot | Restricted filesystem access, non-root execution |
| Network | TAP + Bridge + VXLAN | Per-VM network device, overlay isolation |
| Syscall | Seccomp | Blocked privileged syscalls in guest |
| Storage | Per-VM rootfs | Isolated ext4 filesystem per VM |

### Trust Boundaries

| Component | Trust | Rationale |
|-----------|-------|-----------|
| SwarmKit Manager | High | Controls scheduling, secrets, join tokens |
| swarmd-firecracker | High | KVM access, root privileges for networking |
| Firecracker VMM | High | Rust-based, minimal attack surface |
| Guest VM | Low | Hardware-isolated, attack limited to VM |

---

## Hardening Checklist

### Production Deployment

- [ ] **Config file permissions**: All config files at `0600`, directories at `0700`
- [ ] **Join tokens**: Never hardcoded in scripts or committed to VCS
- [ ] **Token logging**: Tokens logged at DEBUG level only (never INFO/WARN)
- [ ] **Health endpoint**: Bind to `127.0.0.1:8080` (not `0.0.0.0`)
- [ ] **TLS for Consul**: Enable `UseTLS`, provide `TLSCertFile`/`TLSKeyFile`
- [ ] **TLS for SwarmKit**: Encrypt manager-worker gRPC communication
- [ ] **Build hardening**: Use `-trimpath -buildmode=pie -ldflags "-s -w"`
- [ ] **Jailer enabled**: Run microVMs in chroot with non-root UID/GID
- [ ] **seccomp enabled**: Block privileged syscalls in guest kernel

### CI / Development

- [ ] **gosec** linter enabled in CI pipeline
- [ ] **nilerr**, **prealloc**, **noctx** linters active
- [ ] **Race detector** in test suite (`go test -race`)
- [ ] **Pre-commit hook**: Secret scanner checks for hardcoded tokens/keys
- [ ] **Dependency scanning**: Periodic audit of `go.mod` for CVEs

### File Permission Reference

| Path | Permission | Reason |
|------|-----------|--------|
| `/etc/swarmcracker/config.yaml` | `0600` | Contains join token, network config |
| `/var/lib/swarmkit/state/` | `0700` | Cluster state, certificates |
| `/var/lib/swarmcracker/snapshots/` | `0700` | VM memory snapshots |
| `/var/lib/swarmcracker/volumes/` | `0700` | Volume metadata |
| `/var/lib/swarmcracker/rootfs/` | `0755` | Read-only rootfs images |
| `/var/run/firecracker/` | `0755` | API sockets (runtime only) |

---

## Seccomp Profile

SwarmCracker applies a restrictive seccomp profile to guest Firecracker VMs. The default action is `SCMP_ACT_ERRNO` — unknown syscalls return errors.

### Allowed Syscalls

Basic operations: `read`, `write`, `open`, `close`, `stat`, `poll`, `mmap`, etc.
Networking: `socket`, `bind`, `listen`, `accept`, `connect`, `sendmsg`, `recvmsg`
Process: `clone`, `fork`, `execve`, `exit`, `wait4`, `kill`
Filesystem: `mkdir`, `rmdir`, `creat`, `unlink`, `chmod`, `chown`

### Blocked Privileged Syscalls

The following syscalls are **intentionally blocked** for defense-in-depth:

| Syscall | Risk |
|---------|------|
| `mount`, `umount2` | Filesystem manipulation |
| `pivot_root`, `chroot` | Root directory change |
| `swapon`, `swapoff` | Memory swap control |
| `reboot`, `kexec_load` | System reboot/kexec |
| `init_module`, `delete_module` | Kernel module loading |
| `iopl`, `ioperm` | Direct hardware I/O |
| `acct` | Process accounting toggle |
| `settimeofday`, `clock_settime` | System time manipulation |
| `sethostname`, `setdomainname` | Host identity spoofing |

> **Note:** These syscalls apply to the **guest VM**, not the host. KVM hardware isolation is the primary security boundary. Seccomp provides defense-in-depth.

### Configuring Custom Seccomp

```yaml
security:
  seccomp:
    enabled: true
    profile: custom  # or "default"
    custom_profile_path: /etc/swarmcracker/seccomp.json
```

---

## Secure Deployment Guide

### 1. Generate Secure Tokens

```bash
# Never hardcode tokens. Use environment or vault:
export SWARM_JOIN_TOKEN=$(swarmctl token create)
```

### 2. Deploy with Ansible (Secure)

```bash
# Store tokens in Ansible Vault, not plaintext
ansible-vault encrypt_string --name 'swarm_token' 'SWMTKN-1-...'

# Run playbook with vault password
ansible-playbook -i inventory playbooks/setup-cluster.yml \
  --vault-password-file ~/.ansible-vault-pass
```

### 3. Verify File Permissions

```bash
# After deployment, verify:
ansible all -m shell -a "stat -c '%a %n' /etc/swarmcracker/config.yaml"
# Expected: 600 /etc/swarmcracker/config.yaml

ansible all -m shell -a "stat -c '%a %n' /var/lib/swarmkit/"
# Expected: 700 /var/lib/swarmkit/
```

### 4. Enable Consul TLS

```yaml
# /etc/swarmcracker/config.yaml
consul:
  address: "127.0.0.1:8500"
  use_tls: true
  tls_cert_file: /etc/swarmcracker/consul.crt
  tls_key_file: /etc/swarmcracker/consul.key
  tls_ca_file: /etc/swarmcracker/consul-ca.crt
```

### 5. Health Endpoint Hardening

```yaml
# /etc/systemd/system/swarmd-manager.service
# Default binds to 127.0.0.1:8080 (localhost only)
# Do NOT change to 0.0.0.0 unless behind a reverse proxy with auth
```

### 6. Build from Source (Hardened)

```bash
# Build with security flags
make all
# Equivalent to:
# go build -v -trimpath -buildmode=pie \
#   -ldflags "-s -w -X main.Version=..." \
#   -o build/swarmd-firecracker ./cmd/swarmd-firecracker/main.go
```

Flags explained:
- `-trimpath`: Removes filesystem paths from binary (privacy)
- `-buildmode=pie`: Position-independent executable (ASLR support)
- `-s -w`: Strip debug info and symbol table (smaller binary, less info leak)

---

## Init Injection Troubleshooting

### Problem: VMs fail to boot with "No init found"

**Symptoms:**
```
[    0.123456] Kernel panic - not syncing: No working init found.
```

**Root Cause:** The init system was not injected into the rootfs before ext4 image creation.

### How Init Injection Works

SwarmCracker uses `InjectIntoDir()` to inject an init system **BEFORE** the ext4 rootfs image is created:

```
1. OCI Image → Extract to temp directory
2. Detect init type (tini, dumb-init, systemd, etc.)
3. InjectIntoDir(tempDir, ociInfo):
   - Scratch images: inject busybox + tini
   - Tini/dumb-init: create /init → /sbin/init symlink
   - Systemd: REJECT (incompatible)
   - None: inject tini
4. Create ext4 image from temp directory
5. Boot Firecracker with rootfs
```

### Common Issues

| Issue | Solution |
|-------|----------|
| `Inject(rootfsPath)` no longer works | Use `InjectIntoDir(tmpDir, info)` before ext4 creation |
| systemd images rejected | Use tini-init or dumb-init based images |
| Permission denied writing to temp | Ensure `/tmp` or configured temp dir is writable |
| Busybox not found | Install `busybox-static` on the host for scratch images |

### Debugging Init Injection

```bash
# Check init injection in logs
journalctl -u swarmd-firecracker | grep "init"

# Expected output:
# "Detected init type" type=tini
# "init injection completed" path=/sbin/init

# Manually inspect rootfs
debugfs -R "ls -l /sbin/init" /var/lib/firecracker/rootfs/task-id.ext4
```

### Migration from Deprecated Inject()

The old `Inject(rootfsPath)` method has been deprecated and replaced with no-op stubs. It never actually mounted the ext4 image — it only created a temporary directory that was immediately deleted. All init injection now happens via `InjectIntoDir()` before ext4 creation in the image preparation pipeline.

---

## CVE / Advisory History

| ID | Severity | Description | Fixed In |
|----|----------|-------------|----------|
| CVR-1.2 | Critical | Hardcoded join token in deploy script | 2026-05-11 |
| CVR-1.3a | Critical | SSH command injection in deploy | 2026-05-11 |
| CVR-1.3b | Critical | Firecracker config injection via fmt.Sprintf | 2026-05-11 |
| CVR-1.4a | Critical | Tar ZIP Slip path traversal | 2026-05-11 |
| CVR-1.4b | Critical | Secret/config path traversal | 2026-05-11 |
| CVR-1.5a | Critical | Executor double-close panic | 2026-05-11 |
| CVR-1.5b | Critical | VM state data race | 2026-05-11 |
| CVR-1.7a | Critical | Snapshot process leak | 2026-05-11 |
| CVR-1.7b | Critical | VM left paused on snapshot failure | 2026-05-11 |
| CVR-1.9 | Critical | Network pkill command injection | 2026-05-11 |
| CVR-1.10 | Critical | Unchecked type assertion panic | 2026-05-11 |
| CVR-2.x | High | 22 hardening fixes | 2026-05-11 |
| CVR-3.x | Medium | Permissions, build, context, seccomp | 2026-05-11 |
