package swarmkit

import (
	"bytes"
	"context"
	"fmt"
	"time"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/agent"
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

// healthcheckProbeTimeout returns the probe timeout, falling back to the default
// when the config leaves it unset or negative.
func healthcheckProbeTimeout(h *types.Healthcheck) time.Duration {
	if h == nil || h.Timeout <= 0 {
		return defaultHealthcheckTimeout
	}
	return h.Timeout
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
