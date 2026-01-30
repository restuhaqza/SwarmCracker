# Contributing to SwarmCracker

Thank you for your interest in contributing to SwarmCracker! This document provides guidelines and instructions for contributing.

## 📋 Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [Coding Standards](#coding-standards)
- [Testing Guidelines](#testing-guidelines)
- [Submitting Changes](#submitting-changes)

## 🤝 Code of Conduct

We are committed to providing a welcoming and inclusive environment. Please be respectful and constructive in all interactions.

## 🚀 Getting Started

### Prerequisites

- Go 1.21 or higher
- Linux with KVM support
- Firecracker v1.0.0+
- Docker (for running tests)
- Make

### Setup Development Environment

1. **Fork and clone the repository:**
   ```bash
   git clone https://github.com/YOUR_USERNAME/swarmcracker.git
   cd swarmcracker
   ```

2. **Install development tools:**
   ```bash
   make install-tools
   ```

3. **Download dependencies:**
   ```bash
   make deps
   ```

4. **Run tests to verify setup:**
   ```bash
   make test
   ```

## 🔄 Development Workflow

### Branch Strategy

- `main` - Stable production code
- `develop` - Integration branch for features
- `feature/*` - Feature branches
- `bugfix/*` - Bug fix branches
- `hotfix/*` - Urgent production fixes

### Creating a Feature Branch

```bash
git checkout -b feature/your-feature-name
```

### Making Changes

1. Write code following our [coding standards](#coding-standards)
2. Add tests for your changes
3. Ensure all tests pass: `make test`
4. Run linting: `make lint`
5. Format code: `make fmt`

### Commit Messages

Follow conventional commit format:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

**Types:**
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Test additions or changes
- `chore`: Build process or tooling changes

**Examples:**
```bash
git commit -m "feat(executor): add VM snapshot support"
git commit -m "fix(network): resolve TAP device cleanup issue"
git commit -m "docs(api): update executor interface documentation"
```

## 📝 Coding Standards

### Go Guidelines

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` for formatting
- Run `golangci-lint` before committing
- Write godoc comments for exported functions
- Keep functions small and focused

### Naming Conventions

- **Packages:** lowercase, single word when possible
- **Constants:** UPPER_SNAKE_CASE
- **Variables:** camelCase
- **Interfaces:** Usually -er suffix (e.g., `Executor`, `Translator`)
- **Errors:** Should start with lowercase (except proper nouns)

### Error Handling

```go
// Good
if err != nil {
    return fmt.Errorf("failed to start VM: %w", err)
}

// Bad
if err != nil {
    panic(err)
}
```

### Logging

Use structured logging with zerolog:

```go
log.Info().
    Str("task_id", t.ID).
    Str("vm_id", vm.ID).
    Msg("VM started successfully")

log.Error().
    Err(err).
    Str("task_id", t.ID).
    Msg("Failed to prepare task")
```

## 🧪 Testing Guidelines

### Unit Tests

- Write tests for all public functions
- Aim for >80% code coverage
- Use table-driven tests for multiple cases
- Mock external dependencies

Example:

```go
func TestTranslate(t *testing.T) {
    tests := []struct {
        name    string
        task    *api.Task
        want    interface{}
        wantErr bool
    }{
        {
            name: "simple container",
            task: &api.Task{
                Spec: &api.TaskSpec{
                    Runtime: &api.TaskSpec_Container{
                        Container: &api.Container{
                            Image: "nginx:latest",
                        },
                    },
                },
            },
            wantErr: false,
        },
        // Add more test cases
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := translator.Translate(tt.task)
            if (err != nil) != tt.wantErr {
                t.Errorf("Translate() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            // Add assertions
        })
    }
}
```

### Integration Tests

Place in `test/integration/`:

```go
// +build integration

package integration

import (
    "testing"
)

func TestVMLifecycle(t *testing.T) {
    // Integration test that actually starts a VM
}
```

Run with:
```bash
make integration-test
```

## 📤 Submitting Changes

### Pull Request Process

1. **Update documentation** if you've changed functionality
2. **Add tests** for your changes
3. **Ensure all tests pass**: `make test`
4. **Update CHANGELOG.md** (if applicable)
5. **Push to your fork** and create a pull request

### Pull Request Template

```markdown
## Description
Brief description of changes

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing
How was this tested?

## Checklist
- [ ] Code follows project style guidelines
- [ ] Self-review completed
- [ ] Comments added to complex code
- [ ] Documentation updated
- [ ] No new warnings generated
- [ ] Tests added/updated
- [ ] All tests pass
```

## 📧 Getting Help

- Open an issue for bugs or feature requests
- Start a discussion for questions
- Check existing documentation first

## 🙏 Recognition

Contributors will be acknowledged in the project documentation.

---

Thank you for contributing to SwarmCracker! 🔥
