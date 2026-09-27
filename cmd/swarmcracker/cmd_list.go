package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/rs/zerolog/log"
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
	cmd.Flags().StringVar(&vmSocketDir, "socket-dir", vmSocketDirDefault, "Directory containing Firecracker VM sockets")

	return cmd
}

// runList executes the list command
func runList() error {
	// Create state manager
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Reconcile the CLI's own state with live processes: entries marked as
	// running whose socket is gone are stale.
	reconciledCount := stateMgr.Reconcile(func(id string) bool {
		return runtime.IsVMSocketAlive(filepath.Join(vmSocketDir, id+runtime.VMSocketSuffix), 300*time.Millisecond)
	})
	if reconciledCount > 0 {
		fmt.Printf("Reconciled %d stale VM state(s)\n", reconciledCount)
	}

	// Discover VMs started by the daemon for services/tasks. Those live in
	// SwarmKit, not in the CLI state file, so they would otherwise be invisible.
	running, err := runtime.DiscoverRunningVMs(vmSocketDir)
	if err != nil {
		log.Warn().Err(err).Str("socket_dir", vmSocketDir).Msg("Failed to discover running VMs")
	}

	// Merge state entries with discovered VMs (state wins on conflict), then
	// enrich the discovered ones with PID/image/service metadata.
	vms := runtime.MergeVMs(stateMgr.List(), running)
	enrichVMs(vms)

	// Filter VMs based on --all flag
	var filteredVMs []*runtime.VMState
	for _, vm := range vms {
		if listAll || vm.Status == "running" || vm.Status == "starting" {
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
	fmt.Printf("%-20s %-12s %-14s %-25s %-8s %-12s\n",
		"ID", "STATUS", "SERVICE", "IMAGE", "PID", "STARTED")
	fmt.Println(strings.Repeat("-", 105))

	// Print each VM
	for _, vm := range vms {
		// Calculate uptime
		uptime := formatUptime(time.Since(vm.StartTime))

		image := truncateMiddle(vm.Image, 23)
		service := truncateMiddle(vm.Service, 12)
		pid := "-"
		if vm.PID > 0 {
			pid = fmt.Sprintf("%d", vm.PID)
		}

		// Print row
		fmt.Printf("%-20s %-12s %-14s %-25s %-8s %-12s\n",
			vm.ID,
			formatStatus(vm.Status),
			service,
			image,
			pid,
			uptime,
		)
	}

	// Print summary
	fmt.Printf("\nTotal: %d VM(s)\n", len(vms))
	return nil
}

// truncateMiddle shortens s to at most max characters, appending an ellipsis.
func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
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
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}
