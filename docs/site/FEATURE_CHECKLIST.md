# Feature Documentation Checklist

## Quick Visual: What's Missing from the Website

### ✅ Already Documented on Website
- [x] MicroVM isolation (Firecracker + KVM)
- [x] SwarmKit orchestration (services, scaling)
- [x] Fast boot (< 100ms)
- [x] Rolling updates
- [x] Basic installation (one-line install)

### ❌ Missing from Website (Added in v0.6.0+)

| Feature | Git Commit | Website Status | Priority |
|---------|-----------|----------------|----------|
| **VXLAN Networking** | b3ad1c9 | ❌ Not mentioned | 🔴 P0 |
| **Consul Peer Discovery** | 1a54c67 | ❌ Not mentioned | 🔴 P0 |
| **CNI Network Provider** | 1ed6a20 | ❌ Not mentioned | 🟡 P1 |
| **Jailer Cgroup Limits** | 9f92dd0 | ❌ Not mentioned | 🔴 P0 |
| **Parent Cgroup Config** | ed45893 | ❌ Not mentioned | 🟡 P1 |
| **swarmctl CLI** | f3dfb12 | ❌ Not mentioned | 🔴 P0 |
| **Node Management** | 02a65a4 | ❌ Not mentioned | 🔴 P0 |
| **Service Management** | 02a65a4 | ❌ Not mentioned | 🔴 P0 |
| **Doctor Command** | ad1c908 | ❌ Not mentioned | 🟡 P1 |
| **Leave/Deinit/Reset** | f684982 | ❌ Not mentioned | 🟡 P1 |
| **Deploy Command** | 75993e8 | ❌ Not mentioned | 🟡 P1 |
| **Ansible Automation** | 1a54c67 | ❌ Not mentioned | 🟡 P1 |
| **go-containerregistry** | 80f5524 | ❌ Not mentioned | 🟢 P2 |
| **HTTP Init Wrapper** | ae9fb56 | ❌ Not mentioned | 🟢 P2 |
| **Snapshot Support** | 7fefc37 | ❌ Not mentioned | 🟡 P1 |
| **Cgroup IO Limits** | 75993e8 | ❌ Not mentioned | 🟡 P1 |

**Legend**: 🔴 P0 = Must Have | 🟡 P1 = Should Have | 🟢 P2 = Nice to Have

---

## Page-by-Page Update Checklist

### 📄 Landing Page (index.html)

#### Hero Section
- [ ] Update tagline to mention multi-node
- [ ] Add VXLAN networking mention
- [ ] Update description

#### Features Section (6 cards)
- [ ] **Keep**: MicroVM isolation, SwarmKit, Fast boot, Hardware security
- [ ] **Update**: VXLAN → Add "with Consul discovery"
- [ ] **Update**: Rolling updates → Add "with health monitoring"
- [ ] **Add**: Multi-node clustering (new card)
- [ ] **Add**: Production security (Jailer)
- [ ] **Add**: Advanced control (swarmctl)

#### Stats Section
- [ ] Keep: <100ms boot, <5MB overhead, 100% KVM, Linux
- [ ] Add: 4-8ms latency, 0% packet loss

#### How It Works Section
- [ ] Step 1: Add VXLAN flag to init command
- [ ] Step 2: Add VXLAN flag to join command
- [ ] Add Step 5: "Configure VXLAN overlay" (optional)

#### Installation Section (Tabs)
- [ ] **Manager tab**: Add `--vxlan-enabled` and `--vxlan-peers`
- [ ] **Worker tab**: Add `--vxlan-enabled`
- [ ] Add note: "For multi-node clusters"

#### Footer
- [ ] Add "VXLAN Networking" link
- [ ] Add "Jailer Security" link
- [ ] Add "swarmctl CLI Reference" link
- [ ] Add "Ansible Deployment" link

---

### 📄 New Pages to Create

#### VXLAN Networking Guide
- [ ] Create `guides/vxlan-networking.html`
- [ ] What is VXLAN?
- [ ] Architecture diagram
- [ ] Configuration (flags, Consul)
- [ ] Troubleshooting (FDB, UDP, service discovery)
- [ ] Test results (4-8ms, 0% loss)

#### Jailer Security Guide
- [ ] Create `guides/jailer-security.html`
- [ ] What is jailer?
- [ ] Cgroup limits (CPU, memory, I/O)
- [ ] Configuration examples
- [ ] When to use jailer
- [ ] Performance impact

#### swarmctl CLI Reference
- [ ] Create `reference/swarmctl.html`
- [ ] Node commands (ls-nodes, promote, drain)
- [ ] Service commands (ls-services, create, scale)
- [ ] Task commands (ls-tasks, inspect)
- [ ] Connection options (--socket)

#### Ansible Deployment Guide
- [ ] Create `guides/ansible-deployment.html`
- [ ] Prerequisites
- [ ] Inventory setup
- [ ] Playbook walkthrough
- [ ] Verification steps

#### Multi-Node Example
- [ ] Create `examples/multi-node.html`
- [ ] 3-node cluster setup
- [ ] VXLAN configuration
- [ ] Deploy services across nodes
- [ ] Verify cross-node networking

---

### 📄 Architecture Diagrams

#### Main Architecture (update existing)
- [ ] Add Consul block
- [ ] Add VXLAN overlay network
- [ ] Add CNI provider option
- [ ] Add Jailer cgroup hierarchy

#### New: VXLAN Architecture
- [ ] Create `assets/img/architecture-vxlan.svg`
- [ ] Show 3 nodes (Manager + 2 Workers)
- [ ] Overlay network (192.168.127.0/24)
- [ ] Underlay (physical network)
- [ ] Consul peer discovery flow

#### New: Jailer Architecture
- [ ] Create `assets/img/architecture-jailer.svg`
- [ ] Show cgroup hierarchy
- [ ] Chroot directory structure
- [ ] Resource limits visualization

---

## 🎯 Priority Order

### Week 1: Landing Page Foundation
1. [ ] Update features section (add 3 new cards)
2. [ ] Update installation tabs (VXLAN flags)
3. [ ] Update stats (latency, packet loss)
4. [ ] Update footer links

### Week 2: Core Documentation
5. [ ] Create VXLAN networking guide
6. [ ] Create Jailer security guide
7. [ ] Create swarmctl CLI reference

### Week 3: Advanced Guides
8. [ ] Create Ansible deployment guide
9. [ ] Create multi-node example
10. [ ] Update main architecture diagram

### Week 4: Polish & Diagrams
11. [ ] Create VXLAN architecture diagram
12. [ ] Create Jailer architecture diagram
13. [ ] Test all pages on mobile
14. [ ] Deploy and gather feedback

---

## 📊 Content Sources

### Existing Documentation to Reference

**VXLAN**:
- `pkg/network/VXLAN_TEST_COVERAGE_SUMMARY.md`
- `docs/planning/consul-integration.md`
- `docs/user/guides/networking.md` (already has VXLAN info!)

**Jailer**:
- `pkg/security/TESTING_SUMMARY.md`
- `pkg/security/TEST_COMPLETION_REPORT.md`
- Firecracker jailer documentation

**swarmctl**:
- `docs/user/reference/cli.md`
- `docs/user/guides/swarmkit.md`

**Ansible**:
- `infrastructure/ansible/README.md`
- `infrastructure/ansible/inventory/libvirt/hosts`

**Testing**:
- `docs/dev/testing/e2e-tests.md` (already updated!)
- `test/e2e/README.md`

---

## ✅ Launch Checklist

Before going live with updates:

- [ ] All P0 features documented
- [ ] All code examples tested
- [ ] All links working (no 404s)
- [ ] Mobile-responsive (test on phone)
- [ ] Code blocks have syntax highlighting
- [ ] Copy-to-clipboard buttons work
- [ ] diagrams render correctly
- [ ] SEO meta tags updated
- [ ] OpenGraph tags for social sharing
- [ ] Deploy to GitHub Pages
- [ ] Test on staging URL first

---

**Last Updated**: 2026-05-03
**Status**: ✅ Ready to implement
