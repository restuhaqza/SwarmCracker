package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingTestArtifact is the artifact the directive fixtures name.
const pendingTestArtifact = "mig-0123456789abcdef"

// stageArtifact adds one artifact directory to a node's migration root, the way a
// completed stage would leave it, and returns it.
func stageArtifact(t *testing.T, root, artifactID string) string {
	t.Helper()

	dir := filepath.Join(root, artifactID)
	require.NoError(t, os.Mkdir(dir, 0o700))
	return dir
}

// newPendingStaging makes one node-local migration root holding the artifact
// directory a prepare would have staged into, and returns the root. Take re-derives
// a directive's directory from this root, so a fixture without it is not a directive
// this node can follow.
func newPendingStaging(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	stageArtifact(t, root, pendingTestArtifact)
	return root
}

// pendingFixture is a usable directive: the next task of svc-1 to start on this
// node restores pendingTestArtifact from the directory the manager staged it in.
// CreatedAt is "now" because Take judges age against the wall clock, not against a
// fixed instant a test cannot move.
func pendingFixture(root string) PendingRestore {
	return PendingRestore{
		ServiceID:  "svc-1",
		ArtifactID: pendingTestArtifact,
		Dir:        filepath.Join(root, pendingTestArtifact),
		CreatedAt:  time.Now().UTC(),
	}
}

func newTestPendingRestores(t *testing.T) (*PendingRestores, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), PendingRestoreDirName)
	store, err := NewPendingRestores(dir)
	require.NoError(t, err)
	return store, dir
}

func TestPendingRestoreDir(t *testing.T) {
	cases := []struct {
		name     string
		stateDir string
		want     string
	}{
		{name: "the daemon's state directory", stateDir: "/var/lib/swarmkit", want: "/var/lib/swarmkit/" + PendingRestoreDirName},
		{name: "a trailing separator is cleaned", stateDir: "/var/lib/swarmkit/", want: "/var/lib/swarmkit/" + PendingRestoreDirName},
		{name: "a relative state directory keeps its shape", stateDir: "state", want: "state/" + PendingRestoreDirName},
		{name: "no state directory", stateDir: "", want: ""},
		{name: "blank state directory", stateDir: "   ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PendingRestoreDir(tc.stateDir))
		})
	}
}

func TestNewPendingRestores(t *testing.T) {
	t.Run("creates the directory 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", PendingRestoreDirName)
		store, err := NewPendingRestores(dir)
		require.NoError(t, err)
		require.NotNil(t, store)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
			"a directive names a node-local path and is readable only by the daemon")
	})

	t.Run("an existing directory is reused", func(t *testing.T) {
		dir := t.TempDir()
		first, err := NewPendingRestores(dir)
		require.NoError(t, err)
		require.NotNil(t, first)

		second, err := NewPendingRestores(dir)
		require.NoError(t, err, "a daemon restart must open the same directory again")
		assert.NotNil(t, second)
	})

	t.Run("an empty directory is refused", func(t *testing.T) {
		store, err := NewPendingRestores(" ")
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), "directory")
	})

	t.Run("a file in the way is reported", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), PendingRestoreDirName)
		require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))

		store, err := NewPendingRestores(path)
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), path)
	})

	// Opening a directory is not the same as being able to write a directive into
	// it: MkdirAll succeeds on an existing directory whatever its mode, and Put
	// runs after the manager has already staged the artifact here. A node that
	// could not keep the directive must refuse to start.
	t.Run("a read-only directory is refused", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "records")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		store, err := NewPendingRestores(dir)
		if err == nil {
			if os.Geteuid() == 0 {
				t.Skip("running as root: a 0500 directory is still writable, so the probe cannot fail")
			}
			t.Fatalf("opening %s with no write permission reported no error", dir)
		}
		assert.Nil(t, store)
		assert.Contains(t, err.Error(), dir, "the error must name the directory that cannot hold directives")
		assert.Contains(t, err.Error(), "not writable")
	})

	t.Run("the probe leaves no file behind", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "records")
		_, err := NewPendingRestores(dir)
		require.NoError(t, err)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "the write probe is cleaned up, so opening leaves no residue a human must explain")
	})
}

func TestPendingRestoresPutTake(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	want := pendingFixture(root)

	require.NoError(t, store.Put(want))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a successful Put leaves exactly one file, no temporary remainder")
	assert.Equal(t, want.ServiceID+".json", entries[0].Name(),
		"one directive per service, named after the service")

	info, err := os.Stat(filepath.Join(dir, want.ServiceID+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the directive is written 0600, like the rest of the daemon's state")

	got, found, err := store.Take(want.ServiceID, root)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want.ServiceID, got.ServiceID)
	assert.Equal(t, want.ArtifactID, got.ArtifactID)
	assert.Equal(t, want.Dir, got.Dir)
	assert.True(t, want.CreatedAt.Equal(got.CreatedAt), "createdAt = %v, want %v", got.CreatedAt, want.CreatedAt)

	// The directive is consumed once: a second task starting from the same service
	// must not restore the artifact again.
	_, found, err = store.Take(want.ServiceID, root)
	require.NoError(t, err)
	assert.False(t, found)

	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a consumed directive leaves neither its record nor its claim")
}

// TestPendingRestoresTakeRequiresTheMigrationRoot pins the one piece of context Take
// cannot derive from the record: the root it re-checks the directory against. A blank
// root would compare a node-local path with nothing, and the directive must survive
// the refusal untouched.
func TestPendingRestoresTakeRequiresTheMigrationRoot(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	record := pendingFixture(root)
	require.NoError(t, store.Put(record))

	_, found, err := store.Take(record.ServiceID, " ")
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "migration root")
	assert.FileExists(t, filepath.Join(dir, record.ServiceID+pendingRecordSuffix),
		"a take that cannot judge the directive must not consume it")
}

// TestPendingRestoresTakeRefusesADirectoryItCannotTrust is ruling I1: the directory a
// directive names is checked again at the instant the directive is consumed, not only
// when it was written. A record that fails is refused with a sentence naming the field
// and is not followed - and, being evidence of what the node believes, it is put back.
func TestPendingRestoresTakeRefusesADirectoryItCannotTrust(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, root string, record *PendingRestore)
		wantSub string
	}{
		{
			name: "a directory under another root",
			prepare: func(t *testing.T, _ string, record *PendingRestore) {
				// A real directory, with the artifact's name, under a root this node
				// does not call its migration root.
				record.Dir = filepath.Join(newPendingStaging(t), pendingTestArtifact)
			},
			wantSub: "dir",
		},
		{
			name: "a directory that is a symbolic link",
			prepare: func(t *testing.T, root string, _ *PendingRestore) {
				path := filepath.Join(root, pendingTestArtifact)
				require.NoError(t, os.RemoveAll(path))
				require.NoError(t, os.Symlink(t.TempDir(), path))
			},
			wantSub: "dir",
		},
		{
			name: "a directory that is gone",
			prepare: func(t *testing.T, root string, _ *PendingRestore) {
				require.NoError(t, os.RemoveAll(filepath.Join(root, pendingTestArtifact)))
			},
			wantSub: "dir",
		},
		{
			name: "a directory that is a file",
			prepare: func(t *testing.T, root string, _ *PendingRestore) {
				path := filepath.Join(root, pendingTestArtifact)
				require.NoError(t, os.RemoveAll(path))
				require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o600))
			},
			wantSub: "dir",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, dir := newTestPendingRestores(t)
			root := newPendingStaging(t)
			record := pendingFixture(root)
			tc.prepare(t, root, &record)
			require.NoError(t, store.Put(record))

			_, found, err := store.Take(record.ServiceID, root)
			require.Error(t, err)
			assert.False(t, found, "a directive naming a directory this node cannot trust restores nothing")
			assert.Contains(t, err.Error(), tc.wantSub, "the refusal names the field that is wrong")

			// The record is evidence, not something a refused take destroys.
			assert.FileExists(t, filepath.Join(dir, record.ServiceID+pendingRecordSuffix),
				"a refused directive is put back where a human can read it")
			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			assert.Len(t, entries, 1, "the refusal leaves neither a claim nor a second copy behind")
		})
	}
}

func TestPendingRestoresPutReplaces(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	root := newPendingStaging(t)
	first := pendingFixture(root)
	require.NoError(t, store.Put(first))

	// The manager retries a prepare; the second directive replaces the first
	// rather than queueing a second restore for the same service.
	second := first
	second.ArtifactID = "mig-fedcba9876543210"
	second.Dir = stageArtifact(t, root, second.ArtifactID)
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	require.NoError(t, store.Put(second))

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1, "a second Put must replace the directive, not add one")
	assert.Equal(t, second.ArtifactID, all[0].ArtifactID)

	got, found, err := store.Take(first.ServiceID, root)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, second.ArtifactID, got.ArtifactID)
}

// TestPendingRestoresTakeIsAtomic is the point of the rename: two tasks of the same
// service can start at the same instant, and the artifact restores once. Every
// round puts one directive and starts every take on one barrier; exactly one must
// win, and the others must answer not-found rather than an error.
func TestPendingRestoresTakeIsAtomic(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	root := newPendingStaging(t)
	const racers = 8
	const rounds = 25

	for round := 0; round < rounds; round++ {
		require.NoError(t, store.Put(pendingFixture(root)))

		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan bool, racers)
		errs := make(chan error, racers)

		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, found, err := store.Take("svc-1", root)
				if err != nil {
					errs <- err
					return
				}
				results <- found
			}()
		}

		close(start)
		wg.Wait()
		close(results)
		close(errs)

		for err := range errs {
			require.NoError(t, err, "a lost race is not an error")
		}
		won := 0
		for found := range results {
			if found {
				won++
			}
		}
		assert.Equal(t, 1, won, "round %d: exactly one take may consume the directive", round)
	}
}

func TestPendingRestoresTakeRefusesAnExpiredDirective(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)

	stale := pendingFixture(root)
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, store.Put(stale))

	ignored, found, err := store.Take(stale.ServiceID, root)
	require.NoError(t, err)
	assert.False(t, found, "a directive past its TTL must not restore anything")
	// The record travels with the not-found answer so the caller that ignored it can say
	// which service and artifact it ignored. found is the only answer to act on.
	assert.Equal(t, stale.ServiceID, ignored.ServiceID, "an expired directive is reported, not silently dropped")
	assert.Equal(t, stale.ArtifactID, ignored.ArtifactID)
	assert.Equal(t, stale.CreatedAt, ignored.CreatedAt)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "an expired directive Take meets is deleted, not left to expire twice")

	// Expiry is judged before the directory: a migration whose artifact was already
	// discarded is an old directive, not a broken one, so it is a clean not-found.
	goneRoot := newPendingStaging(t)
	gone := pendingFixture(goneRoot)
	gone.ServiceID = "svc-gone"
	gone.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, store.Put(gone))
	require.NoError(t, os.RemoveAll(gone.Dir))

	_, found, err = store.Take(gone.ServiceID, goneRoot)
	require.NoError(t, err, "an expired directive whose artifact is gone is simply expired")
	assert.False(t, found)

	// The boundary is the TTL itself: a directive that is still inside it is usable.
	fresh := pendingFixture(root)
	fresh.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL + time.Minute)
	require.NoError(t, store.Put(fresh))

	_, found, err = store.Take(fresh.ServiceID, root)
	require.NoError(t, err)
	assert.True(t, found)
}

// TestPendingRestoresDeleteArtifact is ruling I1(b): the removal is artifact-scoped,
// because two managers can migrate the same service and the loser's cleanup must not
// delete the winner's directive while the winner still reports "moved".
func TestPendingRestoresDeleteArtifact(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	require.NoError(t, store.Put(pendingFixture(root)))

	// Another artifact's removal is refused and leaves the directive alone.
	err := store.DeleteArtifact("svc-1", "mig-fedcba9876543210")
	require.ErrorIs(t, err, ErrDirectiveReplaced)
	assert.Contains(t, err.Error(), pendingTestArtifact, "the refusal says which artifact the service waits for")
	assert.FileExists(t, filepath.Join(dir, "svc-1"+pendingRecordSuffix),
		"a mismatched removal is not a silent success: the directive stays")

	// The owner's removal wins.
	require.NoError(t, store.DeleteArtifact("svc-1", pendingTestArtifact))
	require.NoFileExists(t, filepath.Join(dir, "svc-1"+pendingRecordSuffix))

	// Idempotent: there is no directive left, and that is the answer the retried
	// cleanup needs.
	require.NoError(t, store.DeleteArtifact("svc-1", pendingTestArtifact))
	require.NoError(t, store.DeleteArtifact("never-prepared", pendingTestArtifact))

	// An artifact ID is required: the whole point is that the caller says which
	// directive it owns.
	require.Error(t, store.DeleteArtifact("svc-1", ""))
}

// TestPendingRestoresDeleteArtifactLeavesACorruptRecordGone pins the one exception:
// a record this build cannot decode names no artifact and can restore nothing, so it
// goes with the claim instead of wedging a cleanup.
func TestPendingRestoresDeleteArtifactLeavesACorruptRecordGone(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, []byte("{not a directive"), 0o600))

	require.NoError(t, store.DeleteArtifact("svc-1", pendingTestArtifact))
	require.NoFileExists(t, path)
}

// TestPendingRestoresDeleteArtifactDoesNotClobberANewerDirective pins the interleaving
// the claim exists for: a prepare lands while the removal is deciding, so the claimed
// record is dropped rather than linked back over the newer directive.
func TestPendingRestoresDeleteArtifactDoesNotClobberANewerDirective(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	require.NoError(t, store.Put(pendingFixture(root)))

	newer := pendingFixture(root)
	newer.ArtifactID = "mig-fedcba9876543210"
	newer.Dir = stageArtifact(t, root, newer.ArtifactID)
	newerBody, err := json.Marshal(newer)
	require.NoError(t, err)

	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	original := linkPendingRecord
	interleaved := false
	linkPendingRecord = func(oldname, newname string) error {
		if !interleaved {
			interleaved = true
			require.NoError(t, os.WriteFile(path, newerBody, 0o600))
		}
		return original(oldname, newname)
	}
	t.Cleanup(func() { linkPendingRecord = original })

	// The removal asks for an artifact the claimed directive does not name, so the
	// refusal path (the put-back) is the one under test.
	require.ErrorIs(t, store.DeleteArtifact("svc-1", "mig-0000000000000000"), ErrDirectiveReplaced)
	assert.True(t, interleaved, "the test must have written a newer directive during the removal")

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	var got PendingRestore
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, newer.ArtifactID, got.ArtifactID, "the newer directive is what the node keeps")
}

func TestPendingRestoresTakeMissing(t *testing.T) {
	store, _ := newTestPendingRestores(t)

	got, found, err := store.Take("never-prepared", newPendingStaging(t))
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, PendingRestore{}, got)
}

// TestPendingRestoresCorruptRecord proves the ruling the restored registry makes
// too: a record this build cannot use is evidence of what the node believes, so it
// is reported by path and left on disk for a human, not deleted. Take must also put
// back the file it claimed - a corrupt directive is not consumed by the attempt.
func TestPendingRestoresCorruptRecord(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, []byte("{not a directive"), 0o600))

	_, found, err := store.Take("svc-1", root)
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), path, "the error names the file a human must inspect")
	assert.FileExists(t, path, "a corrupt record is left in place, not consumed by a failed Take")

	_, skipped, listErr := store.List()
	require.NoError(t, listErr)
	assert.Equal(t, []string{path}, skipped)

	// DeleteArtifact cannot decode it either, and removes it with its claim, so a bad
	// file cannot wedge a rollback's cleanup.
	require.NoError(t, store.DeleteArtifact("svc-1", pendingTestArtifact))
	require.NoFileExists(t, path)
}

// TestPendingRestoresRefusedRecordDoesNotOverwriteANewerDirective is ruling I6: the
// put-back of a refused record is the one write Take makes, and the manager may have
// prepared the service again while the take was reading. The newer directive wins; the
// record this take refused is dropped with its claim.
func TestPendingRestoresRefusedRecordDoesNotOverwriteANewerDirective(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, []byte("{not a directive"), 0o600))

	// The newer directive, written the way a retried prepare writes one.
	newer := pendingFixture(root)
	newer.ArtifactID = "mig-fedcba9876543210"
	newer.Dir = stageArtifact(t, root, newer.ArtifactID)
	newerBody, err := json.Marshal(newer)
	require.NoError(t, err)

	// The interleaving itself: the prepare lands after Take claimed the record and
	// before Take puts the refused one back.
	original := linkPendingRecord
	interleaved := false
	linkPendingRecord = func(oldname, newname string) error {
		if !interleaved {
			interleaved = true
			require.NoError(t, os.WriteFile(path, newerBody, 0o600))
		}
		return original(oldname, newname)
	}
	t.Cleanup(func() { linkPendingRecord = original })

	_, found, err := store.Take("svc-1", root)
	require.Error(t, err, "the record that was claimed is still refused")
	assert.False(t, found)
	assert.True(t, interleaved, "the test must have written a newer directive during the take")

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	var got PendingRestore
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, newer.ArtifactID, got.ArtifactID,
		"the newer directive is what the node holds: the refused record does not replace it")

	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	require.Len(t, entries, 1, "the claim is dropped with the record it held")
	assert.Equal(t, "svc-1"+pendingRecordSuffix, entries[0].Name())
}

func TestPendingRestoresTakeRefusesAMisdirectedRecord(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	other := pendingFixture(root)
	other.ServiceID = "svc-2"

	body, err := json.Marshal(other)
	require.NoError(t, err)
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	_, found, err := store.Take("svc-1", root)
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), path)
	assert.FileExists(t, path)
}

// TestPendingRestoresRePutKeepsTheOriginalAge pins the retry ruling I3 exists for:
// Take is destructive, so a caller whose import failed keeps the record it took and
// Puts it back with the same CreatedAt. The store never re-stamps the age, so a
// retried directive does not win a second TTL.
func TestPendingRestoresRePutKeepsTheOriginalAge(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	root := newPendingStaging(t)

	record := pendingFixture(root)
	// Inside the ten-minute TTL, older than the five-minute sweep below.
	record.CreatedAt = time.Now().UTC().Add(-6 * time.Minute)
	require.NoError(t, store.Put(record))

	taken, found, err := store.Take(record.ServiceID, root)
	require.NoError(t, err)
	require.True(t, found)

	// The import failed after the take: the record goes back as it came.
	require.NoError(t, store.Put(taken))

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1)
	assert.True(t, record.CreatedAt.Equal(all[0].CreatedAt),
		"a re-put record keeps its CreatedAt: %v, want %v", all[0].CreatedAt, record.CreatedAt)

	// The age is the record's, not the instant it was re-written: a Put that stamped
	// "now" would put the directive back at the start of every sweep window.
	removed, err := store.Sweep(5 * time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, removed, "the directive is six minutes old, not fresh")
}

func TestPendingRestoresList(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)

	empty, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Empty(t, skipped)

	for _, serviceID := range []string{"svc-c", "svc-a", "svc-b"} {
		rec := pendingFixture(root)
		rec.ServiceID = serviceID
		require.NoError(t, store.Put(rec))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "svc-bad"+pendingRecordSuffix), []byte("{}"), 0o600))
	// A claim and a directory are not directives; neither may be listed or reported.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".svc-a-123.taken"), []byte("{}"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "svc-d"+pendingRecordSuffix), 0o700))

	all, skipped, err := store.List()
	require.NoError(t, err)
	require.Len(t, all, 3)
	ids := make([]string, 0, len(all))
	for _, rec := range all {
		ids = append(ids, rec.ServiceID)
	}
	assert.Equal(t, []string{"svc-a", "svc-b", "svc-c"}, ids, "List is sorted so startup reconciliation is deterministic")
	assert.True(t, sort.StringsAreSorted(ids))
	assert.Equal(t, []string{filepath.Join(dir, "svc-bad"+pendingRecordSuffix)}, skipped)

	// Age is not a usability failure: an expired directive is still what the node
	// believes, and Sweep - not List - removes it.
	stale := pendingFixture(root)
	stale.ServiceID = "svc-stale"
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Hour)
	require.NoError(t, store.Put(stale))

	all, _, err = store.List()
	require.NoError(t, err)
	assert.Len(t, all, 4)
}

func TestPendingRestoresSweep(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	root := newPendingStaging(t)

	stale := pendingFixture(root)
	stale.ServiceID = "svc-stale"
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, store.Put(stale))

	staler := pendingFixture(root)
	staler.ServiceID = "svc-staler"
	staler.CreatedAt = time.Now().UTC().Add(-24 * time.Hour)
	require.NoError(t, store.Put(staler))

	fresh := pendingFixture(root)
	require.NoError(t, store.Put(fresh))

	removed, err := store.Sweep(DefaultPendingRestoreTTL)
	require.NoError(t, err)
	assert.Equal(t, 2, removed, "both directives past the sweep age go")

	all, _, err := store.List()
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, fresh.ServiceID, all[0].ServiceID)

	// A negative age is a caller error, not "sweep everything".
	_, err = store.Sweep(-time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")
}

// TestPendingRestoresSweepReclaimsAbandonedClaims covers the crash window between
// Take's rename and its read: the claim is a file of its own, and an abandoned one
// must not accumulate for ever.
func TestPendingRestoresSweepReclaimsAbandonedClaims(t *testing.T) {
	store, dir := newTestPendingRestores(t)

	claim := filepath.Join(dir, ".svc-1-1234.taken")
	require.NoError(t, os.WriteFile(claim, []byte("{}"), 0o600))
	old := time.Now().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, os.Chtimes(claim, old, old))

	removed, err := store.Sweep(DefaultPendingRestoreTTL)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	require.NoFileExists(t, claim)
}

func TestPendingRestoresRejectsBadDirectives(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PendingRestore)
		wantSub string
	}{
		{
			name:    "no service ID",
			mutate:  func(r *PendingRestore) { r.ServiceID = "" },
			wantSub: "serviceId is empty",
		},
		{
			name:    "service ID with a path separator",
			mutate:  func(r *PendingRestore) { r.ServiceID = "svc/1" },
			wantSub: "serviceId",
		},
		{
			name:    "service ID with a dot",
			mutate:  func(r *PendingRestore) { r.ServiceID = ".." },
			wantSub: "serviceId",
		},
		{
			name:    "service ID with whitespace",
			mutate:  func(r *PendingRestore) { r.ServiceID = "svc 1" },
			wantSub: "serviceId",
		},
		{
			name:    "service ID past the file name limit",
			mutate:  func(r *PendingRestore) { r.ServiceID = strings.Repeat("s", MaxIDLength+1) },
			wantSub: "serviceId",
		},
		{
			name:    "no artifact ID",
			mutate:  func(r *PendingRestore) { r.ArtifactID = " " },
			wantSub: "artifactId is empty",
		},
		{
			name:    "no directory",
			mutate:  func(r *PendingRestore) { r.Dir = "" },
			wantSub: "dir is empty",
		},
		{
			name:    "relative directory",
			mutate:  func(r *PendingRestore) { r.Dir = "migrations/mig-0123456789abcdef" },
			wantSub: "dir",
		},
		{
			name:    "no creation time",
			mutate:  func(r *PendingRestore) { r.CreatedAt = time.Time{} },
			wantSub: "createdAt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, dir := newTestPendingRestores(t)
			rec := pendingFixture(newPendingStaging(t))
			tc.mutate(&rec)

			err := store.Put(rec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub,
				"the error must name the field that is wrong: %v", err)

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "a refused directive writes nothing")
		})
	}
}

func TestPendingRestoresZeroValueIsRefused(t *testing.T) {
	var zero PendingRestores

	require.Error(t, zero.Put(pendingFixture(newPendingStaging(t))))
	_, _, err := zero.Take("svc-1", t.TempDir())
	require.Error(t, err)
	require.Error(t, zero.DeleteArtifact("svc-1", pendingTestArtifact))
	_, _, err = zero.List()
	require.Error(t, err)
	_, err = zero.Sweep(time.Minute)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NewPendingRestores")
}

// TestPendingRestoresRestart proves the directives outlive the daemon process: a
// second store over the same directory consumes what the first wrote.
func TestPendingRestoresRestart(t *testing.T) {
	first, dir := newTestPendingRestores(t)
	root := newPendingStaging(t)
	want := pendingFixture(root)
	require.NoError(t, first.Put(want))

	second, err := NewPendingRestores(dir)
	require.NoError(t, err)

	got, found, err := second.Take(want.ServiceID, root)
	require.NoError(t, err)
	require.True(t, found, "a restarted daemon must still honour a prepare it received before the restart")
	assert.Equal(t, want, got)
}
