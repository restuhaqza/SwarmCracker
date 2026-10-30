package swarmkit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// fakeRestoredStore is the node's restored-instance store as the executor reads
// it. It records every call so a test can prove a stop reached the store — and
// that a failed stop did not.
type fakeRestoredStore struct {
	record    scruntime.RestoredInstance
	found     bool
	getErr    error
	deleteErr error

	gets    []string
	deletes []string
}

func (f *fakeRestoredStore) Get(taskID string) (scruntime.RestoredInstance, bool, error) {
	f.gets = append(f.gets, taskID)
	if f.getErr != nil {
		return scruntime.RestoredInstance{}, false, f.getErr
	}
	return f.record, f.found, nil
}

func (f *fakeRestoredStore) Delete(taskID string) error {
	f.deletes = append(f.deletes, taskID)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.found = false
	return nil
}

// fakeRestoredStopper is the manager that owns a restored VM's Firecracker
// process.
type fakeRestoredStopper struct {
	err     error
	stopped []string
}

func (f *fakeRestoredStopper) StopRestored(taskID string) error {
	f.stopped = append(f.stopped, taskID)
	return f.err
}

// restoredTestRecord is the record a target node writes for the task it restored.
func restoredTestRecord(taskID, sourceID string) scruntime.RestoredInstance {
	socketDir := "/var/run/firecracker"
	return scruntime.RestoredInstance{
		TaskID:     taskID,
		VMID:       sourceID,
		ArtifactID: "mig-0123456789abcdef",
		SocketPath: filepath.Join(socketDir, taskID+scruntime.VMSocketSuffix),
		TapName:    scruntime.TapName(sourceID, 0),
		VsockPath:  scruntime.VsockPath(socketDir, sourceID),
		DiskPath:   "/var/lib/swarmcracker/tasks/" + sourceID + ".ext4",
	}
}

// startFakeAPISocket publishes a Unix socket that answers Firecracker's liveness
// probe, which is what a restored VM's API socket looks like while its process
// runs. The directory is short because a Unix socket path is capped at ~104 bytes.
func startFakeAPISocket(t *testing.T) string {
	t.Helper()

	dir := shortHealthcheckDir(t)
	socketPath := filepath.Join(dir, "restored.sock")
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return socketPath
}

// teardownFixture is one controller wired to a counting VMM manager, so a test can
// tell the two stop paths apart: the manager the executor booted the VM with, and
// the store+stopper that own a VM restored from a migration artifact.
type teardownFixture struct {
	ctrl    *Controller
	store   *fakeRestoredStore
	stopper *fakeRestoredStopper

	vmmStops   int
	vmmRemoves int
}

// newTeardownFixture builds a task controller whose VM can be stopped through
// either path. A nil store or stopper leaves the controller without that
// dependency, which is the daemon's state before the setter is called.
func newTeardownFixture(t *testing.T, store *fakeRestoredStore, stopper *fakeRestoredStopper, started bool) *teardownFixture {
	t.Helper()

	fixture := &teardownFixture{store: store, stopper: stopper}
	vmm := &MockVMMManager{
		StopFunc: func(context.Context, *types.Task) error {
			fixture.vmmStops++
			return nil
		},
		RemoveFunc: func(context.Context, *types.Task) error {
			fixture.vmmRemoves++
			return nil
		},
	}

	fixture.ctrl = &Controller{
		task:       &api.Task{ID: "new-task"},
		config:     &Config{RootfsDir: t.TempDir(), LogDir: t.TempDir()},
		vmmMgr:     vmm,
		networkMgr: &mockNetworkManagerFull{},
		logger:     zerolog.Nop(),
		started:    started,
	}
	if store != nil {
		fixture.ctrl.restored = store
	}
	if stopper != nil {
		fixture.ctrl.restoredStopper = stopper
	}
	return fixture
}

// teardownCalls are the three ways SwarmKit ends a task. All three must honour a
// restored instance: a stop that reaches only one of them leaves the other two
// killing nothing while the real VM keeps running.
var teardownCalls = []struct {
	name string
	call func(*Controller) error
}{
	{name: "Shutdown", call: func(c *Controller) error { return c.Shutdown(context.Background()) }},
	{name: "Terminate", call: func(c *Controller) error { return c.Terminate(context.Background()) }},
	{name: "Remove", call: func(c *Controller) error { return c.Remove(context.Background()) }},
}

// TestTeardownStopsTheRestoredVM pins the whole stop of a restored task: the VM is
// stopped through the manager that owns the process, the record goes with it, and
// the VMM manager — which tracks no process for a task it never booted — is not
// asked to stop or remove anything.
func TestTeardownStopsTheRestoredVM(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
			fixture := newTeardownFixture(t, store, &fakeRestoredStopper{}, true)

			require.NoError(t, call.call(fixture.ctrl))

			require.Equal(t, []string{"new-task"}, fixture.stopper.stopped,
				"the VM the record names is the one that is stopped")
			require.Equal(t, []string{"new-task"}, store.deletes,
				"a confirmed stop forgets the record")
			require.Zero(t, fixture.vmmStops,
				"the VMM manager never booted a restored task's VM, so it is never asked to stop it")
			if call.name == "Remove" {
				// Remove still runs the VMM manager's resource cleanup: it is
				// idempotent for a task it never tracked, and it is what removes a
				// leftover socket or cgroup.
				require.Equal(t, 1, fixture.vmmRemoves)
			} else {
				require.Zero(t, fixture.vmmRemoves)
			}
		})
	}
}

// TestTeardownKeepsTheRecordWhenTheStopFails pins the retry contract: a stop that
// failed may have left the process alive, so its record is the only pointer to it
// and must survive. The failure is reported, not swallowed.
func TestTeardownKeepsTheRecordWhenTheStopFails(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
			stopper := &fakeRestoredStopper{err: errors.New("signal SIGTERM: operation not permitted")}
			fixture := newTeardownFixture(t, store, stopper, true)

			err := call.call(fixture.ctrl)
			require.Error(t, err)
			require.ErrorContains(t, err, `"new-task"`)
			require.ErrorContains(t, err, "record is kept",
				"the error must say why the record survived")
			require.Empty(t, store.deletes, "a failed stop keeps the record")
			require.Equal(t, []string{"new-task"}, stopper.stopped)
		})
	}
}

// TestTeardownForgetsARestoredVMThatIsAlreadyGone pins the sentinel: a manager
// that tracks no process for the task has nothing left to stop, so the end state
// the teardown asked for holds and the record — the only thing left — is deleted.
func TestTeardownForgetsARestoredVMThatIsAlreadyGone(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
			stopper := &fakeRestoredStopper{
				err: fmt.Errorf("%w: task %q has no restored VM on this node", snapshot.ErrNoRestoredVM, "new-task"),
			}
			fixture := newTeardownFixture(t, store, stopper, true)

			require.NoError(t, call.call(fixture.ctrl),
				"a VM that is already gone is not a teardown failure")
			require.Equal(t, []string{"new-task"}, store.deletes)
		})
	}
}

// TestTeardownWithoutARecordIsUnchanged pins today's behaviour for a task this
// node booted itself: the store has nothing, so the VMM manager stops the VM and
// no record is touched.
func TestTeardownWithoutARecordIsUnchanged(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			store := &fakeRestoredStore{}
			stopper := &fakeRestoredStopper{}
			fixture := newTeardownFixture(t, store, stopper, true)

			require.NoError(t, call.call(fixture.ctrl))

			require.Equal(t, []string{"new-task"}, store.gets, "the store is asked, and answers nothing")
			require.Empty(t, stopper.stopped)
			require.Empty(t, store.deletes)
			if call.name == "Remove" {
				require.Equal(t, 1, fixture.vmmRemoves)
			} else {
				require.Equal(t, 1, fixture.vmmStops)
			}
		})
	}
}

// TestTeardownWithoutARegistryIsUnchanged pins the executor without the restored
// instances wiring: it is the daemon before the setter is called, and it must
// behave exactly as it did before this feature existed.
func TestTeardownWithoutARegistryIsUnchanged(t *testing.T) {
	stopper := &fakeRestoredStopper{}
	fixture := newTeardownFixture(t, nil, stopper, true)

	require.NoError(t, fixture.ctrl.Shutdown(context.Background()))

	require.Equal(t, 1, fixture.vmmStops)
	require.Empty(t, stopper.stopped)
}

// TestTeardownStopsARestoredVMItNeverMarkedStarted pins the ordering the import
// has to survive: a restore can leave the VM running before the executor's own
// start ran, so c.started is not the evidence that a VM exists — the record is.
func TestTeardownStopsARestoredVMItNeverMarkedStarted(t *testing.T) {
	store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
	fixture := newTeardownFixture(t, store, &fakeRestoredStopper{}, false)

	require.NoError(t, fixture.ctrl.Shutdown(context.Background()))

	require.Equal(t, []string{"new-task"}, fixture.stopper.stopped)
	require.Equal(t, []string{"new-task"}, store.deletes)
	require.Zero(t, fixture.vmmStops)
}

// TestTeardownWithoutAStopperReportsIt pins the honest failure: the record says a
// VM is running and the executor has no way to reach its process. The record stays,
// and the error names the task.
func TestTeardownWithoutAStopperReportsIt(t *testing.T) {
	store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
	fixture := newTeardownFixture(t, store, nil, true)

	err := fixture.ctrl.Shutdown(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, `"new-task"`)
	require.Empty(t, store.deletes, "a record naming an unstoppable VM is kept")
}

// TestTeardownWithAnUnreadableRecordFallsBackToTheVMMManager pins the corrupt
// record: it names no VM the executor may act on, and deleting it would destroy
// the only evidence of what the node believes. Today's teardown runs — the VMM
// manager is keyed by this task's own ID, so it can never reach another task's VM.
func TestTeardownWithAnUnreadableRecordFallsBackToTheVMMManager(t *testing.T) {
	store := &fakeRestoredStore{getErr: errors.New("restored-instance record is corrupt")}
	stopper := &fakeRestoredStopper{}
	fixture := newTeardownFixture(t, store, stopper, true)

	require.NoError(t, fixture.ctrl.Shutdown(context.Background()))

	require.Equal(t, 1, fixture.vmmStops)
	require.Empty(t, stopper.stopped)
	require.Empty(t, store.deletes, "an unusable record is left in place for inspection")
}

// TestSetRestoredInstancesReachesControllers pins the one setter the daemon calls:
// a controller built afterwards stops through the store and the stopper it was
// given, not through the VMM manager.
func TestSetRestoredInstancesReachesControllers(t *testing.T) {
	exec := &Executor{
		config:      &Config{KernelPath: "/boot/vmlinux", BridgeIP: "192.168.127.1/24"},
		controllers: make(map[string]*Controller),
	}
	store := &fakeRestoredStore{}
	stopper := &fakeRestoredStopper{}

	exec.SetRestoredInstances(store, stopper)

	ctrl, err := exec.Controller(&api.Task{ID: "new-task"})
	require.NoError(t, err)
	controller, ok := ctrl.(*Controller)
	require.True(t, ok)
	require.Equal(t, store, controller.restored, "the controller reads the node's records")
	require.Equal(t, stopper, controller.restoredStopper, "the controller stops through the manager that owns the process")
}

// TestSetRestoredInstancesOnANilExecutor pins the nil-receiver guard every other
// Executor method has: a setter that panicked would take the daemon down at startup.
func TestSetRestoredInstancesOnANilExecutor(t *testing.T) {
	var exec *Executor
	require.NotPanics(t, func() { exec.SetRestoredInstances(&fakeRestoredStore{}, &fakeRestoredStopper{}) })
}

// TestProductionRestoredDependencies pins that the daemon can hand its real
// dependencies to the setter: the node's record store and pkg/snapshot's manager
// satisfy the two interfaces, so no adapter can drift from them.
func TestProductionRestoredDependencies(t *testing.T) {
	var (
		_ RestoredInstanceStore = (*scruntime.RestoredRegistry)(nil)
		_ RestoredVMStopper     = (*snapshot.Manager)(nil)
		_ RestoredInstanceStore = (*fakeRestoredStore)(nil)
		_ RestoredVMStopper     = (*fakeRestoredStopper)(nil)
	)
}

// TestTeardownKeepsTheRecordWhenThisDaemonLostTheProcess pins the one case where
// "no such restored VM" is not the end: the store's process table lives in memory,
// so a daemon restart makes every restored VM look untracked while it keeps
// serving. A recorded socket that still answers is the evidence the daemon lost,
// so the record — the only pointer to a live Firecracker — survives and the error
// says the VM must be stopped by hand.
func TestTeardownKeepsTheRecordWhenThisDaemonLostTheProcess(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			record := restoredTestRecord("new-task", "source-task")
			record.SocketPath = startFakeAPISocket(t)
			store := &fakeRestoredStore{record: record, found: true}
			stopper := &fakeRestoredStopper{err: snapshot.ErrNoRestoredVM}
			fixture := newTeardownFixture(t, store, stopper, true)

			err := call.call(fixture.ctrl)
			require.Error(t, err)
			require.ErrorIs(t, err, snapshot.ErrRestoredVMUntracked)
			require.ErrorContains(t, err, "restarted")
			require.ErrorContains(t, err, "must be stopped manually")
			require.Empty(t, store.deletes,
				"the record is the only pointer to a VM this daemon cannot signal")
		})
	}
}

// TestTeardownReportsARecordItCannotDelete pins the last step of a stop: the VM is
// gone and only the record cleanup failed, which the caller must hear — a silent
// success would leave a record that says a VM exists forever.
func TestTeardownReportsARecordItCannotDelete(t *testing.T) {
	for _, call := range teardownCalls {
		t.Run(call.name, func(t *testing.T) {
			store := &fakeRestoredStore{
				record:    restoredTestRecord("new-task", "source-task"),
				found:     true,
				deleteErr: errors.New("read-only file system"),
			}
			fixture := newTeardownFixture(t, store, &fakeRestoredStopper{}, true)

			err := call.call(fixture.ctrl)
			require.Error(t, err)
			require.ErrorContains(t, err, `"new-task"`)
			require.ErrorContains(t, err, "could not be deleted")
			require.Equal(t, []string{"new-task"}, store.deletes)
		})
	}
}

// TestTeardownTwiceIsIdempotent pins that a second teardown is a no-op: SwarmKit
// can deliver a shutdown after the task was already removed, and a path that
// re-reported a stopped VM (or re-signalled a PID) would fail a task that ended
// cleanly.
func TestTeardownTwiceIsIdempotent(t *testing.T) {
	store := &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true}
	stopper := &fakeRestoredStopper{}
	fixture := newTeardownFixture(t, store, stopper, true)

	require.NoError(t, fixture.ctrl.Shutdown(context.Background()))
	require.NoError(t, fixture.ctrl.Shutdown(context.Background()),
		"a teardown that already stopped the VM reports nothing to do")

	require.Equal(t, []string{"new-task"}, stopper.stopped, "the VM is signalled once")
	require.Equal(t, []string{"new-task"}, store.deletes, "the record is deleted once")
	require.Zero(t, fixture.vmmStops,
		"the VMM manager never booted a restored task's VM, so it is never asked to stop it")
}

// TestControllerGuestVsockPath pins the one path every in-executor probe dials: the
// muxer the task's restored record names when it has one, and the derived path
// otherwise. The probes cannot use the derivation on a restored task — the VM's
// state file names the source task's muxer, so <socketDir>/<newTaskID>.vsock belongs
// to nothing and every probe would fail against a perfectly healthy VM.
func TestControllerGuestVsockPath(t *testing.T) {
	socketDir := t.TempDir()

	cases := []struct {
		name    string
		store   *fakeRestoredStore
		want    string
		derived string
	}{
		{
			name:    "a task this executor booted keeps the derivation",
			derived: scruntime.VsockPath(socketDir, "new-task"),
		},
		{
			name:    "no record answers the derivation",
			store:   &fakeRestoredStore{},
			derived: scruntime.VsockPath(socketDir, "new-task"),
		},
		{
			name:    "a restored task answers its record's muxer",
			store:   &fakeRestoredStore{record: restoredTestRecord("new-task", "source-task"), found: true},
			want:    scruntime.VsockPath("/var/run/firecracker", "source-task"),
			derived: scruntime.VsockPath(socketDir, "new-task"),
		},
		{
			name:    "an unreadable record falls back to the derivation",
			store:   &fakeRestoredStore{getErr: errors.New("restored-instance record is corrupt")},
			derived: scruntime.VsockPath(socketDir, "new-task"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTeardownFixture(t, tc.store, nil, true)
			fixture.ctrl.config.SocketDir = socketDir
			fixture.ctrl.socketPath = filepath.Join(socketDir, "new-task"+scruntime.VMSocketSuffix)

			fixture.ctrl.resolveGuestVsockPath()

			want := tc.want
			if want == "" {
				want = tc.derived
			}
			require.Equal(t, want, fixture.ctrl.guestVsockPath())
			if tc.want != "" {
				require.NotEqual(t, tc.derived, fixture.ctrl.guestVsockPath(),
					"the derived muxer belongs to no process on a restored task")
			}
		})
	}
}
