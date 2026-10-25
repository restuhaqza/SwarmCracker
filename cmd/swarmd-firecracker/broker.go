package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/urfave/cli/v2"
)

// defaultBrokerListen is the broker's cluster-wide port. The manager always
// dials its own fixed port (broker defaultPort) and ignores any port in the
// node's advertised address, so both sides must agree on 4245.
const defaultBrokerListen = "0.0.0.0:4245"

// brokerRuntime holds the daemon's node broker settings.
type brokerRuntime struct {
	listenAddr string
	socketDir  string
	// logDir holds the per-task console logs. It is the directory the executor
	// writes, so a task's log is the one `swarmcracker vm logs` reads.
	logDir string
}

// brokerRuntimeFrom resolves the broker settings. An empty listen address
// disables the broker, which is useful for a node that should not accept
// manager traffic.
func brokerRuntimeFrom(ctx *cli.Context) brokerRuntime {
	return brokerRuntime{
		listenAddr: ctx.String("broker-listen"),
		socketDir:  ctx.String("socket-dir"),
		logDir:     scruntime.DefaultLogDir(),
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
		Tasks:     broker.SocketDirSource{Dir: cfg.socketDir, LogDir: cfg.logDir},
	})
	if err != nil {
		return nil, nil, err
	}

	// The node serves exec, logs, files and health. Exec and files dial the
	// task's guest agent over the vsock muxer; logs read the console file the
	// executor wrote; health reads the per-task sidecar the executor publishes.
	// The public route has already resolved the reference and enforced the
	// caller's selectors.
	handlers := []struct {
		op string
		h  broker.Handler
	}{
		{"exec", broker.NewExecHandler(broker.DialAgent)},
		{"logs", broker.NewLogsHandler()},
		{"files", broker.NewFilesHandler(broker.DialAgent)},
		{"health", broker.NewHealthHandler()},
	}
	for _, entry := range handlers {
		if err := srv.Handle(entry.op, entry.h); err != nil {
			return nil, nil, err
		}
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
