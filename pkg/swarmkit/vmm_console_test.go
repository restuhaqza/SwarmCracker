package swarmkit

import (
	"os"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortSocketDir returns a short temporary directory. Unix socket paths are
// capped (~108 bytes on Linux) and t.TempDir() can approach that limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sc-vmm-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestVMMManagerConsoleLifecycle verifies the console server is tracked while a
// VM runs and torn down (socket removed) when the VM is forgotten.
func TestVMMManagerConsoleLifecycle(t *testing.T) {
	dir := shortSocketDir(t)
	vmm := &VMMManager{socketDir: dir, logger: zerolog.Nop()}

	srv, err := console.New(console.Config{SocketDir: dir, TaskID: "vm-x", Logger: &vmm.logger})
	require.NoError(t, err)

	vmm.setConsole("vm-x", srv)
	_, err = os.Stat(console.SocketPath(dir, "vm-x"))
	require.NoError(t, err, "console socket should exist while the VM is tracked")

	// forgetProcess is used by Stop/ForceStop/Remove and must tear the console
	// down too.
	vmm.forgetProcess("vm-x")
	_, err = os.Stat(console.SocketPath(dir, "vm-x"))
	assert.True(t, os.IsNotExist(err), "console socket should be removed on cleanup")

	// Closing an unknown task is a no-op.
	assert.NotPanics(t, func() { vmm.closeConsole("does-not-exist") })
}

// TestVMMManagerRemoveProcessClosesConsole verifies RemoveProcess also drops the
// console rather than leaking the socket.
func TestVMMManagerRemoveProcessClosesConsole(t *testing.T) {
	dir := shortSocketDir(t)
	vmm := &VMMManager{socketDir: dir, logger: zerolog.Nop()}

	srv, err := console.New(console.Config{SocketDir: dir, TaskID: "vm-y"})
	require.NoError(t, err)
	vmm.setConsole("vm-y", srv)

	vmm.RemoveProcess("vm-y")

	_, err = os.Stat(console.SocketPath(dir, "vm-y"))
	assert.True(t, os.IsNotExist(err), "console socket should be removed by RemoveProcess")
}
