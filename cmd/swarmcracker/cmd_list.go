package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/spf13/cobra"
)

var (
	listAll    bool
	listFormat string
)

// newListCommand creates the list command
func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List running microVMs",
		Long: `List all running SwarmCracker microVMs.

This command displays information about all microVMs managed by SwarmCracker,
including their ID, status, image, PID, and uptime.

Example:
  swarmcracker list
  swarmcracker list --all
  swarmcracker list --format json`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList()
		},
	}

	cmd.Flags().BoolVar(&listAll, "all", false, "Show all VMs including stopped ones")
	cmd.Flags().StringVar(&listFormat, "format", "table", "Output format (table, json)")

	return cmd
}

// runList executes the list command
func runList() error {
	// Create state manager
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Get all VMs
	vms := stateMgr.List()

	// Filter VMs based on --all flag
	var filteredVMs []*runtime.VMState
	for _, vm := range vms {
		if listAll {
			filteredVMs = append(filteredVMs, vm)
		} else if vm.Status == "running" || vm.Status == "starting" {
			filteredVMs = append(filteredVMs, vm)
		}
	}

	// Output based on format
	switch strings.ToLower(listFormat) {
	case "json":
		return outputJSON(filteredVMs)
	default:
		return outputTable(filteredVMs)
	}
}

// outputTable displays VMs in table format
func outputTable(vms []*runtime.VMState) error {
	if len(vms) == 0 {
		fmt.Println("No VMs found.")
		return nil
	}

	// Print header
	fmt.Printf("%-20s %-12s %-25s %-8s %-12s\n",
		"ID", "STATUS", "IMAGE", "PID", "STARTED")
	fmt.Println(strings.Repeat("-", 90))

	// Print each VM
	for _, vm := range vms {
		// Calculate uptime
		uptime := formatUptime(time.Since(vm.StartTime))

		// Truncate image if too long
		image := vm.Image
		if len(image) > 23 {
			image = image[:20] + "..."
		}

		// Format PID
		pid := fmt.Sprintf("%d", vm.PID)

		// Print row
		fmt.Printf("%-20s %-12s %-25s %-8s %-12s\n",
			vm.ID,
			formatStatus(vm.Status),
			image,
			pid,
			uptime,
		)
	}

	// Print summary
	fmt.Printf("\nTotal: %d VM(s)\n", len(vms))
	return nil
}

// outputJSON displays VMs in JSON format
func outputJSON(vms []*runtime.VMState) error {
	data, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

// formatStatus formats status string with color/indicator
func formatStatus(status string) string {
	switch status {
	case "running":
		return "Running " + "✓"
	case "starting":
		return "Starting" + "→"
	case "stopped":
		return "Stopped" + "•"
	case "error":
		return "Error   " + "✗"
	default:
		return status
	}
}

// formatUptime formats a duration into human-readable form
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	} else if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, minutes)
	} else {
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}
