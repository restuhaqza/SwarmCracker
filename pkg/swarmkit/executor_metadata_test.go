//go:build !integration

package swarmkit

import (
	"os"
	"path/filepath"
	"testing"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestControllerWriteVMMetadata verifies the daemon records the guest IP next to
// the VM socket so the CLI can display it for service-managed VMs.
func TestControllerWriteVMMetadata(t *testing.T) {
	dir := t.TempDir()
	ctrl := &Controller{
		config: &Config{SocketDir: dir},
		logger: zerolog.Nop(),
	}

	task := &types.Task{
		ID: "task-abc",
		Networks: []types.NetworkAttachment{
			{
				Network:   types.Network{ID: "net-1"},
				Addresses: []string{"192.168.127.42/24"},
			},
		},
	}

	ctrl.writeVMMetadata(task)

	md, ok := scruntime.ReadVMMetadata(dir, "task-abc")
	require.True(t, ok, "metadata file should exist after writeVMMetadata")
	assert.Equal(t, "task-abc", md.ID)
	assert.Equal(t, "net-1", md.NetworkID)
	assert.Equal(t, []string{"192.168.127.42/24"}, md.IPAddresses)
}

// TestControllerWriteVMMetadata_MultiNetwork checks that the first network names
// the network while addresses from every attachment are collected.
func TestControllerWriteVMMetadata_MultiNetwork(t *testing.T) {
	dir := t.TempDir()
	ctrl := &Controller{
		config: &Config{SocketDir: dir},
		logger: zerolog.Nop(),
	}

	task := &types.Task{
		ID: "task-multi",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net-a"}, Addresses: []string{"10.0.0.2/24"}},
			{Network: types.Network{ID: "net-b"}, Addresses: []string{"10.1.0.2/24"}},
		},
	}

	ctrl.writeVMMetadata(task)

	md, ok := scruntime.ReadVMMetadata(dir, "task-multi")
	require.True(t, ok)
	assert.Equal(t, "net-a", md.NetworkID)
	assert.ElementsMatch(t, []string{"10.0.0.2/24", "10.1.0.2/24"}, md.IPAddresses)
}

// TestControllerWriteVMMetadata_NoSocketDir makes sure a missing socket dir is a
// silent no-op rather than a panic.
func TestControllerWriteVMMetadata_NoSocketDir(t *testing.T) {
	ctrl := &Controller{
		config: &Config{},
		logger: zerolog.Nop(),
	}
	assert.NotPanics(t, func() {
		ctrl.writeVMMetadata(&types.Task{ID: "task-1"})
	})
}

// TestCleanupVMSockets_RemovesMetadata ensures teardown drops the metadata file
// along with the sockets.
func TestCleanupVMSockets_RemovesMetadata(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, scruntime.WriteVMMetadata(dir, &scruntime.VMMetadata{
		ID:          "vm-1",
		IPAddresses: []string{"10.0.0.2/24"},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vm-1.sock"), []byte("x"), 0o644))

	mgr := &VMMManager{socketDir: dir, logger: zerolog.Nop()}
	mgr.cleanupVMSockets("vm-1")

	if _, ok := scruntime.ReadVMMetadata(dir, "vm-1"); ok {
		t.Error("metadata file should be removed by cleanupVMSockets")
	}
	if _, err := os.Stat(filepath.Join(dir, "vm-1.sock")); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed, stat err = %v", err)
	}
}
