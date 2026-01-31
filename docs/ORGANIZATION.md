# Documentation Organization

This document describes how the SwarmCracker documentation is organized.

## Documentation Structure

```
SwarmCracker/
├── README.md                 # Main project README (quick start)
├── CONTRIBUTING.md           # Contribution guidelines
├── PROJECT.md                # Project status and roadmap
├── AGENTS.md                 # Agent-specific configuration
│
├── docs/                     # Main documentation directory
│   ├── index.md              # Documentation index (START HERE)
│   ├── overview.md           # Project overview
│   ├── INSTALL.md            # Installation guide
│   ├── CONFIG.md             # Configuration reference
│   ├── ARCHITECTURE.md       # System architecture
│   ├── DEVELOPMENT.md        # Development guide
│   ├── ORGANIZATION.md       # Code organization
│   │
│   ├── testing/              # Testing documentation
│   │   ├── README.md         # Testing overview
│   │   ├── unit.md           # Unit testing guide
│   │   ├── integration.md    # Integration testing guide
│   │   ├── e2e.md            # E2E testing guide
│   │   ├── testinfra.md      # Test infrastructure guide
│   │   └── implementation.md # Framework implementation
│   │
│   └── reports/              # Test reports
│       ├── IMAGE_PREPARER_TESTS_REPORT.md
│       └── NETWORK_MANAGER_TESTS_REPORT.md
│
└── test/                     # Test code
    ├── README.md             # Test directory overview
    ├── e2e/                  # E2E test code
    ├── integration/          # Integration test code
    ├── testinfra/            # Infrastructure test code
    └── mocks/                # Mock implementations
```

## Documentation Files

### Root Level

#### README.md
**Purpose**: Main project landing page
**Audience**: New users
**Contents**: Quick start, overview, links to detailed docs

#### CONTRIBUTING.md
**Purpose**: Contribution guidelines
**Audience**: Contributors
**Contents**: Workflow, commit messages, PR process

#### PROJECT.md
**Purpose**: Project status and roadmap
**Audience**: Everyone
**Contents**: Version, features, progress, next steps

#### AGENTS.md
**Purpose**: Agent-specific configuration
**Audience**: Users deploying agents
**Contents**: Agent setup, configuration, management

### docs/ Directory

#### docs/index.md
**Purpose**: Main documentation navigation
**Audience**: Everyone (START HERE)
**Contents**: Links to all documentation, quick reference

#### docs/overview.md
**Purpose**: Detailed project overview
**Audience**: New users
**Contents**: Features, architecture, components, getting started

#### docs/INSTALL.md
**Purpose**: Installation instructions
**Audience**: Users
**Contents**: Prerequisites, installation steps, verification

#### docs/CONFIG.md
**Purpose**: Configuration reference
**Audience**: Users
**Contents**: All config options, examples, defaults

#### docs/ARCHITECTURE.md
**Purpose**: System architecture
**Audience**: Developers
**Contents**: Design, components, data flow, interfaces

#### docs/DEVELOPMENT.md
**Purpose**: Development guide
**Audience**: Contributors
**Contents**: Setup, workflow, testing, debugging

#### docs/ORGANIZATION.md
**Purpose**: Code organization
**Audience**: Developers
**Contents**: Directory structure, packages, modules

### docs/testing/ Directory

#### docs/testing/README.md
**Purpose**: Testing overview
**Audience**: Everyone
**Contents**: Test types, running tests, workflow

#### docs/testing/unit.md
**Purpose**: Unit testing guide
**Audience**: Developers
**Contents**: Writing unit tests, examples, best practices

#### docs/testing/integration.md
**Purpose**: Integration testing guide
**Audience**: Developers
**Contents**: Setup, running integration tests, troubleshooting

#### docs/testing/e2e.md
**Purpose**: E2E testing guide
**Audience**: Developers, QA
**Contents**: E2E test setup, scenarios, Docker Swarm

#### docs/testing/testinfra.md
**Purpose**: Test infrastructure guide
**Audience**: Developers, DevOps
**Contents**: Environment validation, checks, CI/CD

#### docs/testing/implementation.md
**Purpose**: Test framework implementation
**Audience**: Test framework developers
**Contents**: Framework architecture, internals, extensions

### docs/reports/ Directory

Contains detailed test reports:
- IMAGE_PREPARER_TESTS_REPORT.md
- NETWORK_MANAGER_TESTS_REPORT.md

## Navigation

### For New Users
1. Start with [README.md](../README.md)
2. Read [docs/INSTALL.md](INSTALL.md) for installation
3. Check [docs/CONFIG.md](CONFIG.md) for configuration
4. See [docs/testing/testinfra.md](testing/testinfra.md) to verify setup

### For Contributors
1. Read [CONTRIBUTING.md](../CONTRIBUTING.md)
2. Review [docs/DEVELOPMENT.md](DEVELOPMENT.md)
3. Study [docs/ARCHITECTURE.md](ARCHITECTURE.md)
4. Follow [docs/testing/README.md](testing/README.md) for testing

### For Testers
1. Start with [docs/testing/README.md](testing/README.md)
2. Check [docs/testing/testinfra.md](testing/testinfra.md)
3. Run integration tests: [docs/testing/integration.md](testing/integration.md)
4. Run E2E tests: [docs/testing/e2e.md](testing/e2e.md)

## Documentation Conventions

### File Formats
- **Markdown (.md)** - All documentation
- **Go code** - Examples in doc comments
- **YAML** - Configuration examples

### Naming
- `README.md` - Directory overview
- `index.md` - Navigation/index
- Lowercase with hyphens for multi-word files
- UPPER_CASE for titles in text

### Structure
- H1 for document title
- H2 for main sections
- H3 for subsections
- Code blocks for examples
- Tables for summaries
- Links for navigation

### Labels
- 🚧 **Work in Progress**
- ✅ **Complete**
- ⚠️ **Deprecated**
- 📋 **Planning**

## Quick Reference

### Common Tasks

| Task | Document |
|------|----------|
| Install SwarmCracker | [docs/INSTALL.md](INSTALL.md) |
| Configure SwarmCracker | [docs/CONFIG.md](CONFIG.md) |
| Understand architecture | [docs/ARCHITECTURE.md](ARCHITECTURE.md) |
| Start developing | [docs/DEVELOPMENT.md](DEVELOPMENT.md) |
| Write unit tests | [docs/testing/unit.md](testing/unit.md) |
| Run integration tests | [docs/testing/integration.md](testing/integration.md) |
| Run E2E tests | [docs/testing/e2e.md](testing/e2e.md) |
| Validate environment | [docs/testing/testinfra.md](testing/testinfra.md) |

### Documentation Updates

When adding features:
1. Update [docs/ARCHITECTURE.md](ARCHITECTURE.md) if design changes
2. Add to [docs/CONFIG.md](CONFIG.md) if new config options
3. Update [docs/testing/](testing/) if adding tests
4. Keep [PROJECT.md](../PROJECT.md) current with status

## Search Tips

### Looking for something specific?

**Installation?**
→ [docs/INSTALL.md](INSTALL.md)

**Configuration options?**
→ [docs/CONFIG.md](CONFIG.md)

**How to contribute?**
→ [CONTRIBUTING.md](../CONTRIBUTING.md)

**Testing setup?**
→ [docs/testing/](testing/)

**System design?**
→ [docs/ARCHITECTURE.md](ARCHITECTURE.md)

**Project status?**
→ [PROJECT.md](../PROJECT.md)

## Missing Documentation?

If you can't find what you need:
1. Check [docs/index.md](index.md) for full index
2. Search GitHub Issues
3. Ask on Discord
4. Create an issue requesting documentation

## Keeping Docs Current

### Before Merging PR
- Update relevant documentation
- Add new docs for new features
- Check for outdated information
- Update examples if needed

### Documentation Review
Checklist:
- [ ] All new features documented
- [ ] Config options in CONFIG.md
- [ ] Architecture changes in ARCHITECTURE.md
- [ ] Tests documented in testing/
- [ ] Examples updated
- [ ] Links working

---

**Last Updated**: 2026-02-01
**Maintained By**: SwarmCracker Team
