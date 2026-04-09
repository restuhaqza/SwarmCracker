# Secret Prevention Guide

SwarmCracker uses **pre-commit hooks** to prevent secrets from being committed to the repository.

## What's Protected

The pre-commit hook detects and blocks:

### Secrets & Credentials
- 🔑 SSH private keys (RSA, ED25519)
- 🔑 API keys (GitHub, AWS, etc.)
- 🔑 Authentication tokens
- 🔑 Passwords in code
- 🔑 Certificate files (.pem, .key, .csr)

### Sensitive Files
- 📁 .env files (any variant)
- 📁 Vagrant artifacts (.vagrant/)
- 📁 IDE config (.DS_Store, Thumbs.db)
- 📁 History files (.*_history)
- 📁 Compiled binaries

### Patterns Detected

```
password.*=.*"..."
api[_-]?key.*=.*"..."
secret[_-]?key.*=.*"..."
token.*=.*"..."
private[_-]?key.*=.*"..."
BEGIN.*PRIVATE KEY
ssh-rsa.* AAAA
ghp_[a-zA-Z0-9]{36}
AKIA[0-9A-Z]{16}
```

## Installation

### Automatic Setup

```bash
./scripts/install-hooks.sh
```

### Manual Setup

```bash
# Copy hook to .githooks directory
mkdir -p .githooks
cp .git/hooks/pre-commit .githooks/

# Configure git to use .githooks
git config core.hooksPath .githooks
```

## Usage

### Normal Commit

```bash
git add .
git commit -m "feat: add new feature"
# ✅ Pre-commit checks run automatically
```

### If Secret Detected

```bash
git add secret_file.go
git commit -m "add secret"
# ❌ PRE-COMMIT CHECKS FAILED
# ❌ SECRET DETECTED in secret_file.go
# ❌ Commit aborted
```

### Bypass (Not Recommended)

```bash
git commit --no-verify -m "bypass hooks"
# ⚠️  Skips all checks
```

## .gitignore

Additional patterns in `.gitignore`:

```
# Secrets
*.pem
*.key
*.env
*.env.*
credentials
secrets
.vagrant/

# SSH keys
id_rsa*
id_ed25519*
```

## Testing

Test the hooks work:

```bash
# Create a test file with a fake secret
echo 'password = "supersecret123"' > test.txt

# Try to commit
git add test.txt
git commit -m "test: should fail"

# Should see:
# ❌ SECRET DETECTED in test.txt
# ❌ PRE-COMMIT CHECKS FAILED
```

## Best Practices

1. **Never bypass hooks** unless absolutely necessary
2. **Review changes** before committing
3. **Use environment variables** for secrets
4. **Commit .env.example** (not .env)
5. **Rotate secrets** if accidentally exposed
6. **Update .gitignore** when new sensitive files appear

## Troubleshooting

### Hook Not Running

```bash
# Check if hooks are configured
git config core.hooksPath
# Should output: .githooks

# If not set, run:
git config core.hooksPath .githooks
```

### Hook Too Strict

If the hook is blocking legitimate commits:

1. Review the pre-commit hook: `.githooks/pre-commit`
2. Adjust patterns or add exceptions
3. Test changes before committing

### False Positives

If you get false positives:

1. Check if the pattern matches legitimate code
2. Use environment variables instead
3. Add exception in pre-commit hook if needed

## Related Files

- `.githooks/pre-commit` - Main hook script
- `.gitignore` - Files to ignore
- `.gitsecrets_config` - Pattern reference
- `scripts/install-hooks.sh` - Installation script

## Resources

- [Git Hooks Documentation](https://git-scm.com/docs/githooks)
- [git-secrets](https://github.com/awslabs/git-secrets) - Alternative tool
- [truffleHog](https://github.com/trufflesecurity/trufflehog) - Secret scanner

---

**Remember:** Once a secret is committed, it's in git history forever. Prevention is better than cure!
