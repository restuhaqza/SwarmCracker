package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyControlClientFiles is the migration allowlist. Every entry is a
// command file that still uses the raw SwarmKit client for a legacy path.
// A file must be removed from this list when it is migrated; the test also
// fails if a migrated file still appears, so the allowlist cannot go stale
// silently.
var legacyControlClientFiles = map[string]bool{
	"cmd_config.go":        true,
	"cmd_network_admin.go": true,
	"cmd_node.go":          true,
	"cmd_secret.go":        true,
	"cmd_service.go":       true,
	"cmd_task.go":          true,
	"resolve.go":           true,
	"task_health.go":       true,
}

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
