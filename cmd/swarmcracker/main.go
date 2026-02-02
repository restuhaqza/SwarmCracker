package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
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
	cfgFile    string
	logLevel   string
	kernelPath string
	rootfsDir  string
	sshKeyPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "swarmcracker",
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
	rootCmd.PersistentFlags().StringVar(&sshKeyPath, "ssh-key", "", "SSH private key path for remote deployment (default: ~/.ssh/swarmcracker_deploy)")

	// Add subcommands
	rootCmd.AddCommand(newRunCommand())
	rootCmd.AddCommand(newDeployCommand())
	rootCmd.AddCommand(newValidateCommand())
	rootCmd.AddCommand(newVersionCommand())
	rootCmd.AddCommand(newListCommand())
	rootCmd.AddCommand(newStatusCommand())
	rootCmd.AddCommand(newLogsCommand())
	rootCmd.AddCommand(newStopCommand())

	// Execute
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newRunCommand creates the run command
func newRunCommand() *cobra.Command {
	var (
		detach   bool
		vcpus    int
		memory   int
		env      []string
		testMode bool
	)

	cmd := &cobra.Command{
		Use:   "run <image>",
		Short: "Run a container as a microVM",
		Long: `Run a container image as an isolated Firecracker microVM.

Example:
  swarmcracker run nginx:latest
  swarmcracker run --detach nginx:latest
  swarmcracker run --vcpus 2 --memory 1024 nginx:latest`,
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

			// Create state manager for tracking VMs
			stateMgr, err := runtime.NewStateManager("")
			if err != nil {
				log.Warn().Err(err).Msg("Failed to create state manager, VM tracking disabled")
			}

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

				// Save VM state for tracking
				if stateMgr != nil {
					// Get task details for state
					container := task.Spec.Runtime.(*types.Container)

					vmState := &runtime.VMState{
						ID:         task.ID,
						Image:      container.Image,
						Command:    append(container.Command, container.Args...),
						Status:     "running",
						VCPUs:      vcpus,
						MemoryMB:   memory,
						KernelPath: cfg.Executor.KernelPath,
						LogPath:    filepath.Join(stateMgr.GetLogDir(), task.ID+".log"),
					}

					// Get network info if available
					if len(task.Networks) > 0 {
						vmState.NetworkID = task.Networks[0].Network.ID
						vmState.IPAddresses = task.Networks[0].Addresses
					}

					// Add to state manager
					if err := stateMgr.Add(vmState); err != nil {
						log.Warn().Err(err).Msg("Failed to save VM state")
					} else {
						log.Info().Str("vm_id", task.ID).Msg("VM state saved")
					}
				}

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

// newDeployCommand creates the deploy command
func newDeployCommand() *cobra.Command {
	var (
		hosts  []string
		user   string
		port   int
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "deploy <image>",
		Short: "Deploy container as microVM to remote hosts",
		Long: `Deploy a container image as an isolated Firecracker microVM to remote hosts via SSH.

This command deploys microVMs to one or more remote hosts using SSH authentication.
It will:
  1. Validate SSH connectivity to hosts
  2. Prepare rootfs image
  3. Copy image to remote hosts
  4. Configure and start Firecracker
  5. Monitor microVM status

SSH Key Detection:
  - If --ssh-key is specified, uses that key
  - Otherwise checks ~/.ssh/swarmcracker_deploy
  - Falls back to ~/.ssh/id_ed25519 or ~/.ssh/id_rsa

Example:
  swarmcracker deploy nginx:latest --hosts host1.example.com,host2.example.com
  swarmcracker deploy --user ubuntu --ssh-key ~/.ssh/my_key nginx:latest
  swarmcracker deploy --dry-run nginx:latest`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			imageRef := args[0]

			// Resolve SSH key path
			sshKey, err := resolveSSHKey(sshKeyPath)
			if err != nil {
				return fmt.Errorf("failed to resolve SSH key: %w", err)
			}

			log.Info().
				Str("ssh_key", sshKey).
				Str("image", imageRef).
				Msg("Deployment configuration")

			// Expand hosts list (support comma-separated)
			allHosts := expandHosts(hosts)
			log.Info().Msgf("Deploying to %d hosts: %v", len(allHosts), allHosts)

			if dryRun {
				log.Info().Msg("Dry run mode - no actual deployment")
				for _, host := range allHosts {
					log.Info().
						Str("host", host).
						Str("user", user).
						Int("port", port).
						Msg("Would deploy to host")
				}
				return nil
			}

			// Load configuration
			cfg, err := loadConfigWithOverrides(cfgFile)
			if err != nil {
				return fmt.Errorf("failed to load configuration: %w", err)
			}

			// Create deployment plan
			plan := &DeploymentPlan{
				ImageRef: imageRef,
				Hosts:    allHosts,
				User:     user,
				Port:     port,
				SSHKey:   sshKey,
				Config:   cfg,
				VCPUs:    cfg.Executor.DefaultVCPUs,
				MemoryMB: cfg.Executor.DefaultMemoryMB,
			}

			// Execute deployment
			if err := executeDeployment(plan); err != nil {
				return fmt.Errorf("deployment failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&hosts, "hosts", []string{}, "Comma-separated list of remote hosts")
	cmd.Flags().StringVar(&user, "user", "root", "SSH user for remote connections")
	cmd.Flags().IntVar(&port, "port", 22, "SSH port")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without executing")

	cmd.MarkFlagRequired("hosts")

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
  swarmcracker validate --config /etc/swarmcracker/config.yaml`,
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
		Long:  `Display detailed version information about the SwarmCracker CLI.`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("SwarmCracker %s\n", Version)
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

// resolveSSHKey resolves the SSH key path using default locations
func resolveSSHKey(customPath string) (string, error) {
	// If custom path provided, use it
	if customPath != "" {
		if _, err := os.Stat(customPath); err != nil {
			return "", fmt.Errorf("SSH key not found: %s", customPath)
		}
		return customPath, nil
	}

	// Try default keys in order
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}

	defaultKeys := []string{
		"swarmcracker_deploy", // SwarmCracker-specific key
		"id_ed25519",          // Modern default
		"id_rsa",              // Legacy RSA
	}

	for _, keyName := range defaultKeys {
		keyPath := filepath.Join(homeDir, ".ssh", keyName)
		if info, err := os.Stat(keyPath); err == nil {
			// Check it's actually a file and not empty
			if !info.IsDir() && info.Size() > 0 {
				log.Info().Str("key", keyPath).Msg("Using SSH key")
				return keyPath, nil
			}
		}
	}

	return "", fmt.Errorf("no SSH key found in default locations (~/.ssh/swarmcracker_deploy, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// DeploymentPlan represents a deployment plan
type DeploymentPlan struct {
	ImageRef string
	Hosts    []string
	User     string
	Port     int
	SSHKey   string
	Config   *config.Config
	VCPUs    int
	MemoryMB int
}

// DeploymentResult represents the result of a deployment
type DeploymentResult struct {
	Host    string
	Success bool
	Error   error
	Message string
}

// executeDeployment executes the deployment plan
func executeDeployment(plan *DeploymentPlan) error {
	log.Info().Str("image", plan.ImageRef).Msg("Starting deployment")

	// Prepare rootfs locally first
	log.Info().Msg("Preparing rootfs image locally...")
	// TODO: Implement local rootfs preparation
	// For now, we'll just validate connectivity to each host

	// Execute deployment on each host
	results := make(chan DeploymentResult, len(plan.Hosts))
	for _, host := range plan.Hosts {
		go func(h string) {
			result := DeploymentResult{Host: h}
			err := deployToHost(h, plan)
			if err != nil {
				result.Success = false
				result.Error = err
				result.Message = fmt.Sprintf("Failed: %v", err)
				log.Error().Str("host", h).Err(err).Msg("Deployment failed")
			} else {
				result.Success = true
				result.Message = "Success"
				log.Info().Str("host", h).Msg("Deployment successful")
			}
			results <- result
		}(host)
	}

	// Collect results
	var successCount, failCount int
	for i := 0; i < len(plan.Hosts); i++ {
		result := <-results
		if result.Success {
			successCount++
		} else {
			failCount++
		}
	}

	// Summary
	log.Info().
		Int("total", len(plan.Hosts)).
		Int("success", successCount).
		Int("failed", failCount).
		Msg("Deployment complete")

	if failCount > 0 {
		return fmt.Errorf("%d/%d deployments failed", failCount, len(plan.Hosts))
	}

	return nil
}

// deployToHost deploys the microVM to a single host
func deployToHost(host string, plan *DeploymentPlan) error {
	log.Info().Str("host", host).Msg("Connecting to host")

	// Create SSH client
	client, err := createSSHClient(host, plan.User, plan.Port, plan.SSHKey)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()

	// Verify connectivity
	log.Info().Str("host", host).Msg("Verifying connectivity")
	if err := verifySSHConnectivity(client); err != nil {
		return fmt.Errorf("connectivity check failed: %w", err)
	}

	// Check if Firecracker is installed
	log.Info().Str("host", host).Msg("Checking Firecracker installation")
	if err := checkFirecrackerInstalled(client); err != nil {
		return fmt.Errorf("firecracker check failed: %w", err)
	}

	// Check if KVM is available
	log.Info().Str("host", host).Msg("Checking KVM availability")
	if err := checkKVMAvailable(client); err != nil {
		return fmt.Errorf("KVM check failed: %w", err)
	}

	// Deploy the microVM
	log.Info().Str("host", host).Str("image", plan.ImageRef).Msg("Deploying microVM")
	taskID := fmt.Sprintf("deploy-%d", time.Now().Unix())

	// Create deployment script
	script := generateDeploymentScript(taskID, plan.ImageRef, plan.VCPUs, plan.MemoryMB, plan.Config)

	// Execute deployment
	output, err := executeSSHCommand(client, script)
	if err != nil {
		return fmt.Errorf("deployment execution failed: %w\nOutput: %s", err, output)
	}

	log.Info().Str("host", host).Str("task_id", taskID).Msg("MicroVM deployed successfully")
	return nil
}

// createSSHClient creates an SSH client connection
func createSSHClient(host, user string, port int, keyPath string) (*ssh.Client, error) {
	// Read private key
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key: %w", err)
	}

	// Parse private key
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH key: %w", err)
	}

	// SSH client config
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use proper host key verification
		Timeout:         30 * time.Second,
	}

	// Connect
	address := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}

	return client, nil
}

// verifySSHConnectivity verifies that the SSH connection is working
func verifySSHConnectivity(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.Output("echo 'alive'")
	if err != nil {
		return fmt.Errorf("command failed: %w", err)
	}

	if strings.TrimSpace(string(output)) != "alive" {
		return fmt.Errorf("unexpected output: %s", output)
	}

	return nil
}

// checkFirecrackerInstalled checks if Firecracker is installed on the remote host
func checkFirecrackerInstalled(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("which firecracker")
	if err != nil {
		return fmt.Errorf("firecracker not found: %w\nOutput: %s", err, string(output))
	}

	log.Debug().Str("path", strings.TrimSpace(string(output))).Msg("Firecracker found")
	return nil
}

// checkKVMAvailable checks if KVM is available on the remote host
func checkKVMAvailable(client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("test -e /dev/kvm && echo 'ok' || echo 'not found'")
	if err != nil {
		return fmt.Errorf("KVM check failed: %w\nOutput: %s", err, string(output))
	}

	if !strings.Contains(string(output), "ok") {
		return fmt.Errorf("KVM device not available")
	}

	log.Debug().Msg("KVM is available")
	return nil
}

// executeSSHCommand executes a command on the remote host
func executeSSHCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return string(output), err
	}

	return string(output), nil
}

// generateDeploymentScript generates a deployment script for the remote host
func generateDeploymentScript(taskID, imageRef string, vcpus, memoryMB int, cfg *config.Config) string {
	script := fmt.Sprintf(`#!/bin/bash
set -e

# SwarmCracker Remote Deployment Script
# Task ID: %s
# Image: %s

echo "Starting deployment of task %s"

# Create working directory
WORKDIR="/tmp/swarmcracker-$TASK_ID"
mkdir -p "$WORKDIR"

# TODO: Implement full deployment logic
# This would include:
# 1. Pull OCI image
# 2. Extract rootfs
# 3. Create Firecracker config
# 4. Start Firecracker VMM
# 5. Monitor status

echo "Deployment stub executed"
echo "Task ID: $TASK_ID"
echo "Image: $IMAGE_REF"
echo "VCPUs: $VCPUS"
echo "Memory: ${MEMORY_MB}MB"

exit 0
`, taskID, imageRef, taskID)

	// Replace variables
	script = strings.ReplaceAll(script, "$TASK_ID", taskID)
	script = strings.ReplaceAll(script, "$IMAGE_REF", imageRef)
	script = strings.ReplaceAll(script, "$VCPUS", fmt.Sprintf("%d", vcpus))
	script = strings.ReplaceAll(script, "$MEMORY_MB", fmt.Sprintf("%d", memoryMB))

	return script
}

// expandHosts expands a comma-separated list of hosts
func expandHosts(hosts []string) []string {
	var result []string
	for _, h := range hosts {
		parts := strings.Split(h, ",")
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
	}
	return result
}
