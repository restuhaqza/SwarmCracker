package sandbox

// The event bus and who publishes on it: the fan-out rules, and the two sources that live
// in this package's own loops — the reaper taking an idle sandbox back, and a reconcile pass
// finding a sandbox's VMM gone. Both run on the injected clock, so none of this sleeps for
// real time to pass.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// TestEventsBusDeliversToEverySubscriber pins the bus's fan-out: one change, every watcher,
// and each one gets its own copy of the same change.
func TestEventsBusDeliversToEverySubscriber(t *testing.T) {
	bus := NewEvents()
	first, cancelFirst := bus.Subscribe()
	defer cancelFirst()
	second, cancelSecond := bus.Subscribe()
	defer cancelSecond()

	sent := Change{Kind: ChangeCreated, ID: "sbx-1", At: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	bus.Publish(sent)

	for name, ch := range map[string]<-chan Change{"first": first, "second": second} {
		select {
		case got := <-ch:
			assert.Equal(t, sent.Kind, got.Kind, name)
			assert.Equal(t, "sbx-1", got.ID, name)
			assert.Equal(t, sent.At, got.At, name)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s subscriber got no change", name)
		}
	}
}

// TestEventsBusDropsRatherThanBlocking pins the rule that keeps a watcher from stalling an
// operation: a subscriber that has fallen a whole buffer behind loses the changes after it,
// and the publisher still returns.
func TestEventsBusDropsRatherThanBlocking(t *testing.T) {
	bus := NewEvents()
	slow, cancel := bus.Subscribe()
	defer cancel()

	change := Change{Kind: ChangeCreated, ID: "sbx-1"}
	// One more than the buffer, from a goroutine so a blocking Publish fails the test
	// instead of hanging it.
	published := make(chan struct{})
	go func() {
		for i := 0; i <= changeBuffer; i++ {
			bus.Publish(change)
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that is not reading")
	}

	delivered := 0
	for {
		select {
		case <-slow:
			delivered++
			continue
		default:
		}
		break
	}
	assert.Equal(t, changeBuffer, delivered, "a subscriber keeps its buffer and loses the rest")
}

// TestEventsBusCancelEndsTheSubscription pins the lifecycle: cancel closes the channel, the
// subscriber's range ends, and a second cancel is harmless.
func TestEventsBusCancelEndsTheSubscription(t *testing.T) {
	bus := NewEvents()
	ch, cancel := bus.Subscribe()

	cancel()
	_, open := <-ch
	assert.False(t, open, "cancel closes the subscription")
	cancel()

	// A change published after the cancel reaches nobody, and does not panic on the
	// closed channel.
	bus.Publish(Change{Kind: ChangeRemoved, ID: "sbx-1"})
	_, open = <-ch
	assert.False(t, open)
}

// TestRecordChangeCarriesTheRecordsOwnClockAndReason pins what a change says: the record as
// it was, stamped by the record's own Updated where it has one, so a watcher sees the same
// moment the caller did.
func TestRecordChangeCarriesTheRecordsOwnClockAndReason(t *testing.T) {
	updated := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	rec := Record{ID: "sbx-1", State: StateFailed, Reason: "the VMM is gone", Updated: updated}

	change := RecordChange(ChangeFailed, rec, updated.Add(time.Hour))
	require.NotNil(t, change.Record)
	assert.Equal(t, ChangeFailed, change.Kind)
	assert.Equal(t, "sbx-1", change.ID)
	assert.Equal(t, updated, change.At, "the record's own clock is what the change reports")
	assert.Equal(t, "the VMM is gone", change.Record.Reason)

	// A record with no clock of its own is stamped with the node's now, which the caller
	// passes in rather than this package reading a clock of its own.
	at := updated.Add(2 * time.Hour)
	fresh := RecordChange(ChangeCreated, Record{ID: "sbx-2"}, at)
	assert.Equal(t, at.UTC(), fresh.At)

	// A removal has no record to carry: the sandbox is gone, and a zero record would
	// describe a sandbox with nothing in it rather than one that is not there.
	removal := RemovalChange("sbx-3", at)
	assert.Equal(t, ChangeRemoved, removal.Kind)
	assert.Equal(t, "sbx-3", removal.ID)
	assert.Nil(t, removal.Record)
}

// TestReapPublishesTheRemovalItPerformed is the reaper's half of the stream: a sandbox taken
// back without a caller asking is still a change a watcher has to hear about, and one the
// reaper could not take back is not.
//
// The publish is what is under test, so removing it fails this test rather than only the
// API's — a change nobody publishes is invisible on every stream.
func TestReapPublishesTheRemovalItPerformed(t *testing.T) {
	t.Run("a sandbox it took back", func(t *testing.T) {
		f := newFixture(t)
		bus := NewEvents()
		f.manager.cfg.Events = bus
		watcher, stop := bus.Subscribe()
		defer stop()

		require.NoError(t, f.store.Put(idleSandbox(f, "sbx-idle")))

		// A sandbox that is not idle yet: the reaper leaves it, and leaving it publishes
		// nothing. It is read first so the assertion below cannot pass by accident.
		busy := testRecord("sbx-busy")
		busy.LastActive, busy.IdleTTLSeconds = f.clock.Now(), int64(time.Minute/time.Second)
		require.NoError(t, f.store.Put(busy))

		reaped, err := f.manager.Reap(context.Background(), f.clock.Now())
		require.NoError(t, err)
		require.Len(t, reaped, 1)
		require.Equal(t, "sbx-idle", reaped[0].ID)

		select {
		case change := <-watcher:
			assert.Equal(t, ChangeRemoved, change.Kind)
			assert.Equal(t, "sbx-idle", change.ID)
			assert.Nil(t, change.Record, "a removed sandbox has no record to carry")
			assert.Equal(t, f.clock.Now().UTC(), change.At, "the change is stamped by the node's clock")
		case <-time.After(2 * time.Second):
			t.Fatal("reaping a sandbox published no change")
		}

		select {
		case extra := <-watcher:
			t.Fatalf("the reaper published %q for a sandbox it left alone", extra.ID)
		case <-time.After(100 * time.Millisecond):
		}
	})

	t.Run("a sandbox it could not take back", func(t *testing.T) {
		f := newFixture(t)
		bus := NewEvents()
		f.manager.cfg.Events = bus
		watcher, stop := bus.Subscribe()
		defer stop()

		wedged := idleSandbox(f, "sbx-wedged")
		wedged.Disk = filepath.Join(t.TempDir(), "sbx-wedged.ext4")
		require.NoError(t, os.WriteFile(wedged.Disk, []byte("disk"), 0o600))
		require.NoError(t, f.store.Put(wedged))
		require.NoError(t, f.instances.Put(vminstance.Instance{
			ID: "sbx-wedged", Kind: vminstance.KindSandbox, PID: 4242,
		}))
		f.instances.alive = func(int) bool { return true }

		original := stopVMM
		stopVMM = func(pid int, _ time.Duration) error {
			if pid == 4242 {
				return fmt.Errorf("process is wedged")
			}
			return original(pid, time.Second)
		}
		t.Cleanup(func() { stopVMM = original })

		reaped, err := f.manager.Reap(context.Background(), f.clock.Now())
		require.NoError(t, err)
		require.Len(t, reaped, 1)
		require.Error(t, reaped[0].Err)

		select {
		case change := <-watcher:
			t.Fatalf("a sandbox that was not removed published %q", change.Kind)
		case <-time.After(100 * time.Millisecond):
		}
	})
}

// TestListPublishesAFailureWhenAReconcileFindsTheVMMGone is the reconcile half: a record
// whose VMM is no longer in the node's registry is marked failed, and that transition is
// announced with the reason — which is the part a watcher cannot read from anywhere else.
func TestListPublishesAFailureWhenAReconcileFindsTheVMMGone(t *testing.T) {
	f := newFixture(t)
	bus := NewEvents()
	f.manager.cfg.Events = bus
	watcher, stop := bus.Subscribe()
	defer stop()

	require.NoError(t, f.store.Put(testRecord("sbx-lost")))

	records, _, err := f.manager.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, StateFailed, records[0].State, "the record is marked failed as it is read")

	select {
	case change := <-watcher:
		assert.Equal(t, ChangeFailed, change.Kind)
		assert.Equal(t, "sbx-lost", change.ID)
		require.NotNil(t, change.Record, "a failed sandbox still exists, so the change carries it")
		assert.Contains(t, change.Record.Reason, "VMM is gone")
		assert.Equal(t, f.clock.Now().UTC(), change.At)
	case <-time.After(2 * time.Second):
		t.Fatal("reconciling a lost sandbox published no change")
	}

	// The transition happens once: a second pass finds the record already failed, and
	// publishing the same failure again would be a stream that reports a change that did
	// not happen.
	_, _, err = f.manager.List()
	require.NoError(t, err)
	select {
	case extra := <-watcher:
		t.Fatalf("a second reconcile published %q for a record that did not change", extra.Kind)
	case <-time.After(100 * time.Millisecond):
	}
}
