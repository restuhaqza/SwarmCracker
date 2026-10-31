package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// spoolArtifactID is one well-formed artifact ID for the store's tests.
const spoolArtifactID = "mig-0123456789abcdef"

// spoolOtherArtifactID is a second, so a test can prove one artifact's sweep or
// removal cannot reach another's.
const spoolOtherArtifactID = "mig-fedcba9876543210"

// spoolBody returns a deterministic body of size bytes. It is generated rather
// than repeated so a size or digest mistake shows up as different bytes, not as a
// prefix that happens to match.
func spoolBody(size int, seed byte) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i*7) + seed
	}
	return body
}

// spoolFile describes body as a manifest entry under name.
func spoolFile(name string, body []byte) File {
	sum := sha256.Sum256(body)
	return File{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

// newTestStore builds a store over a fresh directory with a clock the test owns.
// The clock starts at the wall clock, so a fixture written a moment ago is
// younger than any TTL a test then applies by aging a path with os.Chtimes.
func newTestStore(t *testing.T, ttl time.Duration) (*Store, *time.Time) {
	t.Helper()

	now := time.Now()
	store, err := NewStore(StoreConfig{
		Root: filepath.Join(t.TempDir(), SpoolDirName),
		TTL:  ttl,
		Now:  func() time.Time { return now },
	})
	require.NoError(t, err)
	return store, &now
}

// writeSpoolFile spools one file the way the relay does and requires it to land.
func writeSpoolFile(t *testing.T, store *Store, artifactID string, file File, body []byte) {
	t.Helper()

	writer, err := store.Writer(artifactID, file)
	require.NoError(t, err)
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}

// requireCode asserts a typed failure's code and reason.
func requireCode(t *testing.T, err error, code output.Code, reason string) *output.Error {
	t.Helper()

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, code, typed.Code, typed.Message)
	if reason != "" {
		require.Equal(t, reason, typed.Details["reason"], typed.Message)
	}
	return typed
}

// TestNewStoreValidatesItsConfiguration pins what a daemon must pass and what it
// gets when it does not: a relative root would follow the process's working
// directory rather than its state, and a negative TTL would sweep everything the
// moment it ran.
func TestNewStoreValidatesItsConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		cfg     StoreConfig
		wantErr string
	}{
		{name: "no root", cfg: StoreConfig{}, wantErr: "the spool root is required"},
		{name: "relative root", cfg: StoreConfig{Root: "spool"}, wantErr: "is relative"},
		{name: "negative ttl", cfg: StoreConfig{Root: "/var/lib/swarmcracker/" + SpoolDirName, TTL: -time.Second}, wantErr: "is negative"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStore(tc.cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestNewStoreDefaults pins the two defaults: the TTL is the migration's own
// rollback window, and the clock is the wall clock.
func TestNewStoreDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), SpoolDirName)
	store, err := NewStore(StoreConfig{Root: root})
	require.NoError(t, err)

	assert.Equal(t, root, store.Root())
	assert.Equal(t, DefaultSpoolTTL, store.TTL())
}

// TestSpoolTTLOutlastsATransfer pins the one relationship the two durations have:
// an artifact the manager is still allowed to be moving must never be swept, so
// the resting window is the transfer deadline plus a margin rather than a second
// number that happens to be shorter.
func TestSpoolTTLOutlastsATransfer(t *testing.T) {
	assert.Equal(t, 30*time.Minute, DefaultTransferDeadline)
	assert.Greater(t, DefaultSpoolTTL, DefaultTransferDeadline,
		"the spool TTL must outlast the transfer a relay is allowed to take")
	assert.GreaterOrEqual(t, DefaultSpoolTTL, DefaultTransferDeadline+time.Minute,
		"and it must leave room after the transfer, not merely tie it")
}

// TestSpoolRootFollowsTheStateDirectory pins the one rule for where the spool
// lives, so the daemon and this package cannot disagree.
func TestSpoolRootFollowsTheStateDirectory(t *testing.T) {
	assert.Equal(t, "", SpoolRoot(""))
	assert.Equal(t, "", SpoolRoot("   "))
	assert.Equal(t, filepath.Join("/var/lib/swarmkit", "migration-spool"), SpoolRoot("/var/lib/swarmkit"))
}

// TestStoreRoundTripsOneFile is the spool's contract in one test: a file goes in,
// is the only thing in its artifact's directory, comes back byte for byte, and the
// directory is gone after Remove.
func TestStoreRoundTripsOneFile(t *testing.T) {
	store, _ := newTestStore(t, 0)
	body := spoolBody(1<<20, 3)
	file := spoolFile("vm.mem", body)

	writeSpoolFile(t, store, spoolArtifactID, file, body)

	dir := filepath.Join(store.Root(), spoolArtifactID)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "vm.mem", entries[0].Name())

	info, err := entries[0].Info()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a memory image is 0600")

	rootInfo, err := os.Stat(store.Root())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), rootInfo.Mode().Perm(), "the spool root is private")

	reader, err := store.Open(spoolArtifactID, file)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, body, got)

	require.NoError(t, store.Remove(spoolArtifactID))
	_, err = os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist), "Remove leaves no artifact directory")

	require.NoError(t, store.Remove(spoolArtifactID), "Remove is idempotent")
}

// TestStoreWriterRefusesASecondFile pins the spool's shape: one file per artifact
// at a time. A second file is refused while the slot is taken, whether the first
// writer is still open or its file is already published and waiting to be handed
// on, and the refusal names what is in the way.
func TestStoreWriterRefusesASecondFile(t *testing.T) {
	store, _ := newTestStore(t, 0)
	state := spoolFile("vm.state", spoolBody(64, 1))
	memory := spoolFile("vm.mem", spoolBody(128, 2))

	writer, err := store.Writer(spoolArtifactID, state)
	require.NoError(t, err)

	_, err = store.Writer(spoolArtifactID, memory)
	typed := requireCode(t, err, output.CodeConflict, "spool_occupied")
	assert.Equal(t, "vm.state", typed.Details["held"])

	_, err = store.Writer(spoolArtifactID, state)
	requireCode(t, err, output.CodeConflict, "spool_occupied")

	_, err = writer.Write(spoolBody(64, 1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	// The published file still occupies the slot: the caller hands it on and
	// removes it before the next one.
	_, err = store.Writer(spoolArtifactID, memory)
	requireCode(t, err, output.CodeConflict, "spool_occupied")

	require.NoError(t, store.Remove(spoolArtifactID))
	writeSpoolFile(t, store, spoolArtifactID, memory, spoolBody(128, 2))
}

// TestStoreWriterRefusesAFileItDidNotWrite pins the leftover case: a file in the
// artifact's directory that this writer is not replacing still occupies the slot,
// so a daemon that restarted mid-migration cannot be handed a second file beside
// the one it left behind.
func TestStoreWriterRefusesAFileItDidNotWrite(t *testing.T) {
	store, _ := newTestStore(t, 0)
	state := spoolFile("vm.state", spoolBody(64, 1))
	writeSpoolFile(t, store, spoolArtifactID, state, spoolBody(64, 1))

	leftover := filepath.Join(store.Root(), spoolArtifactID, "orphan.bin")
	require.NoError(t, os.WriteFile(leftover, []byte("stale"), 0o600))

	memory := spoolFile("vm.mem", spoolBody(128, 2))
	_, err := store.Writer(spoolArtifactID, memory)
	typed := requireCode(t, err, output.CodeConflict, "spool_occupied")
	assert.Equal(t, "orphan.bin", typed.Details["held"])
}

// TestStoreWriterAllowsOnlyOneConcurrentOwner pins the reservation: a writer that
// owns the slot keeps it, so no second writer can believe an empty-looking
// directory means the slot is free.
func TestStoreWriterAllowsOnlyOneConcurrentOwner(t *testing.T) {
	store, _ := newTestStore(t, 0)

	holder, err := store.Writer(spoolArtifactID, spoolFile("vm.state", spoolBody(8, 1)))
	require.NoError(t, err)

	const writers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	refused := 0

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Writer(spoolArtifactID, spoolFile("vm.mem", spoolBody(8, 2)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				refused++
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, writers, refused, "the writer that owns the slot keeps it")

	require.NoError(t, holder.Abort())

	writer, err := store.Writer(spoolArtifactID, spoolFile("vm.mem", spoolBody(8, 2)))
	require.NoError(t, err, "the slot is free once its writer has aborted")
	require.NoError(t, writer.Abort())
}

// TestStoreWriterRemovesTheSlotOnMismatch pins the failure path: bytes that are
// not the declared length or digest publish nothing and leave nothing behind -
// not an artifact directory a restore could read and not a temporary a later
// sweep would have to find.
func TestStoreWriterRemovesTheSlotOnMismatch(t *testing.T) {
	body := spoolBody(64, 5)

	cases := []struct {
		name   string
		file   File
		write  []byte
		code   output.Code
		reason string
	}{
		{
			name:   "short body",
			file:   spoolFile("vm.mem", body),
			write:  body[:len(body)-1],
			code:   output.CodeInvalid,
			reason: "size_mismatch",
		},
		{
			name:   "body longer than the manifest",
			file:   File{Name: "vm.mem", Size: 8, SHA256: spoolFile("vm.mem", body).SHA256},
			write:  body,
			code:   output.CodeInvalid,
			reason: "size_mismatch",
		},
		{
			name:   "digest does not match",
			file:   spoolFile("vm.mem", spoolBody(64, 6)),
			write:  body,
			code:   output.CodeConflict,
			reason: "checksum_mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newTestStore(t, 0)

			writer, err := store.Writer(spoolArtifactID, tc.file)
			require.NoError(t, err)
			_, writeErr := writer.Write(tc.write)
			closeErr := writer.Close()

			// A write that is too long fails at the write; every other mismatch
			// is caught when the file is closed.
			if writeErr == nil {
				require.Error(t, closeErr)
				requireCode(t, closeErr, tc.code, tc.reason)
			} else {
				requireCode(t, writeErr, tc.code, tc.reason)
			}

			_, statErr := os.Stat(filepath.Join(store.Root(), spoolArtifactID))
			assert.True(t, errors.Is(statErr, os.ErrNotExist), "a refused file leaves no artifact directory")

			partials, err := filepath.Glob(filepath.Join(store.Root(), spoolPartialPrefix+"*"))
			require.NoError(t, err)
			assert.Empty(t, partials, "a refused file leaves no temporary")
		})
	}
}

// TestStoreAbortPublishesNothing pins the other failure path: a transfer the
// caller has already given up on - the source answered the wrong digest - cannot
// publish a complete-looking file just because the bytes it did write were valid.
func TestStoreAbortPublishesNothing(t *testing.T) {
	store, _ := newTestStore(t, 0)
	body := spoolBody(64, 7)
	file := spoolFile("vm.mem", body)

	writer, err := store.Writer(spoolArtifactID, file)
	require.NoError(t, err)
	_, err = writer.Write(body)
	require.NoError(t, err)

	require.NoError(t, writer.Abort())
	require.NoError(t, writer.Abort(), "Abort is idempotent")
	require.NoError(t, writer.Close(), "Close after Abort publishes nothing")

	_, statErr := os.Stat(filepath.Join(store.Root(), spoolArtifactID))
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

// TestStoreOpenRefusesAChangedFile pins the read side: a spooled file is checked
// against the manifest again when it is opened and again as it is read, so a file
// that changed between the two hops fails the second one instead of reaching the
// target under a checksum that describes something else.
func TestStoreOpenRefusesAChangedFile(t *testing.T) {
	body := spoolBody(64, 8)

	t.Run("size changed", func(t *testing.T) {
		store, _ := newTestStore(t, 0)
		file := spoolFile("vm.mem", body)
		writeSpoolFile(t, store, spoolArtifactID, file, body)

		require.NoError(t, os.WriteFile(filepath.Join(store.Root(), spoolArtifactID, "vm.mem"), []byte("short"), 0o600))

		_, err := store.Open(spoolArtifactID, file)
		requireCode(t, err, output.CodeConflict, "size_mismatch")
	})

	t.Run("digest changed", func(t *testing.T) {
		store, _ := newTestStore(t, 0)
		file := spoolFile("vm.mem", body)
		writeSpoolFile(t, store, spoolArtifactID, file, body)

		changed := spoolBody(64, 9)
		require.NoError(t, os.WriteFile(filepath.Join(store.Root(), spoolArtifactID, "vm.mem"), changed, 0o600))

		reader, err := store.Open(spoolArtifactID, file)
		require.NoError(t, err)
		_, err = io.ReadAll(reader)
		requireCode(t, err, output.CodeConflict, "checksum_mismatch")
		require.NoError(t, reader.Close())
	})
}

// TestStoreOpenRefusesWhatIsNotAnArtifact pins the directory rule: only a real
// directory answers for an artifact, and only a real file inside it is read. A
// symbolic link in either place would serve bytes from outside the spool under
// this artifact's name.
func TestStoreOpenRefusesWhatIsNotAnArtifact(t *testing.T) {
	body := spoolBody(32, 10)
	file := spoolFile("vm.mem", body)

	t.Run("missing artifact", func(t *testing.T) {
		store, _ := newTestStore(t, 0)
		_, err := store.Open(spoolArtifactID, file)
		requireCode(t, err, output.CodeNotFound, "spool_missing")
	})

	t.Run("artifact directory is a symlink", func(t *testing.T) {
		store, _ := newTestStore(t, 0)
		real := t.TempDir()
		require.NoError(t, os.MkdirAll(store.Root(), 0o700))
		require.NoError(t, os.Symlink(real, filepath.Join(store.Root(), spoolArtifactID)))

		_, err := store.Open(spoolArtifactID, file)
		requireCode(t, err, output.CodeNotFound, "spool_missing")

		_, err = store.Writer(spoolArtifactID, file)
		requireCode(t, err, output.CodeConflict, "spool_changed")
	})

	t.Run("entry is a symlink", func(t *testing.T) {
		store, _ := newTestStore(t, 0)
		writeSpoolFile(t, store, spoolArtifactID, file, body)

		dir := filepath.Join(store.Root(), spoolArtifactID)
		target := filepath.Join(t.TempDir(), "elsewhere")
		require.NoError(t, os.WriteFile(target, body, 0o600))
		require.NoError(t, os.Remove(filepath.Join(dir, "vm.mem")))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, "vm.mem")))

		_, err := store.Open(spoolArtifactID, file)
		requireCode(t, err, output.CodeNotFound, "spool_missing")
	})
}

// TestStoreRemoveSweepsPartials pins cleanup of a write that crashed: the
// temporary carries the artifact ID precisely so the artifact's own removal can
// find it, and it is removed whether or not the artifact directory exists.
func TestStoreRemoveSweepsPartials(t *testing.T) {
	store, _ := newTestStore(t, 0)
	require.NoError(t, os.MkdirAll(store.Root(), 0o700))

	partial := filepath.Join(store.Root(), spoolPartialPrefix+spoolArtifactID+"-123456")
	require.NoError(t, os.WriteFile(partial, []byte("half"), 0o600))

	otherPartial := filepath.Join(store.Root(), spoolPartialPrefix+spoolOtherArtifactID+"-123456")
	require.NoError(t, os.WriteFile(otherPartial, []byte("half"), 0o600))

	require.NoError(t, store.Remove(spoolArtifactID))

	_, err := os.Stat(partial)
	assert.True(t, errors.Is(err, os.ErrNotExist), "the artifact's own partial is removed")
	_, err = os.Stat(otherPartial)
	assert.NoError(t, err, "another artifact's partial is not")
}

// TestStoreSweepRemovesOnlyStaleEntries pins the TTL sweep: what a migration
// abandoned goes, what is in flight stays, and an entry this package did not
// create is never touched.
func TestStoreSweepRemovesOnlyStaleEntries(t *testing.T) {
	store, now := newTestStore(t, time.Hour)
	body := spoolBody(16, 11)

	writeSpoolFile(t, store, spoolArtifactID, spoolFile("vm.mem", body), body)
	writeSpoolFile(t, store, spoolOtherArtifactID, spoolFile("vm.mem", body), body)

	stale := now.Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(store.Root(), spoolOtherArtifactID), stale, stale))

	stalePartial := filepath.Join(store.Root(), spoolPartialPrefix+spoolArtifactID+"-crash")
	require.NoError(t, os.WriteFile(stalePartial, []byte("half"), 0o600))
	require.NoError(t, os.Chtimes(stalePartial, stale, stale))

	freshPartial := filepath.Join(store.Root(), spoolPartialPrefix+spoolOtherArtifactID+"-writing")
	require.NoError(t, os.WriteFile(freshPartial, []byte("half"), 0o600))

	notOurs := filepath.Join(store.Root(), "README")
	require.NoError(t, os.WriteFile(notOurs, []byte("not a migration"), 0o600))
	require.NoError(t, os.Chtimes(notOurs, stale, stale))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 2, removed)

	_, err = os.Stat(filepath.Join(store.Root(), spoolArtifactID))
	assert.NoError(t, err, "an artifact younger than the TTL stays")

	_, err = os.Stat(filepath.Join(store.Root(), spoolOtherArtifactID))
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(stalePartial)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	_, err = os.Stat(freshPartial)
	assert.NoError(t, err, "a write in progress is not swept")
	_, err = os.Stat(notOurs)
	assert.NoError(t, err, "an entry the spool did not create is left alone")
}

// TestStoreSweepReachesTheTTLBoundary pins the age rule: an entry exactly one TTL
// old is stale, and a second younger than that is not.
func TestStoreSweepReachesTheTTLBoundary(t *testing.T) {
	store, now := newTestStore(t, time.Hour)
	body := spoolBody(16, 12)
	writeSpoolFile(t, store, spoolArtifactID, spoolFile("vm.mem", body), body)

	dir := filepath.Join(store.Root(), spoolArtifactID)
	boundary := now.Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, boundary, boundary))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 1, removed, "an entry exactly one TTL old is stale")

	writeSpoolFile(t, store, spoolArtifactID, spoolFile("vm.mem", body), body)
	younger := now.Add(-59 * time.Minute)
	require.NoError(t, os.Chtimes(dir, younger, younger))

	removed, err = store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

// TestStoreSweepSkipsAHeldWriter pins the one race the TTL could lose: a transfer
// longer than the TTL is still a transfer, and sweeping it would pull the file out
// from under the writer that is filling it.
func TestStoreSweepSkipsAHeldWriter(t *testing.T) {
	store, now := newTestStore(t, time.Minute)
	file := spoolFile("vm.mem", spoolBody(16, 13))

	writer, err := store.Writer(spoolArtifactID, file)
	require.NoError(t, err)
	defer func() { _ = writer.Abort() }()

	dir := filepath.Join(store.Root(), spoolArtifactID)
	stale := now.Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, stale, stale))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)

	_, err = os.Stat(dir)
	assert.NoError(t, err, "the artifact being written is not swept")
}

// TestStoreSweepSkipsAHeldWritersTemporary pins the other half of that race: the
// writer's bytes live in a temporary beside the directory, so a sweep that only
// checked the directory would unlink the file under a transfer that is still
// running - and the writer's Close would then rename an anonymous inode and fail.
// The transfer must survive to publish.
func TestStoreSweepSkipsAHeldWritersTemporary(t *testing.T) {
	store, now := newTestStore(t, time.Minute)
	body := spoolBody(32, 16)
	file := spoolFile("vm.mem", body)

	writer, err := store.Writer(spoolArtifactID, file)
	require.NoError(t, err)
	defer func() { _ = writer.Abort() }()

	// Half the transfer is on disk, and the sweep is run "after" the TTL has
	// passed for both the directory and the temporary.
	_, err = writer.Write(body[:16])
	require.NoError(t, err)

	stale := now.Add(-time.Hour)
	dir := filepath.Join(store.Root(), spoolArtifactID)
	require.NoError(t, os.Chtimes(writer.tmpPath, stale, stale))
	require.NoError(t, os.Chtimes(dir, stale, stale))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)

	_, err = os.Stat(writer.tmpPath)
	require.NoError(t, err, "the temporary of a writer that holds the slot is not swept")

	// The point of keeping it: the transfer finishes and publishes exactly the
	// file the artifact describes.
	_, err = writer.Write(body[16:])
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reader, err := store.Open(spoolArtifactID, file)
	require.NoError(t, err)
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, body, got, "a swept-mid-transfer file is still this artifact's file")
}

// TestStoreSweepSkipsAnArtifactBeingRead pins the read side of the same rule: a
// Stage that is streaming a spooled file for longer than the TTL must not have
// its directory removed underneath it, and the moment the reader closes the
// artifact is sweepable again.
func TestStoreSweepSkipsAnArtifactBeingRead(t *testing.T) {
	store, now := newTestStore(t, time.Minute)
	body := spoolBody(64, 17)
	file := spoolFile("vm.mem", body)
	writeSpoolFile(t, store, spoolArtifactID, file, body)

	reader, err := store.Open(spoolArtifactID, file)
	require.NoError(t, err)

	dir := filepath.Join(store.Root(), spoolArtifactID)
	stale := now.Add(-time.Hour)
	require.NoError(t, os.Chtimes(dir, stale, stale))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	_, err = os.Stat(dir)
	assert.NoError(t, err, "the artifact being staged is not swept")

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, body, got, "the reader still serves the file it opened")

	removed, err = store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 1, removed, "once the reader is closed the artifact is stale like any other")
	_, err = os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// TestStoreSweepStillReachesAnUnheldTemporary pins the other side of the partial
// rule: a temporary no writer holds is swept when it is stale, so the check that
// protects a live transfer does not turn the spool into a leak.
func TestStoreSweepStillReachesAnUnheldTemporary(t *testing.T) {
	store, now := newTestStore(t, time.Minute)
	require.NoError(t, os.MkdirAll(store.Root(), 0o700))

	partial := filepath.Join(store.Root(), spoolPartialPrefix+spoolArtifactID+"-crash")
	require.NoError(t, os.WriteFile(partial, []byte("half"), 0o600))
	stale := now.Add(-time.Hour)
	require.NoError(t, os.Chtimes(partial, stale, stale))

	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	_, err = os.Stat(partial)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// TestStoreSweepsAnAbsentRoot pins the first-run case: a manager that has never
// migrated has no spool directory, and a sweep of it is not an error.
func TestStoreSweepsAnAbsentRoot(t *testing.T) {
	store, _ := newTestStore(t, time.Minute)
	removed, err := store.Sweep()
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

// TestStoreRejectsInvalidInput pins the checks made before any path is built: the
// artifact ID addresses a directory and the name addresses a file inside it, so
// both are held to the same rules the nodes apply.
func TestStoreRejectsInvalidInput(t *testing.T) {
	store, _ := newTestStore(t, 0)
	valid := spoolFile("vm.mem", spoolBody(8, 14))

	cases := []struct {
		name       string
		artifactID string
		file       File
	}{
		{name: "artifact id with a separator", artifactID: "../mig-0123456789abcdef", file: valid},
		{name: "artifact id is not an artifact id", artifactID: "snap-0123456789abcdef", file: valid},
		{name: "empty artifact id", artifactID: "", file: valid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Writer(tc.artifactID, tc.file)
			requireCode(t, err, output.CodeInvalid, "invalid_artifact_id")

			_, err = store.Open(tc.artifactID, tc.file)
			requireCode(t, err, output.CodeInvalid, "invalid_artifact_id")

			err = store.Remove(tc.artifactID)
			requireCode(t, err, output.CodeInvalid, "invalid_artifact_id")
		})
	}

	// A file description is checked by the two operations that read or write
	// bytes; Remove addresses the directory and never looks inside it.
	files := []struct {
		name string
		file File
	}{
		{name: "file name with a separator", file: File{Name: "../vm.mem", Size: 8, SHA256: valid.SHA256}},
		{name: "file name is a directory", file: File{Name: "..", Size: 8, SHA256: valid.SHA256}},
		{name: "size is zero", file: File{Name: "vm.mem", SHA256: valid.SHA256}},
		{name: "size is past the ceiling", file: File{Name: "vm.mem", Size: MaxFileSize + 1, SHA256: valid.SHA256}},
		{name: "digest is short", file: File{Name: "vm.mem", Size: 8, SHA256: "abc"}},
		{name: "digest is uppercase", file: File{Name: "vm.mem", Size: 8, SHA256: strings.Repeat("A", 64)}},
	}

	for _, tc := range files {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Writer(spoolArtifactID, tc.file)
			requireCode(t, err, output.CodeInvalid, "invalid_file")

			_, err = store.Open(spoolArtifactID, tc.file)
			requireCode(t, err, output.CodeInvalid, "invalid_file")
		})
	}
}

// TestStoreReportsAnUnusableRoot pins the I/O failure path: a root this daemon
// cannot use is unavailable, and the message names the operation rather than the
// path the manager keeps its guests' images under.
func TestStoreReportsAnUnusableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(root, []byte("in the way"), 0o600))

	store, err := NewStore(StoreConfig{Root: root})
	require.NoError(t, err)

	_, err = store.Writer(spoolArtifactID, spoolFile("vm.mem", spoolBody(8, 15)))
	requireCode(t, err, output.CodeUnavailable, "spool_unavailable")

	_, err = store.Sweep()
	requireCode(t, err, output.CodeUnavailable, "spool_unavailable")
}

// TestStoreWriterRefusesAnEmptyFileName pins the one field the manifest's own
// validation and the transport must agree on, through the shared File.Validate.
func TestStoreWriterRefusesAnEmptyFileName(t *testing.T) {
	store, _ := newTestStore(t, 0)

	_, err := store.Writer(spoolArtifactID, File{Name: "", Size: 8, SHA256: strings.Repeat("a", 64)})
	requireCode(t, err, output.CodeInvalid, "invalid_file")
}
