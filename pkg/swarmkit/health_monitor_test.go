package swarmkit

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forceStopRecorder wraps MockVMMManager and records ForceStop calls so a test
// can prove the monitor stopped the VM exactly once.
type forceStopRecorder struct {
	*MockVMMManager
	mu    sync.Mutex
	calls int
	ch    chan struct{}
}

func newForceStopRecorder() *forceStopRecorder {
	r := &forceStopRecorder{ch: make(chan struct{}, 1)}
	r.MockVMMManager = &MockVMMManager{
		ForceStopFunc: func(ctx context.Context, task *types.Task) error {
			r.mu.Lock()
			r.calls++
			r.mu.Unlock()
			select {
			case r.ch <- struct{}{}:
			default:
			}
			return nil
		},
	}
	return r
}

func (r *forceStopRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// wait blocks until ForceStop has been called at least once.
func (r *forceStopRecorder) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("ForceStop was not called")
	}
}

// newMonitorController builds a Controller whose monitor dials vsockUDS (so
// vsockUDSPath(socketPath) resolves back to it) and whose VM operations go to
// vmm.
func newMonitorController(t *testing.T, vsockUDS string, hc *api.HealthConfig, vmm VMMManagerInterface) *Controller {
	t.Helper()
	return &Controller{
		task: &api.Task{
			ID: "task-hc-monitor",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx", Healthcheck: hc},
				},
			},
		},
		internalTask: &types.Task{
			ID:   "task-hc-monitor",
			Spec: types.TaskSpec{Runtime: &types.Container{Image: "nginx"}},
		},
		config:     &Config{SocketDir: t.TempDir(), Hostname: "test-host"},
		trans:      &MockTaskTranslator{},
		vmmMgr:     vmm,
		networkMgr: &MockNetworkManager{},
		socketPath: strings.TrimSuffix(vsockUDS, ".vsock") + ".sock",
		prepared:   true,
	}
}

// waitForMonitor asserts the monitor goroutine has exited (the WaitGroup
// drains), so a stop path really joined it.
func waitForMonitor(t *testing.T, ctrl *Controller) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		ctrl.healthWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor goroutine did not exit")
	}
}

// TestController_HealthMonitor_StopsUnhealthyVM asserts the core behaviour:
// after Retries consecutive failing probes the monitor calls ForceStop and the
// goroutine exits.
func TestController_HealthMonitor_StopsUnhealthyVM(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, uds, hc, vmm)
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()
	vmm.wait(t)

	waitForMonitor(t, ctrl)
	assert.Equal(t, 1, vmm.count(), "ForceStop must be called exactly once")
	assert.Contains(t, ctrl.healthcheckFailure(), "boom")
}

// TestController_HealthMonitor_StopsARestoredUnhealthyVM pins the other stop path
// SwarmKit drives: a health verdict. A restored task's VM is not the VMM manager's,
// so the monitor must stop it through the record — a ForceStop would kill nothing
// and leave the unhealthy VM serving.
func TestController_HealthMonitor_StopsARestoredUnhealthyVM(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	vmm := newForceStopRecorder()
	store := &fakeRestoredStore{record: restoredTestRecord("task-hc-monitor", "source-task"), found: true}
	stopper := &fakeRestoredStopper{}

	ctrl := newMonitorController(t, uds, hc, vmm)
	ctrl.restored = store
	ctrl.restoredStopper = stopper
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()
	waitForMonitor(t, ctrl)

	assert.Equal(t, []string{"task-hc-monitor"}, stopper.stopped,
		"the health verdict stops the VM the record names")
	assert.Equal(t, []string{"task-hc-monitor"}, store.deletes)
	assert.Equal(t, 0, vmm.count(), "the VMM manager tracks no process for a restored task")
}

// TestController_HealthMonitor_HealthyKeepsPolling asserts a healthy task is
// never stopped, no matter how many probes pass.
func TestController_HealthMonitor_HealthyKeepsPolling(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "true"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, uds, hc, vmm)
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()
	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, 0, vmm.count(), "a healthy task must never be stopped")
	assert.Empty(t, ctrl.healthcheckFailure())
}

// TestController_HealthMonitor_TransportErrorCountsAsUnhealthy asserts a dial
// failure is counted as an unhealthy probe (not a fatal error) and the monitor
// keeps polling until retries are exhausted.
func TestController_HealthMonitor_TransportErrorCountsAsUnhealthy(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "true"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(100 * time.Millisecond),
		Retries:  3,
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, missing, hc, vmm)
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()
	vmm.wait(t)

	waitForMonitor(t, ctrl)
	assert.Equal(t, 1, vmm.count())
}

// TestController_HealthMonitor_NoHealthcheckStartsNothing asserts a task with
// no healthcheck gets no goroutine at all.
func TestController_HealthMonitor_NoHealthcheckStartsNothing(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	ctrl := newMonitorController(t, missing, nil, newForceStopRecorder())

	ctrl.startHealthMonitor()

	assert.Nil(t, ctrl.healthCancel, "no healthcheck must not start a monitor")
	assert.Empty(t, ctrl.healthcheckFailure())
}

// TestController_HealthMonitor_DisabledStartsNothing asserts a ["NONE"] test
// starts no monitor either.
func TestController_HealthMonitor_DisabledStartsNothing(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	ctrl := newMonitorController(t, missing, &api.HealthConfig{Test: []string{"NONE"}}, newForceStopRecorder())

	ctrl.startHealthMonitor()

	assert.Nil(t, ctrl.healthCancel)
}

// TestController_Wait_ReportsHealthFailure asserts Wait's error carries the
// recorded health failure (command + output) so SwarmKit sets task.Status.Err
// and the CLI can surface it.
func TestController_Wait_ReportsHealthFailure(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	exitErr := errors.New("exit status 1")
	vmm := newForceStopRecorder()
	vmm.WaitFunc = func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
		return &types.TaskStatus{State: types.TaskStateFailed, Err: exitErr, Message: exitErr.Error()}, nil
	}
	ctrl := newMonitorController(t, uds, hc, vmm)
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()
	vmm.wait(t)

	err := ctrl.Wait(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "healthcheck failed")
	assert.Contains(t, err.Error(), "boom")
	assert.True(t, errors.Is(err, exitErr), "the exit error must stay in the chain: %v", err)
}

// TestController_Wait_NoHealthFailureUnchanged asserts Wait is byte-for-byte
// unchanged for a task with no recorded health failure.
func TestController_Wait_NoHealthFailureUnchanged(t *testing.T) {
	exitErr := errors.New("exit status 1")
	ctrl := &Controller{
		task:   &api.Task{ID: "task-wait-plain"},
		config: &Config{},
		trans:  &MockTaskTranslator{},
		vmmMgr: &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return &types.TaskStatus{State: types.TaskStateFailed, Err: exitErr}, nil
			},
		},
	}

	err := ctrl.Wait(context.Background())
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "healthcheck failed"))
	assert.True(t, errors.Is(err, exitErr))
}

// TestController_HealthMonitor_StopPaths asserts every teardown path stops and
// joins the monitor, and that calling it twice is safe.
func TestController_HealthMonitor_StopPaths(t *testing.T) {
	stopMethods := map[string]func(context.Context, *Controller) error{
		"Shutdown":  func(ctx context.Context, c *Controller) error { return c.Shutdown(ctx) },
		"Terminate": func(ctx context.Context, c *Controller) error { return c.Terminate(ctx) },
		"Remove":    func(ctx context.Context, c *Controller) error { return c.Remove(ctx) },
		"Close":     func(ctx context.Context, c *Controller) error { return c.Close() },
	}

	for name, stop := range stopMethods {
		t.Run(name, func(t *testing.T) {
			_, uds := startFakeVsockMuxer(t)
			hc := &api.HealthConfig{
				Test:     []string{"CMD-SHELL", "true"},
				Interval: gogotypes.DurationProto(10 * time.Millisecond),
				Timeout:  gogotypes.DurationProto(2 * time.Second),
				Retries:  3,
			}
			ctrl := newMonitorController(t, uds, hc, &MockVMMManager{})
			ctrl.started = true // let Shutdown/Terminate take their real path

			ctrl.startHealthMonitor()
			require.NotNil(t, ctrl.healthCancel)

			require.NoError(t, stop(context.Background(), ctrl))
			assert.Nil(t, ctrl.healthCancel, "the stop path must clear the monitor")
			waitForMonitor(t, ctrl)

			// Idempotent: a repeated stop is safe and does nothing.
			require.NoError(t, stop(context.Background(), ctrl))
			assert.Nil(t, ctrl.healthCancel)
		})
	}
}

// TestController_HealthMonitor_StopWithoutMonitor asserts stopHealthMonitor is
// safe when no monitor ever started.
func TestController_HealthMonitor_StopWithoutMonitor(t *testing.T) {
	ctrl := newMonitorController(t, filepath.Join(shortHealthcheckDir(t), "missing.vsock"), nil, &MockVMMManager{})
	require.NotPanics(t, ctrl.stopHealthMonitor)
	require.NotPanics(t, ctrl.stopHealthMonitor)
}
