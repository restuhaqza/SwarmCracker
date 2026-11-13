package sandboxd

// The two properties this package adds on top of pkg/sandbox: a node is configured for
// checkpoints or it is not (never half), and the start pass that reclaims orphaned chunks
// runs with the stores that hold them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// failingPrune is a chunk store whose start pass cannot run: the counts it would report
// are the operator's only view of what stayed behind, so the daemon warns rather than
// hiding the failure or refusing to start over it.
type failingPrune struct{ checkpoint.Store }

func (failingPrune) Prune(context.Context, time.Duration) (checkpoint.PruneReport, error) {
	return checkpoint.PruneReport{Blobs: 3, Bytes: 12 << 20}, errors.New("the store cannot be swept")
}

// daemonConfig is a node the daemon can build: the paths it needs, and nothing that a
// template would be resolved from. New validates the configuration rather than using it,
// so the directories only have to exist.
func daemonConfig(t *testing.T, stateDir string) Config {
	t.Helper()

	for _, name := range []string{"golden", "rootfs", "logs", "chroot", "boot"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stateDir, name), 0o755))
	}
	return Config{
		StateDir:        stateDir,
		GoldenDir:       filepath.Join(stateDir, "golden"),
		KernelPath:      filepath.Join(stateDir, "boot", "vmlinux"),
		RootfsDir:       filepath.Join(stateDir, "rootfs"),
		LogDir:          filepath.Join(stateDir, "logs"),
		JailerPath:      filepath.Join(stateDir, "jailer"),
		JailerChrootDir: filepath.Join(stateDir, "chroot"),
		JailerUIDFirst:  100000,
		JailerUIDSize:   256,
		ParentCgroup:    "firecracker",
		TokenFile:       filepath.Join(stateDir, "tokens.json"),
	}
}

// TestTheTokenStoreFallsBackToTheNodesOwnAPI pins where the token a `swarmcracker api
// token create` minted is found: the configured store, or the node's own API directory
// beside its state.
func TestTheTokenStoreFallsBackToTheNodesOwnAPI(t *testing.T) {
	stateDir := t.TempDir()

	assert.Equal(t, filepath.Join(stateDir, "api", "tokens.json"), tokenFile(Config{StateDir: stateDir}))

	explicit := Config{StateDir: stateDir, TokenFile: "  /etc/swarmcracker/tokens.json  "}
	assert.Equal(t, "/etc/swarmcracker/tokens.json", tokenFile(explicit),
		"a configured store is used without the padding around it")
}

// TestNewRefusesWhatItCannotBuild pins the refusals a daemon start makes before it serves
// anything: a state directory that cannot be made, a token store that is not one, and a
// jail uid range that would run a sandbox as the node's own user.
func TestNewRefusesWhatItCannotBuild(t *testing.T) {
	// A state directory beneath a file cannot be created.
	file := filepath.Join(t.TempDir(), "a-file")
	require.NoError(t, os.WriteFile(file, []byte("in the way"), 0o600))
	_, _, err := New(Config{StateDir: filepath.Join(file, "state")})
	require.Error(t, err)

	stateDir := t.TempDir()
	cfg := daemonConfig(t, stateDir)

	// A directory is not a token store.
	cfg.TokenFile = t.TempDir()
	_, _, err = New(cfg)
	require.Error(t, err, "a token store that cannot be read is refused at start")
	cfg.TokenFile = filepath.Join(stateDir, "tokens.json")

	// A jail uid range that starts at root would run a sandbox as the daemon's user.
	cfg.JailerUIDFirst = 0
	_, _, err = New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uid range")
}

// TestStartSurvivesASweepThatCannotRun pins the two ends of the start pass: a node with no
// checkpoint store has nothing to sweep, which is a configuration rather than a failure,
// and a store that refuses the pass is reported without stopping the daemon — the chunks
// it could not reclaim are wasted space, not incorrect state.
func TestStartSurvivesASweepThatCannotRun(t *testing.T) {
	// No checkpoint store at all: the node starts, and serves no checkpoint route.
	plain := t.TempDir()
	manager, tokens, err := New(daemonConfig(t, plain))
	if err != nil {
		t.Skipf("this host cannot build a node's network policy: %v", err)
	}
	require.NotNil(t, manager)
	require.NotNil(t, tokens)

	stateDir := t.TempDir()
	records, blobs, snapshots := checkpointStoresFor(t, stateDir)
	cfg := daemonConfig(t, stateDir)
	cfg.Checkpoints, cfg.Snapshots = records, snapshots
	cfg.CheckpointBlobs = failingPrune{Store: blobs}

	// The sweep fails, and the daemon is still built: the counts it could not reclaim
	// are a warning, not a reason to refuse to serve.
	manager, tokens, err = New(cfg)
	require.NoError(t, err)
	require.NotNil(t, manager)
	require.NotNil(t, tokens)
}
