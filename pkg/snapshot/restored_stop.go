package snapshot

import (
	"errors"
	"fmt"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// ErrRestoredVMUntracked means this daemon tracks no process for a restored VM
// whose Firecracker API socket still answers: the daemon that started the VM has
// restarted, and the in-memory process table went with it. The VM is running and
// nothing on this node can signal it, so it has to be stopped by hand.
var ErrRestoredVMUntracked = errors.New("the restored VM is not tracked by this daemon")

// restoredSocketProbeTimeout bounds the liveness probe of a recorded API socket
// when a stop asks whether the VM is really gone. A live Firecracker answers in
// microseconds; the timeout only covers a wedged process.
const restoredSocketProbeTimeout = 500 * time.Millisecond

// RestoredRecordStore is the node's record of the VM a task inherited from a
// migration: *runtime.RestoredRegistry is the production implementation. A record
// that exists but cannot be used is (zero, false, err), never a silent "no record".
type RestoredRecordStore interface {
	// Get answers (record, true, nil) for a task this node restored,
	// (zero, false, nil) for one it never did, and an error for a record it holds
	// but cannot use.
	Get(taskID string) (runtime.RestoredInstance, bool, error)
	// Delete forgets a task's record. Deleting a missing record is success: a stop
	// can run twice.
	Delete(taskID string) error
}

// RestoredStopper stops the Firecracker process a restore started for a task.
// *Manager is the production implementation: only the manager that started the
// process holds its handle, and it answers ErrNoRestoredVM for a task it tracks
// none for.
type RestoredStopper interface {
	StopRestored(taskID string) error
}

// StopAndForgetRestoredVM ends the restored VM the record names and deletes the
// record that pointed at it. It is the one rule both stop paths use — the
// executor's teardown and the node broker's DELETE /v1/tasks/{id}/restored — so
// the two cannot disagree about whether a record survives a stop.
//
// The record is the only pointer to the VM's process, so it is deleted only when
// the VM is confirmed gone:
//
//   - the stop succeeded: the record goes;
//   - the manager tracks no process for the task (ErrNoRestoredVM) *and* the
//     recorded API socket no longer answers: the VM is gone, the record goes;
//   - the manager tracks no process but the recorded socket still answers: the
//     daemon restarted and lost the process table, so the VM is running and this
//     error wraps ErrRestoredVMUntracked. The record stays — deleting it would
//     destroy the only pointer to a live Firecracker — and the message says the VM
//     must be stopped manually;
//   - the stop failed for any other reason: the record stays so a retry can still
//     reach the process.
//
// A stop that succeeded but whose record could not be deleted is reported: the VM
// is gone, only the pointer remains.
func StopAndForgetRestoredVM(stopper RestoredStopper, records RestoredRecordStore, record runtime.RestoredInstance) error {
	taskID := record.TaskID

	stopErr := stopper.StopRestored(taskID)
	switch {
	case stopErr == nil:
		// The VM is confirmed gone.
	case errors.Is(stopErr, ErrNoRestoredVM):
		// "Not tracked" is not the same as "gone": the process table lives in the
		// daemon's memory, so a daemon restart makes every restored VM look
		// untracked while it keeps serving. The recorded socket is the evidence
		// that outlives the daemon.
		if runtime.IsVMSocketAlive(record.SocketPath, restoredSocketProbeTimeout) {
			return fmt.Errorf("%w: task %q: the VM is still running on this node but this daemon no longer tracks its process (it restarted), so it must be stopped manually; the record is kept", ErrRestoredVMUntracked, taskID)
		}
	default:
		return fmt.Errorf("task %q: the restored VM was not stopped, so its record is kept for a retry: %w", taskID, stopErr)
	}

	if err := records.Delete(taskID); err != nil {
		return fmt.Errorf("task %q: the restored VM is stopped but its record could not be deleted: %w", taskID, err)
	}
	return nil
}
