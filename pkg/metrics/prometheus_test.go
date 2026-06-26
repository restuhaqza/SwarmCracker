package metrics

import (
	"strings"
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

func TestSetVXLANPeerCount(t *testing.T) {
	SetVXLANPeerCount("test-instance", 3, 5)

	assert.Equal(t, 3.0, testutil.ToFloat64(VXLANPeers))
	assert.Equal(t, 5.0, testutil.ToFloat64(VXLANExpectedPeers))
}

func TestSetManagerHealth(t *testing.T) {
	SetManagerHealth("test-instance", true)
	assert.Equal(t, 1.0, testutil.ToFloat64(ManagerHealth))

	SetManagerHealth("test-instance", false)
	assert.Equal(t, 0.0, testutil.ToFloat64(ManagerHealth))
}

func TestSetRaftHealth(t *testing.T) {
	SetRaftHealth("test-instance", true)
	assert.Equal(t, 1.0, testutil.ToFloat64(RaftHealth))

	SetRaftHealth("test-instance", false)
	assert.Equal(t, 0.0, testutil.ToFloat64(RaftHealth))
}

func TestSetVMMetrics(t *testing.T) {
	SetVMMetrics("worker1", "task-abc", "nginx", 1.5, 134217728, 1024, 512)

	assert.Equal(t, 1.5, testutil.ToFloat64(VMCPU.WithLabelValues("worker1", "task-abc", "nginx")))
	assert.Equal(t, 134217728.0, testutil.ToFloat64(VMMemory.WithLabelValues("worker1", "task-abc", "nginx")))
	assert.Equal(t, 1024.0, testutil.ToFloat64(VMNetRx.WithLabelValues("worker1", "task-abc", "nginx")))
	assert.Equal(t, 512.0, testutil.ToFloat64(VMNetTx.WithLabelValues("worker1", "task-abc", "nginx")))
}

func TestClearVMMetrics(t *testing.T) {
	// Set metrics first
	SetVMMetrics("worker1", "task-xyz", "redis", 0.5, 64000000, 100, 200)

	// Verify they're set
	assert.Equal(t, 0.5, testutil.ToFloat64(VMCPU.WithLabelValues("worker1", "task-xyz", "redis")))

	// Clear and verify labels are gone by checking the metric family
	ClearVMMetrics("worker1", "task-xyz", "redis")

	// After deletion, re-accessing should return a new (zero) set
	// The old label values are deleted from the register
	metricsOutput := gatherMetricText(VMCPU)
	assert.False(t, strings.Contains(metricsOutput, `task_id="task-xyz"`), "task-xyz labels should be removed")
}

func TestSetDiskUsage(t *testing.T) {
	SetDiskUsage("worker1", "snapshots", 1073741824)

	assert.Equal(t, 1073741824.0, testutil.ToFloat64(DiskUsageBytes.WithLabelValues("worker1", "snapshots")))
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

// gatherMetricText returns the text representation of a metric for assertion.
func gatherMetricText(c prometheus.Collector) string {
	ch := make(chan prometheus.Metric, 100)
	go func() {
		c.Collect(ch)
		close(ch)
	}()
	var sb strings.Builder
	for m := range ch {
		sb.WriteString(m.Desc().String())
	}
	return sb.String()
}
