package swarmkit

import (
	"context"
	"fmt"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	swarmcrackermetrics "github.com/restuhaqza/swarmcracker/pkg/metrics"
)

// MetricsPollInterval is how often the guest is asked for its own resource
// counters. It mirrors the low end of Docker's own stats interval: frequent
// enough that a 1-minute rate() window has several samples, infrequent enough
// that a node running many microVMs spends no meaningful time in the guest
// agent.
//
// It is a variable so tests can shorten it; the daemon never changes it.
var MetricsPollInterval = 15 * time.Second

// metricsProbeTimeout bounds one sample: dial, hello and the metrics reply. The
// agent's dial retries for up to 5s while the guest agent is still starting, so
// this leaves room for a handshake that has to wait one out.
const metricsProbeTimeout = 10 * time.Second

// startMetricsMonitor launches the goroutine that samples the guest's resource
// counters for the lifetime of the VM.
//
// Unlike startHealthMonitor it does not depend on a healthcheck: every task gets
// the four per-VM series, because every task consumes CPU, memory and network.
// A sample that fails leaves the series unset rather than writing zeros, and a
// later tick recovers (the counters are cumulative, so one missed interval costs
// nothing but that interval).
func (c *Controller) startMetricsMonitor() {
	c.metricsMu.Lock()
	if c.metricsCancel != nil {
		// Already polling (Start called twice); never leak a second goroutine.
		c.metricsMu.Unlock()
		return
	}
	task := c.task
	if task == nil {
		c.metricsMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.metricsCancel = cancel
	// Capture the labels now: Update may reassign c.task, and the poller must
	// keep labelling the VM it sampled.
	c.metricsTaskID = task.ID
	// Same rule as serviceName: the service ID is the attribution label, and a
	// task without one is its own service.
	service := task.ID
	if task.ServiceID != "" {
		service = task.ServiceID
	}
	c.metricsWG.Add(1)
	c.metricsMu.Unlock()

	udsPath := vsockUDSPath(c.socketPath)
	go func() {
		defer c.metricsWG.Done()
		c.monitorMetrics(ctx, udsPath, task.ID, service)
	}()
}

// monitorMetrics samples the guest until ctx is cancelled. It never takes c.mu,
// for the same reason the health monitor does not: teardown holds that lock.
func (c *Controller) monitorMetrics(ctx context.Context, udsPath, taskID, service string) {
	// The first sample is immediate so a series exists as soon as the VM is
	// running, rather than one interval later.
	timer := time.NewTimer(0)
	defer timer.Stop()

	warned := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		sample, err := sampleGuestMetrics(ctx, udsPath)
		if ctx.Err() != nil {
			// A teardown cancels the context; never report that as a failure.
			return
		}
		if err != nil {
			// Warn once per outage: a guest that cannot answer (an image whose
			// agent predates the metrics op, or one still booting) would
			// otherwise log on every tick forever.
			if !warned {
				warned = true
				c.logger.Warn().
					Str("task_id", taskID).
					Err(err).
					Msg("Guest metrics unavailable; per-task resource series stay unset until the guest agent answers")
			}
		} else {
			warned = false
			swarmcrackermetrics.RecordVMTaskMetrics(
				c.config.Hostname, taskID, service,
				sample.CPUSeconds, sample.MemoryBytes, sample.NetRxBytes, sample.NetTxBytes,
			)
		}

		timer.Reset(MetricsPollInterval)
	}
}

// sampleGuestMetrics dials the guest agent and reads one metrics sample. Every
// call uses its own connection: the agent serves framed requests serially, and
// a poll must never queue behind a long-running exec.
func sampleGuestMetrics(ctx context.Context, udsPath string) (agent.GuestMetrics, error) {
	ctx, cancel := context.WithTimeout(ctx, metricsProbeTimeout)
	defer cancel()

	client, err := agent.Dial(ctx, udsPath, agent.DefaultPort)
	if err != nil {
		return agent.GuestMetrics{}, fmt.Errorf("metrics: dial guest agent: %w", err)
	}
	defer func() { _ = client.Close() }()

	return client.Metrics(ctx)
}

// stopMetricsMonitor cancels and joins the poller, then deletes the task's
// resource series. It is idempotent and safe to call when no monitor is running.
//
// The series are deleted after the join, so a sample already in flight cannot
// re-create them behind the clear. Clearing is the counterpart of the health
// gauge's clear: once the VM is gone its last sample describes nothing, and a
// stale row would survive a scale-down.
func (c *Controller) stopMetricsMonitor() {
	c.metricsMu.Lock()
	cancel := c.metricsCancel
	c.metricsCancel = nil
	taskID := c.metricsTaskID
	c.metricsMu.Unlock()

	if cancel != nil {
		cancel()
		c.metricsWG.Wait()
	}
	if taskID != "" && c.config != nil {
		swarmcrackermetrics.ClearVMTaskMetrics(c.config.Hostname, taskID)
	}
}
