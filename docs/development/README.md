# Development Guide

> Contribute to SwarmCracker — code style, testing, PR process.

---

## Quick Start

### Prerequisites

- Go 1.21+
- Git
- Make
- golangci-lint (for linting)

### Setup

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
go mod download
make build
```

---

## Project Structure

```
SwarmCracker/
├── cmd/
│   ├── swarmcracker/     # Main CLI
│   ├── swarmctl/         # SwarmKit control CLI
│   └── deploy/           # Deployment tool
├── pkg/
│   ├── config/           # Configuration parsing
│   ├── executor/         # SwarmKit executor
│   ├── translator/       # Task translation
│   ├── network/          # TAP/bridge/VXLAN
│   ├── jailer/           # Security sandbox
│   ├── storage/          # Rootfs/volumes
│   ├── snapshot/         # VM snapshots
│   ├── metrics/          # Prometheus metrics
│   ├── swarmkit/         # SwarmKit integration
│   └── types/            # Shared types
├── docs/                 # Documentation
├── infrastructure/       # Ansible playbooks
├── test-automation/      # Test scripts
└── Makefile
```

---

## Code Style

### Go Conventions

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use [golangci-lint](https://golangci-lint.run/) for linting
- Table-driven tests preferred

### Commit Style

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

Types: feat, fix, docs, refactor, test, chore, perf
Scopes: executor, translator, network, jailer, storage, config, cli

Examples:
- feat(executor): add VM snapshot support
- fix(network): resolve TAP cleanup race
- docs(cli): document --subnet flag
```

### PR Process

1. Fork repository
2. Create feature branch
3. Make changes + add tests
4. Run `make lint && make test`
5. Submit PR with description

---

## Testing

### Run Tests

```bash
# All tests
make test

# Specific package
go test ./pkg/executor/...

# With coverage
go test -cover ./pkg/...

# Benchmarks
go test -bench=. ./pkg/...
```

### Test Structure

```go
func TestExecutor_Prepare(t *testing.T) {
    tests := []struct {
        name    string
        task    *types.Task
        wantErr bool
    }{
        {"valid task", validTask(), false},
        {"invalid task", invalidTask(), true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // test logic
        })
    }
}
```

See [Testing Overview](../testing/) for test strategy and coverage.

---

## Secrets Prevention

**Never commit secrets:**

- SSH keys
- Passwords
- API tokens
- `.vagrant/` directories
- Private key files

### Pre-commit Hook

```bash
# Install git-secrets
brew install git-secrets  # macOS
sudo apt install git-secrets  # Linux

git secrets --install
git secrets --register-aws
```

---

## Documentation

### Update Docs

When adding features:

1. Update relevant guide in `docs/guides/`
2. Add CLI docs to `docs/reference/cli.md`
3. Update architecture if needed

### Doc Style

- Use active voice
- Include examples
- Keep sections focused
- Link to related docs

---

## Release Process

### Version Scheme

Follow [SemVer](https://semver.org/):

- MAJOR: Breaking changes
- MINOR: New features
- PATCH: Bug fixes

### Release Steps

1. Update version in code
2. Update docs with version
3. Run full test suite
4. Create release PR
5. Tag release: `git tag v0.6.0`
6. Push tag: `git push origin v0.6.0`

---

## Make Targets

```bash
make build         # Build binaries
make test          # Run tests
make lint          # Run linter
make coverage      # Generate coverage report
make clean         # Clean artifacts
make install       # Install to /usr/local/bin
make uninstall     # Remove binaries
```

---

## IDE Setup

### VS Code

```json
{
  "go.toolsManagement.autoUpdate": true,
  "go.lintTool": "golangci-lint",
  "go.lintOnSave": "package"
}
```

### GoLand

- Enable golangci-lint
- Configure Go 1.21 SDK

---

## Troubleshooting

### Build Fails

```bash
# Check Go version
go version  # Must be 1.21+

# Clear module cache
go clean -modcache

# Re-download dependencies
go mod download
```

### Lint Errors

```bash
# Run lint with details
golangci-lint run ./pkg/...

# Fix auto-fixable issues
golangci-lint run --fix
```

### Test Fails

```bash
# Run with verbose output
go test -v ./pkg/executor/...

# Check for race conditions
go test -race ./pkg/...
```

---

## External Resources

- [Go Documentation](https://go.dev/doc/)
- [SwarmKit GitHub](https://github.com/moby/swarmkit)
- [Firecracker GitHub](https://github.com/firecracker-microvm/firecracker)
- [Effective Go](https://go.dev/doc/effective_go)

---

**See Also:** [Testing Overview](../testing/) | [Architecture](../architecture/)