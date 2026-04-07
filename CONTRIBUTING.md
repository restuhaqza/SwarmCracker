# Contributing to SwarmCracker

SwarmCracker is a custom SwarmKit executor that runs workloads as Firecracker microVMs. Contributions — code, docs, bug reports, ideas — are all welcome.

## Table of Contents

- [Quick Start](#quick-start)
- [How to Contribute](#how-to-contribute)
- [Development Setup](#development-setup)
- [Project Structure](#project-structure)
- [Development Workflow](#development-workflow)
- [Coding Standards](#coding-standards)
- [Testing](#testing)
- [Pull Request Process](#pull-request-process)
- [Release Process](#release-process)

---

## Quick Start

```bash
# 1. Fork the repo, then clone your fork
git clone https://github.com/YOUR_USERNAME/SwarmCracker.git
cd SwarmCracker

# 2. Install dependencies
go mod download

# 3. Verify everything works
make test
make lint
```

That's it. You're ready to hack.

---

## How to Contribute

| Type | How |
|------|-----|
| **Bug report** | [Open an issue](.github/ISSUE_TEMPLATE/bug_report.md) with reproduction steps |
| **Feature request** | [Open an issue](.github/ISSUE_TEMPLATE/feature_request.md) describing the use case |
| **Code** | Fork → branch → PR (see [Development Workflow](#development-workflow)) |
| **Documentation** | Fork → fix → PR. See [docs/CONVENTIONS.md](docs/CONVENTIONS.md) for file naming |
| **Question** | Start a [GitHub Discussion](https://github.com/restuhaqza/SwarmCracker/discussions) |

### Before You Start

1. **Check existing issues** — your idea might already be tracked or in progress.
2. **Comment on the issue** — for significant work, propose your approach first. This avoids wasted effort if the maintainers have a different direction in mind.
3. **Keep scope tight** — one PR, one concern. A PR that adds a feature, refactors networking, and updates docs is harder to review than three focused PRs.

---

## Development Setup

### Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| Go | 1.24+ | Check with `go version` |
| make | any | Build automation |
| git | any | |
| Docker / Podman | any | For integration tests |
| KVM | — | `/dev/kvm` must exist. Not needed for unit tests. |
| Firecracker | 1.14+ | Auto-installed by `install.sh`. Needed for integration/e2e tests. |

### Build

```bash
# Build all binaries
make all

# Build a specific binary
make swarmcracker
make swarmd-firecracker
make swarmcracker-agent

# Install to $GOPATH/bin
make install
```

### Test

```bash
# Unit tests (no KVM needed)
make test

# With race detector
go test -race ./pkg/...

# With coverage
go test -coverprofile=coverage.out ./pkg/...
go tool cover -html=coverage.out

# Integration tests (requires KVM + Firecracker)
make test-integration

# E2E tests
make test-e2e
```

### Lint & Format

```bash
make lint    # golangci-lint
make fmt     # gofmt
```

---

## Project Structure

```
swarmcracker/
├── cmd/                     # CLI entrypoints
│   ├── swarmcracker/        #   Main orchestration CLI
│   ├── swarmd-firecracker/  #   SwarmKit agent with FC executor
│   └── swarmcracker-agent/  #   Agent daemon
├── pkg/                     # Core library code
│   ├── executor/            #   Firecracker executor implementation
│   ├── translator/          #   SwarmKit task → VM config translation
│   ├── image/               #   OCI image → rootfs preparation
│   ├── network/             #   TAP devices, bridges, VXLAN overlay
│   ├── lifecycle/           #   VM start/stop/monitor lifecycle
│   ├── storage/             #   Pluggable volume driver system
│   ├── config/              #   Configuration parsing & validation
│   ├── security/            #   Jailer, capabilities, seccomp
│   ├── swarmkit/            #   SwarmKit API integration
│   └── types/               #   Shared interfaces & types
├── test/                    # Test helpers & mocks
│   ├── mocks/               #   Mock implementations
│   ├── integration/         #   Integration tests (tag: integration)
│   └── e2e/                 #   End-to-end tests (tag: e2e)
├── infrastructure/          # Ansible playbooks, Terraform
├── docs/                    # All documentation
│   ├── guides/              #   How-to guides
│   ├── architecture/        #   Design docs
│   ├── development/         #   Contributor docs
│   └── getting-started/     #   Setup guides
└── examples/                # Example configurations
```

---

## Development Workflow

### 1. Create a Branch

```bash
# Feature
git checkout -b feature/descriptive-name

# Bug fix
git checkout -b fix/descriptive-name

# Hotfix (urgent, from main)
git checkout -b hotfix/descriptive-name
```

**Branch naming:** `feature/`, `fix/`, `docs/`, `refactor/`, `test/`, `chore/`

### 2. Make Changes

- Write code following [Coding Standards](#coding-standards).
- Add or update tests.
- Update docs if user-facing behavior changes.
- Run `make lint` and `make test` before committing.

### 3. Commit

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>

[optional body explaining why, not what]

[optional footer: BREAKING CHANGE, Fixes #, etc.]
```

**Scopes:** `executor`, `translator`, `network`, `lifecycle`, `image`, `storage`, `config`, `security`, `cli`, `docs`

**Types:**
| Type | Purpose |
|------|---------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `refactor` | Code change that neither fixes nor adds |
| `test` | Adding or updating tests |
| `chore` | Build, CI, tooling |
| `perf` | Performance improvement |

**Examples:**
```bash
feat(executor): add VM snapshot and restore support
fix(network): resolve TAP device cleanup race on shutdown
docs(cli): document new --subnet flag for swarmd-firecracker
refactor(translator): extract resource mapping into separate function
```

### 4. Push & Open PR

```bash
git push origin feature/descriptive-name
```

Then open a pull request against `main`. See [Pull Request Process](#pull-request-process).

---

## Coding Standards

### Go

- Follow [Effective Go](https://go.dev/doc/effective_go.html).
- Run `gofmt` — the formatter is law.
- Run `golangci-lint` before pushing. Config lives in `.golangci.yml`.
- Write `godoc` comments on all exported types, functions, and constants.
- Accept `context.Context` as the first parameter. Always check for cancellation.

### Naming

| Element | Convention | Example |
|---------|-----------|---------|
| Packages | lowercase, single word | `executor`, `network` |
| Exported functions | PascalCase | `StartVM`, `TranslateTask` |
| Unexported | camelCase | `createTAPDevice` |
| Interfaces | noun or `-er` suffix | `VMMManager`, `Translator` |
| Constants | PascalCase for exported, camelCase for unexported | `MaxVMsPerNode`, `defaultBridgeName` |
| Errors | lowercase start (Go convention) | `fmt.Errorf("failed to start vm: %w", err)` |

### Error Handling

```go
// ✅ Wrap errors with context
if err := vmm.Start(ctx, cfg); err != nil {
    return fmt.Errorf("failed to start VM %s: %w", vmID, err)
}

// ✅ Sentinel errors for expected conditions
var ErrVMNotFound = errors.New("vm not found")

// ❌ Never panic in library code
if err != nil {
    panic(err)  // Don't
}

// ❌ Never swallow errors
if err != nil {
    // silent
}
```

### Logging

Use structured logging with [zerolog](https://github.com/rs/zerolog):

```go
// ✅ Structured fields
log.Info().
    Str("task_id", task.ID).
    Str("vm_id", vmID).
    Int("vcpu", cfg.VCPUs).
    Msg("VM started")

// ✅ Errors with context
log.Error().
    Err(err).
    Str("task_id", task.ID).
    Msg("Failed to prepare rootfs")

// ❌ String interpolation
log.Info().Msgf("Started VM %s with %d CPUs", vmID, cpus)
```

### Code Organization

- Keep functions focused. If it's doing two things, split it.
- Interfaces belong in `pkg/types/` — implementations in their own package.
- Avoid circular imports. Use dependency injection.

---

## Testing

### Coverage Targets

| Package | Target |
|---------|--------|
| Core (executor, translator, lifecycle) | 80%+ |
| Network, storage, image | 70%+ |
| Config, types | 90%+ |

### Unit Tests

```go
func TestTranslate(t *testing.T) {
    tests := []struct {
        name    string
        task    *types.Task
        want    *VMConfig
        wantErr bool
    }{
        {
            name: "simple nginx container",
            task: mocks.NewTestTask("task-1", "nginx:alpine"),
            want: &VMConfig{Image: "nginx:alpine"},
        },
        {
            name: "missing image",
            task: &types.Task{},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := translator.Translate(tt.task)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}
```

### Integration & E2E Tests

Use build tags to separate from unit tests:

```go
//go:build integration
// +build integration

package integration_test

func TestVMLifecycle(t *testing.T) {
    // Requires KVM + Firecracker
}
```

```bash
# Run only unit tests (CI does this)
make test

# Run integration tests (local, needs KVM)
make test-integration
```

---

## Pull Request Process

1. **Update docs** if the change affects user-facing behavior, CLI flags, or configuration.
2. **Add tests** for new code. PRs that reduce coverage without explanation will be asked to add tests.
3. **Run checks locally** — `make lint && make test` — before pushing. CI will do the same.
4. **Fill out the PR template** — describe the change, type, and testing.
5. **Keep PRs small** — aim for under 400 lines of diff. Reviewers can't give good feedback on mega-PRs.
6. **One commit per logical change** — squash unrelated changes. Use interactive rebase if needed.
7. **Respond to review feedback** — address comments or explain your reasoning. Silence isn't resolution.

### CI Pipeline

Every PR runs:

1. **Vet** — `go vet ./pkg/... ./cmd/...`
2. **Test** — `go test -short -race` on core packages
3. **Build** — `make all`
4. **Lint** — `golangci-lint`

All must pass before merge.

---

## Release Process

SwarmCracker follows [Semantic Versioning](https://semver.org/):

- **MAJOR** — Breaking changes to CLI, config, or API
- **MINOR** — New features, backwards compatible
- **PATCH** — Bug fixes

Releases are handled by maintainers. See the [release workflow](.github/workflows/release.yml) for automation details.

---

## Getting Help

- **Issues** — Bug reports and feature requests
- **Discussions** — Questions, architecture ideas, general chat
- **Existing docs** — Start with [docs/INDEX.md](docs/INDEX.md)

## Code of Conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Be respectful. Be constructive. Build cool stuff.

## License

Contributions are licensed under the Apache License 2.0, same as the project. See [LICENSE](LICENSE).
