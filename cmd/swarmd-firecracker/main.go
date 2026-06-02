// swarmd-firecracker - SwarmKit agent with Firecracker executor support
//
// This is a modified SwarmKit agent that integrates SwarmCracker's Firecracker
// microVM executor. It can join any SwarmKit cluster and run tasks as isolated
// microVMs instead of containers.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/manager/allocator/networkallocator"
	"github.com/moby/swarmkit/v2/node"
	"github.com/restuhaqza/swarmcracker/pkg/cni"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/health"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	// Version is set by build flags (-X main.Version=...)
	Version = "0.0.0-dev"
)

const (
	defaultStateDir  = "/var/lib/swarmkit"
	defaultJoinRetry = 3
)

func main() {
	app := cli.NewApp()
	app.Name = "swarmd-firecracker"
	app.Usage = "SwarmKit agent with Firecracker microVM executor"
	app.Version = Version
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "state-dir",
			Aliases: []string{"d"},
			Usage:   "State directory for swarmd",
			Value:   defaultStateDir,
		},
		&cli.StringFlag{
			Name:  "join-addr",
			Usage: "Address of manager to join (format: host:port)",
		},
		&cli.StringFlag{
			Name:  "join-token",
			Usage: "Join token for cluster",
		},
		&cli.StringFlag{
			Name:  "listen-remote-api",
			Usage: "Listen address for remote API (use 127.0.0.1:4242 for worker-only nodes)",
			Value: "0.0.0.0:4242",
		},
		&cli.StringFlag{
			Name:  "listen-control-api",
			Usage: "Path to control API socket",
			Value: "/var/run/swarmkit/swarm.sock",
		},
		&cli.StringFlag{
			Name:  "advertise-remote-api",
			Usage: "Advertise address for remote API",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "hostname",
			Usage: "Hostname for this node",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "manager",
			Usage: "Start as manager (agent by default)",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  "force-new-cluster",
			Usage: "Force new cluster from current state",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "kernel-path",
			Usage: "Path to Firecracker kernel image",
			Value: "/usr/share/firecracker/vmlinux",
		},
		&cli.StringFlag{
			Name:  "rootfs-dir",
			Usage: "Directory for container rootfs",
			Value: "/var/lib/firecracker/rootfs",
		},
		&cli.StringFlag{
			Name:  "socket-dir",
			Usage: "Directory for Firecracker sockets",
			Value: "/var/run/firecracker",
		},
		&cli.IntFlag{
			Name:  "default-vcpus",
			Usage: "Default VCPUs per microVM",
			Value: 1,
		},
		&cli.IntFlag{
			Name:  "default-memory",
			Usage: "Default memory (MB) per microVM",
			Value: 512,
		},
		&cli.StringFlag{
			Name:  "bridge-name",
			Usage: "Bridge name for VM networking",
			Value: "swarm-br0",
		},
		&cli.StringFlag{
			Name:  "subnet",
			Usage: "Subnet for VM IP allocation",
			Value: "192.168.127.0/24",
		},
		&cli.StringFlag{
			Name:  "bridge-ip",
			Usage: "Bridge IP address",
			Value: "192.168.127.1/24",
		},
		&cli.StringFlag{
			Name:  "ip-mode",
			Usage: "IP allocation mode",
			Value: "static",
		},
		&cli.BoolFlag{
			Name:  "nat-enabled",
			Usage: "Enable NAT for internet access",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "vxlan-enabled",
			Usage: "Enable VXLAN overlay for cross-node VM networking",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "vxlan-peers",
			Usage: "Comma-separated list of VXLAN peer worker IPs (e.g., 192.168.56.12,192.168.56.13)",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "Enable debug logging",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "health-addr",
			Usage: "Address for health check HTTP server",
			Value: "127.0.0.1:8080",
		},
		&cli.BoolFlag{
			Name:  "enable-jailer",
			Usage: "Enable Firecracker jailer for enhanced security isolation",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "jailer-path",
			Usage: "Path to jailer binary",
			Value: "/usr/local/bin/jailer",
		},
		&cli.IntFlag{
			Name:  "jailer-uid",
			Usage: "UID to run jailed Firecracker processes (default: 1000)",
			Value: 1000,
		},
		&cli.IntFlag{
			Name:  "jailer-gid",
			Usage: "GID to run jailed Firecracker processes (default: 1000)",
			Value: 1000,
		},
		&cli.StringFlag{
			Name:  "jailer-chroot-dir",
			Usage: "Base directory for jailer chroots",
			Value: "/var/lib/swarmcracker/jailer",
		},
		&cli.StringFlag{
			Name:  "parent-cgroup",
			Usage: "Parent cgroup for jailer VMs (e.g., firecracker)",
			Value: "firecracker",
		},
		&cli.StringFlag{
			Name:  "cgroup-version",
			Usage: "Cgroup version: v1 or v2 (default: auto-detect)",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "enable-cgroups",
			Usage: "Enable cgroup resource limits (requires jailer)",
			Value: true,
		},
		// CNI Network Provider flags
		&cli.BoolFlag{
			Name:  "enable-cni",
			Usage: "Enable CNI network provider for SwarmKit network allocation",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "cni-plugin-dir",
			Usage: "Directory containing CNI plugin binaries",
			Value: cni.DefaultPluginDir,
		},
		&cli.StringFlag{
			Name:  "cni-config-dir",
			Usage: "Directory for CNI network configurations",
			Value: cni.DefaultConfigDir,
		},
		&cli.StringFlag{
			Name:  "cni-subnet-pool",
			Usage: "IP pool for CNI network allocation",
			Value: cni.DefaultSubnetPool,
		},
		&cli.IntFlag{
			Name:  "cni-subnet-size",
			Usage: "Subnet size for CNI networks",
			Value: cni.DefaultSubnetSize,
		},
		&cli.IntFlag{
			Name:  "cni-vxlan-port",
			Usage: "VXLAN UDP port for overlay networks",
			Value: cni.DefaultVXLANPort,
		},
		// Consul service discovery
		// Consul service discovery
		&cli.BoolFlag{
			Name:  "consul-enabled",
			Usage: "Enable Consul service discovery for VXLAN peers",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "consul-address",
			Usage: "Consul agent address",
			Value: "127.0.0.1:8500",
		},
		&cli.IntFlag{
			Name:  "heartbeat-tick",
			Usage: "Heartbeat tick in seconds",
			Value: 1,
		},
		&cli.IntFlag{
			Name:  "election-tick",
			Usage: "Election tick in seconds",
			Value: 10,
		},
	}
	app.Action = runAgent

	if err := app.Run(os.Args); err != nil {
		log.L.Fatalf("%v", err)
	}
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

	// Create state directory
	stateDir := ctx.String("state-dir")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Ensure default config exists (auto-generate on first run)
	if created, err := config.EnsureDefaultConfig(); err != nil {
		log.G(context.Background()).Warnf("Failed to create default config: %v (continuing with CLI flags)", err)
	} else if created {
		log.G(context.Background()).Infof("Default config created at %s", config.GetDefaultConfigPath())
	}

	// Create SwarmCracker executor
	natEnabled := ctx.Bool("nat-enabled")
	executorConfig := &swarmkit.Config{
		FirecrackerPath: "firecracker",
		KernelPath:      ctx.String("kernel-path"),
		RootfsDir:       ctx.String("rootfs-dir"),
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
	}

	// Start health check server
	healthChecker := health.NewChecker(ctx.String("bridge-name"), "firecracker")
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/healthz", healthChecker)
		healthAddr := ctx.String("health-addr")
		log.G(context.Background()).Infof("Starting health check server on %s", healthAddr)
		if err := http.ListenAndServe(healthAddr, mux); err != nil {
			log.G(context.Background()).WithError(err).Warn("Health check server failed")
		}
	}()

	// Create CNI network provider if enabled
	var networkProvider networkallocator.Provider
	var networkConfig *networkallocator.Config

	if ctx.Bool("enable-cni") {
		cniConfig := &cni.CNIConfig{
			BridgeName:   ctx.String("bridge-name"),
			SubnetPool:   ctx.String("cni-subnet-pool"),
			SubnetSize:   ctx.Int("cni-subnet-size"),
			VXLANPort:    uint32(ctx.Int("cni-vxlan-port")),
			IPAMType:     "host-local",
			PluginDir:    ctx.String("cni-plugin-dir"),
			ConfigDir:    ctx.String("cni-config-dir"),
			EnableIPMasq: true,
		}

		cniProvider, err := cni.NewCNIProvider(cniConfig)
		if err != nil {
			return fmt.Errorf("failed to create CNI provider: %w", err)
		}

		networkProvider = cniProvider
		networkConfig = &networkallocator.Config{
			DefaultAddrPool: []string{cniConfig.SubnetPool},
			SubnetSize:      uint32(cniConfig.SubnetSize),
			VXLANUDPPort:    cniConfig.VXLANPort,
		}

		log.G(context.Background()).Infof(
			"CNI network provider enabled (plugin-dir=%s, config-dir=%s, pool=%s, vxlan-port=%d)",
			cniConfig.PluginDir,
			cniConfig.ConfigDir,
			cniConfig.SubnetPool,
			cniConfig.VXLANPort,
		)
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

	// Start node
	if err := startNode(nodeConfig, fcExecutor); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	return nil
}

// parseCommaSeparated parses a comma-separated string into a slice.
func parseCommaSeparated(s string) []string {
	if s == "" {
		return []string{}
	}
	result := []string{}
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// checkAndMigrateClusterAddress checks if the stored cluster address matches
// the desired advertise address. If not, it clears the cluster state to allow
// a fresh start with the new address.
func checkAndMigrateClusterAddress(stateDir, advertiseAddr string) error {
	stateFile := filepath.Join(stateDir, "state.json")

	// Read existing state
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No existing state, nothing to migrate
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	// Parse the state to extract the address
	// The state.json format is: [{"node_id":"...","addr":"..."}]
	type peerEntry struct {
		NodeID string `json:"node_id"`
		Addr   string `json:"addr"`
	}
	var peers []peerEntry
	if err := json.Unmarshal(data, &peers); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	// Check if any peer has a wildcard address that needs migration
	needsMigration := false
	for _, peer := range peers {
		if peer.Addr != "" && strings.HasPrefix(peer.Addr, "0.0.0.0:") {
			needsMigration = true
			fmt.Printf("Detected wildcard address %s, migrating to %s\n", peer.Addr, advertiseAddr)
			break
		}
	}

	if needsMigration {
		// Clear the entire state directory to force a fresh cluster
		// This is necessary because the raft snapshot contains the old address
		// and the CA signer is stored in the raft encrypted state
		fmt.Printf("Clearing cluster state to allow address migration\n")

		// Remove raft state
		raftDir := filepath.Join(stateDir, "raft")
		if err := os.RemoveAll(raftDir); err != nil {
			fmt.Printf("Warning: failed to remove raft state: %v\n", err)
		}

		// Remove certificates to force fresh CA generation
		certDir := filepath.Join(stateDir, "certificates")
		if err := os.RemoveAll(certDir); err != nil {
			fmt.Printf("Warning: failed to remove certificates: %v\n", err)
		}

		// Remove state.json
		if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: failed to remove state file: %v\n", err)
		}

		// Remove worker state
		workerDir := filepath.Join(stateDir, "worker")
		if err := os.RemoveAll(workerDir); err != nil {
			fmt.Printf("Warning: failed to remove worker state: %v\n", err)
		}

		fmt.Printf("Cluster state cleared. A new cluster will be created.\n")
	}

	return nil
}

func startNode(config *node.Config, executor *swarmkit.Executor) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// If this is a manager with a new advertise address, check if we need to
	// clear the old cluster state to use the new address.
	if config.AdvertiseRemoteAPI != "" && config.JoinAddr == "" {
		if err := checkAndMigrateClusterAddress(config.StateDir, config.AdvertiseRemoteAPI); err != nil {
			log.G(ctx).WithError(err).Warn("Failed to check/migrate cluster address")
		}
	}

	// Create node
	n, err := node.New(config)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Start the node - this is NON-BLOCKING in SwarmKit
	// It returns immediately after starting internal goroutines
	if err := n.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	log.G(ctx).Infof("Node started successfully (hostname=%s, state-dir=%s)", config.Hostname, config.StateDir)

	// Wait for node to be ready (optional but good for debugging)
	select {
	case <-n.Ready():
		log.G(ctx).Info("Node is ready")
	case <-time.After(30 * time.Second):
		log.G(ctx).Warn("Node readiness check timeout (continuing anyway)")
	case <-ctx.Done():
		return fmt.Errorf("context canceled before node was ready")
	}

	// If this is a manager, generate and print join tokens for workers
	if config.JoinAddr == "" {
		printJoinTokens(ctx, config.StateDir)
	}

	// Wait for shutdown signal
	sig := <-sigChan
	log.G(ctx).WithField("signal", sig).Info("Received shutdown signal")

	// Stop the node gracefully
	log.G(ctx).Info("Stopping node...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := n.Stop(shutdownCtx); err != nil {
		log.G(ctx).WithError(err).Error("Failed to stop node gracefully")
		return err
	}

	// Close executor (cleanup dnsmasq, VXLAN)
	if err := executor.Close(); err != nil {
		log.G(ctx).WithError(err).Warn("Failed to close executor")
	}

	// Check if node stopped with any error
	if err := n.Err(ctx); err != nil {
		return fmt.Errorf("node stopped with error: %w", err)
	}

	log.G(ctx).Info("Node stopped successfully")
	return nil
}

func setupLogging(ctx *cli.Context) {
	// Setup SwarmKit logging (uses logrus)
	level := logrus.InfoLevel
	if ctx.Bool("debug") {
		level = logrus.DebugLevel
	}
	logrus.SetLevel(level)
	logrus.SetOutput(os.Stderr)
	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})
}

// printJoinTokens reads the actual join tokens from the cluster's Raft store
// via the local control API socket using the node's own TLS certificates.
func printJoinTokens(ctx context.Context, stateDir string) {
	socketPath := "/var/run/swarmkit/swarm.sock"

	// Load the node's TLS certificates for mTLS to the control socket
	certDir := filepath.Join(stateDir, "certificates")
	certFile := filepath.Join(certDir, "swarm-node.crt")
	keyFile := filepath.Join(certDir, "swarm-node.key")
	caFile := filepath.Join(certDir, "swarm-root-ca.crt")

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to load node TLS certificate for token retrieval")
		return
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to read root CA certificate")
		return
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: true, // Unix socket, no hostname to verify
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithContextDialer(func(dialCtx context.Context, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		}),
		grpc.WithBlock(),
	}

	conn, err := grpc.Dial("unix://"+socketPath, dialOpts...)
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to connect to control API for token retrieval")
		return
	}
	defer conn.Close()

	client := api.NewControlClient(conn)
	listCtx, listCancel := context.WithTimeout(ctx, 10*time.Second)
	defer listCancel()

	resp, err := client.ListClusters(listCtx, &api.ListClustersRequest{})
	if err != nil {
		log.G(ctx).WithError(err).Warn("Failed to list clusters for token retrieval")
		return
	}

	for _, cluster := range resp.Clusters {
		workerToken := cluster.RootCA.JoinTokens.Worker
		managerToken := cluster.RootCA.JoinTokens.Manager

		log.G(ctx).Infof("========================================")
		log.G(ctx).Infof("CLUSTER JOIN TOKENS")
		log.G(ctx).Infof("========================================")
		log.G(ctx).Debugf("Worker token: %s", workerToken)   // DEBUG level to avoid exposing token in logs
		log.G(ctx).Debugf("Manager token: %s", managerToken) // DEBUG level to avoid exposing token in logs
		log.G(ctx).Infof("========================================")

		// Write tokens to a file
		tokenFile := filepath.Join(stateDir, "join-tokens.txt")
		tokenContent := fmt.Sprintf("WORKER_TOKEN=%s\nMANAGER_TOKEN=%s\n", workerToken, managerToken)
		if err := os.WriteFile(tokenFile, []byte(tokenContent), 0600); err != nil {
			log.G(ctx).WithError(err).Warnf("Failed to write join tokens to %s", tokenFile)
		} else {
			log.G(ctx).Infof("Join tokens saved to %s", tokenFile)
		}
	}
}
