package swarmkit

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/healthcheck"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// maxHealthcheckOutput bounds the combined output captured from a probe and
// returned for the failure message (4 KiB).
const maxHealthcheckOutput = 4 << 10

// defaultHealthcheckTimeout is used when a task declares a healthcheck without a
// positive timeout. SwarmKit's "inherit" is a nil duration, which converts to
// zero, and a zero timeout would cancel every probe the instant it starts. Our
// CLI always supplies a default, but a spec built elsewhere may not.
const defaultHealthcheckTimeout = 30 * time.Second

// defaultHealthcheckInterval is used when a task declares a healthcheck without
// a positive interval. Like the timeout, SwarmKit's "inherit" is a nil duration
// that converts to zero, and a zero interval would spin the CPU probing back to
// back.
const defaultHealthcheckInterval = 30 * time.Second

// healthcheckProbeTimeout returns the probe timeout, falling back to the default
// when the config leaves it unset or negative.
func healthcheckProbeTimeout(h *types.Healthcheck) time.Duration {
	if h == nil || h.Timeout <= 0 {
		return defaultHealthcheckTimeout
	}
	return h.Timeout
}

// contextErr carries a context error out of the health gate while staying
// classifiable by SwarmKit. Its agent decides whether an error is retryable with
// github.com/pkg/errors.Cause, which unwraps only errors implementing
// `Cause() error` — NOT the `Unwrap() error` that fmt.Errorf("%w") provides.
// A plainly wrapped context error would therefore mark a shutdown during the
// health gate as a task FAILURE instead of a retry. This type satisfies both
// conventions, so errors.Is and pkg/errors.Cause both recover the sentinel.
type contextErr struct{ err error }

func (e contextErr) Error() string { return "healthcheck: " + e.err.Error() }
func (e contextErr) Unwrap() error { return e.err } // for errors.Is / errors.As
func (e contextErr) Cause() error  { return e.err } // for pkg/errors.Cause

// healthcheckProbeInterval returns the delay between probes, falling back to the
// default when the config leaves it unset or negative.
func healthcheckProbeInterval(h *types.Healthcheck) time.Duration {
	if h == nil || h.Interval <= 0 {
		return defaultHealthcheckInterval
	}
	return h.Interval
}

// healthcheckCommand converts a HealthConfig.Test into the guest argv to run.
//
//	["CMD-SHELL", cmd] -> ["/bin/sh", "-c", cmd]
//	["CMD", args...]   -> args
//	["NONE"] or empty  -> disabled (enabled=false)
//	anything else      -> error
func healthcheckCommand(test []string) ([]string, bool, error) {
	if len(test) == 0 {
		return nil, false, nil
	}
	switch test[0] {
	case "NONE":
		return nil, false, nil
	case "CMD-SHELL":
		if len(test) < 2 {
			return nil, false, fmt.Errorf("healthcheck: CMD-SHELL requires a command")
		}
		return []string{"/bin/sh", "-c", test[1]}, true, nil
	case "CMD":
		if len(test) < 2 {
			return nil, false, fmt.Errorf("healthcheck: CMD requires at least one argument")
		}
		return append([]string(nil), test[1:]...), true, nil
	default:
		return nil, false, fmt.Errorf("healthcheck: unsupported test type %q", test[0])
	}
}

// healthcheckFromAPI converts a SwarmKit api.HealthConfig into the internal
// types.Healthcheck. A nil input stays nil, meaning no healthcheck is declared.
func healthcheckFromAPI(hc *api.HealthConfig) *types.Healthcheck {
	if hc == nil {
		return nil
	}
	return &types.Healthcheck{
		Test:        append([]string(nil), hc.Test...),
		Interval:    healthcheckDuration(hc.Interval),
		Timeout:     healthcheckDuration(hc.Timeout),
		Retries:     int(hc.Retries),
		StartPeriod: healthcheckDuration(hc.StartPeriod),
	}
}

// healthcheckDuration converts a proto duration to a time.Duration. A nil
// duration is zero (SwarmKit's "inherit"). An invalid duration is reported and
// treated as zero rather than failing the whole task conversion.
func healthcheckDuration(p *gogotypes.Duration) time.Duration {
	if p == nil {
		return 0
	}
	d, err := gogotypes.DurationFromProto(p)
	if err != nil {
		log.Warn().Err(err).Msg("invalid healthcheck duration; using zero")
		return 0
	}
	return d
}

// boundedBuffer accumulates at most limit bytes and silently discards the rest,
// so a chatty guest cannot make a probe allocate unbounded memory.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// runHealthcheck executes cmd inside the guest via the #43 vsock agent. It dials
// the Firecracker vsock muxer at udsPath, runs cmd with no TTY and no stdin, and
// reports healthy iff the exit code is 0. The whole probe (dial + exec) is
// bounded by h.Timeout. The returned string is the combined stdout+stderr,
// truncated to maxHealthcheckOutput bytes, for use in a failure message.
//
// A dial or exec error is returned as an error; the caller counts that as an
// unhealthy probe. The connection is always closed.
func runHealthcheck(ctx context.Context, udsPath string, h *types.Healthcheck, cmd []string) (bool, string, error) {
	if h == nil {
		return false, "", fmt.Errorf("healthcheck: nil config")
	}

	ctx, cancel := context.WithTimeout(ctx, healthcheckProbeTimeout(h))
	defer cancel()

	client, err := agent.Dial(ctx, udsPath, agent.DefaultPort)
	if err != nil {
		return false, "", fmt.Errorf("healthcheck: dial guest agent: %w", err)
	}
	defer func() { _ = client.Close() }()

	out := &boundedBuffer{limit: maxHealthcheckOutput}
	code, err := client.Exec(ctx, cmd, agent.ExecOptions{Stdout: out, Stderr: out})
	if err != nil {
		return false, out.String(), fmt.Errorf("healthcheck: exec: %w", err)
	}
	return code == 0, out.String(), nil
}

// awaitHealthy blocks until the task's healthcheck passes, fails, or ctx is
// done. It is a no-op for a task that declares no healthcheck (or disables it),
// so tasks without one start exactly as before.
//
// It must NOT be called while holding c.mu: Shutdown, Terminate and Remove also
// take c.mu, and holding it for the whole start period would stall a
// `service rm` until the task became healthy.
func (c *Controller) awaitHealthy(ctx context.Context) error {
	container := c.task.Spec.GetContainer()
	if container == nil || container.Healthcheck == nil {
		return nil
	}

	h := healthcheckFromAPI(container.Healthcheck)
	argv, enabled, err := healthcheckCommand(h.Test)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	if !enabled {
		return nil
	}

	tracker := healthcheck.NewTracker(h, c.startTime)
	interval := healthcheckProbeInterval(h)
	udsPath := vsockUDSPath(c.socketPath)
	cmd := strings.Join(argv, " ")

	// Publish the starting state before the first probe so a node-local
	// `vm status` shows the task is being checked rather than looking healthy.
	c.publishHealth(scruntime.HealthStarting, 0, cmd, "")

	attempts := 0
	for {
		if err := ctx.Err(); err != nil {
			return contextErr{err}
		}

		attempts++
		healthy, output, probeErr := runHealthcheck(ctx, udsPath, h, argv)

		// A shutdown must surface as a context error, never as an unhealthy
		// verdict: SwarmKit retries on context.Canceled/DeadlineExceeded but
		// fails the task on anything else.
		if err := ctx.Err(); err != nil {
			return contextErr{err}
		}

		if probeErr != nil {
			// A transport error counts as an unhealthy probe, not a fatal
			// error: the guest agent may still be starting up.
			c.logger.Warn().Err(probeErr).Int("attempt", attempts).Msg("Healthcheck probe failed")
			healthy = false
		}

		switch tracker.Observe(time.Now(), healthy) {
		case healthcheck.VerdictHealthy:
			c.publishHealth(scruntime.HealthHealthy, 0, cmd, output)
			c.logger.Info().Int("attempts", attempts).Msg("Task is healthy")
			return nil
		case healthcheck.VerdictUnhealthy:
			c.publishHealth(scruntime.HealthUnhealthy, tracker.Failures(), cmd, output)
			c.logger.Info().
				Int("attempts", attempts).
				Str("command", cmd).
				Str("output", output).
				Msg("Task failed its healthcheck")
			return fmt.Errorf("healthcheck failed after %d attempts: %s: %s", attempts, cmd, output)
		default:
			// VerdictStarting: the task is not RUNNING yet, so "starting" is the
			// honest state; the streak and the last output carry the detail.
			c.publishHealth(scruntime.HealthStarting, tracker.Failures(), cmd, output)
		}

		// VerdictStarting: wait one interval, or until ctx is done.
		select {
		case <-ctx.Done():
			return contextErr{ctx.Err()}
		case <-time.After(interval):
		}
	}
}
