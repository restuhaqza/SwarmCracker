// Package main provides the SwarmCracker agent binary for SwarmKit integration.
//
// This binary registers SwarmCracker as a custom executor with SwarmKit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/moby/swarmkit/v2/agent"
	"github.com/moby/swarmkit/v2/agent/exec"
	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/restuhaqza/swarmcracker/pkg/swarmkit"
	"gopkg.in/yaml.v3"
)

var (
	configPath  = flag.String("config", "/etc/swarmcracker/config.yaml", "Path to configuration file")
	debug       = flag.Bool("debug", false, "Enable debug logging")
	version     = flag.Bool("version", false, "Show version information")
	managerAddr = flag.String("manager-addr", "", "SwarmKit manager address (e.g., 127.0.0.1:4242)")
	joinToken   = flag.String("join-token", "", "SwarmKit join token")
	foreignID   = flag.String("foreign-id", "", "Foreign ID for this agent")
	listenAddr  = flag.String("listen-addr", "0.0.0.0:0", "Listen address for remote API")
	stateDir    = flag.String("state-dir", "/var/lib/swarmcracker", "State directory")
)

// Config holds the agent configuration.
type Config struct {
	Executor *swarmkit.Config `yaml:"executor"`
}

func main() {
	flag.Parse()

	// Setup logging
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	log.Logger = &logger

	if *version {
		fmt.Println("SwarmCracker Agent v0.1.0-alpha")
		os.Exit(0)
	}

	// Load configuration
	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	log.Info().Msg("Starting SwarmCracker agent")

	// Create executor
	executor, err := swarmkit.NewExecutor(config.Executor)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create executor")
	}
	defer executor.Close()

	// Create SwarmKit agent
	a, err := createAgent(executor)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create agent")
	}

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start agent in background
	errChan := make(chan error, 1)
	go func() {
		if err := a.Start(context.Background()); err != nil {
			errChan <- fmt.Errorf("agent failed: %w", err)
		}
	}()

	log.Info().Msg("SwarmCracker agent running")

	// Wait for signal or error
	select {
	case sig := <-sigChan:
		log.Info().Str("signal", sig.String()).Msg("Received shutdown signal")
		a.Stop(context.Background())
	case err := <-errChan:
		log.Fatal().Err(err).Msg("Agent error")
	}

	log.Info().Msg("SwarmCracker agent stopped")
}

// loadConfig loads the configuration from a YAML file.
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// Return default config if file doesn't exist
		if os.IsNotExist(err) {
			log.Warn().Msg("Config file not found, using defaults")
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set defaults if needed
	if config.Executor == nil {
		config.Executor = &swarmkit.Config{}
	}

	return &config, nil
}

// defaultConfig returns the default configuration.
func defaultConfig() *Config {
	return &Config{
		Executor: &swarmkit.Config{
			FirecrackerPath: "firecracker",
			KernelPath:      "/usr/share/firecracker/vmlinux",
			RootfsDir:       "/var/lib/firecracker/rootfs",
			Debug:           *debug,
		},
	}
}

// createAgent creates a SwarmKit agent with the SwarmCracker executor.
func createAgent(executor *swarmkit.Executor) (*agent.Agent, error) {
	// Create executor adapter
	execAdapter := &executorAdapter{
		executor: executor,
	}

	// Agent configuration
	config := agent.Config{
		Executor:    execAdapter,
		Managers:    getManagers(),
		JoinRaft:    false,
		StateDir:    *stateDir,
		ForeignID:   *foreignID,
		ListenAddr:  *listenAddr,
		AdvertiseAddr: *listenAddr,
	}

	// Create agent
	a, err := agent.New(&config)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent: %w", err)
	}

	return a, nil
}

// getManagers returns the list of manager addresses.
func getManagers() []api.Peer {
	if *managerAddr == "" {
		// Use default manager address
		*managerAddr = "127.0.0.1:4242"
	}

	return []api.Peer{
		{
			Addr: *managerAddr,
		},
	}
}

// executorAdapter adapts SwarmCracker executor to SwarmKit's executor interface.
type executorAdapter struct {
	executor *swarmkit.Executor
}

// Describe is called to describe the current state of a task.
func (a *executorAdapter) Describe(ctx context.Context, t *api.Task) (*api.TaskStatus, error) {
	return a.executor.Describe(ctx, t)
}

// Prepare is called to prepare a task for execution.
func (a *executorAdapter) Prepare(ctx context.Context, t *api.Task) error {
	return a.executor.Prepare(ctx, t)
}

// Start is called to start a task.
func (a *executorAdapter) Start(ctx context.Context, t *api.Task) error {
	return a.executor.Start(ctx, t)
}

// Wait is called to wait for a task to complete.
func (a *executorAdapter) Wait(ctx context.Context, t *api.Task) (*api.ExitStatus, error) {
	return a.executor.Wait(ctx, t)
}

// Stop is called to stop a task.
func (a *executorAdapter) Stop(ctx context.Context, t *api.Task) error {
	return a.executor.Stop(ctx, t)
}

// Remove is called to remove a task.
func (a *executorAdapter) Remove(ctx context.Context, t *api.Task) error {
	return a.executor.Remove(ctx, t)
}

// Close closes the executor.
func (a *executorAdapter) Close() error {
	return a.executor.Close()
}

// Event returns the event channel for task status updates.
func (a *executorAdapter) Event(ctx context.Context) (<-chan *api.TaskStatus, error) {
	return a.executor.Event(ctx)
}
