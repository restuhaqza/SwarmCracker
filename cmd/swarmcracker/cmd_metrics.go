package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/metrics"
	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/spf13/cobra"
)

var (
	metricsFormat  string
	metricsTaskID  string
	metricsRefresh int
)

// newMetricsCommand creates the metrics command
func newMetricsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Show VM resource usage metrics",
		Long: `Display resource usage metrics for running microVMs.

This command shows CPU, memory, and network metrics collected from the /proc filesystem.
Metrics can be displayed in table or JSON format.

Example:
  swarmcracker metrics
  swarmcracker metrics --task task-1234567890
  swarmcracker metrics --format json
  swarmcracker metrics --refresh 5`,
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMetrics()
		},
	}

	cmd.Flags().StringVar(&metricsFormat, "format", "table", "Output format (table, json)")
	cmd.Flags().StringVar(&metricsTaskID, "task", "", "Show metrics for specific task only")
	cmd.Flags().IntVar(&metricsRefresh, "refresh", 0, "Refresh interval in seconds (0 = one-time display)")

	return cmd
}

// runMetrics executes the metrics command
func runMetrics() error {
	// Create state manager to get running VMs
	stateMgr, err := runtime.NewStateManager("")
	if err != nil {
		return fmt.Errorf("failed to create state manager: %w", err)
	}

	// Create metrics collector
	collector, err := createMetricsCollector()
	if err != nil {
		return fmt.Errorf("failed to create metrics collector: %w", err)
	}

	// If refresh is specified, run in watch mode
	if metricsRefresh > 0 {
		return runMetricsWatch(collector, stateMgr)
	}

	// One-time display
	return displayMetricsOnce(collector, stateMgr)
}

// runMetricsWatch runs metrics in watch mode with periodic refresh
func runMetricsWatch(collector *metrics.Collector, stateMgr *runtime.StateManager) error {
	interval := time.Duration(metricsRefresh) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Clear screen on first run
	clearScreen()

	for {
		// Collect and display
		if err := collectAndDisplay(collector, stateMgr); err != nil {
			return err
		}

		// Wait for next tick or interrupt
		select {
		case <-ticker.C:
			clearScreen()
		case <-time.After(100 * time.Millisecond):
			// Check for interrupt (simple approach)
			// In production, use signal handling
		}
	}
}

// displayMetricsOnce displays metrics once without refresh
func displayMetricsOnce(collector *metrics.Collector, stateMgr *runtime.StateManager) error {
	return collectAndDisplay(collector, stateMgr)
}

// collectAndDisplay collects metrics and displays them
func collectAndDisplay(collector *metrics.Collector, stateMgr *runtime.StateManager) error {
	// Get all VMs from state manager
	vms := stateMgr.List()

	// Filter by task ID if specified
	var targetVMs []*runtime.VMState
	if metricsTaskID != "" {
		vm, err := stateMgr.Get(metricsTaskID)
		if err != nil {
			return fmt.Errorf("VM not found: %s", metricsTaskID)
		}
		targetVMs = []*runtime.VMState{vm}
	} else {
		// Only show running VMs
		for _, vm := range vms {
			if vm.Status == "running" || vm.Status == "starting" {
				targetVMs = append(targetVMs, vm)
			}
		}
	}

	// Collect metrics for each VM
	metricsList := make(map[string]*metrics.VMMetrics)
	for _, vm := range targetVMs {
		if vm.PID > 0 {
			m, err := collector.Collect(vm.ID, vm.PID)
			if err != nil {
				// Don't fail entirely, just log
				fmt.Fprintf(os.Stderr, "Warning: failed to collect metrics for %s: %v\n", vm.ID, err)
				continue
			}
			metricsList[vm.ID] = m
		}
	}

	// Output based on format
	switch strings.ToLower(metricsFormat) {
	case "json":
		return outputMetricsJSON(metricsList, targetVMs)
	default:
		return outputMetricsTable(metricsList, targetVMs)
	}
}

// outputMetricsTable displays metrics in table format
func outputMetricsTable(metricsList map[string]*metrics.VMMetrics, vms []*runtime.VMState) error {
	if len(vms) == 0 {
		fmt.Println("No VMs found.")
		return nil
	}

	// Print header
	fmt.Printf("%-20s %-10s %-12s %-12s %-12s %-12s\n",
		"ID", "PID", "CPU (ms)", "MEM (MB)", "RX (bytes)", "TX (bytes)")
	fmt.Println(strings.Repeat("-", 90))

	// Print each VM
	for _, vm := range vms {
		m, exists := metricsList[vm.ID]
		if !exists {
			// No metrics available
			fmt.Printf("%-20s %-10d %-12s %-12s %-12s %-12s\n",
				vm.ID, vm.PID, "N/A", "N/A", "N/A", "N/A")
			continue
		}

		// Format values
		cpuMs := fmt.Sprintf("%.2f", m.CPUMs)
		memMB := fmt.Sprintf("%.2f", float64(m.MemoryKB)/1024.0)
		rxBytes := formatBytes(m.NetRxBytes)
		txBytes := formatBytes(m.NetTxBytes)

		// Print row
		fmt.Printf("%-20s %-10d %-12s %-12s %-12s %-12s\n",
			vm.ID, m.PID, cpuMs, memMB, rxBytes, txBytes)
	}

	// Print summary
	fmt.Printf("\nTotal: %d VM(s)\n", len(vms))

	// Print timestamp
	fmt.Printf("Collected at: %s\n", time.Now().Format("2006-01-02 15:04:05"))

	return nil
}

// outputMetricsJSON displays metrics in JSON format
func outputMetricsJSON(metricsList map[string]*metrics.VMMetrics, vms []*runtime.VMState) error {
	// Combine VM state with metrics
	type vmMetricsWithState struct {
		*runtime.VMState
		Metrics *metrics.VMMetrics `json:"metrics,omitempty"`
	}

	result := make([]vmMetricsWithState, 0, len(vms))
	for _, vm := range vms {
		item := vmMetricsWithState{
			VMState: vm,
		}
		if m, exists := metricsList[vm.ID]; exists {
			item.Metrics = m
		}
		result = append(result, item)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

// createMetricsCollector creates a metrics collector
func createMetricsCollector() (*metrics.Collector, error) {
	// Determine state directory for metrics
	var stateDir string
	if os.Geteuid() == 0 {
		// Running as root
		stateDir = "/var/run/swarmcracker/metrics"
	} else {
		// Running as non-root
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine home directory: %w", err)
		}
		stateDir = filepath.Join(homeDir, ".swarmcracker", "metrics")
	}

	return metrics.NewCollector(stateDir)
}

// formatBytes formats a byte count into human-readable form
func formatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// clearScreen clears the terminal screen
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}
