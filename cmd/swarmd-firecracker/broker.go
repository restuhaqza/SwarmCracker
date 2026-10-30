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
	// rootfsDir is the executor's rootfs directory, recorded on a snapshot so
	// restore can tell a missing task disk from a corrupt snapshot.
	rootfsDir string
	// taskAlive decides whether an existing task socket belongs to a live VMM.
	// Nil keeps the stat-only resolution tests use; the daemon sets it to the
	// Firecracker process probe.
	taskAlive func(socketPath string) bool
	// restored is the node's store of restored-instance records: tasks started
	// from a migration artifact run a VM whose TAP device, vsock muxer and disk
	// belong to the source task, and only this record says so. It is built
	// before startNode (see openDaemonState) because opening it can fail, and
	// the same instance is shared with anything else that must read the records.
	// Nil means this node keeps no records, which is only true in tests.
	restored *scruntime.RestoredRegistry
}

// logRestoredInstances reports the restored instances this node believes it
// hosts, once, when the broker comes up. It is the daemon's startup view of the
// record store: after a restart an operator can tell whether the node still maps
// a migrated task to its VM. A record whose VM is gone is not pruned here —
// resolution owns that answer, because deleting a record that a still-starting
// VM is about to use would break the restore it describes.
func logRestoredInstances(ctx context.Context, restored *scruntime.RestoredRegistry) {
	if restored == nil {
		return
	}

	records, err := restored.List()
	if err != nil {
		log.G(ctx).WithError(err).Warn("Restored instances: cannot read the record store")
		return
	}
	if len(records) == 0 {
		return
	}

	log.G(ctx).Infof("Restored instances: %d task(s) run a restored VM", len(records))
	for _, record := range records {
		log.G(ctx).Debugf("restored instance: task %s runs the VM of %s (artifact %s)",
			record.TaskID, record.VMID, record.ArtifactID)
	}
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
		rootfsDir:        ctx.String("rootfs-dir"),
		taskAlive:        taskSocketAlive,
	}
}

// startBroker starts the node-side broker: an mTLS HTTP server that lets the
// manager reach this node's tasks for exec, logs, files and health. It uses the
// node's existing cluster certificate, so a manager authenticates with the same
// identity it uses for the ingress mesh, and no user token is involved.
//
// It returns the server, whether the experimental snapshot store actually became
// active, and a stop function that is safe to call more than once. The boolean is
// the effective capability: a node whose snapshot store failed to build must not
// advertise `snapshots` even when --snapshots-enable was passed. A disabled broker
// returns (nil, false, no-op, nil); a certificate or bind failure is returned
// rather than swallowed, so a misconfigured node fails loudly instead of silently
// refusing manager traffic.
// taskSocketAlive reports whether a Firecracker API socket still belongs to a
// live VMM. The process table is the signal rather than an API probe: during
// startup the socket exists before the API answers, and treating that window
// as exited would turn a legitimate exec into a 404. A crashed VMM leaves its
// socket file behind, which is the case this probe exists to catch.
func taskSocketAlive(socketPath string) bool {
	return scruntime.FindFirecrackerPIDBySocket(socketPath) > 0
}

func startBroker(ctx context.Context, cfg brokerRuntime, stateDir string) (*broker.Server, bool, func(), error) {
	if cfg.listenAddr == "" {
		return nil, false, func() {}, nil
	}

	logRestoredInstances(ctx, cfg.restored)
	cert, caPool, err := loadIngressTLS(stateDir)
	if err != nil {
		return nil, false, nil, fmt.Errorf("node broker: %w", err)
	}

	srv, err := broker.NewServer(broker.ServerConfig{
		Addr:      cfg.listenAddr,
		TLSConfig: serverTLSConfig(cert, caPool),
		Tasks: broker.SocketDirSource{
			Dir:       cfg.socketDir,
			LogDir:    cfg.logDir,
			IsRunning: cfg.taskAlive,
		},
	})
	if err != nil {
		return nil, false, nil, err
	}

	// The node serves exec, logs, files, health, metrics, attach and the
	// experimental snapshots operation. Exec and files dial the task's guest agent
	// over the vsock muxer; logs read the console file the executor wrote; health
	// reads the per-task sidecar the executor publishes; metrics reads the last
	// in-guest sample the daemon's poller cached; attach relays the interactive
	// console Unix socket in both directions; snapshots use the node-local
	// pkg/snapshot store, and restored stops the VM a restore started from that
	// same store. The public route has already resolved the reference and
	// enforced the caller's selectors.
	snapshotOp, restoredOp, snapshotsActive := snapshotHandlers(ctx, cfg)
	handlers := []struct {
		op string
		h  broker.Handler
	}{
		{"exec", broker.NewExecHandler(broker.DialAgent)},
		{"attach", broker.NewAttachHandler(broker.DialConsole)},
		{"logs", broker.NewLogsHandler()},
		{"files", broker.NewFilesHandler(broker.DialAgent)},
		{"health", broker.NewHealthHandler()},
		{"metrics", broker.NewMetricsHandler()},
		{"snapshots", snapshotOp},
		{"restored", restoredOp},
	}
	for _, entry := range handlers {
		if err := srv.Handle(entry.op, entry.h); err != nil {
			return nil, false, nil, err
		}
	}

	lis, err := srv.Listen()
	if err != nil {
		return nil, false, nil, err
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
	return srv, snapshotsActive, stop, nil
}

// snapshotHandlers builds the experimental snapshots and restored-VM operations
// and reports whether their store is usable. Both handlers share one store
// instance: the restored process handle lives in the manager that restored the
// VM, so a second store would answer no_restored_vm for this node's own restores.
// Disabled means registered handlers with no store, which answer a typed 501 —
// the routes still exist. A manager construction failure is logged and degrades
// to the same disabled handlers: a bad snapshot directory must not stop the
// broker, because exec, logs, files and health do not depend on it. The boolean
// is what the daemon passes to the API runtime, so /v1/version never advertises a
// store that does not exist.
func snapshotHandlers(
	ctx context.Context, cfg brokerRuntime,
) (snapshots, restored broker.Handler, active bool) {
	if !cfg.snapshotsEnabled {
		return broker.NewSnapshotHandler(broker.SnapshotHandlerConfig{}),
			broker.NewRestoredHandler(broker.SnapshotHandlerConfig{}), false
	}

	mgr, err := snapshot.NewManager(snapshot.SnapshotConfig{
		Enabled:      true,
		SnapshotDir:  cfg.snapshotDir,
		MaxSnapshots: 3,
		MaxAge:       168 * time.Hour,
	})
	if err != nil {
		log.G(ctx).WithError(err).Warn("Node broker: snapshots disabled (snapshot store unavailable)")
		return broker.NewSnapshotHandler(broker.SnapshotHandlerConfig{}),
			broker.NewRestoredHandler(broker.SnapshotHandlerConfig{}), false
	}

	handlerCfg := broker.SnapshotHandlerConfig{
		Store:     mgr,
		SocketDir: cfg.socketDir,
		MaxAge:    168 * time.Hour,
		RootfsDir: cfg.rootfsDir,
	}
	return broker.NewSnapshotHandler(handlerCfg), broker.NewRestoredHandler(handlerCfg), true
}
