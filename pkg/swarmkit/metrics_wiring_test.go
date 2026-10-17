package swarmkit

import (
	"context"
	"errors"
	"testing"

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

// TestController_PostStartFailureMetricsWiring pins executor.go's four
// post-start paths. Each increments the crash counter, decrements the running
// gauge, and must leave vm_boot_errors_total alone. The old RecordVMCrashed
// folded the last two together, so its return would fail the boot-error
// assertion.
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
		"force_terminate": {
			reason:  "force_terminate",
			wantErr: false,
			vmm:     func() *MockVMMManager { return &MockVMMManager{} },
			drive:   func(ctx context.Context, c *Controller) error { return c.Terminate(ctx) },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			instance := "wiring-crash-" + name
			service := "svc-" + name

			ctrl := newMetricsWiringController(instance, service, tc.vmm())
			// Terminate takes its real path only for a started VM; Wait does not
			// care, so setting it for every case is harmless.
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
