package main

import (
	"context"

	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/tracing"
	"github.com/urfave/cli/v2"
)

// startTracing builds the optional trace exporter from the daemon's flags.
//
// Tracing off — the default — constructs no exporter and dials nothing, so a
// usable provider always comes back and the caller never branches on whether the
// feature is on.
func startTracing(ctx *cli.Context) (*tracing.Provider, error) {
	provider, err := tracing.New(context.Background(), tracing.Config{
		Enabled:     ctx.Bool("tracing-enabled"),
		Endpoint:    ctx.String("tracing-endpoint"),
		ServiceName: ctx.String("tracing-service-name"),
		SampleRatio: ctx.Float64("tracing-sample-ratio"),
	})
	if err != nil {
		return nil, err
	}

	if provider.Enabled() {
		log.G(context.Background()).Infof(
			"Tracing enabled (endpoint=%s, service=%s, sample-ratio=%v)",
			ctx.String("tracing-endpoint"), ctx.String("tracing-service-name"), ctx.Float64("tracing-sample-ratio"))
	}
	return provider, nil
}

// shutdownTracing flushes buffered spans. Exiting matters more than a complete
// export, so the last flush is bounded: a collector that has gone away must not
// delay a node stop.
func shutdownTracing(provider *tracing.Provider) {
	ctx, cancel := context.WithTimeout(context.Background(), tracing.ShutdownTimeout)
	defer cancel()

	if err := provider.Shutdown(ctx); err != nil {
		log.G(context.Background()).Warnf("Tracing shutdown: %v", err)
	}
}
