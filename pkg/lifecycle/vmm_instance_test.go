package lifecycle

// What `vm create` publishes, and when.
//
// The CLI starts VMs through this manager, so it owes the node the same record
// the daemon writes: the VM's identity in the registry before the guest is
// reachable. These tests pin the order on the real start path — at the moment
// Firecracker is told to start, the record is already there and still says the VM
// is booting — and that a failed configuration keeps it.

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

	dir, err := os.MkdirTemp("", "sc-lc-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// recordingStore is the node registry as a start sees it. Alive is pinned to true
// because the record is what the test reads back, not the process.
func recordingStore(t *testing.T) *vminstance.Store {
	t.Helper()

	store, err := vminstance.NewStore(vminstance.Config{
		Dir:   vminstance.Dir(t.TempDir()),
		Alive: func(int) bool { return true },
	})
	require.NoError(t, err)
	return store
}

// fakeFirecracker is a binary that starts, drops a marker, and then does nothing.
//
// The marker is what makes the test deterministic: the manager unlinks the task's
// socket before it starts the process, so the test's API server can only bind
// after that unlink has happened. Waiting for the marker puts the bind after it.
func fakeFirecracker(t *testing.T) (marker string) {
	t.Helper()

	dir := t.TempDir()
	marker = filepath.Join(dir, "started")
	// The script keeps the caller's PATH: it needs touch and sleep itself, while
	// its directory goes first so LookPath finds this fake before any real one.
	script := "#!/bin/sh\ntouch " + marker + "\nexec sleep 30\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "firecracker"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// waitForFile blocks until path exists, so a test can bind its API server after
// the manager has cleared the socket path.
func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was never created", path)
}

// serveVMAPI answers the manager's readiness probe and every configuration call
// on socketPath. configure decides how those calls are answered.
func serveVMAPI(t *testing.T, socketPath string, configure func(w http.ResponseWriter, r *http.Request)) {
	t.Helper()

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		configure(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
}

// startRecordConfig is the Firecracker configuration a `vm create` start
// consumes, in the shape the translator produces.
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

// killRecordedPID ends the fake VMM a test started.
func killRecordedPID(store *vminstance.Store, taskID string) {
	record, found, err := store.Get(taskID)
	if err != nil || !found || record.PID <= 0 {
		return
	}
	if proc, findErr := os.FindProcess(record.PID); findErr == nil {
		_ = proc.Kill()
	}
}

// startVM runs Start in the background, binds the test's API server once the VMM
// process exists, and returns the start's result. The bind has to wait for the
// marker because Start unlinks the task's socket first.
func startVM(t *testing.T, vm *VMMManager, task *types.Task, marker, socketPath string,
	configure func(w http.ResponseWriter, r *http.Request),
) error {
	t.Helper()

	result := make(chan error, 1)
	go func() {
		result <- vm.Start(context.Background(), task, startRecordConfig(t))
	}()

	waitForFile(t, marker)
	serveVMAPI(t, socketPath, configure)

	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return")
		return nil
	}
}

// TestStartedVMIsRecordedBeforeItBoots pins the record a `vm create` leaves: it is
// in the store when Firecracker is told to start, it names the task's own socket
// and muxer, and it is marked ready once the start returns.
func TestStartedVMIsRecordedBeforeItBoots(t *testing.T) {
	marker := fakeFirecracker(t)
	socketDir := shortVMDir(t)
	store := recordingStore(t)
	socketPath := scruntime.SocketPath(socketDir, "task-cli-cold")

	// What the store held when the VM was told to start. The channel carries it
	// out of the handler without a data race.
	atStart := make(chan vminstance.Instance, 1)
	configure := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/actions" {
			// A read failure is reported as "no record": the assertion that
			// matters happens in the test goroutine, not in this handler.
			record, found, getErr := store.Get("task-cli-cold")
			if getErr != nil || !found {
				record = vminstance.Instance{}
			}
			atStart <- record
		}
		w.WriteHeader(http.StatusNoContent)
	}

	vm := NewVMMManager(&ManagerConfig{SocketDir: socketDir, Instances: store}).(*VMMManager)
	task := &types.Task{
		ID:          "task-cli-cold",
		ServiceID:   "service-cli",
		NodeID:      "node-local",
		Annotations: map[string]string{},
	}
	require.NoError(t, startVM(t, vm, task, marker, socketPath, configure))
	// Kill the fake VMM directly: Stop waits out the manager's shutdown window
	// against the test's own API server, which is not what this test is about.
	t.Cleanup(func() { killRecordedPID(store, task.ID) })

	during := <-atStart
	require.NotEmpty(t, during.ID,
		"the record must exist when Firecracker is told to start, not after it answers")
	assert.False(t, during.Ready, "the VM is still booting at that point")

	record, found, err := store.Get(task.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, vminstance.KindTask, record.Kind)
	assert.Equal(t, scruntime.SocketPath(socketDir, task.ID), record.APISocket)
	assert.Equal(t, scruntime.VsockPath(socketDir, task.ID), record.VsockPath)
	assert.Positive(t, record.PID)
	assert.True(t, record.Ready, "a configured VM is marked ready")
}

// TestStartKeepsItsRecordWhenTheVMCannotBeConfigured pins the failure side for the
// CLI path: the record is written before the API is asked anything, so a failed
// configuration leaves it behind for the teardown.
func TestStartKeepsItsRecordWhenTheVMCannotBeConfigured(t *testing.T) {
	marker := fakeFirecracker(t)
	socketDir := shortVMDir(t)
	store := recordingStore(t)
	socketPath := scruntime.SocketPath(socketDir, "task-cli-broken")

	refuse := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}

	vm := NewVMMManager(&ManagerConfig{SocketDir: socketDir, Instances: store}).(*VMMManager)
	task := &types.Task{ID: "task-cli-broken", ServiceID: "service-cli", NodeID: "node-local"}

	require.Error(t, startVM(t, vm, task, marker, socketPath, refuse))

	record, found, err := store.Get(task.ID)
	require.NoError(t, err)
	require.True(t, found, "a VM whose configuration failed must still have a record")
	assert.False(t, record.Ready)
}

// refusingRecorder is a registry that rejects every write: the shape of a full
// disk or a directory that lost its permissions.
type refusingRecorder struct{}

func (refusingRecorder) Put(vminstance.Instance) error { return os.ErrPermission }

// The reads answer "no record": the start fails before one exists, so a teardown
// has nothing to find.
func (refusingRecorder) Get(string) (vminstance.Instance, bool, error) {
	return vminstance.Instance{}, false, nil
}

func (refusingRecorder) Peek(string) (vminstance.Instance, bool, error) {
	return vminstance.Instance{}, false, nil
}

func (refusingRecorder) Delete(string) error { return nil }

// TestStartIsRefusedWhenTheRegistryCannotTakeTheRecord pins the CLI-side
// consequence and its wording: the VM does not run, and the error says the
// registry could not record it — not that Firecracker failed.
func TestStartIsRefusedWhenTheRegistryCannotTakeTheRecord(t *testing.T) {
	fakeFirecracker(t)
	socketDir := shortVMDir(t)

	vm := NewVMMManager(&ManagerConfig{SocketDir: socketDir, Instances: refusingRecorder{}}).(*VMMManager)
	task := &types.Task{ID: "task-cli-refused", ServiceID: "service-cli", NodeID: "node-local"}

	err := vm.Start(context.Background(), task, startRecordConfig(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to start VM")
	assert.Contains(t, err.Error(), "instance registry could not record it")
	assert.Contains(t, err.Error(), "permission denied")
}

// TestRemoveTearsDownAFailedStartsRecord pins the cleanup half of SBX-015 for the
// CLI: a start that failed after it published its record leaves no VM entry, and
// Remove used to return without looking. The record is the only handle to the disk
// and devices the failed process left behind, so removing an untracked VM is a
// teardown.
func TestRemoveTearsDownAFailedStartsRecord(t *testing.T) {
	socketDir := shortVMDir(t)

	alive := true
	store, err := vminstance.NewStore(vminstance.Config{
		Dir:   vminstance.Dir(t.TempDir()),
		Alive: func(int) bool { return alive },
	})
	require.NoError(t, err)

	disk := filepath.Join(t.TempDir(), "task-failed.ext4")
	require.NoError(t, os.WriteFile(disk, []byte("rootfs"), 0o600))
	require.NoError(t, store.Put(vminstance.Instance{
		ID:        "task-failed",
		Kind:      vminstance.KindTask,
		PID:       os.Getpid(),
		APISocket: scruntime.SocketPath(socketDir, "task-failed"),
		Disk:      disk,
		Booted:    time.Now().UTC(),
	}))
	alive = false // the failed start killed its process

	vm := NewVMMManager(&ManagerConfig{SocketDir: socketDir, Instances: store}).(*VMMManager)

	require.NoError(t, vm.Remove(context.Background(), &types.Task{ID: "task-failed"}))

	_, found, peekErr := store.Peek("task-failed")
	require.NoError(t, peekErr)
	assert.False(t, found, "the failed start's record is deleted once its resources are free")

	_, statErr := os.Stat(disk)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the disk the record named is freed")
}
