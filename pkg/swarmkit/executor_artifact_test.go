package swarmkit

// The task start of a node that was told what to restore.
//
// Every test here drives Controller.Start, because that is the seam the feature lives
// at: the directive is claimed before any boot, and the whole point of the "no directive"
// case is that the start is the one this executor always performed. The collaborators
// record what they were asked, so a start can be compared call for call rather than by
// whether it returned an error.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/migration"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// startCalls is the ordered record of what a task start asked of its collaborators.
type startCalls struct {
	mu    sync.Mutex
	calls []string
}

func (l *startCalls) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *startCalls) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// fakePendingStore is the node's directive store as a task start uses it.
type fakePendingStore struct {
	directive scruntime.PendingRestore
	found     bool
	takeErr   error
	peekErr   error
	putErr    error

	takes     []string
	roots     []string
	peeks     []string
	peekRoots []string
	puts      []scruntime.PendingRestore
}

func (f *fakePendingStore) Take(serviceID, root string) (scruntime.PendingRestore, bool, error) {
	f.takes = append(f.takes, serviceID)
	f.roots = append(f.roots, root)
	if f.takeErr != nil {
		return scruntime.PendingRestore{}, false, f.takeErr
	}
	record, found := f.directive, f.found
	// The real store consumes a directive as it hands it out; a second task of the
	// same service must find nothing waiting.
	f.found = false
	return record, found, nil
}

// Peek answers what Take would, without consuming: it is what Prepare asks.
func (f *fakePendingStore) Peek(serviceID, root string) (scruntime.PendingRestore, bool, error) {
	f.peeks = append(f.peeks, serviceID)
	f.peekRoots = append(f.peekRoots, root)
	if f.peekErr != nil {
		return scruntime.PendingRestore{}, false, f.peekErr
	}
	return f.directive, f.found, nil
}

func (f *fakePendingStore) Put(record scruntime.PendingRestore) error {
	f.puts = append(f.puts, record)
	if f.putErr != nil {
		return f.putErr
	}
	// A put directive is a directive waiting again: a retry of the same start must
	// claim it.
	f.directive, f.found = record, true
	return nil
}

// fakeArtifactRestorer is pkg/snapshot's manager as the executor drives it: it records
// the info, socket and console path it was handed, and answers what a load would.
type fakeArtifactRestorer struct {
	log     *startCalls
	err     error
	stopErr error

	infos    []*snapshot.SnapshotInfo
	sockets  []string
	consoles []string
	stops    []string
}

func (f *fakeArtifactRestorer) RestoreArtifact(
	_ context.Context, info *snapshot.SnapshotInfo, socketPath, consolePath string,
) error {
	f.infos = append(f.infos, info)
	f.sockets = append(f.sockets, socketPath)
	f.consoles = append(f.consoles, consolePath)
	if f.log != nil {
		f.log.add("restore")
	}
	return f.err
}

func (f *fakeArtifactRestorer) StopRestored(taskID string) error {
	f.stops = append(f.stops, taskID)
	return f.stopErr
}

// fakeRestoreGuest is the guest agent of a restored VM: it records the argv it was run
// with, which is how a test reads the address the guest was given.
type fakeRestoreGuest struct {
	commands [][]string
}

func (g *fakeRestoreGuest) Exec(_ context.Context, argv []string) (int, error) {
	g.commands = append(g.commands, argv)
	return 0, nil
}

func (g *fakeRestoreGuest) Close() error { return nil }

// fakeRestoreDialer answers the guest agent at any muxer path, so a restore completes
// without a VM.
type fakeRestoreDialer struct {
	guest migration.Executor
	err   error
	paths []string
}

func (d *fakeRestoreDialer) Dial(_ context.Context, vsockPath string) (migration.Executor, error) {
	d.paths = append(d.paths, vsockPath)
	if d.err != nil {
		return nil, d.err
	}
	return d.guest, nil
}

// artifactStartFixture is one node's task start: the controller, the collaborators it
// records into, and the staged artifact a directive may name.
type artifactStartFixture struct {
	ctrl     *Controller
	calls    *startCalls
	pending  *fakePendingStore
	restorer *fakeArtifactRestorer
	vmm      *MockVMMManager
	store    *fakeRestoredStore
	guest    *fakeRestoreGuest
	dialer   *fakeRestoreDialer

	logs      *bytes.Buffer
	root      string
	socketDir string
	logDir    string
	artifact  migration.Artifact
	directive scruntime.PendingRestore
}

// artifactStartOptions are the parts of the fixture a test varies. The zero value is a
// node with no migration wiring at all: a plain boot.
type artifactStartOptions struct {
	// withMigration wires a directive store, a restorer and the fake effects. Its
	// absence is a node that restores nothing, which is the baseline a start is
	// compared against.
	withMigration bool
	// directivePending makes the store hold the fixture's staged directive. Without
	// it, a wired node has nothing prepared for the service — the ordinary case.
	directivePending bool
	restoreErr       error
	// noService builds a task that belongs to no service.
	noService bool
}

const (
	artifactServiceID = "service-1"
	artifactTaskID    = "new-task"
	artifactSourceID  = "source-task"
	artifactBridge    = "swarm-br0"
	artifactBridgeIP  = "192.168.127.1/24"
	artifactGuestIP   = "192.168.127.37/24"
	artifactNetwork   = "ingress"
)

// artifactFixtureStaged stages the artifact the fixture's directive names: three small
// files with the sizes and digests the manifest describes, under <root>/<artifactID>.
func artifactFixtureStaged(t *testing.T, root string) (migration.Artifact, scruntime.PendingRestore) {
	t.Helper()

	dir := filepath.Join(root, "mig-0123456789abcdef")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	files := map[string]string{
		"vm.state":    "state of the source VM",
		"vm.mem":      "memory of the source VM",
		"rootfs.ext4": "disk of the source VM",
	}
	described := make(map[string]migration.File, len(files))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
		sum := sha256.Sum256([]byte(body))
		described[name] = migration.File{
			Name:   name,
			Size:   int64(len(body)),
			SHA256: hex.EncodeToString(sum[:]),
		}
	}

	artifact := migration.Artifact{
		SchemaVersion:   v1.SchemaVersion,
		Kind:            migration.ArtifactKind,
		ID:              "mig-0123456789abcdef",
		SnapshotID:      "snap-0123456789abcdef",
		SourceTaskID:    artifactSourceID,
		RequestedTaskID: artifactSourceID,
		SourceNodeID:    "source-node",
		ServiceID:       artifactServiceID,
		CreatedAt:       time.Now().UTC(),
		SnapshotAt:      time.Now().UTC().Add(-time.Minute),
		TapName:         scruntime.TapName(artifactSourceID, 0),
		VsockPath:       scruntime.VsockPath("/var/run/firecracker", artifactSourceID),
		DiskPath:        "/var/lib/firecracker/rootfs/tasks/" + artifactSourceID + ".ext4",
		StateFile:       described["vm.state"],
		MemoryFile:      described["vm.mem"],
		DiskFile:        described["rootfs.ext4"],
	}
	require.NoError(t, artifact.Validate())

	body, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, migration.ArtifactDocumentName), body, 0o644))

	directive := scruntime.PendingRestore{
		ServiceID:  artifactServiceID,
		ArtifactID: artifact.ID,
		Dir:        dir,
		CreatedAt:  artifact.CreatedAt,
	}
	return artifact, directive
}

// newArtifactStartFixture builds a task start for one prepared task. Everything the
// start touches records into calls, in order.
func newArtifactStartFixture(t *testing.T, opts artifactStartOptions) *artifactStartFixture {
	t.Helper()

	root := t.TempDir()
	socketDir := t.TempDir()
	logDir := t.TempDir()
	artifact, directive := artifactFixtureStaged(t, root)

	serviceID := artifactServiceID
	if opts.noService {
		serviceID = ""
	}

	calls := &startCalls{}
	fixture := &artifactStartFixture{
		calls:     calls,
		logs:      &bytes.Buffer{},
		root:      root,
		socketDir: socketDir,
		logDir:    logDir,
		artifact:  artifact,
		directive: directive,
		guest:     &fakeRestoreGuest{},
	}
	fixture.dialer = &fakeRestoreDialer{guest: fixture.guest}
	fixture.vmm = &MockVMMManager{
		StartFunc: func(context.Context, *types.Task, interface{}) error {
			calls.add("vmm.Start")
			return nil
		},
		WaitFunc: func(context.Context, *types.Task) (*types.TaskStatus, error) {
			calls.add("vmm.Wait")
			return &types.TaskStatus{State: types.TaskStateRunning}, nil
		},
	}
	// The store records what the restore writes, so a teardown (and the vsock
	// resolution a start performs) sees the VM the restore recorded.
	fixture.store = &fakeRestoredStore{}
	fixture.restorer = &fakeArtifactRestorer{log: calls, err: opts.restoreErr}

	task := &api.Task{ID: artifactTaskID, ServiceID: serviceID}
	fixture.ctrl = &Controller{
		task: task,
		config: &Config{
			RootfsDir:  t.TempDir(),
			SocketDir:  socketDir,
			LogDir:     logDir,
			BridgeName: artifactBridge,
			BridgeIP:   artifactBridgeIP,
			Hostname:   "target-node",
		},
		vmmMgr: fixture.vmm,
		trans: &MockTaskTranslator{TranslateFunc: func(*types.Task) (interface{}, error) {
			calls.add("translate")
			return map[string]interface{}{}, nil
		}},
		networkMgr: &MockNetworkManager{GetTapIPFunc: func(string) (string, error) {
			return "192.168.127.37", nil
		}},
		logger:       zerolog.New(fixture.logs).With().Timestamp().Logger(),
		prepared:     true,
		internalTask: preparedTask(artifactTaskID, serviceID),
		restored:     fixture.store,
	}

	if opts.withMigration {
		fixture.pending = &fakePendingStore{}
		if opts.directivePending {
			fixture.pending.directive = directive
			fixture.pending.found = true
		}
		tap := TapEffects{
			Create: func(name, bridge string) error {
				calls.add("tap.create " + name + " " + bridge)
				return nil
			},
			Up: func(name string) error {
				calls.add("tap.up " + name)
				return nil
			},
			Delete: func(name string) error {
				calls.add("tap.delete " + name)
				return nil
			},
		}
		fixture.ctrl.migration = &MigrationRestoreConfig{
			Pending:  fixture.pending,
			Root:     root,
			Restorer: fixture.restorer,
			Agent:    fixture.dialer,
			Tap:      tap,
		}
	}
	return fixture
}

// preparedTask is the task a start boots or restores: one network attachment with the
// address SwarmKit allocated for it, which is what the boot path reads too.
func preparedTask(taskID, serviceID string) *types.Task {
	return &types.Task{
		ID:        taskID,
		ServiceID: serviceID,
		NodeID:    "target-node",
		Networks: []types.NetworkAttachment{{
			Network: types.Network{
				ID:   "net-1",
				Spec: types.NetworkSpec{Name: artifactNetwork, Driver: "bridge", Subnet: "192.168.127.0/24"},
			},
			Addresses: []string{artifactGuestIP},
		}},
	}
}

// TestStartWithoutADirectiveBootsExactlyAsBefore is the no-behaviour-change rule, and it
// compares the call log rather than asserting "no error": a start on a node wired for
// migrations whose service has nothing prepared must ask its collaborators the same
// things, in the same order, as a start on a node with no migration wiring at all.
func TestStartWithoutADirectiveBootsExactlyAsBefore(t *testing.T) {
	baseline := newArtifactStartFixture(t, artifactStartOptions{})
	require.NoError(t, baseline.ctrl.Start(context.Background()))

	wired := newArtifactStartFixture(t, artifactStartOptions{withMigration: true})
	require.NoError(t, wired.ctrl.Start(context.Background()))

	assert.Equal(t, baseline.calls.list(), wired.calls.list(),
		"a task whose service has no directive boots exactly as it did before this feature")
	assert.Equal(t, []string{artifactServiceID}, wired.pending.takes,
		"the one lookup the feature adds on the hot path")
	assert.Equal(t, []string{wired.root}, wired.pending.roots)
	assert.Empty(t, wired.pending.puts)
	assert.Empty(t, wired.restorer.infos, "no artifact is loaded without a directive")
	assert.Empty(t, wired.dialer.paths)
}

// TestStartRestoresTheStagedArtifactInsteadOfBooting pins the directive's whole effect:
// the VM is loaded from the artifact, the TAP is created down and brought up after the
// guest is re-addressed, and no fresh VM is booted beside it.
func TestStartRestoresTheStagedArtifactInsteadOfBooting(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{
		withMigration:    true,
		directivePending: true,
	})

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	require.Len(t, fixture.restorer.infos, 1, "the artifact is loaded exactly once")
	info := fixture.restorer.infos[0]
	assert.Equal(t, fixture.directive.Dir, filepath.Dir(info.StatePath),
		"the state file is the staged copy")
	assert.Equal(t, filepath.Join(fixture.directive.Dir, "vm.mem"), info.MemoryPath)
	assert.Equal(t, filepath.Join(fixture.directive.Dir, "rootfs.ext4"), info.DiskPath)
	assert.Equal(t, artifactTaskID, info.TaskID, "the restorer tracks the process by the NEW task")
	assert.Equal(t, fixture.artifact.DiskPath, info.RootfsPath,
		"the disk is materialised at the path the state file recorded")

	assert.Equal(t, filepath.Join(fixture.socketDir, artifactTaskID+scruntime.VMSocketSuffix),
		fixture.restorer.sockets[0], "the API socket belongs to the new task")

	assert.Equal(t, []string{
		"tap.create " + scruntime.TapName(artifactSourceID, 0) + " " + artifactBridge,
		"restore",
		"tap.up " + scruntime.TapName(artifactSourceID, 0),
	}, fixture.calls.list(), "the TAP is down while the guest is re-addressed")
	assert.NotContains(t, fixture.calls.list(), "translate", "a restored task is not translated into a boot")
	assert.NotContains(t, fixture.calls.list(), "vmm.Start", "no fresh VM is booted beside the restored one")

	assert.Empty(t, fixture.pending.puts, "a successful restore does not put the directive back")
	assert.Empty(t, fixture.pending.takes[1:], "the directive is claimed once")
}

// TestStartGivesTheGuestTheTasksAddress pins the network identity a moved guest adopts:
// the address from the task's own attachment and the gateway from the node's bridge
// address, which is what the boot path would have put on the kernel command line.
func TestStartGivesTheGuestTheTasksAddress(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	require.Len(t, fixture.dialer.paths, 1)
	assert.Equal(t, fixture.artifact.VsockPath, fixture.dialer.paths[0],
		"the agent is dialled at the muxer the artifact recorded")

	assert.Equal(t, [][]string{
		{"ip", "addr", "flush", "dev", "eth0"},
		{"ip", "addr", "add", artifactGuestIP, "dev", "eth0"},
		{"ip", "route", "replace", "default", "via", "192.168.127.1"},
	}, fixture.guest.commands)
}

// TestStartGivesTheRestoredGuestTheNewTasksConsole pins the log path: the console the
// restorer opens is the new task's, because that is the file `vm logs` on the moved task
// reads. The source task's ID is inside the artifact and must not become this node's log.
func TestStartGivesTheRestoredGuestTheNewTasksConsole(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	require.Len(t, fixture.restorer.consoles, 1)
	assert.Equal(t, filepath.Join(fixture.logDir, artifactTaskID+".log"),
		fixture.restorer.consoles[0])
	assert.NotContains(t, fixture.restorer.consoles[0], artifactSourceID)
}

// TestStartDropsTheStagedBytesOnSuccess pins the cleanup: the artifact was this node's
// only until the guest ran. The source's discard owns the snapshot; this node's copy is
// dead weight and a second restore's shadow.
func TestStartDropsTheStagedBytesOnSuccess(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	assert.NoDirExists(t, fixture.directive.Dir)
}

// TestStartKeepsTheDirectiveAndTheBytesOnFailure pins the retry contract: a restore that
// failed puts the directive back with the instant it was prepared at — so the retry
// still restores rather than booting — and leaves the artifact where the retry will find
// it.
func TestStartKeepsTheDirectiveAndTheBytesOnFailure(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{
		withMigration:    true,
		directivePending: true,
		restoreErr:       errors.New("the state file is not a Firecracker state"),
	})

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "the state file is not a Firecracker state")

	require.Len(t, fixture.pending.puts, 1, "the retry must restore the same artifact")
	assert.Equal(t, fixture.directive, fixture.pending.puts[0],
		"the directive goes back exactly as it was, CreatedAt included")
	assert.DirExists(t, fixture.directive.Dir, "the bytes are what the retry restores")
	assert.FileExists(t, filepath.Join(fixture.directive.Dir, migration.ArtifactDocumentName))

	assert.NotContains(t, fixture.calls.list(), "vmm.Start",
		"a task whose restore failed is not also booted cold")
	assert.Equal(t, []string{artifactTaskID}, fixture.restorer.stops,
		"the failure takes down the process the restorer may have started")
}

// TestStartRefusesWithoutANodeConfig pins the wiring mistake a restore cannot survive:
// a controller with a directive store but no node configuration has no bridge, socket
// directory or log directory to give the guest, and must fail rather than panic.
func TestStartRefusesWithoutANodeConfig(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{
		withMigration:    true,
		directivePending: true,
	})
	fixture.ctrl.config = nil

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "no node configuration")
	assert.Empty(t, fixture.restorer.infos)
	assert.Len(t, fixture.pending.puts, 1, "the directive survives for the next attempt")
}

// TestStartReportsAProcessItCouldNotStop pins the undo's own failure: a restore that
// failed and left a VM the restorer could not stop is reported with both, because the
// second is a process nobody can resolve.
func TestStartReportsAProcessItCouldNotStop(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{
		withMigration:    true,
		directivePending: true,
		restoreErr:       errors.New("the load was refused"),
	})
	fixture.restorer.stopErr = errors.New("signal SIGKILL: operation not permitted")

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "the load was refused")
	assert.ErrorContains(t, err, "stop the restored VM")
	assert.ErrorContains(t, err, "operation not permitted")
}

// TestStartReportsADirectiveItCouldNotPutBack pins the honest failure: when the retry
// cannot restore, the caller hears both the restore failure and the missing directive,
// because the second is what makes the next attempt boot cold.
func TestStartReportsADirectiveItCouldNotPutBack(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{
		withMigration:    true,
		directivePending: true,
		restoreErr:       errors.New("load failed"),
	})
	fixture.pending.putErr = errors.New("read-only file system")

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "load failed")
	assert.ErrorContains(t, err, "could not be put back")
	assert.ErrorContains(t, err, "read-only file system")
}

// TestStartConsumesADirectiveOnce pins the store's contract through the start path: two
// tasks of one service starting one after another restore one artifact. The second task
// finds nothing prepared and boots, which is exactly what a directive per service means.
func TestStartConsumesADirectiveOnce(t *testing.T) {
	first := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, first.ctrl.Start(context.Background()))

	// The same node, the same store: the directive was consumed by the first task.
	second := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	second.pending = first.pending
	second.ctrl.migration.Pending = first.pending

	require.NoError(t, second.ctrl.Start(context.Background()))

	assert.Empty(t, second.restorer.infos, "a second task of the service does not restore again")
	assert.Contains(t, second.calls.list(), "vmm.Start", "it boots its own VM instead")
}

// TestStartHandlesAnUnusableDirective pins the three answers a lookup can give: an
// expired directive (returned with found == false and Expired set) boots cold because the
// store has deleted it, a directive this build cannot use fails the task and is left for the
// retry, and a task that belongs to no service looks nothing up at all.
func TestStartHandlesAnUnusableDirective(t *testing.T) {
	t.Run("an expired directive is ignored", func(t *testing.T) {
		fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
		expired := fixture.directive
		expired.CreatedAt = time.Now().UTC().Add(-2 * scruntime.DefaultPendingRestoreTTL)
		// The store's answer for a directive past its TTL: the record comes back with
		// found == false and Expired set, so the caller knows which directive it
		// ignored rather than guessing from a zero instant.
		expired.Expired = true
		fixture.pending.directive, fixture.pending.found = expired, false

		require.NoError(t, fixture.ctrl.Start(context.Background()))

		assert.Contains(t, fixture.calls.list(), "vmm.Start")
		assert.Empty(t, fixture.restorer.infos)
		assert.Empty(t, fixture.pending.puts, "an expired directive is the store's to drop, not this task's to keep")

		// One warning, naming the service and the artifact it ignored: an operator has
		// to be able to see that a migration's artifact expired instead of being restored.
		logged := fixture.logs.String()
		assert.Contains(t, logged, "older than its TTL")
		assert.Contains(t, logged, artifactServiceID)
		assert.Contains(t, logged, fixture.directive.ArtifactID)
		assert.Equal(t, 1, strings.Count(logged, "older than its TTL"), "one line, not one per retry")
	})

	t.Run("a store error fails the task and keeps the directive", func(t *testing.T) {
		fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
		fixture.pending.takeErr = errors.New("the directive is corrupt")

		err := fixture.ctrl.Start(context.Background())
		require.Error(t, err, "an unusable directive is not a licence to boot a second VM")
		assert.ErrorContains(t, err, "the directive is corrupt")

		assert.NotContains(t, fixture.calls.list(), "vmm.Start",
			"the source node's VM is still running: a cold boot here would be a second one")
		assert.Empty(t, fixture.restorer.infos)
		assert.Empty(t, fixture.pending.puts,
			"the store already put the record back, so the retry finds the same directive")
	})

	t.Run("a task with no service looks nothing up", func(t *testing.T) {
		fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, noService: true})

		require.NoError(t, fixture.ctrl.Start(context.Background()))

		assert.Empty(t, fixture.pending.takes)
		assert.Contains(t, fixture.calls.list(), "vmm.Start")
	})
}

// TestStartRestoresOncePerTask pins that a start that already ran does not claim a
// second directive: a re-entrant Start is the task manager repeating itself, not a
// second migration.
func TestStartRestoresOncePerTask(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, fixture.ctrl.Start(context.Background()))

	// A new directive lands for the same service after the task is running.
	fixture.pending.directive = fixture.directive
	fixture.pending.found = true
	fixture.pending.directive.ArtifactID = "mig-fedcba9876543210"

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	assert.Len(t, fixture.restorer.infos, 1, "a started task does not restore a second VM")
}

// TestStartFailsWhenTheStagedManifestDoesNotMatchTheDirective pins the two mismatches a
// staging directory can hold: a manifest that validates but names another artifact, and
// one exported from another service. Both fail the task, keep the directive, and restore
// nothing — a VM nobody asked this task to run is worse than a failed task.
func TestStartFailsWhenTheStagedManifestDoesNotMatchTheDirective(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fixture *artifactStartFixture)
		want   string
	}{
		{
			name: "another artifact",
			mutate: func(t *testing.T, fixture *artifactStartFixture) {
				fixture.pending.directive.ArtifactID = "mig-fedcba9876543210"
			},
			want: "describes artifact",
		},
		{
			name: "another service",
			mutate: func(t *testing.T, fixture *artifactStartFixture) {
				rewriteArtifact(t, fixture.directive.Dir, func(a *migration.Artifact) {
					a.ServiceID = "service-2"
				})
			},
			want: "was exported from service",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
			tc.mutate(t, fixture)

			err := fixture.ctrl.Start(context.Background())
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, fixture.restorer.infos)
			require.Len(t, fixture.pending.puts, 1, "the directive survives for a human to look at")
			assert.DirExists(t, fixture.directive.Dir)
		})
	}
}

// rewriteArtifact edits a staged manifest in place, leaving its files alone.
func rewriteArtifact(t *testing.T, dir string, mutate func(*migration.Artifact)) {
	t.Helper()

	path := filepath.Join(dir, migration.ArtifactDocumentName)
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	var artifact migration.Artifact
	require.NoError(t, json.Unmarshal(body, &artifact))
	mutate(&artifact)
	require.NoError(t, artifact.Validate())

	body, err = json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, body, 0o644))
}

// TestStartFailsWhenTheTaskHasNoAddressToGive pins the derivation's refusal: a restore
// with no attachment address cannot re-address the guest, and a guest left on the source
// node's address would answer for the service from two nodes.
func TestStartFailsWhenTheTaskHasNoAddressToGive(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*types.Task)
		want   string
	}{
		{
			name:   "no attachment",
			mutate: func(task *types.Task) { task.Networks = nil },
			want:   "no network attachment",
		},
		{
			name:   "no address",
			mutate: func(task *types.Task) { task.Networks[0].Addresses = nil },
			want:   "has no address",
		},
		{
			name:   "an address that is not one",
			mutate: func(task *types.Task) { task.Networks[0].Addresses = []string{"not-an-address"} },
			want:   "is not an IP address",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
			tc.mutate(fixture.ctrl.internalTask)

			err := fixture.ctrl.Start(context.Background())
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, fixture.restorer.infos)
			assert.Len(t, fixture.pending.puts, 1)
		})
	}
}

// TestRestoredGuestAddressFollowsTheBootPath pins the derivation against the values the
// boot path reads: the first attachment's first address, the network's gateway when it
// names one, and the node's bridge address otherwise.
func TestRestoredGuestAddressFollowsTheBootPath(t *testing.T) {
	cases := []struct {
		name        string
		addresses   []string
		subnet      string
		gateway     string
		bridgeIP    string
		wantCIDR    string
		wantGateway string
		wantErr     string
	}{
		{
			name:        "an address with its prefix",
			addresses:   []string{"192.168.127.37/24"},
			subnet:      "192.168.127.0/24",
			bridgeIP:    "192.168.127.1/24",
			wantCIDR:    "192.168.127.37/24",
			wantGateway: "192.168.127.1",
		},
		{
			name:        "a bare address takes the network's prefix",
			addresses:   []string{"10.10.0.9"},
			subnet:      "10.10.0.0/16",
			bridgeIP:    "192.168.127.1/24",
			wantCIDR:    "10.10.0.9/16",
			wantGateway: "192.168.127.1",
		},
		{
			// The boot path hands the kernel the network's netmask and strips any
			// prefix the attachment address carries (networkBootArgs). A restored guest
			// must come up on the same broadcast domain the booted one would have.
			name:        "the network's prefix wins over the address's",
			addresses:   []string{"10.10.9.7/16"},
			subnet:      "10.10.0.0/24",
			bridgeIP:    "192.168.127.1/24",
			wantCIDR:    "10.10.9.7/24",
			wantGateway: "192.168.127.1",
		},
		{
			name:        "a bare address on a network with no subnet takes /24",
			addresses:   []string{"192.168.127.40"},
			bridgeIP:    "192.168.127.1/24",
			wantCIDR:    "192.168.127.40/24",
			wantGateway: "192.168.127.1",
		},
		{
			name:        "the network's gateway wins over the bridge",
			addresses:   []string{"10.10.0.9/16"},
			subnet:      "10.10.0.0/16",
			gateway:     "10.10.0.1",
			bridgeIP:    "192.168.127.1/24",
			wantCIDR:    "10.10.0.9/16",
			wantGateway: "10.10.0.1",
		},
		{
			name:      "an IPv6 address is refused",
			addresses: []string{"fd00::1/64"},
			bridgeIP:  "192.168.127.1/24",
			wantErr:   "not IPv4",
		},
		{
			name:      "no gateway anywhere",
			addresses: []string{"192.168.127.37/24"},
			wantErr:   "no gateway",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &types.Task{Networks: []types.NetworkAttachment{{
				Network: types.Network{Spec: types.NetworkSpec{
					Name:    artifactNetwork,
					Subnet:  tc.subnet,
					Gateway: tc.gateway,
				}},
				Addresses: tc.addresses,
			}}}

			cidr, gateway, err := restoredGuestAddress(task, tc.bridgeIP)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantCIDR, cidr)
			assert.Equal(t, tc.wantGateway, gateway)
		})
	}
}

// TestWaitOnARestoredVMRecordsNoTerminalEvent pins the metric guard. A restored VM was
// never counted as started by this executor (that happens in startVM, which a restore
// replaces), so its end must move no counter: recording a crash here decrements
// vms_running for a VM it never incremented, and marks the task's service as crashing.
func TestWaitOnARestoredVMRecordsNoTerminalEvent(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, fixture.ctrl.Start(context.Background()))

	// The VM has exited: the process behind the recorded socket is gone.
	previousAlive := restoredVMAlive
	restoredVMAlive = func(string) bool { return false }
	t.Cleanup(func() { restoredVMAlive = previousAlive })

	crashesBefore := testutil.ToFloat64(swarmcrackermetrics.VMCrashes.WithLabelValues(
		"target-node", artifactServiceID, "unexpected_exit"))
	runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)

	require.NoError(t, fixture.ctrl.Wait(context.Background()),
		"the task waits for the VM it runs, not for a process the VMM manager never had")

	assert.Equal(t, crashesBefore, testutil.ToFloat64(swarmcrackermetrics.VMCrashes.WithLabelValues(
		"target-node", artifactServiceID, "unexpected_exit")),
		"a restored VM's end is not a crash this executor counted")
	assert.Equal(t, runningBefore, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
		"a VM this executor never counted running is not counted stopped")
	assert.False(t, fixture.ctrl.terminalClaimed, "nothing claimed the terminal event")

	// And the VMM manager was never asked: it tracks no process for this task.
	assert.NotContains(t, fixture.calls.list(), "vmm.Wait",
		"the VMM manager is not asked to wait for a VM it never started")
}

// TestWaitOnARestoredVMBoundsItself pins the wait's other half: while the VM runs, Wait
// blocks, and a cancelled wait returns without recording anything.
func TestWaitOnARestoredVMBoundsItself(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, fixture.ctrl.Start(context.Background()))

	previousAlive := restoredVMAlive
	previousPoll := restoredVMWaitPoll
	restoredVMAlive = func(string) bool { return true }
	restoredVMWaitPoll = time.Millisecond
	t.Cleanup(func() {
		restoredVMAlive = previousAlive
		restoredVMWaitPoll = previousPoll
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, fixture.ctrl.Wait(ctx), context.Canceled)
}
