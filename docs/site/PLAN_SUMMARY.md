# Documentation Update Plan — Quick Summary

## 🎯 Goal

Bring the landing page and documentation website in sync with SwarmCracker v0.6.0+ features, especially VXLAN networking, Jailer security, and advanced CLI tools.

---

## 📊 Current vs. Future

### What's on the Site Now
- ✅ Basic landing page
- ✅ Simple installation
- ✅ Core features (MicroVMs, SwarmKit, fast boot)
- ❌ **Missing**: VXLAN, Jailer, swarmctl, Ansible, snapshots

### What Needs to Be Added

#### High Priority (Must Have)
1. **VXLAN Networking** — Multi-node clusters, Consul discovery
2. **Jailer Security** — Cgroup limits, production isolation
3. **swarmctl CLI** — Node/service/task management
4. **Updated Installation** — VXLAN flags, Consul setup
5. **Updated Architecture Diagram** — Add Consul, VXLAN, CNI

#### Medium Priority (Should Have)
6. Ansible deployment guide
7. Multi-node cluster example
8. Configuration reference updates
9. Snapshot/restore guide

#### Low Priority (Nice to Have)
10. Interactive architecture explorer
11. Command builder tool
12. Configuration generator

---

## 🚀 Implementation Plan

### Phase 1: Landing Page Updates (Week 1)

**Hero Section**
- Tagline: "Multi-Node Firecracker MicroVMs with SwarmKit"

**Features Section**
Add cards for:
- 🔥 Multi-Node Clustering (VXLAN + Consul)
- 🛡️ Production Security (Jailer + cgroups)
- 🎛️ Advanced Control (swarmctl CLI)

**Installation Section**
Add VXLAN flags:
```bash
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- init \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12
```

**Stats Section**
Add:
- 4-8ms cross-node latency
- 0% packet loss (tested)

### Phase 2: New Documentation Pages (Week 2)

**Guides**
1. VXLAN Networking Guide
   - Architecture diagrams
   - Configuration examples
   - Troubleshooting FDB/Consul

2. Jailer Security Guide
   - Cgroup limits (CPU, memory, I/O)
   - Chroot isolation
   - When to use jailer

3. swarmctl CLI Reference
   - Node commands (promote, drain, etc.)
   - Service commands (create, scale, update)
   - Task inspection

4. Ansible Deployment Guide
   - Inventory setup
   - Playbook walkthrough
   - Verification steps

### Phase 3: Architecture Diagrams (Week 3)

**Update Main Diagram**
- Add Consul block
- Add VXLAN overlay
- Add CNI provider option
- Add Jailer cgroups

**New VXLAN Diagram**
- 3-node cluster
- Overlay/underlay networks
- Consul peer discovery flow

**New Jailer Diagram**
- Cgroup hierarchy
- Chroot structure
- Resource limits

### Phase 4: Interactive Elements (Week 4)

**Optional Enhancements**
- Architecture explorer (hover for details)
- Command builder (generate install commands)
- Config generator (YAML output)

---

## 📁 File Structure

### Current
```
docs/site/
└── index.html  # Single landing page
```

### Proposed
```
docs/site/
├── index.html                  # Landing page (updated)
├── guides/
│   ├── vxlan-networking.html
│   ├── jailer-security.html
│   ├── swarmctl-cli.html
│   └── ansible-deployment.html
├── examples/
│   ├── multi-node.html
│   └── production.html
└── assets/
    └── img/
        ├── architecture-vxlan.svg
        └── architecture-jailer.svg
```

**Still No-Build**: Keep using Tailwind CDN, single HTML files (no React/Vite needed yet).

---

## ✅ Success Metrics

- [ ] All v0.6.0+ features on landing page
- [ ] VXLAN guide live
- [ ] Jailer guide live
- [ ] swarmctl reference live
- [ ] Installation commands include VXLAN
- [ ] Architecture diagrams updated
- [ ] Time to first VM < 5 minutes
- [ ] Multi-node setup < 15 minutes

---

## 🎨 Design Notes

### Keep Consistent
- Dark theme with orange accents
- Syntax-highlighted code blocks
- Copy-to-clipboard buttons
- Responsive design

### Add New
- VXLAN architecture diagram
- Jailer security diagram
- Tabbed installation (Manager/Worker with VXLAN)
- Interactive command builder (optional)

---

## 📝 Content Strategy

### Website vs. GitHub Docs

**Website (swarmcracker.restuhaqza.dev)**
- Landing page + marketing
- Quick start (5 minutes)
- Overview guides
- Examples

**GitHub Docs (github.com/restuhaqza/SwarmCracker/tree/main/docs)**
- Detailed reference
- Implementation details
- Development guides
- API documentation

**Flow**: Website → Quick Start → Link to GitHub docs → Deep dives → Return to examples

---

## 🗓️ Timeline

| Week | Tasks |
|------|-------|
| 1 | Landing page updates (features, installation, stats) |
| 2 | New guides (VXLAN, Jailer, swarmctl, Ansible) |
| 3 | Architecture diagrams (VXLAN, Jailer) |
| 4 | Interactive elements (optional) |

---

## 🚦 Next Steps

1. **Review this plan** — Any priorities to change?
2. **Create design mockups** — Visuals for new pages
3. **Start with P0** — Landing page updates first
4. **Test locally** — Ensure mobile-responsive
5. **Deploy to GitHub Pages** — Get feedback
6. **Iterate** — Based on metrics and user feedback

---

**Status**: ✅ Plan ready — waiting for your approval to start implementation
