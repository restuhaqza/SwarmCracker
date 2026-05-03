# Documentation Website Update Plan

> Comprehensive plan to bring the landing page and docs site in sync with SwarmCracker v0.6.0+ features

---

## Current State Analysis

### What's on the Website Now
- Basic landing page with core features
- Simple installation instructions
- Feature highlights (MicroVM isolation, SwarmKit, fast boot, etc.)
- Stats section
- Tabbed installation (Manager/Worker/Manual)

### What's Missing (New Features Since v0.6.0)

Based on git history and CHANGELOG.md, these features are **not yet documented** on the website:

1. **VXLAN Cross-Node Networking** (feat/network)
   - Multi-cluster support with Consul-based peer discovery
   - VXLAN overlay (ID 100, UDP 4789)
   - Cross-node VM communication
   - Consul integration for service discovery

2. **CNI Network Provider** (feat/cni)
   - SwarmKit IPAM integration
   - Alternative to VXLAN for networking
   - Plugin-based network providers

3. **Jailer Integration** (feat/jailer)
   - Production security with cgroup resource limits
   - CPU and memory limits
   - Parent cgroup configuration
   - Chroot isolation

4. **SwarmKit Control CLI (swarmctl)** (feat/swarmctl)
   - Node management (`ls-nodes`, `promote`, `demote`, `drain`)
   - Service management (`ls-services`, `create-service`, `rm-service`)
   - Task inspection (`ls-tasks`, `inspect`)
   - mTLS authentication to control socket

5. **Advanced CLI Commands** (feat/cli)
   - `doctor` — System health diagnostics
   - `leave`, `deinit`, `reset` — Cluster cleanup
   - `deploy` — Deploy to specific hosts
   - Enhanced join tokens with validation

6. **Image Management Improvements** (feat/image)
   - go-containerregistry (replaced containers/image)
   - HTTP server init wrapper for VMs
   - Custom init script support

7. **Ansible Deployment** (feat/ansible)
   - Automated cluster deployment
   - Consul role
   - VXLAN-enabled deployment
   - Production-ready configuration

8. **Testing Infrastructure** (test)
   - Unit tests, mock tests, integration tests
   - VXLAN test coverage
   - E2E testing with Vagrant/libvirt

9. **Snapshot Support** (feat/snapshot)
   - VM snapshot and restore (Firecracker v1.14.x)
   - Rollback capabilities

10. **Cgroup IO Limits** (feat/cli)
    - Disk I/O throttling per VM
    - Production resource isolation

---

## Update Plan

### Phase 1: Update Landing Page (High Priority)

#### 1.1 Update Hero Section
- **Current**: "Run containers as isolated microVMs"
- **Add**: Mention VXLAN multi-node clusters
- **Suggested**: "Orchestrate Firecracker microVMs across multi-node clusters with SwarmKit"

#### 1.2 Update Features Section
Add new feature cards:

```markdown
**NEW: Multi-Node Clustering**
- VXLAN overlay networking with Consul peer discovery
- Cross-node VM communication
- Tested: Worker1 ↔ Worker2, 0% packet loss, 4-8ms latency

**NEW: Production Security**
- Jailer integration with cgroup resource limits
- CPU, memory, and disk I/O throttling
- Chroot isolation for each VM

**NEW: Advanced Control**
- swarmctl CLI for node/service/task management
- Doctor command for health diagnostics
- Snapshot and rollback support
```

Update existing cards:
- "VXLAN Networking" → Add "with Consul service discovery"
- "Rolling Updates" → Add "with health monitoring"

#### 1.3 Update Stats Section
Add new stats:
```
4-8ms    Cross-node latency
0%       Packet loss (tested)
100%     KVM isolation
```

#### 1.4 Update Installation Section
Add VXLAN flags to install commands:
```bash
# Manager with VXLAN
curl -fsSL https://swarmcracker.restuhaqza.dev/install.sh | sudo bash -s -- init \
  --vxlan-enabled \
  --vxlan-peers 192.168.1.11,192.168.1.12

# Worker with VXLAN
curl -fsSL https://swarmcracker.restuhaqzqa.dev/install.sh | sudo bash -s -- join \
  --manager <MANAGER_IP>:4242 \
  --token SWMTKN-1-... \
  --vxlan-enabled
```

#### 1.5 Add "How It Works" — Networking
New section explaining VXLAN:
```
Manager schedules task
    → Worker assigns overlay IP (SwarmKit IPAM)
    → Firecracker boots VM
    → TAP → swarm-br0 → VXLAN (UDP 4789)
    → Consul discovers peer
    → Other workers update FDB
    → VMs talk across nodes
```

#### 1.6 Update Footer Links
Add links to new documentation:
- VXLAN Networking Guide
- Jailer Security Guide
- swarmctl CLI Reference
- Ansible Deployment

---

### Phase 2: Create New Documentation Pages

#### 2.1 VXLAN Networking Guide
**File**: `docs/site/guides/vxlan-networking.html` (or redirect to docs)

**Content**:
- What is VXLAN and why SwarmCracker uses it
- Architecture diagram (Manager → Workers → VMs via VXLAN)
- Configuration options (VXLAN ID, port, peer discovery)
- Consul integration
- Troubleshooting (FDB entries, UDP traffic, service discovery)
- Test results from `pkg/network/VXLAN_TEST_COVERAGE_SUMMARY.md`

#### 2.2 Jailer Security Guide
**File**: `docs/site/guides/jailer-security.html` (or redirect to docs)

**Content**:
- What is jailer and why it matters for production
- Cgroup v2 limits (CPU, memory, disk I/O)
- Chroot isolation
- Configuration examples
- Performance impact
- When to use jailer vs. vanilla Firecracker

#### 2.3 swarmctl CLI Reference
**File**: `docs/site/reference/swarmctl.html` (or redirect to docs)

**Content**:
- Node commands: `ls-nodes`, `promote`, `demote`, `drain`, `activate`
- Service commands: `ls-services`, `create-service`, `rm-service`, `scale`, `update`
- Task commands: `ls-tasks`, `inspect`
- Connection to control socket (`--socket`)
- Examples for common workflows

#### 2.4 Ansible Deployment Guide
**File**: `docs/site/guides/ansible-deployment.html` (or redirect to docs)

**Content**:
- Prerequisites (inventory, SSH access)
- Inventory structure (`inventory/libvirt/hosts`)
- Playbook walkthrough (`site.yml`)
- Consul role
- VXLAN configuration
- Verification steps

---

### Phase 3: Update Architecture Diagrams

#### 3.1 Update Main Architecture Diagram
**Current**: `docs/architecture/swarmcracker-architecture.svg`
**Add**:
- Consul service discovery block
- VXLAN overlay network (swarm-br0-vxlan)
- CNI network provider option
- Jailer cgroup hierarchy

#### 3.2 Create VXLAN Architecture Diagram
**File**: `docs/architecture/vxlan-networking.svg`
**Show**:
- 3 nodes (Manager + 2 Workers)
- VXLAN overlay (192.168.127.0/24)
- Underlay (physical network)
- Consul peer discovery flow
- FDB update mechanism

#### 3.3 Create Jailer Architecture Diagram
**File**: `docs/architecture/jailer-security.svg`
**Show**:
- Parent cgroup hierarchy
- Chroot directory structure
- Resource limits (CPU, memory, I/O)
- Control socket path

---

### Phase 4: Improve Navigation

#### 4.1 Add "Guides" Section to Nav
```html
<a href="#guides">Guides</a>
  ├── VXLAN Networking
  ├── Jailer Security
  ├── swarmctl CLI
  └── Ansible Deployment
```

#### 4.2 Add "Reference" Section
```html
<a href="#reference">Reference</a>
  ├── CLI Commands
  ├── Configuration
  └── Architecture
```

---

### Phase 5: Content Updates

#### 5.1 Update Getting Started
- Add VXLAN setup as optional step
- Mention Consul prerequisites
- Add troubleshooting section for multi-node

#### 5.2 Update Installation Guide
- Add `--vxlan-enabled` flag documentation
- Add Consul installation instructions
- Add Jailer setup (optional, for production)

#### 5.3 Update Configuration Reference
- Add VXLAN config options
- Add Consul config
- Add Jailer/cgroup config
- Add CNI provider config

---

### Phase 6: Examples and Tutorials

#### 6.1 Multi-Node Cluster Example
**File**: `docs/site/examples/multi-node.html`
**Content**:
- 3-node cluster setup (1 manager, 2 workers)
- VXLAN configuration
- Service deployment across nodes
- Verification steps

#### 6.2 Production Deployment Example
**File**: `docs/site/examples/production.html`
**Content**:
- Jailer enabled
- Resource limits configured
- Ansible automation
- Monitoring and health checks

#### 6.3 Snapshot and Rollback Tutorial
**File**: `docs/site/guides/snapshots.html`
**Content**:
- Create snapshot
- List snapshots
- Restore from snapshot
- Rollback scenario

---

### Phase 7: Interactive Elements

#### 7.1 Architecture Explorer
Add interactive diagram where users can:
- Hover over components to see descriptions
- Click to drill down into docs
- Toggle between networking modes (VXLAN vs CNI)

#### 7.2 Command Builder
Add interactive form that generates commands:
- Select: Manager or Worker
- Toggle: VXLAN enabled/disabled
- Toggle: Jailer enabled/disabled
- Output: Ready-to-run command

#### 7.3 Configuration Generator
Add form that generates `config.yaml`:
- Cluster size
- Networking mode
- Resource limits
- Download or copy to clipboard

---

## Implementation Priority

### Must Have (P0) — This Release
1. ✅ Update landing page features (add VXLAN, Jailer, swarmctl)
2. ✅ Update installation section (VXLAN flags)
3. ✅ Update architecture diagram (add Consul, VXLAN)
4. ✅ Add VXLAN networking guide
5. ✅ Add Jailer security guide
6. ✅ Update footer links

### Should Have (P1) — Next Release
7. swarmctl CLI reference
8. Ansible deployment guide
9. Multi-node example
10. Configuration reference updates

### Nice to Have (P2) — Future
11. Interactive architecture explorer
12. Command builder tool
13. Configuration generator
14. Production deployment example

---

## Technical Implementation Notes

### File Structure (Current)
```
docs/site/
├── index.html              # Landing page (single file)
├── README.md               # Dev documentation
├── CNAME                   # Custom domain
└── .github/workflows/      # GitHub Pages deployment
```

### File Structure (Proposed)
```
docs/site/
├── index.html              # Main landing page
├── guides/
│   ├── vxlan-networking.html
│   ├── jailer-security.html
│   ├── swarmctl-cli.html
│   └── ansible-deployment.html
├── examples/
│   ├── multi-node.html
│   └── production.html
├── reference/
│   ├── cli-commands.html
│   └── configuration.html
└── assets/
    ├── img/
    │   ├── architecture-vxlan.svg
    │   └── architecture-jailer.svg
    └── css/
        └── custom.css
```

### Build vs No-Build Decision

**Current**: No-build approach (Tailwind CDN, single HTML file)
**Pros**: Simple, fast, no dependencies
**Cons**: Hard to maintain as site grows

**Recommended**: Keep no-build for now, but:
1. Split into multiple HTML files (still no build)
2. Use shared CSS file
3. Add simple JavaScript for interactivity
4. Consider Vite + React when site becomes complex (10+ pages)

---

## Content Migration from Docs

Some content already exists in `docs/` — we should:
1. **Keep detailed docs in `docs/`** (GitHub repo)
2. **Link from website to GitHub docs** for deep dives
3. **Host overview/getting-started on website** (marketing + quick start)

Example flow:
- Website landing page → "Get Started"
- Website quick start (5 min)
- Link to GitHub docs for detailed guides
- Return to website for examples/tutorials

---

## Success Metrics

### Documentation Coverage
- [ ] All v0.6.0+ features mentioned on landing page
- [ ] VXLAN networking documented
- [ ] Jailer security documented
- [ ] swarmctl CLI documented
- [ ] Ansible deployment documented

### User Experience
- [ ] Time to first running VM < 5 minutes
- [ ] Multi-node setup time < 15 minutes
- [ ] All broken links fixed
- [ ] Mobile-responsive

### SEO and Discoverability
- [ ] Meta tags updated
- [ ] OpenGraph tags for social sharing
- [ ] Sitemap.xml generated
- [ ] robots.txt configured

---

## Next Steps

1. **Review this plan** with team
2. **Prioritize P0 items** for immediate work
3. **Create design mockups** for new pages
4. **Set up dev environment** (if switching to build system)
5. **Start implementation** with landing page updates
6. **Test deployment** to GitHub Pages
7. **Gather feedback** from users
8. **Iterate** based on metrics

---

**Last Updated**: 2026-05-03
**Version**: 1.0
**Status**: Draft — Ready for Review
