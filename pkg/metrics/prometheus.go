// Package metrics provides Prometheus metrics for SwarmCracker observability.
//
// Metrics are auto-registered with the default prometheus registry,
// exposed via promhttp.Handler() on the /metrics endpoint.
//
// Dashboard compatibility:
//   - Grafana dashboards in infrastructure/observability/grafana/dashboards/
//   - Alert rules in infrastructure/observability/prometheus/alerts.yml
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metric name prefix for all SwarmCracker metrics.
const namespace = "swarmcracker"

// ── VM Lifecycle Metrics ──────────────────────────────────────────

// VMsRunning tracks the number of currently running VMs.
// Type: Gauge
// Labels: instance
var VMsRunning = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vms_running",
	Help:      "Number of microVMs currently in running state.",
})

// VMsTotal tracks VM lifecycle transitions.
// Type: CounterVec
// Labels: instance, status (started, stopped, crashed)
var VMsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "vms_total",
	Help:      "Total number of VM lifecycle transitions by status.",
}, []string{"instance", "status"})

// VMBootDuration tracks the time it takes for a VM to boot.
// Type: HistogramVec
// Labels: instance, service
// Buckets: 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60 (seconds)
var VMBootDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: namespace,
	Name:      "vm_boot_duration_seconds",
	Help:      "Time to boot a microVM from start to running.",
	Buckets:   []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
}, []string{"instance", "service"})

// VMBootErrors counts VM boot failures.
// Type: CounterVec
// Labels: instance, reason
var VMBootErrors = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "vm_boot_errors_total",
	Help:      "Total number of VM boot failures by reason.",
}, []string{"instance", "reason"})

// ── Network Metrics ───────────────────────────────────────────────

// VXLANPeers tracks the current number of VXLAN peers.
// Type: Gauge
// Labels: instance
var VXLANPeers = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vxlan_peers",
	Help:      "Number of active VXLAN peers for this node.",
})

// VXLANExpectedPeers tracks the expected number of VXLAN peers.
// Type: Gauge
// Labels: instance
var VXLANExpectedPeers = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vxlan_expected_peers",
	Help:      "Expected number of VXLAN peers (set from config or Consul).",
})

// ── Cluster Health Metrics ────────────────────────────────────────

// ManagerHealth indicates whether the SwarmKit manager is healthy.
// Type: Gauge
// Labels: instance
// Values: 1 = healthy, 0 = unhealthy
var ManagerHealth = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "manager_health",
	Help:      "SwarmKit manager health (1 = healthy, 0 = unhealthy).",
})

// RaftHealth indicates whether the Raft consensus is healthy.
// Type: Gauge
// Labels: instance
// Values: 1 = healthy, 0 = unhealthy
var RaftHealth = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "raft_health",
	Help:      "SwarmKit Raft consensus health (1 = healthy, 0 = unhealthy).",
})

// ── Resource Metrics ──────────────────────────────────────────────

// DiskUsageBytes tracks the disk usage of the swarmcracker state directory.
// Type: Gauge
// Labels: instance, path (e.g., snapshots, rootfs, state)
var DiskUsageBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "disk_usage_bytes",
	Help:      "Disk space used by SwarmCracker directory.",
}, []string{"instance", "path"})

// ── Per-VM Resource Metrics ───────────────────────────────────────

// VMCPU tracks CPU usage per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMCPU = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_cpu_seconds",
	Help:      "CPU time consumed by a VM in seconds.",
}, []string{"instance", "task_id", "service"})

// VMMemory tracks memory usage per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMMemory = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_memory_bytes",
	Help:      "RSS memory used by a VM in bytes.",
}, []string{"instance", "task_id", "service"})

// VMNetRx tracks network bytes received per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMNetRx = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_net_rx_bytes",
	Help:      "Total network bytes received by a VM.",
}, []string{"instance", "task_id", "service"})

// VMNetTx tracks network bytes transmitted per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMNetTx = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_net_tx_bytes",
	Help:      "Total network bytes transmitted by a VM.",
}, []string{"instance", "task_id", "service"})

// ── Convenience Helpers ───────────────────────────────────────────

// RecordVMStarted records a VM start event.
// Increments VMsRunning and VMsTotal{status="started"}.
func RecordVMStarted(instance, service string) {
	VMsRunning.Inc()
	VMsTotal.WithLabelValues(instance, "started").Inc()
	_ = service // reserved for future per-service VMsRunning gauge
}

// RecordVMStopped records a VM stop event.
// Decrements VMsRunning and increments VMsTotal{status="stopped"}.
func RecordVMStopped(instance string) {
	VMsRunning.Dec()
	VMsTotal.WithLabelValues(instance, "stopped").Inc()
}

// RecordVMCrashed records a VM crash event.
// Decrements VMsRunning and increments VMsTotal{status="crashed"}.
func RecordVMCrashed(instance, reason string) {
	VMsRunning.Dec()
	VMsTotal.WithLabelValues(instance, "crashed").Inc()
	if reason != "" {
		VMBootErrors.WithLabelValues(instance, reason).Inc()
	}
}

// RecordVMBootDuration records the time taken to boot a VM.
// Uses Observe to add an observation to the histogram.
func RecordVMBootDuration(instance, service string, durationSeconds float64) {
	VMBootDuration.WithLabelValues(instance, service).Observe(durationSeconds)
}

// SetVXLANPeerCount sets the current and expected VXLAN peer counts.
func SetVXLANPeerCount(instance string, current, expected int) {
	VXLANPeers.Set(float64(current))
	VXLANExpectedPeers.Set(float64(expected))
}

// SetManagerHealth sets the manager health gauge.
func SetManagerHealth(instance string, healthy bool) {
	if healthy {
		ManagerHealth.Set(1)
	} else {
		ManagerHealth.Set(0)
	}
}

// SetRaftHealth sets the Raft health gauge.
func SetRaftHealth(instance string, healthy bool) {
	if healthy {
		RaftHealth.Set(1)
	} else {
		RaftHealth.Set(0)
	}
}

// SetVMMetrics updates the per-VM resource gauges from collected metrics.
func SetVMMetrics(instance, taskID, service string, cpuSec float64, memoryBytes uint64, rxBytes, txBytes uint64) {
	VMCPU.WithLabelValues(instance, taskID, service).Set(cpuSec)
	VMMemory.WithLabelValues(instance, taskID, service).Set(float64(memoryBytes))
	VMNetRx.WithLabelValues(instance, taskID, service).Set(float64(rxBytes))
	VMNetTx.WithLabelValues(instance, taskID, service).Set(float64(txBytes))
}

// ClearVMMetrics removes a VM's per-resource metric labels (e.g., when VM stops).
func ClearVMMetrics(instance, taskID, service string) {
	VMCPU.DeleteLabelValues(instance, taskID, service)
	VMMemory.DeleteLabelValues(instance, taskID, service)
	VMNetRx.DeleteLabelValues(instance, taskID, service)
	VMNetTx.DeleteLabelValues(instance, taskID, service)
}

// SetDiskUsage sets the disk usage gauge for a given path.
func SetDiskUsage(instance, path string, bytes int64) {
	DiskUsageBytes.WithLabelValues(instance, path).Set(float64(bytes))
}
