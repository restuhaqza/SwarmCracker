package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyControlClientFiles is the migration allowlist. It is empty: every
// command path now reaches the raw SwarmKit control surface through
// pkg/client/legacy. A new direct api.ControlClient reference must be routed
// through pkg/client instead of re-opening this list.
var legacyControlClientFiles = map[string]bool{}

func TestCommandControlClientBoundary(t *testing.T) {
	found := controlClientFiles(t, ".")

	assert.Equal(t, legacyControlClientFiles, found,
		"new api.ControlClient use is not allowed; route the path through pkg/client or add a reviewed migration entry")
}

func TestAPIPackageControlClientBoundary(t *testing.T) {
	found := controlClientFiles(t, filepath.Join("..", "..", "pkg", "api"))
	assert.Empty(t, found, "pkg/api must not import the SwarmKit control client")
}

func controlClientFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	found := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		if strings.Contains(string(data), "api.ControlClient") {
			found[entry.Name()] = true
		}
	}
	return found
}
