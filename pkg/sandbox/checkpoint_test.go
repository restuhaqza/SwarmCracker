package sandbox

// The checkpoint store's rules at the edges, and the paths a fork takes when a store
// refuses.
//
// The happy paths live in fork_test.go, beside the fork they belong to. What is here is
// what those tests never reach: a record a fork could not use, a directory that cannot be
// written, a chunk store that answers with an error. They are worth pinning for the same
// reason the success is: a checkpoint is an image a clone resumes, so a store that
// refuses quietly — or one whose refusal is reported as "not there" — is how a fork comes
// to resume something the node could not vouch for.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
)

// failingBlobs is the chunk store with one method failing on demand. It embeds the real
// store, so every method a test does not name still behaves as the node's does. list
// overrides what the store reports it holds, which is how a manifest read is reached on a
// store that has already given up its own.
type failingBlobs struct {
	checkpoint.Store
	list        []string
	errList     error
	errManifest error
	errDelete   error
	errPrune    error
}

func (f *failingBlobs) List(ctx context.Context) ([]string, error) {
	if f.errList != nil {
		return nil, f.errList
	}
	if f.list != nil {
		return f.list, nil
	}
	return f.Store.List(ctx)
}

func (f *failingBlobs) Manifest(ctx context.Context, id string) (checkpoint.Manifest, error) {
	if f.errManifest != nil {
		return checkpoint.Manifest{}, f.errManifest
	}
	return f.Store.Manifest(ctx, id)
}

func (f *failingBlobs) Delete(ctx context.Context, id string) error {
	if f.errDelete != nil {
		return f.errDelete
	}
	return f.Store.Delete(ctx, id)
}

func (f *failingBlobs) Prune(ctx context.Context, minAge time.Duration) (checkpoint.PruneReport, error) {
	if f.errPrune != nil {
		return checkpoint.PruneReport{}, f.errPrune
	}
	return f.Store.Prune(ctx, minAge)
}

// validCheckpoint is a record a fork could use, which is what the store rules are
// exercised against: each test breaks one field of this and nothing else.
func validCheckpoint(name string) CheckpointRecord {
	return CheckpointRecord{
		Name: name, Sandbox: "sbx-1", Template: "base@1", Kernel: "/boot/vmlinux",
		State: "/var/lib/x/state", Disk: "/var/lib/x/disk.ext4", Token: "0123456789abcdef",
		VsockPath: "/run/firecracker/sbx-1.vsock",
		Mem: checkpoint.Manifest{
			FormatVersion: checkpoint.FormatVersion, ID: name, TotalSize: 4,
			State:     checkpoint.State{Size: 2, SHA256: strings.Repeat("a", 64)},
			ChunkSize: checkpoint.ChunkSize,
			Chunks:    []checkpoint.Digest{checkpoint.Digest(strings.Repeat("b", 64))},
			Pin:       checkpoint.Pin{Firecracker: "vTEST", Kernel: strings.Repeat("c", 64)},
		},
	}
}

func newCheckpointDir(t *testing.T) (*CheckpointStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), DirNameCheckpoints)
	store, err := NewCheckpointStore(dir)
	require.NoError(t, err)
	return store, filepath.Clean(dir)
}

// TestCheckpointStoreRefusesARecordAForkCouldNotUse is the rule a store applies on write
// and again on read: a checkpoint a clone could not resume is refused by name, rather
// than stored and discovered by the fork that resumes it.
func TestCheckpointStoreRefusesARecordAForkCouldNotUse(t *testing.T) {
	store, _ := newCheckpointDir(t)

	cases := []struct {
		what string
		ruin func(*CheckpointRecord)
		says string
	}{
		{"a name that cannot name one", func(r *CheckpointRecord) { r.Name = "not a name!" }, "cannot name one"},
		{"no sandbox it came from", func(r *CheckpointRecord) { r.Sandbox = "  " }, "names the sandbox"},
		{"no template", func(r *CheckpointRecord) { r.Template = "" }, "names its template"},
		{"no kernel", func(r *CheckpointRecord) { r.Kernel = "" }, "names its template"},
		{"no vsock path", func(r *CheckpointRecord) { r.VsockPath = "" }, "vsock path"},
		{"no state document", func(r *CheckpointRecord) { r.State = "" }, "state document"},
		{"no disk copy", func(r *CheckpointRecord) { r.Disk = "" }, "state document"},
		{"no credential", func(r *CheckpointRecord) { r.Token = "" }, "agent token"},
		{"a memory image that cannot be read", func(r *CheckpointRecord) { r.Mem.Chunks = nil }, "memory image"},
	}
	for i, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			rec := validCheckpoint(fmt.Sprintf("c%d", i))
			tc.ruin(&rec)

			err := store.Put(rec)
			require.Error(t, err, tc.what)
			assert.Contains(t, err.Error(), tc.says)

			// And the record is not on disk: a refusal that still wrote would be
			// found by the next listing. A name that cannot name a checkpoint is
			// refused by the read too, which is the same rule seen twice.
			_, found, getErr := store.Get(rec.Name)
			if getErr == nil {
				assert.False(t, found, "a refused record is not a checkpoint")
			}
		})
	}
}

// TestCheckpointStoreOpensOnlyADirectoryItCanWrite pins the check the daemon makes at
// start: a node whose checkpoint directory cannot be written refuses to serve
// checkpoints, rather than accepting them and failing per call.
func TestCheckpointStoreOpensOnlyADirectoryItCanWrite(t *testing.T) {
	_, err := NewCheckpointStore("   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a directory is required")

	// A directory beneath a file cannot be created.
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, []byte("in the way"), 0o600))
	_, err = NewCheckpointStore(filepath.Join(file, "under-a-file"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")

	if os.Geteuid() == 0 {
		t.Skip("running as root: a directory that refuses writes cannot be built")
	}
	readonly := filepath.Join(t.TempDir(), "readonly")
	require.NoError(t, os.Mkdir(readonly, 0o500))
	_, err = NewCheckpointStore(readonly)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not writable")
}

// TestCheckpointStoreNamesWhatItOwns pins the two paths the rest of the node builds from:
// the directory checkpoints live in, and the one directory a checkpoint owns.
func TestCheckpointStoreNamesWhatItOwns(t *testing.T) {
	store, dir := newCheckpointDir(t)

	assert.Equal(t, dir, store.Dir())
	assert.Equal(t, filepath.Join(dir, "c1"), store.Path("c1"))
}

// TestCheckpointStoreRefusesARecordThatIsNotItsOwn pins the two reads that must be
// errors rather than "not found": a record a human moved into another checkpoint's
// directory, and one the node cannot read. Answering "no such checkpoint" to either
// would let a fork resume an image the node never validated.
func TestCheckpointStoreRefusesARecordThatIsNotItsOwn(t *testing.T) {
	store, _ := newCheckpointDir(t)
	require.NoError(t, store.Put(validCheckpoint("c1")))
	recordPath := filepath.Join(store.Path("c1"), "checkpoint.json")

	// The file names another checkpoint: one directory is one checkpoint.
	moved := validCheckpoint("elsewhere")
	body, err := json.Marshal(moved)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(recordPath, body, 0o600))

	_, _, err = store.Get("c1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one record is one checkpoint")

	// A name that cannot name a checkpoint is refused before the disk is read.
	_, _, err = store.Get("not a name!")
	require.Error(t, err)

	if os.Geteuid() != 0 {
		require.NoError(t, os.Chmod(recordPath, 0o000))
		_, _, err = store.Get("c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	}

	// A name this node has never recorded is not an error: it is simply not there.
	_, found, err := store.Get("never-written")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestCheckpointStoreReportsAWriteItCannotFinish pins the two failures a create meets on
// a node whose disk has given up: a path that cannot become a directory, and a rename
// that cannot land. Both must name what they were doing.
func TestCheckpointStoreReportsAWriteItCannotFinish(t *testing.T) {
	store, _ := newCheckpointDir(t)

	// A file where the checkpoint's directory belongs.
	require.NoError(t, os.WriteFile(store.Path("c1"), []byte("in the way"), 0o600))
	err := store.Put(validCheckpoint("c1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create")

	// A directory where the record belongs: the rename cannot land on it.
	require.NoError(t, store.Put(validCheckpoint("c2")))
	recordPath := filepath.Join(store.Path("c2"), "checkpoint.json")
	require.NoError(t, os.Remove(recordPath))
	require.NoError(t, os.Mkdir(recordPath, 0o700))

	err = store.Put(validCheckpoint("c2"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename into place")
}

// TestCheckpointStoreDeleteIsIdempotentAndBounded pins the rule a teardown relies on: a
// delete that runs twice is not an error, and a name that cannot name a checkpoint is
// refused rather than resolved against the directory.
func TestCheckpointStoreDeleteIsIdempotentAndBounded(t *testing.T) {
	store, _ := newCheckpointDir(t)
	require.NoError(t, store.Put(validCheckpoint("c1")))

	require.NoError(t, store.Delete("c1"))
	_, statErr := os.Stat(store.Path("c1"))
	assert.True(t, os.IsNotExist(statErr), "the whole directory goes with the record")

	// A teardown runs whether or not the checkpoint was ever published.
	assert.NoError(t, store.Delete("never-written"))

	assert.Error(t, store.Delete("not a name!"))
}

// TestCheckpointListingNarrowsToOneSandboxAndRefusesABrokenStore pins the listing both
// routes read: the node's own, one sandbox's, and the store that cannot be read at all —
// which is an error rather than an empty listing that looks like "no checkpoints".
func TestCheckpointListingNarrowsToOneSandboxAndRefusesABrokenStore(t *testing.T) {
	bare := newFixture(t)
	_, _, err := bare.manager.Checkpoints(sbxAnySandbox)
	assert.ErrorIs(t, err, ErrNotImplemented)

	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err = c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	// A checkpoint of a sandbox that no longer exists is exactly what the node-scoped
	// listing is for, so the record names a sandbox the store knows nothing about.
	elsewhere := validCheckpoint("c2")
	elsewhere.Sandbox = "sbx-2"
	require.NoError(t, c.manager.cfg.Checkpoints.Put(elsewhere))

	all, skipped, err := c.manager.Checkpoints(sbxAnySandbox)
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, all, 2)
	assert.Equal(t, []string{"c1", "c2"}, []string{all[0].Name, all[1].Name})

	narrowed, _, err := c.manager.Checkpoints("sbx-2")
	require.NoError(t, err)
	require.Len(t, narrowed, 1)
	assert.Equal(t, "c2", narrowed[0].Name)

	// A store whose directory is gone cannot be read, and saying "no checkpoints" would
	// be a lie an operator acts on.
	require.NoError(t, os.RemoveAll(c.manager.cfg.Checkpoints.Dir()))
	_, _, err = c.manager.Checkpoints(sbxAnySandbox)
	require.Error(t, err)
}

// sbxAnySandbox is the empty selector: every checkpoint on the node.
const sbxAnySandbox = ""

// TestCheckpointByNameAnswersWhatTheNodeHas pins the read a fork route resolves its input
// with: a name this node has, a name it does not, and a name that could never be one.
func TestCheckpointByNameAnswersWhatTheNodeHas(t *testing.T) {
	bare := newFixture(t)
	_, err := bare.manager.CheckpointByName("c1")
	assert.ErrorIs(t, err, ErrNotImplemented)

	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err = c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	got, err := c.manager.CheckpointByName("c1")
	require.NoError(t, err)
	assert.Equal(t, "sbx-1", got.Sandbox, "the record names the sandbox it was taken from")

	_, err = c.manager.CheckpointByName("absent")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = c.manager.CheckpointByName("   ")
	assert.ErrorIs(t, err, ErrInvalid)
}

// TestCheckpointRefusesASandboxItCannotImage pins the refusals a checkpoint makes before
// it spends a gigabyte of images: a sandbox that is not ready, one with no VMM to pause,
// one whose guest credential is gone, and a node whose own pin cannot be read. Each names
// the sandbox, because the operator's next question is which one.
func TestCheckpointRefusesASandboxItCannotImage(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)

	// Not ready: a sandbox still coming up has no guest that could be frozen.
	starting := c.readySandbox(t, "starting")
	starting.State = StateCreated
	require.NoError(t, c.store.Put(starting))
	_, err := c.manager.Checkpoint(context.Background(), "starting", "c1")
	assert.ErrorIs(t, err, ErrNotReady)

	// Ready, but with no running VMM: there is nothing to take an image of.
	c.readySandbox(t, "no-vmm")
	require.NoError(t, c.instances.Delete("no-vmm"))
	_, err = c.manager.Checkpoint(context.Background(), "no-vmm", "c2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no running VMM")

	// The guest's credential is gone, so the record a fork would need cannot be written.
	lonely := c.readySandbox(t, "no-token")
	require.NoError(t, os.Remove(lonely.AgentTokenFile))
	_, err = c.manager.Checkpoint(context.Background(), "no-token", "c3")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent token")

	// A node whose Firecracker binary is not there cannot say what it was built with, and
	// an image without that pin is one no fork could check.
	c.readySandbox(t, "no-pin")
	c.manager.cfg.FirecrackerBinary = filepath.Join(t.TempDir(), "absent-firecracker")
	_, err = c.manager.Checkpoint(context.Background(), "no-pin", "c4")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pin")

	// None of the four left a checkpoint behind.
	listed, _, err := c.manager.Checkpoints(sbxAnySandbox)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

// TestCheckpointNamesAreMintedOrRefused pins the rule that keeps a checkpoint addressable:
// a caller who names none gets one the node mints, a name already taken is refused rather
// than replacing an image another clone resumes, and a name that could never name one is
// refused before the store is asked.
func TestCheckpointNamesAreMintedOrRefused(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")

	minted, err := c.manager.allocateCheckpointName("")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(minted, "c-"), "a name the caller did not choose is minted: %s", minted)

	_, err = c.manager.allocateCheckpointName("not a name!")
	assert.ErrorIs(t, err, ErrInvalid)

	_, err = c.manager.Checkpoint(context.Background(), "sbx-1", "taken")
	require.NoError(t, err)
	_, err = c.manager.allocateCheckpointName("taken")
	assert.ErrorIs(t, err, ErrExists)

	if os.Geteuid() == 0 {
		t.Skip("running as root: a checkpoint directory that refuses reads cannot be built")
	}
	require.NoError(t, c.manager.cfg.Checkpoints.Put(validCheckpoint("unreadable")))
	require.NoError(t, os.Chmod(filepath.Dir(c.manager.cfg.Checkpoints.Path("unreadable")), 0o000))
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Dir(c.manager.cfg.Checkpoints.Path("unreadable")), 0o700)
	})

	// A store that cannot be read refuses the name: minting it blind would make a
	// second checkpoint of a name the node already has.
	_, err = c.manager.allocateCheckpointName("fresh")
	require.Error(t, err)
}

// TestForkFromSandboxTakesItsOwnCheckpoint is the one-step form the CLI's `fork <sandbox>`
// uses. It pins the two things a caller needs from it: the clone, and the name of the
// image it left behind — a fork whose checkpoint the caller cannot name is one they
// cannot free.
func TestForkFromSandboxTakesItsOwnCheckpoint(t *testing.T) {
	c := newCheckpointFixture(t)
	actions := stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")

	forks, err := c.manager.ForkFromSandbox(context.Background(), "sbx-1", ForkRequest{IDs: []string{"clone-1"}})
	require.NoError(t, err)
	require.Len(t, forks, 1)
	assert.Equal(t, StateReady, forks[0].State)
	assert.Contains(t, *actions, agent.QuiesceFreeze,
		"the sandbox is frozen for the checkpoint the fork takes of what it is running now")

	listed, _, err := c.manager.Checkpoints(sbxAnySandbox)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, strings.HasPrefix(listed[0].Name, "c-"), "the checkpoint is named: %s", listed[0].Name)
	assert.Equal(t, listed[0].Name, forks[0].ForkedFrom)
	assert.Equal(t, "sbx-1", listed[0].Sandbox)

	// A sandbox that is not there is refused before an image is written.
	attached := len(c.network.attached)
	_, err = c.manager.ForkFromSandbox(context.Background(), "absent", ForkRequest{})
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Len(t, c.network.attached, attached, "no clone is allocated for a sandbox that is not there")
}

// TestForkRefusesACheckpointWhoseImagesAreNotThere pins the failure a fork meets when the
// node's disk has lost what a record still names: the state document, and then the
// memory image behind it. Both are refusals the caller learns before a clone exists — a
// clone resumed from half an image is a guest that boots into garbage.
func TestForkRefusesACheckpointWhoseImagesAreNotThere(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")

	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	// The record names a state document the node does not have.
	stray := chk
	stray.State = filepath.Join(filepath.Dir(chk.State), "absent.state")
	require.NoError(t, c.manager.cfg.Checkpoints.Put(stray))

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "state document")
	assert.Empty(t, c.network.attached, "nothing is allocated for an image that cannot be staged")

	// The state document is back, and the memory manifest now names chunks the store
	// does not hold: the same count, so the manifest is well formed, and content
	// nothing stored.
	tampered, err := c.manager.CheckpointByName("c1")
	require.NoError(t, err)
	// The state document is back where the record expects it: what is missing now is the
	// memory image the manifest names.
	tampered.State = chk.State
	for i := range tampered.Mem.Chunks {
		tampered.Mem.Chunks[i] = checkpoint.Digest(strings.Repeat("d", 64))
	}
	require.NoError(t, c.manager.cfg.Checkpoints.Put(tampered))

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "c1", "the refusal names the checkpoint whose image is not there")
	assert.Empty(t, c.network.attached)

	// And the staging directory is not left behind by either refusal.
	entries, err := os.ReadDir(filepath.Join(c.manager.cfg.RootfsDir, DirName))
	require.NoError(t, err)
	assert.Empty(t, entries, "a staging directory that failed is removed with the failure")
}

// TestDeletingACheckpointReportsAStoreItCannotRead pins what a delete does when the chunk
// store will not answer: it fails rather than reporting a deletion whose freed and
// retained bytes it could not measure — an operator reading "freed" has to be reading a
// number the node actually knows.
func TestDeletingACheckpointReportsAStoreItCannotRead(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	for _, name := range []string{"c1", "c2"} {
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", name)
		require.NoError(t, err)
	}

	c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errList: errors.New("the store is unavailable")}
	_, err := c.manager.DeleteCheckpoint(context.Background(), "c1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list the checkpoints whose images remain")

	// The same delete when the listing works but a manifest cannot be read: the retained
	// figure is what the remaining checkpoints reference, so it cannot be guessed. The
	// store reports a manifest it then refuses to hand over, which is the state a delete
	// meets when the disk is failing rather than empty.
	c.manager.cfg.CheckpointBlobs = &failingBlobs{
		Store: c.blobs, list: []string{"c2"}, errManifest: errors.New("the manifest is unreadable"),
	}
	_, err = c.manager.DeleteCheckpoint(context.Background(), "c2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read the manifest")
}

// TestAFailedCheckpointPutsBackWhatItWroteEvenWhenTheStoreRefuses pins the cleanup a
// failed publish runs: it removes its manifest and reclaims the chunks nothing names, and
// a store that refuses either is logged rather than escalated — the caller is already
// returning the failure that brought it here.
func TestAFailedCheckpointPutsBackWhatItWroteEvenWhenTheStoreRefuses(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	// A manifest that will not go leaves the store as it was, and the pass still
	// returns: the checkpoint's own failure is what the caller hears about.
	c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errDelete: errors.New("the manifest will not go")}
	c.manager.discardCheckpointImages(context.Background(), "c1")
	ids, err := c.blobs.List(context.Background())
	require.NoError(t, err)
	assert.Contains(t, ids, "c1", "a manifest that could not be removed is still the store's")

	// A store that cannot reclaim is the same: logged, not fatal.
	c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errPrune: errors.New("the store cannot prune")}
	c.manager.discardCheckpointImages(context.Background(), "c1")

	// A node that keeps no checkpoints has nothing to put back.
	c.manager.cfg.CheckpointBlobs = nil
	c.manager.discardCheckpointImages(context.Background(), "c1")
}

// TestReconcileCheckpointsReportsAStoreItCannotPrune pins the daemon's start pass: a node
// that cannot reclaim the chunks a dead checkpoint left refuses the pass loudly, because
// the alternative is a node that reports a clean start while 4 MiB blocks it cannot name
// pile up.
func TestReconcileCheckpointsReportsAStoreItCannotPrune(t *testing.T) {
	bare := newFixture(t)
	_, err := bare.manager.ReconcileCheckpoints(context.Background())
	assert.ErrorIs(t, err, ErrNotImplemented)

	c := newCheckpointFixture(t)
	c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errPrune: errors.New("the store cannot prune")}
	_, err = c.manager.ReconcileCheckpoints(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reclaim the checkpoint chunks")
}

// TestMoveFileFallsBackWhenTheTwoPathsAreNotOneFilesystem pins the copy a checkpoint makes
// when the snapshot manager's directory and the checkpoint directory are on different
// filesystems — a node may be configured that way, and /dev/shm is the tmpfs every Linux
// node has for exactly this. The source must not survive the move either way.
func TestMoveFileFallsBackWhenTheTwoPathsAreNotOneFilesystem(t *testing.T) {
	shm, err := os.MkdirTemp("/dev/shm", "swarmcracker-move-")
	if err != nil {
		t.Skip("/dev/shm is not writable here: the cross-device path cannot be exercised")
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })

	from := filepath.Join(t.TempDir(), "rootfs.ext4")
	require.NoError(t, os.WriteFile(from, []byte("the checkpoint's disk"), 0o600))
	to := filepath.Join(shm, "disk.ext4")

	if renameErr := os.Rename(from, to); renameErr == nil {
		t.Skip("this host renames across these two paths, so the fallback is not taken")
	}

	require.NoError(t, moveFile(from, to))
	got, err := os.ReadFile(to)
	require.NoError(t, err)
	assert.Equal(t, "the checkpoint's disk", string(got), "the bytes are the file's, not a truncated copy")
	_, statErr := os.Stat(from)
	assert.True(t, os.IsNotExist(statErr), "a move leaves no source behind")

	info, err := os.Stat(to)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the copy a move writes has the mode a checkpoint file has")
}

// TestCopyStreamToWritesACheckpointFileAndSaysWhenItCannot pins the writer behind a
// published disk copy: the mode a checkpoint file has, and the refusal a path that cannot
// be written produces rather than a silent partial file.
func TestCopyStreamToWritesACheckpointFileAndSaysWhenItCannot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.ext4")
	require.NoError(t, copyStreamTo(path, strings.NewReader("the disk")))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "the disk", string(got))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a checkpoint file is readable by the daemon alone")

	err = copyStreamTo(filepath.Join(t.TempDir(), "no-such-directory", "disk.ext4"), strings.NewReader("the disk"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-directory")

	// A device that takes no more bytes reports the write: a checkpoint whose disk copy
	// silently stopped short is one a clone would resume from.
	if _, statErr := os.Stat("/dev/full"); statErr == nil {
		err = copyStreamTo("/dev/full", strings.NewReader(strings.Repeat("x", 4096)))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no space")
	}
}

// TestACheckpointThatCannotPublishLeavesNoImage pins the cleanup a failed publish runs:
// no record, no directory and no chunk. The memory image is stored before the record
// exists, so a publish that stopped half-way would leave 4 MiB blocks nothing names and
// no delete a caller could run to free them.
func TestACheckpointThatCannotPublishLeavesNoImage(t *testing.T) {
	cases := []struct {
		what   string
		break_ func(t *testing.T, c *checkpointFixture) func()
		says   string
	}{
		{
			"the checkpoint's own directory cannot be created",
			func(t *testing.T, c *checkpointFixture) func() {
				if os.Geteuid() == 0 {
					t.Skip("running as root: a directory that refuses writes cannot be built")
				}
				dir := c.manager.cfg.Checkpoints.Dir()
				require.NoError(t, os.Chmod(dir, 0o500))
				return func() { _ = os.Chmod(dir, 0o700) }
			},
			"create",
		},
		{
			"the state document it was handed is not there",
			func(t *testing.T, c *checkpointFixture) func() {
				c.snapshots.omitState = true
				return func() {}
			},
			"state document",
		},
		{
			"the memory image it was handed is not there",
			func(t *testing.T, c *checkpointFixture) func() {
				c.snapshots.omitMemory = true
				return func() {}
			},
			"memory image",
		},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			c := newCheckpointFixture(t)
			stubQuiesce(t, nil)
			c.readySandbox(t, "sbx-1")
			restore := tc.break_(t, c)
			defer restore()

			_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
			require.Error(t, err, tc.what)
			assert.Contains(t, err.Error(), tc.says)

			listed, _, err := c.manager.Checkpoints(sbxAnySandbox)
			require.NoError(t, err)
			assert.Empty(t, listed, "a checkpoint that failed to publish is not one a fork can find")

			ids, err := c.blobs.List(context.Background())
			require.NoError(t, err)
			assert.Empty(t, ids, "the memory image it wrote is reclaimed with the failure")

			_, statErr := os.Stat(c.manager.cfg.Checkpoints.Path("c1"))
			assert.True(t, os.IsNotExist(statErr), "its directory goes too")
		})
	}
}

// TestAPublishedCheckpointSurvivesAWorkingCopyItCannotRemove pins the one failure a
// checkpoint tolerates: the snapshot's own record is an index entry, and a checkpoint that
// is whole is not withheld because a second index would not let go of it.
func TestAPublishedCheckpointSurvivesAWorkingCopyItCannotRemove(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	c.snapshots.deleteErr = errors.New("the snapshot index is read-only")

	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err, "a working copy that will not go is a warning, not a failed checkpoint")
	assert.Equal(t, "c1", chk.Name)

	listed, _, err := c.manager.Checkpoints(sbxAnySandbox)
	require.NoError(t, err)
	assert.Len(t, listed, 1)
}

// TestAForkThatCannotBuildItsCloneFreesWhatItAllocated pins the two resources a clone
// needs before it can run: a namespace of its own, and its own copy of the checkpoint's
// disk. A clone that cannot get either is freed and reported, and the clones before it
// stand.
func TestAForkThatCannotBuildItsCloneFreesWhatItAllocated(t *testing.T) {
	t.Run("the node cannot attach a network", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		c.network.err = errors.New("no free addresses in the pool")
		forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", IDs: []string{"clone-1"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no free addresses")
		assert.Empty(t, forks)

		rec, err := c.manager.Get("clone-1")
		require.NoError(t, err)
		assert.Equal(t, StateFailed, rec.State, "the clone's record says why it is not there")
	})

	t.Run("the checkpoint's disk is not there", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		// The record names a disk the node has lost. A fork meets it at admission, which
		// reserves the disk each clone will copy: the clone is refused before its record
		// exists, so there is not even a failed record to free — the same "holds nothing"
		// rule, one step earlier.
		stray := chk
		stray.Disk = filepath.Join(filepath.Dir(chk.Disk), "absent.ext4")
		require.NoError(t, c.manager.cfg.Checkpoints.Put(stray))

		forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", IDs: []string{"clone-2"}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "absent.ext4", "the refusal names the image the node has lost")
		assert.Empty(t, forks)

		_, getErr := c.manager.Get("clone-2")
		assert.ErrorIs(t, getErr, ErrNotFound, "no clone was made, not even a failed one")
		assert.Empty(t, c.network.attached)
		assert.Empty(t, c.launcher.requests)
	})
}

// TestDeletingACheckpointReportsWhatItCouldNotFree pins the three failures a delete can
// meet: a record that will not go, a manifest that will not go, and a store that cannot
// reclaim. Each is reported rather than swallowed, because a delete that answered "ok"
// without freeing anything is the leak this path exists to prevent.
func TestDeletingACheckpointReportsWhatItCouldNotFree(t *testing.T) {
	t.Run("the record will not go", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: a directory that refuses writes cannot be built")
		}
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		dir := c.manager.cfg.Checkpoints.Dir()
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		_, err = c.manager.DeleteCheckpoint(context.Background(), "c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delete")
	})

	t.Run("the manifest will not go", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errDelete: errors.New("the manifest will not go")}
		_, err = c.manager.DeleteCheckpoint(context.Background(), "c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "forget its manifest")
	})

	t.Run("the chunks cannot be reclaimed", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		c.manager.cfg.CheckpointBlobs = &failingBlobs{Store: c.blobs, errPrune: errors.New("the store cannot prune")}
		_, err = c.manager.DeleteCheckpoint(context.Background(), "c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reclaim the chunks")
	})
}

// TestForkRefusesAnImageItCannotResumeHere pins the two refusals a fork makes against the
// node rather than against the image: a pin this node cannot read, and a staging directory
// it cannot create. Both happen before a clone exists, which is the point — the pin is
// checked before a byte is materialized.
func TestForkRefusesAnImageItCannotResumeHere(t *testing.T) {
	t.Run("this node's pin cannot be read", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		c.manager.cfg.FirecrackerBinary = filepath.Join(t.TempDir(), "absent-firecracker")
		_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pin")
		assert.Empty(t, c.network.attached, "nothing is allocated for an image the node cannot check")
	})

	t.Run("the images cannot be staged", func(t *testing.T) {
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		// The rootfs directory is a file, so the staging directory under it cannot exist.
		blocked := filepath.Join(t.TempDir(), "not-a-directory")
		require.NoError(t, os.WriteFile(blocked, []byte("in the way"), 0o600))
		c.manager.cfg.RootfsDir = blocked

		_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stage its images")
		assert.Empty(t, c.network.attached)
	})
}

// TestForkOfANegativeTtlIsNeverReaped pins the third TTL the contract allows: a caller that
// asks for clones that never go idle passes a negative TTL, and the record they get says so
// rather than carrying a value the reaper would read as a deadline.
func TestForkOfANegativeTtlIsNeverReaped(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	forks, err := c.manager.Fork(context.Background(), ForkRequest{
		Checkpoint: "c1", IDs: []string{"clone-1"}, TTL: -time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, forks, 1)
	assert.Zero(t, forks[0].IdleTTLSeconds, "a clone that never goes idle carries no TTL")
	assert.Zero(t, forks[0].TTL(), "which is how the reaper reads \"never\"")
}

// TestTheGuestConversationWithNoGuestToReach pins the production dialer: the node asks the
// guest through its own muxer, and a sandbox with no socket there is an error naming what it
// could not reach rather than a silence that reads as a refusal.
func TestTheGuestConversationWithNoGuestToReach(t *testing.T) {
	rec := testRecord("sbx-1")
	rec.VsockPath = filepath.Join(t.TempDir(), "absent.vsock")

	_, err := askGuestToQuiesce(context.Background(), rec, agent.QuiesceFreeze)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reach its guest agent")

	// The same failure on the release path is a warning: the checkpoint it follows is
	// already published, so a guest that cannot be reached must not take it with it. The
	// assertion is that the call returns rather than hanging on a writer that will never
	// be released.
	m := newCheckpointFixture(t).manager
	m.release(context.Background(), rec)
}

// TestCheckpointRefusalsThatComeFromTheStore pins the two a create meets when the node's
// own bookkeeping is the problem: a credential it cannot read, and a checkpoint directory
// it cannot write a record into. Both fail the checkpoint before an image is published.
func TestCheckpointRefusalsThatComeFromTheStore(t *testing.T) {
	t.Run("the guest's credential cannot be read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: a file that refuses reads cannot be built")
		}
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		rec := c.readySandbox(t, "sbx-1")
		require.NoError(t, os.Chmod(rec.AgentTokenFile, 0o000))

		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent token")

		listed, _, listErr := c.manager.Checkpoints(sbxAnySandbox)
		require.NoError(t, listErr)
		assert.Empty(t, listed)
	})

	t.Run("the record cannot be written", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: a directory that refuses writes cannot be built")
		}
		c := newCheckpointFixture(t)
		stubQuiesce(t, nil)
		c.readySandbox(t, "sbx-1")
		_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
		require.NoError(t, err)

		// The checkpoint's own directory exists and cannot be written in, which is the
		// state a full or read-only disk puts a record in.
		own := c.manager.cfg.Checkpoints.Path("c1")
		require.NoError(t, os.Chmod(own, 0o500))
		t.Cleanup(func() { _ = os.Chmod(own, 0o700) })

		again, err := c.manager.CheckpointByName("c1")
		require.NoError(t, err)
		err = c.manager.cfg.Checkpoints.Put(again)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "c1")
	})
}

// TestAForkWhoseGuestCannotReleaseIsStillReady pins the one release the fork tolerates: a
// guest that does not implement quiesce cannot have been frozen by it, so it is logged and
// the clone is ready — an older template must still fork.
func TestAForkWhoseGuestCannotReleaseIsStillReady(t *testing.T) {
	c := newCheckpointFixture(t)
	c.readySandbox(t, "sbx-1")

	original := askGuestToQuiesce
	askGuestToQuiesce = func(_ context.Context, _ Record, action string) (agent.QuiesceReport, error) {
		if action == agent.QuiesceThaw {
			return agent.QuiesceReport{}, &agent.Error{Code: agent.CodeUnsupportedOp, Message: "this guest has no quiesce"}
		}
		return agent.QuiesceReport{Action: action, Mode: agent.QuiesceModeFreeze, Mounts: []string{"/"}}, nil
	}
	t.Cleanup(func() { askGuestToQuiesce = original })

	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: chk.Name, IDs: []string{"clone-1"}})
	require.NoError(t, err, "an older template must still fork")
	require.Len(t, forks, 1)
	assert.Equal(t, StateReady, forks[0].State)
}

// TestAForkCannotTakeANameThatIsInUse pins the clone naming rule: a caller that supplies the
// ids gets clones under those names or an error, never a clone that overwrote a sandbox
// already on the node.
func TestAForkCannotTakeANameThatIsInUse(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: chk.Name, IDs: []string{"sbx-1"}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrExists)
	assert.Empty(t, forks)

	kept, err := c.manager.Get("sbx-1")
	require.NoError(t, err)
	assert.Equal(t, StateReady, kept.State, "the sandbox that holds the name is untouched")
}
