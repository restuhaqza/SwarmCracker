// swarmd-firecracker - SwarmKit agent with Firecracker executor support
//
// This is a modified SwarmKit agent that integrates SwarmCracker's Firecracker
// microVM executor. It can join any SwarmKit cluster and run tasks as isolated
// microVMs instead of containers.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/moby/swarmkit/v2/node"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

const (
	defaultStateDir  = "/var/lib/swarmkit"
	defaultJoinRetry = 3
)

func main() {
	app := cli.NewApp()
	app.Name = "swarmd-firecracker"
	app.Usage = "SwarmKit agent with Firecracker microVM executor"
	app.Version = "0.1.0"
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
			Usage: "Listen address for remote API (e.g., 0.0.0.0:4242)",
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
			Name:  "debug",
			Usage: "Enable debug logging",
			Value: false,
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

	// Create SwarmCracker executor
	executorConfig := &swarmkit.Config{
		FirecrackerPath: "firecracker",
		KernelPath:      ctx.String("kernel-path"),
		RootfsDir:       ctx.String("rootfs-dir"),
		SocketDir:       ctx.String("socket-dir"),
		DefaultVCPUs:    ctx.Int("default-vcpus"),
		DefaultMemoryMB: ctx.Int("default-memory"),
		BridgeName:      ctx.String("bridge-name"),
		Subnet:          ctx.String("subnet"),
		BridgeIP:        ctx.String("bridge-ip"),
		IPMode:          ctx.String("ip-mode"),
		NATEnabled:      ctx.Bool("nat-enabled"),
		Debug:           ctx.Bool("debug"),
	}

	fcExecutor, err := swarmkit.NewExecutor(executorConfig)
	if err != nil {
		return fmt.Errorf("failed to create Firecracker executor: %w", err)
	}

	log.G(context.Background()).Infof(
		"SwarmCracker executor initialized (kernel=%s, rootfs=%s, bridge=%s)",
		executorConfig.KernelPath,
		executorConfig.RootfsDir,
		executorConfig.BridgeName,
	)

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
		ForceNewCluster:    ctx.Bool("force-new-cluster"),
		HeartbeatTick:      uint32(ctx.Int("heartbeat-tick")),
		ElectionTick:       uint32(ctx.Int("election-tick")),
		Availability:       api.NodeAvailabilityActive,
	}

	// Start node
	if err := startNode(nodeConfig); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	return nil
}

func startNode(config *node.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create node
	n, err := node.New(config)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

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
