package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/urfave/cli/v2"
)

// defaultBrokerListen is the broker's cluster-wide port. The manager derives
// the same port from the node address, so this is the only place it is chosen.
const defaultBrokerListen = "0.0.0.0:4245"

// brokerRuntime holds the daemon's node broker settings.
type brokerRuntime struct {
	listenAddr string
	socketDir  string
}

// brokerRuntimeFrom resolves the broker settings. An empty listen address
// disables the broker, which is useful for a node that should not accept
// manager traffic.
func brokerRuntimeFrom(ctx *cli.Context) brokerRuntime {
	return brokerRuntime{
		listenAddr: ctx.String("broker-listen"),
		socketDir:  ctx.String("socket-dir"),
	}
}

// startBroker starts the node-side broker: an mTLS HTTP server that lets the
// manager reach this node's tasks for exec, logs, files and health. It uses the
// node's existing cluster certificate, so a manager authenticates with the same
// identity it uses for the ingress mesh, and no user token is involved.
//
// It returns the server and a stop function that is safe to call more than
// once. A disabled broker returns (nil, no-op, nil); a certificate or bind
// failure is returned rather than swallowed, so a misconfigured node fails
// loudly instead of silently refusing manager traffic.
func startBroker(ctx context.Context, cfg brokerRuntime, stateDir string) (*broker.Server, func(), error) {
	if cfg.listenAddr == "" {
		return nil, func() {}, nil
	}

	cert, caPool, err := loadIngressTLS(stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("node broker: %w", err)
	}

	srv, err := broker.NewServer(broker.ServerConfig{
		Addr:      cfg.listenAddr,
		TLSConfig: serverTLSConfig(cert, caPool),
		Tasks:     broker.SocketDirSource{Dir: cfg.socketDir},
	})
	if err != nil {
		return nil, nil, err
	}

	lis, err := srv.Listen()
	if err != nil {
		return nil, nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if serveErr := srv.Serve(runCtx, lis); serveErr != nil {
			log.G(ctx).WithError(serveErr).Warn("Node broker stopped")
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}

	log.G(ctx).Infof("Node broker listening on %s", srv.Addr())
	return srv, stop, nil
}
