package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// newStatusCommand creates the status command
func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <vm-id>",
		Short: "Show detailed VM status",
		Long: `Display detailed information about a specific microVM.

This command shows comprehensive status information including configuration,
resources, network settings, and current state.

Example:
  swarmcracker status nginx-1
  swarmcracker status task-1234567890`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(args[0])
		},
	}

	return cmd
}

// runStatus executes the status command
func runStatus(vmID string) error {
	// Create state manager
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Get VM state
	vmState, err := stateMgr.Get(vmID)
	if err != nil {
		// Not a CLI-managed VM. Fall back to a VM the node knows (for example a
		// service task started by the daemon): the instance registry first, then
		// the socket scan.
		vmState, err = resolveRunningVM(vmID)
		if err != nil {
			return fmt.Errorf("VM not found: %s", vmID)
		}
	} else if vmState.Source == "" {
		// A state entry is the CLI's own note, not a record. Say which source can
		// actually describe this VM's host paths, so the view never implies a
		// record it did not read.
		vmState.Source = vmSource(vmID)
	}

	// Print VM information
	printVMStatus(vmState)

	// Query Firecracker API for live state if running
	if vmState.Status == "running" || vmState.Status == "starting" {
		if err := queryFirecrackerAPI(vmState); err != nil {
			log.Warn().Err(err).Msg("Could not query Firecracker API")
		}
	}

	return nil
}

// vmSource says how a VM's host paths were established, for the view that reads
// the CLI's own state: the node's registry when it has a live record for the VM,
// otherwise the socket-name fallback. A registry that cannot be read is the
// fallback too, never an error — `vm status` on a VM the state file knows must
// not fail because the daemon's directory is not readable by this user.
func vmSource(id string) string {
	store := vmInstanceStore()
	if store == nil {
		return runtime.VMSourceDerived
	}
	if _, found, err := store.Get(id); err == nil && found {
		return runtime.VMSourceRegistry
	}
	return runtime.VMSourceDerived
}

// formatSourceDetail explains the SOURCE line of `vm status`. The difference is
// the point: one is the node's own statement of where the VM is, the other a name
// derived from a task ID that is going away once every VM has a record.
func formatSourceDetail(source string) string {
	switch source {
	case runtime.VMSourceRegistry:
		return "registry (instance record)"
	case runtime.VMSourceDerived:
		return "derived (socket name, no record)"
	default:
		return formatSource(source)
	}
}

// printVMStatus prints formatted VM status information
func printVMStatus(vm *runtime.VMState) {
	fmt.Println("=" + strings.Repeat("=", 78))
	fmt.Printf("VM ID:     %s\n", vm.ID)
	fmt.Println("=" + strings.Repeat("=", 78))

	// Status section
	fmt.Println("\n[Status]")
	fmt.Printf("  State:        %s\n", formatStatusDetailed(vm.Status))
	fmt.Printf("  Source:       %s\n", formatSourceDetail(vm.Source))
	fmt.Printf("  PID:          %d\n", vm.PID)
	fmt.Printf("  Started:      %s\n", vm.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("  Uptime:       %s\n", formatDuration(time.Since(vm.StartTime)))

	// Health section: the daemon's live healthcheck verdict, read from the
	// node-local metadata file. It is only present for daemon-managed tasks that
	// declare a healthcheck, and only on the node hosting the task — SwarmKit
	// cannot carry it cluster-wide.
	if vm.Health != nil && vm.Health.State != "" {
		fmt.Println("\n[Health]")
		fmt.Printf("  State:        %s\n", formatHealth(vm.Health.State))
		if vm.Health.FailingStreak > 0 {
			fmt.Printf("  Failing:      %d consecutive probe(s)\n", vm.Health.FailingStreak)
		}
		if vm.Health.Command != "" {
			fmt.Printf("  Check:        %s\n", vm.Health.Command)
		}
		if vm.Health.Output != "" {
			fmt.Printf("  Last output:  %s\n", collapseOutput(vm.Health.Output))
		}
		if !vm.Health.UpdatedAt.IsZero() {
			fmt.Printf("  Observed:     %s\n", vm.Health.UpdatedAt.Format("2006-01-02 15:04:05"))
		}
	}

	// Configuration section
	fmt.Println("\n[Configuration]")
	if vm.VCPUs > 0 {
		fmt.Printf("  vCPUs:        %d\n", vm.VCPUs)
	}
	if vm.MemoryMB > 0 {
		fmt.Printf("  Memory:       %d MB\n", vm.MemoryMB)
	}
	if vm.KernelPath != "" {
		fmt.Printf("  Kernel:       %s\n", vm.KernelPath)
	}
	if vm.RootfsPath != "" {
		fmt.Printf("  Rootfs:       %s\n", vm.RootfsPath)
	}

	// Container section
	fmt.Println("\n[Container]")
	fmt.Printf("  Image:        %s\n", vm.Image)
	if vm.Service != "" {
		fmt.Printf("  Service:      %s\n", vm.Service)
	}
	if len(vm.Command) > 0 {
		fmt.Printf("  Command:      %s\n", fmt.Sprintf("%v", vm.Command))
	}

	// Network section
	if vm.NetworkID != "" || len(vm.IPAddresses) > 0 {
		fmt.Println("\n[Network]")
		if vm.NetworkID != "" {
			fmt.Printf("  Network ID:   %s\n", vm.NetworkID)
		}
		if len(vm.IPAddresses) > 0 {
			fmt.Printf("  IPs:          %s\n", fmt.Sprintf("%v", vm.IPAddresses))
		}
	}

	// Files section
	fmt.Println("\n[Files]")
	fmt.Printf("  Socket:       %s\n", vm.SocketPath)
	if vm.LogPath != "" {
		fmt.Printf("  Log:          %s\n", vm.LogPath)
	}

	// Error section (if any)
	if vm.LastError != "" {
		fmt.Println("\n[Error]")
		fmt.Printf("  Message:      %s\n", vm.LastError)
		if !vm.ErrorTime.IsZero() {
			fmt.Printf("  Time:         %s\n", vm.ErrorTime.Format("2006-01-02 15:04:05"))
		}
	}

	fmt.Println()
}

// formatStatusDetailed returns a detailed status string with color indicators
func formatStatusDetailed(status string) string {
	switch status {
	case "running":
		return "Running ✓"
	case "starting":
		return "Starting →"
	case "stopped":
		return "Stopped •"
	case "error":
		return "Error ✗"
	default:
		return status
	}
}

// formatHealth renders a published healthcheck state. "Failing" is the state
// Docker's own status hides: the probe is failing but the retries threshold is
// not reached, so the task is still running and no restart has fired.
func formatHealth(state string) string {
	switch state {
	case runtime.HealthHealthy:
		return "Healthy ✓"
	case runtime.HealthFailing:
		return "Failing ✗ (retries not exhausted; task still running)"
	case runtime.HealthUnhealthy:
		return "Unhealthy ✗ (restarting)"
	case runtime.HealthStarting:
		return "Starting →"
	default:
		return state
	}
}

// collapseOutput flattens a probe's captured output onto a single line and
// bounds it, so a chatty check cannot wrap the status view. Truncation happens
// on a rune boundary so a multibyte character is never split.
func collapseOutput(s string) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	const maxOutput = 200
	if len(collapsed) <= maxOutput {
		return collapsed
	}
	cut := maxOutput
	for cut > 0 && !utf8.RuneStart(collapsed[cut]) {
		cut--
	}
	return collapsed[:cut] + "..."
}

// formatDuration formats a duration into human-readable form: the standard
// duration string followed by its parts in brackets, e.g.
// "33m7s ([33m 7s])".
//
// The arithmetic runs on a copy. An earlier version subtracted each unit from the
// duration it was formatting, so the `d.String()` it printed had already been
// reduced to the seconds remainder and `vm status` read "7s ([33m 7s])" for a VM
// up 33 minutes.
func formatDuration(d time.Duration) string {
	rounded := d.Round(time.Second)

	remaining := rounded
	days := remaining / (24 * time.Hour)
	remaining -= days * 24 * time.Hour
	hours := remaining / time.Hour
	remaining -= hours * time.Hour
	minutes := remaining / time.Minute
	remaining -= minutes * time.Minute
	seconds := remaining / time.Second

	parts := make([]string, 0, 4)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}

	return fmt.Sprintf("%s ([%s])", rounded, strings.Join(parts, " "))
}

// queryFirecrackerAPI queries the Firecracker API for live VM state
func queryFirecrackerAPI(vm *runtime.VMState) error {
	// Check if socket exists
	if _, err := os.Stat(vm.SocketPath); os.IsNotExist(err) {
		return fmt.Errorf("socket file not found: %s", vm.SocketPath)
	}

	// Create HTTP client for Unix socket
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", vm.SocketPath)
			},
		},
	}

	// Query machine configuration
	configResp, err := client.Get("http://unix/machine-config")
	if err == nil {
		defer configResp.Body.Close()
		if configResp.StatusCode == http.StatusOK {
			var config map[string]interface{}
			if err := json.NewDecoder(configResp.Body).Decode(&config); err == nil {
				fmt.Println("\n[Live State (from Firecracker API)]")
				if vCPUCount, ok := config["vcpu_count"].(float64); ok {
					fmt.Printf("  vCPUs (live): %d\n", int(vCPUCount))
				}
				if memSizeMb, ok := config["mem_size_mib"].(float64); ok {
					fmt.Printf("  Memory (live): %d MB\n", int(memSizeMb))
				}
				if htEnabled, ok := config["ht_enabled"].(bool); ok {
					fmt.Printf("  Hyperthreading: %v\n", htEnabled)
				}
			}
		}
	}

	// Query current state
	infoResp, err := client.Get("http://unix/")
	if err == nil {
		defer infoResp.Body.Close()
		// Just verify connectivity
		if infoResp.StatusCode == http.StatusOK {
			fmt.Println("  API Status:    Connected")
		}
	}

	return nil
}
