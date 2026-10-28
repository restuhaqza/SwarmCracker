//go:build !integration

package swarmkit

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/restuhaqza/swarmcracker/pkg/agent"
	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedGuest answers the framed protocol with a fixed metrics sample instead of
// reading /proc. It exists because the poller's job — dial, label, record, clear
// — is what these tests prove; parsing real /proc is covered in pkg/agent.
type cannedGuest struct {
	ln     net.Listener
	sample *agent.GuestMetrics // nil means "reply with an error frame"
	stop   chan struct{}
}

// startCannedGuest listens on a vsock muxer path and answers hello and metrics.
// Any other request gets an error frame, which also pins that the poller sends
// exactly the ops it needs.
func startCannedGuest(t *testing.T, sample *agent.GuestMetrics) (*cannedGuest, string) {
	t.Helper()
	// Unix socket paths are short-lived here; t.TempDir can exceed the limit on
	// some hosts, so use the same short-dir helper the healthcheck tests use.
	udsPath := filepath.Join(shortHealthcheckDir(t), "task.vsock")
	ln, err := net.Listen("unix", udsPath)
	require.NoError(t, err)

	g := &cannedGuest{ln: ln, sample: sample, stop: make(chan struct{})}
	go g.serve()
	t.Cleanup(func() {
		close(g.stop)
		_ = ln.Close()
	})
	return g, udsPath
}

func (g *cannedGuest) serve() {
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.handle(conn)
	}
}

func (g *cannedGuest) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	line, err := readMuxHandshakeLine(conn)
	if err != nil {
		return
	}
	if line != fmt.Sprintf("CONNECT %d", agent.DefaultPort) {
		_, _ = io.WriteString(conn, "ERR wrong port\n")
		return
	}
	if _, err := fmt.Fprintf(conn, "OK %d\n", 1073741824); err != nil {
		return
	}

	for {
		var req agent.Request
		if _, err := agent.ReadFrame(conn, &req); err != nil {
			return
		}
		switch req.Op {
		case "hello":
			if err := agent.WriteFrame(conn, agent.Message{Type: "hello", Version: agent.Version}); err != nil {
				return
			}
		case "metrics":
			var msg agent.Message
			if g.sample == nil {
				msg = agent.Message{Type: "error", Error: "metrics: read cpu seconds: not a procfs"}
			} else {
				msg = agent.Message{Type: "metrics", Metrics: g.sample}
			}
			if err := agent.WriteFrame(conn, msg); err != nil {
				return
			}
		default:
			_ = agent.WriteFrame(conn, agent.Message{Type: "error", Error: "unknown op " + req.Op})
			return
		}
	}
}

// shortMetricsInterval shortens the poll period for the duration of a test.
func shortMetricsInterval(t *testing.T) {
	t.Helper()
	orig := MetricsPollInterval
	MetricsPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { MetricsPollInterval = orig })
}

// taskSeriesCount counts the samples of name carrying this task's labels.
func taskSeriesCount(t *testing.T, name, instance, taskID string) int {
	t.Helper()
	return countSamples(t, name, map[string]string{"instance": instance, "task_id": taskID})
}

// TestController_MetricsMonitor_RecordsGuestSample is the core assertion: the
// poller publishes all four per-VM series with the task's instance/task_id/
// service labels.
func TestController_MetricsMonitor_RecordsGuestSample(t *testing.T) {
	shortMetricsInterval(t)
	_, uds := startCannedGuest(t, &agent.GuestMetrics{
		CPUSeconds:  12.5,
		MemoryBytes: 64 << 20,
		NetRxBytes:  1234,
		NetTxBytes:  5678,
	})
	ctrl := newMonitorController(t, uds, nil, &MockVMMManager{})
	t.Cleanup(ctrl.stopMetricsMonitor)

	ctrl.startMetricsMonitor()

	// The controller's task has no ServiceID, so the service label falls back to
	// the task ID — the same rule serviceName applies.
	const instance = "test-host"
	want := map[string]float64{
		"swarmcracker_vm_cpu_seconds":  12.5,
		"swarmcracker_vm_memory_bytes": 64 << 20,
		"swarmcracker_vm_net_rx_bytes": 1234,
		"swarmcracker_vm_net_tx_bytes": 5678,
	}

	require.Eventually(t, func() bool {
		for name := range want {
			if taskSeriesCount(t, name, instance, "task-hc-monitor") != 1 {
				return false
			}
		}
		return true
	}, 3*time.Second, 10*time.Millisecond, "the four per-VM series must be published")

	assert.Equal(t, want["swarmcracker_vm_cpu_seconds"],
		testutil.ToFloat64(swarmcrackermetrics.VMCPU.WithLabelValues(instance, "task-hc-monitor", "task-hc-monitor")))
	assert.Equal(t, want["swarmcracker_vm_memory_bytes"],
		testutil.ToFloat64(swarmcrackermetrics.VMMemory.WithLabelValues(instance, "task-hc-monitor", "task-hc-monitor")))
}

// TestController_MetricsMonitor_ClearsOnStop pins that teardown deletes the
// series: a scaled-down service must not leave a stale row behind.
func TestController_MetricsMonitor_ClearsOnStop(t *testing.T) {
	shortMetricsInterval(t)
	_, uds := startCannedGuest(t, &agent.GuestMetrics{CPUSeconds: 1, MemoryBytes: 2, NetRxBytes: 3, NetTxBytes: 4})
	ctrl := newMonitorController(t, uds, nil, &MockVMMManager{})
	t.Cleanup(ctrl.stopMetricsMonitor)

	ctrl.startMetricsMonitor()
	require.Eventually(t, func() bool {
		return taskSeriesCount(t, "swarmcracker_vm_cpu_seconds", "test-host", "task-hc-monitor") == 1
	}, 3*time.Second, 10*time.Millisecond)

	ctrl.stopMetricsMonitor()

	for _, name := range []string{
		"swarmcracker_vm_cpu_seconds",
		"swarmcracker_vm_memory_bytes",
		"swarmcracker_vm_net_rx_bytes",
		"swarmcracker_vm_net_tx_bytes",
	} {
		assert.Zero(t, taskSeriesCount(t, name, "test-host", "task-hc-monitor"),
			"%s must be deleted when the task is torn down", name)
	}

	// Joining is what makes the delete safe: a sample already in flight cannot
	// re-create the series behind the clear.
	done := make(chan struct{})
	go func() {
		ctrl.metricsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("metrics monitor goroutine did not exit")
	}
}

// TestController_MetricsMonitor_FailedSampleWritesNothing proves a guest that
// cannot report is not published as an idle one: no series appears, and the
// poller keeps trying (a later sample would still be recorded).
func TestController_MetricsMonitor_FailedSampleWritesNothing(t *testing.T) {
	shortMetricsInterval(t)
	_, uds := startCannedGuest(t, nil) // always answers with an error frame
	ctrl := newMonitorController(t, uds, nil, &MockVMMManager{})
	t.Cleanup(ctrl.stopMetricsMonitor)

	ctrl.startMetricsMonitor()

	// Give the poller several intervals to (not) publish.
	time.Sleep(150 * time.Millisecond)
	assert.Zero(t, taskSeriesCount(t, "swarmcracker_vm_cpu_seconds", "test-host", "task-hc-monitor"),
		"a failed sample must never be recorded as zero")
}

// TestController_MetricsMonitor_StartsOnce keeps a double Start from leaking a
// second goroutine, mirroring the health monitor's guard.
func TestController_MetricsMonitor_StartsOnce(t *testing.T) {
	shortMetricsInterval(t)
	_, uds := startCannedGuest(t, &agent.GuestMetrics{CPUSeconds: 5})
	ctrl := newMonitorController(t, uds, nil, &MockVMMManager{})
	t.Cleanup(ctrl.stopMetricsMonitor)

	ctrl.startMetricsMonitor()
	first := ctrl.metricsCancel
	ctrl.startMetricsMonitor()
	assert.Equal(t, fmt.Sprintf("%p", first), fmt.Sprintf("%p", ctrl.metricsCancel),
		"a second start must not replace the running monitor")
}

// TestController_MetricsMonitor_SkipsTasklessController covers the defensive
// branch: a controller with no task cannot be labelled, so it polls nothing.
func TestController_MetricsMonitor_SkipsTasklessController(t *testing.T) {
	ctrl := &Controller{config: &Config{Hostname: "test-host"}}
	ctrl.startMetricsMonitor()
	assert.Nil(t, ctrl.metricsCancel)
}
