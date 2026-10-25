package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/broker"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
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
	// snapshotsEnabled turns on the experimental snapshot operation. It is
	// disabled by default: the handler is still registered, so a manager gets a
	// typed 501 instead of the server's unregistered-operation answer.
	snapshotsEnabled bool
	// snapshotDir is the pkg/snapshot root. pkg/snapshot writes
	// <snapshotDir>/<id>/metadata.json and that directory is the source of truth.
	snapshotDir string
}

// brokerRuntimeFrom resolves the broker settings. An empty listen address
// disables the broker, which is useful for a node that should not accept
// manager traffic.
func brokerRuntimeFrom(ctx *cli.Context) brokerRuntime {
	return brokerRuntime{
		listenAddr:       ctx.String("broker-listen"),
		socketDir:        ctx.String("socket-dir"),
		logDir:           scruntime.DefaultLogDir(),
		snapshotsEnabled: ctx.Bool("snapshots-enable"),
		snapshotDir:      ctx.String("snapshot-dir"),
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

	// The node serves exec, logs, files, health and the experimental snapshots
	// operation. Exec and files dial the task's guest agent over the vsock muxer;
	// logs read the console file the executor wrote; health reads the per-task
	// sidecar the executor publishes; snapshots use the node-local pkg/snapshot
	// store. The public route has already resolved the reference and enforced the
	// caller's selectors.
	handlers := []struct {
		op string
		h  broker.Handler
	}{
		{"exec", broker.NewExecHandler(broker.DialAgent)},
		{"logs", broker.NewLogsHandler()},
		{"files", broker.NewFilesHandler(broker.DialAgent)},
		{"health", broker.NewHealthHandler()},
		{"snapshots", snapshotHandler(ctx, cfg)},
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

// snapshotHandler builds the experimental snapshots operation. Disabled means a
// registered handler with no store, which answers a typed 501 — the route still
// exists. A manager construction failure is logged and degrades to the same
// disabled handler: a bad snapshot directory must not stop the broker, because
// exec, logs, files and health do not depend on it.
func snapshotHandler(ctx context.Context, cfg brokerRuntime) broker.Handler {
	if !cfg.snapshotsEnabled {
		return broker.NewSnapshotHandler(nil)
	}

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{
		Enabled:      true,
		SnapshotDir:  cfg.snapshotDir,
		MaxSnapshots: 3,
		MaxAge:       168 * time.Hour,
	})
	if err != nil {
		log.G(ctx).WithError(err).Warn("Node broker: snapshots disabled (snapshot store unavailable)")
		return broker.NewSnapshotHandler(nil)
	}
	return broker.NewSnapshotHandler(mgr)
}
