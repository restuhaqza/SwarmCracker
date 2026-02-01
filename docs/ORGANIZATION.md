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
└── docs/                     # Main documentation directory
    ├── index.md              # Documentation index (START HERE)
    ├── ORGANIZATION.md       # This file - documentation organization
    │
    ├── guides/               # User-facing guides
    │   ├── README.md         # Guides overview
    │   ├── installation.md   # Installation guide
    │   ├── configuration.md  # Configuration reference
    │   ├── file-management.md # File management guide
    │   └── swarmkit/        # SwarmKit-specific guides
    │       ├── README.md     # SwarmKit guides index
    │       ├── overview.md   # SwarmKit research and analysis
    │       ├── user-guide.md # SwarmKit user guide
    │       ├── deployment.md # SwarmKit deployment guide
    │       ├── audit.md      # SwarmKit security audit
    │       └── clarification-summary.md # Design clarifications
    │
    ├── architecture/         # Technical architecture
    │   ├── README.md         # Architecture overview
    │   ├── system.md         # System architecture and design
    │   └── swarmkit-integration.md # SwarmKit integration details
    │
    ├── development/          # Developer guides
    │   ├── README.md         # Development overview
    │   ├── getting-started.md # Development setup
    │   └── testing.md        # Testing guide for developers
    │
    ├── testing/              # Testing documentation
    │   ├── README.md         # Testing overview
    │   ├── unit.md           # Unit testing guide
    │   ├── integration.md    # Integration testing guide
    │   ├── e2e.md            # E2E testing guide
    │   ├── e2e_swarmkit.md   # SwarmKit E2E tests
    │   ├── testinfra.md      # Test infrastructure guide
    │   ├── implementation.md # Framework implementation
    │   └── strategy.md       # Testing strategy
    │
    └── reports/              # Test reports
        ├── README.md         # Reports overview
        ├── BUGS_ISSUES.md    # Known bugs and issues
        ├── e2e/              # E2E test reports
        │   ├── README.md     # E2E reports index
        │   ├── phase1-results.md
        │   ├── phase2-results.md
        │   ├── real-vm-report.md
        │   ├── summary.md
        │   └── test-report.md
        └── unit/             # Unit test reports
            ├── README.md     # Unit reports index
            ├── image.md      # Image preparer tests
            └── network.md    # Network manager tests
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
**Purpose**: Main documentation navigation hub
**Audience**: Everyone (START HERE)
**Contents**: Links to all documentation, quick reference, common commands

#### docs/ORGANIZATION.md
**Purpose**: Documentation organization guide
**Audience**: Document maintainers
**Contents**: Structure, file descriptions, conventions

### docs/guides/ Directory

User-facing documentation for using SwarmCracker.

#### guides/README.md
**Purpose**: Guides navigation
**Audience**: Users
**Contents**: Overview of all user guides

#### guides/installation.md
**Purpose**: Installation instructions
**Audience**: Users
**Contents**: Prerequisites, installation steps, verification

#### guides/configuration.md
**Purpose**: Configuration reference
**Audience**: Users
**Contents**: All config options, examples, defaults

#### guides/file-management.md
**Purpose**: File management guide
**Audience**: Users, Administrators
**Contents**: Managing files and directories in SwarmCracker

### docs/guides/swarmkit/ Directory

SwarmKit-specific documentation.

#### guides/swarmkit/README.md
**Purpose**: SwarmKit guides index
**Audience**: SwarmKit users
**Contents**: Overview of SwarmKit documentation

#### guides/swarmkit/overview.md
**Purpose**: SwarmKit research and analysis
**Audience**: Technical users
**Contents**: SwarmKit architecture, research findings

#### guides/swarmkit/user-guide.md
**Purpose**: SwarmKit user guide
**Audience**: SwarmKit users
**Contents**: How to use SwarmKit features in SwarmCracker

#### guides/swarmkit/deployment.md
**Purpose**: SwarmKit deployment guide
**Audience**: DevOps, System administrators
**Contents**: Deploying SwarmKit with SwarmCracker

#### guides/swarmkit/audit.md
**Purpose**: Security audit
**Audience**: Security reviewers
**Contents**: Security audit findings for SwarmKit integration

#### guides/swarmkit/clarification-summary.md
**Purpose**: Design clarifications
**Audience**: Developers
**Contents**: Clarifications on SwarmKit design decisions

### docs/architecture/ Directory

Technical architecture documentation.

#### architecture/README.md
**Purpose**: Architecture overview
**Audience**: Technical readers
**Contents**: Overview of architecture documentation

#### architecture/system.md
**Purpose**: System architecture
**Audience**: Developers, Architects
**Contents**: Design, components, data flow, interfaces

#### architecture/swarmkit-integration.md
**Purpose**: SwarmKit integration details
**Audience**: Developers
**Contents**: How SwarmCracker integrates with SwarmKit

### docs/development/ Directory

Developer documentation.

#### development/README.md
**Purpose**: Development overview
**Audience**: Contributors
**Contents**: Overview of development documentation

#### development/getting-started.md
**Purpose**: Development setup guide
**Audience**: New contributors
**Contents**: Setup, workflow, development environment

#### development/testing.md
**Purpose**: Testing guide for developers
**Audience**: Contributors
**Contents**: How to run and write tests

### docs/testing/ Directory

Complete testing documentation.

#### testing/README.md
**Purpose**: Testing overview
**Audience**: Everyone
**Contents**: Test types, running tests, workflow

#### testing/unit.md
**Purpose**: Unit testing guide
**Audience**: Developers
**Contents**: Writing unit tests, examples, best practices

#### testing/integration.md
**Purpose**: Integration testing guide
**Audience**: Developers
**Contents**: Setup, running integration tests, troubleshooting

#### testing/e2e.md
**Purpose**: E2E testing guide
**Audience**: Developers, QA
**Contents**: E2E test setup, scenarios, Docker Swarm

#### testing/e2e_swarmkit.md
**Purpose**: SwarmKit E2E tests
**Audience**: Developers, QA
**Contents**: SwarmKit-specific end-to-end testing

#### testing/testinfra.md
**Purpose**: Test infrastructure guide
**Audience**: Developers, DevOps
**Contents**: Environment validation, checks, CI/CD

#### testing/implementation.md
**Purpose**: Test framework implementation
**Audience**: Test framework developers
**Contents**: Framework architecture, internals, extensions

#### testing/strategy.md
**Purpose**: Testing strategy
**Audience**: QA, Developers
**Contents**: Overall testing approach and methodology

### docs/reports/ Directory

Test execution reports and results.

#### reports/README.md
**Purpose**: Reports overview
**Audience**: Everyone
**Contents**: Index of all test reports

#### reports/BUGS_ISSUES.md
**Purpose**: Known bugs and issues
**Audience**: Everyone
**Contents**: Current bugs, tracking, status

### docs/reports/e2e/ Directory

E2E test reports.

#### reports/e2e/README.md
**Purpose**: E2E reports index
**Audience**: QA, Developers
**Contents**: Overview of E2E test reports

#### reports/e2e/phase1-results.md
**Purpose**: Phase 1 E2E results
**Audience**: QA, Developers
**Contents**: Initial E2E testing results

#### reports/e2e/phase2-results.md
**Purpose**: Phase 2 E2E results
**Audience**: QA, Developers
**Contents**: Follow-up E2E testing results

#### reports/e2e/real-vm-report.md
**Purpose**: Real VM testing report
**Audience**: QA, Developers
**Contents**: Real environment VM test results

#### reports/e2e/summary.md
**Purpose**: E2E test summary
**Audience**: Everyone
**Contents**: Summary of all E2E testing

#### reports/e2e/test-report.md
**Purpose**: General E2E test report
**Audience**: QA, Developers
**Contents**: Detailed E2E test report

### docs/reports/unit/ Directory

Unit test reports.

#### reports/unit/README.md
**Purpose**: Unit reports index
**Audience**: Developers
**Contents**: Overview of unit test reports

#### reports/unit/image.md
**Purpose**: Image preparer tests
**Audience**: Developers
**Contents**: Image preparation component test results

#### reports/unit/network.md
**Purpose**: Network manager tests
**Audience**: Developers
**Contents**: Network management component test results

## Navigation

### For New Users
1. Start with [README.md](../README.md)
2. Read [guides/installation.md](guides/installation.md) for installation
3. Check [guides/configuration.md](guides/configuration.md) for configuration
4. See [testing/testinfra.md](testing/testinfra.md) to verify setup

### For Contributors
1. Read [CONTRIBUTING.md](../CONTRIBUTING.md)
2. Review [development/getting-started.md](development/getting-started.md)
3. Study [architecture/system.md](architecture/system.md)
4. Follow [development/testing.md](development/testing.md) for testing

### For Testers
1. Start with [testing/README.md](testing/README.md)
2. Check [testing/testinfra.md](testing/testinfra.md)
3. Run integration tests: [testing/integration.md](testing/integration.md)
4. Run E2E tests: [testing/e2e.md](testing/e2e.md)

### For SwarmKit Users
1. Read [guides/swarmkit/overview.md](guides/swarmkit/overview.md)
2. Follow [guides/swarmkit/user-guide.md](guides/swarmkit/user-guide.md)
3. Deploy with [guides/swarmkit/deployment.md](guides/swarmkit/deployment.md)
4. Check [testing/e2e_swarmkit.md](testing/e2e_swarmkit.md) for tests

## Documentation Conventions

### File Formats
- **Markdown (.md)** - All documentation
- **Go code** - Examples in doc comments
- **YAML** - Configuration examples

### Naming
- `README.md` - Directory overview
- `index.md` - Navigation/index
- Lowercase with hyphens for multi-word files
- Descriptive names (e.g., `overview.md` not `RESEARCH.md`)

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
| Install SwarmCracker | [guides/installation.md](guides/installation.md) |
| Configure SwarmCracker | [guides/configuration.md](guides/configuration.md) |
| Manage files | [guides/file-management.md](guides/file-management.md) |
| Understand architecture | [architecture/system.md](architecture/system.md) |
| Start developing | [development/getting-started.md](development/getting-started.md) |
| Write tests | [development/testing.md](development/testing.md) |
| Run unit tests | [testing/unit.md](testing/unit.md) |
| Run integration tests | [testing/integration.md](testing/integration.md) |
| Run E2E tests | [testing/e2e.md](testing/e2e.md) |
| Validate environment | [testing/testinfra.md](testing/testinfra.md) |
| Use SwarmKit | [guides/swarmkit/user-guide.md](guides/swarmkit/user-guide.md) |
| Deploy SwarmKit | [guides/swarmkit/deployment.md](guides/swarmkit/deployment.md) |

### Documentation Updates

When adding features:
1. Update [architecture/system.md](architecture/system.md) if design changes
2. Add to [guides/configuration.md](guides/configuration.md) if new config options
3. Update [testing/](testing/) if adding tests
4. Keep [PROJECT.md](../PROJECT.md) current with status

## Search Tips

### Looking for something specific?

**Installation?**
→ [guides/installation.md](guides/installation.md)

**Configuration?**
→ [guides/configuration.md](guides/configuration.md)

**File management?**
→ [guides/file-management.md](guides/file-management.md)

**SwarmKit?**
→ [guides/swarmkit/](guides/swarmkit/)

**Architecture?**
→ [architecture/](architecture/)

**Contributing?**
→ [development/getting-started.md](development/getting-started.md)

**Testing?**
→ [testing/](testing/)

**Test reports?**
→ [reports/](reports/)

## Missing Documentation?

If you can't find what you need:
1. Check [index.md](index.md) for full index
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
- [ ] Config options in guides/configuration.md
- [ ] Architecture changes in architecture/system.md
- [ ] Tests documented in testing/
- [ ] Examples updated
- [ ] Links working

---

**Last Updated**: 2026-02-01
**Maintained By**: SwarmCracker Team
