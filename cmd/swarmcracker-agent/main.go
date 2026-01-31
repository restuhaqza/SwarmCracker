// Package main provides a simple SwarmCracker agent for testing.
//
// This is a simplified agent that demonstrates the executor integration.
// For production use, integrate with swarmkit's agent package.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/moby/swarmkit/v2/api"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
)

var (
	configPath = flag.String("config", "/etc/swarmcracker/config.yaml", "Path to configuration file")
	debug      = flag.Bool("debug", false, "Enable debug logging")
	version    = flag.Bool("version", false, "Show version information")
)

func main() {
	flag.Parse()

	// Setup logging
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger()

	if *version {
		fmt.Println("SwarmCracker Agent v0.1.0-alpha")
		fmt.Println("SwarmKit Executor Integration")
		os.Exit(0)
	}

	log.Info().Msg("SwarmCracker Agent - SwarmKit Executor")
	log.Info().Msg("This is a demo agent showing executor functionality")

	// Load configuration
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Create executor
	executor, err := swarmkit.NewExecutor(config)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create executor")
	}

	// Test executor
	testExecutor(executor)

	// Wait for signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Info().Msg("Agent ready. Press Ctrl+C to exit")
	<-sigChan

	log.Info().Msg("Shutting down")
}

// loadConfig loads the configuration from a YAML file.
func loadConfig(path string) (*swarmkit.Config, error) {
	// For now, return default config
	// TODO: Implement YAML config loading
	return &swarmkit.Config{
		FirecrackerPath: "firecracker",
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       "/var/lib/firecracker/rootfs",
		SocketDir:       "/var/run/firecracker",
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
		BridgeName:      "swarm-br0",
		Debug:           *debug,
	}, nil
}

// testExecutor tests the executor functionality.
func testExecutor(executor *swarmkit.Executor) {
	ctx := context.Background()

	// Test Describe
	desc, err := executor.Describe(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to describe executor")
		return
	}

	log.Info().
		Str("hostname", desc.Hostname).
		Int64("cpus", desc.Resources.NanoCPUs/1e9).
		Int64("memory_gb", desc.Resources.MemoryBytes/(1024*1024*1024)).
		Msg("Executor capabilities")

	// Test Configure
	node := &api.Node{
		ID: "test-node-1",
		Description: &api.NodeDescription{
			Hostname: desc.Hostname,
		},
	}

	if err := executor.Configure(ctx, node); err != nil {
		log.Error().Err(err).Msg("Failed to configure executor")
	} else {
		log.Info().Msg("Executor configured successfully")
	}

	// Test Controller creation
	task := &api.Task{
		ID:        "test-task-1",
		ServiceID: "test-service-1",
		NodeID:    "test-node-1",
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image:   "nginx:alpine",
					Command: []string{"nginx"},
					Args:    []string{"-g", "daemon off;"},
				},
			},
			Resources: &api.ResourceRequirements{
				Reservations: &api.Resources{
					NanoCPUs:    1e9,  // 1 CPU
					MemoryBytes: 512 * 1024 * 1024, // 512 MB
				},
			},
		},
	}

	ctrl, err := executor.Controller(task)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create controller")
		return
	}

	log.Info().
		Str("task_id", task.ID).
		Str("service_id", task.ServiceID).
		Msg("Controller created successfully")

	// Test Prepare (will fail without Docker/Firecracker, but tests the interface)
	if err := ctrl.Prepare(ctx); err != nil {
		log.Warn().Err(err).Msg("Prepare failed (expected without Docker/Firecracker)")
	} else {
		log.Info().Msg("Prepare succeeded")
	}

	// Test Remove (cleanup)
	if err := ctrl.Remove(ctx); err != nil {
		log.Error().Err(err).Msg("Remove failed")
	} else {
		log.Info().Msg("Remove succeeded")
	}
}
