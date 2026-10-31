package swarmkit

// Fix round 2. The window this round closes is the one between Prepare and Start:
// Prepare peeks the directive to skip the image pull and the TAP device, and Start
// takes it to restore. If the directive expires or is claimed by a sibling task of
// the same service in between, the old Start booted cold — no prepared image, no
// TAP — and the task failed with an unrelated "rootfs/TAP missing" error.
//
// The rule now: Start peeks first and boots only when the peek found nothing. A
// directive that was there and can no longer be taken fails the task with a sentence
// that says so, because SwarmKit's retry then runs Prepare without a directive, does
// the full preparation and boots normally.
//
//	N1 peek-then-take: an expired/sibling-claimed directive fails, never boots
//	N1 a peek error fails the task
//	N1 no directive at all is unchanged: take, then the very same boot
//	record-delete failure on teardown says the TAP and disk are held too

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestStartFailsWhenTheDirectiveGoesAwayBetweenPrepareAndStart is N1. Prepare saw a
// directive and skipped the image pull and the TAP device; Start cannot claim it. A
// cold boot here would start a VM this node never prepared — no rootfs, no TAP — which
// fails with an error that names neither the migration nor the fix.
func TestStartFailsWhenTheDirectiveGoesAwayBetweenPrepareAndStart(t *testing.T) {
	cases := []struct {
		name string
		// lose makes the directive Prepare saw unavailable to Start, the way the
		// TTL or a sibling task of the same service would.
		lose func(fixture *artifactStartFixture)
	}{
		{
			name: "expired between prepare and start",
			lose: func(fixture *artifactStartFixture) {
				fixture.pending.directive.Expired = true
				fixture.pending.found = false
			},
		},
		{
			name: "claimed by a sibling task first",
			lose: func(fixture *artifactStartFixture) {
				// Another task of the service claimed and consumed it.
				fixture.pending.directive = scruntime.PendingRestore{}
				fixture.pending.found = false
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})

			// Prepare sees the directive: the boot's work is skipped for this task.
			fixture.ctrl.prepared = false
			fixture.ctrl.imagePrep = &MockImagePreparer{}
			require.NoError(t, fixture.ctrl.Prepare(context.Background()))
			fixture.calls.add("prepared")

			// ...and it is gone before the start, which is the window this round
			// closes.
			tc.lose(fixture)

			err := fixture.ctrl.Start(context.Background())
			require.Error(t, err, "a task prepared for a restore must not boot cold")
			assert.ErrorContains(t, err, "no longer available")
			assert.ErrorContains(t, err, "the retry starts a fresh VM")

			assert.NotContains(t, fixture.calls.list(), "vmm.Start",
				"no cold boot without the image and the TAP Prepare skipped")
			assert.NotContains(t, fixture.calls.list(), "translate")
			assert.Empty(t, fixture.restorer.infos, "nothing is restored for a directive nobody holds")
			assert.Empty(t, fixture.pending.puts,
				"there is no directive to put back: the store dropped it, or a sibling owns it")
		})
	}
}

// TestStartRestoresADirectiveThatLandsBetweenThePeekAndTheTake pins the other end of
// the same window: a directive the peek missed because it landed a moment later is
// still this task's to restore. Booting would consume it and start a second VM beside
// the one the source node is still running.
func TestStartRestoresADirectiveThatLandsBetweenThePeekAndTheTake(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true})
	// The peek answers "nothing", the take finds the directive the manager wrote in
	// between: the fake store is told to report it only from the take.
	pending := &lateDirectiveStore{directive: fixture.directive}
	fixture.ctrl.migration.Pending = pending

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	require.Len(t, fixture.restorer.infos, 1, "the directive the take found is restored")
	assert.NotContains(t, fixture.calls.list(), "vmm.Start")
}

// lateDirectiveStore answers "nothing" to a peek and hands the directive to the first
// take, which is what a prepare racing a start looks like from the executor's side.
type lateDirectiveStore struct {
	directive scruntime.PendingRestore

	taken bool
}

func (s *lateDirectiveStore) Peek(string, string) (scruntime.PendingRestore, bool, error) {
	return scruntime.PendingRestore{}, false, nil
}

func (s *lateDirectiveStore) Take(string, string) (scruntime.PendingRestore, bool, error) {
	if s.taken {
		return scruntime.PendingRestore{}, false, nil
	}
	s.taken = true
	return s.directive, true, nil
}

func (s *lateDirectiveStore) Put(scruntime.PendingRestore) error { return nil }

// TestStartFailsWhenAPreparedDirectiveCannotBeTaken is the same failure for the other
// half of the take: the directive is still there but the store cannot hand it over. The
// store keeps it for the retry, and the task fails rather than booting beside whatever
// the source node is still running.
func TestStartFailsWhenAPreparedDirectiveCannotBeTaken(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.pending.takeErr = errors.New("the claim could not be renamed")

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "the claim could not be renamed")
	assert.ErrorContains(t, err, "no longer available")
	assert.ErrorContains(t, err, "the retry starts a fresh VM")

	assert.NotContains(t, fixture.calls.list(), "vmm.Start")
	assert.Empty(t, fixture.restorer.infos)
}

// TestStartFailsWhenThePeekItselfFails is N1's third answer: a peek the store cannot
// answer is not "nothing prepared". Booting on it would start a second VM beside the
// source's whenever the directive was there but unreadable.
func TestStartFailsWhenThePeekItselfFails(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.pending.peekErr = errors.New("the directive is corrupt")

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "the directive is corrupt")
	assert.ErrorContains(t, err, "cannot be used")

	assert.NotContains(t, fixture.calls.list(), "vmm.Start")
	assert.Empty(t, fixture.restorer.infos)
	assert.Empty(t, fixture.pending.takes, "a peek that cannot answer is not a reason to claim")
}

// TestStartWithoutADirectivePeeksTakesThenBoots pins the ordinary case end to end: the
// peek answers nothing, the take answers nothing, and the boot is the one this executor
// always ran — the same call log, in the same order.
func TestStartWithoutADirectivePeeksTakesThenBoots(t *testing.T) {
	baseline := newArtifactStartFixture(t, artifactStartOptions{})

	var prepared int
	wired := newArtifactStartFixture(t, artifactStartOptions{withMigration: true})
	wired.ctrl.imagePrep = &MockImagePreparer{PrepareFunc: func(context.Context, *types.Task) error {
		prepared++
		return nil
	}}
	wired.ctrl.prepared = false
	require.NoError(t, wired.ctrl.Prepare(context.Background()))

	require.NoError(t, baseline.ctrl.Start(context.Background()))
	require.NoError(t, wired.ctrl.Start(context.Background()))

	assert.Equal(t, 1, prepared, "a task with nothing prepared pulls its image")
	assert.Equal(t, baseline.calls.list(), wired.calls.list(),
		"a peek that finds nothing changes nothing about the boot")
	assert.Equal(t, []string{artifactServiceID, artifactServiceID}, wired.pending.peeks,
		"Prepare and Start each peek, and neither consumes")
	assert.Equal(t, []string{artifactServiceID}, wired.pending.takes, "and Start still takes once")
	assert.Empty(t, wired.restorer.infos)
	assert.Empty(t, wired.pending.puts)
}

// TestTeardownSaysTheTAPAndDiskAreHeldWithTheRecord pins the logging gap: a record
// delete that keeps failing on teardown holds the two resources the record names too,
// because nothing is deleted for a VM whose record still says it exists. An error that
// names only the record leaves an operator to discover two more leaks.
func TestTeardownSaysTheTAPAndDiskAreHeldWithTheRecord(t *testing.T) {
	record := restoredTestRecord("new-task", "source-task")
	store := &fakeRestoredStore{record: record, found: true, deleteErr: errors.New("read-only file system")}
	fixture := newTeardownFixture(t, store, &fakeRestoredStopper{}, true)

	var taps, disks []string
	fixture.ctrl.restoredLeftovers = RestoredLeftovers{
		DeleteTap:  func(name string) error { taps = append(taps, name); return nil },
		DeleteDisk: func(path string) error { disks = append(disks, path); return nil },
	}

	err := fixture.ctrl.Shutdown(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "could not be deleted", "the record is still what failed")
	assert.ErrorContains(t, err, record.TapName,
		"the error names the TAP device that is held with it")
	assert.ErrorContains(t, err, record.DiskPath,
		"and the disk")
	assert.Contains(t, err.Error(), "held",
		"one clause says the two resources stay with the record")

	assert.Empty(t, taps, "nothing is deleted for a VM whose record still says it exists")
	assert.Empty(t, disks)
}
