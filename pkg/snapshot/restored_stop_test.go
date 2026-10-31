package snapshot

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// fakeRestoredRecords is the node's restored-instance store as the stop rule
// reads it: one record, and every delete remembered.
type fakeRestoredRecords struct {
	record    runtime.RestoredInstance
	found     bool
	getErr    error
	deleteErr error

	deletes []string
}

func (f *fakeRestoredRecords) Get(taskID string) (runtime.RestoredInstance, bool, error) {
	if f.getErr != nil {
		return runtime.RestoredInstance{}, false, f.getErr
	}
	return f.record, f.found, nil
}

func (f *fakeRestoredRecords) Delete(taskID string) error {
	f.deletes = append(f.deletes, taskID)
	return f.deleteErr
}

// fakeRestoredStopper stands in for the manager that owns a restored VM's process.
type fakeRestoredStopper struct {
	err     error
	stopped []string
}

func (f *fakeRestoredStopper) StopRestored(taskID string) error {
	f.stopped = append(f.stopped, taskID)
	return f.err
}

// stopTestRecord is a record whose API socket lives under a short directory,
// because a Unix socket path is capped at ~104 bytes.
func stopTestRecord(t *testing.T) runtime.RestoredInstance {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "sc-restored-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return runtime.RestoredInstance{
		TaskID:     "new-task",
		VMID:       "source-task",
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(dir, "new-task.sock"),
		TapName:    runtime.TapName("source-task", 0),
		VsockPath:  runtime.VsockPath(dir, "source-task"),
		DiskPath:   filepath.Join(dir, "source-task.ext4"),
	}
}

// liveAPISocket publishes a Unix socket that answers the Firecracker liveness
// probe, which is what a restored VM's socket looks like while its process runs.
func liveAPISocket(t *testing.T, socketPath string) {
	t.Helper()

	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestStopAndForgetRestoredVMStopsAndDeletes pins the happy path: the VM is
// stopped through the manager that owns it and the record goes with it.
func TestStopAndForgetRestoredVMStopsAndDeletes(t *testing.T) {
	record := stopTestRecord(t)
	stopper := &fakeRestoredStopper{}
	records := &fakeRestoredRecords{record: record, found: true}

	require.NoError(t, StopAndForgetRestoredVM(stopper, records, record))
	assert.Equal(t, []string{"new-task"}, stopper.stopped)
	assert.Equal(t, []string{"new-task"}, records.deletes)
}

// TestStopAndForgetRestoredVMKeepsTheRecordWhenTheStopFails pins the retry
// contract: a stop that failed may have left the process alive, so its record is
// the only pointer to it and must survive. The failure is reported.
func TestStopAndForgetRestoredVMKeepsTheRecordWhenTheStopFails(t *testing.T) {
	record := stopTestRecord(t)
	stopper := &fakeRestoredStopper{err: errors.New("kill: operation not permitted")}
	records := &fakeRestoredRecords{record: record, found: true}

	err := StopAndForgetRestoredVM(stopper, records, record)
	require.Error(t, err)
	assert.ErrorContains(t, err, `"new-task"`)
	assert.ErrorContains(t, err, "record is kept")
	assert.Empty(t, records.deletes, "a failed stop keeps the record")
}

// TestStopAndForgetRestoredVMForgetsAVMThatIsGone pins the sentinel: the manager
// tracks no process and the recorded socket is gone, so the end state the caller
// asked for holds and the record is deleted.
func TestStopAndForgetRestoredVMForgetsAVMThatIsGone(t *testing.T) {
	record := stopTestRecord(t)
	stopper := &fakeRestoredStopper{
		err: fmt.Errorf("%w: task %q has no restored VM on this node", ErrNoRestoredVM, record.TaskID),
	}
	records := &fakeRestoredRecords{record: record, found: true}

	require.NoError(t, StopAndForgetRestoredVM(stopper, records, record))
	assert.Equal(t, []string{"new-task"}, records.deletes)
}

// TestStopAndForgetRestoredVMKeepsARecordWhoseSocketStillAnswers pins the daemon
// restart: the manager's process table is in memory, so "no such restored VM"
// does not mean the process is gone. A recorded socket that still answers proves
// a live Firecracker, so the record is the only pointer to it and must survive —
// with an error that says the VM has to be stopped by hand.
func TestStopAndForgetRestoredVMKeepsARecordWhoseSocketStillAnswers(t *testing.T) {
	record := stopTestRecord(t)
	liveAPISocket(t, record.SocketPath)
	stopper := &fakeRestoredStopper{err: ErrNoRestoredVM}
	records := &fakeRestoredRecords{record: record, found: true}

	err := StopAndForgetRestoredVM(stopper, records, record)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRestoredVMUntracked)
	assert.ErrorContains(t, err, "must be stopped manually")
	assert.Empty(t, records.deletes, "a live VM's record is the only pointer to its process")
}

// TestStopAndForgetRestoredVMReportsARecordItCannotDelete pins the last step: the
// VM is stopped, so the caller must learn that only the record cleanup failed.
func TestStopAndForgetRestoredVMReportsARecordItCannotDelete(t *testing.T) {
	record := stopTestRecord(t)
	stopper := &fakeRestoredStopper{}
	records := &fakeRestoredRecords{record: record, found: true, deleteErr: errors.New("read-only file system")}

	err := StopAndForgetRestoredVM(stopper, records, record)
	require.Error(t, err)
	assert.ErrorContains(t, err, "could not be deleted")
	assert.Equal(t, []string{"new-task"}, records.deletes)
}
