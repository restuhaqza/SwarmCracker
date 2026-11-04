package network

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoveNetNSRemovesAPath pins the ordinary path: the namespace file the
// record names is removed.
func TestRemoveNetNSRemovesAPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sbx-1")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	require.NoError(t, RemoveNetNS(path))

	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestRemoveNetNSTreatsAGoneNamespaceAsSuccess pins the idempotency a teardown
// needs: a retried cleanup, and a VM that died outside a stop path, reach here with
// the namespace already gone, and that is what removal wanted.
func TestRemoveNetNSTreatsAGoneNamespaceAsSuccess(t *testing.T) {
	require.NoError(t, RemoveNetNS(filepath.Join(t.TempDir(), "absent")))
}

// TestRemoveNetNSRequiresAPath pins the one refusal: an empty path is not a
// namespace, and unmounting or removing "" would address the working directory.
func TestRemoveNetNSRequiresAPath(t *testing.T) {
	require.Error(t, RemoveNetNS("  "))
}
