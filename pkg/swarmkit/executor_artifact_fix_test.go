package swarmkit

// Fix round 1 of the artifact start path. These are the rules the first review found
// missing, each pinned where the executor lives:
//
//	C1 a teardown that races a restore must not leave an orphan VM
//	C2 a restored task's host ports must be published, and unpublished on teardown
//	I1 a stopped restored VM's TAP device and materialised disk are deleted
//	I2 a restored VM killed by its healthcheck must report why
//	I3 only expiry boots cold; any other unusable directive fails the task
//	I4 a directive skips the image pull and the network preparation a boot needs
//	I7 a task on a user-defined network is restored onto that network's bridge
//	M2 the nil seams of restoreDeps select the production effects
//	M7 dropping extra attachments is logged

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/migration"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// gatedArtifactRestorer is a restorer whose load blocks until the test releases it, which
// is the only way to make the "a stop arrives while the VM is loading" window a test can
// stand in: everything before the load, and the load itself, is a VM nobody can see yet.
type gatedArtifactRestorer struct {
	*fakeArtifactRestorer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedArtifactRestorer) RestoreArtifact(
	ctx context.Context, info *snapshot.SnapshotInfo, socketPath, consolePath string,
) error {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return g.fakeArtifactRestorer.RestoreArtifact(ctx, info, socketPath, consolePath)
}

// waitForRestoreStop blocks until the controller has been told to stop the restore it is
// running, so a test can prove the abort is what released the teardown rather than a
// sleep.
func waitForRestoreStop(t *testing.T, ctrl *Controller) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctrl.restoreMu.Lock()
		stopped := ctrl.restoreStopped
		ctrl.restoreMu.Unlock()
		if stopped {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the teardown never marked the task stopped")
}

// restoredFixtureWithPorts is the artifact fixture with a service that publishes a host
// port, and a network manager that records what it is asked to program.
func restoredFixtureWithPorts(t *testing.T, published *[]types.PublishedPort, unpublished *[]string) *artifactStartFixture {
	t.Helper()

	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.ctrl.task.ServiceAnnotations = api.Annotations{
		Labels: map[string]string{types.PublishLabel: "8080:80/tcp"},
	}
	fixture.ctrl.networkMgr = &MockNetworkManager{
		GetTapIPFunc: func(string) (string, error) {
			// A restored task's TAP is the source task's, so the new task has no
			// allocation: the address has to come from the task's own attachment.
			return "", errors.New("no TAP device found")
		},
		PublishPortsFunc: func(_ string, guestIP string, ports []types.PublishedPort) error {
			*published = append(*published, ports...)
			assert.Equal(t, "192.168.127.37", guestIP, "the rules point at the address the restore just configured")
			return nil
		},
		UnpublishPortsFunc: func(taskID string, _ []types.PublishedPort) error {
			*unpublished = append(*unpublished, taskID)
			return nil
		},
	}
	return fixture
}

// TestStopDuringARestoreLeavesNoVMBehind is C1: a teardown that arrives while a load is
// running must not answer "nothing to shut down" and leave the restore to publish a
// running VM afterwards. The teardown waits, the restore aborts at its next checkpoint,
// and what is left is nothing: no process, no TAP device, no record.
func TestStopDuringARestoreLeavesNoVMBehind(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	gated := &gatedArtifactRestorer{
		fakeArtifactRestorer: fixture.restorer,
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	fixture.ctrl.migration.Restorer = gated

	started := make(chan error, 1)
	go func() { started <- fixture.ctrl.Start(context.Background()) }()

	select {
	case <-gated.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the restore never started")
	}

	shutdown := make(chan error, 1)
	go func() { shutdown <- fixture.ctrl.Shutdown(context.Background()) }()

	// The teardown has marked the task stopped and is waiting for the restore; only now
	// may the load finish, and the abort is what it must find.
	waitForRestoreStop(t, fixture.ctrl)
	close(gated.release)

	startErr := <-started
	shutdownErr := <-shutdown

	require.Error(t, startErr, "the restore aborts rather than publishing a VM for a stopped task")
	assert.ErrorContains(t, startErr, "stopped")

	require.NoError(t, shutdownErr, "a teardown that waited for the abort has nothing left to stop")

	// No VM is running: the restorer that started one was told to stop it.
	assert.Equal(t, []string{artifactTaskID}, fixture.restorer.stops,
		"the abort takes down the process the load started")
	assert.Contains(t, fixture.calls.list(), "tap.delete "+scruntime.TapName(artifactSourceID, 0),
		"the abort removes the TAP device too")

	// And no record survives: a record would resolve a task that is gone to a VM that
	// nobody can see.
	assert.False(t, fixture.store.found, "no record survives a stop that raced the restore")
	assert.Empty(t, fixture.restorer.infos[1:], "the VM is restored once")
	assert.Empty(t, fixture.ctrl.inheritedVMSocket(), "nothing was published as this task's VM")

	// The directive goes back, so the replacement task restores the same artifact.
	require.Len(t, fixture.pending.puts, 1, "the migration is not lost to a stop")
	assert.Equal(t, fixture.directive, fixture.pending.puts[0])
}

// TestRestoreRefusesToStartAfterAStop pins the other end of the handshake: a teardown that
// arrived before the restore began must not be forgotten by it. The directive goes back and
// the task fails; nothing is loaded.
func TestRestoreRefusesToStartAfterAStop(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})

	// The teardown ran first: the controller has been told to stop.
	fixture.ctrl.awaitRestoreStop()

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errTaskStoppedBeforeRestore)
	assert.Empty(t, fixture.restorer.infos, "no VM is loaded for a task that was stopped")
	require.Len(t, fixture.pending.puts, 1, "the directive survives for the replacement task")
}

// TestStartUnwindsARestoreWhosePortsCannotBePublished pins the failure path C2 introduces:
// a restored VM with no host forwarding cannot serve, so the task fails — and it fails with
// the VM taken down, the TAP deleted, the record forgotten and the directive back, rather
// than leaving a guest nobody can reach and a consumed directive.
func TestStartUnwindsARestoreWhosePortsCannotBePublished(t *testing.T) {
	var published []types.PublishedPort
	var unpublished []string
	fixture := restoredFixtureWithPorts(t, &published, &unpublished)
	fixture.ctrl.networkMgr = &MockNetworkManager{
		GetTapIPFunc: func(string) (string, error) { return "", errors.New("no TAP device found") },
		PublishPortsFunc: func(string, string, []types.PublishedPort) error {
			return errors.New("port 8080 is already bound")
		},
		UnpublishPortsFunc: func(taskID string, _ []types.PublishedPort) error {
			unpublished = append(unpublished, taskID)
			return nil
		},
	}

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "port 8080 is already bound")

	assert.Equal(t, []string{artifactTaskID}, fixture.restorer.stops,
		"the VM a failed publish leaves behind is stopped")
	assert.Contains(t, fixture.calls.list(), "tap.delete "+scruntime.TapName(artifactSourceID, 0))
	assert.False(t, fixture.store.found, "the record goes with the VM")
	assert.Equal(t, []string{artifactTaskID}, unpublished, "a half-programmed publish is swept")
	require.Len(t, fixture.pending.puts, 1, "the retry restores the same artifact")
	assert.DirExists(t, fixture.directive.Dir)
}

// stoppedDialer answers the guest agent only when the restore is cancelled: it is the
// shape of a load that resumed a guest which never starts answering, and the state the
// restore's own 30-second wait would otherwise hold a teardown in for its whole timeout.
type stoppedDialer struct {
	entered chan struct{}
	once    sync.Once
}

func (d *stoppedDialer) Dial(ctx context.Context, _ string) (migration.Executor, error) {
	d.once.Do(func() { close(d.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestStopDuringTheGuestWaitDoesNotHoldTheTeardown pins that the wait for the guest agent is
// not a 30-second hold on a teardown: the restore's context belongs to the controller, so a
// stop ends it at once and the restore unwinds.
func TestStopDuringTheGuestWaitDoesNotHoldTheTeardown(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	dialer := &stoppedDialer{entered: make(chan struct{})}
	fixture.ctrl.migration.Agent = dialer

	started := make(chan error, 1)
	go func() { started <- fixture.ctrl.Start(context.Background()) }()

	select {
	case <-dialer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the restore never reached the guest-agent wait")
	}

	shutdown := make(chan error, 1)
	go func() { shutdown <- fixture.ctrl.Shutdown(context.Background()) }()

	select {
	case err := <-shutdown:
		require.NoError(t, err, "the teardown has nothing left to stop")
	case <-time.After(10 * time.Second):
		t.Fatal("the teardown waited for the aborted restore's guest-agent timeout")
	}

	startErr := <-started
	require.Error(t, startErr, "a restore whose guest never answered is not a running task")
	assert.Equal(t, []string{artifactTaskID}, fixture.restorer.stops,
		"the VM the load started is stopped by the undo")
	assert.False(t, fixture.store.found, "no record survives")
	require.Len(t, fixture.pending.puts, 1)
}

// TestStartPublishesTheTasksPortsAfterARestore is C2: the boot path programs host
// forwarding inside startVM, which a restored task never runs. Without this a moved task
// with published ports comes up with dead rules and no error at all.
func TestStartPublishesTheTasksPortsAfterARestore(t *testing.T) {
	var published []types.PublishedPort
	var unpublished []string
	fixture := restoredFixtureWithPorts(t, &published, &unpublished)

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	assert.Equal(t, []types.PublishedPort{{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080}}, published,
		"the restored task's host ports are programmed")

	// The teardown releases them: a task stopped by an update never reaches Remove, so a
	// rule left behind would shadow the replacement's.
	fixture.ctrl.restoredStopper = &fakeRestoredStopper{}
	require.NoError(t, fixture.ctrl.Shutdown(context.Background()))
	assert.Equal(t, []string{artifactTaskID}, unpublished,
		"a stopped restored task unpublishes the ports its restore published")
}

// TestTeardownRemovesTheRestoredTAPAndDisk is I1. The node's teardown is keyed by the task
// it runs, but the two host-side names a restored VM leaves are the source task's: the TAP
// device the snapshot recorded and the disk the restore materialised at the source path.
// Nothing else on the node deletes them, so a migration that completes leaks both.
func TestTeardownRemovesTheRestoredTAPAndDisk(t *testing.T) {
	var taps, disks []string

	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			taps, disks = nil, nil

			record := restoredTestRecord("new-task", "source-task")
			fixture := newTeardownFixture(t,
				&fakeRestoredStore{record: record, found: true},
				&fakeRestoredStopper{},
				true)
			fixture.ctrl.restoredLeftovers = RestoredLeftovers{
				DeleteTap: func(name string) error {
					taps = append(taps, name)
					return nil
				},
				DeleteDisk: func(path string) error {
					disks = append(disks, path)
					return nil
				},
			}

			require.NoError(t, call.call(fixture.ctrl))

			assert.Equal(t, []string{record.TapName}, taps,
				"the device is deleted under the name the record holds, not the new task's")
			assert.Equal(t, []string{record.DiskPath}, disks,
				"the disk the restore materialised is deleted with the record")
		})
	}
}

// TestTeardownKeepsTheLeftoversWhenTheStopFails pins the other half of I1: a stop that
// failed may have left the VM running, so its device and its disk are still in use. Only a
// VM confirmed gone leaves garbage.
func TestTeardownKeepsTheLeftoversWhenTheStopFails(t *testing.T) {
	record := restoredTestRecord("new-task", "source-task")
	fixture := newTeardownFixture(t,
		&fakeRestoredStore{record: record, found: true},
		&fakeRestoredStopper{err: errors.New("signal SIGKILL: operation not permitted")},
		true)

	var taps, disks []string
	fixture.ctrl.restoredLeftovers = RestoredLeftovers{
		DeleteTap:  func(name string) error { taps = append(taps, name); return nil },
		DeleteDisk: func(path string) error { disks = append(disks, path); return nil },
	}

	require.Error(t, fixture.ctrl.Shutdown(context.Background()))
	assert.Empty(t, taps, "a live VM keeps its device")
	assert.Empty(t, disks, "a live VM keeps the disk it runs from")
}

// TestWaitOnARestoredVMReportsAHealthCheckFailure is I2: the process belongs to
// pkg/snapshot, so a migrated VM force-stopped by its health monitor is the one exit this
// executor learns the reason for. Answering nil would turn "killed for failing its check"
// into COMPLETE with no reason — the opposite of what happened.
func TestWaitOnARestoredVMReportsAHealthCheckFailure(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, fixture.ctrl.Start(context.Background()))

	previousAlive := restoredVMAlive
	restoredVMAlive = func(string) bool { return false }
	t.Cleanup(func() { restoredVMAlive = previousAlive })

	fixture.ctrl.healthMu.Lock()
	fixture.ctrl.healthFailure = "guest agent reports the service is not ready"
	fixture.ctrl.healthMu.Unlock()

	err := fixture.ctrl.Wait(context.Background())
	require.Error(t, err, "a migrated VM killed by its healthcheck is not a clean completion")
	assert.ErrorContains(t, err, "healthcheck failed")
	assert.ErrorContains(t, err, "guest agent reports the service is not ready")
	assert.False(t, fixture.ctrl.terminalClaimed,
		"the reason travels to SwarmKit; it does not move a counter this executor never moved")
}

// TestWaitOnARestoredVMWithoutAFailureStillCompletes pins that I2 did not turn every exit
// into a failure: a VM that went away on its own is still a nil wait.
func TestWaitOnARestoredVMWithoutAFailureStillCompletes(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	require.NoError(t, fixture.ctrl.Start(context.Background()))

	previousAlive := restoredVMAlive
	restoredVMAlive = func(string) bool { return false }
	t.Cleanup(func() { restoredVMAlive = previousAlive })

	require.NoError(t, fixture.ctrl.Wait(context.Background()))
}

// TestStartFailsOnAStoreError leaves the directive in place for the retry is I3 at the
// start path: only an expired directive boots cold, because only an expired one is gone.
func TestStartFailsOnAStoreError(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.pending.takeErr = errors.New("the directive is corrupt")

	err := fixture.ctrl.Start(context.Background())
	require.Error(t, err, "an unusable directive is not a licence to boot a second VM")
	assert.ErrorContains(t, err, "the directive is corrupt")
	assert.ErrorContains(t, err, "cannot be used")

	assert.NotContains(t, fixture.calls.list(), "vmm.Start", "no cold boot beside the source node's VM")
	assert.NotContains(t, fixture.calls.list(), "translate")
	assert.Empty(t, fixture.restorer.infos)
}

// TestPrepareSkipsTheBootsWorkForARestoredTask is I4: image preparation and network
// preparation are what a boot needs and a restore does not. Running them anyway costs the
// target an image pull it never uses, fails the task on an image it cannot pull for a VM
// that will never boot, and leaves a second TAP device standing with no guest behind it.
func TestPrepareSkipsTheBootsWorkForARestoredTask(t *testing.T) {
	var imagePreps, networkPreps int

	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.ctrl.imagePrep = &MockImagePreparer{PrepareFunc: func(context.Context, *types.Task) error {
		imagePreps++
		return nil
	}}
	fixture.ctrl.networkMgr = &MockNetworkManager{PrepareNetworkFunc: func(context.Context, *types.Task) error {
		networkPreps++
		return nil
	}}
	fixture.ctrl.prepared = false

	require.NoError(t, fixture.ctrl.Prepare(context.Background()))

	assert.Zero(t, imagePreps, "a restored task pulls no image")
	assert.Zero(t, networkPreps, "a restored task creates no TAP of its own")
	assert.True(t, fixture.ctrl.prepared)
	require.NotNil(t, fixture.ctrl.internalTask, "the task is still prepared for the restore")
	assert.Equal(t, []string{artifactServiceID}, fixture.pending.peeks,
		"the peek is what answers the question, and it does not consume the directive")
	assert.Empty(t, fixture.pending.takes, "Prepare does not claim the directive")
}

// TestPrepareStillDoesTheBootsWorkWithoutADirective pins the no-behaviour-change rule at
// Prepare: a task whose service has nothing staged is prepared exactly as before.
func TestPrepareStillDoesTheBootsWorkWithoutADirective(t *testing.T) {
	var imagePreps, networkPreps int

	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true})
	fixture.ctrl.imagePrep = &MockImagePreparer{PrepareFunc: func(context.Context, *types.Task) error {
		imagePreps++
		return nil
	}}
	fixture.ctrl.networkMgr = &MockNetworkManager{PrepareNetworkFunc: func(context.Context, *types.Task) error {
		networkPreps++
		return nil
	}}
	fixture.ctrl.prepared = false

	require.NoError(t, fixture.ctrl.Prepare(context.Background()))

	assert.Equal(t, 1, imagePreps)
	assert.Equal(t, 1, networkPreps)
}

// TestPrepareFailsOnAnUnusableDirective pins I3 before the expensive half: a directive this
// build cannot use must fail the task while nothing has been pulled, not after.
func TestPrepareFailsOnAnUnusableDirective(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true})
	fixture.pending.peekErr = errors.New("the directive is corrupt")
	fixture.ctrl.imagePrep = &MockImagePreparer{}
	fixture.ctrl.prepared = false

	err := fixture.ctrl.Prepare(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "the directive is corrupt")
	assert.False(t, fixture.ctrl.prepared)
}

// TestStartUsesTheTaskNetworksBridge is I7: the boot path attaches a task's TAP to the
// bridge its attachment derives (a user-defined network has its own), and a restore that
// used Config.BridgeName would put a migrated guest's address on the default L2 segment.
func TestStartUsesTheTaskNetworksBridge(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	// The node's flat L2 is not this task's network, and the network names its own bridge.
	fixture.ctrl.config.Subnet = "192.168.127.0/24"
	fixture.ctrl.internalTask.Networks[0].Network = types.Network{
		ID: "net-backend",
		Spec: types.NetworkSpec{
			Name:         "backend",
			Subnet:       "10.10.0.0/16",
			Gateway:      "10.10.0.1",
			DriverConfig: &types.DriverConfig{Bridge: &types.BridgeConfig{Name: "br-backend"}},
		},
	}

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	tapName := scruntime.TapName(artifactSourceID, 0)
	assert.Contains(t, fixture.calls.list(), "tap.create "+tapName+" br-backend",
		"the TAP joins the bridge the boot path would have chosen for this attachment")
	assert.NotContains(t, fixture.calls.list(), "tap.create "+tapName+" "+artifactBridge)
}

// TestRestoreDepsSelectProductionEffects is M2: the null seams in MigrationRestoreConfig are
// what production leaves empty, so the mapping from nil to the real effect is the wiring
// every daemon relies on and nothing else asserts.
func TestRestoreDepsSelectProductionEffects(t *testing.T) {
	ctrl := &Controller{
		config:   &Config{BridgeName: "swarm-br0", Subnet: "192.168.127.0/24", LogDir: t.TempDir()},
		logger:   zerolog.Nop(),
		restored: &fakeRestoredStore{},
	}

	deps := ctrl.restoreDeps(&MigrationRestoreConfig{})

	assert.Equal(t, reflect.ValueOf(network.CreateTAPDeviceDown).Pointer(),
		reflect.ValueOf(deps.TapCreate).Pointer(), "nil TapCreate is the down creator")
	assert.Equal(t, reflect.ValueOf(network.BringTAPUp).Pointer(),
		reflect.ValueOf(deps.TapUp).Pointer())
	assert.Equal(t, reflect.ValueOf(network.RemoveTAPDevice).Pointer(),
		reflect.ValueOf(deps.TapDelete).Pointer())
	assert.Equal(t, reflect.ValueOf(migration.ProductionDialer).Pointer(),
		reflect.ValueOf(deps.Agent).Pointer(), "nil Agent is the node's real guest client")
	require.NotNil(t, deps.Registry, "the restore writes the record a teardown reads")
	require.NotNil(t, deps.Checkpoint, "the restore is cancellable by this controller's stop")

	assert.NoError(t, deps.Checkpoint(), "nothing has been stopped yet")
	ctrl.awaitRestoreStop()
	assert.ErrorIs(t, deps.Checkpoint(), errRestoreStopped)
}

// TestStartLogsDroppedAttachments is M7: a restored guest keeps only its first attachment,
// and a network that disappears from a task without a word is the kind of change an operator
// only notices when the service stops answering.
func TestStartLogsDroppedAttachments(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	fixture.ctrl.internalTask.Networks = append(fixture.ctrl.internalTask.Networks, types.NetworkAttachment{
		Network:   types.Network{Spec: types.NetworkSpec{Name: "backend", Driver: "bridge", Subnet: "10.10.0.0/16"}},
		Addresses: []string{"10.10.0.9/16"},
	})

	require.NoError(t, fixture.ctrl.Start(context.Background()))

	logged := fixture.logs.String()
	assert.Contains(t, logged, "dropped_networks")
	assert.Contains(t, logged, "backend", "the log names the attachment that is not attached")
	assert.True(t, strings.Contains(logged, "first network attachment"))
}

// TestRacingStartsRestoreOnce pins that the published restore state is not a lock that can
// be taken twice: a second Start while the first is loading must not load a second VM.
func TestRacingStartsRestoreOnce(t *testing.T) {
	fixture := newArtifactStartFixture(t, artifactStartOptions{withMigration: true, directivePending: true})
	_, err := fixture.ctrl.beginRestore(context.Background())
	require.NoError(t, err)
	defer fixture.ctrl.endRestore()

	_, secondErr := fixture.ctrl.beginRestore(context.Background())
	assert.ErrorIs(t, secondErr, errRestoreStopped,
		"one restore per controller: a second start is the task manager repeating itself")
}
