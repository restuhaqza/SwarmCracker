# Documentation Website Planning

> Planning documents for the SwarmCracker documentation website update (2026-05-03)

## Overview

This directory contains planning documents created during the website update to bring the landing page in sync with SwarmCracker v0.6.0+ features.

## Documents

### UPDATE_PLAN.md
Comprehensive technical implementation plan covering:
- Current state analysis
- Missing features (VXLAN, Jailer, swarmctl, etc.)
- Detailed implementation phases
- File structure proposals
- Content strategy (website vs. GitHub docs)

**Use when**: Need detailed technical guidance for implementation

### PLAN_SUMMARY.md
Executive summary with:
- Visual comparison (current vs. future)
- Week-by-week timeline
- Success metrics
- Priority ordering

**Use when**: Need quick overview of the plan

### FEATURE_CHECKLIST.md
Page-by-page update checklist with:
- Feature coverage matrix
- Priority tracking (P0/P1/P2)
- Content sources reference
- Launch checklist

**Use when**: Tracking progress and ensuring completeness

### BEFORE_AFTER.md
Visual comparison showing:
- Website state before and after
- User journey improvements
- Metrics and ROI analysis
- Time investment estimate

**Use when**: Justifying the work or showing impact

## Implementation Status

### ✅ Completed (2026-05-03)

- [x] Landing page updated
- [x] Hero section emphasizes multi-node
- [x] 3 new feature cards added
- [x] Networking section created
- [x] Installation commands updated with VXLAN flags
- [x] Stats section expanded (latency, packet loss)
- [x] Footer links expanded

### 📋 In Progress

- [ ] VXLAN networking guide
- [ ] Jailer security guide
- [ ] swarmctl CLI reference
- [ ] Ansible deployment guide

### 📅 Planned

- [ ] Multi-node example
- [ ] Production deployment example
- [ ] Architecture diagrams (VXLAN, Jailer)
- [ ] Interactive elements (optional)

## Notes

- These documents are **temporary planning artifacts**
- Once implementation is complete, this directory can be archived or removed
- Keep until website is fully updated and all features are documented

## Related

- **Website**: `docs/site/index.html`
- **GitHub Documentation**: `docs/user/`
- **Project Board**: [GitHub Issues](https://github.com/restuhaqza/SwarmCracker/issues)

---

**Created**: 2026-05-03
**Status**: Active planning
**Next Review**: After implementation complete
