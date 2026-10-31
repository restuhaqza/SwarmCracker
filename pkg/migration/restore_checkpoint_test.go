package migration

// A restore can be cancelled: the task it is bringing a VM up for may be stopped while
// the load runs, and the ruling is that a stop must not leave a VM behind. The seam is
// deps.Checkpoint, which Restore asks between its steps; every answer but nil takes the
// same undo path a failed step does.
//
// These tests pin the whole handshake: the abort is reported as an abort, it happens
// before anything exists when it is asked early, and at every later boundary the undo
// removes exactly what had been created by then — the VM, the TAP device and the record.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errRestoreStopped is the sentinel a caller's checkpoint returns when the task was
// stopped. It is deliberately a test-local error: pkg/migration must not know what a
// stopped task is, only that a checkpoint said no.
var errRestoreStopped = errors.New("the task was stopped")

// abortAfter returns a checkpoint that allows n-1 calls and refuses the n-th, and
// counts how many times it was asked.
func abortAfter(n int) (checkpoint func() error, calls *int) {
	asked := 0
	return func() error {
		asked++
		if asked >= n {
			return errRestoreStopped
		}
		return nil
	}, &asked
}

// TestRestoreAbortsAtEveryBoundary pins the cancellable restore: the checkpoint is
// asked before anything exists and after each step that creates something, and an abort
// at any of them leaves no VM, no TAP device and no record.
func TestRestoreAbortsAtEveryBoundary(t *testing.T) {
	cases := []struct {
		name string
		// abortAt is the checkpoint call that refuses (1-based).
		abortAt int
		// tapCreated is whether the TAP device existed when the abort arrived.
		tapCreated bool
	}{
		{name: "before anything is created", abortAt: 1},
		{name: "after the snapshot is loaded", abortAt: 2, tapCreated: true},
		{name: "after the guest is re-addressed", abortAt: 3, tapCreated: true},
		{name: "after the TAP is brought up", abortAt: 4, tapCreated: true},
		{name: "after the record is written", abortAt: 5, tapCreated: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			checkpoint, calls := abortAfter(tc.abortAt)
			deps := h.deps()
			deps.Checkpoint = checkpoint

			_, err := Restore(context.Background(), h.input, deps)

			require.Error(t, err)
			assert.ErrorIs(t, err, errRestoreStopped, "the abort is reported as the cause, not as a mystery")
			assert.ErrorContains(t, err, stepAborted)
			assert.GreaterOrEqual(t, *calls, 1)

			// No VM is left running: the undo stopped the process the load started.
			assert.Equal(t, 1, h.restorer.stopCalls, "the VM the abort left behind is stopped")
			assert.Equal(t, h.input.TaskID, h.restorer.stopTask)

			if tc.tapCreated {
				assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapDeleted,
					"the TAP device the restore created is removed")
			} else {
				assert.Empty(t, h.tapDeleted, "nothing was created, so there is nothing to delete")
			}

			// And no record survives: a record would resolve a task to a VM that is gone.
			_, found := h.record()
			assert.False(t, found, "no record survives an aborted restore")
			assert.Empty(t, h.records())

			h.assertArtifactIntact()
		})
	}
}

// TestRestoreWithoutACheckpointIsUnchanged pins the nil seam: a caller that cannot be
// cancelled (the broker's import, a test) restores exactly as before.
func TestRestoreWithoutACheckpointIsUnchanged(t *testing.T) {
	h := newRestoreHarness(t)

	instance, err := h.restore(context.Background())
	require.NoError(t, err)
	require.Equal(t, h.input.TaskID, instance.TaskID)
	require.Zero(t, h.restorer.stopCalls, "a successful restore stops nothing")
	require.Empty(t, h.tapDeleted)
}

// TestRestoreCheckpointRunsOnceTheRecordExists pins the last boundary's position: the
// checkpoint after the record is written must come before the restore reports success, so
// an abort there is still an abort — not a VM published for a task that was stopped in the
// instant between the record and the return.
func TestRestoreCheckpointRunsOnceTheRecordExists(t *testing.T) {
	h := newRestoreHarness(t)
	registry := &recordingStore{events: &h.events}
	h.store = registry

	checkpoint, _ := abortAfter(5)
	deps := h.deps()
	deps.Registry = registry
	deps.Checkpoint = checkpoint

	_, err := Restore(context.Background(), h.input, deps)

	require.Error(t, err)
	assert.ErrorContains(t, err, stepAborted)
	assert.Equal(t, []string{h.input.TaskID}, registry.deleted,
		"the record this restore just wrote is deleted by its undo")
}
