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
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
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

// VMCrashes counts VM failures that happened after a VM reached running.
// Type: CounterVec
// Labels: instance, service, reason
//
// It is deliberately separate from VMBootErrors: a boot failure and a
// post-start crash are different failure modes with different remedies, and
// only the latter means a VM that was counted running has gone away.
var VMCrashes = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace,
	Name:      "vm_crashes_total",
	Help:      "Total number of VM crashes after reaching running state, by reason.",
}, []string{"instance", "service", "reason"})

// VMHealth is the live healthcheck state of a service on a node, as a one-hot
// gauge: exactly one state label per {instance,service} has value 1 and the
// other three have value 0.
// Type: GaugeVec
// Labels: instance, service, state
//
// This is not a second health source: the state strings are pkg/runtime's
// VMHealth states, published from the same choke point that persists them to
// the node-local sidecar.
var VMHealth = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_health",
	Help:      "Live healthcheck state of a microVM service (1 for the current state, 0 otherwise).",
}, []string{"instance", "service", "state"})

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

// DiskTotalBytes tracks the total capacity of the filesystem backing a
// SwarmCracker directory, published alongside DiskUsageBytes so a pressure
// alert can be a ratio rather than an absolute threshold.
// Type: Gauge
// Labels: instance, path (e.g., snapshots, rootfs, state)
var DiskTotalBytes = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "disk_total_bytes",
	Help:      "Total disk space of the filesystem backing a SwarmCracker directory.",
}, []string{"instance", "path"})

// ── Per-VM Resource Metrics ───────────────────────────────────────

// VMBalloonMB records the configured memory-balloon target per VM, in MiB.
// Type: Gauge
// Labels: instance, task_id, service
//
// It is set from the task's balloon annotation when a VM is booted (see
// RecordVMBalloon). A VM without a configured balloon emits no series, because
// an unset gauge carrying no signal would be worse than no series at all.
var VMBalloonMB = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_balloon_mib",
	Help:      "Configured memory-balloon target for a microVM, in MiB.",
}, []string{"instance", "task_id", "service"})

// The four series below are written by the guest-metrics poller: the daemon asks
// the guest agent for the VM's own /proc counters every
// pkg/swarmkit.MetricsPollInterval and records the answer (see
// RecordVMTaskMetrics). They describe what the workload inside the VM used, not
// what the Firecracker process on the host used, so a busy guest and a busy VMM
// are distinguishable.
//
// All four are cumulative, so they are read with rate()/increase(); a task that
// is torn down has its series deleted (ClearVMTaskMetrics), so a scaled-down
// service leaves no stale row behind.

// VMCPU tracks CPU usage per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMCPU = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_cpu_seconds",
	Help:      "Cumulative busy CPU time consumed inside the guest, in seconds.",
}, []string{"instance", "task_id", "service"})

// VMMemory tracks memory usage per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMMemory = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_memory_bytes",
	Help:      "Memory in use inside the guest, in bytes (MemTotal - MemAvailable).",
}, []string{"instance", "task_id", "service"})

// VMNetRx tracks network bytes received per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMNetRx = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_net_rx_bytes",
	Help:      "Cumulative bytes received on the guest's non-loopback interfaces.",
}, []string{"instance", "task_id", "service"})

// VMNetTx tracks network bytes transmitted per VM.
// Type: Gauge
// Labels: instance, task_id, service
var VMNetTx = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: namespace,
	Name:      "vm_net_tx_bytes",
	Help:      "Cumulative bytes transmitted on the guest's non-loopback interfaces.",
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

// RecordVMBootError records a VM that never reached running.
// It increments VMBootErrors{reason} and nothing else: a VM that failed to
// boot was never counted as running, so VMsRunning must not move, and it is not
// a crash.
func RecordVMBootError(instance, reason string) {
	VMBootErrors.WithLabelValues(instance, reason).Inc()
}

// RecordVMCrash records a VM failure after it reached running.
// It decrements VMsRunning (the VM was counted when it started), increments
// VMsTotal{status="crashed"} and VMCrashes{service,reason}. It does not touch
// VMBootErrors: a post-start crash is not a boot failure.
func RecordVMCrash(instance, service, reason string) {
	VMsRunning.Dec()
	VMsTotal.WithLabelValues(instance, "crashed").Inc()
	VMCrashes.WithLabelValues(instance, service, reason).Inc()
}

// vmHealthStates are the one-hot states VMHealth publishes. They are
// pkg/runtime's VMHealth states, so the gauge and the node-local sidecar always
// agree on what a state is called.
var vmHealthStates = []string{
	scruntime.HealthStarting,
	scruntime.HealthHealthy,
	scruntime.HealthFailing,
	scruntime.HealthUnhealthy,
}

// RecordVMHealth publishes a one-hot health state for a service on a node: the
// given state becomes 1 and the other three become 0. An empty or unknown state
// deletes all four series for {instance,service}, so a task that no longer
// reports health leaves no stale series behind.
func RecordVMHealth(instance, service, state string) {
	known := false
	for _, s := range vmHealthStates {
		if s == state {
			known = true
			break
		}
	}
	if !known {
		VMHealth.DeletePartialMatch(prometheus.Labels{"instance": instance, "service": service})
		return
	}
	for _, s := range vmHealthStates {
		if s == state {
			VMHealth.WithLabelValues(instance, service, s).Set(1)
			continue
		}
		VMHealth.WithLabelValues(instance, service, s).Set(0)
	}
}

// RecordVMBootDuration records the time taken to boot a VM.
// Uses Observe to add an observation to the histogram.
func RecordVMBootDuration(instance, service string, durationSeconds float64) {
	VMBootDuration.WithLabelValues(instance, service).Observe(durationSeconds)
}

// RecordVMBalloon records the configured memory-balloon target for a VM.
// Callers must only invoke it for a VM that actually has a balloon configured:
// a VM without one gets no series, so the metric always carries signal.
func RecordVMBalloon(instance, taskID, service string, mib float64) {
	VMBalloonMB.WithLabelValues(instance, taskID, service).Set(mib)
}

// RecordVMTaskMetrics publishes one guest sample for a task: the four per-VM
// resource series, all keyed by the same instance/task_id/service labels.
//
// The caller must pass a sample the guest actually reported. A failed poll is
// not a zero sample, and writing zeros would pin a busy VM at 0 and look like an
// idle one.
func RecordVMTaskMetrics(instance, taskID, service string, cpuSeconds float64, memoryBytes, netRxBytes, netTxBytes uint64) {
	VMCPU.WithLabelValues(instance, taskID, service).Set(cpuSeconds)
	VMMemory.WithLabelValues(instance, taskID, service).Set(float64(memoryBytes))
	VMNetRx.WithLabelValues(instance, taskID, service).Set(float64(netRxBytes))
	VMNetTx.WithLabelValues(instance, taskID, service).Set(float64(netTxBytes))
}

// ClearVMTaskMetrics removes every per-VM resource series for a task.
//
// It is called when a VM is torn down: the guest is gone, so its last sample
// describes nothing, and a left-behind series would keep a scaled-down service
// on a dashboard and in any per-task sum.
func ClearVMTaskMetrics(instance, taskID string) {
	match := prometheus.Labels{"instance": instance, "task_id": taskID}
	VMCPU.DeletePartialMatch(match)
	VMMemory.DeletePartialMatch(match)
	VMNetRx.DeletePartialMatch(match)
	VMNetTx.DeletePartialMatch(match)
}

// RecordDiskUsage publishes used and total bytes for one path. Callers must
// only invoke it for a path that exists: a missing path gets no series rather
// than a misleading zero, so a pressure ratio is never computed against a
// directory the daemon does not actually own.
func RecordDiskUsage(instance, path string, usedBytes, totalBytes uint64) {
	DiskUsageBytes.WithLabelValues(instance, path).Set(float64(usedBytes))
	DiskTotalBytes.WithLabelValues(instance, path).Set(float64(totalBytes))
}
