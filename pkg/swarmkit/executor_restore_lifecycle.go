package swarmkit

// This file owns the third state a restored task can be in: a restore is running.
//
// A restore is the one moment a task's VM exists and nothing in the node says so. The
// record that maps the task to its process is written by the restore's last step, and
// the restore itself runs outside c.mu so a teardown is never stalled by a load and a
// guest-agent wait. The two taken together are what a teardown must not be allowed to
// misread: at the instant a load is resuming a guest, the record store answers "no
// restored VM for this task" and c.started is false, so today's stop path would report
// "nothing to shut down" — and a few hundred milliseconds later the restore would
// publish a running VM for a task SwarmKit has already replaced, next to the cold
// replacement it just scheduled.
//
// The state below makes the pair observable to each other, without either of them
// holding a lock the other needs:
//
//	restoring     a restore is in flight (set before the first effect, cleared after
//	              the last one, including the abort's unwind)
//	restoreStopped a teardown asked this task to stop
//	restoreDone   closed when the restore in flight finishes; nil when there is none
//
// The rules are the two ends of one handshake:
//
//   - the start path publishes "restoring", and checks "stopped" between every long
//     step (migration.Restore's Checkpoint), so a stop that arrived aborts the restore
//     and unwinds the VM, the TAP device and the record;
//   - the teardown that finds "restoring" publishes "stopped" and waits for the
//     restore to finish, so the VM is either gone (aborted) or recorded (finished) by
//     the time it looks the task up. There is no instant in which it finds neither.
//
// The stop marker is not cleared: a restore for a task that was already stopped must
// not run, and SwarmKit starts a replacement as a new task, which gets a new
// controller. beginRestore is what refuses it — and the directive goes back, so the
// replacement restores the same artifact.
//
// The restore runs under a context this controller can cancel, so a step that has no
// checkpoint near it (the guest-agent wait, a guest command) ends when the teardown does
// rather than at its own timeout; the abort still unwinds through the checkpoint at the
// next step boundary.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// errTaskStoppedBeforeRestore is the abort a task start reports when its VM was asked
// to stop before the restore began. It is not a restore failure: nothing was created,
// and the directive goes back for the replacement task.
var errTaskStoppedBeforeRestore = errors.New(
	"the task was stopped before its artifact could be restored, so no VM was started")

// errRestoreStopped is the cause migration.Restore reports when a checkpoint says the
// task was stopped. The restore's own undo runs on it, exactly as on any other failure.
var errRestoreStopped = errors.New(
	"the task was stopped while its artifact was being restored")

// beginRestore publishes a restore in flight, or refuses one for a task that was
// already stopped. Refusing is the answer that keeps the two halves ordered when a
// teardown arrives first: without it, a stop that ran between the directive lookup and
// the load would be forgotten, and this start would publish a VM for a stopped task.
//
// It also derives the restore's context from the start's: a teardown that arrives during
// a step with no checkpoint nearby — the 30-second guest-agent wait is one — ends that
// step instead of waiting for it, and the restore then unwinds on its next boundary.
func (c *Controller) beginRestore(parent context.Context) (context.Context, error) {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()

	if c.restoreStopped {
		return nil, errTaskStoppedBeforeRestore
	}
	if c.restoring {
		// One restore per controller: a second Start is the task manager repeating
		// itself, and a VM is already being loaded for this task.
		return nil, errRestoreStopped
	}
	ctx, cancel := context.WithCancel(parent)
	c.restoring = true
	c.restoreCancel = cancel
	c.restoreDone = make(chan struct{})
	return ctx, nil
}

// endRestore closes the restore out, waking any teardown waiting on it and releasing the
// restore's context. It is safe to call when no restore is in flight, so the start path
// can defer it unconditionally.
func (c *Controller) endRestore() {
	c.restoreMu.Lock()
	if !c.restoring {
		c.restoreMu.Unlock()
		return
	}
	c.restoring = false
	done := c.restoreDone
	cancel := c.restoreCancel
	c.restoreDone, c.restoreCancel = nil, nil
	c.restoreMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		close(done)
	}
}

// restoreCheckpoint is the abort signal migration.Restore reads between its steps: a
// stop that has been requested for this task. It takes restoreMu for the read and holds
// nothing else, so it cannot deadlock the teardown that set the marker.
func (c *Controller) restoreCheckpoint() error {
	c.restoreMu.Lock()
	defer c.restoreMu.Unlock()

	if !c.restoreStopped {
		return nil
	}
	return errRestoreStopped
}

// awaitRestoreStop marks this task stopped and waits for an in-flight restore to
// unwind. It answers whether it had to wait, which is the teardown's evidence that a
// VM can no longer appear behind it: the restore either aborted (nothing exists) or
// finished (its record exists), so the lookup that follows is the whole truth.
//
// It is called with c.mu held by every teardown path, and that is safe: the restore
// never takes c.mu, only restoreMu, the health and terminal latches, and the store. The
// wait is bounded by the restore's own timeouts, and the checkpoint makes it end at the
// next step boundary rather than at the end of a 30-second guest wait.
func (c *Controller) awaitRestoreStop() (waited bool) {
	c.restoreMu.Lock()
	c.restoreStopped = true
	if !c.restoring {
		c.restoreMu.Unlock()
		return false
	}
	done, cancel := c.restoreDone, c.restoreCancel
	c.restoreMu.Unlock()

	// A step with no checkpoint near it (the guest-agent wait, a guest command bounded
	// by its own deadline) must not hold the teardown for its whole timeout: the restore's
	// context is this controller's to end.
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return true
}

// RestoredLeftovers deletes the two host-side names a restored VM leaves behind when it
// is stopped: the TAP device the snapshot recorded and the disk the restore materialised
// at the path the state file names.
//
// They are not the record's — the record is the pointer, these are the resources — and
// neither is deleted anywhere else: the VMM manager never created them (pkg/snapshot and
// this restore did), pkg/network's cleanup is keyed by the task's own name while the
// device is the source task's, and the disk lives at the source path. Without this a
// node that migrated N tasks accumulates N TAP devices and N disk copies for ever.
type RestoredLeftovers struct {
	// DeleteTap removes the recorded TAP device. Nil selects network.RemoveTAPDevice,
	// which treats a device that is already gone as success.
	DeleteTap func(name string) error
	// DeleteDisk removes the materialised disk. Nil selects os.Remove, which ignores
	// a file that is already gone through the caller's IsNotExist check.
	DeleteDisk func(path string) error
}

// tapDelete is the TAP remover this controller uses.
func (l RestoredLeftovers) tapDelete(name string) error {
	if l.DeleteTap != nil {
		return l.DeleteTap(name)
	}
	return network.RemoveTAPDevice(name)
}

// diskDelete is the disk remover this controller uses.
func (l RestoredLeftovers) diskDelete(path string) error {
	if l.DeleteDisk != nil {
		return l.DeleteDisk(path)
	}
	return os.Remove(path)
}

// removeRestoredLeftovers deletes the TAP device and the disk a stopped restored VM
// named. Both are best effort and both are reported through the logger: the VM is
// already gone, so a leftover is a leak to report, not a teardown failure to raise —
// the record is what a retry needs, and it has already been deleted.
func (c *Controller) removeRestoredLeftovers(taskID string, tapName, diskPath string) {
	if tapName != "" {
		if err := c.restoredLeftovers.tapDelete(tapName); err != nil {
			c.logger.Warn().Err(err).Str("tap", tapName).Str("task_id", taskID).
				Msg("The stopped restored VM's TAP device could not be removed")
		}
	}
	if diskPath != "" {
		if err := c.restoredLeftovers.diskDelete(diskPath); err != nil && !os.IsNotExist(err) {
			c.logger.Warn().Err(err).Str("disk", diskPath).Str("task_id", taskID).
				Msg("The stopped restored VM's disk could not be removed")
		}
	}
}

// discardRestoredVM unwinds a VM this restore brought up but will not run, for a failure
// that happened after migration.Restore returned. The restore's own undo covered every
// failure inside it; this covers the steps after it (publishing the task's host ports,
// for one), and it is the same three effects in the same order: stop the process, delete
// the device, forget the record.
//
// The disk is deliberately kept: the directive goes back, and the retry materialises the
// disk over this path anyway. Deleting it here would make a transient failure cost the
// whole transfer's bytes on every attempt.
func (c *Controller) discardRestoredVM(
	taskID string, instance scruntime.RestoredInstance, deps migration.RestoreDeps,
) error {
	var problems []error

	if stopper, ok := deps.Snapshot.(migration.RestoredVMStopper); ok {
		if err := stopper.StopRestored(taskID); err != nil && !errors.Is(err, snapshot.ErrNoRestoredVM) {
			problems = append(problems, fmt.Errorf("stop the restored VM: %w", err))
		}
	}
	if err := deps.TapDelete(instance.TapName); err != nil {
		problems = append(problems, fmt.Errorf("delete the TAP device %s: %w", instance.TapName, err))
	}
	if err := deps.Registry.Delete(taskID); err != nil {
		problems = append(problems, fmt.Errorf("delete the restored-instance record: %w", err))
	}

	return errors.Join(problems...)
}
