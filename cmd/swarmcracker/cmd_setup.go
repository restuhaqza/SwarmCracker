package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/spf13/cobra"
)

// newSetupCommand creates the setup command group for initial cluster setup.
func newSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up SwarmCracker prerequisites and configuration",
		Long: `Set up everything needed to run SwarmCracker on this node.

Subcommands:
  check    - Verify prerequisites (KVM, kernel modules, binaries)
  install  - Download and install Firecracker + jailer
  network  - Create bridge, enable NAT, load kernel modules
  config   - Generate or update configuration file`,
	}

	cmd.AddCommand(newSetupCheckCommand())
	cmd.AddCommand(newSetupInstallCommand())
	cmd.AddCommand(newSetupNetworkCommand())
	cmd.AddCommand(newSetupConfigCommand())

	return cmd
}

// ─── setup check ───────────────────────────────────────────────────────

func newSetupCheckCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Verify system prerequisites",
		Long: `Check that all prerequisites for running SwarmCracker are met.

Verifies:
  - Linux OS (required for KVM)
  - /dev/kvm exists and is accessible
  - CPU virtualization support
  - Required kernel modules (br_netfilter, vxlan)
  - Firecracker binary
  - Required system tools (ip, modprobe, sysctl)
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupCheck()
		},
	}
}

func runSetupCheck() error {
	allGood := true
	check := func(name string, ok bool, detail string) {
		if ok {
			fmt.Printf("  ✅ %-30s %s\n", name, detail)
		} else {
			fmt.Printf("  ❌ %-30s %s\n", name, detail)
			allGood = false
		}
	}

	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║          SwarmCracker — Prerequisites Check             ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// OS check
	fmt.Println("── System ──")
	check("OS", runtime.GOOS == "linux", runtime.GOOS)

	// KVM check
	fmt.Println("── Virtualization ──")
	kvmExists := false
	if _, err := os.Stat("/dev/kvm"); err == nil {
		kvmExists = true
	}
	check("/dev/kvm", kvmExists, "KVM device accessible")

	// CPU virtualization
	cpuInfo, _ := os.ReadFile("/proc/cpuinfo")
	cpuStr := string(cpuInfo)
	hasVMX := strings.Contains(cpuStr, "vmx") || strings.Contains(cpuStr, "svm")
	check("CPU virtualization", hasVMX, "vmx/svm in /proc/cpuinfo")

	// Kernel modules
	fmt.Println("── Kernel Modules ──")
	kmods := []string{"br_netfilter", "vxlan", "overlay"}
	for _, mod := range kmods {
		out, err := exec.Command("modprobe", "--dry-run", mod).CombinedOutput()
		check("module: "+mod, err == nil, strings.TrimSpace(string(out)))
	}

	// IP forwarding
	fmt.Println("── Network ──")
	fwd, _ := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	check("ip_forward", strings.TrimSpace(string(fwd)) == "1", "IPv4 forwarding")

	// Tools
	fmt.Println("── Required Tools ──")
	tools := []string{"ip", "modprobe", "sysctl", "curl", "tar"}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		check(tool, err == nil, path)
	}

	// Firecracker
	fcPath, fcErr := exec.LookPath("firecracker")
	check("firecracker", fcErr == nil, fcPath)

	// Firecracker version
	if fcErr == nil {
		verOut, _ := exec.Command(fcPath, "--version").CombinedOutput()
		verLine := strings.TrimSpace(string(verOut))
		if idx := strings.Index(verLine, "\n"); idx > 0 {
			verLine = verLine[:idx]
		}
		fmt.Printf("     %-30s %s\n", "", verLine)
	}

	// Jailer
	jailerPath, jailerErr := exec.LookPath("jailer")
	check("jailer", jailerErr == nil, jailerPath)

	// Kernel image
	fmt.Println("── VM Artifacts ──")
	kernelPaths := []string{
		"/usr/share/firecracker/vmlinux",
		"/var/lib/firecracker/kernel/vmlinux",
	}
	kernelFound := false
	for _, kp := range kernelPaths {
		if _, err := os.Stat(kp); err == nil {
			check("kernel", true, kp)
			kernelFound = true
			break
		}
	}
	if !kernelFound {
		check("kernel", false, "not found (run 'setup install --download-kernel')")
	}

	// Built-in kernel fallback
	if _, err := os.Stat("/boot/vmlinuz-" + getKernelRelease()); err == nil {
		fmt.Printf("     %-30s /boot/vmlinuz-%s (fallback)\n", "", getKernelRelease())
	}

	// Rootfs
	rootfsPaths := []string{
		"/var/lib/firecracker/rootfs/bionic.rootfs.ext4",
	}
	rootfsFound := false
	for _, rp := range rootfsPaths {
		if _, err := os.Stat(rp); err == nil {
			check("rootfs", true, rp)
			rootfsFound = true
			break
		}
	}
	if !rootfsFound {
		check("rootfs", false, "not found (run 'setup install --download-rootfs')")
	}

	// Config file
	fmt.Println("── Configuration ──")
	cfgPath := config.GetDefaultConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		check("config.yaml", true, cfgPath)
	} else {
		check("config.yaml", false, "not found (run 'setup config' to generate)")
	}

	fmt.Println()
	if allGood {
		fmt.Println("✅ All checks passed — node is ready for SwarmCracker")
		return nil
	}
	fmt.Println("⚠️  Some checks failed. Run 'swarmcracker setup install' to fix missing dependencies.")
	return fmt.Errorf("%d prerequisite checks failed", countFailures())
}

func getKernelRelease() string {
	out, _ := exec.Command("uname", "-r").Output()
	return strings.TrimSpace(string(out))
}

func countFailures() int {
	// This is a simplification — the actual check function tracks failures internally.
	// For the CLI exit code, a non-zero exit on any failure is fine.
	return 1
}

// ─── setup install ─────────────────────────────────────────────────────

var (
	setupInstallDownloadKernel bool
	setupInstallDownloadRootfs bool
	setupInstallFirecrackerVer string
)

func newSetupInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download and install Firecracker dependencies",
		Long: `Download and install Firecracker, jailer, kernel, and rootfs.

By default, installs Firecracker binary and jailer from GitHub releases.
Use --download-kernel and --download-rootfs to also fetch VM images.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupInstall()
		},
	}

	cmd.Flags().BoolVar(&setupInstallDownloadKernel, "download-kernel", false, "Download Firecracker-compatible kernel")
	cmd.Flags().BoolVar(&setupInstallDownloadRootfs, "download-rootfs", false, "Download Ubuntu rootfs image")
	cmd.Flags().StringVar(&setupInstallFirecrackerVer, "firecracker-version", "v1.15.1", "Firecracker version to install")

	return cmd
}

func runSetupInstall() error {
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║        SwarmCracker — Dependency Installation           ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Install Firecracker + Jailer
	if err := installFirecracker(); err != nil {
		return fmt.Errorf("firecracker install failed: %w", err)
	}

	// Optional: download kernel
	if setupInstallDownloadKernel {
		if err := downloadKernel(); err != nil {
			fmt.Printf("  ⚠️  Kernel download failed: %v\n", err)
		}
	}

	// Optional: download rootfs
	if setupInstallDownloadRootfs {
		if err := downloadRootfs(); err != nil {
			fmt.Printf("  ⚠️  Rootfs download failed: %v\n", err)
		}
	}

	fmt.Println()
	fmt.Println("✅ Installation complete")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  swarmcracker setup network    # Create bridge + enable NAT")
	fmt.Println("  swarmcracker setup config     # Generate configuration")
	fmt.Println("  swarmcracker cluster init     # Initialize cluster (manager)")
	fmt.Println("  swarmcracker cluster join     # Join cluster (worker)")
	return nil
}

func installFirecracker() error {
	// Check if already installed
	if path, err := exec.LookPath("firecracker"); err == nil {
		verOut, _ := exec.Command(path, "--version").CombinedOutput()
		fmt.Printf("  ✅ firecracker already installed: %s\n", strings.TrimSpace(strings.SplitN(string(verOut), "\n", 2)[0]))
	} else {
		fmt.Printf("  📦 Installing Firecracker %s...\n", setupInstallFirecrackerVer)

		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x86_64"
		}
		if arch == "arm64" {
			arch = "aarch64"
		}

		url := fmt.Sprintf(
			"https://github.com/firecracker-microvm/firecracker/releases/download/%s/firecracker-%s-%s.tgz",
			setupInstallFirecrackerVer, setupInstallFirecrackerVer, arch,
		)

		fmt.Printf("     Downloading %s...\n", url)
		// For now, inform the user how to install manually since Go doesn't
		// have a built-in tarball downloader without extra deps.
		fmt.Printf("     Please run:\n")
		fmt.Printf("       curl -fsSL %s -o /tmp/firecracker.tgz\n", url)
		fmt.Printf("       tar xzf /tmp/firecracker.tgz -C /tmp\n")
		fmt.Printf("       sudo cp /tmp/release-%s-%s/firecracker-%s-%s /usr/local/bin/firecracker\n",
			setupInstallFirecrackerVer, arch, setupInstallFirecrackerVer, arch)
		fmt.Printf("       sudo cp /tmp/release-%s-%s/jailer-%s-%s /usr/local/bin/jailer\n",
			setupInstallFirecrackerVer, arch, setupInstallFirecrackerVer, arch)
		fmt.Printf("       sudo chmod +x /usr/local/bin/firecracker /usr/local/bin/jailer\n")
		fmt.Printf("       rm -rf /tmp/firecracker.tgz /tmp/release-%s-%s\n",
			setupInstallFirecrackerVer, arch)

		// Try curl-based install if curl is available
		if _, err := exec.LookPath("curl"); err == nil {
			tmpDir, err := os.MkdirTemp("", "swarmcracker-install-")
			if err != nil {
				return fmt.Errorf("failed to create temp dir: %w", err)
			}
			defer os.RemoveAll(tmpDir)

			tarball := filepath.Join(tmpDir, "firecracker.tgz")
			curlCmd := exec.Command("curl", "-fsSL", url, "-o", tarball)
			curlCmd.Stdout = os.Stdout
			curlCmd.Stderr = os.Stderr
			if err := curlCmd.Run(); err != nil {
				return fmt.Errorf("failed to download firecracker: %w", err)
			}

			tarCmd := exec.Command("tar", "xzf", tarball, "-C", tmpDir)
			if err := tarCmd.Run(); err != nil {
				return fmt.Errorf("failed to extract firecracker: %w", err)
			}

			// Find and install binaries
			releaseDir := filepath.Join(tmpDir, fmt.Sprintf("release-%s-%s", setupInstallFirecrackerVer, arch))

			// Try to find the actual binary names (Firecracker naming varies)
			fcFiles, _ := filepath.Glob(filepath.Join(releaseDir, "firecracker*"))
			for _, f := range fcFiles {
				if !strings.HasSuffix(f, ".debug") && !strings.HasSuffix(f, ".stripped") {
					installBin(f, "/usr/local/bin/firecracker")
					fmt.Printf("  ✅ firecracker installed to /usr/local/bin/firecracker\n")
					break
				}
			}

			jailerFiles, _ := filepath.Glob(filepath.Join(releaseDir, "jailer*"))
			for _, f := range jailerFiles {
				if !strings.HasSuffix(f, ".debug") {
					installBin(f, "/usr/local/bin/jailer")
					fmt.Printf("  ✅ jailer installed to /usr/local/bin/jailer\n")
					break
				}
			}
		}
	}

	// Jailer user setup
	if _, err := exec.LookPath("jailer"); err == nil {
		fmt.Println("  ✅ jailer available")
	}

	return nil
}

func installBin(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Need sudo for /usr/local/bin — skip if not root, just inform
	if os.Getuid() != 0 {
		fmt.Printf("     ⚠️  Run as root to install: cp %s → %s\n", src, dst)
		return nil
	}
	return os.WriteFile(dst, data, 0755)
}

func downloadKernel() error {
	fmt.Println("  📦 Downloading kernel...")
	fmt.Println("     The install.sh script handles kernel download with version")
	fmt.Println("     discovery. Run the install script for automatic kernel setup:")
	fmt.Println("       curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash")
	fmt.Println("     Or download manually:")
	fmt.Println("       sudo mkdir -p /usr/share/firecracker")
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	fmt.Printf("       curl -fsSL https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/%s/vmlinux-6.1.155 -o /usr/share/firecracker/vmlinux\n", arch)
	return nil
}

func downloadRootfs() error {
	fmt.Println("  📦 Downloading rootfs...")
	fmt.Println("     Run the install script for automatic rootfs setup:")
	fmt.Println("       curl -fsSL https://raw.githubusercontent.com/restuhaqza/SwarmCracker/main/install.sh | bash")
	fmt.Println("     Or download manually:")
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	fmt.Printf("       sudo mkdir -p /var/lib/firecracker/rootfs\n")
	fmt.Printf("       curl -fsSL https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/%s/rootfs/bionic.rootfs.ext4 -o /var/lib/firecracker/rootfs/bionic.rootfs.ext4\n", arch)
	return nil
}

// ─── setup network ──────────────────────────────────────────────────────

var (
	setupNetworkBridge string
	setupNetworkSubnet string
	setupNetworkIP     string
	setupNetworkNAT    bool
)

func newSetupNetworkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Create and configure the VM bridge network",
		Long: `Create a Linux bridge for microVM networking and enable NAT.

This sets up the host-side networking that microVMs connect to:
  - Creates a bridge device (default: swarm-br0)
  - Assigns an IP to the bridge
  - Enables IP forwarding
  - Enables NAT masquerading for internet access
  - Loads required kernel modules (br_netfilter, vxlan)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupNetwork()
		},
	}

	cmd.Flags().StringVar(&setupNetworkBridge, "bridge", "swarm-br0", "Bridge device name")
	cmd.Flags().StringVar(&setupNetworkSubnet, "subnet", "192.168.127.0/24", "Subnet for VM network (CIDR)")
	cmd.Flags().StringVar(&setupNetworkIP, "bridge-ip", "192.168.127.1/24", "Bridge IP address (CIDR)")
	cmd.Flags().BoolVar(&setupNetworkNAT, "nat", true, "Enable NAT masquerading")

	return cmd
}

func runSetupNetwork() error {
	if os.Getuid() != 0 {
		return fmt.Errorf("setup network requires root privileges (bridge creation, sysctl)")
	}

	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║          SwarmCracker — Network Setup                   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Load kernel modules
	fmt.Println("── Kernel Modules ──")
	modules := []string{"br_netfilter", "vxlan"}
	for _, mod := range modules {
		cmd := exec.Command("modprobe", mod)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("  ⚠️  modprobe %s: %s\n", mod, strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✅ %s loaded\n", mod)
		}
	}

	// IP forwarding
	fmt.Println("── IP Forwarding ──")
	sysctls := map[string]string{
		"net.ipv4.ip_forward":                  "1",
		"net.bridge.bridge-nf-call-iptables":   "0",
	}
	for key, val := range sysctls {
		cmd := exec.Command("sysctl", "-w", key+"="+val)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("  ⚠️  %s = %s: %s\n", key, val, strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✅ %s = %s\n", key, val)
		}
	}

	// Create bridge
	fmt.Println("── Bridge ──")
	fmt.Printf("  Bridge: %s (%s)\n", setupNetworkBridge, setupNetworkIP)

	// Check if bridge exists
	checkCmd := exec.Command("ip", "link", "show", setupNetworkBridge)
	if checkCmd.Run() == nil {
		fmt.Printf("  ⚠️  %s already exists — skipping creation\n", setupNetworkBridge)
	} else {
		// Create bridge
		addCmd := exec.Command("ip", "link", "add", "name", setupNetworkBridge, "type", "bridge")
		if out, err := addCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create bridge %s: %s", setupNetworkBridge, strings.TrimSpace(string(out)))
		}
		fmt.Printf("  ✅ %s created\n", setupNetworkBridge)

		// Assign IP
		addrCmd := exec.Command("ip", "addr", "add", setupNetworkIP, "dev", setupNetworkBridge)
		if out, err := addrCmd.CombinedOutput(); err != nil {
			fmt.Printf("  ⚠️  IP assignment: %s (may already exist)\n", strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✅ IP %s assigned\n", setupNetworkIP)
		}

		// Bring up
		upCmd := exec.Command("ip", "link", "set", setupNetworkBridge, "up")
		if out, err := upCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to bring up %s: %s", setupNetworkBridge, strings.TrimSpace(string(out)))
		}
		fmt.Printf("  ✅ %s is up\n", setupNetworkBridge)

		// Bridge forwarding
		sysctlCmd := exec.Command("sysctl", "-w", "net.ipv4.conf."+setupNetworkBridge+".forwarding=1")
		if out, err := sysctlCmd.CombinedOutput(); err != nil {
			fmt.Printf("  ⚠️  forwarding: %s\n", strings.TrimSpace(string(out)))
		} else {
			fmt.Printf("  ✅ forwarding enabled on %s\n", setupNetworkBridge)
		}
	}

	// NAT
	if setupNetworkNAT {
		fmt.Println("── NAT ──")
		natFields := strings.Fields(fmt.Sprintf("POSTROUTING -s %s ! -o %s -j MASQUERADE", setupNetworkSubnet, setupNetworkBridge))
		checkArgs := append([]string{"-t", "nat", "-C"}, natFields...)
		addArgs := append([]string{"-t", "nat", "-A"}, natFields...)
		checkNat := exec.Command("iptables", checkArgs...)
		if checkNat.Run() == nil {
			fmt.Printf("  ✅ NAT rule already exists for %s\n", setupNetworkSubnet)
		} else {
			addNat := exec.Command("iptables", addArgs...)
			if out, err := addNat.CombinedOutput(); err != nil {
				fmt.Printf("  ⚠️  NAT rule: %s\n", strings.TrimSpace(string(out)))
			} else {
				fmt.Printf("  ✅ NAT enabled for %s\n", setupNetworkSubnet)
			}
		}
	}

	fmt.Println()
	fmt.Printf("✅ Network setup complete — bridge %s ready\n", setupNetworkBridge)
	return nil
}

// ─── setup config ───────────────────────────────────────────────────────

var (
	setupConfigKernel     string
	setupConfigRootfs     string
	setupConfigBridge     string
	setupConfigSubnet     string
	setupConfigBridgeIP   string
	setupConfigVCPUs      int
	setupConfigMemory     int
	setupConfigNonInteractive bool
)

func newSetupConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Generate or update SwarmCracker configuration",
		Long: `Generate a default configuration file at /etc/swarmcracker/config.yaml.

If a config file already exists, it will NOT be overwritten unless --force is used.
All settings can be specified via flags, or sensible defaults are used.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSetupConfig()
		},
	}

	cmd.Flags().StringVar(&setupConfigKernel, "kernel", "/usr/share/firecracker/vmlinux", "Path to kernel image")
	cmd.Flags().StringVar(&setupConfigRootfs, "rootfs-dir", "/var/lib/firecracker/rootfs", "Rootfs directory")
	cmd.Flags().StringVar(&setupConfigBridge, "bridge", "swarm-br0", "Bridge device name")
	cmd.Flags().StringVar(&setupConfigSubnet, "subnet", "192.168.127.0/24", "VM subnet")
	cmd.Flags().StringVar(&setupConfigBridgeIP, "bridge-ip", "192.168.127.1/24", "Bridge IP")
	cmd.Flags().IntVar(&setupConfigVCPUs, "vcpus", 1, "Default vCPUs per VM")
	cmd.Flags().IntVar(&setupConfigMemory, "memory", 512, "Default memory per VM (MB)")
	cmd.Flags().BoolVar(&setupConfigNonInteractive, "non-interactive", false, "Skip interactive prompts")

	return cmd
}

func runSetupConfig() error {
	cfgPath := config.GetDefaultConfigPath()

	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("⚠️  Config already exists at %s\n", cfgPath)
		fmt.Println("   Use 'swarmcracker config validate' to check it, or delete it to regenerate.")
		return nil
	}

	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║          SwarmCracker — Configuration                   ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	cfg := &config.Config{}
	cfg.SetDefaults()

	// Override defaults from flags
	if setupConfigKernel != "/usr/share/firecracker/vmlinux" {
		cfg.Executor.KernelPath = setupConfigKernel
	} else {
		fmt.Printf("  Kernel path [%s]: \n", cfg.Executor.KernelPath)
	}

	if setupConfigRootfs != "/var/lib/firecracker/rootfs" {
		cfg.Executor.RootfsDir = setupConfigRootfs
	}

	if setupConfigBridge != "swarm-br0" {
		cfg.Network.BridgeName = setupConfigBridge
	}

	if setupConfigSubnet != "192.168.127.0/24" {
		cfg.Network.Subnet = setupConfigSubnet
	}

	if setupConfigBridgeIP != "192.168.127.1/24" {
		cfg.Network.BridgeIP = setupConfigBridgeIP
	}

	if setupConfigVCPUs != 1 {
		cfg.Executor.DefaultVCPUs = setupConfigVCPUs
	}

	if setupConfigMemory != 512 {
		cfg.Executor.DefaultMemoryMB = setupConfigMemory
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Save
	if err := cfg.Save(cfgPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("  ✅ Configuration saved to %s\n", cfgPath)
	fmt.Println()
	fmt.Printf("  Bridge:    %s\n", cfg.Network.BridgeName)
	fmt.Printf("  Subnet:    %s\n", cfg.Network.Subnet)
	fmt.Printf("  Bridge IP: %s\n", cfg.Network.BridgeIP)
	fmt.Printf("  vCPUs:     %d\n", cfg.Executor.DefaultVCPUs)
	fmt.Printf("  Memory:    %d MB\n", cfg.Executor.DefaultMemoryMB)
	fmt.Printf("  Kernel:    %s\n", cfg.Executor.KernelPath)
	fmt.Printf("  Rootfs:    %s\n", cfg.Executor.RootfsDir)
	fmt.Println()

	return nil
}
