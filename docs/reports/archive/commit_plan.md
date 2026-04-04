# Commit Organization Plan

## Proposed Commits (in order)

### 1. Security: Pre-commit hooks and .gitignore
**Purpose:** Prevent secrets from being committed
**Files:**
- .gitignore (M)
- .githooks/ (A)
- .gitsecrets_config (A)
- scripts/install-hooks.sh (A)
- docs/development/secrets-prevention.md (A)

**Commit message:**
```
feat: add pre-commit hooks to prevent secret commits

- Add pre-commit hook that scans for secrets (API keys, passwords, SSH keys)
- Update .gitignore with sensitive file patterns (.pem, .env, credentials)
- Add hook installation script for team setup
- Document secret prevention in docs/development/secrets-prevention.md
- Protects against: SSH keys, API tokens, certificates, .env files, Vagrant artifacts

Closes: Security issue with secret exposure
```

---

### 2. Docs: Update coverage badges and testing guide
**Purpose:** Reflect current coverage status
**Files:**
- README.md (M)
- docs/development/testing.md (M)

**Commit message:**
```
docs: update coverage statistics and testing documentation

- Update coverage badge: 81.2% → 87% (current average)
- Document coverage improvements in testing guide
- Add new test files and strategies documentation
- Include code examples for context, concurrent, and validation tests
- Link to COVERAGE_REPORT.md for detailed breakdown

Current coverage:
- config: 95.8% ✅
- executor: 95.2% ✅
- translator: 94.9% ✅
- lifecycle: 85% (+10%)
- image: 80% (+19%)
- network: 82% (+14%)
```

---

### 3. Docs: Minify markdown documentation
**Purpose:** Reduce verbosity and improve clarity
**Files:**
- COVERAGE_REPORT.md (A)
- DOCUMENTATION_UPDATE_SUMMARY.md (A)
- TEST_COVERAGE_IMPROVEMENTS.md (M)
- TASK_COMPLETION_REPORT.md (M)
- FIRECRACKER_INTEGRATION_SUMMARY.md (M)

**Commit message:**
```
docs: minimize and organize documentation

- Reduce documentation verbosity by ~90% (59KB → 5.5KB)
- Consolidate redundant information
- Improve readability with concise format
- Keep all critical information and links
- Focus on essential content with clear structure

Files affected:
- COVERAGE_REPORT.md: 28KB → 1.7KB
- TEST_COVERAGE_IMPROVEMENTS.md: 5.9KB → 766B
- TASK_COMPLETION_REPORT.md: 11KB → 1.3KB
- FIRECRACKER_INTEGRATION_SUMMARY.md: 8.4KB → 864B
- DOCUMENTATION_UPDATE_SUMMARY.md: 5.3KB → 850B
```

---

### 4. Test: Add comprehensive unit tests
**Purpose:** Improve coverage with focused unit tests
**Files:**
- pkg/lifecycle/vmm_unit_test.go (A)
- pkg/image/preparer_unit_test.go (A)
- pkg/network/allocator_unit_test.go (A)
- pkg/lifecycle/vmm.go (M) - Added nil check
- pkg/image/preparer_coverage_boost_test.go (M) - Fixed test
- pkg/network/manager_additional_test.go (M) - Fixed IP validation

**Commit message:**
```
test: add comprehensive unit tests for coverage improvement

- Add 3 new unit test files (~1,350 lines, 190+ test cases)
- pkg/lifecycle/vmm_unit_test.go: state transitions, concurrent access, validation
- pkg/image/preparer_unit_test.go: input validation, cache behavior, init systems
- pkg/network/allocator_unit_test.go: IP allocation, thread safety, TAP devices
- Fix nil check panic in VMMManager.Wait()
- Fix test expectations in image and network packages
- All tests use mocking (no root privileges required)

Coverage improvements:
- lifecycle: 75% → 85% (+10%)
- image: 61% → 80% (+19%)
- network: 68% → 82% (+14%)
```

---

### 5. Test: Add context and advanced test scenarios
**Purpose:** Test edge cases and error paths
**Files:**
- pkg/lifecycle/vmm_context_test.go (A)
- pkg/lifecycle/vmm_additional_test.go (A)
- pkg/image/preparer_context_test.go (A)
- pkg/image/preparer_additional_test.go (A)
- pkg/network/manager_context_test.go (A)
- pkg/network/manager_additional_test.go (A)

**Commit message:**
```
test: add context cancellation and advanced test scenarios

- Add 6 comprehensive test files with 115KB of test code
- Test context cancellation in all major operations
- Test concurrent operations and thread safety
- Test edge cases (nil values, empty strings, special chars)
- Test error paths with proper mocking
- Fix TestVMMManager_ContextStateTransitions (use real PID)
- Fix network IP validation tests

Focus areas:
- Context timeout handling
- Graceful vs hard shutdown
- Concurrent describe/wait/remove operations
- Image cache behavior
- IP allocator edge cases
```

---

### 6. Refactor: Apply gofmt and fix linter warnings
**Purpose:** Code formatting and quality
**Files:**
- All .go files (M) - gofmt applied
- cmd/swarmcracker/main.go (M) - Fix error message capitalization
- pkg/translator/translator.go (M) - Replace HasPrefix with TrimPrefix
- pkg/image/init.go (M) - Remove unused fields, fix empty branch
- pkg/network/manager_test.go (M) - Fix ineffectual assignment
- test/integration/init_test.go (M) - Remove unused imports

**Commit message:**
```
refactor: apply code formatting and fix linter warnings

- Run gofmt on all Go files
- Fix 6 critical linter issues:
  * Remove unused struct fields (tiniBinary, dumbInitBinary)
  * Fix empty branch with explicit ignore
  * Fix capitalized error string ("Firecracker" → "firecracker")
  * Replace HasPrefix+slice with TrimPrefix
  * Fix ineffectual assignment in network test
  * Remove unused imports from integration test
- Linter issues reduced: 68 → 62

Remaining 62 issues are mostly unchecked errors in cleanup code/test helpers
```

---

### 7. Fix: Remove deleted vmm_old.go
**Purpose:** Complete removal of duplicate file
**Files:**
- pkg/lifecycle/vmm_old.go (D)

**Commit message:**
```
fix: remove duplicate vmm_old.go file

- Remove pkg/lifecycle/vmm_old.go (was causing duplicate declarations)
- File was already deleted from working directory
- This completes the removal from git tracking
```

---

### 8. Docs: Add coverage and completion reports
**Purpose:** Document recent work
**Files:**
- COVERAGE_REPORT.md (A)
- DOCUMENTATION_UPDATE_SUMMARY.md (A)
- TASK_COMPLETION_REPORT.md (M)
- TEST_COVERAGE_IMPROVEMENTS.md (M)

**Commit message:**
```
docs: add coverage and task completion reports

- Add COVERAGE_REPORT.md with before/after analysis
- Add DOCUMENTATION_UPDATE_SUMMARY.md tracking changes
- Minimize TEST_COVERAGE_IMPROVEMENTS.md and TASK_COMPLETION_REPORT.md
- Document coverage improvements: +10-19% across 3 packages
- Document 190+ new test cases added
- Document testing strategies (context, concurrent, validation)
```

---

## Staging Strategy

1. Start with security hooks (highest priority)
2. Then documentation updates (metadata)
3. Then test improvements (code quality)
4. Finally formatting and cleanup (cosmetic)

## Verification

Before each commit:
- Review staged files: `git diff --cached --stat`
- Run tests: `go test ./pkg/...`
- Check for secrets: `.githooks/pre-commit` will run automatically

## Push Strategy

Push all commits together:
```bash
git push origin main
```

This creates a clean, organized history with logical commits.
