# SwarmCracker Documentation

Welcome to the SwarmCracker documentation. This guide helps you navigate the project documentation.

## Quick Links

- [Getting Started](../README.md) - Quick start guide
- [Installation](guides/installation.md) - Installation instructions
- [Configuration](guides/configuration.md) - Configuration reference
- [Development](development/getting-started.md) - Contributor guide
- [Testing](testing/) - Complete testing documentation

## Documentation Structure

### [User Guides](guides/)
User-facing documentation for SwarmCracker.

- [Installation Guide](guides/installation.md) - How to install SwarmCracker
- [Configuration Guide](guides/configuration.md) - Configure SwarmCracker
- [File Management](guides/file-management.md) - Managing files
- [SwarmKit Guides](guides/swarmkit/) - SwarmKit-specific documentation
  - [Overview](guides/swarmkit/overview.md) - SwarmKit research
  - [User Guide](guides/swarmkit/user-guide.md) - Using SwarmKit
  - [Deployment](guides/swarmkit/deployment.md) - Deploying SwarmKit
  - [Audit](guides/swarmkit/audit.md) - Security audit

### [Architecture](architecture/)
Technical architecture documentation.

- [System Architecture](architecture/system.md) - Overall system design
- [SwarmKit Integration](architecture/swarmkit-integration.md) - Integration details

### [Development](development/)
Developer documentation.

- [Getting Started](development/getting-started.md) - Development setup
- [Testing](development/testing.md) - Testing guide

### [Testing](testing/)
Complete testing documentation.

- [Testing Overview](testing/README.md) - Testing framework overview
- [Unit Tests](testing/unit.md) - Unit testing guide
- [Integration Tests](testing/integration.md) - Integration testing with Firecracker
- [E2E Tests](testing/e2e.md) - End-to-end testing
- [SwarmKit E2E](testing/e2e_swarmkit.md) - SwarmKit-specific E2E tests
- [Test Infrastructure](testing/testinfra.md) - Infrastructure validation
- [Test Strategy](testing/strategy.md) - Testing strategy and approach

### [Test Reports](reports/)
Test execution reports and results.

- [E2E Reports](reports/e2e/) - End-to-end test results
  - [Phase 1 Results](reports/e2e/phase1-results.md)
  - [Phase 2 Results](reports/e2e/phase2-results.md)
  - [Real VM Report](reports/e2e/real-vm-report.md)
  - [Summary](reports/e2e/summary.md)
- [Unit Reports](reports/unit/) - Component test results
  - [Image Tests](reports/unit/image.md)
  - [Network Tests](reports/unit/network.md)

## Getting Started

### New Users
1. Read the [main README](../README.md) for an overview
2. Follow the [Installation Guide](guides/installation.md)
3. Check the [Configuration Guide](guides/configuration.md)

### Contributors
1. Read [Development Guide](development/getting-started.md) for setup
2. Review [System Architecture](architecture/system.md) for design
3. Follow the [Testing Guide](development/testing.md)
4. See [Testing Documentation](testing/) for practices

### Testers
1. Start with [Testing Overview](testing/README.md)
2. Check [test infrastructure](testing/testinfra.md) first
3. Run [integration tests](testing/integration.md)
4. Validate with [E2E tests](testing/e2e.md)

## Documentation Index

### By Topic

**Installation & Setup**
- [Installation](guides/installation.md)
- [Configuration](guides/configuration.md)
- [Development Setup](development/getting-started.md)

**Architecture & Design**
- [System Architecture](architecture/system.md)
- [SwarmKit Integration](architecture/swarmkit-integration.md)
- [Code Organization](ORGANIZATION.md)

**Development**
- [Development Guide](development/getting-started.md)
- [Testing Guide](development/testing.md)
- [Testing Documentation](testing/)

**Testing**
- [Testing Overview](testing/README.md)
- [Unit Tests](testing/unit.md)
- [Integration Tests](testing/integration.md)
- [E2E Tests](testing/e2e.md)
- [Test Infrastructure](testing/testinfra.md)

### By Format

**Guides** - Step-by-step instructions
**References** - Technical specifications
**Reports** - Test results and analysis

## Conventions

### Documentation Labels
- 🚧 **Work in Progress** - Document being updated
- ✅ **Complete** - Fully documented
- ⚠️ **Deprecated** - Outdated content
- 📋 **Planning** - Not yet implemented

### Code Examples
Code blocks show shell commands:
```bash
make test
```

Or Go code:
```go
func example() {
    fmt.Println("Hello")
}
```

## Resources

- [GitHub Repository](https://github.com/restuhaqza/swarmcracker)
- [Issues](https://github.com/restuhaqza/swarmcracker/issues)
- [Discord Community](https://discord.com/invite/clawd)

## Quick Reference

### Common Commands
```bash
# Build
make build

# Test
make test              # Unit tests
make integration-test # Integration tests
make e2e-test         # E2E tests
make testinfra        # Infrastructure checks

# Run
./build/swarmcracker --help
```

### File Locations
- Source code: `pkg/`
- Tests: `test/`
- Documentation: `docs/`
- Examples: `examples/`
- Configuration: `config.example.yaml`

## Need Help?

- Check existing documentation
- Search [GitHub Issues](https://github.com/restuhaqza/swarmcracker/issues)
- Join [Discord](https://discord.com/invite/clawd)
- Create a new issue

---

**Last Updated**: 2026-02-01
**Version**: v0.1.0-alpha
