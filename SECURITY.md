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

### Public Disclosure

We follow responsible disclosure:

- If a fix is available, the advisory is published alongside the patch release.
- If no fix exists yet, we work with you on a timeline (typically 90 days).
- Credit is given to the reporter unless anonymity is requested.

---

## Security Model

Understanding SwarmCracker's threat model helps you evaluate risk:

### Isolation Boundaries

SwarmCracker runs each workload as a Firecracker microVM with:

- **Hardware virtualization** via KVM — separate kernel per VM
- **Jailer** sandboxing — restricted filesystem and process namespace
- **Network isolation** — per-VM TAP devices, bridge segmentation, optional VXLAN encryption

### Trust Boundaries

| Component | Trust Level | Notes |
|-----------|------------|-------|
| SwarmKit Manager | High | Controls scheduling and secrets |
| swarmd-firecracker agent | High | Runs on worker nodes with KVM access |
| Firecracker VMM | High | Minimal attack surface, Rust-based |
| Guest VMs | Low | Isolated from host and other VMs |

### What's Not Covered

- **Host OS hardening** — SwarmCracker assumes a properly secured Linux host. See [Security Guide](docs/guides/security.md).
- **Network-level attacks** — VXLAN overlay is not encrypted by default. Use WireGuard or similar if needed.
- **Image supply chain** — SwarmCracker trusts container registries. Verify image signatures separately.

---

## Best Practices for Deployers

1. **Keep SwarmCracker updated** — security fixes ship in patch releases.
2. **Restrict KVM access** — only the swarmcracker user needs `/dev/kvm`.
3. **Use TLS for SwarmKit** — encrypt manager-worker communication.
4. **Review Firecracker kernel config** — minimize attack surface in guest kernels.
5. **Rotate join tokens** — especially after node removal.
6. **Enable audit logging** — for compliance and incident response.

---

## Security-Related Configuration

See [docs/guides/security.md](docs/guides/security.md) for detailed hardening instructions including:
- Jailer configuration
- Resource limits
- Seccomp profiles
- Network policies
