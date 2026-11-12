package sandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(StoreConfig{Dir: Dir(t.TempDir())})
	require.NoError(t, err)
	return store
}

func testRecord(id string) Record {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	return Record{
		ID: id, State: StateReady, Template: "base@1",
		Created: now, Updated: now, LastActive: now,
		Disk: "/var/lib/firecracker/golden-test/" + id + ".ext4",
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := newTestStore(t)
	rec := testRecord("sbx1")
	require.NoError(t, store.Put(rec))

	got, found, err := store.Get("sbx1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, rec, got)

	// A replace is a replace: the second write is what a reader sees.
	rec.State = StateFailed
	rec.Reason = "the guest never answered"
	rec.Updated = rec.Updated.Add(time.Second)
	require.NoError(t, store.Put(rec))
	got, found, err = store.Get("sbx1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, StateFailed, got.State)

	records, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, records, 1)
	assert.Equal(t, "sbx1", records[0].ID)
}

func TestStoreRefusesAnUnusableRecordBeforeWritingIt(t *testing.T) {
	store := newTestStore(t)

	rec := testRecord("sbx1")
	rec.Disk = "relative/path"
	assert.Error(t, store.Put(rec))

	_, found, err := store.Get("sbx1")
	require.NoError(t, err)
	assert.False(t, found, "a refused record must publish nothing")

	// A record must not name another sandbox: a lookup would find the wrong answer.
	rec = testRecord("sbx1")
	require.NoError(t, os.WriteFile(filepath.Join(store.Dir(), "sbx1.json"),
		[]byte(`{"id":"other","state":"ready","created":"2026-11-12T10:00:00Z","updated":"2026-11-12T10:00:00Z"}`), 0o600))
	_, _, err = store.Get("sbx1")
	assert.Error(t, err, "a record that names another sandbox is an error, not a hit")
}

func TestStoreListingSkipsABadFileAndKeepsTheRest(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.Put(testRecord("sbx1")))
	require.NoError(t, store.Put(testRecord("sbx2")))

	// A record a human edited into an unreadable state must not hide the others.
	bad := filepath.Join(store.Dir(), "broken.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))

	records, skipped, err := store.List()
	require.NoError(t, err)
	assert.Len(t, records, 2)
	assert.Equal(t, []string{bad}, skipped)

	// A temporary in flight is not a record and is not a skipped record either.
	tmp := filepath.Join(store.Dir(), tempPrefix+"sbx3-123"+tempSuffix)
	require.NoError(t, os.WriteFile(tmp, []byte("{}"), 0o600))
	records, skipped, err = store.List()
	require.NoError(t, err)
	assert.Len(t, records, 2)
	assert.Equal(t, []string{bad}, skipped)
}

func TestStoreDeleteIsIdempotentAndDurable(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.Put(testRecord("sbx1")))
	require.NoError(t, store.Delete("sbx1"))

	_, found, err := store.Get("sbx1")
	require.NoError(t, err)
	assert.False(t, found)

	assert.NoError(t, store.Delete("sbx1"), "deleting a record that is gone is success")
}

func TestStoreKeepsRecordsPrivate(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.Put(testRecord("sbx1")))

	dirInfo, err := os.Stat(store.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm(),
		"a record names host paths and a credential's location: the directory is the daemon's")

	fileInfo, err := os.Stat(filepath.Join(store.Dir(), "sbx1.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
}

func TestStoreRefusesAnIDThatCannotNameAFile(t *testing.T) {
	store := newTestStore(t)

	for _, id := range []string{"", "a/b", "..", "a.b", "with space"} {
		_, _, err := store.Get(id)
		assert.Error(t, err, "id %q must be refused", id)
		assert.Error(t, store.Delete(id))
	}
}
