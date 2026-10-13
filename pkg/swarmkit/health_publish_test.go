package swarmkit

import (
	"context"
	"os"
	"path/filepath"
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
// must read "failing" (still RUNNING, no restart yet) before it reads
// "unhealthy". Docker's own status hides exactly this window.
func TestController_HealthMonitor_PublishesFailingBeforeUnhealthy(t *testing.T) {
	_, uds := startFakeVsockMuxer(t)
	hc := &api.HealthConfig{
		Test:     []string{"CMD-SHELL", "echo boom 1>&2; exit 3"},
		Interval: gogotypes.DurationProto(25 * time.Millisecond),
		Timeout:  gogotypes.DurationProto(2 * time.Second),
		Retries:  8,
	}
	vmm := newForceStopRecorder()
	ctrl := newMonitorController(t, uds, hc, vmm)
	ctrl.logger = zerolog.Nop()
	t.Cleanup(ctrl.stopHealthMonitor)
	ctrl.writeVMMetadata(ctrl.internalTask)

	ctrl.startHealthMonitor()

	failing := waitForHealthState(t, ctrl.config.SocketDir, "task-hc-monitor", scruntime.HealthFailing, 5*time.Second)
	assert.GreaterOrEqual(t, failing.FailingStreak, 1)
	assert.Less(t, failing.FailingStreak, 8, "the retries threshold must not be reached yet")
	assert.Contains(t, failing.Output, "boom")
	assert.Equal(t, 0, vmm.count(), "no restart may fire while the task is merely failing")

	vmm.wait(t)
	waitForMonitor(t, ctrl)

	assert.Equal(t, scruntime.HealthUnhealthy, readHealth(t, ctrl.config.SocketDir, "task-hc-monitor").State)
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
