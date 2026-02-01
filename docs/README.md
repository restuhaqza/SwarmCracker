# SwarmCracker Documentation

Welcome to the SwarmCracker documentation. This is your starting point for learning about, deploying, and using SwarmCracker.

## 🚀 Quick Start

**New here? Start with these:**

1. [Main README](../../README.md) - Project overview and quick start
2. [Installation Guide](guides/installation.md) - Set up SwarmCracker
3. [Local Development Example](../../examples/local-dev/README.md) - Try it out locally
4. [Comprehensive Deployment Guide](guides/swarmkit/deployment-comprehensive.md) - Production deployment

## 📚 Documentation by Topic

### Getting Started

| Document | Description |
|----------|-------------|
| [Installation Guide](guides/installation.md) | Prerequisites and installation |
| [Configuration Reference](guides/configuration.md) | All configuration options |
| [Networking Guide](guides/networking.md) | VM networking setup |
| [Init Systems Guide](guides/init-systems.md) | Tini/dumb-init integration |

### SwarmKit Deployment

| Document | Description |
|----------|-------------|
| [Comprehensive Deployment Guide](guides/swarmkit/deployment-comprehensive.md) | **START HERE** - Complete production guide (31KB) |
| [Basic Deployment Guide](guides/swarmkit/deployment.md) | Simple deployment walkthrough |
| [SwarmKit User Guide](guides/swarmkit/user-guide.md) | Using SwarmKit features |
| [Deployment Index](guides/swarmkit/DEPLOYMENT_INDEX.md) | All deployment resources |

### Architecture

| Document | Description |
|----------|-------------|
| [System Architecture](architecture/system.md) | High-level system design |
| [SwarmKit Integration](architecture/swarmkit-integration.md) | How SwarmCracker integrates with SwarmKit |

### Development

| Document | Description |
|----------|-------------|
| [Getting Started](development/getting-started.md) | Contributing and development workflow |
| [Testing Guide](development/testing.md) | Running and writing tests |

### Testing Reports

| Document | Description |
|----------|-------------|
| [Test Coverage Report](reports/coverage/) | **Coverage status: 85-88% overall** |
| [E2E Test Summary](reports/e2e/summary.md) | End-to-end test results |
| [Unit Test Reports](reports/unit/) | Unit test coverage reports |
| [Bug Reports](reports/BUGS_ISSUES.md) | Known issues and bugs |

## 🗂️  Documentation Structure

```
docs/
├── README.md                          # This file - START HERE
├── architecture/                      # System architecture docs
│   ├── system.md                      # High-level architecture
│   └── swarmkit-integration.md        # SwarmKit integration details
├── guides/                            # User-facing guides
│   ├── installation.md                # Installation instructions
│   ├── configuration.md               # Configuration reference
│   ├── networking.md                  # VM networking guide
│   ├── init-systems.md                # Init system guide
│   └── swarmkit/                      # SwarmKit-specific guides
│       ├── deployment-comprehensive.md # Main deployment guide
│       ├── deployment.md              # Basic deployment
│       ├── user-guide.md              # SwarmKit usage
│       └── DEPLOYMENT_INDEX.md        # All deployment resources
├── development/                       # Developer docs
│   ├── getting-started.md            # Contribution guide
│   └── testing.md                     # Testing guide
└── reports/                           # Test reports and analysis
    ├── BUGS_ISSUES.md                 # Known issues
    ├── e2e/                           # E2E test reports
    └── unit/                          # Unit test reports
```

## 🔍 Search by Use Case

**I want to...**

- **Install SwarmCracker:** [Installation Guide](guides/installation.md)
- **Deploy a cluster:** [Comprehensive Deployment Guide](guides/swarmkit/deployment-comprehensive.md)
- **Configure networking:** [Networking Guide](guides/networking.md)
- **Understand init systems:** [Init Systems Guide](guides/init-systems.md)
- **Contribute code:** [Getting Started](development/getting-started.md)
- **Run tests:** [Testing Guide](development/testing.md)
- **Review architecture:** [System Architecture](architecture/system.md)
- **See test results:** [E2E Test Summary](reports/e2e/summary.md)

## 📝 Contributing to Documentation

When adding or updating documentation:

1. **Use clear titles** - Make content scannable
2. **Add examples** - Show, don't just tell
3. **Keep it current** - Update as code changes
4. **Link liberally** - Connect related docs
5. **Proofread** - Check for clarity and correctness

See [Development Guide](development/getting-started.md) for contribution guidelines.

---

**Last Updated:** 2026-02-01  
**Version:** v0.1.0-alpha
