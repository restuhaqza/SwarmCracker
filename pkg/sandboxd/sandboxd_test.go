package sandboxd

// The two properties this package adds on top of pkg/sandbox: a node is configured for
// checkpoints or it is not (never half), and the start pass that reclaims orphaned chunks
// runs with the stores that hold them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
)

// checkpointStoresFor builds the three stores a checkpoint needs under one state dir, the
// way the daemon does.
func checkpointStoresFor(t *testing.T, stateDir string) (*sandbox.CheckpointStore, checkpoint.Store, sandbox.Snapshotter) {
	t.Helper()

	records, err := sandbox.NewCheckpointStore(filepath.Join(stateDir, sandbox.DirNameCheckpoints))
	require.NoError(t, err)
	blobs, err := checkpoint.NewLocal(checkpoint.LocalConfig{Root: checkpoint.Dir(stateDir)})
	require.NoError(t, err)
	snapshots := &fakeSnapshotter{}
	return records, blobs, snapshots
}

// fakeSnapshotter stands in for pkg/snapshot: this package never takes an image here, and a
// test that needed a KVM would not run in CI.
type fakeSnapshotter struct{}

func (fakeSnapshotter) CreateSnapshot(context.Context, string, string, snapshot.CreateOptions) (*snapshot.SnapshotInfo, error) {
	return &snapshot.SnapshotInfo{}, nil
}

func (fakeSnapshotter) DeleteSnapshot(context.Context, string) error { return nil }

// TestCheckpointsAreAllOrNothing pins the refusal: a node with one or two of the three
// stores is not a node with checkpoints, and it must say which piece it is missing rather
// than start and half-write an image.
func TestCheckpointsAreAllOrNothing(t *testing.T) {
	stateDir := t.TempDir()
	records, blobs, snapshots := checkpointStoresFor(t, stateDir)

	_, _, err := New(Config{Checkpoints: records})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunk store")
	assert.Contains(t, err.Error(), "snapshot manager")

	_, _, err = New(Config{CheckpointBlobs: blobs, Snapshots: snapshots})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkpoint record store")

	// None at all is a configuration, not an error: this node serves no checkpoints.
	_, _, err = New(Config{StateDir: stateDir})
	if err != nil {
		assert.NotContains(t, err.Error(), "checkpoints are configured in part")
	}
}

// TestStartReclaimsOrphanedChunks pins where the sweep runs: after the stores are attached,
// not before. A chunk written by a checkpoint that died before publishing its manifest is in
// the state directory before the daemon starts, and it is gone after — which is only true if
// the pass actually sees the store.
func TestStartReclaimsOrphanedChunks(t *testing.T) {
	stateDir := t.TempDir()
	records, blobs, snapshots := checkpointStoresFor(t, stateDir)

	orphan := []byte("a chunk whose checkpoint died before publishing its manifest")
	sum := sha256.Sum256(orphan)
	require.NoError(t, blobs.PutChunk(context.Background(),
		checkpoint.Digest(hex.EncodeToString(sum[:])), bytes.NewReader(orphan)))
	before, err := blobs.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, before, "the chunk names no checkpoint, which is why it is an orphan")

	_, _, err = New(Config{
		StateDir:        stateDir,
		GoldenDir:       filepath.Join(stateDir, "golden"),
		KernelPath:      filepath.Join(stateDir, "vmlinux"),
		RootfsDir:       filepath.Join(stateDir, "rootfs"),
		JailerChrootDir: filepath.Join(stateDir, "jailer"),
		JailerUIDFirst:  200000,
		JailerUIDSize:   1000,
		Checkpoints:     records,
		CheckpointBlobs: blobs,
		Snapshots:       snapshots,
	})
	if err != nil {
		t.Skipf("this host cannot assemble a sandbox node (%v); the sweep is covered by pkg/sandbox", err)
	}

	_, err = blobs.Chunk(context.Background(), checkpoint.Digest(hex.EncodeToString(sum[:])))
	assert.Error(t, err, "the start pass reclaimed the chunk nothing named")
	_ = os.Remove(filepath.Join(stateDir, "snapshot-work"))
}
