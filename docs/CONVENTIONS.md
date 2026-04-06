# Markdown File Naming Convention

Standard naming conventions for all Markdown documentation files in the SwarmCracker project.

---

## 📝 General Rules

### 1. Use Lowercase
All filenames should be lowercase (except README.md, INDEX.md, DOCS.md).

✅ **Correct:**
- `getting-started.md`
- `rolling-updates.md`
- `multi-arch-support.md`

❌ **Incorrect:**
- `Getting-Started.md`
- `ROLLING-UPDATES.md`
- `vagrant-fixes.md`

### 2. Use Kebab-Case
Use hyphens (`-`) to separate words, not underscores or spaces.

✅ **Correct:**
- `security-hardening.md`
- `firecracker-vm.md`
- `swarmkit-integration.md`

❌ **Incorrect:**
- `security_hardening.md`
- `firecracker vm.md`
- `SwarmkitIntegration.md`

### 3. Keep It Short & Descriptive
Filenames should be concise but clearly describe the content.

✅ **Correct:**
- `testing.md` (not `guide-to-testing-strategies.md`)
- `installation.md` (not `how-to-install.md`)

### 4. Special Files (Exceptions)
These files use UPPERCASE by convention:
- `README.md` - Project/folder documentation
- `INDEX.md` - Documentation index
- `DOCS.md` - Master documentation index
- `AGENTS.md` - Agent configuration
- `CONTRIBUTING.md` - Contribution guidelines

---

## 📁 Directory-Specific Conventions

### Root Level
```
projects/swarmcracker/
├── README.md              ✅ Standard
├── DOCS.md                ✅ Standard
├── AGENTS.md              ✅ Standard
├── CONTRIBUTING.md        ✅ Standard
└── LICENSE                ✅ Standard
```

### Documentation (`docs/`)
```
docs/
├── README.md              ✅ Standard
├── INDEX.md               ✅ Standard
├── vxlan-overlay.md       ✅ Kebab-case
├── architecture/
│   ├── README.md
│   ├── system.md          ✅ Simple
│   └── swarmkit-integration.md  ✅ Kebab-case
├── guides/
│   ├── README.md
│   ├── rolling-updates.md ✅ Kebab-case
│   └── multi-arch-support.md  ✅ Kebab-case
└── getting-started/
    ├── README.md
    ├── installation.md    ✅ Simple
    └── local-dev.md       ✅ Kebab-case
```

### Infrastructure (`infrastructure/ansible/`)
```
infrastructure/ansible/
├── README.md              ✅ Standard
├── test-report.md         ✅ Kebab-case (was test-report.md)
└── verification-report.md ✅ Kebab-case (was verification-report.md)
```

### Test Automation (`test-automation/`)
```
test-automation/
├── README.md              ✅ Standard
├── README.ansible-testing.md  ✅ Descriptive suffix
└── vagrant-fixes.md      ✅ Kebab-case (was vagrant-fixes.md)
```

### Test Directories (`test/`)
```
test/
├── README.md
├── integration/
│   ├── README.md
│   └── firecracker-setup.md  ✅ Kebab-case (was firecracker-setup.md)
└── e2e/
    └── README.md
```

---

## 🔄 Migration Guide

### Files to Rename

| Old Name | New Name | Location |
|----------|----------|----------|
| `vxlan-overlay.md` | `vxlan-overlay.md` | `docs/` |
| `installation.md` | `installation.md` | `docs/getting-started/` |
| `test-report.md` | `test-report.md` | `infrastructure/ansible/` |
| `verification-report.md` | `verification-report.md` | `infrastructure/ansible/` |
| `vagrant-fixes.md` | `vagrant-fixes.md` | `test-automation/` |
| `firecracker-setup.md` | `firecracker-setup.md` | `test/integration/` |

### Update References

After renaming, update all references in:
1. Other markdown files
2. Links in README files
3. Documentation indexes
4. CI/CD configuration (if any)

---

## 🎯 Naming Patterns by Content Type

### Guides & How-Tos
Pattern: `<topic>.md` or `<topic>-<subtopic>.md`

Examples:
- `installation.md`
- `configuration.md`
- `rolling-updates.md`
- `security-hardening.md`

### Concepts & Architecture
Pattern: `<concept>.md`

Examples:
- `system.md`
- `networking.md`
- `overview.md`

### Quick Starts
Pattern: `quick-start.md` or `<topic>-quick-start.md`

Examples:
- `quick-start.md`
- `local-dev.md`

### Testing
Pattern: `<test-type>.md`

Examples:
- `unit.md`
- `integration.md`
- `e2e.md`
- `strategy.md`

### Reports
Pattern: `<report-type>-report.md`

Examples:
- `test-report.md`
- `verification-report.md`

---

## ✅ Checklist for New Files

Before committing a new `.md` file:

- [ ] Filename is lowercase
- [ ] Uses hyphens (not underscores)
- [ ] Concise and descriptive
- [ ] Follows directory convention
- [ ] References updated in other files
- [ ] Added to appropriate README/index

---

## 📊 Summary

| Convention | Example | Status |
|------------|---------|--------|
| Lowercase | `getting-started.md` | ✅ Required |
| Kebab-case | `multi-arch-support.md` | ✅ Required |
| Short & clear | `testing.md` | ✅ Required |
| README uppercase | `README.md` | ✅ Exception |
| INDEX uppercase | `INDEX.md` | ✅ Exception |
| DOCS uppercase | `DOCS.md` | ✅ Exception |

---

**Last Updated:** 2026-04-06  
**Version:** 1.0  
**Enforced By:** Documentation review process
