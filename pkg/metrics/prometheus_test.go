package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
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

func TestRecordVMCrashed(t *testing.T) {
	VMsRunning.Set(2)
	before := testutil.ToFloat64(VMsRunning)
	totalBefore := testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "crashed"))
	errBefore := testutil.ToFloat64(VMBootErrors.WithLabelValues("test-instance", "oom_kill"))

	RecordVMCrashed("test-instance", "oom_kill")

	assert.Equal(t, before-1, testutil.ToFloat64(VMsRunning))
	assert.Equal(t, totalBefore+1, testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "crashed")))
	assert.Equal(t, errBefore+1, testutil.ToFloat64(VMBootErrors.WithLabelValues("test-instance", "oom_kill")))
}

func TestRecordVMCrashedEmptyReason(t *testing.T) {
	VMsRunning.Set(2)
	before := testutil.ToFloat64(VMsRunning)
	totalBefore := testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "crashed"))

	RecordVMCrashed("test-instance", "")

	assert.Equal(t, before-1, testutil.ToFloat64(VMsRunning))
	assert.Equal(t, totalBefore+1, testutil.ToFloat64(VMsTotal.WithLabelValues("test-instance", "crashed")))
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
func TestMetricsAreRegistered(t *testing.T) {
	// Verify all metrics are registered with the default prometheus registry
	metrics := []prometheus.Collector{
		VMsRunning,
		VMsTotal,
		VMBootDuration,
		VMBootErrors,
		VXLANPeers,
		VXLANExpectedPeers,
		ManagerHealth,
		RaftHealth,
		DiskUsageBytes,
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
