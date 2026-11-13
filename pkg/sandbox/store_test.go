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

// TestStoreRefusesToBeUsedBeforeItIsOpened pins the zero value: a daemon that wired no
// store gets an error it can act on from every operation, not a panic or an empty node.
func TestStoreRefusesToBeUsedBeforeItIsOpened(t *testing.T) {
	var unopened Store
	assert.Empty(t, unopened.Dir())

	assert.Error(t, unopened.Put(testRecord("a")))
	_, found, err := unopened.Get("a")
	assert.False(t, found)
	assert.Error(t, err)
	_, _, err = unopened.List()
	assert.Error(t, err)
	assert.Error(t, unopened.Delete("a"))
}

// TestNewStoreCreatesTheDirectoryAndRefusesAnEmptyOne pins what a node gets when it
// starts: the record directory is created under the state directory it names, and a
// store asked to live nowhere is refused rather than defaulting somewhere.
func TestNewStoreCreatesTheDirectoryAndRefusesAnEmptyOne(t *testing.T) {
	assert.Empty(t, Dir(""), "a node with no state directory has no record directory")

	_, err := NewStore(StoreConfig{})
	assert.Error(t, err, "a store with no directory cannot exist")

	state := t.TempDir()
	store, err := NewStore(StoreConfig{Dir: Dir(state)})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(state, DirName), store.Dir())

	info, statErr := os.Stat(store.Dir())
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(dirMode), info.Mode().Perm(), "records are the daemon's alone")
}

// TestStoreSaysWhenASandboxWasNeverRecorded pins the distinction a caller depends on:
// an ID this node never recorded is "not found", not an error.
func TestStoreSaysWhenASandboxWasNeverRecorded(t *testing.T) {
	store := newTestStore(t)
	_, found, err := store.Get("never")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestStoreReportsARecordItCannotDelete pins the failure a rm path must not swallow: a
// record that exists and cannot be removed keeps its sandbox visible on the next pass,
// so the error names the path rather than reporting a removal that did not happen.
func TestStoreReportsARecordItCannotDelete(t *testing.T) {
	store := newTestStore(t)

	// A non-empty directory where the record file belongs: removing it as a file fails,
	// whatever the process's privileges.
	path := filepath.Join(store.Dir(), "wedged"+recordSuffix)
	require.NoError(t, os.MkdirAll(filepath.Join(path, "held"), 0o700))

	err := store.Delete("wedged")
	require.Error(t, err)
	assert.Contains(t, err.Error(), path)
}

// TestStoreNamesARecordPathItCannotWrite pins the failure a caller must not mistake for a
// successful write: a path that is not a file fails the write and says which store, and a
// read of it is an error rather than "no such sandbox".
func TestStoreNamesARecordPathItCannotWrite(t *testing.T) {
	store := newTestStore(t)

	// A non-empty directory where the record file belongs: the write cannot be published
	// under that name, whatever the process's privileges.
	held := filepath.Join(store.Dir(), "wedged"+recordSuffix)
	require.NoError(t, os.MkdirAll(filepath.Join(held, "held"), 0o700))

	err := store.Put(testRecord("wedged"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox store")

	_, found, err := store.Get("wedged")
	assert.False(t, found)
	require.Error(t, err, "a record that cannot be read is not an absent one")
}

// TestStoreReportsADirectoryItCannotUse pins the two ways the record directory can fail a
// node: a state directory that is a file, and one that disappeared after the daemon
// opened it.
func TestStoreReportsADirectoryItCannotUse(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600))

	_, err := NewStore(StoreConfig{Dir: filepath.Join(blocked, DirName)})
	require.Error(t, err, "a record directory under a file cannot be created")

	store := newTestStore(t)
	require.NoError(t, os.RemoveAll(store.Dir()))

	err = store.Put(testRecord("a"))
	require.Error(t, err, "a write into a directory that is gone is an error, not a silent loss")
	assert.Contains(t, err.Error(), "sandbox store")
}

// TestStoreRefusesARecordItCouldNotHaveWritten pins that a file a human edited, or one an
// older build wrote, is refused when it is read rather than handed out as a record this
// node could have produced.
func TestStoreRefusesARecordItCouldNotHaveWritten(t *testing.T) {
	store := newTestStore(t)

	// A record naming a state no build defines: one record file, one sandbox, and a state
	// nothing would know how to act on.
	edited := `{"id":"edited","state":"half-ready","template":"base@1","created":"2026-11-12T10:00:00Z","updated":"2026-11-12T10:00:00Z"}`
	require.NoError(t, os.WriteFile(filepath.Join(store.Dir(), "edited"+recordSuffix), []byte(edited), 0o600))

	_, found, err := store.Get("edited")
	assert.False(t, found)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state")
}
