package main

// The registry a node opens at startup.
//
// SBX-011 writes a record on every cold start; the daemon's load-time pass is the
// other half of that contract. It reports what a previous daemon life left behind
// and removes only the half-written temporaries a crash leaves — a stale record
// stays, because it is the only handle to the netns, TAP device and disk of a VM
// that died outside a stop path.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// TestLogInstanceRegistryKeepsStaleRecords pins the ruling: reporting a stale
// record must not delete it, or the teardown that still has to free its
// resources loses its only pointer.
func TestLogInstanceRegistryKeepsStaleRecords(t *testing.T) {
	registryDir := vminstance.Dir(t.TempDir())
	socketPath := filepath.Join(t.TempDir(), "task-1.sock")
	require.NoError(t, os.WriteFile(socketPath, nil, 0o600))

	// A record written while its process was alive: the store refuses a record
	// that already names a dead PID, which is the half the scan is about.
	live, err := vminstance.NewStore(vminstance.Config{
		Dir:   registryDir,
		Alive: func(int) bool { return true },
	})
	require.NoError(t, err)
	require.NoError(t, live.Put(vminstance.Instance{
		ID:        "task-1",
		Kind:      vminstance.KindTask,
		PID:       4242,
		APISocket: socketPath,
		Booted:    time.Now().UTC(),
	}))

	// The daemon restarts and finds that process gone.
	dead, err := vminstance.NewStore(vminstance.Config{
		Dir:   registryDir,
		Alive: func(int) bool { return false },
	})
	require.NoError(t, err)

	logInstanceRegistry(context.Background(), dead)

	_, statErr := os.Stat(filepath.Join(registryDir, "task-1.json"))
	assert.NoError(t, statErr, "a stale record is left in place for the teardown that follows")
}

// TestLogInstanceRegistryToleratesNoStore pins the daemon's test-only shape: a
// node that never opened a registry must not panic on the startup pass.
func TestLogInstanceRegistryToleratesNoStore(t *testing.T) {
	logInstanceRegistry(context.Background(), nil)
}
