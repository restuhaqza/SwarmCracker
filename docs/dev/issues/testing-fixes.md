# SwarmCracker Testing Fixes

## Overview

During end-to-end testing of SwarmCracker with Firecracker microVMs on QEMU/KVM Vagrant cluster, several issues were discovered that prevent the system from working correctly. This document outlines the fixes needed.

---

## Issue 1: Kernel Download Returns HTML

**Severity:** 🔴 Critical  
**Location:** Setup scripts, documentation  
**Discovered:** 2026-04-20

### Problem
The URL `https://github.com/firecracker-microvm/firecracker-demo/raw/main/x86_64/vmlinux.bin` returns an HTML page (GitHub's redirect page) instead of the actual binary kernel. This causes Firecracker to fail with:

```
Invalid Elf magic number
```

The downloaded "kernel" is 302KB of HTML text.

### Root Cause
- GitHub LFS files don't serve correctly via raw URLs
- GitHub redirects to an HTML page explaining the file is stored elsewhere

### Fix Options
1. **Extract from host kernel** — Decompress `/boot/vmlinuz-*` (XZ compression at offset ~21196)
2. **Bundle in repo** — Include pre-built kernel in `test-automation/resources/`
3. **Use direct CDN URL** — Host on S3 or release assets with direct-download link
4. **Build at runtime** — Use kernel build script in setup

### Recommended Fix
Option 2: Bundle a working kernel in the repo and update setup scripts to use it.

### Files to Change
- `test-automation/scripts/setup-node.sh` — Fix download URL
- `docs/user/getting-started/README.md` — Update instructions
- `test-automation/resources/vmlinux` — Add bundled kernel (if not in repo)

---

## Issue 2: Manager Advertise Address Not Set

**Severity:** 🔴 Critical  
**Location:** `cmd/swarmd-firecracker/`, Ansible playbooks, setup scripts  
**Discovered:** 2026-04-20

### Problem
The manager generates join tokens that contain `0.0.0.0:4242` as the connection address. Workers then try to connect to `0.0.0.0:4242` which is not routable, resulting in:

```
dial tcp 0.0.0.0:4242: connect: connection refused
```

### Root Cause
- `--listen-remote-api 0.0.0.0:4242` is set correctly (bind to all interfaces)
- `--advertise-remote-api` is NOT set, so SwarmKit uses the bind address as the advertise address
- Workers receive `0.0.0.0:4242` from the join token and fail to connect

### Fix
Add `--advertise-remote-api` with the actual IP address of the manager node.

### Implementation
1. **Detect IP automatically** in startup scripts:
   ```bash
   MANAGER_IP=$(ip addr show eth1 | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)
   --advertise-remote-api $MANAGER_IP:4242
   ```

2. **Update Ansible playbook** to set advertise address based on inventory

3. **Add flag documentation** to getting-started guide

### Files to Change
- `test-automation/scripts/setup-manager.sh` — Add advertise address
- `infrastructure/ansible/roles/swarmcracker/tasks/manager.yml` — Add advertise address
- `cmd/swarmd-firecracker/main.go` — Consider adding auto-detect fallback
- `docs/user/getting-started/README.md` — Document the flag

---

## Issue 3: Image Extraction Context Timeout

**Severity:** 🟡 Medium  
**Location:** `pkg/image/preparer.go`  
**Discovered:** 2026-04-20

### Problem
Docker-based OCI image extraction (`extractWithDockerCLI`) fails with `signal: killed` because the context from the task manager cancels too quickly.

### Root Cause
- Task manager passes a short timeout context
- `docker create` + `docker export` can take several seconds for large images
- Process receives SIGKILL before completion

### Fix Options
1. **Increase timeout** — Use longer context deadline in task manager
2. **Add retry** — Retry extraction with exponential backoff
3. **Use streaming extraction** — Avoid creating intermediate containers

### Recommended Fix
Option 1: Increase the image preparation timeout to at least 60 seconds.

### Files to Change
- `pkg/agent/taskmanager/` — Increase timeout for image prep
- `pkg/image/preparer.go` — Add context timeout documentation

---

## Issue 4: Vagrantfile Uses QEMU Not KVM

**Severity:** 🔴 Critical  
**Location:** `test-automation/Vagrantfile`  
**Discovered:** 2026-04-20

### Problem
The Vagrantfile configures VMs with QEMU emulation instead of KVM, causing:

```
Kvm error: Missing KVM capabilities: 0x38
```

### Root Cause
```ruby
lv.cpu_mode = "custom"
lv.cpu_model = "qemu64"
lv.nested = false
lv.driver = "qemu"
```

This was intentional for running tests on machines without KVM, but it breaks Firecracker which requires KVM.

### Fix
Change to KVM with nested virtualization:
```ruby
lv.cpu_mode = "host-passthrough"
lv.nested = true
lv.driver = "kvm"
```

**Prerequisite:** Host must have `kvm_intel` module loaded with nested=Y.

### Files to Change
- `test-automation/Vagrantfile` — Update provider config
- `test-automation/README.md` — Add KVM prerequisite docs

---

## Issue 5: Setup Script Missing Dependencies

**Severity:** 🟡 Medium  
**Location:** `test-automation/scripts/`  
**Discovered:** 2026-04-20

### Problem
The test setup doesn't install required dependencies:
- Docker (OCI image extraction fallback)
- Firecracker binary
- Network bridge setup

### Root Cause
Original setup assumed Ansible would handle everything, but manual testing needed a quick setup script.

### Fix
Update `scripts/setup-node.sh` to include all dependencies.

### Files to Change
- `test-automation/scripts/setup-node.sh` — Complete dependency list

---

## Issue 6: Conflicting systemd Services

**Severity:** 🟡 Medium  
**Location:** Ansible playbooks  
**Discovered:** 2026-04-20

### Problem
Ansible creates `swarmd-manager.service` and `swarmd-worker.service` that start automatically and conflict with manual `swarmd-firecracker` testing.

### Fix Options
1. **Use systemd exclusively** — Fix configs and only use systemd
2. **Add disable flag** — Allow disabling systemd for manual testing
3. **Better integration** — Make systemd configs match manual startup parameters

### Recommended Fix
Option 1: Fix systemd configs to include all required parameters (advertise address, kernel path, bridge name).

### Files to Change
- `infrastructure/ansible/roles/swarmcracker/templates/swarmd-manager.service.j2`
- `infrastructure/ansible/roles/swarmcracker/templates/swarmd-worker.service.j2`

---

## Implementation Priority

| Priority | Issue | Dependencies |
|----------|-------|--------------|
| P0 | Issue 4 (Vagrantfile KVM) | None |
| P0 | Issue 1 (Kernel) | None |
| P0 | Issue 2 (Advertise Address) | None |
| P1 | Issue 5 (Setup Script) | Issue 1, Issue 2 |
| P1 | Issue 6 (Systemd) | Issue 2 |
| P2 | Issue 3 (Timeout) | None |

---

## Testing Checklist

After implementing fixes, verify:

1. [ ] `vagrant up` creates VMs with KVM (`Domain type: kvm` in output)
2. [ ] `/usr/share/firecracker/vmlinux` is valid ELF (`file` shows ELF 64-bit)
3. [ ] Manager starts with advertise address in logs
4. [ ] Worker joins cluster (Node is ready)
5. [ ] Service tasks reach RUNNING state
6. [ ] `ps aux | grep firecracker` shows running VMs
7. [ ] `ip link show swarm-br0` shows active tap interfaces