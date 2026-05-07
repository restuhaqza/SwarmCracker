# Documentation Reorganization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Separate public user docs from internal planning docs into two directories for professional appearance and better organization.

**Architecture:** Create `internal-docs/` directory with categorized subfolders (plans/, research/, specs/), move implementation plans and research notes from `docs/` to `internal-docs/`, rename files to lowercase convention, delete redundant `INDEX.md`.

**Tech Stack:** Git, Markdown, File system operations

---

## File Structure

### Files to Create

| File | Purpose |
|------|---------|
| `internal-docs/README.md` | Index of internal documentation |
| `internal-docs/plans/` | Implementation plans directory |
| `internal-docs/research/` | Research notes directory |
| `internal-docs/specs/` | Design specs directory |

### Files to Move

| Source | Destination | Category |
|--------|-------------|----------|
| `docs/TODO-IMPLEMENTATION-PLAN.md` | `internal-docs/plans/todo-implementation.md` | Plan |
| `docs/INIT_DEINIT_PLAN.md` | `internal-docs/plans/init-deinit.md` | Plan |
| `docs/UNIT_TEST_PLAN.md` | `internal-docs/plans/unit-test.md` | Plan |
| `docs/E2E_SCENARIO_PLAN.md` | `internal-docs/plans/e2e-scenario.md` | Plan |
| `docs/IMAGE-SDK-RESEARCH.md` | `internal-docs/research/image-sdk.md` | Research |
| `docs/IMAGE-PREPARATION-NO-DAEMON.md` | `internal-docs/research/image-preparation-no-daemon.md` | Research |
| `docs/swarmkit-overlay-integration.md` | `internal-docs/research/swarmkit-overlay-integration.md` | Research |
| `docs/CONVENTIONS.md` | `internal-docs/CONVENTIONS.md` | Meta |

### Files to Delete

| File | Reason |
|------|--------|
| `docs/INDEX.md` | Redundant with `docs/README.md` |

---

## Task 1: Create internal-docs Directory Structure

**Files:**
- Create: `internal-docs/`
- Create: `internal-docs/plans/`
- Create: `internal-docs/research/`
- Create: `internal-docs/specs/`

- [ ] **Step 1: Create directory structure**

```bash
mkdir -p internal-docs/plans internal-docs/research internal-docs/specs
```

Expected: Three directories created under `internal-docs/`

- [ ] **Step 2: Verify directories exist**

```bash
ls -la internal-docs/
```

Expected: `plans/`, `research/`, `specs/` directories visible

- [ ] **Step 3: Commit**

```bash
git add internal-docs/
git commit -m "docs: create internal-docs directory structure"
```

---

## Task 2: Create internal-docs README

**Files:**
- Create: `internal-docs/README.md`

- [ ] **Step 1: Write internal-docs index**

Create file `internal-docs/README.md`:

```markdown
# Internal Documentation

> Planning, research, and design specs for SwarmCracker contributors.

---

## Categories

| Category | Description |
|----------|-------------|
| [Plans](plans/) | Implementation plans, test strategies |
| [Research](research/) | Technical research, investigation notes |
| [Specs](specs/) | Design specifications |

---

## Quick Links

### Plans

| Plan | Status | Description |
|------|--------|-------------|
| [TODO Implementation](plans/todo-implementation.md) | Complete | TODO items resolution |
| [Init/Deinit](plans/init-deinit.md) | In Progress | Cluster lifecycle commands |
| [Unit Test](plans/unit-test.md) | Active | Test coverage strategy |
| [E2E Scenario](plans/e2e-scenario.md) | Reference | Full deployment walkthrough |

### Research

| Document | Description |
|----------|-------------|
| [Image SDK](research/image-sdk.md) | OCI image handling research |
| [Image Preparation](research/image-preparation-no-daemon.md) | Rootfs preparation approach |
| [SwarmKit Overlay](research/swarmkit-overlay-integration.md) | Network overlay integration |

---

## Related

- [CONVENTIONS.md](CONVENTIONS.md) - Documentation conventions
- [Public Docs](../docs/) - User-facing documentation
```

- [ ] **Step 2: Verify file created**

```bash
cat internal-docs/README.md
```

Expected: Full content visible

- [ ] **Step 3: Commit**

```bash
git add internal-docs/README.md
git commit -m "docs: add internal-docs index"
```

---

## Task 3: Move Plan Documents

**Files:**
- Move: `docs/TODO-IMPLEMENTATION-PLAN.md` → `internal-docs/plans/todo-implementation.md`
- Move: `docs/INIT_DEINIT_PLAN.md` → `internal-docs/plans/init-deinit.md`
- Move: `docs/UNIT_TEST_PLAN.md` → `internal-docs/plans/unit-test.md`
- Move: `docs/E2E_SCENARIO_PLAN.md` → `internal-docs/plans/e2e-scenario.md`

- [ ] **Step 1: Move TODO Implementation Plan**

```bash
git mv docs/TODO-IMPLEMENTATION-PLAN.md internal-docs/plans/todo-implementation.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 2: Move Init/Deinit Plan**

```bash
git mv docs/INIT_DEINIT_PLAN.md internal-docs/plans/init-deinit.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 3: Move Unit Test Plan**

```bash
git mv docs/UNIT_TEST_PLAN.md internal-docs/plans/unit-test.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 4: Move E2E Scenario Plan**

```bash
git mv docs/E2E_SCENARIO_PLAN.md internal-docs/plans/e2e-scenario.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 5: Verify all plans moved**

```bash
ls -la internal-docs/plans/
```

Expected: Four files: `todo-implementation.md`, `init-deinit.md`, `unit-test.md`, `e2e-scenario.md`

- [ ] **Step 6: Commit**

```bash
git commit -m "docs: move implementation plans to internal-docs/plans/"
```

---

## Task 4: Move Research Documents

**Files:**
- Move: `docs/IMAGE-SDK-RESEARCH.md` → `internal-docs/research/image-sdk.md`
- Move: `docs/IMAGE-PREPARATION-NO-DAEMON.md` → `internal-docs/research/image-preparation-no-daemon.md`
- Move: `docs/swarmkit-overlay-integration.md` → `internal-docs/research/swarmkit-overlay-integration.md`

- [ ] **Step 1: Move Image SDK Research**

```bash
git mv docs/IMAGE-SDK-RESEARCH.md internal-docs/research/image-sdk.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 2: Move Image Preparation Research**

```bash
git mv docs/IMAGE-PREPARATION-NO-DAEMON.md internal-docs/research/image-preparation-no-daemon.md
```

Expected: File moved and renamed to lowercase

- [ ] **Step 3: Move SwarmKit Overlay Integration**

```bash
git mv docs/swarmkit-overlay-integration.md internal-docs/research/swarmkit-overlay-integration.md
```

Expected: File moved (already lowercase)

- [ ] **Step 4: Verify all research moved**

```bash
ls -la internal-docs/research/
```

Expected: Three files: `image-sdk.md`, `image-preparation-no-daemon.md`, `swarmkit-overlay-integration.md`

- [ ] **Step 5: Commit**

```bash
git commit -m "docs: move research notes to internal-docs/research/"
```

---

## Task 5: Move CONVENTIONS.md

**Files:**
- Move: `docs/CONVENTIONS.md` → `internal-docs/CONVENTIONS.md`

- [ ] **Step 1: Move CONVENTIONS.md**

```bash
git mv docs/CONVENTIONS.md internal-docs/CONVENTIONS.md
```

Expected: File moved to internal-docs root

- [ ] **Step 2: Verify file moved**

```bash
ls -la internal-docs/CONVENTIONS.md
```

Expected: File exists at new location

- [ ] **Step 3: Commit**

```bash
git commit -m "docs: move CONVENTIONS.md to internal-docs/"
```

---

## Task 6: Delete Redundant INDEX.md

**Files:**
- Delete: `docs/INDEX.md`

- [ ] **Step 1: Verify INDEX.md still exists**

```bash
ls -la docs/INDEX.md
```

Expected: File exists

- [ ] **Step 2: Delete INDEX.md**

```bash
git rm docs/INDEX.md
```

Expected: File removed from repository

- [ ] **Step 3: Verify deletion**

```bash
ls -la docs/INDEX.md
```

Expected: `No such file or directory`

- [ ] **Step 4: Commit**

```bash
git commit -m "docs: remove redundant INDEX.md"
```

---

## Task 7: Final Verification

**Files:**
- None (verification only)

- [ ] **Step 1: Verify docs/ structure**

```bash
ls -la docs/
```

Expected: Only user-facing directories remain: `getting-started/`, `guides/`, `architecture/`, `reference/`, `development/`, `testing/`, `site/`, `README.md`, `superpowers/`

- [ ] **Step 2: Verify internal-docs/ structure**

```bash
ls -la internal-docs/
```

Expected: `README.md`, `CONVENTIONS.md`, `plans/`, `research/`, `specs/`

- [ ] **Step 3: Verify no broken files left in docs/**

```bash
find docs/ -type f -name "*.md" | grep -v -E "(getting-started|guides|architecture|reference|development|testing|site|superpowers)" | grep -v "README.md"
```

Expected: Empty output (no stray files)

- [ ] **Step 4: View git status**

```bash
git status
```

Expected: `nothing to commit, working tree clean`

- [ ] **Step 5: View git log for this session**

```bash
git log --oneline -10
```

Expected: 6 new commits visible:
1. `docs: create internal-docs directory structure`
2. `docs: add internal-docs index`
3. `docs: move implementation plans to internal-docs/plans/`
4. `docs: move research notes to internal-docs/research/`
5. `docs: move CONVENTIONS.md to internal-docs/`
6. `docs: remove redundant INDEX.md`

---

## Success Criteria Checklist

- [ ] Public docs contain only user-facing content
- [ ] Internal docs organized by category (plans/, research/, specs/)
- [ ] No duplicate index files (INDEX.md deleted)
- [ ] Consistent lowercase naming for internal docs
- [ ] Clear README in both docs/ and internal-docs/
- [ ] No broken links after migration
- [ ] All changes committed to git

---

## Notes

- Each task produces one logical commit
- File moves use `git mv` to preserve history
- Lowercase naming follows markdown best practices
- No content changes - only restructuring
- Implementation time: 15-20 minutes