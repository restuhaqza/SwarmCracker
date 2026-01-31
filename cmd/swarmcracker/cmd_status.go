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
		return fmt.Errorf("VM not found: %s", vmID)
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

// printVMStatus prints formatted VM status information
func printVMStatus(vm *runtime.VMState) {
	fmt.Println("=" + strings.Repeat("=", 78))
	fmt.Printf("VM ID:     %s\n", vm.ID)
	fmt.Println("=" + strings.Repeat("=", 78))

	// Status section
	fmt.Println("\n[Status]")
	fmt.Printf("  State:        %s\n", formatStatusDetailed(vm.Status))
	fmt.Printf("  PID:          %d\n", vm.PID)
	fmt.Printf("  Started:      %s\n", vm.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("  Uptime:       %s\n", formatDuration(time.Since(vm.StartTime)))

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

// formatDuration formats a duration into human-readable form
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second

	parts := []string{}
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

	return fmt.Sprintf("%s (%s)", d.String(), fmt.Sprintf("%v", parts))
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
