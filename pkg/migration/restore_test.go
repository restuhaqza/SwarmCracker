package migration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restoreAt is the instant the fixture restore's clock reports. It is fixed so
// the record's CreatedAt is a value the test can name.
var restoreAt = time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)

// restoreClock is the clock a restore sees. step 0 is a fixed clock; a positive
// step advances on every read, which is how a test reaches the guest-agent
// deadline without spending it.
type restoreClock struct {
	at   time.Time
	step time.Duration
}

func (c *restoreClock) Now() time.Time {
	now := c.at
	c.at = c.at.Add(c.step)
	return now
}

// fakeRestorer stands in for pkg/snapshot's manager. It records what it was asked
// to load and implements StopRestored, which is the capability a failed restore
// uses to kill the VM it started.
type fakeRestorer struct {
	events *[]string

	calls    int
	info     *snapshot.SnapshotInfo
	socket   string
	started  bool
	stopErrs error

	stopCalls int
	stopTask  string
	err       error
}

func (f *fakeRestorer) RestoreFromSnapshot(_ context.Context, info *snapshot.SnapshotInfo, socketPath string) error {
	f.calls++
	f.info, f.socket = info, socketPath
	*f.events = append(*f.events, "restore")
	if f.err != nil {
		return f.err
	}
	f.started = true
	return nil
}

func (f *fakeRestorer) StopRestored(taskID string) error {
	f.stopCalls++
	f.stopTask = taskID
	*f.events = append(*f.events, "stop "+taskID)
	return f.stopErrs
}

// leakyRestorer is a restorer that starts the process and then reports an error:
// the shape the interface forbids it to leave behind, and the one the undo has to
// clean up without being told that anything started.
type leakyRestorer struct {
	*fakeRestorer
	startErr error
}

func (r *leakyRestorer) RestoreFromSnapshot(_ context.Context, info *snapshot.SnapshotInfo, socketPath string) error {
	r.info, r.socket = info, socketPath
	r.calls++
	r.started = true
	*r.events = append(*r.events, "restore")
	return r.startErr
}

// recordingStore is a restored-instance store whose failures are scripted and
// whose every call is written to the harness's event log. It exists because the
// undo's record delete leaves nothing on disk to observe — a failed restore never
// wrote a record — so order can only be asserted through the seam. A record that
// already exists is planted in the real registry instead, because that is the
// behaviour the refusal depends on.
type recordingStore struct {
	events    *[]string
	getErr    error
	putErr    error
	deleteErr error

	deleted []string
}

func (s *recordingStore) Get(taskID string) (scruntime.RestoredInstance, bool, error) {
	*s.events = append(*s.events, "record-get "+taskID)
	if s.getErr != nil {
		return scruntime.RestoredInstance{}, false, s.getErr
	}
	return scruntime.RestoredInstance{}, false, nil
}

func (s *recordingStore) Put(instance scruntime.RestoredInstance) error {
	*s.events = append(*s.events, "record-put "+instance.TaskID)
	return s.putErr
}

func (s *recordingStore) Delete(taskID string) error {
	s.deleted = append(s.deleted, taskID)
	*s.events = append(*s.events, "record-delete "+taskID)
	return s.deleteErr
}

// fakeDialer is the guest-agent dialler. failures is how many dials fail before
// one succeeds: a restored guest needs a moment before its agent answers.
type fakeDialer struct {
	events   *[]string
	executor Executor
	failures int

	attempts int
	vsock    string
}

func (d *fakeDialer) Dial(_ context.Context, vsockPath string) (Executor, error) {
	d.attempts++
	d.vsock = vsockPath
	*d.events = append(*d.events, "dial "+vsockPath)
	if d.attempts <= d.failures {
		return nil, errors.New("dial " + vsockPath + ": connection refused")
	}
	return d.executor, nil
}

// fakeExecutor is the guest: it records every argv and fails the command at
// failAt (1-based) with an error, or at exitAt with a status and no error.
type fakeExecutor struct {
	events   *[]string
	failAt   int
	exitAt   int
	executed [][]string
	closed   int
}

func (e *fakeExecutor) Exec(_ context.Context, argv []string) (int, error) {
	e.executed = append(e.executed, argv)
	*e.events = append(*e.events, "exec "+strings.Join(argv, " "))

	switch {
	case e.failAt != 0 && len(e.executed) == e.failAt:
		return 1, errors.New("ip: exit status 1: RTNETLINK answers: Operation not permitted")
	case e.exitAt != 0 && len(e.executed) == e.exitAt:
		return 2, nil
	default:
		return 0, nil
	}
}

func (e *fakeExecutor) Close() error {
	e.closed++
	return nil
}

// restoreHarness is one staged artifact plus the fake effects a restore drives.
type restoreHarness struct {
	t        *testing.T
	input    RestoreInput
	events   []string
	clock    *restoreClock
	restorer *fakeRestorer
	dialer   *fakeDialer
	guest    *fakeExecutor

	registry *scruntime.RestoredRegistry
	// store is what deps() wires as the registry: the node's real registry
	// unless a test substitutes one whose calls it can see.
	store RestoredInstanceStore

	tapCreated []string
	tapUps     []string
	tapDeleted []string

	tapCreateErr error
	tapUpErr     error
}

func newRestoreHarness(t *testing.T) *restoreHarness {
	t.Helper()

	root := t.TempDir()
	artifact := validArtifact()
	// The host-side names are derived from the source task by the one rule in
	// pkg/runtime, exactly as the exporting node derived them.
	artifact.TapName = scruntime.TapName(artifact.SourceTaskID, 0)
	artifact.VsockPath = scruntime.VsockPath("/var/run/firecracker", artifact.SourceTaskID)

	dir := filepath.Join(root, artifact.ID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	artifact.StateFile = stagedRestoreFile(t, dir, "vm.state", "state-of-the-guest")
	artifact.MemoryFile = stagedRestoreFile(t, dir, "vm.mem", "memory-of-the-guest")
	artifact.DiskFile = stagedRestoreFile(t, dir, "rootfs.ext4", "disk-of-the-guest")
	require.NoError(t, artifact.Validate())

	registryDir := filepath.Join(root, "restored-instances")
	registry, err := scruntime.NewRestoredRegistry(registryDir)
	require.NoError(t, err)

	h := &restoreHarness{
		t:        t,
		clock:    &restoreClock{at: restoreAt},
		registry: registry,
		store:    registry,
	}
	h.restorer = &fakeRestorer{events: &h.events}
	h.guest = &fakeExecutor{events: &h.events}
	h.dialer = &fakeDialer{events: &h.events, executor: h.guest}
	h.input = RestoreInput{
		Artifact:  artifact,
		Dir:       dir,
		TaskID:    "targettask1",
		GuestCIDR: "192.168.127.37/24",
		Gateway:   "192.168.127.2",
		Bridge:    "swarm-br0",
		SocketDir: filepath.Join(root, "run"),
	}
	return h
}

// stagedRestoreFile writes one staged file and returns the description the
// manifest would carry for it.
func stagedRestoreFile(t *testing.T, dir, name, body string) File {
	t.Helper()

	writeTempFile(t, dir, name, body)
	return File{Name: name, Size: int64(len(body)), SHA256: sha256Hex(body)}
}

func (h *restoreHarness) deps() RestoreDeps {
	return RestoreDeps{
		Snapshot: h.restorer,
		TapCreate: func(name, bridge string) error {
			h.events = append(h.events, "tap-create "+name+" "+bridge)
			h.tapCreated = append(h.tapCreated, name)
			return h.tapCreateErr
		},
		TapUp: func(name string) error {
			h.events = append(h.events, "tap-up "+name)
			h.tapUps = append(h.tapUps, name)
			return h.tapUpErr
		},
		TapDelete: func(name string) error {
			h.events = append(h.events, "tap-delete "+name)
			h.tapDeleted = append(h.tapDeleted, name)
			return nil
		},
		Agent:    h.dialer,
		Registry: h.store,
		Now:      h.clock.Now,
	}
}

func (h *restoreHarness) restore(ctx context.Context) (scruntime.RestoredInstance, error) {
	return Restore(ctx, h.input, h.deps())
}

// record returns the registry's record for the new task.
func (h *restoreHarness) record() (scruntime.RestoredInstance, bool) {
	h.t.Helper()

	instance, found, err := h.registry.Get(h.input.TaskID)
	require.NoError(h.t, err)
	return instance, found
}

// records returns every usable record, which is how a test checks that a refused
// restore wrote none even when the task ID itself was refused.
func (h *restoreHarness) records() []scruntime.RestoredInstance {
	h.t.Helper()

	records, _, err := h.registry.List()
	require.NoError(h.t, err)
	return records
}

// assertArtifactIntact checks the staged artifact is still on disk: a restore never
// discards the bytes it was given, so a retry can use them again.
func (h *restoreHarness) assertArtifactIntact() {
	h.t.Helper()

	for _, entry := range stagedFiles(h.input.Artifact) {
		assert.FileExists(h.t, filepath.Join(strings.TrimSpace(h.input.Dir), entry.file.Name))
	}
}

// stubAgentRetryWait removes the real pause between guest-agent dials, so a test
// that reaches the deadline does not spend it. A cancelled context still reports
// cancellation; a live one reports no error, which is what "the pause elapsed"
// means to the caller.
func stubAgentRetryWait(t *testing.T) {
	t.Helper()

	original := agentRetryWait
	agentRetryWait = func(ctx context.Context, _ time.Duration) error {
		return ctx.Err()
	}
	t.Cleanup(func() { agentRetryWait = original })
}

// TestRestoreTrimsTheAddressesItGivesTheGuest pins that the argv the guest sees is
// exactly what was validated: a request with surrounding whitespace parses, so
// the raw value must not be what reaches ip(8).
func TestRestoreTrimsTheAddressesItGivesTheGuest(t *testing.T) {
	h := newRestoreHarness(t)
	h.input.GuestCIDR = "  192.168.127.37/24\n"
	h.input.Gateway = "\t192.168.127.2 "

	_, err := h.restore(context.Background())
	require.NoError(t, err)

	assert.Equal(t, [][]string{
		{"ip", "addr", "flush", "dev", "eth0"},
		{"ip", "addr", "add", "192.168.127.37/24", "dev", "eth0"},
		{"ip", "route", "replace", "default", "via", "192.168.127.2"},
	}, h.guest.executed)
}

// TestRestoreTrimsThePathsItUses pins that the directories the restore hands to
// the filesystem are the values it validated: a trailing space passes IsAbs (the
// leading byte is still "/") and then addresses a directory nobody created, so the
// failure would land inside the staged-file check or inside Firecracker.
func TestRestoreTrimsThePathsItUses(t *testing.T) {
	h := newRestoreHarness(t)
	h.input.Dir += "  "
	h.input.SocketDir = "\t" + h.input.SocketDir + "\n"
	artifact := h.input.Artifact
	wantDir := strings.TrimSpace(h.input.Dir)
	wantSocketDir := strings.TrimSpace(h.input.SocketDir)

	instance, err := h.restore(context.Background())
	require.NoError(t, err)

	require.NotNil(t, h.restorer.info)
	assert.Equal(t, filepath.Join(wantDir, artifact.StateFile.Name), h.restorer.info.StatePath, "the staged path is built from the trimmed dir")
	assert.Equal(t, filepath.Join(wantDir, artifact.MemoryFile.Name), h.restorer.info.MemoryPath)
	assert.Equal(t, filepath.Join(wantDir, artifact.DiskFile.Name), h.restorer.info.DiskPath)
	assert.Equal(t, filepath.Join(wantSocketDir, h.input.TaskID+scruntime.VMSocketSuffix), instance.SocketPath,
		"the API socket goes in the trimmed socket dir")

	record, found := h.record()
	require.True(t, found)
	assert.Equal(t, instance, record)
	h.assertArtifactIntact()
}

// TestRestoreStartsTheArtifactAsTheNewTask is the happy path: the effects happen
// in one order, the snapshot is asked for the artifact's bytes under the recorded
// disk path, the guest is re-addressed with argv, and the record names the source
// task as the VM behind the new one.
func TestRestoreStartsTheArtifactAsTheNewTask(t *testing.T) {
	h := newRestoreHarness(t)
	// The guest agent needs two attempts before it answers, which a restored
	// guest does while its agent starts accepting connections.
	h.dialer.failures = 2
	artifact := h.input.Artifact
	socketPath := filepath.Join(h.input.SocketDir, h.input.TaskID+scruntime.VMSocketSuffix)

	instance, err := h.restore(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{
		"tap-create " + artifact.TapName + " " + h.input.Bridge,
		"restore",
		"dial " + artifact.VsockPath,
		"dial " + artifact.VsockPath,
		"dial " + artifact.VsockPath,
		"exec ip addr flush dev eth0",
		"exec ip addr add 192.168.127.37/24 dev eth0",
		"exec ip route replace default via 192.168.127.2",
		"tap-up " + artifact.TapName,
	}, h.events, "the TAP is created before the load and comes up only after the guest is re-addressed")

	assert.Equal(t, [][]string{
		{"ip", "addr", "flush", "dev", "eth0"},
		{"ip", "addr", "add", "192.168.127.37/24", "dev", "eth0"},
		{"ip", "route", "replace", "default", "via", "192.168.127.2"},
	}, h.guest.executed, "the guest is driven with argv, never a shell string")

	assert.Equal(t, artifact.VsockPath, h.dialer.vsock, "the muxer path is the one the snapshot recorded")

	require.NotNil(t, h.restorer.info)
	assert.Equal(t, artifact.SnapshotID, h.restorer.info.ID)
	assert.Equal(t, h.input.TaskID, h.restorer.info.TaskID, "the process is tracked as the new task")
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.StateFile.Name), h.restorer.info.StatePath)
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.MemoryFile.Name), h.restorer.info.MemoryPath)
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.DiskFile.Name), h.restorer.info.DiskPath)
	assert.Equal(t, artifact.DiskPath, h.restorer.info.RootfsPath, "the disk is materialised at the recorded path")
	assert.Equal(t, artifact.StateFile.SHA256, h.restorer.info.Checksum)
	assert.Equal(t, socketPath, h.restorer.socket)

	want := scruntime.RestoredInstance{
		TaskID:     h.input.TaskID,
		VMID:       artifact.SourceTaskID,
		ArtifactID: artifact.ID,
		SocketPath: socketPath,
		TapName:    artifact.TapName,
		VsockPath:  artifact.VsockPath,
		DiskPath:   artifact.DiskPath,
		CreatedAt:  restoreAt,
	}
	assert.Equal(t, want, instance)

	record, found := h.record()
	require.True(t, found)
	assert.Equal(t, want, record)

	assert.Zero(t, h.restorer.stopCalls, "a successful restore stops nothing")
	assert.Equal(t, []string{artifact.TapName}, h.tapUps)
	assert.Empty(t, h.tapDeleted)
	assert.Equal(t, 1, h.guest.closed, "the restore closes its guest connection")
	h.assertArtifactIntact()
}

// TestRestoreRefusesATaskThatAlreadyRunsAVM pins I1: a task that already has a
// restored record must be refused before any effect. Continuing would materialise
// the artifact's disk over the file the live VM is running from, and the failed
// attempt's undo would delete the record of a VM that kept running.
func TestRestoreRefusesATaskThatAlreadyRunsAVM(t *testing.T) {
	h := newRestoreHarness(t)
	planted := scruntime.RestoredInstance{
		TaskID:     h.input.TaskID,
		VMID:       h.input.Artifact.SourceTaskID,
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(h.input.SocketDir, h.input.TaskID+scruntime.VMSocketSuffix),
		TapName:    h.input.Artifact.TapName,
		VsockPath:  h.input.Artifact.VsockPath,
		DiskPath:   h.input.Artifact.DiskPath,
		CreatedAt:  restoreAt,
	}
	require.NoError(t, h.registry.Put(planted))

	_, err := h.restore(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already runs a restored VM")
	assert.Contains(t, err.Error(), "discarded first")

	assert.Empty(t, h.tapCreated, "no TAP device is created")
	assert.Zero(t, h.restorer.calls, "the restorer is never called")
	assert.Zero(t, h.dialer.attempts, "the guest is never dialled")

	record, found := h.record()
	require.True(t, found, "the refusal does not delete the live VM's record")
	assert.Equal(t, planted, record)
	h.assertArtifactIntact()
}

// TestRestoreRefusesWhenTheExistingRecordCannotBeRead pins that the check fails
// closed: a record that cannot be read is not evidence that the task is free, and
// guessing wrong destroys a running VM.
func TestRestoreRefusesWhenTheExistingRecordCannotBeRead(t *testing.T) {
	h := newRestoreHarness(t)
	h.store = &recordingStore{events: &h.events, getErr: errors.New("permission denied")}

	_, err := h.restore(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot tell whether the task already runs one")
	assert.Contains(t, err.Error(), "permission denied")

	assert.Empty(t, h.tapCreated)
	assert.Zero(t, h.restorer.calls)
}

// TestRestoreRefusesBeforeAnyEffect pins the checks that must run before the first
// side effect: a request that cannot work must not create a device, start a VM, or
// record anything. The checksum case is the important one: the manifest is intact,
// only the bytes are not.
func TestRestoreRefusesBeforeAnyEffect(t *testing.T) {
	overwrite := func(in *RestoreInput, file File, body []byte) {
		path := filepath.Join(in.Dir, file.Name)
		require.NoError(t, os.WriteFile(path, body, 0o600))
	}

	cases := []struct {
		name       string
		mutate     func(t *testing.T, in *RestoreInput)
		wantPhrase string
	}{
		{
			name: "checksum mismatch",
			mutate: func(t *testing.T, in *RestoreInput) {
				t.Helper()
				// Same length, different bytes: only the digest can catch it.
				overwrite(in, in.Artifact.MemoryFile, bytes.Repeat([]byte("x"), int(in.Artifact.MemoryFile.Size)))
			},
			wantPhrase: "SHA-256",
		},
		{
			name: "size mismatch",
			mutate: func(t *testing.T, in *RestoreInput) {
				t.Helper()
				overwrite(in, in.Artifact.DiskFile, bytes.Repeat([]byte("x"), int(in.Artifact.DiskFile.Size)+1))
			},
			wantPhrase: "the manifest describes",
		},
		{
			name: "staged file missing",
			mutate: func(t *testing.T, in *RestoreInput) {
				t.Helper()
				require.NoError(t, os.Remove(filepath.Join(in.Dir, in.Artifact.StateFile.Name)))
			},
			wantPhrase: "stateFile",
		},
		{
			name: "staged file is not a regular file",
			mutate: func(t *testing.T, in *RestoreInput) {
				t.Helper()
				path := filepath.Join(in.Dir, in.Artifact.MemoryFile.Name)
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			},
			wantPhrase: "not a regular file",
		},
		{
			name:       "dir is empty",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Dir = " " },
			wantPhrase: "dir is empty",
		},
		{
			name:       "dir is relative",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Dir = "staged/artifact" },
			wantPhrase: "dir",
		},
		{
			name:       "task id is empty",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.TaskID = "" },
			wantPhrase: "taskId",
		},
		{
			name:       "task id has a separator",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.TaskID = "target/task" },
			wantPhrase: "taskId",
		},
		{
			name:       "task id has a dot",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.TaskID = "target.task" },
			wantPhrase: "taskId",
		},
		{
			// The artifact is internally consistent — the TAP name and the vsock
			// path are the ones this source task ID derives — so only the source
			// task ID rule can refuse it. Without that rule the restore would run
			// to the last step and fail inside the registry with the VM already up.
			name: "source task id has a dot",
			mutate: func(_ *testing.T, in *RestoreInput) {
				in.Artifact.SourceTaskID = "source.task"
				in.Artifact.TapName = scruntime.TapName(in.Artifact.SourceTaskID, 0)
				in.Artifact.VsockPath = scruntime.VsockPath("/var/run/firecracker", in.Artifact.SourceTaskID)
			},
			wantPhrase: "sourceTaskId",
		},
		{
			name: "source task id has a separator",
			mutate: func(_ *testing.T, in *RestoreInput) {
				in.Artifact.SourceTaskID = "source/task"
				in.Artifact.TapName = scruntime.TapName(in.Artifact.SourceTaskID, 0)
				in.Artifact.VsockPath = scruntime.VsockPath("/var/run/firecracker", in.Artifact.SourceTaskID)
			},
			wantPhrase: "sourceTaskId",
		},
		{
			name:       "guest cidr has no prefix length",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.GuestCIDR = "192.168.127.37" },
			wantPhrase: "guestCidr",
		},
		{
			name:       "guest cidr is not an address",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.GuestCIDR = "not-an-address/24" },
			wantPhrase: "guestCidr",
		},
		{
			name:       "guest cidr is IPv6",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.GuestCIDR = "fd00::37/64" },
			wantPhrase: "guestCidr",
		},
		{
			name:       "gateway is not an address",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Gateway = "192.168.127.999" },
			wantPhrase: "gateway",
		},
		{
			name:       "gateway is IPv6",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Gateway = "fd00::2" },
			wantPhrase: "gateway",
		},
		{
			name:       "bridge is empty",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Bridge = "" },
			wantPhrase: "bridge",
		},
		{
			name:       "bridge is not an interface name",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Bridge = "swarm/br0" },
			wantPhrase: "bridge",
		},
		{
			name:       "socket dir is empty",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.SocketDir = "" },
			wantPhrase: "socketDir is empty",
		},
		{
			name:       "socket dir is relative",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.SocketDir = "run" },
			wantPhrase: "socketDir",
		},
		{
			name: "artifact has no disk copy",
			mutate: func(_ *testing.T, in *RestoreInput) {
				in.Artifact.DiskFile = File{}
			},
			wantPhrase: "diskFile",
		},
		{
			name:       "artifact is another document",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Artifact.Kind = "Snapshot" },
			wantPhrase: "kind",
		},
		{
			name: "vsock path belongs to another task",
			mutate: func(_ *testing.T, in *RestoreInput) {
				in.Artifact.VsockPath = scruntime.VsockPath("/var/run/firecracker", "anothertask")
			},
			wantPhrase: "vsockPath",
		},
		{
			name:       "tap name belongs to another task",
			mutate:     func(_ *testing.T, in *RestoreInput) { in.Artifact.TapName = scruntime.TapName("anothertask", 0) },
			wantPhrase: "tapName",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			tc.mutate(t, &h.input)

			_, err := h.restore(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantPhrase)

			assert.Empty(t, h.tapCreated, "no TAP device is created")
			assert.Zero(t, h.restorer.calls, "the restorer is never called")
			assert.Zero(t, h.dialer.attempts, "the guest is never dialled")
			assert.Empty(t, h.records(), "no record is written")
		})
	}
}

// TestRestoreRefusesUnwiredDeps pins that a missing effect is a request error
// before any effect runs, not a panic halfway through a restore.
func TestRestoreRefusesUnwiredDeps(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*RestoreDeps)
		wantPhrase string
	}{
		{"no snapshot restorer", func(d *RestoreDeps) { d.Snapshot = nil }, "Snapshot"},
		{"no tap creator", func(d *RestoreDeps) { d.TapCreate = nil }, "TapCreate"},
		{"no tap bring-up", func(d *RestoreDeps) { d.TapUp = nil }, "TapUp"},
		{"no tap remover", func(d *RestoreDeps) { d.TapDelete = nil }, "TapDelete"},
		{"no agent dialler", func(d *RestoreDeps) { d.Agent = nil }, "Agent"},
		{"no registry", func(d *RestoreDeps) { d.Registry = nil }, "Registry"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			deps := h.deps()
			tc.mutate(&deps)

			_, err := Restore(context.Background(), h.input, deps)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantPhrase)

			assert.Empty(t, h.tapCreated)
			assert.Zero(t, h.restorer.calls)
		})
	}
}

// TestRestoreUndoesAFailedRestore pins the rollback: whatever the failure, the
// device is removed, the VM the load may have started is stopped, no record
// survives, and the artifact stays where it is so the move can be retried.
func TestRestoreUndoesAFailedRestore(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, h *restoreHarness, deps *RestoreDeps)
		wantStep string
		// wantTapUp is true for a failure at or after the bring-up: the device
		// came up and is then removed, which is still the correct rollback.
		wantTapUp bool
	}{
		{
			name: "the snapshot cannot be loaded",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.restorer.err = errors.New("firecracker: load failed")
			},
			wantStep: stepRestoreSnapshot,
		},
		{
			name: "the TAP cannot be created",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.tapCreateErr = errors.New("operation not permitted")
			},
			wantStep: stepCreateTap,
		},
		{
			name: "the guest cannot be re-addressed",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.guest.failAt = 2
			},
			wantStep: stepReaddressGuest,
		},
		{
			name: "the guest reports a status and no error",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.guest.exitAt = 3
			},
			wantStep: stepReaddressGuest,
		},
		{
			name: "the TAP cannot be brought up",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.tapUpErr = errors.New("operation not permitted")
			},
			wantStep:  stepTapUp,
			wantTapUp: true,
		},
		{
			// The store refuses the write, which a real registry can only be made
			// to do with a device failure — and a directory planted at the record
			// path is no longer usable here: step 1 now reads that path, so it
			// would refuse the restore before any effect instead.
			name: "the record cannot be written",
			setup: func(_ *testing.T, h *restoreHarness, deps *RestoreDeps) {
				deps.Registry = &recordingStore{events: &h.events, putErr: errors.New("no space left on device")}
			},
			wantStep:  stepRecordInstance,
			wantTapUp: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			deps := h.deps()
			tc.setup(t, h, &deps)

			_, err := Restore(context.Background(), h.input, deps)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantStep)

			// The stop is attempted on every failure, not only when the load
			// returned nil: a restorer that started a process and then errored
			// leaves one behind, and only it can take that process down.
			assert.Equal(t, 1, h.restorer.stopCalls, "the stop is unconditional; a missing process is not a failure")
			assert.Equal(t, h.input.TaskID, h.restorer.stopTask, "the VM is tracked by the task it runs as")

			if tc.wantStep == stepCreateTap {
				assert.Empty(t, h.tapDeleted, "a TAP that was never created is not removed")
			} else {
				assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapDeleted, "the TAP device is removed")
			}
			if tc.wantTapUp {
				assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapUps)
			} else {
				assert.Empty(t, h.tapUps, "a failure before the bring-up leaves the TAP down")
			}
			assert.Empty(t, h.records(), "a failed restore leaves no record")
			h.assertArtifactIntact()
		})
	}
}

// TestRestoreStopsTheVMARestorerLeakedOnError pins I2: the undo must not infer
// from the load's return value that no process exists. A restorer that starts
// Firecracker and then errors owes a process, and the stop is the only thing that
// can take it down — otherwise it keeps running with its TAP deleted underneath.
func TestRestoreStopsTheVMARestorerLeakedOnError(t *testing.T) {
	h := newRestoreHarness(t)
	leaky := &leakyRestorer{
		fakeRestorer: h.restorer,
		startErr:     errors.New("firecracker: cannot bind the vsock muxer"),
	}
	deps := h.deps()
	deps.Snapshot = leaky

	_, err := Restore(context.Background(), h.input, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), stepRestoreSnapshot)

	assert.Equal(t, 1, leaky.stopCalls, "the stop is attempted even though the load reported an error")
	assert.Equal(t, h.input.TaskID, leaky.stopTask)
	assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapDeleted)
	assert.NotContains(t, err.Error(), stepUndo, "a stop that succeeded is not reported as a cleanup failure")
}

// TestRestoreUndoOrder pins the order of the rollback: the VM goes first (so
// nothing keeps writing to the disk the record names), then the device it used,
// then the record that describes it. The record delete leaves nothing on disk to
// observe, so the store is substituted and every call is traced.
func TestRestoreUndoOrder(t *testing.T) {
	h := newRestoreHarness(t)
	store := &recordingStore{events: &h.events}
	h.store = store
	h.guest.failAt = 1

	_, err := h.restore(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), stepReaddressGuest)

	assert.Equal(t, []string{
		"record-get " + h.input.TaskID,
		"tap-create " + h.input.Artifact.TapName + " " + h.input.Bridge,
		"restore",
		"dial " + h.input.Artifact.VsockPath,
		"exec ip addr flush dev eth0",
		"stop " + h.input.TaskID,
		"tap-delete " + h.input.Artifact.TapName,
		"record-delete " + h.input.TaskID,
	}, h.events, "the undo stops the VM, then removes the device, then the record")
	assert.Equal(t, []string{h.input.TaskID}, store.deleted)
}

// TestRestoreFailsWithinTheGuestAgentDeadline pins the bounded wait: an agent that
// never answers fails the restore inside the deadline, and the half-started VM is
// undone. The clock advances on every read, so the deadline is reached without the
// test waiting for it.
func TestRestoreFailsWithinTheGuestAgentDeadline(t *testing.T) {
	h := newRestoreHarness(t)
	h.clock.step = time.Second
	stubAgentRetryWait(t)
	h.dialer.failures = maxGuestDialAttempts * 2

	start := time.Now()
	_, err := h.restore(context.Background())
	require.Error(t, err)

	assert.Contains(t, err.Error(), stepGuestAgent)
	assert.Less(t, time.Since(start), 2*time.Second, "the wait is bounded, not the test's patience")
	assert.GreaterOrEqual(t, h.dialer.attempts, 2, "the restore retries before it gives up")

	assert.Equal(t, 1, h.restorer.stopCalls, "the restored VM is stopped")
	assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapDeleted)
	assert.Empty(t, h.tapUps)
	assert.Empty(t, h.records())
	h.assertArtifactIntact()
}

// TestRestoreGuestAgentWaitIsBoundedWithoutAClock pins the attempt cap: even a
// clock that never advances cannot make the wait endless.
func TestRestoreGuestAgentWaitIsBoundedWithoutAClock(t *testing.T) {
	h := newRestoreHarness(t)
	stubAgentRetryWait(t)
	h.dialer.failures = maxGuestDialAttempts * 2

	_, err := h.restore(context.Background())
	require.Error(t, err)

	assert.Contains(t, err.Error(), stepGuestAgent)
	assert.Equal(t, maxGuestDialAttempts, h.dialer.attempts)
}

// TestRestoreStopsWhenTheContextEnds pins that cancellation during the wait is
// reported as cancellation rather than retried until the deadline.
func TestRestoreStopsWhenTheContextEnds(t *testing.T) {
	h := newRestoreHarness(t)
	h.clock.step = time.Second
	h.dialer.failures = maxGuestDialAttempts * 2

	ctx, cancel := context.WithCancel(context.Background())
	// The stub pauses cancel the context on their first call, which is what a
	// caller giving up looks like to the wait.
	original := agentRetryWait
	agentRetryWait = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	t.Cleanup(func() { agentRetryWait = original })

	_, err := h.restore(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, h.dialer.attempts, "the wait ends on the first cancelled pause")
	assert.Equal(t, 1, h.restorer.stopCalls)
}

// TestRestoreAgentConnectionIsClosed pins the connection's lifetime: the restore
// closes its guest connection on success and on failure, and the VM outlives it.
func TestRestoreAgentConnectionIsClosed(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *restoreHarness)
	}{
		{name: "success", setup: func(*restoreHarness) {}},
		{name: "failed re-address", setup: func(h *restoreHarness) { h.guest.failAt = 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			tc.setup(h)

			_, _ = h.restore(context.Background())
			assert.Equal(t, 1, h.guest.closed)
		})
	}
}

// TestRestoreUndoReportsAnUnstoppableRestorer pins that a restorer which cannot
// stop the VM it started does not silently leak it: the failure says so.
func TestRestoreUndoReportsAnUnstoppableRestorer(t *testing.T) {
	h := newRestoreHarness(t)
	h.guest.failAt = 1

	deps := h.deps()
	deps.Snapshot = restorerWithoutStop{fake: h.restorer}

	_, err := Restore(context.Background(), h.input, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), stepReaddressGuest)
	assert.Contains(t, err.Error(), "cannot stop the VM it started")
	assert.Equal(t, []string{h.input.Artifact.TapName}, h.tapDeleted, "the rest of the undo still runs")
}

// restorerWithoutStop is a SnapshotRestorer with no StopRestored method.
type restorerWithoutStop struct {
	fake *fakeRestorer
}

func (r restorerWithoutStop) RestoreFromSnapshot(ctx context.Context, info *snapshot.SnapshotInfo, socketPath string) error {
	return r.fake.RestoreFromSnapshot(ctx, info, socketPath)
}

// TestRestoreUndoReportsFailures pins that cleanup failures are reported next to
// the failure that caused them: a device left behind is something an operator has
// to know, and a cleanup failure must not be allowed to mask the step that failed.
// A stop that finds no restored VM, or a record that is already gone, is not a
// failure: the undo wanted exactly that.
func TestRestoreUndoReportsFailures(t *testing.T) {
	errRecordDelete := errors.New("the record directory is locked")

	cases := []struct {
		name       string
		setup      func(t *testing.T, h *restoreHarness, deps *RestoreDeps)
		wantPhrase string
		// wantAlso is the step that failed, which the joined cleanup error must
		// not hide.
		wantAlso string
		wantUndo bool
	}{
		{
			name: "the TAP device cannot be removed",
			setup: func(_ *testing.T, _ *restoreHarness, deps *RestoreDeps) {
				deps.TapDelete = func(string) error { return errors.New("device is busy") }
			},
			wantPhrase: "delete the TAP device",
			wantAlso:   stepReaddressGuest,
			wantUndo:   true,
		},
		{
			name: "the VM cannot be stopped",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.restorer.stopErrs = errors.New("process is gone and its state is unknown")
			},
			wantPhrase: "stop the restored VM",
			wantAlso:   stepReaddressGuest,
			wantUndo:   true,
		},
		{
			name: "the record cannot be deleted",
			setup: func(_ *testing.T, h *restoreHarness, deps *RestoreDeps) {
				deps.Registry = &recordingStore{events: &h.events, deleteErr: errRecordDelete}
			},
			wantPhrase: "delete the restored-instance record",
			wantAlso:   stepReaddressGuest,
			wantUndo:   true,
		},
		{
			name: "the stop finds no restored VM",
			setup: func(_ *testing.T, h *restoreHarness, _ *RestoreDeps) {
				h.restorer.stopErrs = fmt.Errorf("%w: task is already gone", snapshot.ErrNoRestoredVM)
			},
			wantPhrase: stepReaddressGuest,
			wantUndo:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRestoreHarness(t)
			h.guest.failAt = 1

			deps := h.deps()
			tc.setup(t, h, &deps)

			_, err := Restore(context.Background(), h.input, deps)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantPhrase)
			if tc.wantAlso != "" {
				assert.Contains(t, err.Error(), tc.wantAlso, "the step that failed is still named")
			}
			if tc.wantUndo {
				assert.Contains(t, err.Error(), stepUndo)
			} else {
				assert.NotContains(t, err.Error(), stepUndo, "a VM that is already gone is not a cleanup failure")
			}
		})
	}
}

// TestRestoreUndoJoinsEveryFailure pins that the undo reports all three cleanup
// failures at once, in the order it attempted them: an operator debugging a
// half-torn-down restore needs the list, not the first item of it.
func TestRestoreUndoJoinsEveryFailure(t *testing.T) {
	h := newRestoreHarness(t)
	h.guest.failAt = 1
	h.restorer.stopErrs = errors.New("process is gone and its state is unknown")

	deps := h.deps()
	deps.TapDelete = func(string) error { return errors.New("device is busy") }
	deps.Registry = &recordingStore{events: &h.events, deleteErr: errors.New("the record directory is locked")}

	_, err := Restore(context.Background(), h.input, deps)
	require.Error(t, err)

	message := err.Error()
	stop := strings.Index(message, "stop the restored VM")
	tap := strings.Index(message, "delete the TAP device")
	record := strings.Index(message, "delete the restored-instance record")
	require.GreaterOrEqual(t, stop, 0, "the failed stop is reported: %s", message)
	require.GreaterOrEqual(t, tap, 0, "the failed delete is reported: %s", message)
	require.GreaterOrEqual(t, record, 0, "the failed record delete is reported: %s", message)
	assert.Less(t, stop, tap, "the VM is stopped before its device is removed")
	assert.Less(t, tap, record, "the record goes last")
	assert.Contains(t, message, stepReaddressGuest, "the step that failed is not masked")
}

// TestDialAgentRefusesACancelledContext pins the adapter's error path without a
// guest: a dial that cannot start is reported, never a half-connected executor.
func TestDialAgentRefusesACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executor, err := DialAgent(ctx, filepath.Join(t.TempDir(), "task-1.vsock"))
	require.Error(t, err)
	assert.Nil(t, executor)
}

// TestReaddressCommands pins the argv sequence itself: an order change or a shell
// string is a behaviour change a test should stop.
func TestReaddressCommands(t *testing.T) {
	assert.Equal(t, [][]string{
		{"ip", "addr", "flush", "dev", "eth0"},
		{"ip", "addr", "add", "192.168.127.37/24", "dev", "eth0"},
		{"ip", "route", "replace", "default", "via", "192.168.127.2"},
	}, readdressCommands("192.168.127.37/24", "192.168.127.2"))
}

// TestRestorationInfo pins the mapping from artifact to pkg/snapshot: the staged
// paths, the recorded disk path, and the state checksum.
func TestRestorationInfo(t *testing.T) {
	h := newRestoreHarness(t)
	artifact := h.input.Artifact

	info := restorationInfo(h.input)
	assert.Equal(t, artifact.SnapshotID, info.ID)
	assert.Equal(t, h.input.TaskID, info.TaskID)
	assert.Equal(t, artifact.SourceNodeID, info.NodeID)
	assert.Equal(t, artifact.ServiceID, info.ServiceID)
	assert.Equal(t, artifact.SnapshotAt, info.CreatedAt)
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.StateFile.Name), info.StatePath)
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.MemoryFile.Name), info.MemoryPath)
	assert.Equal(t, filepath.Join(h.input.Dir, artifact.DiskFile.Name), info.DiskPath)
	assert.Equal(t, artifact.DiskPath, info.RootfsPath)
	assert.Equal(t, artifact.StateFile.SHA256, info.Checksum)
	assert.Equal(t, artifact.StateFile.Size+artifact.MemoryFile.Size+artifact.DiskFile.Size, info.SizeBytes)
}

// TestAgentExecutorReportsGuestStderr pins the failure message a re-address leaves
// an operator with: the guest's stderr, or a sentence saying it wrote none.
func TestAgentExecutorReportsGuestStderr(t *testing.T) {
	assert.Equal(t, "ip: cannot find device", guestStderr("  ip: cannot find device\n"))
	assert.Equal(t, "the guest wrote no error output", guestStderr(" \n"))
}

// fakeGuest is a guest agent connection whose answers are scripted.
type fakeGuest struct {
	result agent.ExecResult
	err    error
	stderr string

	commands [][]string
	closed   int
}

func (g *fakeGuest) ExecStatus(_ context.Context, cmd []string, opts agent.ExecOptions) (agent.ExecResult, error) {
	g.commands = append(g.commands, cmd)
	if g.stderr != "" && opts.Stderr != nil {
		if _, err := opts.Stderr.Write([]byte(g.stderr)); err != nil {
			return agent.ExecResult{}, err
		}
	}
	return g.result, g.err
}

func (g *fakeGuest) Close() error {
	g.closed++
	return nil
}

// TestAgentExecutor pins the adapter the restore drives in production: argv reaches
// the guest unchanged, a zero exit is success, and a non-zero exit is an error that
// carries the status and the guest's stderr.
func TestAgentExecutor(t *testing.T) {
	cases := []struct {
		name       string
		guest      *fakeGuest
		argv       []string
		wantCode   int
		wantErr    string
		wantNoCall bool
	}{
		{
			name:  "a successful command",
			guest: &fakeGuest{},
			argv:  []string{"ip", "addr", "flush", "dev", "eth0"},
		},
		{
			name:     "a non-zero exit carries the status and stderr",
			guest:    &fakeGuest{result: agent.ExecResult{ExitCode: 2}, stderr: "RTNETLINK answers: File exists\n"},
			argv:     []string{"ip", "addr", "add", "192.168.127.37/24", "dev", "eth0"},
			wantCode: 2,
			wantErr:  "exit status 2: RTNETLINK answers: File exists",
		},
		{
			name:     "a silent failure says so",
			guest:    &fakeGuest{result: agent.ExecResult{ExitCode: 1}},
			argv:     []string{"ip", "route", "replace", "default", "via", "192.168.127.2"},
			wantCode: 1,
			wantErr:  "the guest wrote no error output",
		},
		{
			name:     "a guest-agent failure is not reported as success",
			guest:    &fakeGuest{err: errors.New("exec: send request: broken pipe")},
			argv:     []string{"ip", "addr", "flush", "dev", "eth0"},
			wantErr:  "broken pipe",
			wantCode: 0,
		},
		{
			name:       "an empty command is refused before the guest is asked",
			guest:      &fakeGuest{},
			argv:       nil,
			wantErr:    "empty command",
			wantNoCall: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := &agentExecutor{client: tc.guest}

			code, err := executor.Exec(context.Background(), tc.argv)
			assert.Equal(t, tc.wantCode, code)
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			}

			if tc.wantNoCall {
				assert.Empty(t, tc.guest.commands)
				return
			}
			assert.Equal(t, [][]string{tc.argv}, tc.guest.commands, "argv reaches the guest unchanged")
		})
	}
}

// TestAgentExecutorClose pins that the adapter is the io.Closer the restore
// releases the connection with.
func TestAgentExecutorClose(t *testing.T) {
	guest := &fakeGuest{}
	executor := &agentExecutor{client: guest}

	var closer io.Closer = executor
	require.NoError(t, closer.Close())
	assert.Equal(t, 1, guest.closed)
}
