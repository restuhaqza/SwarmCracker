# SwarmCracker Documentation

Welcome to the SwarmCracker documentation. This guide helps you navigate the project documentation.

## Quick Links

- [Getting Started](README.md) - Quick start guide
- [Installation](docs/INSTALL.md) - Installation instructions
- [Development Guide](docs/DEVELOPMENT.md) - Contributor guide
- [Testing Guide](docs/testing/) - Complete testing documentation

## Documentation Structure

### User Documentation
- [README.md](README.md) - Project overview and quick start
- [INSTALL.md](docs/INSTALL.md) - Installation instructions
- [CONFIG.md](docs/CONFIG.md) - Configuration reference

### Developer Documentation
- [DEVELOPMENT.md](docs/DEVELOPMENT.md) - Development setup and workflow
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) - System architecture
- [ORGANIZATION.md](docs/ORGANIZATION.md) - Code organization
- [CONTRIBUTING.md](CONTRIBUTING.md) - Contribution guidelines

### Testing Documentation
See [docs/testing/](docs/testing/) for complete testing documentation:
- [Testing Overview](docs/testing/README.md) - Testing framework overview
- [Unit Tests](docs/testing/unit.md) - Unit testing guide
- [Integration Tests](docs/testing/integration.md) - Integration testing with Firecracker
- [E2E Tests](docs/testing/e2e.md) - End-to-end testing with SwarmKit
- [Test Infrastructure](docs/testing/testinfra.md) - Infrastructure validation

### Project Documentation
- [PROJECT.md](PROJECT.md) - Project status and roadmap
- [AGENTS.md](AGENTS.md) - Agent-specific configuration

### Test Reports
- [docs/reports/](docs/reports/) - Detailed test reports

## Getting Started

### New Users
1. Read the [README.md](README.md) for an overview
2. Follow [INSTALL.md](docs/INSTALL.md) to install SwarmCracker
3. Check [CONFIG.md](docs/CONFIG.md) for configuration options

### Contributors
1. Read [DEVELOPMENT.md](docs/DEVELOPMENT.md) for setup
2. Review [ARCHITECTURE.md](docs/ARCHITECTURE.md) for system design
3. Follow [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines
4. See [docs/testing/](docs/testing/) for testing practices

### Testers
1. Start with [docs/testing/README.md](docs/testing/README.md)
2. Check [test infrastructure](docs/testing/testinfra.md) first
3. Run [integration tests](docs/testing/integration.md)
4. Validate with [E2E tests](docs/testing/e2e.md)

## Documentation Index

### By Topic

**Installation & Setup**
- [Installation](docs/INSTALL.md)
- [Configuration](docs/CONFIG.md)
- [Development Setup](docs/DEVELOPMENT.md)

**Architecture & Design**
- [Architecture](docs/ARCHITECTURE.md)
- [Code Organization](docs/ORGANIZATION.md)
- [Project Status](PROJECT.md)

**Development**
- [Development Guide](docs/DEVELOPMENT.md)
- [Contributing](CONTRIBUTING.md)
- [Testing](docs/testing/)

**Testing**
- [Testing Overview](docs/testing/README.md)
- [Integration Tests](docs/testing/integration.md)
- [E2E Tests](docs/testing/e2e.md)
- [Test Infrastructure](docs/testing/testinfra.md)

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
