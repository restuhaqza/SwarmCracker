# ADR-005: Blessed Deployment Path

- **Date:** 2026-06-02
- **Status:** ✅ Accepted

## Context

SwarmCracker currently has 5 competing deployment mechanisms, none of which are complete end-to-end:

| Path | Coverage | Gaps |
|------|----------|------|
| `install.sh` | Binary + Firecracker + kernel + rootfs | No Consul, no VXLAN, no systemd, no CNI |
| Ansible | Full cluster (Consul, CNI, VXLAN, roles) | Downloads stale GitHub binary, no snapshot dir |
| Docker Compose | 3-node cluster | Can't work on single KVM host, hardcoded token |
| Vagrant | 3-node libvirt | Unmaintained, diverged from Ansible |
| Manual (README) | `swarmcracker cluster init` | Assumes all deps pre-installed |

This fragmentation is the root cause of ~40% of the pain points identified in the architecture review. New users don't know which path to follow, and no single path works end-to-end without manual steps.

## Decision

SwarmCracker will have exactly **one blessed production path** and **one blessed development path**:

### Production Path
```
install.sh → swarmcracker setup → swarmcracker cluster init/join → systemd
```

1. `curl ... | bash` — downloads latest release binary + checksum verification
2. `swarmcracker setup` — installs Firecracker, kernel, rootfs, creates bridge, generates config
3. `swarmcracker cluster init` (manager) or `swarmcracker cluster join` (worker)
4. Systemd units auto-generated on completion

### Development Path
```
make all → make test-e2e
```

1. `git clone` + `make all` — builds from source
2. `make test-e2e` — runs full cluster E2E via test-automation

### Everything Else → contrib/

- Docker Compose → `contrib/docker-compose/`
- Vagrant → evaluated for deletion or `contrib/vagrant/`
- Ansible → remains as the **advanced/production** option, documented separately in `docs/user/guides/ansible.md`
- Manual `swarmd-firecracker` → documented as advanced/development only

## Consequences

### Positive
- New user has exactly one way to get started — no decision paralysis
- `install.sh` + `swarmcracker setup` converge on the same end state as Ansible
- Release CI smoke test (SETUP-02) ensures the blessed path binary always works
- Config auto-generation (SETUP-03) ensures workers have config after join
- `contrib/` directory clearly signals "not the main path"

### Negative
- Docker Compose demo is lost (but it was misleading — couldn't actually work)
- Ansible becomes "advanced" rather than "default" — but this reflects reality
- `install.sh` needs heavy work to absorb Ansible logic (tracked as SETUP-04)

## Migration Plan

1. ADR accepted (this document)
2. Move docker-compose → contrib/ (POLISH-01)
3. Add `swarmcracker setup` subcommand (SETUP-04)
4. Rewrite README quick start to match blessed path
5. Write `docs/user/guides/advanced-deployment.md` for Ansible, manual
6. Update mkdocs nav
