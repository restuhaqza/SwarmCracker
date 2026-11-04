package swarmkit

// What a cold start publishes, and when.
//
// SBX-011 is not "the record is written"; it is "the record is written *first*".
// A node that records a VM only after its API answers has nothing to clean up
// when the boot fails, and nothing to resolve while it is still booting. These
// tests hold the start path to that order: the record is in the store while the
// VM is being configured, it says it is still booting, and it survives a start
// that never completes.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// shortVMDir returns a short directory for VM sockets: a Unix socket path is
// limited to roughly 104 bytes, which t.TempDir() can exceed.
func shortVMDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "sc-vmm-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// recordingStore is the node registry as a start sees it. Alive is pinned to
// true because a failed start kills its process: the record file is what the
// invariant is about, and the test must still be able to read it back.
func recordingStore(t *testing.T) *vminstance.Store {
	t.Helper()

	store, err := vminstance.NewStore(vminstance.Config{
		Dir:   vminstance.Dir(t.TempDir()),
		Alive: func(int) bool { return true },
	})
	require.NoError(t, err)
	return store
}

// sleepyFirecracker is a binary that starts and then does nothing: it never
// creates the API socket and never serves it, which is a boot that does not
// complete.
func sleepyFirecracker(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "firecracker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755))
	return path
}

// startRecordConfig is the Firecracker configuration a start consumes: enough
// sections that the API path reaches the /actions call.
func startRecordConfig(t *testing.T) map[string]interface{} {
	t.Helper()

	var cfg map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"machine-config": {"vcpu_count": 1, "mem_size_mib": 128},
		"boot-source": {"kernel_image_path": "/kernel", "boot_args": "console=ttyS0"},
		"drives": [{"drive_id": "rootfs", "path_on_host": "/rootfs.ext4", "is_root_device": true, "is_read_only": false}],
		"vsock": {"vsock_id": "vsock0", "guest_cid": 3}
	}`), &cfg))
	return cfg
}

// TestStartRecordsTheInstanceBeforeItWaitsForTheGuest pins the ordering against a
// boot that never finishes: the process exists, the API never answers, and the
// record is already there with the paths a teardown needs.
func TestStartRecordsTheInstanceBeforeItWaitsForTheGuest(t *testing.T) {
	socketDir := shortVMDir(t)
	store := recordingStore(t)

	manager, err := NewVMMManagerWithConfig(&VMMManagerConfig{
		FirecrackerPath: sleepyFirecracker(t),
		SocketDir:       socketDir,
		Instances:       store,
	})
	require.NoError(t, err)

	task := &types.Task{ID: "task-cold-fail", ServiceID: "svc-1", NodeID: "node-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	require.Error(t, manager.Start(ctx, task, startRecordConfig(t)),
		"the fake VMM never creates its API socket, so the start must fail")

	record, found, err := store.Get(task.ID)
	require.NoError(t, err)
	require.True(t, found, "a VM whose boot failed must still have a record for the teardown that follows")
	assert.Equal(t, vminstance.KindTask, record.Kind)
	assert.Equal(t, "svc-1", record.OwnerID)
	assert.Equal(t, scruntime.SocketPath(socketDir, task.ID), record.APISocket)
	assert.Equal(t, scruntime.VsockPath(socketDir, task.ID), record.VsockPath)
	assert.Equal(t, filepath.Join(socketDir, task.ID+scruntime.ConsoleSocketSuffix), record.Console)
	assert.Equal(t, filepath.Join(socketDir, task.ID+scruntime.VMMetadataSuffix), record.Metadata,
		"the record names the sidecar the executor publishes, so health asks instead of rebuilding the name")
	assert.Positive(t, record.PID)
	assert.False(t, record.Ready, "the VM never answered, so it is still booting")
}

// TestStartRecordsTheInstanceBeforeTheVMBoots pins the same order from the other
// side: at the moment Firecracker is told to start, the record is already in the
// store and still says the VM is booting. It flips to ready only after the start
// returns.
func TestStartRecordsTheInstanceBeforeTheVMBoots(t *testing.T) {
	socketDir := shortVMDir(t)
	store := recordingStore(t)
	socketPath := scruntime.SocketPath(socketDir, "task-cold-ok")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	// What the store held when the VM was told to start. The channel carries it
	// out of the handler without a data race.
	atStart := make(chan vminstance.Instance, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/actions" {
			// A read failure is reported as "no record": the assertion that
			// matters happens in the test goroutine, not in this handler.
			record, found, getErr := store.Get("task-cold-ok")
			if getErr != nil || !found {
				record = vminstance.Instance{}
			}
			atStart <- record
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	manager, err := NewVMMManagerWithConfig(&VMMManagerConfig{
		FirecrackerPath: sleepyFirecracker(t),
		SocketDir:       socketDir,
		Instances:       store,
	})
	require.NoError(t, err)

	task := &types.Task{ID: "task-cold-ok", ServiceID: "svc-1", NodeID: "node-1"}
	require.NoError(t, manager.Start(context.Background(), task, startRecordConfig(t)))
	t.Cleanup(func() { _ = manager.Stop(context.Background(), task) })

	during := <-atStart
	require.NotEmpty(t, during.ID,
		"the record must exist when Firecracker is told to start, not after it answers")
	assert.False(t, during.Ready, "the VM is still booting at that point")

	record, found, err := store.Get(task.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.Ready, "a configured VM is marked ready")
}

// TestStartFailsWhenTheRegistryCannotTakeTheRecord pins the consequence of the
// order: a caller that keeps a registry does not run a VM it cannot record, so
// the start fails and the process is not left behind.
func TestStartFailsWhenTheRegistryCannotTakeTheRecord(t *testing.T) {
	socketDir := shortVMDir(t)

	manager, err := NewVMMManagerWithConfig(&VMMManagerConfig{
		FirecrackerPath: sleepyFirecracker(t),
		SocketDir:       socketDir,
		Instances:       refusingRecorder{},
	})
	require.NoError(t, err)

	task := &types.Task{ID: "task-cold-refused", ServiceID: "svc-1", NodeID: "node-1"}
	err = manager.Start(context.Background(), task, startRecordConfig(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to start VM")
	assert.Contains(t, err.Error(), "instance registry could not record it")
	assert.Contains(t, err.Error(), "permission denied", "the cause is carried through, not swallowed")

	time.Sleep(50 * time.Millisecond)
	_, statErr := os.Stat(scruntime.SocketPath(socketDir, task.ID))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a VM that could not be recorded must not be left running")
}

// refusingRecorder is a registry that rejects every write: the shape of a full
// disk, or a store that was closed under the start.
type refusingRecorder struct{}

func (refusingRecorder) Put(vminstance.Instance) error {
	return os.ErrPermission
}
