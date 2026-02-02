#!/bin/bash
# Setup pre-commit hooks for SwarmCracker

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HOOKS_DIR="$REPO_ROOT/.githooks"

echo "🔧 Setting up pre-commit hooks..."

# Create .githooks directory if it doesn't exist
mkdir -p "$HOOKS_DIR"

# Copy pre-commit hook
if [ -f "$REPO_ROOT/.git/hooks/pre-commit" ]; then
    echo "✅ Existing pre-commit hook found, backing up..."
    cp "$REPO_ROOT/.git/hooks/pre-commit" "$REPO_ROOT/.git/hooks/pre-commit.backup.$(date +%s)"
fi

# Copy to .githooks for version control
cp "$REPO_ROOT/.git/hooks/pre-commit" "$HOOKS_DIR/pre-commit"
chmod +x "$HOOKS_DIR/pre-commit"

# Configure git to use .githooks directory
cd "$REPO_ROOT"
git config core.hooksPath .githooks

echo ""
echo "✅ Pre-commit hooks installed!"
echo ""
echo "📝 What's protected:"
echo "  • SSH private keys"
echo "  • API keys and tokens"
echo "  • Passwords"
echo "  • .env files"
echo "  • Certificate files"
echo "  • Vagrant artifacts"
echo ""
echo "🔍 The hook will automatically:"
echo "  • Scan for secret patterns"
echo "  • Block sensitive file types"
echo "  • Warn about forbidden paths"
echo ""
echo "⚠️  To bypass (not recommended):"
echo "  git commit --no-verify"
echo ""
echo "✅ Done! Hooks are now active."
