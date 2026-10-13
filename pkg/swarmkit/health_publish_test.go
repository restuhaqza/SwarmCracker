package swarmkit

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readHealth reads the health observation from a task's metadata sidecar.
func readHealth(t *testing.T, dir, taskID string) *scruntime.VMHealth {
	t.Helper()
	md, ok := scruntime.ReadVMMetadata(dir, taskID)
	require.Truef(t, ok, "metadata sidecar missing for %q", taskID)
	require.NotNilf(t, md.Health, "metadata sidecar for %q has no health observation", taskID)
	return md.Health
}

// waitForHealthState polls the sidecar until it reports want, so the test never
// depends on sleeping for exactly the right interval. The probe windows below
// are chosen to be tens of times wider than the poll interval.
func waitForHealthState(t *testing.T, dir, taskID, want string, timeout time.Duration) *scruntime.VMHealth {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *scruntime.VMHealth
	for time.Now().Before(deadline) {
		if md, ok := scruntime.ReadVMMetadata(dir, taskID); ok && md.Health != nil {
			last = md.Health
			if md.Health.State == want {
				return md.Health
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("health state never became %q within %s (last observed: %+v)", want, timeout, last)
	return nil
}

// TestController_PublishHealth_PreservesNetworkMetadata asserts a health update
// republishes the sidecar without dropping the network details written at start:
// the CLI reads the guest IP and the health from the same file.
func TestController_PublishHealth_PreservesNetworkMetadata(t *testing.T) {
	dir := t.TempDir()
	ctrl := &Controller{config: &Config{SocketDir: dir}, logger: zerolog.Nop()}

	ctrl.writeVMMetadata(&types.Task{
		ID: "task-h",
		Networks: []types.NetworkAttachment{
			{Network: types.Network{ID: "net-1"}, Addresses: []string{"192.168.127.42/24"}},
		},
	})
	ctrl.publishHealth(scruntime.HealthFailing, 2, "test -f /tmp/ready", "boom")

	md, ok := scruntime.ReadVMMetadata(dir, "task-h")
	require.True(t, ok)
	require.NotNil(t, md.Health)
	assert.Equal(t, scruntime.HealthFailing, md.Health.State)
	assert.Equal(t, 2, md.Health.FailingStreak)
	assert.Equal(t, "boom", md.Health.Output)
	assert.Equal(t, "test -f /tmp/ready", md.Health.Command)
	assert.False(t, md.Health.UpdatedAt.IsZero(), "the observation must be timestamped")

	assert.Equal(t, "net-1", md.NetworkID, "network details must survive a health write")
	assert.Equal(t, []string{"192.168.127.42/24"}, md.IPAddresses)
}

// TestController_PublishHealth_WithoutMetadataIsNoop asserts the publisher never
// creates a sidecar out of nothing (no network snapshot yet) and never panics
// without a socket dir. A task with no healthcheck must produce no health field.
func TestController_PublishHealth_WithoutMetadataIsNoop(t *testing.T) {
	dir := t.TempDir()
	ctrl := &Controller{config: &Config{SocketDir: dir}, logger: zerolog.Nop()}

	assert.NotPanics(t, func() { ctrl.publishHealth(scruntime.HealthHealthy, 0, "probe", "") })
	if _, ok := scruntime.ReadVMMetadata(dir, "task-h"); ok {
		t.Error("publishHealth created a sidecar without a network snapshot")
	}

	bare := &Controller{config: &Config{}, logger: zerolog.Nop()}
	assert.NotPanics(t, func() { bare.publishHealth(scruntime.HealthStarting, 0, "probe", "") })
}

// TestController_PublishHealth_AfterStopIsNoop covers the teardown race: a
// teardown deletes the sidecar, and the health gate publishes without c.mu, so a
// probe racing a stop must not recreate the file. A (re)started VM enables
// publishing again.
func TestController_PublishHealth_AfterStopIsNoop(t *testing.T) {
	dir := t.TempDir()
	ctrl := &Controller{config: &Config{SocketDir: dir}, logger: zerolog.Nop()}
	ctrl.writeVMMetadata(&types.Task{ID: "task-h"})
	ctrl.publishHealth(scruntime.HealthHealthy, 0, "probe", "")
	require.Equal(t, scruntime.HealthHealthy, readHealth(t, dir, "task-h").State)

	// Teardown: stop publishing, then delete the sidecar (as the VM stop does).
	ctrl.stopHealthMonitor()
	require.NoError(t, scruntime.RemoveVMMetadata(dir, "task-h"))

	ctrl.publishHealth(scruntime.HealthStarting, 0, "probe", "")
	if _, ok := scruntime.ReadVMMetadata(dir, "task-h"); ok {
		t.Error("a publish after teardown recreated the sidecar")
	}

	// A restarted VM re-enables publishing.
	ctrl.writeVMMetadata(&types.Task{ID: "task-h"})
	ctrl.publishHealth(scruntime.HealthHealthy, 0, "probe", "")
	assert.Equal(t, scruntime.HealthHealthy, readHealth(t, dir, "task-h").State)
}

// TestController_Start_PublishesStartingThenHealthy asserts the gate publishes
// its live verdict: a task held in the start period reads "starting", and the
// moment a probe passes it reads "healthy".
func TestController_Start_PublishesStartingThenHealthy(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	marker := filepath.Join(t.TempDir(), "ready")
	hc := &api.HealthConfig{
		Test:        []string{"CMD-SHELL", "test -f " + marker},
		Interval:    gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:     gogotypes.DurationProto(2 * time.Second),
		Retries:     3,
		StartPeriod: gogotypes.DurationProto(time.Minute),
	}
	ctrl := newStartController(t, uds, hc)

	errCh := make(chan error, 1)
	go func() { errCh <- ctrl.Start(context.Background()) }()

	// The task is not RUNNING yet and its check is failing, but it is inside the
	// start period, so start-period failures do not count toward retries.
	starting := waitForHealthState(t, ctrl.config.SocketDir, "task-hc-start", scruntime.HealthStarting, 5*time.Second)
	assert.Equal(t, 0, starting.FailingStreak, "start-period failures must not count")
	assert.Contains(t, starting.Command, "test -f")

	require.NoError(t, os.WriteFile(marker, []byte("x"), 0o644))

	select {
	case err := <-errCh:
		require.NoError(t, err, "Start must return once the check passes")
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after the check passed")
	}

	health := readHealth(t, ctrl.config.SocketDir, "task-hc-start")
	assert.Equal(t, scruntime.HealthHealthy, health.State)
	assert.Equal(t, 0, health.FailingStreak)
}

// TestController_Start_PublishesUnhealthyFromGate asserts a task that never
// becomes healthy publishes "unhealthy" with the exhausted streak and the last
// probe output, so the node-local view explains why the task failed.
func TestController_Start_PublishesUnhealthyFromGate(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	ctrl := newStartController(t, uds, hc)

	require.Error(t, ctrl.Start(context.Background()))

	health := readHealth(t, ctrl.config.SocketDir, "task-hc-start")
	assert.Equal(t, scruntime.HealthUnhealthy, health.State)
	assert.Equal(t, 3, health.FailingStreak)
	assert.Contains(t, health.Output, "boom")
}

// TestController_HealthMonitor_PublishesFailingBeforeUnhealthy is the whole
// point of the feature: a task that was healthy and starts failing its checks
// must read "failing" (still RUNNING, no restart yet) before it is stopped.
// Docker's own status hides exactly this window.
//
// The unhealthy verdict itself is only on disk for the instant between the
// publish and ForceStop — teardown removes the sidecar — so it is asserted from
// inside the stop hook, which is what the daemon sees at that moment.
func TestController_HealthMonitor_PublishesFailingBeforeUnhealthy(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(50 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  10,
	}
	ctrl := newMonitorController(t, uds, hc, nil)
	ctrl.logger = zerolog.Nop()
	ctrl.writeVMMetadata(ctrl.internalTask)

	var (
		mu           sync.Mutex
		forceStops   int
		stoppedState string
	)
	ctrl.vmmMgr = &MockVMMManager{
		ForceStopFunc: func(ctx context.Context, task *types.Task) error {
			mu.Lock()
			defer mu.Unlock()
			forceStops++
			if md, ok := scruntime.ReadVMMetadata(ctrl.config.SocketDir, ctrl.internalTask.ID); ok && md.Health != nil {
				stoppedState = md.Health.State
			}
			return nil
		},
	}
	t.Cleanup(ctrl.stopHealthMonitor)

	ctrl.startHealthMonitor()

	failing := waitForHealthState(t, ctrl.config.SocketDir, "task-hc-monitor", scruntime.HealthFailing, 5*time.Second)
	assert.GreaterOrEqual(t, failing.FailingStreak, 1)
	assert.Less(t, failing.FailingStreak, 10, "the retries threshold must not be reached yet")
	assert.Contains(t, failing.Output, "boom")

	mu.Lock()
	assert.Equal(t, 0, forceStops, "no restart may fire while the task is merely failing")
	mu.Unlock()

	waitForMonitor(t, ctrl)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, forceStops, "the VM must be stopped exactly once")
	assert.Equal(t, scruntime.HealthUnhealthy, stoppedState,
		"the unhealthy verdict must be on disk when the VM is stopped")
}

// TestController_HealthMonitor_PublishesFailingInsideStartPeriod pins the
// interaction between the monitor and a long start period: the last probe
// failed, so the state is "failing", but a start-period failure does not count
// toward the retries (Docker semantics), so the streak stays 0 and no restart
// fires. Reporting "healthy" here would be a lie.
func TestController_HealthMonitor_PublishesFailingInsideStartPeriod(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:        []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval:    gogotypes.DurationProto(20 * time.Millisecond),
		Timeout:     gogotypes.DurationProto(2 * time.Second),
		Retries:     3,
		StartPeriod: gogotypes.DurationProto(time.Hour),
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, uds, hc, vmm)
	ctrl.logger = zerolog.Nop()
	t.Cleanup(ctrl.stopHealthMonitor)
	ctrl.writeVMMetadata(ctrl.internalTask)

	ctrl.startHealthMonitor()

	health := waitForHealthState(t, ctrl.config.SocketDir, "task-hc-monitor", scruntime.HealthFailing, 5*time.Second)
	assert.Equal(t, 0, health.FailingStreak, "start-period failures must not count toward the retries")
	assert.Contains(t, health.Output, "boom")
	assert.Equal(t, 0, vmm.count(), "a start-period failure must not restart the task")
}

// TestController_HealthMonitor_DisabledPublishesNothing asserts a ["NONE"] test
// starts no monitor and publishes no health, so the sidecar keeps the shape it
// had before this feature.
func TestController_HealthMonitor_DisabledPublishesNothing(t *testing.T) {
	missing := filepath.Join(shortHealthcheckDir(t), "missing.vsock")
	ctrl := newMonitorController(t, missing, &api.HealthConfig{Test: []string{"NONE"}}, newForceStopRecorder())
	ctrl.logger = zerolog.Nop()
	ctrl.writeVMMetadata(ctrl.internalTask)

	ctrl.startHealthMonitor()

	md, ok := scruntime.ReadVMMetadata(ctrl.config.SocketDir, "task-hc-monitor")
	require.True(t, ok, "the network sidecar must still exist")
	assert.Nil(t, md.Health, "a disabled healthcheck must publish no health")
}

// TestController_HealthMonitor_PublishesHealthy asserts a passing probe keeps
// the published state healthy (and resets the streak).
func TestController_HealthMonitor_PublishesHealthy(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "true"},
		Interval: gogotypes.DurationProto(10 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  3,
	}
	ctrl := newMonitorController(t, uds, hc, &MockVMMManager{})
	ctrl.logger = zerolog.Nop()
	t.Cleanup(ctrl.stopHealthMonitor)
	ctrl.writeVMMetadata(ctrl.internalTask)

	ctrl.startHealthMonitor()

	health := waitForHealthState(t, ctrl.config.SocketDir, "task-hc-monitor", scruntime.HealthHealthy, 5*time.Second)
	assert.Equal(t, 0, health.FailingStreak)
}
