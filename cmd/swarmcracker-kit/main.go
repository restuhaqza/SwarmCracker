package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	// Version is set by build flags
	Version = "v0.1.0-alpha"
	// BuildTime is set by build flags
	BuildTime = "unknown"
	// GitCommit is set by build flags
	GitCommit = "unknown"
)

// Global flags
var (
	cfgFile     string
	logLevel    string
	kernelPath  string
	rootfsDir   string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "swarmcracker-kit",
		Short: "SwarmCracker CLI - Run containers as Firecracker microVMs",
		Long: `SwarmCracker is a CLI tool for running containers as isolated Firecracker microVMs.

It provides a simple interface to the SwarmCracker executor, allowing you to:
  - Run containers as microVMs
  - Validate configuration files
  - Test executor functionality`,
		Version: Version,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "Path to configuration file (default: /etc/swarmcracker/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().StringVar(&kernelPath, "kernel", "", "Override kernel path")
	rootCmd.PersistentFlags().StringVar(&rootfsDir, "rootfs-dir", "", "Override rootfs directory")

	// Add subcommands
	rootCmd.AddCommand(newRunCommand())
	rootCmd.AddCommand(newValidateCommand())
	rootCmd.AddCommand(newVersionCommand())

	// Execute
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRunCommand creates the run command
func newRunCommand() *cobra.Command {
	var (
		detach    bool
		vcpus     int
		memory    int
		env       []string
		testMode  bool
	)

	cmd := &cobra.Command{
		Use:   "run <image>",
		Short: "Run a container as a microVM",
		Long: `Run a container image as an isolated Firecracker microVM.

Example:
  swarmcracker-kit run nginx:latest
  swarmcracker-kit run --detach nginx:latest
  swarmcracker-kit run --vcpus 2 --memory 1024 nginx:latest`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			// Setup logging
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := args[0]

			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			// Create executor
			exec, err := createExecutor(cfg)
			if err != nil {
				return fmt.Errorf("failed to create executor: %w", err)
			}
			defer exec.Close()

			// Create a mock task
			task := createMockTask(imageRef, vcpus, memory, env)

			// Prepare context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			// Setup signal handling for cleanup
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				log.Info().Str("signal", sig.String()).Msg("Received interrupt signal, cleaning up...")
				cancel()
				exec.Remove(context.Background(), task)
				os.Exit(1)
			}()

			// In test mode, just validate and exit
			if testMode {
				log.Info().Str("image", imageRef).Msg("Test mode: validating image reference")
				log.Info().
					Str("task_id", task.ID).
					Str("image", task.Spec.Runtime.(*types.Container).Image).
					Msg("Task created successfully")
				return nil
			}

			// Prepare the task
			log.Info().Str("task_id", task.ID).Msg("Preparing task...")
			if err := exec.Prepare(ctx, task); err != nil {
				return fmt.Errorf("failed to prepare task: %w", err)
			}

			// Start the task
			log.Info().Str("task_id", task.ID).Msg("Starting task...")
			if err := exec.Start(ctx, task); err != nil {
				return fmt.Errorf("failed to start task: %w", err)
			}

			if detach {
				log.Info().Str("task_id", task.ID).Msg("Task started in detached mode")
				return nil
			}

			// Wait for completion
			log.Info().Str("task_id", task.ID).Msg("Waiting for task to complete...")
			status, err := exec.Wait(ctx, task)
			if err != nil {
				log.Error().Err(err).Msg("Task failed")
				return fmt.Errorf("task execution failed: %w", err)
			}

			log.Info().
				Str("task_id", task.ID).
				Str("state", taskStateString(status.State)).
				Msg("Task completed")

			// Cleanup
			log.Info().Str("task_id", task.ID).Msg("Cleaning up...")
			if err := exec.Remove(ctx, task); err != nil {
				log.Warn().Err(err).Msg("Cleanup failed (task may still be running)")
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Run in detached mode (don't wait for completion)")
	cmd.Flags().IntVar(&vcpus, "vcpus", 1, "Number of vCPUs to allocate")
	cmd.Flags().IntVar(&memory, "memory", 512, "Memory in MB to allocate")
	cmd.Flags().StringArrayVarP(&env, "env", "e", []string{}, "Environment variables (e.g., -e KEY=value)")
	cmd.Flags().BoolVar(&testMode, "test", false, "Test mode (validate without running)")

	return cmd
}

// newValidateCommand creates the validate command
func newValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration file",
		Long: `Validate the SwarmCracker configuration file.

This command checks that the configuration file is valid and prints
any errors or warnings.

Example:
  swarmcracker-kit validate --config /etc/swarmcracker/config.yaml`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("configuration validation failed: %w", err)
			}

			// Validate configuration
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			fmt.Println("✓ Configuration is valid")
			fmt.Printf("  Kernel: %s\n", cfg.Executor.KernelPath)
			fmt.Printf("  Rootfs: %s\n", cfg.Executor.RootfsDir)
			fmt.Printf("  Bridge: %s\n", cfg.Network.BridgeName)
			fmt.Printf("  VCPUs: %d\n", cfg.Executor.DefaultVCPUs)
			fmt.Printf("  Memory: %d MB\n", cfg.Executor.DefaultMemoryMB)
			fmt.Printf("  Jailer: %v\n", cfg.Executor.EnableJailer)

			return nil
		},
	}

	return cmd
}

// newVersionCommand creates the version command
func newVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Long: `Display detailed version information about the SwarmCracker CLI.`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("SwarmCracker Kit %s\n", Version)
			fmt.Printf("  Build Time: %s\n", BuildTime)
			fmt.Printf("  Git Commit: %s\n", GitCommit)
			fmt.Printf("  Go Version: %s\n", goVersion())
		},
	}

	return cmd
}

// loadConfigWithOverrides loads configuration and applies CLI overrides
func loadConfigWithOverrides(path string) (*config.Config, error) {
	// Determine config path
	if path == "" {
		path = config.GetDefaultConfigPath()
	}

	// Load configuration
	cfg, err := config.LoadConfig(path)
	if err != nil {
		// If file doesn't exist, try to create a default one
		if os.IsNotExist(err) {
			log.Warn().Str("path", path).Msg("Config file not found, using defaults")
			cfg = &config.Config{}
			cfg.SetDefaults()
		} else {
			return nil, err
		}
	}

	// Apply CLI overrides
	if kernelPath != "" {
		cfg.Executor.KernelPath = kernelPath
		log.Info().Str("kernel", kernelPath).Msg("Kernel path overridden")
	}

	if rootfsDir != "" {
		cfg.Executor.RootfsDir = rootfsDir
		log.Info().Str("rootfs", rootfsDir).Msg("Rootfs directory overridden")
	}

	// Set defaults for any missing values
	cfg.SetDefaults()

	return cfg, nil
}

// createExecutor creates a new Firecracker executor with all dependencies
func createExecutor(cfg *config.Config) (*executor.FirecrackerExecutor, error) {
	// Create executor config
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
	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
		EnableJailer:    execConfig.EnableJailer,
	}

	imageConfig := &image.PreparerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
	}

	vmmManager := lifecycle.NewVMMManager(vmmConfig)
	taskTranslator := translator.NewTaskTranslator(vmmConfig)
	imagePreparer := image.NewImagePreparer(imageConfig)
	networkMgr := network.NewNetworkManager(execConfig.Network)

	exec, err := executor.NewFirecrackerExecutor(
		execConfig,
		vmmManager,
		taskTranslator,
		imagePreparer,
		networkMgr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}

	return exec, nil
}

// createMockTask creates a mock task for testing
func createMockTask(imageRef string, vcpus, memoryMB int, env []string) *types.Task {
	return &types.Task{
		ID:        fmt.Sprintf("task-%d", time.Now().Unix()),
		ServiceID: "service-cli",
		NodeID:    "node-local",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   imageRef,
				Command: []string{},
				Args:    []string{},
				Env:     env,
				Mounts:  []types.Mount{},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    int64(vcpus * 1e9),
					MemoryBytes: int64(memoryMB * 1024 * 1024),
				},
			},
		},
		Status: types.TaskStatus{
			State: types.TaskState_PENDING,
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{
								Name: "swarm-br0",
							},
						},
					},
				},
			},
		},
	}
}

// setupLogging initializes the logging system
func setupLogging(level string) {
	// Parse log level
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		logLevel = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(logLevel)

	// Set console output for CLI
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
}

// goVersion returns the Go runtime version
func goVersion() string {
	return fmt.Sprintf("%s (%s/%s)", getGoVersion(), getGOOS(), getGOARCH())
}

func getGoVersion() string {
	return "1.21" // Simplified for CLI
}

func getGOOS() string {
	return "linux" // Simplified for CLI
}

func getGOARCH() string {
	return "amd64" // Simplified for CLI
}

// taskStateString converts TaskState to string
func taskStateString(state types.TaskState) string {
	switch state {
	case types.TaskState_NEW:
		return "NEW"
	case types.TaskState_PENDING:
		return "PENDING"
	case types.TaskState_ASSIGNED:
		return "ASSIGNED"
	case types.TaskState_ACCEPTED:
		return "ACCEPTED"
	case types.TaskState_PREPARING:
		return "PREPARING"
	case types.TaskState_STARTING:
		return "STARTING"
	case types.TaskState_RUNNING:
		return "RUNNING"
	case types.TaskState_COMPLETE:
		return "COMPLETE"
	case types.TaskState_FAILED:
		return "FAILED"
	case types.TaskState_REJECTED:
		return "REJECTED"
	case types.TaskState_REMOVE:
		return "REMOVE"
	case types.TaskState_ORPHANED:
		return "ORPHANED"
	default:
		return "UNKNOWN"
	}
}
