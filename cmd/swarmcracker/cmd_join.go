package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// joinConfig holds the configuration for joining a cluster
type joinConfig struct {
	ManagerAddr   string
	Token         string
	Hostname      string
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
	AdvertiseAddr string
	Debug         bool
	Worker        bool
	IsManager     bool
}

func newJoinCommand() *cobra.Command {
	cfg := &joinConfig{}

	cmd := &cobra.Command{
		Use:   "join <manager-addr>",
		Short: "Join an existing SwarmCracker cluster",
		Long: `Join an existing SwarmCracker cluster as a worker or manager node.

This command will:
  1. Validate connectivity to the manager
  2. Generate configuration files
  3. Create required directories
  4. Start the worker daemon (swarmd-firecracker)
  5. Join the cluster with the provided token

Examples:
  # Join as a worker node
  swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-...

  # Join with custom advertise address
  swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-... --advertise-addr 192.168.1.11:4242

  # Join with VXLAN overlay enabled
  swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-... --vxlan-enabled

  # Join as a manager node
  swarmcracker join 192.168.1.10:4242 --token SWMTKN-1-... --manager`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
			cfg.ManagerAddr = args[0]
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJoin(cfg)
		},
	}

	// Required flags
	cmd.Flags().StringVar(&cfg.Token, "token", "", "Join token from manager (required)")
	cmd.MarkFlagRequired("token")

	// Node role
	cmd.Flags().BoolVar(&cfg.Worker, "worker", true, "Join as a worker node")
	cmd.Flags().BoolVarP(&cfg.IsManager, "manager", "m", false, "Join as a manager node (requires manager token)")

	// Network flags
	cmd.Flags().StringVar(&cfg.AdvertiseAddr, "advertise-addr", "", "Address to advertise to the cluster (default: auto-detect)")
	cmd.Flags().StringVar(&cfg.Hostname, "hostname", "", "Hostname for this node (default: auto-detect)")

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

func runJoin(cfg *joinConfig) error {
	fmt.Println()
	fmt.Println("🔗 Joining SwarmCracker Cluster")
	fmt.Println(strings.Repeat("─", 50))

	// Auto-detect advertise address if not provided
	if cfg.AdvertiseAddr == "" {
		addr, err := detectAdvertiseAddress()
		if err != nil {
			return fmt.Errorf("failed to auto-detect advertise address: %w. Please specify --advertise-addr", err)
		}
		cfg.AdvertiseAddr = addr
		log.Info().Str("address", cfg.AdvertiseAddr).Msg("Auto-detected advertise address")
	}

	// Auto-detect hostname if not provided
	if cfg.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			cfg.Hostname = "swarm-worker"
		} else {
			cfg.Hostname = hostname
		}
	}

	// Determine node role
	nodeRole := "worker"
	if cfg.IsManager {
		nodeRole = "manager"
	}

	log.Info().
		Str("manager", cfg.ManagerAddr).
		Str("role", nodeRole).
		Str("hostname", cfg.Hostname).
		Msg("Joining cluster")

	// Run pre-flight checks
	fmt.Println("\nRunning pre-flight checks...")
	preflightResult, err := RunPreflightChecks("join")
	if err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	PrintPreflightResults(preflightResult)

	if preflightResult.Failed > 0 {
		fmt.Println("\n[0;31m✗ Pre-flight checks failed. Please fix the issues above and try again.[0m")
		os.Exit(1)
	}

	// Step 1: Validate connectivity to manager
	PrintProgress(1, 5, "Validating connectivity to manager...")
	if err := validateManagerConnectivity(cfg.ManagerAddr); err != nil {
		PrintProgressFailed(1, 5, "Validating connectivity", err)
		return fmt.Errorf("cannot reach manager: %w\nPlease check:\n  - Manager is running\n  - Network connectivity\n  - Firewall rules (port 4242)", err)
	}
	PrintProgressComplete(1, 5, "Manager connectivity validated")

	// Step 1.5: Clear old state for clean join (prevents CA cache issues)
	PrintProgress(2, 5, "Clearing old cluster state...")
	if err := clearOldWorkerState(cfg); err != nil {
		log.Warn().Err(err).Msg("Failed to clear old state (may not exist)")
	}
	PrintProgressComplete(2, 5, "Old state cleared")

	// Step 2: Create directories
	PrintProgress(3, 5, "Creating required directories...")
	if err := createWorkerDirectories(cfg); err != nil {
		PrintProgressFailed(3, 5, "Creating directories", err)
		return fmt.Errorf("failed to create directories: %w", err)
	}
	PrintProgressComplete(3, 5, "Directories created")

	// Step 3: Generate configuration files
	PrintProgress(4, 5, "Generating configuration files...")
	if err := generateWorkerConfigFiles(cfg); err != nil {
		PrintProgressFailed(4, 5, "Generating configuration", err)
		return fmt.Errorf("failed to generate configuration: %w", err)
	}
	PrintProgressComplete(4, 5, "Configuration generated")

	// Step 4: Start the worker service
	PrintProgress(5, 5, "Starting worker service...")

	// Show spinner while starting
	spinnerDone := make(chan bool)
	go Spinner("Starting worker service...", spinnerDone)

	if err := startWorkerService(cfg); err != nil {
		spinnerDone <- true
		PrintProgressFailed(4, 5, "Starting worker service", err)
		return fmt.Errorf("failed to start worker service: %w", err)
	}
	spinnerDone <- true
	PrintProgressComplete(4, 5, "Worker service started")

	// Step 5: Verify node joined successfully
	PrintProgress(5, 5, "Verifying cluster join...")
	spinnerDone = make(chan bool)
	go Spinner("Verifying cluster join...", spinnerDone)
	time.Sleep(5 * time.Second) // Give it time to join
	spinnerDone <- true
	PrintProgressComplete(5, 5, "Cluster join verified")

	// Success message
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ Node joined the cluster successfully!")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Printf("Node:      %s\n", cfg.Hostname)
	fmt.Printf("Role:      %s\n", nodeRole)
	fmt.Printf("Manager:   %s\n", cfg.ManagerAddr)
	fmt.Printf("Address:   %s\n", cfg.AdvertiseAddr)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  - Check node status: swarmcracker status")
	fmt.Println("  - View cluster nodes: swarmcracker list nodes")
	fmt.Println("  - Deploy a service: swarmcracker deploy nginx:latest")
	fmt.Println()
	fmt.Println("Service status:")
	fmt.Println("  systemctl status swarmcracker-worker")
	fmt.Println("========================================")

	return nil
}

func validateManagerConnectivity(managerAddr string) error {
	// Simple TCP connectivity check
	// Extract host:port
	host := managerAddr
	if !strings.Contains(managerAddr, ":") {
		host = managerAddr + ":4242"
	}

	// Use nc or timeout to check connectivity
	cmd := exec.Command("timeout", "5", "nc", "-z", strings.Split(host, ":")[0], strings.Split(host, ":")[1])
	if err := cmd.Run(); err != nil {
		// Fallback: try with bash
		cmd = exec.Command("bash", "-c", fmt.Sprintf("timeout 5 bash -c 'echo > /dev/tcp/%s'", strings.ReplaceAll(host, ":", " ")))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("TCP connectivity check failed")
		}
	}

	log.Debug().Str("manager", managerAddr).Msg("Manager connectivity validated")
	return nil
}

// clearOldWorkerState removes old cluster state to prevent CA cache issues on join
func clearOldWorkerState(cfg *joinConfig) error {
	log.Info().Msg("Clearing old cluster state for clean join...")

	// Stop existing services
	exec.Command("systemctl", "stop", "swarmcracker-worker").Run()
	exec.Command("systemctl", "stop", "swarmd-firecracker").Run()

	// Remove systemd service files
	os.Remove("/etc/systemd/system/swarmcracker-worker.service")
	os.Remove("/etc/systemd/system/swarmd-firecracker.service")

	// Reload systemd
	exec.Command("systemctl", "daemon-reload").Run()

	// Clear SwarmKit state (cached CA certificates cause join failures)
	statePaths := []string{
		cfg.StateDir,
		"/var/lib/swarmkit",
		"/var/run/swarmkit",
		"/var/run/firecracker",
	}

	for _, path := range statePaths {
		if _, err := os.Stat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				log.Warn().Err(err).Str("path", path).Msg("Failed to remove state dir")
			} else {
				log.Debug().Str("path", path).Msg("State directory cleared")
			}
		}
	}

	log.Info().Msg("Old cluster state cleared")
	return nil
}

func createWorkerDirectories(cfg *joinConfig) error {
	dirs := []string{
		cfg.StateDir,
		cfg.ConfigDir,
		cfg.RootfsDir,
		cfg.SocketDir,
		"/var/run/swarmkit",  // Required for systemd ProtectSystem=strict
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		log.Debug().Str("dir", dir).Msg("Directory created")
	}

	return nil
}

func generateWorkerConfigFiles(cfg *joinConfig) error {
	// Generate worker config
	workerConfig := fmt.Sprintf(`# SwarmCracker Worker Configuration
# Generated: %s

worker:
  manager_addr: %s
  join_token: %s
  advertise_addr: %s
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
		cfg.ManagerAddr,
		cfg.Token,
		cfg.AdvertiseAddr,
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

	configPath := filepath.Join(cfg.ConfigDir, "worker-config.yaml")
	if err := os.WriteFile(configPath, []byte(workerConfig), 0644); err != nil {
		return fmt.Errorf("failed to write worker config: %w", err)
	}
	log.Debug().Str("path", configPath).Msg("Worker config written")

	// Generate systemd service file
	serviceTemplate := `[Unit]
Description=SwarmCracker {{if .IsManager}}Manager{{else}}Worker{{end}}
Documentation=https://github.com/restuhaqza/SwarmCracker
After=network.target docker.service
Wants=docker.service

[Service]
Type=notify
ExecStart=/usr/local/bin/swarmd-firecracker \
  {{- if .IsManager}}
  --manager \
  {{- end}}
  --hostname {{.Hostname}} \
  --state-dir {{.StateDir}} \
  {{- if not .IsManager}}
  --join-addr {{.ManagerAddr}} \
  --join-token {{.Token}} \
  {{- end}}
  --listen-remote-api 0.0.0.0:4242 \
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
ReadWritePath={{.StateDir}}
ReadWritePath={{.RootfsDir}}
ReadWritePath={{.SocketDir}}
ReadWritePath=/var/run/swarmkit

[Install]
WantedBy=multi-user.target
`

	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse service template: %w", err)
	}

	servicePath := "/etc/systemd/system/swarmcracker-worker.service"
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

func startWorkerService(cfg *joinConfig) error {
	// Enable and start the service
	log.Info().Msg("Enabling and starting swarmcracker-worker service...")

	// Enable service
	cmd := exec.Command("systemctl", "enable", "swarmcracker-worker.service")
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Warn().Err(err).Str("output", string(output)).Msg("Failed to enable service")
	}

	// Start service
	cmd = exec.Command("systemctl", "start", "swarmcracker-worker.service")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to start service: %w\nOutput: %s", err, string(output))
	}

	// Check status
	cmd = exec.Command("systemctl", "is-active", "swarmcracker-worker.service")
	output, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("service failed to start: %s", strings.TrimSpace(string(output)))
	}

	log.Info().Str("status", strings.TrimSpace(string(output))).Msg("Worker service started")
	return nil
}
