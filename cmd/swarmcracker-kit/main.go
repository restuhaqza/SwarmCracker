package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

var (
	// Version is set by build flags
	Version = "v0.1.0-alpha"
	// BuildTime is set by build flags
	BuildTime = "unknown"
	// GitCommit is set by build flags
	GitCommit = "unknown"
)

func main() {
	// Parse command line flags
	configPath := os.Getenv("SWARMCRACKER_CONFIG")
	if configPath == "" {
		configPath = "/etc/swarmcracker/config.yaml"
	}

	// Load configuration
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Initialize logging
	setupLogging(cfg.Logging)

	// Log startup information
	log.Info().
		Str("version", Version).
		Str("build_time", BuildTime).
		Str("git_commit", GitCommit).
		Msg("Starting SwarmCracker")

	// Create executor - convert config types and inject dependencies
	execConfig := &executor.Config{
		KernelPath:      cfg.Executor.KernelPath,
		InitrdPath:      cfg.Executor.InitrdPath,
		RootfsDir:       cfg.Executor.RootfsDir,
		SocketDir:       cfg.Executor.SocketDir,
		DefaultVCPUs:    cfg.Executor.DefaultVCPUs,
		DefaultMemoryMB: cfg.Executor.DefaultMemoryMB,
		EnableJailer:    cfg.Executor.EnableJailer,
		Jailer: executor.JailerConfig{
			UID:           cfg.Executor.Jailer.UID,
			GID:           cfg.Executor.Jailer.GID,
			ChrootBaseDir: cfg.Executor.Jailer.ChrootBaseDir,
			NetNS:         cfg.Executor.Jailer.NetNS,
		},
		Network: types.NetworkConfig{
			BridgeName:       cfg.Network.BridgeName,
			EnableRateLimit:  cfg.Network.EnableRateLimit,
			MaxPacketsPerSec: cfg.Network.MaxPacketsPerSec,
		},
	}

	// Create component instances (dependency injection)
	vmmManager := lifecycle.NewVMMManager(execConfig)
	taskTranslator := translator.NewTaskTranslator(execConfig)
	imagePreparer := image.NewImagePreparer(execConfig)
	networkMgr := network.NewNetworkManager(execConfig.Network)

	exec, err := executor.NewFirecrackerExecutor(
		execConfig,
		vmmManager,
		taskTranslator,
		imagePreparer,
		networkMgr,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create executor")
	}
	defer exec.Close()

	// TODO: Integrate with SwarmKit agent
	// This is a placeholder for where the SwarmKit agent integration would happen
	log.Info().Msg("Executor initialized. Ready to accept tasks.")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Block until signal received
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("Received shutdown signal")

	// Cleanup
	log.Info().Msg("Shutting down gracefully...")
}

// loadConfig loads configuration from a YAML file
func loadConfig(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// setupLogging initializes the logging system
func setupLogging(cfg config.LoggingConfig) {
	// Set log level
	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// Set log format
	if cfg.Format == "text" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	// Set log output
	if cfg.Output != "stdout" && cfg.Output != "stderr" {
		file, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			log.Logger = log.Output(file)
			defer file.Close()
		}
	}
}
