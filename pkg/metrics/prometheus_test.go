package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordVMStarted(t *testing.T) {
	before := testutil.ToFloat64(VMsRunning)
	totalBefore := testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "started"))

	RecordVMStarted("test-instance", "nginx")

	assert.Equal(t, before+1, testutil.ToFloat64(VMsRunning))
	assert.Equal(t, totalBefore+1, testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "started")))
}

func TestRecordVMStopped(t *testing.T) {
	// Ensure there's at least one running VM so Dec doesn't go negative
	VMsRunning.Set(2)
	before := testutil.ToFloat64(VMsRunning)
	totalBefore := testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "stopped"))

	RecordVMStopped("test-instance")

	assert.Equal(t, before-1, testutil.ToFloat64(VMsRunning))
	assert.Equal(t, totalBefore+1, testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "stopped")))
}

func TestRecordVMBootError(t *testing.T) {
	instance := "boot-error-instance"
	reason := "boot_failure"
	VMsRunning.Set(3)
	runningBefore := testutil.ToFloat64(VMsRunning)
	crashBefore := testutil.ToFloat64(VMCrashes.WithLabelValues(instance, "svc", reason))
	errBefore := testutil.ToFloat64(VMBootErrors.WithLabelValues(instance, reason))

	RecordVMBootError(instance, reason)

	// A VM that never booted was never running and is not a crash.
	assert.Equal(t, runningBefore, testutil.ToFloat64(VMsRunning),
		"boot error must not touch vms_running")
	assert.Equal(t, errBefore+1, testutil.ToFloat64(VMBootErrors.WithLabelValues(instance, reason)))
	assert.Equal(t, crashBefore, testutil.ToFloat64(VMCrashes.WithLabelValues(instance, "svc", reason)),
		"boot error must not touch the crash counter")
}

func TestRecordVMCrash(t *testing.T) {
	instance := "crash-instance"
	service := "web"
	reason := "unexpected_exit"
	VMsRunning.Set(2)
	runningBefore := testutil.ToFloat64(VMsRunning)
	totalBefore := testutil.ToFloat64(VMsTotal.WithLabelValues(instance, "crashed"))
	crashBefore := testutil.ToFloat64(VMCrashes.WithLabelValues(instance, service, reason))
	errBefore := testutil.ToFloat64(VMBootErrors.WithLabelValues(instance, reason))

	RecordVMCrash(instance, service, reason)

	// The VM was counted running, so a crash decrements the gauge exactly once.
	assert.Equal(t, runningBefore-1, testutil.ToFloat64(VMsRunning))
	assert.Equal(t, totalBefore+1, testutil.ToFloat64(VMsTotal.WithLabelValues(instance, "crashed")))
	assert.Equal(t, crashBefore+1, testutil.ToFloat64(VMCrashes.WithLabelValues(instance, service, reason)))
	assert.Equal(t, errBefore, testutil.ToFloat64(VMBootErrors.WithLabelValues(instance, reason)),
		"a post-start crash is not a boot failure")
}

func TestRecordVMBootDuration(t *testing.T) {
	// Record observations
	RecordVMBootDuration("test-instance", "nginx", 1.5)
	RecordVMBootDuration("test-instance", "nginx", 2.0)

	// Verify by checking the registered metric — it should exist with label values
	// testutil.CollectAndCount on the histogram vec should find the metrics
	count := testutil.CollectAndCount(VMBootDuration, "swarmcracker_vm_boot_duration_seconds")
	assert.Greater(t, count, 0, "should have at least one histogram sample")
}

func TestRecordVMBalloon(t *testing.T) {
	RecordVMBalloon("balloon-instance", "task-1", "web", 256)

	// The gauge carries exactly the configured target, not an approximation.
	assert.Equal(t, 256.0,
		testutil.ToFloat64(VMBalloonMB.WithLabelValues("balloon-instance", "task-1", "web")))

	// Each VM is tracked independently: a second VM's target does not disturb
	// the first's, and re-recording a VM overwrites (it is a gauge, not a sum).
	RecordVMBalloon("balloon-instance", "task-2", "web", 1024)
	assert.Equal(t, 256.0,
		testutil.ToFloat64(VMBalloonMB.WithLabelValues("balloon-instance", "task-1", "web")))
	assert.Equal(t, 1024.0,
		testutil.ToFloat64(VMBalloonMB.WithLabelValues("balloon-instance", "task-2", "web")))
}

// metricSampleCount counts the exposed samples of the named metric family whose
// labels match every entry in match. It gathers the default registry so a
// deleted series is distinguishable from one that merely reads back as zero.
func metricSampleCount(t *testing.T, name string, match map[string]string) int {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	count := 0
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := make(map[string]string, len(m.GetLabel()))
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			matched := true
			for k, v := range match {
				if labels[k] != v {
					matched = false
					break
				}
			}
			if matched {
				count++
			}
		}
	}
	return count
}

func TestRecordVMHealthOneHot(t *testing.T) {
	instance := "health-instance"
	service := "web"

	states := []string{
		scruntime.HealthStarting,
		scruntime.HealthHealthy,
		scruntime.HealthFailing,
		scruntime.HealthUnhealthy,
	}

	for _, current := range states {
		RecordVMHealth(instance, service, current)

		for _, s := range states {
			want := 0.0
			if s == current {
				want = 1.0
			}
			assert.Equal(t, want,
				testutil.ToFloat64(VMHealth.WithLabelValues(instance, service, s)),
				"state %q should be %v while the current state is %q", s, want, current)
		}
	}
}

func TestRecordVMHealthClearsOnUnknownState(t *testing.T) {
	instance := "health-clear-instance"
	service := "web"

	RecordVMHealth(instance, service, scruntime.HealthUnhealthy)
	assert.Equal(t, 4, metricSampleCount(t, "swarmcracker_vm_health",
		map[string]string{"instance": instance, "service": service}))

	// An empty state deletes every state for the service: no stale series.
	RecordVMHealth(instance, service, "")
	assert.Equal(t, 0, metricSampleCount(t, "swarmcracker_vm_health",
		map[string]string{"instance": instance, "service": service}))

	// An unknown state is treated the same way.
	RecordVMHealth(instance, service, scruntime.HealthHealthy)
	RecordVMHealth(instance, service, "not-a-state")
	assert.Equal(t, 0, metricSampleCount(t, "swarmcracker_vm_health",
		map[string]string{"instance": instance, "service": service}))
}

func TestRecordDiskUsage(t *testing.T) {
	instance := "disk-instance"

	RecordDiskUsage(instance, "rootfs", 100, 1000)

	assert.Equal(t, 100.0, testutil.ToFloat64(DiskUsageBytes.WithLabelValues(instance, "rootfs")))
	assert.Equal(t, 1000.0, testutil.ToFloat64(DiskTotalBytes.WithLabelValues(instance, "rootfs")))
}

func TestMetricsAreRegistered(t *testing.T) {
	// Verify all metrics are registered with the default prometheus registry
	metrics := []prometheus.Collector{
		VMsRunning,
		VMsTotal,
		VMBootDuration,
		VMBootErrors,
		VMCrashes,
		VMHealth,
		VXLANPeers,
		VXLANExpectedPeers,
		ManagerHealth,
		RaftHealth,
		DiskUsageBytes,
		DiskTotalBytes,
		VMBalloonMB,
		VMCPU,
		VMMemory,
		VMNetRx,
		VMNetTx,
	}
	for _, m := range metrics {
		// promauto registers with prometheus.DefaultRegisterer
		// We can unregister and re-register to verify registration
		unregistered := prometheus.DefaultRegisterer.Unregister(m)
		assert.True(t, unregistered, "metric should be registered")
		prometheus.DefaultRegisterer.MustRegister(m)
	}
}
