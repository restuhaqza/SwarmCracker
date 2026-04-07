package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// initConfig holds the configuration for cluster initialization
type initConfig struct {
	Hostname      string
	AdvertiseAddr string
	ListenAddr    string
	StateDir      string
	ConfigDir     string
	KernelPath    string
	RootfsDir     string
	SocketDir     string
	VCPUs         int
	Memory        int
	BridgeName    string
	Subnet        string
	BridgeIP      string
	VXLANEnabled  bool
	VXLANPeers    string
	Debug         bool
}

func newInitCommand() *cobra.Command {
	cfg := &initConfig{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new SwarmCracker cluster",
		Long: `Initialize a new SwarmCracker cluster on this node.

This command will:
  1. Generate configuration files
  2. Create required directories
  3. Start the manager daemon (swarmd-firecracker)
  4. Generate and display join tokens for workers

After initialization, use 'swarmcracker join' on worker nodes to add them to the cluster.

Examples:
  # Initialize with defaults
  swarmcracker init

  # Initialize with custom advertise address
  swarmcracker init --advertise-addr 192.168.1.10:4242

  # Initialize with VXLAN overlay enabled
  swarmcracker init --vxlan-enabled --vxlan-peers 192.168.1.11,192.168.1.12

  # Initialize with custom resources
  swarmcracker init --vcpus 2 --memory 1024`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cfg)
		},
	}

	// Network flags
	cmd.Flags().StringVar(&cfg.AdvertiseAddr, "advertise-addr", "", "Address to advertise to the cluster (default: auto-detect)")
	cmd.Flags().StringVar(&cfg.ListenAddr, "listen-addr", "0.0.0.0:4242", "Address to listen for incoming connections")

	// Resource flags
	cmd.Flags().StringVar(&cfg.StateDir, "state-dir", "/var/lib/swarmkit", "State directory for cluster data")
	cmd.Flags().StringVar(&cfg.ConfigDir, "config-dir", "/etc/swarmcracker", "Configuration directory")
	cmd.Flags().StringVar(&cfg.KernelPath, "kernel", "/usr/share/firecracker/vmlinux", "Path to Firecracker kernel")
	cmd.Flags().StringVar(&cfg.RootfsDir, "rootfs-dir", "/var/lib/firecracker/rootfs", "Directory for container rootfs")
	cmd.Flags().StringVar(&cfg.SocketDir, "socket-dir", "/var/run/firecracker", "Directory for Firecracker sockets")
	cmd.Flags().IntVar(&cfg.VCPUs, "vcpus", 1, "Default vCPUs per microVM")
	cmd.Flags().IntVar(&cfg.Memory, "memory", 512, "Default memory (MB) per microVM")

	// Network configuration
	cmd.Flags().StringVar(&cfg.BridgeName, "bridge-name", "swarm-br0", "Bridge name for VM networking")
	cmd.Flags().StringVar(&cfg.Subnet, "subnet", "192.168.127.0/24", "Subnet for VM IP allocation")
	cmd.Flags().StringVar(&cfg.BridgeIP, "bridge-ip", "192.168.127.1/24", "Bridge IP address")

	// VXLAN overlay
	cmd.Flags().BoolVar(&cfg.VXLANEnabled, "vxlan-enabled", false, "Enable VXLAN overlay for cross-node VM networking")
	cmd.Flags().StringVar(&cfg.VXLANPeers, "vxlan-peers", "", "Comma-separated list of VXLAN peer worker IPs")

	// Debug
	cmd.Flags().BoolVar(&cfg.Debug, "debug", false, "Enable debug logging")

	return cmd
}

func runInit(cfg *initConfig) error {
	fmt.Println()
	fmt.Println("🔥 Initializing SwarmCracker Cluster")
	fmt.Println(strings.Repeat("─", 50))
	
	// Auto-detect advertise address if not provided
	if cfg.AdvertiseAddr == "" {
		addr, err := detectAdvertiseAddress()
		if err != nil {
			return fmt.Errorf("failed to auto-detect advertise address: %w\nPlease specify --advertise-addr")
		}
		cfg.AdvertiseAddr = addr
		log.Info().Str("address", cfg.AdvertiseAddr).Msg("Auto-detected advertise address")
	}

	// Get hostname
	if cfg.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			cfg.Hostname = "swarm-manager"
		} else {
			cfg.Hostname = hostname
		}
	}

	// Run pre-flight checks
	fmt.Println("\nRunning pre-flight checks...")
	preflightResult, err := RunPreflightChecks("init")
	if err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}
	
	PrintPreflightResults(preflightResult)
	
	if preflightResult.Failed > 0 {
		fmt.Println("\n[0;31m✗ Pre-flight checks failed. Please fix the issues above and try again.[0m")
		fmt.Println("\nHint: Run 'swarmcracker init --help' for configuration options.")
		os.Exit(1)
	}

	// Step 1: Create directories
	PrintProgress(1, 5, "Creating required directories...")
	if err := createDirectories(cfg); err != nil {
		PrintProgressFailed(1, 5, "Creating directories", err)
		return fmt.Errorf("failed to create directories: %w", err)
	}
	PrintProgressComplete(1, 5, "Directories created")

	// Step 2: Generate configuration files
	PrintProgress(2, 5, "Generating configuration files...")
	if err := generateConfigFiles(cfg); err != nil {
		PrintProgressFailed(2, 5, "Generating configuration", err)
		return fmt.Errorf("failed to generate configuration: %w", err)
	}
	PrintProgressComplete(2, 5, "Configuration generated")

	// Step 3: Start the manager service
	PrintProgress(3, 5, "Starting manager service...")
	
	// Show spinner while starting
	spinnerDone := make(chan bool)
	go Spinner("Starting manager service...", spinnerDone)
	
	if err := startManagerService(cfg); err != nil {
		spinnerDone <- true
		PrintProgressFailed(3, 5, "Starting manager service", err)
		return fmt.Errorf("failed to start manager service: %w", err)
	}
	spinnerDone <- true
	PrintProgressComplete(3, 5, "Manager service started")

	// Step 4: Wait for service to be ready and get join tokens
	PrintProgress(4, 5, "Waiting for manager to be ready...")
	spinnerDone = make(chan bool)
	go Spinner("Waiting for manager...", spinnerDone)
	time.Sleep(5 * time.Second) // Give it time to start
	spinnerDone <- true
	PrintProgressComplete(4, 5, "Manager ready")

	// Step 5: Display join tokens
	PrintProgress(5, 5, "Generating join tokens...")
	if err := displayJoinTokens(cfg); err != nil {
		PrintProgressFailed(5, 5, "Generating join tokens", err)
		log.Warn().Err(err).Msg("Failed to retrieve join tokens automatically")
		log.Info().Msg("Join tokens will be available in: " + filepath.Join(cfg.StateDir, "join-tokens.txt"))
	} else {
		PrintProgressComplete(5, 5, "Join tokens generated")
	}

	// Success message
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ SwarmCracker cluster initialized!")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Printf("Manager: %s (%s)\n", cfg.Hostname, cfg.AdvertiseAddr)
	fmt.Println()
	fmt.Println("To add workers to this cluster:")
	fmt.Println()
	fmt.Printf("  swarmcracker join %s --token <WORKER_TOKEN>\n", cfg.AdvertiseAddr)
	fmt.Println()
	fmt.Println("Or run on worker nodes:")
	fmt.Printf("  swarmcracker join %s --token <WORKER_TOKEN>\n", cfg.AdvertiseAddr)
	fmt.Println()
	fmt.Println("View cluster status:")
	fmt.Println("  swarmcracker status")
	fmt.Println()
	fmt.Println("Join tokens saved to:")
	fmt.Printf("  %s\n", filepath.Join(cfg.StateDir, "join-tokens.txt"))
	fmt.Println("========================================")

	return nil
}

func detectAdvertiseAddress() (string, error) {
	// Try to get primary IP address
	cmd := exec.Command("hostname", "-I")
	output, err := cmd.Output()
	if err != nil {
		// Fallback: try ip command
		cmd = exec.Command("ip", "-4", "addr", "show", "scope", "global")
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to detect IP address")
		}

		// Parse ip output
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, "inet ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					ip := strings.Split(parts[1], "/")[0]
					return ip + ":4242", nil
				}
			}
		}
		return "", fmt.Errorf("failed to parse IP address")
	}

	// Parse hostname -I output (space-separated IPs)
	ips := strings.Fields(strings.TrimSpace(string(output)))
	if len(ips) > 0 {
		return ips[0] + ":4242", nil
	}

	return "", fmt.Errorf("no IP addresses found")
}

func createDirectories(cfg *initConfig) error {
	dirs := []string{
		cfg.StateDir,
		cfg.ConfigDir,
		cfg.RootfsDir,
		cfg.SocketDir,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		log.Debug().Str("dir", dir).Msg("Directory created")
	}

	return nil
}

func generateConfigFiles(cfg *initConfig) error {
	// Generate manager config
	managerConfig := fmt.Sprintf(`# SwarmCracker Manager Configuration
# Generated: %s

manager:
  advertise_addr: %s
  listen_addr: %s
  state_dir: %s

firecracker:
  kernel_path: %s
  rootfs_dir: %s
  socket_dir: %s
  default_vcpus: %d
  default_memory: %d

network:
  bridge_name: %s
  subnet: %s
  bridge_ip: %s
  vxlan_enabled: %v
  vxlan_peers: [%s]

logging:
  level: %s
`,
		time.Now().Format(time.RFC3339),
		cfg.AdvertiseAddr,
		cfg.ListenAddr,
		cfg.StateDir,
		cfg.KernelPath,
		cfg.RootfsDir,
		cfg.SocketDir,
		cfg.VCPUs,
		cfg.Memory,
		cfg.BridgeName,
		cfg.Subnet,
		cfg.BridgeIP,
		cfg.VXLANEnabled,
		cfg.VXLANPeers,
		map[bool]string{true: "debug", false: "info"}[cfg.Debug],
	)

	configPath := filepath.Join(cfg.ConfigDir, "manager-config.yaml")
	if err := os.WriteFile(configPath, []byte(managerConfig), 0644); err != nil {
		return fmt.Errorf("failed to write manager config: %w", err)
	}
	log.Debug().Str("path", configPath).Msg("Manager config written")

	// Generate systemd service file
	serviceTemplate := `[Unit]
Description=SwarmCracker Manager
Documentation=https://github.com/restuhaqza/SwarmCracker
After=network.target docker.service
Wants=docker.service

[Service]
Type=notify
ExecStart=/usr/local/bin/swarmd-firecracker \
  --manager \
  --hostname {{.Hostname}} \
  --state-dir {{.StateDir}} \
  --listen-remote-api {{.ListenAddr}} \
  --advertise-remote-api {{.AdvertiseAddr}} \
  --kernel-path {{.KernelPath}} \
  --rootfs-dir {{.RootfsDir}} \
  --socket-dir {{.SocketDir}} \
  --default-vcpus {{.VCPUs}} \
  --default-memory {{.Memory}} \
  --bridge-name {{.BridgeName}} \
  --subnet {{.Subnet}} \
  --bridge-ip {{.BridgeIP}} \
  {{- if .VXLANEnabled}}
  --vxlan-enabled \
  --vxlan-peers {{.VXLANPeers}} \
  {{- end}}
  {{- if .Debug}}
  --debug \
  {{- end}}
  --heartbeat-tick 1 \
  --election-tick 10
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
LimitNPROC=65536

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths={{.StateDir}} {{.RootfsDir}} {{.SocketDir}} /var/run/swarmkit

[Install]
WantedBy=multi-user.target
`

	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse service template: %w", err)
	}

	servicePath := "/etc/systemd/system/swarmcracker-manager.service"
	serviceFile, err := os.Create(servicePath)
	if err != nil {
		return fmt.Errorf("failed to create service file: %w", err)
	}
	defer serviceFile.Close()

	if err := tmpl.Execute(serviceFile, cfg); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}
	log.Debug().Str("path", servicePath).Msg("Systemd service file written")

	// Reload systemd
	log.Info().Msg("Reloading systemd daemon...")
	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		log.Warn().Err(err).Msg("Failed to reload systemd daemon (may need manual reload)")
	}

	return nil
}

func startManagerService(cfg *initConfig) error {
	// Enable and start the service
	log.Info().Msg("Enabling and starting swarmcracker-manager service...")

	// Enable service
	cmd := exec.Command("systemctl", "enable", "swarmcracker-manager.service")
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Warn().Err(err).Str("output", string(output)).Msg("Failed to enable service")
	}

	// Start service
	cmd = exec.Command("systemctl", "start", "swarmcracker-manager.service")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, string(output))
	}

	// Check status
	cmd = exec.Command("systemctl", "is-active", "swarmcracker-manager.service")
	output, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("service failed to start: %s", strings.TrimSpace(string(output)))
	}

	log.Info().Str("status", strings.TrimSpace(string(output))).Msg("Manager service started")
	return nil
}

func displayJoinTokens(cfg *initConfig) error {
	tokenFile := filepath.Join(cfg.StateDir, "join-tokens.txt")

	// Wait a bit for the service to write tokens
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(tokenFile); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// Read and display tokens
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return fmt.Errorf("failed to read join tokens: %w", err)
	}

	fmt.Println()
	fmt.Println("📋 Join Tokens:")
	fmt.Println(string(data))

	return nil
}

func init() {
	// Register architecture-specific defaults
	if runtime.GOARCH == "arm64" {
		// ARM64 may need different kernel path
	}
}
