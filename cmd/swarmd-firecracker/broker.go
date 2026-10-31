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
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/urfave/cli/v2"
)

// defaultBrokerListen is the broker's cluster-wide port. The manager always
// dials its own fixed port (broker defaultPort) and ignores any port in the
// node's advertised address, so both sides must agree on 4245.
const defaultBrokerListen = "0.0.0.0:4245"

// defaultMigrationStagingDir is the node's migration root: <dir>/<artifactID>/,
// published by an export and written by a stage, served by file and removed by
// discard. It sits beside the snapshot root and the task rootfs root, on the same
// filesystem, so publishing an artifact costs links rather than a second copy of a
// memory image and materialising a received disk is a local operation.
const defaultMigrationStagingDir = "/var/lib/firecracker/migrations"

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
	// snapshotManager is the node's snapshot store, built once by the daemon for
	// every run (see newSnapshotManager) and shared by its two consumers: the
	// broker's experimental snapshot and restored routes, and the executor, which
	// needs it to stop a VM restored from a migration artifact. Nil means the store
	// could not be built.
	snapshotManager *snapshot.Manager
	// snapshotDir is the pkg/snapshot root. pkg/snapshot writes
	// <snapshotDir>/<id>/metadata.json and that directory is the source of truth.
	snapshotDir string
	// migrationStagingDir is the node's migration root, <root>/<artifactID>/:
	// export publishes an artifact there (the manifest plus hard links to the
	// snapshot's files, or copies when the root is on another filesystem), stage
	// receives one there, file serves it, and discard removes it. It sits beside
	// the snapshot and rootfs roots, on the same filesystem, so publishing an
	// artifact costs links rather than a second copy of a memory image.
	migrationStagingDir string
	// bridgeName and vxlanPeers are what the re-anneal operation acts on: the
	// bridge a peer forgets a moved guest from, and the overlay peers whose flood
	// entries it re-adds. They are this node's own configuration, so neither is
	// ever named by a request.
	bridgeName string
	vxlanPeers []string
	// subnet is this node's flat L2 subnet. The re-anneal gives it to
	// network.AttachmentBridgeName so a network carved from the flat segment (the
	// ingress network is, by design) keeps its attachments on bridgeName.
	subnet string
	// networkByID resolves a network ID to the network this node has materialised,
	// which is how the re-anneal derives the bridge a moved guest's attachment was
	// on. Nil means only bridgeName is derivable, which is right for the flat
	// segment and wrong for a user-defined network.
	networkByID func(networkID string) (*types.Network, bool)
	// rootfsDir is the executor's rootfs directory, recorded on a snapshot so
	// restore can tell a missing task disk from a corrupt snapshot.
	rootfsDir string
	// taskAlive decides whether an existing task socket belongs to a live VMM.
	// Nil keeps the stat-only resolution tests use; the daemon sets it to the
	// Firecracker process probe.
	taskAlive func(socketPath string) bool
	// restored is the node's store of restored-instance records: tasks started
	// from a migration artifact run a VM whose TAP device, vsock muxer and disk
	// belong to the source task, and only this record says so. It is built before
	// startNode (see openDaemonState) because opening it can fail. The broker's task
	// resolver is its consumer here: exec, logs, files, attach, health and metrics
	// all resolve through it, and the executor's teardown takes the same store
	// through SetRestoredInstances.
	// Nil means this node keeps no records, which is only true in tests.
	restored *scruntime.RestoredRegistry
	// pending is the node's store of pending-restore directives: the manager's prepare
	// route writes one per service here, and the executor consumes it when SwarmKit
	// starts that service's next task. The daemon opens it once (openDaemonState) so
	// the writer and the consumer are the same directory; nil means startBroker opens
	// its own, which is what a caller that only exercises the broker routes gets.
	pending *scruntime.PendingRestores
}

// logRestoredInstances reports the restored instances this node has recorded,
// once, when the broker comes up. It is the daemon's startup view of the record
// store: after a restart an operator can tell whether the node still maps a
// migrated task to its VM. The wording says "recorded", not "running": the
// record is what the node wrote, and whether the VM behind it is alive is a
// question only resolution can answer.
//
// A record that cannot be used is reported by path and left alone: List skips it
// so one bad file cannot hide the rest, and deleting it would destroy the only
// evidence of what the node believes. A record whose VM is gone is not pruned
// either — resolution owns that answer, because deleting a record that a
// still-starting VM is about to use would break the restore it describes.
func logRestoredInstances(ctx context.Context, restored *scruntime.RestoredRegistry) {
	if restored == nil {
		return
	}

	records, skipped, err := restored.List()
	if err != nil {
		log.G(ctx).WithError(err).Warn("Restored instances: cannot read the record store")
		return
	}
	for _, path := range skipped {
		log.G(ctx).Warnf("Restored instance record %s is unreadable and is ignored, not deleted: inspect it, then remove it if the VM is gone", path)
	}
	if len(records) == 0 {
		return
	}

	log.G(ctx).Infof("Restored instances: %d task(s) recorded", len(records))
	for _, record := range records {
		log.G(ctx).Debugf("restored instance: task %s has the VM of %s recorded (artifact %s)",
			record.TaskID, record.VMID, record.ArtifactID)
	}
}

// brokerRuntimeFrom resolves the broker settings. An empty listen address
// disables the broker, which is useful for a node that should not accept
// manager traffic.
func brokerRuntimeFrom(ctx *cli.Context) brokerRuntime {
	return brokerRuntime{
		listenAddr:          ctx.String("broker-listen"),
		socketDir:           ctx.String("socket-dir"),
		logDir:              scruntime.DefaultLogDir(),
		snapshotsEnabled:    ctx.Bool("snapshots-enable"),
		snapshotDir:         ctx.String("snapshot-dir"),
		migrationStagingDir: ctx.String("migration-staging-dir"),
		rootfsDir:           ctx.String("rootfs-dir"),
		bridgeName:          ctx.String("bridge-name"),
		subnet:              ctx.String("subnet"),
		vxlanPeers:          parseCommaSeparated(ctx.String("vxlan-peers")),
		taskAlive:           taskSocketAlive,
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

// newSnapshotManager builds the node's snapshot store.
//
// It is called for every daemon run, not only when --snapshots-enable is set:
// stopping a task that was restored from a migration artifact needs the manager
// that started its Firecracker process, and that is not the experimental public
// snapshot API. Only the public routes stay behind the flag; the store itself is
// shared. A construction failure is logged and degrades to nil — a bad snapshot
// directory must not stop the broker or the agent from starting, and both
// consumers already answer "unavailable" for a nil store — so this returns no
// error.
func newSnapshotManager(ctx context.Context, cfg brokerRuntime) *snapshot.Manager {
	// The daemon's store is the package's defaults with two overrides: it is
	// enabled (the manager is the node's, not the public API's) and it lives at
	// --snapshot-dir. Retention is deliberately not restated here: the defaults are
	// what a node without a snapshot policy gets, and repeating them in this
	// constructor is how the two drift apart.
	snapshotCfg := snapshot.DefaultSnapshotConfig()
	snapshotCfg.Enabled = true
	snapshotCfg.SnapshotDir = cfg.snapshotDir

	mgr, err := snapshot.NewManager(snapshotCfg)
	if err != nil {
		log.G(ctx).WithError(err).Warn(
			"Snapshot store unavailable: the experimental snapshot API stays disabled and a VM restored from a migration cannot be stopped on this node")
		return nil
	}
	return mgr
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

	// A task restored from a migration artifact runs a VM whose host-side names
	// (TAP device, vsock muxer, disk) belong to the source task, so the broker
	// resolves it from its record instead of deriving a path from its ID. The
	// source is built through the constructor rather than as a literal because it
	// also latches the one warning a stale record produces: resolution runs for
	// every exec, logs and health request.
	//
	// The field is an interface: assigning a nil registry would make it non-nil and
	// every lookup would fail, so it is converted only when the daemon opened the
	// store.
	var restored broker.RestoredInstanceSource
	if cfg.restored != nil {
		restored = cfg.restored
	}
	tasks := broker.NewSocketDirSource(cfg.socketDir, cfg.logDir, cfg.taskAlive, restored)

	srv, err := broker.NewServer(broker.ServerConfig{
		Addr:      cfg.listenAddr,
		TLSConfig: serverTLSConfig(cert, caPool),
		Tasks:     tasks,
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
	// same manager instance — the one the executor also holds, so the two cannot
	// disagree about which processes this node owns. The migration operations
	// produce an artifact from a running task,
	// stream one of its files, stage one the target will restore, discard one
	// when it is no longer needed, and record (or remove) the directive that tells
	// this node which artifact the next task of a service restores here; they share
	// the snapshot store export snapshots through and the one migration root for the
	// bytes. The public route has already resolved the reference and enforced the
	// caller's selectors.
	snapshotOp, restoredOp, store, snapshotsActive := snapshotHandlers(cfg)
	// A migration's target is told what to restore before the task that restores it
	// exists, so the directive lives under the daemon's state directory, beside the
	// restored instances the same node writes: the manager prepares it once the
	// artifact is staged, SwarmKit creates the task later, and the executor consumes
	// it at task start. Opening the store can fail, and a node that cannot keep a
	// directive must not answer a prepare as though it had, so the failure stops the
	// broker rather than leaving a route that lies.
	//
	// The daemon opens the store before the broker (openDaemonState) and hands it over,
	// because the executor's task start consumes the same directives; opening a second
	// one here is only for a caller that runs the broker on its own.
	pending := cfg.pending
	if pending == nil {
		opened, err := scruntime.NewPendingRestores(scruntime.PendingRestoreDir(stateDir))
		if err != nil {
			return nil, false, nil, fmt.Errorf("node broker: %w", err)
		}
		pending = opened
	}
	migrationCfg := broker.MigrationHandlerConfig{
		Store:       store,
		ArtifactDir: cfg.migrationStagingDir,
		RootfsDir:   cfg.rootfsDir,
		Pending:     pending,
		// The re-anneal acts on this node's own L2 segment, so it is given this
		// node's bridge and peers rather than reading them from a request. A guest
		// on a user-defined network was never on that bridge, so the network lookup
		// and the flat subnet are what let the node derive the right device.
		BridgeName:  cfg.bridgeName,
		Subnet:      cfg.subnet,
		NetworkByID: cfg.networkByID,
		VXLANPeers:  cfg.vxlanPeers,
	}
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
		{broker.MigrationExportOperation, broker.NewMigrationExportHandler(migrationCfg)},
		{broker.MigrationFileOperation, broker.NewMigrationFileHandler(migrationCfg)},
		{broker.MigrationStageOperation, broker.NewMigrationStageHandler(migrationCfg)},
		{broker.MigrationDiscardOperation, broker.NewMigrationDiscardHandler(migrationCfg)},
		{broker.MigrationPrepareOperation, broker.NewMigrationPrepareHandler(migrationCfg)},
		{broker.MigrationUnprepareOperation, broker.NewMigrationUnprepareHandler(migrationCfg)},
		{broker.MigrationReannealOperation, broker.NewMigrationReannealHandler(migrationCfg)},
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
// and reports the store they share, plus whether it is usable. Both handlers take
// one store instance — the one the daemon built and handed to the executor as well
// (newSnapshotManager): the restored process handle lives in the manager that
// restored the VM, so a second store would answer no_restored_vm for this node's
// own restores, and the executor's stop would reach a process the broker's route
// could not. The store is returned as well because the migration operations
// snapshot through it and move the files it publishes; a nil store means the
// migration export answers its typed 501 while file and stage keep working.
// Disabled means registered handlers with no store, which answer a typed 501 —
// the routes still exist, so a caller gets the capability detail rather than the
// server's unregistered-operation answer. The boolean is what the daemon passes to
// the API runtime, so /v1/version never advertises a store that does not exist:
// the flag and a usable store are both required.
func snapshotHandlers(
	cfg brokerRuntime,
) (snapshots, restored broker.Handler, store broker.SnapshotStore, active bool) {
	// The store is the node's snapshot manager, whether or not the experimental
	// public snapshot API is enabled: live migration exports through it, and a node
	// with the API off must still be able to move a task off itself. The two
	// handlers stay gated below - only the capability the flag controls is.
	if cfg.snapshotManager == nil {
		return broker.NewSnapshotHandler(broker.SnapshotHandlerConfig{}),
			broker.NewRestoredHandler(broker.SnapshotHandlerConfig{}), nil, false
	}
	if !cfg.snapshotsEnabled {
		return broker.NewSnapshotHandler(broker.SnapshotHandlerConfig{}),
			broker.NewRestoredHandler(broker.SnapshotHandlerConfig{}), cfg.snapshotManager, false
	}

	handlerCfg := broker.SnapshotHandlerConfig{
		Store:     cfg.snapshotManager,
		SocketDir: cfg.socketDir,
		MaxAge:    168 * time.Hour,
		RootfsDir: cfg.rootfsDir,
		// The stop route deletes the record once the VM is gone, under the same
		// rule as the executor's teardown. Without it a stopped VM would stay
		// resolvable for as long as its record lived.
		Records: cfg.restored,
	}
	return broker.NewSnapshotHandler(handlerCfg), broker.NewRestoredHandler(handlerCfg), cfg.snapshotManager, true
}
