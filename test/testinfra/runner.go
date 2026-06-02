// Package testinfra provides infrastructure validation for SwarmCracker testing.
//
// Two usage modes:
//
// 1. Test mode (CI / `go test`):
//
//	go test -v -json ./test/testinfra/...     # full JSON output
//	go test -v ./test/testinfra/...             # human-readable
//	go test -short ./test/testinfra/...         # skip build/unit re-check
//
// 2. Library mode (run.sh / programmatic):
//
//	runner := testinfra.NewRunner()
//	report := runner.Run(context.Background())
//	if !report.Ready { os.Exit(1) }
package testinfra

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

// CheckSeverity marks whether a failing check blocks further testing.
type CheckSeverity string

const (
	Required CheckSeverity = "required" // blocks CI / E2E
	Optional CheckSeverity = "optional" // warns but continues
	Info     CheckSeverity = "info"     // informational only
)

// CheckResult holds a single check outcome.
type CheckResult struct {
	Name     string        `json:"name"`
	Severity CheckSeverity `json:"severity"`
	Status   string        `json:"status"` // "pass", "fail", "skip"
	Message  string        `json:"message"`
	Detail   string        `json:"detail,omitempty"`
	Duration string        `json:"duration,omitempty"`
}

// InfraReport is the aggregate output from all checks.
type InfraReport struct {
	Timestamp string        `json:"timestamp"`
	Hostname  string        `json:"hostname"`
	Arch      string        `json:"arch"`
	OS        string        `json:"os"`
	Results   []CheckResult `json:"results"`
	Passed    int           `json:"passed"`
	Failed    int           `json:"failed"`
	Skipped   int           `json:"skipped"`
	Ready     bool          `json:"ready"`
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// Runner executes infrastructure checks independently of testing.T.
type Runner struct {
	projectRoot string
	checks      []checkDef
}

type checkDef struct {
	name     string
	severity CheckSeverity
	fn       func(context.Context) (status, message, detail string)
}

type status string

const (
	passStatus = "pass"
	failStatus = "fail"
	skipStatus = "skip"
)

// NewRunner creates an infra runner. It auto-discovers the project root.
func NewRunner() *Runner {
	return &Runner{
		projectRoot: findProjectRoot(),
		checks:      defaultChecks(),
	}
}

// NewRunnerWithRoot allows overriding the project root (useful in tests).
func NewRunnerWithRoot(root string) *Runner {
	return &Runner{
		projectRoot: root,
		checks:      defaultChecks(),
	}
}

// Run executes all checks and returns the report.
func (r *Runner) Run(ctx context.Context) InfraReport {
	hostname, _ := os.Hostname()
	report := InfraReport{
		Timestamp: time.Now().Format(time.RFC3339),
		Hostname:  hostname,
		Arch:      runtime.GOARCH,
		OS:        runtime.GOOS,
	}

	for _, c := range r.checks {
		start := time.Now()
		st, msg, detail := c.fn(ctx)
		dur := time.Since(start).Round(time.Millisecond)

		result := CheckResult{
			Name:     c.name,
			Severity: c.severity,
			Status:   st,
			Message:  msg,
			Detail:   detail,
			Duration: dur.String(),
		}
		report.Results = append(report.Results, result)

		switch st {
		case passStatus:
			report.Passed++
		case failStatus:
			report.Failed++
		case skipStatus:
			report.Skipped++
		}
	}

	// Ready = no *required* checks failed
	report.Ready = true
	for _, r := range report.Results {
		if r.Severity == Required && r.Status == failStatus {
			report.Ready = false
			break
		}
	}

	return report
}

// PrintText writes a human-readable report to stdout (for shell scripts).
func (r *InfraReport) PrintText() {
	icons := map[string]string{
		passStatus: "✅",
		failStatus: "❌",
		skipStatus: "⚠️ ",
	}

	fmt.Println("=== Infrastructure Check ===")
	fmt.Printf("Host: %s | Arch: %s | OS: %s\n", r.Hostname, r.Arch, r.OS)
	fmt.Println()

	for _, c := range r.Results {
		icon := icons[c.Status]
		sev := ""
		if c.Severity == Required {
			sev = "[required]"
		}
		fmt.Printf("  %s %-30s %-10s %s\n", icon, c.Name, sev, c.Message)
		if c.Detail != "" {
			fmt.Printf("    Detail: %s\n", c.Detail)
		}
	}

	fmt.Println()
	fmt.Printf("Passed: %d  Failed: %d  Skipped: %d\n", r.Passed, r.Failed, r.Skipped)
	if r.Ready {
		fmt.Println("✅ All required checks passed — ready for testing.")
	} else {
		fmt.Println("❌ Some required checks failed — fix before running tests.")
	}
}

// PrintJSON writes the report as JSON to stdout.
func (r *InfraReport) PrintJSON() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}

// ---------------------------------------------------------------------------
// Check definitions
// ---------------------------------------------------------------------------

func defaultChecks() []checkDef {
	return []checkDef{
		// ── Required checks ──────────────────────────────────────
		{name: "GoVersion", severity: Required, fn: checkGoVersion},
		{name: "Architecture", severity: Required, fn: checkArchitecture},
		{name: "OperatingSystem", severity: Required, fn: checkOS},
		{name: "DiskSpace", severity: Required, fn: checkDiskSpace},
		{name: "Memory", severity: Required, fn: checkMemory},

		// ── Optional checks (warn but continue) ─────────────────
		{name: "KVM", severity: Optional, fn: checkKVM},
		{name: "Firecracker", severity: Optional, fn: checkFirecrackerBinary},
		{name: "FirecrackerKernel", severity: Optional, fn: checkFirecrackerKernel},
		{name: "Swarmd", severity: Optional, fn: checkSwarmd},
		{name: "Swarmctl", severity: Optional, fn: checkSwarmctl},
		{name: "SwarmCracker", severity: Optional, fn: checkSwarmCracker},
		{name: "ContainerRuntime", severity: Optional, fn: checkContainerRuntime},
		{name: "NetworkPermissions", severity: Optional, fn: checkNetworkPermissions},
		{name: "VXLAN", severity: Optional, fn: checkVXLAN},
	}
}

// ---------------------------------------------------------------------------
// Required checks
// ---------------------------------------------------------------------------

func checkGoVersion(_ context.Context) (string, string, string) {
	cmd := exec.Command("go", "version")
	out, err := cmd.Output()
	if err != nil {
		return failStatus, "Go not found", err.Error()
	}
	v := strings.TrimSpace(string(out))
	// Check minimum version (1.25+)
	if strings.Contains(v, "go1.20") || strings.Contains(v, "go1.19") || strings.Contains(v, "go1.18") {
		return failStatus, "Go >= 1.25 required", v
	}
	return passStatus, v, ""
}

func checkArchitecture(_ context.Context) (string, string, string) {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return failStatus, fmt.Sprintf("unsupported: %s (need amd64 or arm64)", arch), ""
	}
	return passStatus, arch, ""
}

func checkOS(_ context.Context) (string, string, string) {
	if runtime.GOOS != "linux" {
		return failStatus, fmt.Sprintf("unsupported: %s (Linux required)", runtime.GOOS), ""
	}
	return passStatus, "linux", ""
}

func checkDiskSpace(_ context.Context) (string, string, string) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(".", &stat); err != nil {
		return skipStatus, "cannot check disk space", err.Error()
	}
	gb := float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024)
	if gb < 5.0 {
		return failStatus, fmt.Sprintf("insufficient: %.1f GB (5 GB required)", gb), ""
	}
	return passStatus, fmt.Sprintf("%.1f GB available", gb), ""
}

func checkMemory(_ context.Context) (string, string, string) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return skipStatus, "cannot read /proc/meminfo", err.Error()
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				gb := float64(kb) / (1024 * 1024)
				if gb < 4.0 {
					return failStatus, fmt.Sprintf("insufficient: %.1f GB (4 GB recommended)", gb), ""
				}
				return passStatus, fmt.Sprintf("%.1f GB total", gb), ""
			}
		}
	}
	return skipStatus, "could not determine memory size", ""
}

// ---------------------------------------------------------------------------
// Optional checks
// ---------------------------------------------------------------------------

func checkKVM(_ context.Context) (string, string, string) {
	info, err := os.Stat("/dev/kvm")
	if err != nil {
		return skipStatus, "/dev/kvm not found", "KVM module not loaded or device missing"
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return failStatus, "/dev/kvm is not a character device", ""
	}
	// Test access
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return skipStatus, "/dev/kvm exists but not accessible", "try: sudo usermod -aG kvm $USER && newgrp kvm"
	}
	f.Close()
	return passStatus, "/dev/kvm accessible", ""
}

func checkFirecrackerBinary(_ context.Context) (string, string, string) {
	out, err := exec.Command("firecracker", "--version").CombinedOutput()
	if err != nil {
		return skipStatus, "not found", "install: https://github.com/firecracker-microvm/firecracker/releases"
	}
	return passStatus, strings.TrimSpace(string(out)), ""
}

func checkFirecrackerKernel(_ context.Context) (string, string, string) {
	paths := []string{
		os.ExpandEnv("$HOME/.local/share/firecracker/vmlinux"),
		"/usr/share/firecracker/vmlinux",
		"/boot/vmlinux",
		"/var/lib/firecracker/vmlinux",
	}
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			mb := float64(info.Size()) / (1024 * 1024)
			return passStatus, fmt.Sprintf("%s (%.1f MB)", p, mb), ""
		}
	}
	return skipStatus, "kernel not found", "download from firecracker releases"
}

func checkSwarmd(_ context.Context) (string, string, string) {
	p, err := exec.LookPath("swarmd")
	if err != nil {
		return skipStatus, "not found", "go install github.com/moby/swarmkit/cmd/swarmd@latest"
	}
	return passStatus, p, ""
}

func checkSwarmctl(_ context.Context) (string, string, string) {
	p, err := exec.LookPath("swarmctl")
	if err != nil {
		return skipStatus, "not found", "go install github.com/moby/swarmkit/cmd/swarmctl@latest"
	}
	return passStatus, p, ""
}

func checkSwarmCracker(_ context.Context) (string, string, string) {
	p, err := exec.LookPath("swarmcracker")
	if err != nil {
		return skipStatus, "not found", "run 'make all' from project root"
	}
	return passStatus, p, ""
}

func checkContainerRuntime(_ context.Context) (string, string, string) {
	if p, err := exec.LookPath("docker"); err == nil {
		out, _ := exec.Command("docker", "--version").Output()
		return passStatus, "docker: " + strings.TrimSpace(string(out)), p
	}
	if p, err := exec.LookPath("podman"); err == nil {
		out, _ := exec.Command("podman", "--version").Output()
		return passStatus, "podman: " + strings.TrimSpace(string(out)), p
	}
	return skipStatus, "no container runtime", "docker or podman required for image operations"
}

func checkNetworkPermissions(ctx context.Context) (string, string, string) {
	br := fmt.Sprintf("test-br-%d", time.Now().UnixNano())
	cmd := exec.CommandContext(ctx, "ip", "link", "add", br, "type", "bridge")
	if err := cmd.Run(); err != nil {
		return skipStatus, "cannot create bridge (may need root)", err.Error()
	}
	// Cleanup
	_ = exec.Command("ip", "link", "delete", br).Run()
	return passStatus, "bridge creation OK", ""
}

func checkVXLAN(ctx context.Context) (string, string, string) {
	// Check if vxlan module is available
	out, err := exec.CommandContext(ctx, "lsmod").Output()
	if err == nil && strings.Contains(string(out), "vxlan ") {
		return passStatus, "VXLAN module loaded", ""
	}
	// Try loading
	if err := exec.CommandContext(ctx, "modprobe", "vxlan").Run(); err != nil {
		return skipStatus, "VXLAN module not available", err.Error()
	}
	return passStatus, "VXLAN module loaded", ""
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
