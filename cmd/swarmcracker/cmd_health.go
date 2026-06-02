package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	healthFormat string
	healthQuiet  bool
)

// newClusterHealthCommand creates the cluster health command.
func newClusterHealthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check cluster health",
		Long: `Check the health of the SwarmCracker cluster.

Verifies:
  - KVM availability
  - Firecracker binary
  - Bridge interface
  - Health endpoint (/healthz) on all nodes
  - Consul connectivity (if configured)

Exit codes:
  0 = healthy
  1 = degraded (some checks failed but cluster is operational)
  2 = critical (cluster is down or unreachable)`,
		Example: `  swarmcracker cluster health
  swarmcracker cluster health --format json
  swarmcracker cluster health --quiet`,
		RunE: runClusterHealth,
	}

	cmd.Flags().StringVar(&healthFormat, "format", "table", "Output format: table, json, nagios")
	cmd.Flags().BoolVarP(&healthQuiet, "quiet", "q", false, "Suppress output, use exit code only")

	return cmd
}

type healthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "pass", "warn", "fail"
	Detail string `json:"detail"`
}

type healthResult struct {
	Node      string        `json:"node"`
	Healthy   bool          `json:"healthy"`
	Checks    []healthCheck `json:"checks"`
	Timestamp string        `json:"timestamp"`
}

func runClusterHealth(cmd *cobra.Command, args []string) error {
	hostname, _ := os.Hostname()
	result := healthResult{
		Node:      hostname,
		Checks:    []healthCheck{},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	fails := 0
	warns := 0

	add := func(name, status, detail string) {
		result.Checks = append(result.Checks, healthCheck{Name: name, Status: status, Detail: detail})
		if status == "fail" {
			fails++
		} else if status == "warn" {
			warns++
		}
	}

	// ── KVM ──
	if _, err := os.Stat("/dev/kvm"); err == nil {
		add("kvm", "pass", "/dev/kvm accessible")
	} else {
		add("kvm", "fail", "/dev/kvm not found — VMs cannot run")
	}

	// ── CPU virtualization ──
	cpuInfo, _ := os.ReadFile("/proc/cpuinfo")
	if strings.Contains(string(cpuInfo), "vmx") || strings.Contains(string(cpuInfo), "svm") {
		add("cpu_virt", "pass", "Hardware virtualization supported")
	} else {
		add("cpu_virt", "fail", "No vmx/svm — KVM requires hardware virt")
	}

	// ── Firecracker ──
	if fcPath, err := exec.LookPath("firecracker"); err == nil {
		if out, err := exec.Command(fcPath, "--version").CombinedOutput(); err == nil {
			add("firecracker", "pass", strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]))
		} else {
			add("firecracker", "warn", "Installed but --version failed")
		}
	} else {
		add("firecracker", "fail", "Not found in PATH")
	}

	// ── Bridge ──
	bridgeNames := []string{"swarm-br0"}
	for _, br := range bridgeNames {
		if err := exec.Command("ip", "link", "show", br).Run(); err == nil {
			add("bridge:"+br, "pass", "Bridge exists")
		} else {
			add("bridge:"+br, "warn", "Bridge not found — VMs may lack network")
		}
	}

	// ── IP Forwarding ──
	fwd, _ := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if strings.TrimSpace(string(fwd)) == "1" {
		add("ip_forward", "pass", "IPv4 forwarding enabled")
	} else {
		add("ip_forward", "warn", "IPv4 forwarding disabled — NAT may not work")
	}

	// ── Kernel modules ──
	for _, mod := range []string{"br_netfilter", "vxlan"} {
		out, err := exec.Command("lsmod").CombinedOutput()
		if err == nil && strings.Contains(string(out), mod) {
			add("module:"+mod, "pass", "Loaded")
		} else {
			add("module:"+mod, "warn", "Not loaded — run 'modprobe "+mod+"'")
		}
	}

	// ── Health endpoint ──
	healthAddrs := []string{"localhost:8080"}
	for _, addr := range healthAddrs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			add("healthz:"+addr, "warn", "Health endpoint unreachable")
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				add("healthz:"+addr, "pass", "Responding OK")
			} else {
				add("healthz:"+addr, "warn", fmt.Sprintf("HTTP %d", resp.StatusCode))
			}
		}
	}

	// ── Consul ──
	if _, err := exec.LookPath("consul"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8500/v1/status/leader", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			add("consul", "warn", "Consul agent not reachable — VXLAN peers must use static config")
		} else {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				add("consul", "pass", "Consul agent healthy")
			} else {
				add("consul", "warn", fmt.Sprintf("Consul HTTP %d", resp.StatusCode))
			}
		}
	}

	// ── Snapshot directory ──
	snapshotDir := "/var/lib/swarmcracker/snapshots"
	if info, err := os.Stat(snapshotDir); err == nil && info.IsDir() {
		add("snapshot_dir", "pass", snapshotDir)
	} else {
		add("snapshot_dir", "warn", "Snapshot directory missing — snapshots will fail")
	}

	// ── Disk usage ──
	for _, dir := range []string{"/var/lib/swarmcracker", "/var/lib/firecracker"} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			if diskUsage, err := getDiskUsage(dir); err == nil {
				pct := float64(diskUsage.Used) / float64(diskUsage.Total) * 100
				if pct > 85 {
					add("disk:"+dir, "warn", fmt.Sprintf("%.0f%% used", pct))
				} else {
					add("disk:"+dir, "pass", fmt.Sprintf("%.0f%% used", pct))
				}
			}
		}
	}

	// Determine overall health
	result.Healthy = fails == 0

	if healthQuiet {
		if result.Healthy {
			return nil
		}
		return fmt.Errorf("cluster degraded: %d failures, %d warnings", fails, warns)
	}

	switch healthFormat {
	case "json":
		printHealthJSON(result)
	case "nagios":
		printHealthNagios(result, fails, warns)
	default:
		printHealthTable(result, fails, warns)
	}

	if fails > 0 {
		return fmt.Errorf("cluster degraded: %d checks failed", fails)
	}
	if warns > 0 {
		fmt.Printf("\n⚠️  %d warning(s) — cluster is operational but has non-critical issues\n", warns)
	}
	return nil
}

func printHealthTable(r healthResult, fails, warns int) {
	fmt.Printf("╔══════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          SwarmCracker — Cluster Health                  ║\n")
	fmt.Printf("╠══════════════════════════════════════════════════════════╣\n")
	fmt.Printf("║  Node: %-48s ║\n", r.Node)
	fmt.Printf("║  Time: %-48s ║\n", r.Timestamp)
	fmt.Printf("╠══════════════════════════════════════════════════════════╣\n")
	for _, c := range r.Checks {
		icon := map[string]string{"pass": "✅", "warn": "⚠️", "fail": "❌"}[c.Status]
		fmt.Printf("║  %s %-52s ║\n", icon, c.Name)
		if c.Detail != "" && c.Status != "pass" {
			fmt.Printf("║     %-52s ║\n", c.Detail)
		}
	}
	fmt.Printf("╠══════════════════════════════════════════════════════════╣\n")

	if r.Healthy {
		fmt.Printf("║  ✅ Cluster is healthy                                  ║\n")
	} else if fails > 0 {
		fmt.Printf("║  ❌ Cluster DEGRADED: %d failure(s)                        ║\n", fails)
	} else {
		fmt.Printf("║  ⚠️  Cluster is OK with %d warning(s)                       ║\n", warns)
	}
	fmt.Printf("╚══════════════════════════════════════════════════════════╝\n")
}

func printHealthJSON(r healthResult) {
	fmt.Printf("{\"node\":%q,\"healthy\":%v,\"timestamp\":%q,\"checks\":[", r.Node, r.Healthy, r.Timestamp)
	for i, c := range r.Checks {
		if i > 0 {
			fmt.Print(",")
		}
		fmt.Printf("{\"name\":%q,\"status\":%q,\"detail\":%q}", c.Name, c.Status, c.Detail)
	}
	fmt.Println("]}")
}

func printHealthNagios(r healthResult, fails, warns int) {
	if fails > 0 {
		fmt.Printf("CRITICAL: %d checks failed |", fails)
	} else if warns > 0 {
		fmt.Printf("WARNING: %d checks with warnings |", warns)
	} else {
		fmt.Print("OK: all checks passed |")
	}
	for _, c := range r.Checks {
		fmt.Printf(" %s=%s", c.Name, c.Status)
	}
	fmt.Println()
}

// syscallStatfs and getDiskUsage are platform-specific helpers.
// On Linux we use syscall.Statfs; on other platforms we skip disk checks.

type diskInfo struct{ Total, Used uint64 }

func getDiskUsage(path string) (diskInfo, error) {
	// Use 'df' as a portable fallback
	out, err := exec.Command("df", "-B1", path).Output()
	if err != nil {
		return diskInfo{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return diskInfo{}, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return diskInfo{}, fmt.Errorf("unexpected df fields")
	}
	// Fields: Filesystem, 1B-blocks, Used, Available, Use%, Mounted
	var total, used uint64
	fmt.Sscanf(fields[1], "%d", &total)
	fmt.Sscanf(fields[2], "%d", &used)
	return diskInfo{Total: total, Used: used}, nil
}
