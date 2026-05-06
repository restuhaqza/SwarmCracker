# Contributing

Here's how to work on SwarmCracker.

---

## Setup

<<<<<<< HEAD
### Prerequisites

- Go 1.25+
- Git
- Make
- golangci-lint (for linting)

### Setup
=======
You need Go 1.21+, Git, Make. golangci-lint helps with linting.
>>>>>>> 6b8080a (feat: sync work from dumbledore workspace + coverage boost)

```bash
git clone https://github.com/restuhaqza/SwarmCracker
cd SwarmCracker
go mod download
make build
```

---

## Repo Structure

```
cmd/
├── swarmcracker/    # Main CLI wrapper
├── swarmctl/        # Direct SwarmKit commands
├── swarmd-firecracker/  # Daemon

pkg/
├── executor/        # SwarmKit task → VM config
├── network/         # Bridges, TAP, VXLAN
├── discovery/       # Consul
├── swarmkit/        # SwarmKit glue
├── image/           # OCI extraction
├── lifecycle/       # VM start/stop
├── jailer/          # Security
├── storage/         # Volumes, secrets
├── snapshot/        # State snapshots
├── metrics/         # Prometheus
├── types/           # Shared types

docs/                # User + dev docs
infrastructure/      # Ansible deployment
test-automation/     # Vagrant cluster
```

---

## Code Style

### Go

Follow Effective Go. Run golangci-lint before submitting.

### Commits

Conventional commits:

```
feat(executor): add snapshot restore
fix(network): VXLAN FDB race condition
docs(cli): document new flags
```

Types: feat, fix, docs, refactor, test, chore, perf
Scopes: executor, network, discovery, jailer, storage, cli

---

## Testing

```bash
# Everything
make test

# One package
go test ./pkg/network/...

# Coverage
go test -cover ./pkg/executor/...
```

### Integration Tests

Need a cluster. Use Vagrant:

```bash
cd test-automation
vagrant up
./e2e-test-suite.sh
```

---

## Pull Requests

1. Fork it
2. Branch for your change
3. Write code + tests
4. `make lint && make test`
5. Push and open PR

Describe what you changed and why. If it fixes an issue, mention the number.

---

## Before Submitting

- Tests pass
- Lint clean
- No secrets in code (tokens, keys)
- Documentation updated if needed

---

## Questions

<<<<<<< HEAD
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
- Configure Go 1.25 SDK

---

## Troubleshooting

### Build Fails

```bash
# Check Go version
go version  # Must be 1.25+

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
=======
Open an issue or ask in discussions.
>>>>>>> 6b8080a (feat: sync work from dumbledore workspace + coverage boost)
