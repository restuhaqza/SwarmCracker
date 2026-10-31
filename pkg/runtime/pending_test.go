package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingFixture is a usable directive: the next task of svc-1 to start on this
// node restores mig-0123456789abcdef from the directory the manager staged it in.
// CreatedAt is "now" because Take judges age against the wall clock, not against a
// fixed instant a test cannot move.
func pendingFixture() PendingRestore {
	return PendingRestore{
		ServiceID:  "svc-1",
		ArtifactID: "mig-0123456789abcdef",
		Dir:        filepath.Join("/var/lib/firecracker/migrations", "mig-0123456789abcdef"),
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
	want := pendingFixture()

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

	got, found, err := store.Take(want.ServiceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, want.ServiceID, got.ServiceID)
	assert.Equal(t, want.ArtifactID, got.ArtifactID)
	assert.Equal(t, want.Dir, got.Dir)
	assert.True(t, want.CreatedAt.Equal(got.CreatedAt), "createdAt = %v, want %v", got.CreatedAt, want.CreatedAt)

	// The directive is consumed once: a second task starting from the same service
	// must not restore the artifact again.
	_, found, err = store.Take(want.ServiceID)
	require.NoError(t, err)
	assert.False(t, found)

	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a consumed directive leaves neither its record nor its claim")
}

func TestPendingRestoresPutReplaces(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	first := pendingFixture()
	require.NoError(t, store.Put(first))

	// The manager retries a prepare; the second directive replaces the first
	// rather than queueing a second restore for the same service.
	second := first
	second.ArtifactID = "mig-fedcba9876543210"
	second.Dir = filepath.Join("/var/lib/firecracker/migrations", second.ArtifactID)
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	require.NoError(t, store.Put(second))

	all, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 1, "a second Put must replace the directive, not add one")
	assert.Equal(t, second.ArtifactID, all[0].ArtifactID)

	got, found, err := store.Take(first.ServiceID)
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
	const racers = 8
	const rounds = 25

	for round := 0; round < rounds; round++ {
		require.NoError(t, store.Put(pendingFixture()))

		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan bool, racers)
		errs := make(chan error, racers)

		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, found, err := store.Take("svc-1")
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

	stale := pendingFixture()
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, store.Put(stale))

	_, found, err := store.Take(stale.ServiceID)
	require.NoError(t, err)
	assert.False(t, found, "a directive past its TTL must not restore anything")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "an expired directive Take meets is deleted, not left to expire twice")

	// The boundary is the TTL itself: a directive that is still inside it is usable.
	fresh := pendingFixture()
	fresh.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL + time.Minute)
	require.NoError(t, store.Put(fresh))

	_, found, err = store.Take(fresh.ServiceID)
	require.NoError(t, err)
	assert.True(t, found)
}

func TestPendingRestoresDelete(t *testing.T) {
	store, _ := newTestPendingRestores(t)
	require.NoError(t, store.Put(pendingFixture()))

	require.NoError(t, store.Delete("svc-1"))
	_, found, err := store.Take("svc-1")
	require.NoError(t, err)
	assert.False(t, found)

	// Idempotent: a rollback and an abandoned migration may both discard the
	// directive, and neither may fail because the other ran first.
	require.NoError(t, store.Delete("svc-1"))
}

func TestPendingRestoresTakeMissing(t *testing.T) {
	store, _ := newTestPendingRestores(t)

	got, found, err := store.Take("never-prepared")
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
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, []byte("{not a directive"), 0o600))

	_, found, err := store.Take("svc-1")
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), path, "the error names the file a human must inspect")
	assert.FileExists(t, path, "a corrupt record is left in place, not consumed by a failed Take")

	_, skipped, listErr := store.List()
	require.NoError(t, listErr)
	assert.Equal(t, []string{path}, skipped)

	// Delete does not decode, so a bad file cannot wedge a rollback's cleanup.
	require.NoError(t, store.Delete("svc-1"))
	require.NoFileExists(t, path)
}

func TestPendingRestoresTakeRefusesAMisdirectedRecord(t *testing.T) {
	store, dir := newTestPendingRestores(t)
	other := pendingFixture()
	other.ServiceID = "svc-2"

	body, err := json.Marshal(other)
	require.NoError(t, err)
	path := filepath.Join(dir, "svc-1"+pendingRecordSuffix)
	require.NoError(t, os.WriteFile(path, body, 0o600))

	_, found, err := store.Take("svc-1")
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), path)
	assert.FileExists(t, path)
}

func TestPendingRestoresList(t *testing.T) {
	store, dir := newTestPendingRestores(t)

	empty, skipped, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, empty)
	assert.Empty(t, skipped)

	for _, serviceID := range []string{"svc-c", "svc-a", "svc-b"} {
		rec := pendingFixture()
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
	stale := pendingFixture()
	stale.ServiceID = "svc-stale"
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Hour)
	require.NoError(t, store.Put(stale))

	all, _, err = store.List()
	require.NoError(t, err)
	assert.Len(t, all, 4)
}

func TestPendingRestoresSweep(t *testing.T) {
	store, _ := newTestPendingRestores(t)

	stale := pendingFixture()
	stale.ServiceID = "svc-stale"
	stale.CreatedAt = time.Now().UTC().Add(-DefaultPendingRestoreTTL - time.Minute)
	require.NoError(t, store.Put(stale))

	staler := pendingFixture()
	staler.ServiceID = "svc-staler"
	staler.CreatedAt = time.Now().UTC().Add(-24 * time.Hour)
	require.NoError(t, store.Put(staler))

	fresh := pendingFixture()
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
			rec := pendingFixture()
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

	require.Error(t, zero.Put(pendingFixture()))
	_, _, err := zero.Take("svc-1")
	require.Error(t, err)
	require.Error(t, zero.Delete("svc-1"))
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
	want := pendingFixture()
	require.NoError(t, first.Put(want))

	second, err := NewPendingRestores(dir)
	require.NoError(t, err)

	got, found, err := second.Take(want.ServiceID)
	require.NoError(t, err)
	require.True(t, found, "a restarted daemon must still honour a prepare it received before the restart")
	assert.Equal(t, want, got)
}
