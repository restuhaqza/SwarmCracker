package swarmkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/prometheus/client_golang/prometheus/testutil"
	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the pkg/swarmkit side of the boot/crash split and the health
// gauge teardown. Every assertion gathers the real series registered by
// pkg/metrics (via testutil/countSamples), so reverting a call site in
// executor.go — the whole point of the review finding — fails here. The
// equivalent assertions in pkg/metrics only prove the helpers themselves.

// newMetricsWiringController builds a controller whose real methods
// (startVM/Wait/Terminate) run against the supplied VMM mock and whose
// per-service labels resolve through convertTask exactly as in production.
func newMetricsWiringController(instance, service string, vmm *MockVMMManager) *Controller {
	return &Controller{
		task: &api.Task{
			ID:        "task-" + service,
			ServiceID: service,
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx"},
				},
			},
		},
		internalTask: &types.Task{ID: "task-" + service, ServiceID: service},
		config:       &Config{Hostname: instance},
		trans:        &MockTaskTranslator{},
		vmmMgr:       vmm,
		networkMgr:   &MockNetworkManager{},
		prepared:     true,
	}
}

// TestController_BootFailureMetricsWiring pins executor.go's boot path: a VM
// that fails to start is a boot error only. If :902 is reverted to
// RecordVMCrashed, the running-gauge assertion below fails; if it is pointed at
// RecordVMCrash, the crash-counter assertion fails too.
func TestController_BootFailureMetricsWiring(t *testing.T) {
	instance := "wiring-boot-host"
	reason := "boot_failure"

	runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
	errBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMBootErrors.WithLabelValues(instance, reason))

	ctrl := newMetricsWiringController(instance, "svc-boot", &MockVMMManager{
		StartFunc: func(ctx context.Context, task *types.Task, config interface{}) error {
			return errors.New("no kvm available")
		},
	})

	require.Error(t, ctrl.startVM(context.Background()))

	assert.Equal(t, errBefore+1, testutil.ToFloat64(
		swarmcrackermetrics.VMBootErrors.WithLabelValues(instance, reason)),
		"a failed boot increments vm_boot_errors_total")
	assert.Equal(t, runningBefore, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
		"a VM that never booted must not move vms_running (the negative-gauge bug)")
	assert.Equal(t, 0, countSamples(t, "swarmcracker_vm_crashes_total",
		map[string]string{"instance": instance}),
		"a boot failure must not be counted as a crash")
}

// crashCase drives one post-start failure path on the real controller.
type crashCase struct {
	reason  string
	wantErr bool
	setup   func(*Controller)
	drive   func(context.Context, *Controller) error
	vmm     func() *MockVMMManager
}

// TestController_PostStartFailureMetricsWiring pins executor.go's three
// self-exit paths. Each increments the crash counter, decrements the running
// gauge, and must leave vm_boot_errors_total alone. The old RecordVMCrashed
// folded the last two together, so its return would fail the boot-error
// assertion. A desired-state stop is *not* one of these paths: see
// TestController_DesiredStateStopMetricsWiring.
func TestController_PostStartFailureMetricsWiring(t *testing.T) {
	exitErr := errors.New("exit status 1")

	cases := map[string]crashCase{
		"unexpected_exit": {
			reason:  "unexpected_exit",
			wantErr: true,
			vmm: func() *MockVMMManager {
				return &MockVMMManager{
					WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
						return nil, errors.New("vm died")
					},
				}
			},
			drive: func(ctx context.Context, c *Controller) error { return c.Wait(ctx) },
		},
		"exit_error": {
			reason:  "exit_error",
			wantErr: true,
			vmm: func() *MockVMMManager {
				return &MockVMMManager{
					WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
						return &types.TaskStatus{State: types.TaskStateFailed, Err: exitErr}, nil
					},
				}
			},
			drive: func(ctx context.Context, c *Controller) error { return c.Wait(ctx) },
		},
		"unhealthy": {
			reason:  "unhealthy",
			wantErr: true,
			setup: func(c *Controller) {
				c.recordHealthFailure("probe: boom")
			},
			vmm: func() *MockVMMManager {
				return &MockVMMManager{
					WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
						return &types.TaskStatus{State: types.TaskStateFailed, Err: exitErr}, nil
					},
				}
			},
			drive: func(ctx context.Context, c *Controller) error { return c.Wait(ctx) },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			instance := "wiring-crash-" + name
			service := "svc-" + name

			ctrl := newMetricsWiringController(instance, service, tc.vmm())
			// Wait does not care about c.started, but setting it keeps the case
			// a running VM rather than a not-yet-started one.
			ctrl.started = true
			if tc.setup != nil {
				tc.setup(ctrl)
			}

			runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
			crashBefore := testutil.ToFloat64(
				swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, tc.reason))

			err := tc.drive(context.Background(), ctrl)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, crashBefore+1, testutil.ToFloat64(
				swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, tc.reason)),
				"%s must increment vm_crashes_total", tc.reason)
			assert.Equal(t, runningBefore-1, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
				"%s must decrement vms_running exactly once", tc.reason)
			assert.Equal(t, 0, countSamples(t, "swarmcracker_vm_boot_errors_total",
				map[string]string{"instance": instance}),
				"%s is not a boot failure: vm_boot_errors_total must be untouched", tc.reason)
		})
	}
}

// TestController_DesiredStateStopMetricsWiring pins defect 1 of issue #90: a
// stop the desired state asked for (SwarmKit calls Shutdown for
// `service rm`/scale-down/rolling update; Terminate is the unconditional
// agent-side stop) is a *stop*, not a crash. Before the classification, this
// path recorded a crash for `--force` rm and the crash-loop rule fired on
// ordinary operational churn. The VM is really started (so the running gauge is
// moved by RecordVMStarted) rather than assumed.
func TestController_DesiredStateStopMetricsWiring(t *testing.T) {
	stops := map[string]func(context.Context, *Controller) error{
		"Shutdown":  func(ctx context.Context, c *Controller) error { return c.Shutdown(ctx) },
		"Terminate": func(ctx context.Context, c *Controller) error { return c.Terminate(ctx) },
	}

	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			instance := "wiring-stop-" + name
			service := "svc-stop-" + name

			ctrl := newMetricsWiringController(instance, service, &MockVMMManager{})
			require.NoError(t, ctrl.startVM(context.Background()))

			runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
			stoppedBefore := testutil.ToFloat64(
				swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped"))
			crashedBefore := testutil.ToFloat64(
				swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "crashed"))

			require.NoError(t, stop(context.Background(), ctrl))

			assert.Equal(t, stoppedBefore+1, testutil.ToFloat64(
				swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped")),
				"%s must record exactly one stop", name)
			assert.Equal(t, crashedBefore, testutil.ToFloat64(
				swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "crashed")),
				"%s must not record a crash", name)
			assert.Equal(t, 0, countSamples(t, "swarmcracker_vm_crashes_total",
				map[string]string{"instance": instance}),
				"%s must leave the crash counter untouched", name)
			assert.Equal(t, runningBefore-1, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
				"%s must decrement vms_running exactly once", name)
		})
	}
}

// TestController_SelfExitRecordsCrashAndNoStop pins the other half of the
// classification: a VM that goes away on its own records a crash (its existing
// reason) and no stop, so vms_running still tracks a genuine loss.
func TestController_SelfExitRecordsCrashAndNoStop(t *testing.T) {
	instance := "wiring-self-exit"
	service := "svc-self-exit"

	ctrl := newMetricsWiringController(instance, service, &MockVMMManager{
		WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
			return nil, errors.New("vm died")
		},
	})
	require.NoError(t, ctrl.startVM(context.Background()))

	runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
	stoppedBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped"))
	crashBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit"))

	require.Error(t, ctrl.Wait(context.Background()))

	assert.Equal(t, crashBefore+1, testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit")),
		"a self-exit is a crash")
	assert.Equal(t, stoppedBefore, testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped")),
		"a self-exit is not a stop")
	assert.Equal(t, runningBefore-1, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
		"a self-exit decrements vms_running exactly once")
}

// TestController_CancelledWaitRecordsNothing reproduces the lab observation
// behind defect 1: SwarmKit cancels the in-flight operation to service a new
// desired state, so Wait returns the context error although the VM has not
// exited. That cancellation must not be counted as a crash; the stop that
// follows owns the single terminal record.
func TestController_CancelledWaitRecordsNothing(t *testing.T) {
	instance := "wiring-cancel-wait"
	service := "svc-cancel-wait"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vmm := &MockVMMManager{
		WaitFunc: func(callCtx context.Context, task *types.Task) (*types.TaskStatus, error) {
			// What the task manager does when a Shutdown/Remove desired state
			// arrives while Wait is in flight.
			cancel()
			return nil, callCtx.Err()
		},
	}
	ctrl := newMetricsWiringController(instance, service, vmm)
	require.NoError(t, ctrl.startVM(context.Background()))

	runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
	stoppedBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped"))
	crashBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit"))

	require.Error(t, ctrl.Wait(ctx))

	assert.Equal(t, crashBefore, testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit")),
		"an abandoned wait is not an exit and must not be a crash")
	assert.Equal(t, stoppedBefore, testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped")),
		"an abandoned wait must not record a terminal event")

	// The desired state that caused the cancellation then stops the VM, and that
	// is the one terminal event for it.
	require.NoError(t, ctrl.Shutdown(context.Background()))
	assert.Equal(t, stoppedBefore+1, testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped")))
	assert.Equal(t, runningBefore-1, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
		"the cancelled wait plus the stop must net exactly one decrement")
}

// TestController_HealthMonitorForceStopRecordsUnhealthyCrash drives the real
// health monitor (through the same fake vsock muxer the monitor tests use) and
// asserts the force-stop records the crash at the source, with reason
// `unhealthy`. The monitor kills the VM, so a wait observing that exit sees a
// raw kill error; without classifying at the source the reason would be lost
// (or recorded as `unexpected_exit`).
func TestController_HealthMonitorForceStopRecordsUnhealthyCrash(t *testing.T) {
	instance := "wiring-health-monitor"

	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, uds, hc, vmm)
	ctrl.config.Hostname = instance
	// The service label comes from the same capture the health gauge uses.
	ctrl.writeVMMetadata(ctrl.internalTask)
	service := serviceName(ctrl.internalTask)
	t.Cleanup(ctrl.stopHealthMonitor)

	crashBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unhealthy"))

	ctrl.startHealthMonitor()
	vmm.wait(t)
	waitForMonitor(t, ctrl)

	assert.Equal(t, crashBefore+1, testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unhealthy")),
		"the health force-stop must record the failure signal it acted on")
	assert.Equal(t, 0, countSamples(t, "swarmcracker_vm_crashes_total",
		map[string]string{"instance": instance, "reason": "unexpected_exit"}),
		"a health-induced kill must not be recorded as an unexplained exit")
}

// TestController_HealthForceStopWinsOverKillError pins the ordering inside Wait:
// the monitor kills the VM, so the wait observation is a kill error, yet the
// recorded reason must be the health failure, not `unexpected_exit`.
func TestController_HealthForceStopWinsOverKillError(t *testing.T) {
	instance := "wiring-health-kill"
	service := "svc-health-kill"

	ctrl := newMetricsWiringController(instance, service, &MockVMMManager{
		WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
			// What cmd.Wait reports for ForceStop's SIGKILL.
			return nil, errors.New("signal: killed")
		},
	})
	// What the monitor records before it force-stops.
	ctrl.recordHealthFailure("probe: boom")

	runningBefore := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
	unhealthyBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unhealthy"))
	unexpectedBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit"))

	require.Error(t, ctrl.Wait(context.Background()))

	assert.Equal(t, unhealthyBefore+1, testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unhealthy")),
		"the health failure is the reported reason")
	assert.Equal(t, unexpectedBefore, testutil.ToFloat64(
		swarmcrackermetrics.VMCrashes.WithLabelValues(instance, service, "unexpected_exit")),
		"the kill error must not shadow the health failure")
	assert.Equal(t, runningBefore-1, testutil.ToFloat64(swarmcrackermetrics.VMsRunning))
}

// TestController_ConcurrentWaitAndTerminateRecordsOnce drives the two real
// racing paths — a Wait blocked on the VM and a Terminate — many times on one
// controller per round and asserts (a) the terminal event is recorded exactly
// once per VM and (b) vms_running is never even observed negative. Before the
// classification both paths decremented the gauge for the same VM, which is the
// negative gauge the lab reported.
func TestController_ConcurrentWaitAndTerminateRecordsOnce(t *testing.T) {
	instance := "wiring-concurrent"
	service := "svc-concurrent"

	// Self-contained: one VM is started per round and must end exactly once, so
	// the shared gauge has to come back to 0. Anything below 0 is a double
	// record.
	swarmcrackermetrics.VMsRunning.Set(0)
	t.Cleanup(func() { swarmcrackermetrics.VMsRunning.Set(0) })

	const rounds = 50

	stoppedBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped"))
	crashedBefore := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "crashed"))

	for i := 0; i < rounds; i++ {
		ctrl := newMetricsWiringController(instance, service, &MockVMMManager{
			WaitFunc: func(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
				return nil, errors.New("vm died")
			},
		})
		require.NoError(t, ctrl.startVM(context.Background()))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = ctrl.Wait(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = ctrl.Terminate(context.Background())
		}()
		wg.Wait()

		running := testutil.ToFloat64(swarmcrackermetrics.VMsRunning)
		require.GreaterOrEqual(t, running, 0.0,
			"vms_running must never be negative for a single VM (round %d)", i)
	}

	stopped := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "stopped")) - stoppedBefore
	crashed := testutil.ToFloat64(
		swarmcrackermetrics.VMsTotal.WithLabelValues(instance, "crashed")) - crashedBefore
	assert.Equal(t, float64(rounds), stopped+crashed,
		"each VM must record exactly one terminal event, whichever path wins")
	assert.Equal(t, 0.0, testutil.ToFloat64(swarmcrackermetrics.VMsRunning),
		"%d VMs started and %d terminal events: the gauge must return to its origin",
		rounds, int(stopped+crashed))
}

// TestController_TeardownClearsHealthGauge pins the fix for the never-cleared
// health gauge: every teardown path must remove the four health series for the
// task, so an unhealthy VM that was force-stopped (or a scaled-down service)
// cannot leave swarmcracker_vm_health{state="unhealthy"}==1 set forever. The
// state is seeded through the real publishHealth, and the teardown goes through
// the real controller method. Removing the RecordVMHealth(..., "") call from
// stopHealthMonitor makes every subtest fail.
func TestController_TeardownClearsHealthGauge(t *testing.T) {
	teardowns := map[string]func(context.Context, *Controller) error{
		"Shutdown":  func(ctx context.Context, c *Controller) error { return c.Shutdown(ctx) },
		"Terminate": func(ctx context.Context, c *Controller) error { return c.Terminate(ctx) },
		"Remove":    func(ctx context.Context, c *Controller) error { return c.Remove(ctx) },
		"Close":     func(ctx context.Context, c *Controller) error { return c.Close() },
	}

	for name, teardown := range teardowns {
		t.Run(name, func(t *testing.T) {
			instance := "health-clear-" + name
			service := "svc-" + name

			ctrl := newMetricsWiringController(instance, service, &MockVMMManager{})
			ctrl.config.SocketDir = t.TempDir()
			// Tear the controller down for real rather than via an early return.
			ctrl.started = true

			// A health write goes through the same choke point the monitor uses.
			ctrl.writeVMMetadata(ctrl.internalTask)
			ctrl.publishHealth(scruntime.HealthUnhealthy, 3, "probe", "boom")

			require.Equal(t, 4, countSamples(t, "swarmcracker_vm_health",
				map[string]string{"instance": instance, "service": service}),
				"publishHealth must expose the one-hot state before teardown")
			require.Equal(t, 1.0, testutil.ToFloat64(
				swarmcrackermetrics.VMHealth.WithLabelValues(instance, service, scruntime.HealthUnhealthy)))

			require.NoError(t, teardown(context.Background(), ctrl))

			assert.Equal(t, 0, countSamples(t, "swarmcracker_vm_health",
				map[string]string{"instance": instance, "service": service}),
				"%s must clear every health series for the torn-down task", name)
		})
	}
}
