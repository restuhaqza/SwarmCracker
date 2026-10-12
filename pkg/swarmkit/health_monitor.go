package swarmkit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/healthcheck"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// startHealthMonitor launches a goroutine that watches an already-healthy task
// and force-stops its VM once it fails Retries consecutive probes, so that Wait
// reports a failure and SwarmKit reschedules the task per the service's restart
// policy. It is a no-op when the task declares no healthcheck (or disables it),
// so a task without one gets no goroutine at all.
func (c *Controller) startHealthMonitor() {
	container := c.task.Spec.GetContainer()
	if container == nil || container.Healthcheck == nil {
		return
	}

	h := healthcheckFromAPI(container.Healthcheck)
	argv, enabled, err := healthcheckCommand(h.Test)
	if err != nil || !enabled {
		// awaitHealthy already rejects a malformed test before Start returns,
		// so a monitor is never started for one.
		return
	}

	// The monitor outlives the agent's per-operation context, which is
	// cancelled after every Do. It therefore owns a context rooted at
	// Background, tied to the controller's lifetime: stopHealthMonitor cancels
	// it. healthMu guards the cancel func and the WaitGroup's Add so a
	// concurrent stop cannot race a start.
	c.healthMu.Lock()
	if c.healthCancel != nil {
		// A monitor is already running (Start called twice); never leak a
		// second one.
		c.healthMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.healthCancel = cancel
	c.healthWG.Add(1)
	c.healthMu.Unlock()

	// The task was already healthy when Start returned, so the monitor's clock
	// starts now.
	start := time.Now()
	interval := healthcheckProbeInterval(h)
	udsPath := vsockUDSPath(c.socketPath)
	cmd := strings.Join(argv, " ")

	go func() {
		defer c.healthWG.Done()
		c.monitorHealth(ctx, h, argv, udsPath, cmd, interval, start)
	}()
}

// monitorHealth polls the guest healthcheck every interval and returns when the
// controller is stopped (ctx cancelled) or after it has force-stopped a VM that
// failed Retries consecutive probes. It never takes c.mu.
func (c *Controller) monitorHealth(
	ctx context.Context,
	h *types.Healthcheck,
	argv []string,
	udsPath, cmd string,
	interval time.Duration,
	start time.Time,
) {
	tracker := healthcheck.NewTracker(h, start)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}

		healthy, output, probeErr := runHealthcheck(ctx, udsPath, h, argv)
		if ctx.Err() != nil {
			// A teardown cancels the context; never report that as unhealthy.
			return
		}
		if probeErr != nil {
			// A transport error (e.g. the guest agent not reachable yet) counts
			// as a failed probe, not a fatal error: keep polling until retries
			// are exhausted.
			c.logger.Warn().Err(probeErr).Msg("Healthcheck probe failed")
			healthy = false
		}

		switch tracker.Observe(time.Now(), healthy) {
		case healthcheck.VerdictUnhealthy:
			// Record before stopping: Wait reads the reason once the process
			// exits, which is after this point.
			c.recordHealthFailure(fmt.Sprintf("%s: %s", cmd, output))
			c.logger.Warn().
				Str("command", cmd).
				Str("output", output).
				Msg("Task became unhealthy; stopping VM so SwarmKit reschedules it")
			if err := c.vmmMgr.ForceStop(ctx, c.convertTask()); err != nil {
				c.logger.Error().Err(err).Msg("Failed to stop unhealthy VM")
			}
			return
		}
	}
}

// stopHealthMonitor cancels and joins the monitor goroutine. It is idempotent
// and safe to call when no monitor is running. Joining (waiting on healthWG)
// guarantees a stop path cannot return while the monitor is still touching the
// controller, which -race can then prove.
func (c *Controller) stopHealthMonitor() {
	c.healthMu.Lock()
	cancel := c.healthCancel
	c.healthCancel = nil
	c.healthMu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	c.healthWG.Wait()
}

// recordHealthFailure stores the last health failure reason for Wait to report.
func (c *Controller) recordHealthFailure(reason string) {
	c.healthMu.Lock()
	c.healthFailure = reason
	c.healthMu.Unlock()
}

// healthcheckFailure returns the recorded health failure reason, if any.
func (c *Controller) healthcheckFailure() string {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	return c.healthFailure
}
