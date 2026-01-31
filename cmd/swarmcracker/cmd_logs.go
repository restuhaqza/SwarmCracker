package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/spf13/cobra"
)

var (
	logsFollow bool
	logsTail   int
	logsSince  string
)

// newLogsCommand creates the logs command
func newLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <vm-id>",
		Short: "View VM logs",
		Long: `Display logs from a microVM.

This command shows logs from the Firecracker VMM process for a specific microVM.
Logs are written to files in the state directory.

Example:
  swarmcracker logs nginx-1
  swarmcracker logs --follow nginx-1
  swarmcracker logs --tail 100 nginx-1
  swarmcracker logs --since 1h nginx-1`,
		Args: cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			setupLogging(logLevel)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(args[0])
		},
	}

	cmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVar(&logsTail, "tail", -1, "Show last N lines (default: all)")
	cmd.Flags().StringVar(&logsSince, "since", "", "Show logs since timestamp (e.g., 1h, 30m)")

	return cmd
}

// runLogs executes the logs command
func runLogs(vmID string) error {
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

	// Determine log file path
	logPath := vmState.LogPath
	if logPath == "" {
		// Default to state directory
		logPath = filepath.Join(stateMgr.GetLogDir(), vmID+".log")
	}

	// Check if log file exists
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		// Try alternative paths
		altPaths := []string{
			filepath.Join("/var/log/swarmcracker", vmID+".log"),
			filepath.Join("/tmp", "swarmcracker-"+vmID+".log"),
		}

		for _, altPath := range altPaths {
			if _, err := os.Stat(altPath); err == nil {
				logPath = altPath
				break
			}
		}

		// If still not found, check if it ever existed
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			return fmt.Errorf("log file not found for VM %s (looked in: %s)", vmID, logPath)
		}
	}

	// Open log file
	file, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	// Parse since time
	var sinceTime time.Time
	if logsSince != "" {
		duration, err := parseDuration(logsSince)
		if err != nil {
			return fmt.Errorf("invalid --since value: %w", err)
		}
		sinceTime = time.Now().Add(-duration)
	}

	// Read and display logs
	if logsFollow {
		return followLogs(file, logPath, sinceTime)
	}

	return displayLogs(file, sinceTime)
}

// displayLogs displays logs with optional filtering
func displayLogs(file *os.File, sinceTime time.Time) error {
	scanner := bufio.NewScanner(file)

	// If tail is specified, we need to count lines first
	var lines []string
	if logsTail > 0 {
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("error reading log file: %w", err)
		}

		// Keep only last N lines
		if len(lines) > logsTail {
			lines = lines[len(lines)-logsTail:]
		}

		// Display filtered lines
		for _, line := range lines {
			if shouldDisplayLine(line, sinceTime) {
				fmt.Println(line)
			}
		}
	} else {
		// Stream all lines
		for scanner.Scan() {
			line := scanner.Text()
			if shouldDisplayLine(line, sinceTime) {
				fmt.Println(line)
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("error reading log file: %w", err)
		}
	}

	return nil
}

// followLogs follows the log file and outputs new lines
func followLogs(file *os.File, logPath string, sinceTime time.Time) error {
	// Seek to end of file for follow mode
	initialInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	// If --since is specified, we need to read from that point
	if !sinceTime.IsZero() {
		_, err = file.Seek(0, 0)
	} else {
		_, err = file.Seek(0, 2) // Seek to end
	}
	if err != nil {
		return fmt.Errorf("failed to seek file: %w", err)
	}

	// Setup signal handling for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start polling for new content
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	fmt.Printf("Following logs for %s (Ctrl+C to exit)\n", logPath)

	lastSize := initialInfo.Size()

	for {
		select {
		case <-sigCh:
			fmt.Println("\nStopped following logs")
			return nil
		case <-ticker.C:
			// Check for new content
			info, err := os.Stat(logPath)
			if err != nil {
				// File might have been deleted
				return fmt.Errorf("log file error: %w", err)
			}

			currentSize := info.Size()
			if currentSize > lastSize {
				// File has grown, read new content
				if _, err := file.Seek(lastSize, 0); err != nil {
					return fmt.Errorf("failed to seek file: %w", err)
				}

				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					line := scanner.Text()
					if shouldDisplayLine(line, sinceTime) {
						fmt.Println(line)
					}
				}

				lastSize = currentSize
			} else if currentSize < lastSize {
				// File was truncated or rotated
				if _, err := file.Seek(0, 0); err != nil {
					return fmt.Errorf("failed to seek file: %w", err)
				}
				lastSize = 0
				fmt.Println("[Log file rotated]")
			}
		}
	}
}

// shouldDisplayLine determines if a line should be displayed based on time filter
func shouldDisplayLine(line string, sinceTime time.Time) bool {
	if sinceTime.IsZero() {
		return true
	}

	// Try to extract timestamp from log line
	// Common formats: 2024-02-01 12:34:56, [12:34:56], etc.
	parts := strings.Fields(line)
	if len(parts) > 0 {
		// Try ISO format
		if _, err := time.Parse("2006-01-02T15:04:05", parts[0]); err == nil {
			t, _ := time.Parse("2006-01-02T15:04:05", parts[0])
			return t.After(sinceTime)
		}
		// Try common log format
		if _, err := time.Parse("2006-01-02", parts[0]); err == nil && len(parts) > 1 {
			dateTime := parts[0] + "T" + parts[1]
			if t, err := time.Parse("2006-01-2T15:04:05", dateTime); err == nil {
				return t.After(sinceTime)
			}
		}
	}

	// Can't parse timestamp, include the line
	return true
}

// parseDuration parses a duration string (e.g., "1h", "30m", "1h30m")
func parseDuration(s string) (time.Duration, error) {
	// Support common formats
	s = strings.TrimSpace(s)

	// Try standard Go duration format
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Try simple number format (assume minutes)
	if i, err := strconv.Atoi(s); err == nil {
		return time.Duration(i) * time.Minute, nil
	}

	return 0, fmt.Errorf("invalid duration format: %s", s)
}
