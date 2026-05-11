# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release (main branch) | ✅ |
| Previous minor release | ✅ (security fixes only) |
| Older releases | ❌ |

Security fixes are backported to the latest minor release only. Upgrade to the latest version for full support.

---

## Reporting a Vulnerability

If you discover a security vulnerability in SwarmCracker, please report it responsibly.

### What to Report

- Remote code execution or privilege escalation
- Authentication or authorization bypass
- Denial of service (with reproduction steps)
- Insecure defaults in configuration
- Sensitive data exposure (logs, API responses, etc.)
- Dependency vulnerabilities affecting SwarmCracker

### How to Report

**Preferred:** Open a [GitHub Security Advisory](https://github.com/restuhaqza/SwarmCracker/security/advisories/new) (private, only you and maintainers can see it).

**Alternative:** Email **restuhaqza@gmail.com** with:
- Description of the vulnerability
- Steps to reproduce
- Affected versions
- Potential impact
- Any proposed fix (optional)

### What to Expect

1. **Acknowledgment** within 48 hours
2. **Initial assessment** within 5 business days
3. **Status updates** every 7 days until resolved
4. **Coordinated disclosure** — we will not publicly disclose until a fix is available

---

## Security Model

SwarmCracker runs each workload as a Firecracker microVM with hardware-enforced isolation. See [Security Guide](docs/dev/security.md) for full details.

### Key Security Properties

| Property | Implementation |
|----------|---------------|
| Hardware isolation | KVM — separate kernel per VM |
| Process sandbox | Jailer chroot, non-root UID/GID |
| Syscall filtering | Seccomp — privileged syscalls blocked |
| Network isolation | Per-VM TAP + bridge + VXLAN overlay |
| Storage isolation | Per-VM ext4 rootfs |
| Secret protection | Config files at 0600, tokens at DEBUG log level |
| Build hardening | PIE binary, stripped symbols, trimmed paths |

### Threat Model

- **Guest VM compromise** → Attacker contained to VM (KVM isolation)
- **Agent compromise** → Attacker gains KVM access; can affect all VMs on node
- **Manager compromise** → Attacker controls cluster; rotate all tokens immediately
- **Network MITM** → VXLAN overlay not encrypted by default; use WireGuard or Consul TLS

---

## Production Hardening

### Quick Checklist

```bash
# 1. Verify file permissions
stat -c '%a %n' /etc/swarmcracker/config.yaml  # must be 600
stat -c '%a %n' /var/lib/swarmkit/               # must be 700

# 2. Verify health endpoint binding
ss -tlnp | grep 8080  # should show 127.0.0.1:8080

# 3. Verify build flags
swarmd-firecracker --version  # built with -trimpath -buildmode=pie

# 4. Verify no hardcoded tokens in scripts
grep -r "SWMTKN" scripts/  # should return nothing

# 5. Verify seccomp is enabled
grep -r "seccomp" /etc/swarmcracker/  # should show enabled: true
```

### Build from Source (Hardened)

```bash
# The Makefile applies security flags automatically:
make all
# Equivalent to:
# CGO_ENABLED=0 go build -v -trimpath -buildmode=pie \
#   -ldflags "-s -w -X main.Version=..." \
#   -o build/swarmd-firecracker ./cmd/swarmd-firecracker/main.go
```

### Secure Token Management

- Never hardcode join tokens in scripts or config files
- Store tokens in Ansible Vault or a secrets manager
- Rotate tokens after any node leaves the cluster
- Use `swarmctl token create` to generate fresh tokens

### Consul TLS

Enable TLS for Consul communication to prevent eavesdropping on VXLAN peer discovery:

```yaml
consul:
  use_tls: true
  tls_cert_file: /etc/swarmcracker/consul.crt
  tls_key_file: /etc/swarmcracker/consul.key
  tls_ca_file: /etc/swarmcracker/consul-ca.crt
```

---

## Seccomp Profile

SwarmCracker applies a restrictive seccomp profile to Firecracker guest VMs. Default action is `SCMP_ACT_ERRNO` (deny with error).

### Intentionally Blocked Syscalls

| Syscall | Risk |
|---------|------|
| `mount`, `umount2` | Filesystem manipulation |
| `pivot_root`, `chroot` | Root directory change |
| `swapon`, `swapoff`, `reboot` | System control |
| `init_module`, `delete_module` | Kernel module loading |
| `iopl`, `ioperm` | Direct hardware I/O |
| `settimeofday`, `clock_settime` | Time manipulation |
| `sethostname`, `setdomainname` | Host spoofing |

> The seccomp filter applies to the **guest VM** only. KVM provides the primary hardware isolation boundary. Seccomp is defense-in-depth.

---

## Vulnerability Fix History

### 2026-05-11 — Phase 6 Code Review

**11 Critical fixes:**
- CVR-1.2: Hardcoded join token removed
- CVR-1.3a: SSH command injection fixed (shellescape)
- CVR-1.3b: Firecracker config injection fixed (json.Marshal)
- CVR-1.4a: Tar ZIP Slip path traversal fixed
- CVR-1.4b: Secret/config path traversal fixed
- CVR-1.5a: Executor double-close panic fixed (sync.Once)
- CVR-1.5b: VM state data race fixed (RWMutex)
- CVR-1.7a: Snapshot process leak fixed (process tracking)
- CVR-1.7b: VM paused on snapshot failure fixed (defer resume)
- CVR-1.9: Network pkill injection fixed (PID-file signals)
- CVR-1.10: Type assertion panic fixed (ok check)

**22 High fixes** covering: resource cleanup rollback, state transition guards, goroutine leak prevention, CNI IPAM cleanup, YAML Duration parsing, NATEnabled pointer, token logging downgrade, file permissions, Consul TLS, io.Copy streaming, capability checking, task ID/bridge name/mount path validation.

Full details: [Security Guide](docs/dev/security.md)

---

## CI Security Checks

Our CI pipeline runs on every commit:

| Check | Tool |
|-------|------|
| Secret scanning | Pre-commit hook (custom) |
| Security linting | `gosec` via golangci-lint |
| Race detection | `go test -race` |
| Nil error handling | `nilerr` linter |
| Context usage | `noctx` linter |
| Slice preallocation | `prealloc` linter |

---

## Best Practices for Deployers

1. **Keep SwarmCracker updated** — security fixes ship in patch releases
2. **Restrict KVM access** — only the swarmcracker user needs `/dev/kvm`
3. **Use TLS everywhere** — SwarmKit gRPC, Consul API, and health endpoint (via reverse proxy)
4. **Review Firecracker kernel config** — minimize attack surface in guest kernels
5. **Rotate join tokens** — especially after node removal or suspected compromise
6. **Enable audit logging** — for compliance and incident response
7. **Run regular security audits** — use the hardening checklist above
