---
title: "Security Guide — SwarmCracker"
description: "Comprehensive security documentation for SwarmCracker operators and developers."
---

> Comprehensive security documentation for SwarmCracker operators and developers.

---

## Table of Contents

- [Security Model](#security-model)
- [Hardening Checklist](#hardening-checklist)
- [Seccomp Profile](#seccomp-profile)
- [Secure Deployment Guide](#secure-deployment-guide)
- [Init Injection Troubleshooting](#init-injection-troubleshooting)
- [Vulnerability Fix History](#vulnerability-fix-history)

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
| Syscall | Firecracker seccomp-bpf | Built-in filter on the VMM process (host-side) |
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
- [ ] **Consul discovery (if used)**: Enable only via the daemon flags `--consul-enabled` / `--consul-address`; prefer static `--vxlan-peers`
- [ ] **TLS for SwarmKit**: Encrypt manager-worker gRPC communication
- [ ] **Build hardening**: Use `-trimpath -buildmode=pie -ldflags "-s -w"`
- [ ] **Jailer enabled**: Run microVMs in chroot with non-root UID/GID
- [ ] **Firecracker seccomp**: Built-in seccomp-bpf filter is active on the VMM process (no config toggle)

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
| `/var/lib/firecracker/snapshots/` | `0700` | VM memory snapshots |
| `/var/lib/swarmcracker/volumes/` | `0700` | Volume metadata |
| `/var/lib/swarmcracker/rootfs/` | `0755` | Read-only rootfs images |
| `/var/run/firecracker/` | `0755` | API sockets (runtime only) |

---

## Seccomp Profile

Firecracker ships with its own built-in seccomp-bpf policy, applied to the
**Firecracker VMM process** on the host. SwarmCracker does not define, enable,
or customise that policy: there is no `security.seccomp` (or equivalent) key in
the configuration schema (`pkg/config/config.go`) and no daemon flag for it.

`pkg/jailer` declares `EnableSeccomp` / `SeccompPolicyPath` fields, but they are
**reserved for future use**. Jailer v1.15.x does not accept a `--seccomp` flag,
and Firecracker filters its own syscalls internally.

### Verifying the filter

```bash
# Find the Firecracker VMM process and read its seccomp mode
PID=$(pgrep -f 'firecracker.*--id')
grep Seccomp /proc/"$PID"/status
# Seccomp: 2   (filter mode active)
```

If Firecracker exits with `SIGSYS`, the built-in policy blocked a syscall.
Inspect the kernel log (`sudo dmesg | grep -i seccomp`) rather than trying to
disable filtering.

> **Note:** KVM hardware isolation is the primary security boundary; the guest
> microVM runs its own kernel. Firecracker's seccomp policy protects the VMM
> process, not the guest workload.

---

## Secure Deployment Guide

### 1. Generate Secure Tokens

```bash
# Never hardcode tokens. On the manager, print the generated token:
sudo swarmcracker cluster token worker

# Then export the printed value in the deploying shell (or a secrets manager):
export SWARM_JOIN_TOKEN=SWMTKN-1-...
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

### 4. Configure Consul Service Discovery (Optional)

Consul is only needed for dynamic VXLAN peer discovery, and it is configured
through daemon flags — there is no `consul:` block in
`/etc/swarmcracker/config.yaml`:

```bash
sudo swarmd-firecracker \
  --join-addr 192.168.1.10:4242 \
  --join-token "$SWARM_JOIN_TOKEN" \
  --vxlan-enabled \
  --consul-enabled \
  --consul-address 127.0.0.1:8500
```

For small clusters, static peers are simpler and avoid the Consul dependency:

```bash
--vxlan-enabled --vxlan-peers 192.168.1.11,192.168.1.12
```

The daemon exposes only `--consul-enabled` and `--consul-address`; there is no
SwarmCracker flag for Consul TLS. Configure TLS/ACLs on the Consul agent side.

### 5. Health Endpoint Hardening

```yaml
# /etc/systemd/system/swarmcracker-manager.service
# Default binds to 127.0.0.1:8080 (localhost only)
# Do NOT change to 0.0.0.0 unless behind a reverse proxy with auth
```

### 6. Build from Source (Hardened)

```bash
# Build with security flags
make all
# Equivalent for the daemon (Makefile target swarmd-firecracker):
# go build -v -trimpath -buildmode=pie \
#   -ldflags "-s -w -X main.Version=... -X main.BuildTime=... -X main.GitCommit=..." \
#   -o build/swarmd-firecracker ./cmd/swarmd-firecracker
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

## Vulnerability Fix History

`CVR-*` identifiers are internal code-review findings, not registered CVEs.
All entries below were fixed on 2026-05-11.

| ID | Severity | Description | Fixed On |
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
| CVR-3.x | Medium | Permissions, build, context | 2026-05-11 |
