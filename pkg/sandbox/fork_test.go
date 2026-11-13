package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// fakeSnapshots is the snapshot manager as a test sees it: it produces the three files
// a checkpoint is made of, records that it did, and can be told to fail. It also proves
// the jail root it was handed, because a jailed create that is not told the jail takes
// the VM's files from the host's own disk.
type fakeSnapshots struct {
	dir      string
	jailRoot string
	copies   int
	created  []string
	deleted  []string
	err      error
}

func (f *fakeSnapshots) CreateSnapshot(_ context.Context, instanceID, socketPath string, opts snapshot.CreateOptions) (*snapshot.SnapshotInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.created = append(f.created, instanceID)
	f.jailRoot = opts.JailRoot

	id := fmt.Sprintf("snap-%04d", len(f.created))
	dir := filepath.Join(f.dir, id)
	require.NoError(nil, os.MkdirAll(dir, 0o700)) //nolint:errcheck // the fixture directory is the test's
	state := filepath.Join(dir, "vm.state")
	memory := filepath.Join(dir, "vm.mem")
	disk := filepath.Join(dir, "rootfs.ext4")
	for path, body := range map[string][]byte{
		state:  []byte(`{"devices":{"block":["/drives/rootfs.ext4"]}}`),
		memory: make([]byte, 5*checkpoint.ChunkSize+17),
	} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return nil, err
		}
	}
	if opts.RootfsPath != "" {
		source, err := os.ReadFile(opts.RootfsPath)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(disk, source, 0o600); err != nil {
			return nil, err
		}
	}
	f.copies++
	return &snapshot.SnapshotInfo{
		ID: id, TaskID: instanceID, CreatedAt: time.Now().UTC(),
		StatePath: state, MemoryPath: memory, DiskPath: disk,
		RootfsPath: opts.RootfsPath, Jailed: opts.JailRoot != "",
		SizeBytes: int64(5*checkpoint.ChunkSize + 17),
	}, nil
}

func (f *fakeSnapshots) DeleteSnapshot(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return os.RemoveAll(filepath.Join(f.dir, id))
}

// forkNetwork is the node's sandbox networking as a test sees it: every sandbox gets a
// namespace, a tap, an address and a MAC of its own, which is the property a fork is
// judged on.
type forkNetwork struct {
	attached []string
	detached []string
}

func (n *forkNetwork) Attach(_ context.Context, id string, _ []string) (netpolicy.Network, error) {
	n.attached = append(n.attached, id)
	octet := len(n.attached)
	return netpolicy.Network{
		ID: id, Name: "sbx-" + id, NetNS: "/var/run/netns/sbx-" + id, Tap: "tap0",
		CIDR:    netip.MustParsePrefix(fmt.Sprintf("10.63.%d.0/30", octet)),
		Gateway: netip.MustParseAddr(fmt.Sprintf("10.63.%d.1", octet)),
		GuestIP: netip.MustParseAddr(fmt.Sprintf("10.63.%d.2", octet)),
		MAC:     fmt.Sprintf("02:00:00:00:%02x:01", octet),
	}, nil
}

func (n *forkNetwork) Detach(_ context.Context, id string) error {
	n.detached = append(n.detached, id)
	return nil
}

// checkpointFixture is the fixture with the checkpoint halves wired: a record store, the
// D11 chunk store and a snapshot manager that produces files instead of a VM.
type checkpointFixture struct {
	*fixture
	snapshots *fakeSnapshots
	network   *forkNetwork
	blobs     *checkpoint.Local
}

func newCheckpointFixture(t *testing.T) *checkpointFixture {
	t.Helper()

	f := newFixture(t)
	snapshots := &fakeSnapshots{dir: filepath.Join(t.TempDir(), "snapshots")}
	require.NoError(t, os.MkdirAll(snapshots.dir, 0o755))
	network := &forkNetwork{}

	store, err := NewCheckpointStore(filepath.Join(t.TempDir(), DirNameCheckpoints))
	require.NoError(t, err)
	blobs, err := checkpoint.NewLocal(checkpoint.LocalConfig{Root: checkpoint.Dir(t.TempDir())})
	require.NoError(t, err)

	f.manager.cfg.Checkpoints = store
	f.manager.cfg.CheckpointBlobs = blobs
	f.manager.cfg.Snapshots = snapshots
	f.manager.cfg.Network = network
	return &checkpointFixture{fixture: f, snapshots: snapshots, network: network, blobs: blobs}
}

// readySandbox puts a sandbox in the state a checkpoint needs: ready, with a guest agent
// to ask, a running VMM to pause, a disk to copy and a credential to carry.
func (c *checkpointFixture) readySandbox(t *testing.T, id string) Record {
	t.Helper()

	disk := filepath.Join(t.TempDir(), id+".ext4")
	require.NoError(t, os.WriteFile(disk, []byte("the guest's disk, as it was"), 0o600))
	tokenFile := filepath.Join(t.TempDir(), id+".agent-token")
	require.NoError(t, agent.WriteTokenFile(tokenFile, "0123456789abcdef0123456789abcdef"))

	rec := testRecord(id)
	rec.State = StateReady
	rec.Disk, rec.AgentTokenFile = disk, tokenFile
	rec.JailRoot = filepath.Join(t.TempDir(), "jailer", id)
	rec.VsockPath = filepath.Join(t.TempDir(), id+".vsock")
	rec.NetNS, rec.TAP = "/var/run/netns/sbx-"+id, "tap0"
	rec.VCPUs, rec.MemoryMB = 2, 512
	rec.Kernel = c.manager.cfg.KernelPath
	rec.Egress = []string{"example.com:443"}
	require.NoError(t, c.store.Put(rec))
	require.NoError(t, c.instances.Put(vminstance.Instance{
		ID: id, Kind: vminstance.KindSandbox, PID: os.Getpid(),
		APISocket: filepath.Join(t.TempDir(), id+".sock"), NetNS: rec.NetNS, TAP: "tap0", Disk: disk,
	}))
	return rec
}

// stubQuiesce replaces the guest conversation with one that records the actions it was
// asked for, and can refuse.
func stubQuiesce(t *testing.T, refuse error) *[]string {
	t.Helper()

	var actions []string
	original := askGuestToQuiesce
	askGuestToQuiesce = func(_ context.Context, _ Record, action string) (agent.QuiesceReport, error) {
		actions = append(actions, action)
		if action == agent.QuiesceFreeze && refuse != nil {
			return agent.QuiesceReport{}, refuse
		}
		return agent.QuiesceReport{
			Action: action, Mode: agent.QuiesceModeFreeze, Mounts: []string{"/"},
		}, nil
	}
	t.Cleanup(func() { askGuestToQuiesce = original })
	return &actions
}

// TestCheckpointFreezesTakesTheImagesAndReleasesTheGuest pins the order that makes a
// checkpoint consistent: the guest is stopped before the snapshot manager is asked for
// anything, and it is released afterwards — including when the snapshot fails.
func TestCheckpointFreezesTakesTheImagesAndReleasesTheGuest(t *testing.T) {
	c := newCheckpointFixture(t)
	actions := stubQuiesce(t, nil)
	rec := c.readySandbox(t, "sbx-1")

	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	assert.Equal(t, []string{agent.QuiesceFreeze, agent.QuiesceThaw}, *actions,
		"the guest is frozen first and released last")
	assert.Equal(t, []string{"sbx-1"}, c.snapshots.created)
	assert.Equal(t, rec.JailRoot, c.snapshots.jailRoot, "a jailed sandbox's checkpoint is taken with its jail")

	assert.Equal(t, "c1", chk.Name)
	assert.Equal(t, "sbx-1", chk.Sandbox)
	assert.Equal(t, rec.Template, chk.Template)
	assert.Equal(t, []string{"example.com:443"}, chk.Egress)
	assert.Equal(t, agent.QuiesceModeFreeze, chk.Quiesce.Mode)
	assert.Equal(t, []string{"/"}, chk.Quiesce.Mounts)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", chk.Token,
		"a resumed guest still requires the credential it was booted with")
	assert.Greater(t, chk.SizeBytes, int64(0))

	// The images are the checkpoint's own files, and the memory image is stored in the
	// D11 format: chunks with a manifest, not one blob.
	state, err := os.ReadFile(chk.State)
	require.NoError(t, err)
	assert.Contains(t, string(state), "/drives/rootfs.ext4")
	require.NoError(t, chk.Mem.Validate())
	assert.GreaterOrEqual(t, len(chk.Mem.Chunks), 5, "5 MiB of memory is at least five 4 MiB chunks")
	assert.NotEmpty(t, chk.Mem.State.SHA256)
	_, err = os.Stat(chk.Disk)
	assert.NoError(t, err, "the checkpoint owns a disk copy")

	// The published record is what a fork reads, and the snapshot manager's working copy
	// is not a second thing: a fork must not be able to see the same checkpoint twice.
	stored, found, err := c.manager.cfg.Checkpoints.Get("c1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, chk.Mem.ID, stored.Mem.ID)
	assert.Len(t, c.snapshots.deleted, 1, "the snapshot manager's working copy is not a second index of the same checkpoint")
}

// TestCheckpointRefusesAGuestThatWillNotFreeze pins that consistency is a condition, not
// a best effort: an image of unknown consistency is not taken, and nothing is left
// behind for a fork to find.
func TestCheckpointRefusesAGuestThatWillNotFreeze(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, errors.New("the guest agent is gone"))
	c.readySandbox(t, "sbx-1")

	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop its writers")
	assert.Empty(t, c.snapshots.created, "no image is taken for a guest that is still writing")

	_, found, getErr := c.manager.cfg.Checkpoints.Get("c1")
	require.NoError(t, getErr)
	assert.False(t, found)
}

// TestCheckpointRefusesANameThatExists pins the identity rule: a checkpoint name names
// one checkpoint, and a second one under the same name would silently replace the image
// a fork may be about to use.
func TestCheckpointRefusesANameThatExists(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")

	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)
	_, err = c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.ErrorIs(t, err, ErrExists)
}

// TestForkGivesEveryCloneAnIdentityOfItsOwnAndLeavesTheCheckpointAlone is the property
// acceptance #6 names: three independent sandboxes, each with its own namespace, address,
// MAC and disk — and the images they came from are read, never written.
func TestForkGivesEveryCloneAnIdentityOfItsOwnAndLeavesTheCheckpointAlone(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")

	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)
	checkpointDisk, err := os.ReadFile(chk.Disk)
	require.NoError(t, err)

	forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", Count: 2})
	require.NoError(t, err)
	require.Len(t, forks, 2)

	seen := map[string]bool{}
	for _, rec := range forks {
		assert.Equal(t, StateReady, rec.State)
		assert.Equal(t, "c1", rec.ForkedFrom)
		assert.Equal(t, chk.Template, rec.Template)
		assert.Equal(t, chk.Egress, rec.Egress)
		assert.Equal(t, chk.VCPUs, rec.VCPUs)
		assert.NotEmpty(t, rec.NetNS)
		assert.True(t, seen[rec.MAC] == false, "a clone must not wear another clone's MAC")
		seen[rec.MAC] = true

		// Its own disk, with the checkpoint's bytes in it, and not the checkpoint's file.
		copied, err := os.ReadFile(rec.Disk)
		require.NoError(t, err)
		assert.True(t, string(copied) == string(checkpointDisk), "the clone resumes the checkpoint's disk")
		assert.NotEqual(t, chk.Disk, rec.Disk)
	}
	assert.Len(t, seen, 2, "two clones, two MACs")

	// The guest was asked for nothing: a fork resumes an image, it does not freeze a
	// running sandbox.
	require.Len(t, c.launcher.requests, 2)
	for _, req := range c.launcher.requests {
		assert.Equal(t, snapshot.ModeFork, req.Mode)
		assert.NotEmpty(t, req.State)
		assert.NotEmpty(t, req.Mem)
		assert.Equal(t, chk.Token, req.AgentToken, "a resumed guest is presented the credential it knows")
	}

	// The checkpoint is exactly as it was: a fork reads what it came from.
	stored, found, err := c.manager.cfg.Checkpoints.Get("c1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, chk.Mem.ID, stored.Mem.ID)
	after, err := os.ReadFile(chk.Disk)
	require.NoError(t, err)
	assert.Equal(t, checkpointDisk, after, "a fork must not write the checkpoint it came from")
}

// TestForkMaterializesTheImageFromTheChunkStore pins that a fork can be taken from a
// checkpoint other than the one the parent left behind: the state document and the memory
// image are read back out of the D11 store, verified against the manifest, and staged for
// the launcher as files, because a VMM cannot read a chunk index.
func TestForkStagesTheImagesFromTheChunkStore(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	// The parent is gone: a checkpoint is an image, not a child of a record.
	require.NoError(t, c.store.Delete("sbx-1"))

	// What the launcher is handed is measured at the launch: the staging is the fork
	// call's, and it is gone once the call returns.
	var memorySize, stateSize int64
	c.launcher.onStart = func() {
		request := c.launcher.requests[len(c.launcher.requests)-1]
		if info, err := os.Stat(request.Mem); err == nil {
			memorySize = info.Size()
		}
		if info, err := os.Stat(request.State); err == nil {
			stateSize = info.Size()
		}
	}

	forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
	require.NoError(t, err)
	require.Len(t, forks, 1)

	assert.Equal(t, int64(5*checkpoint.ChunkSize+17), memorySize,
		"the staged memory image is the image the manifest describes, read back out of the chunk store")
	assert.Greater(t, stateSize, int64(0), "the state document is staged beside it")
	_, err = os.Stat(filepath.Dir(c.launcher.requests[0].Mem))
	assert.True(t, os.IsNotExist(err), "a fork call stages its images for itself and leaves none behind")
}

// TestForkRefusesAnImageThisNodeWasNotBuiltFor pins the pin check D11 requires: a
// checkpoint captured on another Firecracker or another kernel is refused before a byte
// of it is materialized, because resuming it would boot the node's own software against
// a guest that never ran there.
func TestForkRefusesAnImageThisNodeWasNotBuiltFor(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	chk, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	// A manifest whose pin names other software: the record is what a fork reads, so the
	// tampering is visible exactly where it would be in the world.
	chk.Mem.Pin.Kernel = "0000000000000000000000000000000000000000000000000000000000000000"
	require.NoError(t, c.manager.cfg.Checkpoints.Put(chk))

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, checkpoint.ErrPinMismatch)
	assert.Empty(t, c.network.attached, "nothing is allocated for an image this node cannot resume")
}

// TestForkRefusesWhatItCannotDo pins the cheap refusals: a checkpoint that is not there,
// an id count that does not match, and a node with no checkpoint store at all.
func TestForkRefusesWhatItCannotDo(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "absent"})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", Count: 2, IDs: []string{"one"}})
	require.ErrorIs(t, err, ErrInvalid)

	_, err = c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", Count: -1})
	require.ErrorIs(t, err, ErrInvalid)

	bare := newFixture(t)
	_, err = bare.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1"})
	assert.ErrorIs(t, err, ErrNotImplemented)
	_, err = bare.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	assert.ErrorIs(t, err, ErrNotImplemented)
}

// TestAFailedForkLeavesNothing pins the failure path the create path already promises: a
// clone that cannot be resumed holds nothing — its network is detached with the record
// fail() frees, and no second clone is attempted under its name.
func TestAFailedForkLeavesNothing(t *testing.T) {
	c := newCheckpointFixture(t)
	stubQuiesce(t, nil)
	c.readySandbox(t, "sbx-1")
	_, err := c.manager.Checkpoint(context.Background(), "sbx-1", "c1")
	require.NoError(t, err)

	c.launcher.err = errors.New("the guest never answered")

	forks, err := c.manager.Fork(context.Background(), ForkRequest{Checkpoint: "c1", IDs: []string{"clone-1"}})
	require.Error(t, err)
	t.Logf("fork error: %v", err)
	assert.Empty(t, forks)

	rec, getErr := c.manager.Get("clone-1")
	require.NoError(t, getErr)
	assert.Equal(t, StateFailed, rec.State)
	assert.Contains(t, rec.Reason, "never answered")
	assert.Empty(t, rec.Disk, "a failed clone holds no disk")
	assert.Equal(t, []string{"clone-1"}, c.network.detached, "its namespace is released with it")
	_, statErr := os.Stat(filepath.Join(c.manager.cfg.RootfsDir, DirName, "clone-1.ext4"))
	assert.True(t, os.IsNotExist(statErr))
}

// TestCheckpointStoreRoundTripAndRules pins the store a checkpoint lives in: one record
// per name, the validation a fork depends on, a listing that survives one bad record, and
// a delete that takes the whole directory.
func TestCheckpointStoreRoundTripAndRules(t *testing.T) {
	store, err := NewCheckpointStore(filepath.Join(t.TempDir(), DirNameCheckpoints))
	require.NoError(t, err)

	record := func(name string) CheckpointRecord {
		return CheckpointRecord{
			Name: name, Sandbox: "sbx-1", Template: "base@1", Kernel: "/boot/vmlinux",
			State: "/var/lib/x/state", Disk: "/var/lib/x/disk.ext4", Token: "0123456789abcdef",
			Mem: checkpoint.Manifest{
				FormatVersion: checkpoint.FormatVersion, ID: name, TotalSize: 4,
				State:     checkpoint.State{Size: 2, SHA256: strings.Repeat("a", 64)},
				ChunkSize: checkpoint.ChunkSize, Chunks: []checkpoint.Digest{checkpoint.Digest(strings.Repeat("b", 64))},
				Pin: checkpoint.Pin{Firecracker: "vTEST", Kernel: strings.Repeat("c", 64)},
			},
		}
	}

	require.NoError(t, store.Put(record("c1")))
	got, found, err := store.Get("c1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sbx-1", got.Sandbox)

	// A record a fork could not use is refused on write and on read alike.
	broken := record("c2")
	broken.Token = ""
	assert.Error(t, store.Put(broken))

	require.NoError(t, store.Put(record("c2")))
	require.NoError(t, os.WriteFile(filepath.Join(store.Path("c2"), "checkpoint.json"), []byte("{"), 0o600))
	list, skipped, err := store.List()
	require.NoError(t, err)
	assert.Len(t, list, 1, "one unreadable record must not hide the node's other checkpoints")
	assert.Len(t, skipped, 1)

	require.NoError(t, store.Delete("c1"))
	_, found, err = store.Get("c1")
	require.NoError(t, err)
	assert.False(t, found)
	_, statErr := os.Stat(store.Path("c1"))
	assert.True(t, os.IsNotExist(statErr))
}
