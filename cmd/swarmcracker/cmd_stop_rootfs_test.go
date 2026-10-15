package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoveEphemeralRootfs covers the CLI teardown side of #69: `vm stop` frees
// the VM's private rootfs copy, and never a shared artifact — an image cache file
// or a golden image is not marked ephemeral.
func TestRemoveEphemeralRootfs(t *testing.T) {
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "task-1.ext4")
	require.NoError(t, os.WriteFile(copyPath, []byte("per-task copy"), 0644))
	sharedPath := filepath.Join(dir, "golden.ext4")
	require.NoError(t, os.WriteFile(sharedPath, []byte("golden artifact"), 0644))

	removeEphemeralRootfs(&runtime.VMState{ID: "task-1", RootfsPath: copyPath, RootfsEphemeral: true})
	assert.NoFileExists(t, copyPath, "the VM's private rootfs copy must be freed")

	removeEphemeralRootfs(&runtime.VMState{ID: "task-2", RootfsPath: sharedPath})
	assert.FileExists(t, sharedPath, "a shared artifact must survive")

	// A missing copy, an unset path and no state at all are no-ops.
	removeEphemeralRootfs(&runtime.VMState{ID: "task-3", RootfsPath: filepath.Join(dir, "gone.ext4"), RootfsEphemeral: true})
	removeEphemeralRootfs(&runtime.VMState{ID: "task-4", RootfsEphemeral: true})
	removeEphemeralRootfs(nil)
}
