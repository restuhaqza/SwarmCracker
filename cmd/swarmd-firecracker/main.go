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
			Name:    "join-addr",
			Usage:   "Address of manager to join (format: host:port)",
		},
		&cli.StringFlag{
			Name:    "join-token",
			Usage:   "Join token for cluster",
		},
		&cli.StringFlag{
			Name:    "listen-remote-api",
			Usage:   "Listen address for remote API (e.g., 0.0.0.0:4242)",
			Value:   "0.0.0.0:4242",
		},
		&cli.StringFlag{
			Name:    "listen-control-api",
			Usage:   "Path to control API socket",
			Value:   "/var/run/swarmkit/swarm.sock",
		},
		&cli.StringFlag{
			Name:  "advertise-remote-api",
			Usage: "Advertise address for remote API",
			Value: "",
		},
		&cli.StringFlag{
			Name:    "hostname",
			Usage:   "Hostname for this node",
			Value:   "",
		},
		&cli.BoolFlag{
			Name:    "manager",
			Usage:   "Start as manager (agent by default)",
			Value:   false,
		},
		&cli.BoolFlag{
			Name:    "force-new-cluster",
			Usage:   "Force new cluster from current state",
			Value:   false,
		},
		&cli.StringFlag{
			Name:    "kernel-path",
			Usage:   "Path to Firecracker kernel image",
			Value:   "/usr/share/firecracker/vmlinux",
		},
		&cli.StringFlag{
			Name:    "rootfs-dir",
			Usage:   "Directory for container rootfs",
			Value:   "/var/lib/firecracker/rootfs",
		},
		&cli.StringFlag{
			Name:    "socket-dir",
			Usage:   "Directory for Firecracker sockets",
			Value:   "/var/run/firecracker",
		},
		&cli.IntFlag{
			Name:    "default-vcpus",
			Usage:   "Default VCPUs per microVM",
			Value:   1,
		},
		&cli.IntFlag{
			Name:    "default-memory",
			Usage:   "Default memory (MB) per microVM",
			Value:   512,
		},
		&cli.StringFlag{
			Name:    "bridge-name",
			Usage:   "Bridge name for VM networking",
			Value:   "swarm-br0",
		},
		&cli.BoolFlag{
			Name:    "debug",
			Usage:   "Enable debug logging",
			Value:   false,
		},
		&cli.IntFlag{
			Name:    "heartbeat-tick",
			Usage:   "Heartbeat tick in seconds",
			Value:   1,
		},
		&cli.IntFlag{
			Name:    "election-tick",
			Usage:   "Election tick in seconds",
			Value:   10,
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
		Hostname:         hostname,
		StateDir:         stateDir,
		JoinAddr:         ctx.String("join-addr"),
		JoinToken:        ctx.String("join-token"),
		ListenRemoteAPI:  ctx.String("listen-remote-api"),
		ListenControlAPI: ctx.String("listen-control-api"),
		AdvertiseRemoteAPI: ctx.String("advertise-remote-api"),
		Executor:         fcExecutor,
		ForceNewCluster:  ctx.Bool("force-new-cluster"),
		HeartbeatTick:    uint32(ctx.Int("heartbeat-tick")),
		ElectionTick:     uint32(ctx.Int("election-tick")),
		Availability:     api.NodeAvailabilityActive,
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

	// Start node in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := n.Start(ctx); err != nil {
			errChan <- fmt.Errorf("node failed: %w", err)
		} else {
			errChan <- nil
		}
	}()

	log.G(ctx).Infof("Node started successfully (hostname=%s, state-dir=%s)", config.Hostname, config.StateDir)

	// Wait for signal or error
	select {
	case <-sigChan:
		log.G(ctx).Info("Received shutdown signal")
	case err := <-errChan:
		if err != nil {
			return err
		}
	}

	// Graceful shutdown
	log.G(ctx).Info("Shutting down node...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := n.Stop(shutdownCtx); err != nil {
		log.G(ctx).WithError(err).Error("Failed to stop node gracefully")
		return err
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
