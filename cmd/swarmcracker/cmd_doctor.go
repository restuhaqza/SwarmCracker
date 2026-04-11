package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// DoctorCheck represents a single diagnostic check with rich output
type DoctorCheck struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Status   string `json:"status"` // "ok", "warning", "error", "skip"
	Message  string `json:"message,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FixHint  string `json:"fix_hint,omitempty"`
	Duration string `json:"duration,omitempty"`
}

// DoctorReport holds the full diagnostic report
type DoctorReport struct {
	Timestamp    string        `json:"timestamp"`
	Hostname     string        `json:"hostname"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	SwarmCracker string        `json:"swarmcracker_version,omitempty"`
	Checks       []DoctorCheck `json:"checks"`
	Summary      DoctorSummary `json:"summary"`
}

// DoctorSummary holds aggregated results
type DoctorSummary struct {
	Total   int `json:"total"`
	OK      int `json:"ok"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
	Skip    int `json:"skip"`
}

type doctorConfig struct {
	JSON    bool
	Quiet   bool
	Verbose bool
}

func newDoctorCommand() *cobra.Command {
	cfg := &doctorConfig{}

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose SwarmCracker system health",
		Long: `Diagnose SwarmCracker system health and configuration.

Checks system dependencies, SwarmCracker services, cluster state,
network configuration, and provides actionable fixes.

Examples:
  swarmcracker doctor
  swarmcracker doctor --json
  swarmcracker doctor --quiet`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cfg)
		},
	}

	cmd.Flags().BoolVar(&cfg.JSON, "json", false, "Output in JSON format")
	cmd.Flags().BoolVarP(&cfg.Quiet, "quiet", "q", false, "Only show warnings and errors")
	cmd.Flags().BoolVarP(&cfg.Verbose, "verbose", "v", false, "Show detailed information")

	return cmd
}

func runDoctor(cfg *doctorConfig) error {
	report := DoctorReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Hostname:  getHostname(),
		OS:        getOSInfo(),
		Arch:      runtime.GOARCH,
	}

	// p prints only when not in JSON mode
	p := func(format string, args ...interface{}) {
		if !cfg.JSON {
			fmt.Printf(format, args...)
		}
	}

	if v := getSwarmCrackerVersion(); v != "" {
		report.SwarmCracker = v
	}

	p("\n🏥 SwarmCracker Doctor\n")
	p(strings.Repeat("─", 50) + "\n")

	// System Requirements
	p("\n📡 System Requirements\n")
	report.Checks = append(report.Checks,
		dc("CPU virtualization (KVM)", "system", checkDoctorKVM, cfg),
		dc("Firecracker binary", "system", checkDoctorFirecracker, cfg),
		dc("Firecracker version", "system", checkDoctorFirecrackerVersion, cfg),
		dc("Kernel image", "system", checkDoctorKernel, cfg),
		dc("TUN/TAP device", "system", checkDoctorTunTap, cfg),
		dc("Jailer binary", "system", checkDoctorJailer, cfg),
	)

	// Resources
	p("\n💾 Resources\n")
	report.Checks = append(report.Checks,
		dc("Available memory", "resources", checkDoctorMemory, cfg),
		dc("CPU cores", "resources", checkDoctorCPU, cfg),
		dc("Disk space (/var/lib)", "resources", checkDoctorDiskSpace, cfg),
	)

	// Networking
	p("\n🌐 Networking\n")
	report.Checks = append(report.Checks,
		dc("Bridge module (br_netfilter)", "network", checkDoctorBridgeModule, cfg),
		dc("Bridge interface (swarm-br0)", "network", checkDoctorBridgeIface, cfg),
		dc("Active TAP devices", "network", checkDoctorTapDevices, cfg),
		dc("Port 4242", "network", checkDoctorPort4242, cfg),
		dc("IP connectivity", "network", checkDoctorIPConnectivity, cfg),
	)

	// Services
	p("\n⚙️  Services\n")
	report.Checks = append(report.Checks,
		dc("Manager service", "services", checkDoctorManagerSvc, cfg),
		dc("Worker service", "services", checkDoctorWorkerSvc, cfg),
		dc("swarmd-firecracker binary", "services", checkDoctorSwarmdBin, cfg),
	)

	// Cluster State
	p("\n🗄️  Cluster State\n")
	report.Checks = append(report.Checks,
		dc("State directory", "cluster", checkDoctorStateDir, cfg),
		dc("Config directory", "cluster", checkDoctorConfigDir, cfg),
		dc("Rootfs directory", "cluster", checkDoctorRootfsDir, cfg),
		dc("Socket directory", "cluster", checkDoctorSocketDir, cfg),
		dc("SwarmKit CA certificates", "cluster", checkDoctorCACerts, cfg),
		dc("Join tokens", "cluster", checkDoctorJoinTokens, cfg),
	)

	// Running VMs
	p("\n🖥️  Running VMs\n")
	report.Checks = append(report.Checks,
		dc("Firecracker processes", "vms", checkDoctorFCProcs, cfg),
		dc("VM state files", "vms", checkDoctorVMStates, cfg),
	)

	// Summarize
	for _, c := range report.Checks {
		report.Summary.Total++
		switch c.Status {
		case "ok":
			report.Summary.OK++
		case "warning":
			report.Summary.Warning++
		case "error":
			report.Summary.Error++
		case "skip":
			report.Summary.Skip++
		}
	}

	if cfg.JSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		printDoctorSummary(&report)
	}

	return nil
}

// dc runs a single doctor check
func dc(name, category string, fn func() DoctorCheck, cfg *doctorConfig) DoctorCheck {
	c := fn()
	c.Name = name
	c.Category = category

	if cfg.Quiet && c.Status == "ok" {
		return c
	}

	if !cfg.JSON {
		printDC(&c, cfg)
	}
	return c
}

func printDC(c *DoctorCheck, cfg *doctorConfig) {
	var icon, color string
	switch c.Status {
	case "ok":
		icon, color = "✓", "\033[0;32m"
	case "warning":
		icon, color = "⚠", "\033[1;33m"
	case "error":
		icon, color = "✗", "\033[0;31m"
	case "skip":
		icon, color = "−", "\033[0;90m"
	}

	fmt.Printf("  %s%s %s\033[0m", color, icon, c.Name)
	if c.Message != "" && (cfg.Verbose || c.Status != "ok") {
		fmt.Printf(" — %s", c.Message)
	}
	fmt.Println()

	if c.Detail != "" && (cfg.Verbose || c.Status == "error") {
		fmt.Printf("    \033[0;90m%s\033[0m\n", c.Detail)
	}
	if c.FixHint != "" && c.Status != "ok" {
		fmt.Printf("    \033[0;36m💡 Fix: %s\033[0m\n", c.FixHint)
	}
}

func printDoctorSummary(r *DoctorReport) {
	fmt.Println()
	fmt.Println(strings.Repeat("─", 50))

	if r.Summary.Error > 0 {
		fmt.Printf("\033[0;31m❌ %d error(s), %d warning(s) found\033[0m\n", r.Summary.Error, r.Summary.Warning)
	} else if r.Summary.Warning > 0 {
		fmt.Printf("\033[1;33m⚠️  System OK with %d warning(s)\033[0m\n", r.Summary.Warning)
	} else {
		fmt.Printf("\033[0;32m✅ All %d checks passed\033[0m\n", r.Summary.OK)
	}

	if r.Summary.Error > 0 || r.Summary.Warning > 0 {
		fmt.Println("\n🔧 Suggested fixes:")
		for _, c := range r.Checks {
			if c.FixHint != "" && c.Status != "ok" {
				fmt.Printf("  • %s\n", c.FixHint)
			}
		}
	}
	fmt.Println()
}

// ── System checks ──

func checkDoctorKVM() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	if _, err := os.Stat("/dev/kvm"); os.IsNotExist(err) {
		c.Status = "error"
		c.Message = "/dev/kvm not found"
		c.FixHint = "Enable KVM in BIOS/UEFI or: sudo modprobe kvm_intel (Intel) / kvm_amd (AMD)"
		return c
	}
	cmd := exec.Command("lsmod")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "kvm") {
		c.Status = "warning"
		c.Message = "KVM module not loaded"
		c.FixHint = "sudo modprobe kvm_intel (Intel) or sudo modprobe kvm_amd (AMD)"
		return c
	}
	c.Message = "KVM available"
	return c
}

func checkDoctorFirecracker() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	path, err := exec.LookPath("firecracker")
	if err != nil {
		c.Status = "error"
		c.Message = "firecracker not found in PATH"
		c.FixHint = "Install: https://github.com/firecracker-microvm/firecracker/releases"
		return c
	}
	c.Message = fmt.Sprintf("found at %s", path)
	return c
}

func checkDoctorFirecrackerVersion() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	path, err := exec.LookPath("firecracker")
	if err != nil {
		c.Status = "skip"
		c.Message = "firecracker not installed"
		return c
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		c.Status = "warning"
		c.Message = "failed to get version"
		c.Detail = strings.TrimSpace(string(out))
		return c
	}
	ver := strings.TrimSpace(string(out))
	c.Message = ver
	// Warn on old versions
	for _, old := range []string{"v1.4", "v1.5", "v1.6", "v1.7", "v1.8"} {
		if strings.Contains(ver, old) {
			c.Status = "warning"
			c.Message = fmt.Sprintf("%s (recommend v1.9+)", ver)
			c.FixHint = "Upgrade: https://github.com/firecracker-microvm/firecracker/releases"
			break
		}
	}
	return c
}

func checkDoctorKernel() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	kernelPath := "/usr/share/firecracker/vmlinux"
	info, err := os.Stat(kernelPath)
	if os.IsNotExist(err) {
		c.Status = "error"
		c.Message = fmt.Sprintf("not found at %s", kernelPath)
		c.FixHint = "Download: https://github.com/firecracker-microvm/firecracker-demo"
		return c
	}
	if info.Size() < 1*1024*1024 {
		c.Status = "warning"
		c.Message = fmt.Sprintf("only %d bytes (may be corrupt)", info.Size())
		c.FixHint = "Re-download the kernel image"
		return c
	}
	c.Message = fmt.Sprintf("%s (%.1f MB)", kernelPath, float64(info.Size())/(1024*1024))
	return c
}

func checkDoctorTunTap() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	if _, err := os.Stat("/dev/net/tun"); os.IsNotExist(err) {
		c.Status = "error"
		c.Message = "/dev/net/tun not found"
		c.FixHint = "sudo modprobe tun && sudo mkdir -p /dev/net && sudo mknod /dev/net/tun c 10 200"
		return c
	}
	c.Message = "/dev/net/tun available"
	return c
}

func checkDoctorJailer() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	path, err := exec.LookPath("jailer")
	if err != nil {
		c.Status = "warning"
		c.Message = "jailer not found (optional, for resource isolation)"
		c.FixHint = "Download jailer from Firecracker releases"
		return c
	}
	c.Message = fmt.Sprintf("found at %s", path)
	return c
}

// ── Resource checks ──

func checkDoctorMemory() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		c.Status = "warning"
		return c
	}
	var memTotal, memAvail uint64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &memAvail)
		}
	}
	c.Message = fmt.Sprintf("%.1f GB total, %.1f GB available",
		float64(memTotal)/(1024*1024), float64(memAvail)/(1024*1024))
	if memAvail < 512*1024 {
		c.Status = "warning"
		c.Message += " — low memory"
		c.FixHint = "Free memory or increase VM resources"
	}
	return c
}

func checkDoctorCPU() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		c.Status = "warning"
		return c
	}
	cores := strings.Count(string(data), "processor\t:")
	c.Message = fmt.Sprintf("%d CPU core(s)", cores)
	if cores < 2 {
		c.Status = "warning"
		c.Message += " — recommend 2+ cores for VMs"
	}
	return c
}

func checkDoctorDiskSpace() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	out, err := exec.Command("df", "-k", "/var/lib").Output()
	if err != nil {
		c.Status = "skip"
		return c
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		c.Status = "skip"
		return c
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		c.Status = "skip"
		return c
	}
	avail, _ := strconv.ParseUint(fields[3], 10, 64)
	availMB := avail / 1024
	c.Message = fmt.Sprintf("%.1f GB available on /var/lib", float64(availMB)/1024)
	if availMB < 1024 {
		c.Status = "warning"
		c.Message += " — low disk space"
		c.FixHint = "Free disk space — rootfs images need ~50-200MB each"
	}
	return c
}

// ── Network checks ──

func checkDoctorBridgeModule() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	if err := exec.Command("modprobe", "-n", "br_netfilter").Run(); err != nil {
		c.Status = "warning"
		c.Message = "br_netfilter module not available"
		c.FixHint = "sudo modprobe br_netfilter"
		return c
	}
	out, _ := exec.Command("lsmod").Output()
	if !strings.Contains(string(out), "br_netfilter") {
		c.Status = "warning"
		c.Message = "available but not loaded"
		c.FixHint = "sudo modprobe br_netfilter"
		return c
	}
	c.Message = "br_netfilter loaded"
	return c
}

func checkDoctorBridgeIface() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	if err := exec.Command("ip", "link", "show", "swarm-br0").Run(); err != nil {
		c.Message = "swarm-br0 not created (normal if not initialized)"
		return c
	}
	c.Status = "ok"
	c.Message = "swarm-br0 exists"
	ipOut, _ := exec.Command("ip", "addr", "show", "swarm-br0").Output()
	for _, line := range strings.Split(string(ipOut), "\n") {
		if strings.Contains(line, "inet ") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				c.Message += fmt.Sprintf(" (%s)", f[1])
			}
		}
	}
	return c
}

func checkDoctorTapDevices() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	out, err := exec.Command("ip", "link", "show", "type", "tap").Output()
	if err != nil {
		c.Message = "no TAP devices"
		return c
	}
	taps := parseTapDevices(string(out))
	if len(taps) == 0 {
		c.Message = "no active TAP devices"
		return c
	}
	c.Message = fmt.Sprintf("%d active TAP device(s)", len(taps))
	c.Detail = strings.Join(taps, ", ")
	return c
}

func checkDoctorPort4242() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	out, _ := exec.Command("ss", "-tlnp", "sport", "=", "4242").Output()
	if strings.Contains(string(out), "4242") {
		if strings.Contains(string(out), "swarmd-firecracker") || strings.Contains(string(out), "swarmcracker") {
			c.Message = "port 4242 in use by SwarmCracker"
		} else {
			c.Status = "warning"
			c.Message = "port 4242 in use by another process"
			c.Detail = strings.TrimSpace(string(out))
			c.FixHint = "Stop the conflicting process or use --listen-addr"
		}
	} else {
		c.Message = "port 4242 available"
	}
	return c
}

func checkDoctorIPConnectivity() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	out, err := exec.Command("ip", "route").Output()
	if err != nil || !strings.Contains(string(out), "default") {
		c.Status = "warning"
		c.Message = "no default route"
		c.FixHint = "Check network configuration"
		return c
	}
	ipOut, _ := exec.Command("hostname", "-I").Output()
	ips := strings.Fields(strings.TrimSpace(string(ipOut)))
	if len(ips) > 0 {
		c.Message = fmt.Sprintf("connected (%s)", ips[0])
	} else {
		c.Message = "connected"
	}
	return c
}

// ── Service checks ──

func checkDoctorManagerSvc() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	if _, err := os.Stat("/etc/systemd/system/swarmcracker-manager.service"); os.IsNotExist(err) {
		c.Message = "manager service not installed"
		return c
	}
	out, err := exec.Command("systemctl", "is-active", "swarmcracker-manager.service").Output()
	if err != nil {
		c.Status = "error"
		c.Message = fmt.Sprintf("manager service is %s", strings.TrimSpace(string(out)))
		c.FixHint = "sudo swarmcracker deinit && sudo swarmcracker init"
		return c
	}
	c.Status = "ok"
	c.Message = fmt.Sprintf("manager service is %s", strings.TrimSpace(string(out)))
	return c
}

func checkDoctorWorkerSvc() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	if _, err := os.Stat("/etc/systemd/system/swarmcracker-worker.service"); os.IsNotExist(err) {
		c.Message = "worker service not installed"
		return c
	}
	out, err := exec.Command("systemctl", "is-active", "swarmcracker-worker.service").Output()
	if err != nil {
		c.Status = "error"
		c.Message = fmt.Sprintf("worker service is %s", strings.TrimSpace(string(out)))
		c.FixHint = "sudo swarmcracker leave && sudo swarmcracker join <addr> --token <token>"
		return c
	}
	c.Status = "ok"
	c.Message = fmt.Sprintf("worker service is %s", strings.TrimSpace(string(out)))
	return c
}

func checkDoctorSwarmdBin() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	path, err := exec.LookPath("swarmd-firecracker")
	if err != nil {
		for _, loc := range []string{"/usr/local/bin/swarmd-firecracker", "/usr/bin/swarmd-firecracker"} {
			if _, err := os.Stat(loc); err == nil {
				path = loc
				break
			}
		}
	}
	if path == "" {
		c.Status = "error"
		c.Message = "swarmd-firecracker not found"
		c.FixHint = "Run the install script or build from source"
		return c
	}
	c.Message = fmt.Sprintf("found at %s", path)
	return c
}

// ── Cluster state checks ──

func checkDoctorStateDir() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	stateDir := "/var/lib/swarmkit"
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		c.Status = "skip"
		c.Message = fmt.Sprintf("%s does not exist (normal if not initialized)", stateDir)
		return c
	}
	entries, _ := os.ReadDir(stateDir)
	c.Message = fmt.Sprintf("%s (%d items)", stateDir, len(entries))
	if len(entries) > 0 {
		c.Detail = formatEntries(entries)
	}
	return c
}

func checkDoctorConfigDir() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	configDir := "/etc/swarmcracker"
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		c.Status = "skip"
		c.Message = fmt.Sprintf("%s does not exist", configDir)
		return c
	}
	entries, _ := os.ReadDir(configDir)
	c.Message = fmt.Sprintf("%s (%d items)", configDir, len(entries))
	if len(entries) > 0 {
		c.Detail = formatEntries(entries)
	}
	return c
}

func checkDoctorRootfsDir() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	rootfsDir := "/var/lib/firecracker/rootfs"
	if _, err := os.Stat(rootfsDir); os.IsNotExist(err) {
		c.Status = "skip"
		c.Message = fmt.Sprintf("%s does not exist", rootfsDir)
		return c
	}
	entries, _ := os.ReadDir(rootfsDir)
	c.Message = fmt.Sprintf("%s (%d rootfs images)", rootfsDir, len(entries))
	if len(entries) > 0 {
		c.Detail = formatEntries(entries)
	}
	return c
}

func checkDoctorSocketDir() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	socketDir := "/var/run/firecracker"
	if _, err := os.Stat(socketDir); os.IsNotExist(err) {
		c.Message = fmt.Sprintf("%s does not exist", socketDir)
		return c
	}
	entries, _ := os.ReadDir(socketDir)
	active := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sock") {
			active++
		}
	}
	if active > 0 {
		c.Status = "ok"
		c.Message = fmt.Sprintf("%d active socket(s)", active)
	} else {
		c.Message = "no active sockets"
	}
	return c
}

func checkDoctorCACerts() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	caDir := "/var/lib/swarmkit"
	if _, err := os.Stat(caDir); os.IsNotExist(err) {
		c.Message = "no cluster state"
		return c
	}
	found := []string{}
	for _, name := range []string{"ca.crt", "worker.crt", "manager.crt"} {
		if _, err := os.Stat(filepath.Join(caDir, "certificates", name)); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		c.Message = "no CA certificates found"
		return c
	}
	c.Status = "ok"
	c.Message = fmt.Sprintf("%d certificate(s) found", len(found))
	c.Detail = strings.Join(found, ", ")
	return c
}

func checkDoctorJoinTokens() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	for _, p := range []string{
		"/var/lib/swarmkit/join-tokens.txt",
		"/var/lib/swarmkit/join-tokens",
	} {
		if info, err := os.Stat(p); err == nil {
			c.Status = "ok"
			c.Message = fmt.Sprintf("found at %s", p)
			if info.ModTime().After(time.Now().Add(-24 * time.Hour)) {
				c.Message += " (recent)"
			} else {
				c.Status = "warning"
				c.Message += fmt.Sprintf(" (old, modified %s)", info.ModTime().Format("Jan 2 15:04"))
				c.FixHint = "Regenerate: sudo swarmcracker deinit && sudo swarmcracker init"
			}
			return c
		}
	}
	c.Message = "no join tokens found"
	return c
}

// ── VM checks ──

func checkDoctorFCProcs() DoctorCheck {
	c := DoctorCheck{Status: "ok"}
	out, err := exec.Command("pgrep", "-c", "-f", "firecracker").Output()
	if err != nil {
		c.Message = "no Firecracker processes running"
		return c
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	c.Message = fmt.Sprintf("%d Firecracker process(es) running", n)
	if n > 10 {
		c.Status = "warning"
		c.Message += " — high count"
	}
	return c
}

func checkDoctorVMStates() DoctorCheck {
	c := DoctorCheck{Status: "skip"}
	stateDir := "/var/lib/swarmkit"
	if _, err := os.Stat(stateDir); os.IsNotExist(err) {
		c.Message = "no state directory"
		return c
	}
	stateFile := filepath.Join(stateDir, "vm-states.json")
	if _, err := os.Stat(stateFile); err == nil {
		data, err := os.ReadFile(stateFile)
		if err == nil {
			var vms []map[string]interface{}
			if json.Unmarshal(data, &vms) == nil {
				c.Status = "ok"
				c.Message = fmt.Sprintf("%d VM state(s) tracked", len(vms))
				return c
			}
		}
		c.Message = fmt.Sprintf("state file: %s", filepath.Base(stateFile))
		return c
	}
	c.Message = "no VM state files found"
	return c
}

// ── Helpers ──

func getHostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "unknown"
}

func getOSInfo() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
		}
	}
	return runtime.GOOS
}

func getSwarmCrackerVersion() string {
	out, err := exec.Command("swarmcracker", "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func formatEntries(entries []os.DirEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + fmt.Sprintf(" (+%d more)", len(names)-5)
	}
	return strings.Join(names, ", ")
}
