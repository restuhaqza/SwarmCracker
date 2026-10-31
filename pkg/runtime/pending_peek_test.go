package runtime

// The read half of the directive store: a task's Prepare asks whether a directive is
// waiting for its service before it does the work a boot needs, and Take answers the
// same question destructively at start. The two must not disagree, or a task would skip
// an image it then needs — or pull one it never runs.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeekLeavesTheDirectiveInPlace pins the whole point of the read: a prepare must not
// consume the directive it only asked about.
func TestPeekLeavesTheDirectiveInPlace(t *testing.T) {
	root := newPendingStaging(t)
	store, _ := newTestPendingRestores(t)
	want := pendingFixture(root)
	require.NoError(t, store.Put(want))

	got, found, err := store.Peek(want.ServiceID, root)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want, got)

	// The directive is still there for the task that claims it.
	taken, taken2, err := store.Take(want.ServiceID, root)
	require.NoError(t, err)
	require.True(t, taken2, "Peek did not consume the directive")
	assert.Equal(t, want, taken)
}

// TestPeekAnswersWhatTakeWould pins the shared verdicts: nothing prepared, an expired
// directive (which Peek reports but does not delete — Take deletes it), and an unusable
// record, which is an error for both.
func TestPeekAnswersWhatTakeWould(t *testing.T) {
	t.Run("nothing was prepared", func(t *testing.T) {
		root := newPendingStaging(t)
		store, _ := newTestPendingRestores(t)

		_, found, err := store.Peek("svc-1", root)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("an expired directive is reported, not deleted", func(t *testing.T) {
		root := newPendingStaging(t)
		store, _ := newTestPendingRestores(t)
		expired := pendingFixture(root)
		expired.CreatedAt = expired.CreatedAt.Add(-2 * DefaultPendingRestoreTTL)
		require.NoError(t, store.Put(expired))

		got, found, err := store.Peek(expired.ServiceID, root)
		require.NoError(t, err)
		assert.False(t, found, "a peek must not promise a restore Take would refuse")
		assert.True(t, got.Expired, "the reason is explicit, not a zero CreatedAt")
		assert.Equal(t, expired.ArtifactID, got.ArtifactID, "the record is still returned to name what was ignored")

		// Peek is a read: the record is still the node's belief until Take or Sweep
		// removes it.
		_, statErr := os.Stat(filepath.Join(store.dir, expired.ServiceID+pendingRecordSuffix))
		assert.NoError(t, statErr)
	})

	t.Run("an unusable record is an error", func(t *testing.T) {
		root := newPendingStaging(t)
		store, dir := newTestPendingRestores(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "svc-1"+pendingRecordSuffix), []byte("{not json"), 0o600))

		_, found, err := store.Peek("svc-1", root)
		require.Error(t, err)
		assert.False(t, found)
		assert.Contains(t, err.Error(), "corrupt")
	})
}

// TestTakeReportsExpiryExplicitly pins the answer a caller distinguishes from "nothing was
// prepared": a zero CreatedAt means the store recorded nothing, and only the explicit flag
// says the directive was there and was too old.
func TestTakeReportsExpiryExplicitly(t *testing.T) {
	root := newPendingStaging(t)
	store, _ := newTestPendingRestores(t)
	expired := pendingFixture(root)
	expired.CreatedAt = expired.CreatedAt.Add(-2 * DefaultPendingRestoreTTL)
	require.NoError(t, store.Put(expired))

	got, found, err := store.Take(expired.ServiceID, root)
	require.NoError(t, err)
	assert.False(t, found)
	assert.True(t, got.Expired, "expiry is an explicit signal")

	// The other answer: nothing was ever prepared, and nothing is claimed to have expired.
	_, found, err = store.Take("svc-2", root)
	require.NoError(t, err)
	assert.False(t, found)
}
