# Docker Swarm Reference Audit

## Audit Date
2026-02-01

## Purpose
Identify all misleading "Docker Swarm" references in the SwarmCracker codebase that should be updated to clarify we use SwarmKit directly.

## Files Audited

### Critical (Must Fix)

#### 1. README.md
**Lines with issues:**
- Line 5: `Firecracker MicroVMs meet Docker Swarm Orchestration` ❌
  - Should be: `Firecracker MicroVMs with SwarmKit Orchestration`
- Line 15: `Hardware-isolated microVMs with the simplicity of Docker Swarm` ❌
  - Should be: `Hardware-isolated microVMs with SwarmKit orchestration`
- Line 30: `🐳 **Swarm Simplicity** - Use familiar Docker Swarm commands and workflows` ❌
  - Should be: `🐳 **SwarmKit Orchestration** - Use swarmctl for service management`
- Prerequisites section: `**Docker Swarm** initialized or SwarmKit standalone` ⚠️
  - Should clarify: SwarmKit standalone (swarmd/swarmctl)
- Quick Start section: Mentions `docker service create` ❌
  - Should use: `swarmctl service create`

**Priority**: CRITICAL - This is the first thing users see

#### 2. docs/SWARMKIT_INTEGRATION.md
**Lines with issues:**
- Line 24: `2. **Simple Orchestration** - Uses familiar Docker Swarm commands` ❌
  - Should be: `Uses SwarmKit (swarmctl) commands`

**Priority**: IMPORTANT - Technical documentation

#### 3. docs/testing/E2E_STRATEGY.md
**Lines with issues:**
- Multiple references to "Docker Swarm E2E Tests"
- Should clarify these are for environment validation, not SwarmCracker's main integration
- Distinguish between:
  - Docker Swarm tests (environment validation)
  - SwarmKit tests (actual integration)

**Priority**: IMPORTANT - Testing strategy clarity

#### 4. docs/testing/e2e_swarmkit.md
**Lines with issues:**
- Has good clarification already but could be strengthened
- Line 1: Good - correctly explains SwarmKit vs Docker Swarm
- Keep the clarification, make it more prominent

**Priority**: MINOR - Already mostly correct

#### 5. docs/index.md
**Lines with issues:**
- Reference: `- [E2E Tests](docs/testing/e2e.md) - End-to-end testing with Docker Swarm` ❌
  - Should be: `End-to-end testing with SwarmKit`

**Priority**: MINOR - Documentation index

### Code Files (Go)

#### 6. test/e2e/swarmkit_api_test.go
**Lines with issues:**
- Skip message: `t.Skip("Docker Swarm not initialized. Docker Swarm uses SwarmKit internally.")` ⚠️
  - Should be: `SwarmKit not initialized. See docs for standalone SwarmKit setup.`

**Priority**: MINOR - Test skip messages

#### 7. test/e2e/swarmkit_comprehensive_test.go
**Lines with issues:**
- Similar skip messages about Docker Swarm
- Should reference SwarmKit directly

**Priority**: MINOR - Test messages

#### 8. test/e2e/docker_swarm_test.go
**Status**: This file is OK - it's explicitly for Docker Swarm environment validation
- No changes needed - this is correctly testing Docker Swarm environment

**Priority**: NONE - Purpose is correct

## Summary by Category

### Critical Updates (User-Facing)
1. README.md - Main project description and quick start
2. docs/SWARMKIT_INTEGRATION.md - Core integration documentation

### Important Updates (Technical)
3. docs/testing/E2E_STRATEGY.md - Testing strategy clarity
4. docs/index.md - Documentation index

### Minor Updates (Clarification)
5. test/e2e/swarmkit_api_test.go - Test messages
6. test/e2e/swarmkit_comprehensive_test.go - Test messages

### No Changes Needed
7. test/e2e/docker_swarm_test.go - Correctly tests Docker Swarm environment
8. docs/testing/e2e_swarmkit.md - Already has good clarification

## Correct Terminology Guide

### ✅ Use These Terms
- **SwarmKit** - The orchestration engine
- **swarmd** - SwarmKit daemon/agent
- **swarmctl** - SwarmKit control CLI
- **SwarmKit orchestration** - The type of orchestration we provide
- **Custom executor** - What SwarmCracker implements
- **MicroVM orchestration** - What we do

### ❌ Avoid These Terms (in our context)
- **"Docker Swarm"** (when referring to our integration)
- **"docker service"** (use swarmctl service instead)
- **"Docker Swarm commands"** (use SwarmKit/swarmctl commands)
- **"Swarm simplicity"** (be specific: SwarmKit orchestration)

### ⚠️ Acceptable Contexts for "Docker Swarm"
- Explaining the relationship: "SwarmKit is the engine Docker Swarm uses"
- Comparison: "Unlike Docker Swarm, SwarmKit can be used standalone"
- Testing: Docker Swarm environment validation tests
- Migration: "Migrating from Docker Swarm to SwarmKit"

## Action Plan

### Phase 1: Update Critical Files
1. ✅ Create SWARMKIT_RESEARCH.md (DONE)
2. ⏳ Update README.md
3. ⏳ Update docs/SWARMKIT_INTEGRATION.md

### Phase 2: Create SwarmKit Guide
4. ⏳ Create docs/SWARMKIT_GUIDE.md
5. ⏳ Update docs/index.md

### Phase 3: Update Testing Docs
6. ⏳ Clarify docs/testing/E2E_STRATEGY.md
7. ⏳ Review test code comments

### Phase 4: Create Examples
8. ⏳ Create examples/swarmkit-deployment/README.md
9. ⏳ Update example configurations

### Phase 5: Verification
10. ⏳ Final grep audit
11. ⏳ Verify all changes are accurate

## Success Criteria

- ✅ Research document created
- ⏳ README.md has no misleading "Docker Swarm" references
- ⏳ All docs use "SwarmKit" terminology correctly
- ⏳ SwarmKit guide created with standalone usage
- ⏳ Examples demonstrate SwarmKit (swarmctl) not Docker (docker service)
- ⏳ Clear distinction maintained throughout

---

**Audit Completed**: 2026-02-01  
**Next Step**: Update README.md
