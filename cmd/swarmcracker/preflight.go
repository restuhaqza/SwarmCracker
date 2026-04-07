package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// PreflightCheck represents a single pre-flight check
type PreflightCheck struct {
	Name     string
	Check    func() error
	Required bool
}

// PreflightResult holds the result of all pre-flight checks
type PreflightResult struct {
	Passed  int
	Failed  int
	Warning int
	Checks  []CheckResult
}

// CheckResult holds the result of a single check
type CheckResult struct {
	Name     string
	Passed   bool
	Warning  bool
	Error    error
	Message  string
	Duration time.Duration
}

// RunPreflightChecks runs all pre-flight checks and returns results
func RunPreflightChecks(mode string) (*PreflightResult, error) {
	result := &PreflightResult{}
	
	// Define checks based on mode
	checks := getChecksForMode(mode)
	
	for _, check := range checks {
		start := time.Now()
		checkResult := CheckResult{
			Name:   check.Name,
			Passed: true,
		}
		
		err := check.Check()
		checkResult.Duration = time.Since(start)
		
		if err != nil {
			checkResult.Error = err
			if check.Required {
				checkResult.Passed = false
				result.Failed++
			} else {
				checkResult.Warning = true
				result.Warning++
			}
		} else {
			result.Passed++
		}
		
		result.Checks = append(result.Checks, checkResult)
	}
	
	return result, nil
}

// getChecksForMode returns the appropriate checks for the given mode
func getChecksForMode(mode string) []PreflightCheck {
	baseChecks := []PreflightCheck{
		{
			Name:     "KVM available",
			Check:    checkKVMAvailableLocal,
			Required: true,
		},
		{
			Name:     "Firecracker installed",
			Check:    checkFirecrackerInstalledLocal,
			Required: true,
		},
		{
			Name:     "Kernel image exists",
			Check:    checkKernelExists,
			Required: true,
		},
		{
			Name:     "Bridge can be created",
			Check:    checkBridgeCapability,
			Required: true,
		},
		{
			Name:     "Sufficient memory",
			Check:    checkSufficientMemory,
			Required: false,
		},
		{
			Name:     "Port 4242 available",
			Check:    checkPortAvailable,
			Required: false,
		},
	}
	
	if mode == "join" {
		baseChecks = append(baseChecks, PreflightCheck{
			Name:     "Manager connectivity",
			Check:    checkManagerConnectivity,
			Required: true,
		})
	}
	
	return baseChecks
}

// checkKVMAvailableLocal verifies KVM is available (local version)
func checkKVMAvailableLocal() error {
	if _, err := os.Stat("/dev/kvm"); os.IsNotExist(err) {
		return fmt.Errorf("/dev/kvm not found - KVM not available")
	}
	
	// Check if KVM modules are loaded
	cmd := exec.Command("lsmod")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check loaded modules: %w", err)
	}
	
	if !strings.Contains(string(output), "kvm") {
		return fmt.Errorf("KVM modules not loaded - run: sudo modprobe kvm")
	}
	
	return nil
}

// checkFirecrackerInstalledLocal verifies Firecracker is installed (local version)
func checkFirecrackerInstalledLocal() error {
	_, err := exec.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker not found in PATH - install Firecracker v1.14+")
	}
	
	// Check version
	cmd := exec.Command("firecracker", "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get Firecracker version: %w", err)
	}
	
	version := strings.TrimSpace(string(output))
	log.Debug().Str("version", version).Msg("Firecracker version")
	
	return nil
}

// checkKernelExists verifies kernel image exists
func checkKernelExists() error {
	defaultKernel := "/usr/share/firecracker/vmlinux"
	if _, err := os.Stat(defaultKernel); os.IsNotExist(err) {
		return fmt.Errorf("kernel not found at %s - download or specify --kernel", defaultKernel)
	}
	return nil
}

// checkBridgeCapability verifies bridge networking is possible
func checkBridgeCapability() error {
	// Check if br_netfilter module can be loaded
	cmd := exec.Command("modprobe", "-n", "br_netfilter")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bridge networking not supported - br_netfilter module unavailable")
	}
	
	// Check if ip command is available
	if _, err := exec.LookPath("ip"); err != nil {
		return fmt.Errorf("ip command not found - install iproute2")
	}
	
	return nil
}

// checkSufficientMemory verifies adequate memory is available
func checkSufficientMemory() error {
	// Read /proc/meminfo
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return fmt.Errorf("failed to read memory info: %w", err)
	}
	
	var memTotal, memAvailable uint64
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "MemTotal:") {
			fmt.Sscanf(line, "MemTotal: %d kB", &memTotal)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			fmt.Sscanf(line, "MemAvailable: %d kB", &memAvailable)
		}
	}
	
	// Warn if less than 1GB available
	if memAvailable < 1024*1024 {
		return fmt.Errorf("low memory: only %d MB available (recommend 1GB+)", memAvailable/1024)
	}
	
	log.Debug().Uint64("available_mb", memAvailable/1024).Msg("Memory check passed")
	return nil
}

// checkPortAvailable verifies port 4242 is not in use
func checkPortAvailable() error {
	cmd := exec.Command("ss", "-tlnp", "sport", "=", "4242")
	output, err := cmd.Output()
	if err != nil {
		// ss returns error if no listening socket found - that's good!
		return nil
	}
	
	if strings.Contains(string(output), "4242") {
		return fmt.Errorf("port 4242 already in use - stop existing service or use --listen-addr")
	}
	
	return nil
}

// checkManagerConnectivity verifies connectivity to manager (for join mode)
func checkManagerConnectivity() error {
	// This would need the manager address from config
	// For now, just check network is up
	cmd := exec.Command("ip", "route")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("network not configured - no default route")
	}
	return nil
}

// PrintPreflightResults prints formatted pre-flight check results
func PrintPreflightResults(result *PreflightResult) {
	fmt.Println()
	fmt.Println("📋 Pre-flight Checks")
	fmt.Println(strings.Repeat("─", 50))
	
	for _, check := range result.Checks {
		icon := "✓"
		color := "\033[0;32m" // Green
		
		if check.Warning {
			icon = "⚠"
			color = "\033[1;33m" // Yellow
		} else if !check.Passed {
			icon = "✗"
			color = "\033[0;31m" // Red
		}
		
		fmt.Printf("  %s%s %s\033[0m", color, icon, check.Name)
		
		if check.Error != nil {
			fmt.Printf(" - %s", check.Error.Error())
		}
		
		if check.Duration > 0 && check.Duration < time.Millisecond*100 {
			fmt.Printf(" (%dms)", check.Duration.Milliseconds())
		}
		
		fmt.Println()
	}
	
	fmt.Println(strings.Repeat("─", 50))
	
	// Summary
	if result.Failed == 0 {
		fmt.Printf("\033[0;32m✓ All checks passed (%d passed, %d warnings)\033[0m\n", result.Passed, result.Warning)
	} else {
		fmt.Printf("\033[0;31m✗ %d check(s) failed\033[0m\n", result.Failed)
		if result.Warning > 0 {
			fmt.Printf("\033[1;33m⚠ %d warning(s)\033[0m\n", result.Warning)
		}
	}
	fmt.Println()
}

// PrintProgress creates a progress indicator
func PrintProgress(step int, total int, message string) {
	// Clear line
	fmt.Print("\r\033[K")
	
	// Print progress
	fmt.Printf("[%d/%d] %s", step, total, message)
	
	// Don't print newline - allows updating the same line
}

// PrintProgressComplete marks a step as complete
func PrintProgressComplete(step int, total int, message string) {
	fmt.Printf("\r\033[K\033[0;32m[%d/%d] ✓ %s\033[0m\n", step, total, message)
}

// PrintProgressFailed marks a step as failed
func PrintProgressFailed(step int, total int, message string, err error) {
	fmt.Printf("\r\033[K\033[0;31m[%d/%d] ✗ %s: %v\033[0m\n", step, total, message, err)
}

// Spinner displays a spinning animation during long operations
func Spinner(message string, done chan bool) {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	
	for {
		select {
		case <-done:
			fmt.Print("\r\033[K")
			return
		default:
			fmt.Printf("\r\033[K%s %s", spinners[i%len(spinners)], message)
			time.Sleep(100 * time.Millisecond)
			i++
		}
	}
}
