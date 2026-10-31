package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
	"github.com/moby/swarmkit/v2/node"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/restuhaqza/swarmcracker/pkg/cni"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/health"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"github.com/urfave/cli/v2"
)

// restoredStore opens the node's restored-instance record store under the
// daemon's state directory. The directory comes from --state-dir, the same value
// the SwarmKit node and the cluster certificates use, so the records sit beside
// the node's other state and a daemon restart finds them without configuration.
func restoredStore(stateDir string) (*runtime.RestoredRegistry, error) {
	dir := runtime.RestoredInstanceDir(stateDir)
	if dir == "" {
		return nil, fmt.Errorf("a state directory is required to keep restored-instance records: pass --state-dir")
	}
	return runtime.NewRestoredRegistry(dir)
}

// pendingRestores opens the node's pending-restore directive store under the daemon's
// state directory: <state-dir>/pending-restores, one directive per service. The
// directory comes from --state-dir for the same reason the restored-instance records
// do — a directive written by the process that accepted a prepare must be visible to
// the executor that consumes it, and a daemon restart must find what it wrote.
func pendingRestores(stateDir string) (*runtime.PendingRestores, error) {
	dir := runtime.PendingRestoreDir(stateDir)
	if dir == "" {
		return nil, fmt.Errorf("a state directory is required to keep pending-restore directives: pass --state-dir")
	}
	return runtime.NewPendingRestores(dir)
}

// openDaemonState creates the daemon's state directory and opens the node-local record
// stores that live under it. Every failure is fatal to startup: a node that cannot
// write its state directory cannot join a cluster, a node that cannot keep a
// restored-instance record could restore a VM it would later fail to resolve, and a node
// that cannot keep a pending-restore directive must not answer a prepare as though it had.
func openDaemonState(ctx *cli.Context) (string, *runtime.RestoredRegistry, *runtime.PendingRestores, error) {
	stateDir := ctx.String("state-dir")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return "", nil, nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	restored, err := restoredStore(stateDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to open the restored-instance record store: %w", err)
	}

	pending, err := pendingRestores(stateDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to open the pending-restore directive store: %w", err)
	}
	return stateDir, restored, pending, nil
}

// wireRestoredInstances gives the executor the node's restored-instance records and
// the snapshot manager that owns the Firecracker processes they name.
//
// Both are needed to end a task that was restored from a migration artifact: the
// record says which VM the task runs, and only the manager that started that VM can
// stop it. A nil manager (its construction failed, and that was already logged) is
// passed as a nil interface, never as a typed nil: a typed nil would not be "no
// stopper" but an interface value that panics when called.
func wireRestoredInstances(executor *swarmkit.Executor, store *runtime.RestoredRegistry, mgr *snapshot.Manager) {
	var stopper swarmkit.RestoredVMStopper
	if mgr != nil {
		stopper = mgr
	}
	executor.SetRestoredInstances(store, stopper)
}

// wireMigrationRestores gives the executor what a task start needs to restore the
// artifact a directive names instead of booting a fresh VM: the directive store the
// broker's prepare route also writes, the node's migration root, and the snapshot
// manager that loads the artifact and owns the process it starts.
//
// The store is the same instance the broker holds, and the manager is the same instance
// the executor's teardown stops restored VMs through: a second copy of either would make
// the task start and the task's own stop disagree about which process and which
// directive they are looking at. A node whose snapshot store could not be built (mgr is
// nil) gets no wiring at all: it consumes no directive, so its tasks boot cold rather
// than claiming an artifact it cannot load.
func wireMigrationRestores(
	executor *swarmkit.Executor,
	pending *runtime.PendingRestores,
	cfg brokerRuntime,
	mgr *snapshot.Manager,
) {
	if executor == nil || pending == nil || mgr == nil {
		return
	}
	executor.SetMigrationRestores(swarmkit.MigrationRestoreConfig{
		Pending:  pending,
		Root:     cfg.migrationStagingDir,
		Restorer: swarmkit.NewArtifactRestorer(mgr),
	})
}

func runAgent(ctx *cli.Context) error {
	// Setup logging
	setupLogging(ctx)

	// Get hostname
	hostname := ctx.String("hostname")
	if hostname == "" {
		var err error
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("failed to get hostname: %w", err)
		}
	}

	// Create the state directory and open the node-local record stores that live
	// under it.
	stateDir, restored, pending, err := openDaemonState(ctx)
	if err != nil {
		return err
	}

	// Ensure default config exists (auto-generate on first run)
	if created, err := config.EnsureDefaultConfig(); err != nil {
		log.G(context.Background()).Warnf("Failed to create default config: %v (continuing with CLI flags)", err)
	} else if created {
		log.G(context.Background()).Infof("Default config created at %s", config.GetDefaultConfigPath())
	}

	// Optional OpenTelemetry tracing, off by default: with it disabled no
	// exporter exists and nothing is dialled.
	tracerProvider, err := startTracing(ctx)
	if err != nil {
		return fmt.Errorf("failed to configure tracing: %w", err)
	}
	defer shutdownTracing(tracerProvider)

	// Create SwarmCracker executor
	natEnabled := ctx.Bool("nat-enabled")
	executorConfig := &swarmkit.Config{
		FirecrackerPath: "firecracker",
		Tracer:          tracerProvider.Tracer(),
		KernelPath:      ctx.String("kernel-path"),
		RootfsDir:       ctx.String("rootfs-dir"),
		GoldenDir:       ctx.String("golden-dir"),
		Hostname:        hostname,
		JoinAddr:        ctx.String("join-addr"),
		AdvertiseAddr:   ctx.String("advertise-remote-api"), // For managers, use advertise address
		ConsulEnabled:   ctx.Bool("consul-enabled"),
		ConsulAddress:   ctx.String("consul-address"),
		SocketDir:       ctx.String("socket-dir"),
		DefaultVCPUs:    ctx.Int("default-vcpus"),
		DefaultMemoryMB: ctx.Int("default-memory"),
		BridgeName:      ctx.String("bridge-name"),
		Subnet:          ctx.String("subnet"),
		BridgeIP:        ctx.String("bridge-ip"),
		IPMode:          ctx.String("ip-mode"),
		NATEnabled:      &natEnabled,
		VXLANEnabled:    ctx.Bool("vxlan-enabled"),
		VXLANPeers:      parseCommaSeparated(ctx.String("vxlan-peers")),
		// Consul service discovery
		Debug:    ctx.Bool("debug"),
		StateDir: stateDir,
		// Jailer configuration
		EnableJailer:    ctx.Bool("enable-jailer"),
		JailerPath:      ctx.String("jailer-path"),
		JailerUID:       ctx.Int("jailer-uid"),
		JailerGID:       ctx.Int("jailer-gid"),
		JailerChrootDir: ctx.String("jailer-chroot-dir"),
		ParentCgroup:    ctx.String("parent-cgroup"),
		CgroupVersion:   ctx.String("cgroup-version"),
		EnableCgroups:   ctx.Bool("enable-cgroups"),
	}

	fcExecutor, err := swarmkit.NewExecutor(executorConfig)
	if err != nil {
		return fmt.Errorf("failed to create Firecracker executor: %w", err)
	}

	log.G(context.Background()).Infof(
		"SwarmCracker executor initialized (kernel=%s, rootfs=%s, bridge=%s, vxlan=%v, jailer=%v)",
		executorConfig.KernelPath,
		executorConfig.RootfsDir,
		executorConfig.BridgeName,
		executorConfig.VXLANEnabled,
		executorConfig.EnableJailer,
	)

	if len(executorConfig.VXLANPeers) > 0 {
		log.G(context.Background()).Infof("VXLAN peers configured: %v", executorConfig.VXLANPeers)
	} else if executorConfig.VXLANEnabled {
		log.G(context.Background()).Warn("VXLAN enabled but no peers configured — " +
			"use --vxlan-peers for static peers or --consul-enabled for dynamic discovery")
		log.G(context.Background()).Info("For small clusters (≤5 nodes), static peers are sufficient:\n" +
			"  --vxlan-peers 192.168.1.11,192.168.1.12")
	}

	// Start health + metrics server
	healthChecker := health.NewChecker(ctx.String("bridge-name"), "firecracker")
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/healthz", healthChecker)
		mux.Handle("/metrics", promhttp.Handler())
		// Live per-task health, aggregated cluster-wide by `service ps`/`task ls`.
		mux.Handle(runtime.TaskHealthPath, taskHealthHandler(ctx.String("socket-dir")))
		healthAddr := ctx.String("health-addr")

		// Wrap with security middleware: rate limiting + request timeout
		handler := withRequestTimeout(withRateLimit(mux, 10, 20), 5*time.Second)

		server := &http.Server{
			Addr:         healthAddr,
			Handler:      handler,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  30 * time.Second,
		}

		log.G(context.Background()).Infof("Starting health check server on %s", healthAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.G(context.Background()).WithError(err).Warn("Health check server failed")
		}
	}()

	// Create CNI network provider if enabled
	var networkProvider networkallocator.Provider
	var networkConfig *networkallocator.Config

	if ctx.Bool("enable-cni") {
		// The predefined networks (ingress, docker_gwbridge) are carved from a pool
		// that defaults to the flat L2 subnet, so their task IPs stay reachable
		// cluster-wide. User-defined networks are carved from a pool of their own, so
		// an auto-allocated network is never handed the flat L2 subnet and put on the
		// same addresses as the default network (#77).
		subnetPool := ctx.String("cni-subnet-pool")
		if subnetPool == "" {
			subnetPool = ctx.String("subnet")
		}
		if subnetPool == "" {
			subnetPool = cni.DefaultSubnetPool
		}
		overlayPool := ctx.String("cni-overlay-subnet-pool")
		if overlayPool == "" {
			overlayPool = cni.DefaultSubnetPool
		}

		cniConfig := &cni.CNIConfig{
			BridgeName:        ctx.String("bridge-name"),
			SubnetPool:        subnetPool,
			OverlaySubnetPool: overlayPool,
			FlatSubnet:        ctx.String("subnet"),
			SubnetSize:        ctx.Int("cni-subnet-size"),
			VXLANPort:         uint32(ctx.Int("cni-vxlan-port")),
			IPAMType:          "host-local",
			PluginDir:         ctx.String("cni-plugin-dir"),
			ConfigDir:         ctx.String("cni-config-dir"),
			EnableIPMasq:      true,
		}

		cniProvider, err := cni.NewCNIProvider(cniConfig)
		if err != nil {
			// Degrade gracefully: the daemon can still run workloads, but
			// SwarmKit task network allocation will be unavailable until the
			// CNI plugins are installed.
			log.G(context.Background()).WithError(err).Warn(
				"CNI network provider unavailable; continuing without network allocation (install CNI plugins in " + cniConfig.PluginDir + ")")
		} else {
			networkProvider = cniProvider
			networkConfig = &networkallocator.Config{
				DefaultAddrPool: []string{cniConfig.SubnetPool},
				SubnetSize:      uint32(cniConfig.SubnetSize),
				VXLANUDPPort:    cniConfig.VXLANPort,
			}

			log.G(context.Background()).Infof(
				"CNI network provider enabled (plugin-dir=%s, config-dir=%s, pool=%s, overlay-pool=%s, vxlan-port=%d)",
				cniConfig.PluginDir,
				cniConfig.ConfigDir,
				cniConfig.SubnetPool,
				cniConfig.OverlaySubnetPool,
				cniConfig.VXLANPort,
			)
		}
	} else {
		log.G(context.Background()).Warn("CNI network provider NOT enabled - worker nodes will fail to join")
		log.G(context.Background()).Warn("Use --enable-cni flag to enable network allocation")
	}

	// Create node config
	nodeConfig := &node.Config{
		Hostname:           hostname,
		StateDir:           stateDir,
		JoinAddr:           ctx.String("join-addr"),
		JoinToken:          ctx.String("join-token"),
		ListenRemoteAPI:    ctx.String("listen-remote-api"),
		ListenControlAPI:   ctx.String("listen-control-api"),
		AdvertiseRemoteAPI: ctx.String("advertise-remote-api"),
		Executor:           fcExecutor,
		NetworkProvider:    networkProvider, // ← CNI Provider
		NetworkConfig:      networkConfig,   // ← Network config
		ForceNewCluster:    ctx.Bool("force-new-cluster"),
		HeartbeatTick:      uint32(ctx.Int("heartbeat-tick")),
		ElectionTick:       uint32(ctx.Int("election-tick")),
		Availability:       api.NodeAvailabilityActive,
	}

	// Ingress routing mesh: managers serve the routing table and balance
	// locally; workers fetch the table from the manager they joined through.
	ingressPort := ctx.Int("ingress-port")
	ingressManager := ""
	if joinAddr := ctx.String("join-addr"); joinAddr != "" {
		host := joinAddr
		if h, _, err := net.SplitHostPort(joinAddr); err == nil {
			host = h
		}
		ingressManager = net.JoinHostPort(host, strconv.Itoa(ingressPort))
	}
	ingressCfg := ingressRuntime{
		enabled:     ctx.Bool("ingress-mesh"),
		port:        ingressPort,
		managerAddr: ingressManager,
		socketDir:   ctx.String("socket-dir"),
	}

	// Start node
	brk := brokerRuntimeFrom(ctx)
	brk.restored = restored
	// The directive store is opened once for the daemon: the broker's prepare route
	// writes it and the executor's task start consumes it, so both must address the
	// same directory.
	brk.pending = pending
	// The snapshot store is built for every daemon, not only under
	// --snapshots-enable: its manager owns the process of a VM restored from a
	// migration, so both the executor's stop and the broker's routes need the same
	// instance. Only the public snapshot routes stay gated by the flag.
	brk.snapshotManager = newSnapshotManager(context.Background(), brk)
	wireRestoredInstances(fcExecutor, restored, brk.snapshotManager)
	// A task whose service the manager prepared a directive for restores the staged
	// artifact instead of booting, through the same manager and the same state
	// directory the rest of the node uses.
	wireMigrationRestores(fcExecutor, pending, brk, brk.snapshotManager)
	if err := startNode(nodeConfig, fcExecutor, ingressCfg, apiRuntimeFrom(ctx), brk); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	return nil
}
