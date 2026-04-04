# Documentation Restructure Plan

**Date:** 2026-04-04
**Status:** Planning Phase
**Goal:** Organize 80+ markdown files into a clear, navigable structure

---

## Current State Analysis

### Problems Identified

1. **Root-level clutter** - 10+ markdown files at repo root
2. **Duplicate content** - Multiple deployment guides, test reports scattered
3. **Inconsistent naming** - Mix of UPPER, lower, Title-Case filenames
4. **Archive bloat** - Old testing docs in `/docs/archive/testing/` not clearly marked
5. **Test-automation sprawl** - 11 MD files in test-automation directory
6. **Report fragmentation** - Coverage reports split across multiple locations
7. **No clear hierarchy** - Users can't find "Getting Started" easily
8. **Orphaned files** - COMMIT_PLAN.md, PROJECT.md with unclear purpose

### File Count by Location

| Location | Count | Issues |
|----------|-------|--------|
| Root (`/`) | 10 | Too many, cluttered |
| `/docs/` | 38 | Good structure, but some duplicates |
| `/docs/guides/swarmkit/` | 7 | Multiple deployment docs |
| `/docs/reports/` | 14 | Well organized, but needs archive cleanup |
| `/test-automation/` | 11 | Status reports mixed with guides |
| `/test/` | 3 | Minimal, okay |

---

## Proposed New Structure

```
swarmcracker/
├── README.md                           # Keep (project landing page)
├── CONTRIBUTING.md                     # Keep (dev guidelines)
├── LICENSE                             # Keep
│
├── docs/                               # Main documentation
│   ├── README.md                       # Update (navigation hub)
│   ├── index.md                        # Keep or merge into README
│   │
│   ├── getting-started/                # NEW - User-facing quick starts
│   │   ├── README.md                   # Overview of quick start options
│   │   ├── local-dev.md                # From examples/local-dev/
│   │   ├── vagrant.md                  # From test-automation/QUICKSTART.md
│   │   ├── firecracker-vm.md           # From test-automation/QUICKSTART-FIRECRACKER.md
│   │   └── digitalocean.md             # From test-automation/DIGITALOCEAN.md
│   │
│   ├── guides/                         # Keep (how-to guides)
│   │   ├── README.md                   # Update (guide index)
│   │   ├── installation.md             # Keep
│   │   ├── configuration.md            # Keep
│   │   ├── networking.md               # Keep
│   │   ├── init-systems.md             # Keep
│   │   ├── file-management.md          # Keep
│   │   └── swarmkit/
│   │       ├── README.md               # Update (swarmkit guide index)
│   │       ├── deployment.md           # Consolidate from 3 deployment docs
│   │       ├── user-guide.md           # Keep
│   │       └── overview.md             # Keep
│   │
│   ├── architecture/                   # Keep (technical docs)
│   │   ├── README.md
│   │   ├── system.md                   # Keep
│   │   └── swarmkit-integration.md     # Keep
│   │
│   ├── development/                    # Keep (dev-focused)
│   │   ├── README.md
│   │   ├── testing.md                  # Keep
│   │   ├── secrets-prevention.md       # Keep
│   │   └── getting-started.md          # Keep
│   │
│   ├── reference/                      # NEW - API, config reference
│   │   ├── README.md
│   │   ├── cli.md                      # From existing guides
│   │   ├── config.md                   # Config options reference
│   │   └── api.md                      # If applicable
│   │
│   └── reports/                        # Keep (historical reports)
│       ├── README.md                   # Update (report index)
│       ├── unit/                       # Keep
│       ├── e2e/                        # Keep
│       ├── coverage/                   # Keep
│       └── archive/                    # Consolidate old reports here
│           ├── testing/                # From /docs/archive/testing/
│           └── old-implementation/     # OLD implementation docs
│
├── test-automation/                    # Test infrastructure
│   ├── README.md                       # Update (focus on infrastructure)
│   ├── Vagrantfile                     # Keep
│   ├── Vagrantfile.digitalocean        # Keep
│   ├── start-cluster.sh                # Keep
│   ├── setup-digitalocean.sh           # Keep
│   └── scripts/                        # Keep
│
└── examples/                           # Usage examples
    ├── local-dev/                      # Keep
    └── production-cluster/             # Keep
```

---

## Action Items

### Phase 1: Root Cleanup (High Priority)

**Files to Move:**
- `PROJECT.md` → Merge into `/docs/architecture/overview.md` or delete
- `COMMIT_PLAN.md` → Delete (outdated)
- `TASK_COMPLETION_REPORT.md` → `/docs/reports/archive/`
- `TEST_COVERAGE_IMPROVEMENTS.md` → `/docs/reports/coverage/`
- `COVERAGE_REPORT.md` → `/docs/reports/coverage/`
- `FIRECRACKER_INTEGRATION_SUMMARY.md` → `/docs/reports/e2e/`
- `DOCUMENTATION_UPDATE_SUMMARY.md` → Delete or `/docs/reports/archive/`
- `FIRECRACKER_EXECUTOR_IMPLEMENTATION_REPORT.md` → `/docs/reports/implementation/`

**Files to Keep at Root:**
- `README.md` ✅
- `CONTRIBUTING.md` ✅
- `AGENTS.md` ✅ (workspace file, not part of docs)

### Phase 2: Consolidate SwarmKit Guides

**Merge these into one comprehensive guide:**
- `/docs/guides/swarmkit/deployment.md`
- `/docs/guides/swarmkit/deployment-comprehensive.md`
- `/docs/guides/swarmkit/DEPLOYMENT_INDEX.md`
- `/docs/guides/swarmkit/clarification-summary.md`

**Result:** Single `/docs/guides/swarmkit/deployment.md` with clear sections

### Phase 3: Create Getting Started Section

**Create `/docs/getting-started/` with:**
1. Move `examples/local-dev/README.md` → `local-dev.md`
2. Move `test-automation/QUICKSTART.md` → `vagrant.md`
3. Move `test-automation/QUICKSTART-FIRECRACKER.md` → `firecracker-vm.md`
4. Move `test-automation/DIGITALOCEAN.md` → `digitalocean.md`
5. Create `README.md` explaining each option

### Phase 4: Clean Up Test-Automation Docs

**Keep in `/test-automation/`:**
- `README.md` (update to focus on infrastructure)
- `VAGRANT_FIXES.md` (troubleshooting)

**Move to `/docs/reports/archive/`:**
- `BUGFIX-REPORT.md`
- `CLUSTER-STATUS.md`
- `MANUAL-WORKER-JOIN.md`
- `SOCKET-PREPARATION.md`
- `WORKER1-STATUS.md`
- `AGENT-SETUP.md`

### Phase 5: Archive Cleanup

**Move to `/docs/reports/archive/old-testing/`:**
- All of `/docs/archive/testing/`

**Rationale:** These are historical E2E testing docs, not current guides

### Phase 6: Update Navigation

**Update `/docs/README.md` with clear sections:**
- Getting Started
- User Guides
- Architecture
- Development
- Reference
- Reports

---

## File-by-File Mapping

### Root Level → Move/Delete

| Current File | Action | New Location |
|--------------|--------|--------------|
| `PROJECT.md` | Review for content, then merge or delete | `/docs/architecture/` or delete |
| `COMMIT_PLAN.md` | Delete (outdated) | - |
| `TASK_COMPLETION_REPORT.md` | Move | `/docs/reports/archive/` |
| `TEST_COVERAGE_IMPROVEMENTS.md` | Move | `/docs/reports/coverage/` |
| `COVERAGE_REPORT.md` | Move | `/docs/reports/coverage/` |
| `FIRECRACKER_INTEGRATION_SUMMARY.md` | Move | `/docs/reports/e2e/` |
| `DOCUMENTATION_UPDATE_SUMMARY.md` | Delete or archive | `/docs/reports/archive/` |
| `FIRECRACKER_EXECUTOR_IMPLEMENTATION_REPORT.md` | Move | `/docs/reports/implementation/` |

### Test-Automation → Reorganize

| Current File | Action | New Location |
|--------------|--------|--------------|
| `QUICKSTART.md` | Move | `/docs/getting-started/vagrant.md` |
| `QUICKSTART-FIRECRACKER.md` | Move | `/docs/getting-started/firecracker-vm.md` |
| `DIGITALOCEAN.md` | Move | `/docs/getting-started/digitalocean.md` |
| `BUGFIX-REPORT.md` | Move | `/docs/reports/archive/` |
| `CLUSTER-STATUS.md` | Move | `/docs/reports/archive/` |
| `MANUAL-WORKER-JOIN.md` | Move | `/docs/reports/archive/` |
| `SOCKET-PREPARATION.md` | Move | `/docs/reports/archive/` |
| `WORKER1-STATUS.md` | Move | `/docs/reports/archive/` |
| `AGENT-SETUP.md` | Move | `/docs/reports/archive/` |
| `VAGRANT_FIXES.md` | Keep (troubleshooting) | Stay |
| `README.md` | Update | Stay |

### Docs Archive → Reports Archive

| Current File | Action | New Location |
|--------------|--------|--------------|
| `/docs/archive/testing/e2e_swarmkit.md` | Move | `/docs/reports/archive/testing/e2e-swarmkit.md` |
| `/docs/archive/testing/implementation.md` | Move | `/docs/reports/archive/testing/implementation.md` |
| `/docs/archive/testing/testinfra.md` | Move | `/docs/reports/archive/testing/testinfra.md` |
| Delete `/docs/archive/` after moving | - | - |

---

## Naming Convention

**Adopt:** `kebab-case.md` for all files

**Files to rename:**
- `CLUSTER-STATUS.md` → `cluster-status.md`
- `AGENT-SETUP.md` → `agent-setup.md`
- `MANUAL-WORKER-JOIN.md` → `manual-worker-joins.md`
- `SOCKET-PREPARATION.md` → `socket-preparation.md`
- `WORKER1-STATUS.md` → `worker1-status.md`

---

## Priority Matrix

| Phase | Priority | Effort | Impact |
|-------|----------|--------|--------|
| Phase 1: Root cleanup | 🔴 High | Low | High - Reduces clutter |
| Phase 2: Consolidate SwarmKit guides | 🟡 Medium | Medium | High - One source of truth |
| Phase 3: Create Getting Started | 🟢 Low | Medium | High - Better UX |
| Phase 4: Test-automation cleanup | 🟡 Medium | Low | Medium - Clearer separation |
| Phase 5: Archive cleanup | 🟢 Low | Low | Low - Historical docs |
| Phase 6: Update navigation | 🔴 High | Low | High - Discoverability |

---

## Success Criteria

1. ✅ Root directory has only 3 MD files (README, CONTRIBUTING, AGENTS)
2. ✅ Clear "Getting Started" section visible from main README
3. ✅ No duplicate deployment guides
4. ✅ All reports under `/docs/reports/`
5. ✅ Archive clearly separated from current docs
6. ✅ All filenames use `kebab-case.md`
7. ✅ `/docs/README.md` provides clear navigation

---

## Next Steps

1. **Review this plan** - Get feedback on structure
2. **Create new directories** - Set up folder structure
3. **Move files in phases** - Start with Phase 1
4. **Update internal links** - Fix broken references
5. **Update main README** - Add Getting Started links
6. **PR and review** - Ensure nothing broken

---

**Estimated Effort:** 2-3 hours
**Risk Level:** Low (file moves only, no content changes)
**Recommended Timeline:** Complete before next feature work
