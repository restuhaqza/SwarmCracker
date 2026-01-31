# File Management Guidelines

## ❌ Files to NEVER Commit

### Large Files (>10MB)
- **Tar archives:** `*.tar`, `*.tar.gz`, `*.tar.bz2`
- **Disk images:** `*.img`, `*.iso`, `*.qcow2`
- **Filesystems:** `*.ext4`, `*.ext3`, `*.xfs`
- **Rootfs archives:** `*.rootfs`, `rootfs.tar`
- **Database files:** `*.db`, `*.sqlite`, `*.sqlite3`

### Build Artifacts
- **Binaries:** `/swarmcracker`, `/swarmcracker-agent`, `*.bin`, `*.exe`
- **Coverage reports:** `coverage*.out`, `coverage*.html`, `*.coverprofile`
- **Test outputs:** `test/tmp/`, `test/temp/`

### Temporary/Runtime Files
- **Firecracker sockets:** `/var/run/firecracker/*.sock`
- **Firecracker state:** `/var/lib/firecracker/`
- **Image cache:** `/var/cache/swarmcracker/`
- **Working directories:** `/tmp/`, `*.tmp`

## ✅ What SHOULD Be Committed

### Source Code
- Go source files: `**/*.go`
- Package documentation: `**/*.md`
- Configuration examples: `examples/*.yaml`, `config.example.yaml`

### Assets
- Architecture diagrams: `docs/*.png`, `docs/*.svg`
- Small test data: `< 1MB`
- Documentation: `README.md`, `INSTALL.md`, etc.

### Configuration
- Build files: `Makefile`, `go.mod`, `go.sum`
- CI/CD: `.github/workflows/*.yml`
- Project config: `.gitignore`, `LICENSE`

## 🔍 Pre-Commit Checklist

Before pushing, run:

```bash
# Check for large files
find . -type f -size +10M -not -path "./.git/*" -not -path "./build/*"

# Check current status
git status

# Review what will be pushed
git diff --cached --stat

# Check for binary files
git diff --cached --name-only | xargs file | grep -E "(executable|binary)"
```

## 🚨 Emergency Cleanup

If you accidentally commit a large file:

```bash
# Remove from git cache (NOT local filesystem)
git rm --cached <large-file>

# Amend the commit
git commit --amend --no-edit

# Force push (CAUTION: rewrites history)
git push origin main --force
```

## 📝 Current .gitignore Rules

The `.gitignore` file now blocks:
- ✅ All binaries (`*.exe`, `*.bin`, `/swarmcracker*`)
- ✅ Coverage files (`coverage*.out`, `*.html`)
- ✅ Image artifacts (`pkg/image/*.tar`, `*.rootfs`, `*.ext4`)
- ✅ Build directories (`/build/`, `/dist/`)
- ✅ IDE files (`.idea/`, `.vscode/`)
- ✅ OS files (`.DS_Store`, `Thumbs.db`)

## 💡 Best Practices

1. **Never test with large production data** - use small test datasets
2. **Generate large files at runtime** - don't store them in repo
3. **Use git-lfs for large binaries** - if absolutely necessary
4. **Review git status before committing** - catch mistakes early
5. **Use `.gitignore` proactively** - add patterns before creating files

---

**Rule of Thumb:** If it's larger than 1MB and not source code/documentation, it probably shouldn't be in git.
