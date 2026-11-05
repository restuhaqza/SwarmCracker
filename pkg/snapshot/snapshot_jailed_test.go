package snapshot

// The create side of a jailed VM (SBX-022).
//
// A jail is the one place a checkpoint's paths and the host's paths are different
// names for the same file, and create is where a mistake is silent: naming a host
// path to a chrooted Firecracker fails, but copying the wrong disk succeeds and
// produces a snapshot of a disk the guest never wrote. These tests hold the create
// to both halves: the paths it sends, and the bytes it ends up owning.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotCreatePayload is one /snapshot/create body a fake Firecracker received.
type snapshotCreatePayload struct {
	SnapshotType string `json:"snapshot_type"`
	SnapshotPath string `json:"snapshot_path"`
	MemFilepath  string `json:"mem_file_path"`
}

// jailedFirecracker serves the endpoints a jailed create uses and behaves like a
// VMM inside a chroot: it resolves the paths it is given beneath chroot, so a
// create that named a host path fails here the way Firecracker would. It records
// every body so a test can assert the names the create chose.
func jailedFirecracker(t *testing.T, chroot string) (string, *[]snapshotCreatePayload) {
	t.Helper()

	creates := &[]snapshotCreatePayload{}
	socketPath := testSocketPath(t, "fc-jailed.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/vm", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/snapshot/create", func(w http.ResponseWriter, r *http.Request) {
		var payload snapshotCreatePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*creates = append(*creates, payload)
		if err := os.WriteFile(chrootPath(chroot, payload.SnapshotPath), []byte("mock state"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(chrootPath(chroot, payload.MemFilepath), []byte("mock memory"), 0o600); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		_ = listener.Close()
	})

	require.Eventually(t, func() bool {
		_, statErr := os.Stat(socketPath)
		return statErr == nil
	}, 2*time.Second, 25*time.Millisecond)

	return socketPath, creates
}

// jailedCreateFixture is the shape a jailed instance has when a create reaches it:
// the chroot with the disk the guest wrote, and the pre-boot file the launcher
// staged that disk from.
type jailedCreateFixture struct {
	jailRoot string
	chroot   string
	preBoot  string
}

func newJailedCreateFixture(t *testing.T) jailedCreateFixture {
	t.Helper()

	root := t.TempDir()
	jailRoot := filepath.Join(root, "jails", "firecracker", "sbx-task")
	chroot := filepath.Join(jailRoot, "root")
	require.NoError(t, os.MkdirAll(filepath.Join(chroot, "snapshot"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(chroot, "drives"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(chroot, "drives", "rootfs.ext4"), []byte("the disk the guest wrote"), 0o600))

	preBoot := filepath.Join(t.TempDir(), "task.ext4")
	require.NoError(t, os.WriteFile(preBoot, []byte("the disk before the guest booted"), 0o600))

	return jailedCreateFixture{jailRoot: jailRoot, chroot: chroot, preBoot: preBoot}
}

// TestCreateSnapshotInsideAJail pins the two halves of a jail-aware create: the
// paths Firecracker is given are the chroot's, so the checkpoint records names
// another jail can honour, and the disk the snapshot owns is the one the guest
// wrote inside the jail rather than the caller's pre-boot file.
func TestCreateSnapshotInsideAJail(t *testing.T) {
	fixture := newJailedCreateFixture(t)
	socketPath, creates := jailedFirecracker(t, fixture.chroot)

	mgr, err := NewManager(SnapshotConfig{SnapshotDir: filepath.Join(t.TempDir(), "snapshots")})
	require.NoError(t, err)

	info, err := mgr.CreateSnapshot(context.Background(), "sbx-task", socketPath, CreateOptions{
		ServiceID:  "web",
		JailRoot:   fixture.jailRoot,
		RootfsPath: fixture.preBoot,
	})
	require.NoError(t, err)

	require.Len(t, *creates, 1)
	assert.Equal(t, "/snapshot/vm.state", (*creates)[0].SnapshotPath,
		"a chrooted Firecracker resolves the checkpoint path inside its jail")
	assert.Equal(t, "/snapshot/vm.mem", (*creates)[0].MemFilepath)
	assert.Equal(t, "Full", (*creates)[0].SnapshotType)

	assert.True(t, info.Jailed, "the snapshot records that its paths are a jail's")
	assert.Equal(t, "/drives/rootfs.ext4", info.RootfsPath,
		"the recorded disk is the path the checkpoint resolves, not a host path from this node")

	state, err := os.ReadFile(info.StatePath)
	require.NoError(t, err)
	assert.Equal(t, "mock state", string(state), "the checkpoint is copied out of the jail")
	memory, err := os.ReadFile(info.MemoryPath)
	require.NoError(t, err)
	assert.Equal(t, "mock memory", string(memory))

	disk, err := os.ReadFile(info.DiskPath)
	require.NoError(t, err)
	assert.Equal(t, "the disk the guest wrote", string(disk),
		"a jailed snapshot must hold the live disk, not the file the launcher staged from")

	assert.Equal(t, int64(len(state)+len(memory)+len(disk)), info.SizeBytes)

	preBoot, err := os.ReadFile(fixture.preBoot)
	require.NoError(t, err)
	assert.Equal(t, "the disk before the guest booted", string(preBoot),
		"the caller's file is a source, never a target")

	assert.NoFileExists(t, filepath.Join(fixture.chroot, "snapshot", "vm.mem"),
		"the jail keeps no second memory image after the snapshot owns a copy")
	assert.NoFileExists(t, filepath.Join(fixture.chroot, "snapshot", "vm.state"))

	snapshots, err := mgr.ListSnapshots(SnapshotFilter{})
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.True(t, snapshots[0].Jailed, "the flag survives the metadata round trip")
}

// TestCreateSnapshotRefusesAnUnusableJailRoot pins that a wrong jail is refused
// before the VM is touched: no pause, no staging directory, no snapshot.
func TestCreateSnapshotRefusesAnUnusableJailRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-jail")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	cases := map[string]string{
		"relative":       "jails/sbx-task",
		"missing chroot": filepath.Join(t.TempDir(), "absent"),
		"chroot is file": file,
	}
	for name, jailRoot := range cases {
		t.Run(name, func(t *testing.T) {
			snapshotDir := filepath.Join(t.TempDir(), "snapshots")
			mgr, err := NewManager(SnapshotConfig{SnapshotDir: snapshotDir})
			require.NoError(t, err)

			_, err = mgr.CreateSnapshot(context.Background(), "sbx-task", fakeFirecracker(t), CreateOptions{
				JailRoot: jailRoot,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "jail")

			snapshots, listErr := mgr.ListSnapshots(SnapshotFilter{})
			require.NoError(t, listErr)
			assert.Empty(t, snapshots)

			entries, readErr := os.ReadDir(snapshotDir)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a refused create leaves no staging directory behind")
		})
	}
}

// TestAJailedSnapshotRestoresThroughTheJailedLauncher is the pair the refusal in
// ensureRestoredDisk protects: one checkpoint, refused by the restore that starts a
// VMM outside a chroot, served by the launcher that stages the disk at the path the
// checkpoint names. Refusing is the fail-closed half; the jailed launcher answering
// is what makes the refusal a boundary rather than a hole.
func TestAJailedSnapshotRestoresThroughTheJailedLauncher(t *testing.T) {
	info, kernel := jailedSnapshotFixture(t)

	t.Run("the direct restore refuses it", func(t *testing.T) {
		mgr, err := NewManager(SnapshotConfig{SnapshotDir: filepath.Join(t.TempDir(), "snapshots")})
		require.NoError(t, err)

		before, err := os.Stat(info.DiskPath)
		require.NoError(t, err)

		err = mgr.RestoreFromSnapshot(context.Background(), info, filepath.Join(t.TempDir(), "api.sock"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrJailedSnapshot,
			"the failure is typed so a caller can answer it without reading the text")
		assert.NotErrorIs(t, err, ErrSnapshotIncomplete,
			"the snapshot is whole; it is this restore path that cannot serve it")
		assert.Contains(t, err.Error(), "jailed launcher", "the error must name the way out")
		assert.Contains(t, err.Error(), "/drives/rootfs.ext4", "and the path it cannot honour here")

		// The refusal happens before anything is touched, so the snapshot's own disk
		// copy is neither written nor replaced.
		after, err := os.Stat(info.DiskPath)
		require.NoError(t, err)
		assert.True(t, os.SameFile(before, after))
		assert.True(t, before.ModTime().Equal(after.ModTime()))
	})

	t.Run("the jailed launcher restores it", func(t *testing.T) {
		requireJailPlatform(t)

		launcher, fake := jailedTestLauncher(t)
		fake.install(t)
		launcher.JailerPath = fake.jailerScript(t, launcher, info.TaskID)

		started, err := launcher.Start(context.Background(), LaunchRequest{
			InstanceID: info.TaskID,
			Mode:       ModeRestore,
			Kernel:     kernel,
			Rootfs:     info.DiskPath,
			State:      info.StatePath,
			Mem:        info.MemoryPath,
			VsockPath:  jailedTestVsockPath,
		})
		require.NoError(t, err, "a jail is what a jailed checkpoint needs")
		t.Cleanup(func() { _ = started.Stop(context.Background()) })

		require.Len(t, fake.loads, 1)
		assert.Equal(t, jailedStatePath, fake.loads[0].state, "the checkpoint loads at the path it recorded")
		assert.Equal(t, jailedMemPath, fake.loads[0].mem)

		staged, err := os.ReadFile(filepath.Join(launcher.JailRoot(info.TaskID), "root", "drives", "rootfs.ext4"))
		require.NoError(t, err)
		assert.Equal(t, "the disk the guest wrote", string(staged),
			"the instance runs the disk the snapshot owns, at the name the checkpoint resolves")

		owned, err := os.ReadFile(info.DiskPath)
		require.NoError(t, err)
		assert.Equal(t, "the disk the guest wrote", string(owned),
			"restoring reads the snapshot's disk: it never writes it")
	})
}

// jailedSnapshotFixture writes the files a jailed snapshot owns — the state and
// memory images, the disk copy and the kernel the instance boots with — and returns
// the record a create would have published for them: a checkpoint whose block device
// is the jail's name rather than a host path.
func jailedSnapshotFixture(t *testing.T) (*SnapshotInfo, string) {
	t.Helper()

	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	kernel := write("vmlinux", "kernel-image")
	state := write("vm.state", "mock state")
	memory := write("vm.mem", "mock memory")
	disk := write(diskFileName, "the disk the guest wrote")

	info := &SnapshotInfo{
		ID:         "snap-0123456789abcdef",
		TaskID:     "sbx-task",
		StatePath:  state,
		MemoryPath: memory,
		DiskPath:   disk,
		RootfsPath: jailedRootfsPath,
		Jailed:     true,
	}
	return info, kernel
}
