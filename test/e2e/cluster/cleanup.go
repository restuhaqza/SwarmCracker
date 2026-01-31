package cluster

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// CleanupManager handles cleanup of SwarmKit resources
type CleanupManager struct {
	logger      zerolog.Logger
	stateDirs   []string
	processes   []*os.Process
	networkIfs  []string
	bridges     []string
	cleanupFuncs []func() error
}

// NewCleanupManager creates a new cleanup manager
func NewCleanupManager() *CleanupManager {
	return &CleanupManager{
		logger:      log.With().Str("component", "cleanup").Logger(),
		stateDirs:   make([]string, 0),
		processes:   make([]*os.Process, 0),
		networkIfs:  make([]string, 0),
		bridges:     make([]string, 0),
		cleanupFuncs: make([]func() error, 0),
	}
}

// TrackStateDir tracks a state directory for cleanup
func (cm *CleanupManager) TrackStateDir(dir string) {
	cm.stateDirs = append(cm.stateDirs, dir)
	cm.logger.Debug().Str("dir", dir).Msg("Tracking state directory")
}

// TrackProcess tracks a process for cleanup
func (cm *CleanupManager) TrackProcess(proc *os.Process) {
	if proc != nil {
		cm.processes = append(cm.processes, proc)
		cm.logger.Debug().Int("pid", proc.Pid).Msg("Tracking process")
	}
}

// TrackNetworkInterface tracks a network interface for cleanup
func (cm *CleanupManager) TrackNetworkInterface(ifName string) {
	cm.networkIfs = append(cm.networkIfs, ifName)
	cm.logger.Debug().Str("interface", ifName).Msg("Tracking network interface")
}

// TrackBridge tracks a bridge for cleanup
func (cm *CleanupManager) TrackBridge(bridgeName string) {
	cm.bridges = append(cm.bridges, bridgeName)
	cm.logger.Debug().Str("bridge", bridgeName).Msg("Tracking bridge")
}

// AddCleanupFunc adds a custom cleanup function
func (cm *CleanupManager) AddCleanupFunc(fn func() error) {
	cm.cleanupFuncs = append(cm.cleanupFuncs, fn)
	cm.logger.Debug().Msg("Added custom cleanup function")
}

// Cleanup performs all tracked cleanup operations
func (cm *CleanupManager) Cleanup(ctx context.Context) error {
	cm.logger.Info().Msg("Starting cleanup...")
	var errs []error

	// Run custom cleanup functions first
	for i, fn := range cm.cleanupFuncs {
		if err := fn(); err != nil {
			cm.logger.Error().Err(err).Int("fn_index", i).Msg("Custom cleanup function failed")
			errs = append(errs, fmt.Errorf("cleanup func %d: %w", i, err))
		}
	}

	// Kill tracked processes
	for _, proc := range cm.processes {
		if err := cm.killProcess(proc); err != nil {
			cm.logger.Error().Err(err).Int("pid", proc.Pid).Msg("Failed to kill process")
			errs = append(errs, err)
		}
	}

	// Remove network interfaces
	for _, ifName := range cm.networkIfs {
		if err := cm.removeNetworkInterface(ifName); err != nil {
			cm.logger.Error().Err(err).Str("interface", ifName).Msg("Failed to remove network interface")
			// Don't append error as network cleanup is best-effort
		}
	}

	// Remove bridges
	for _, bridgeName := range cm.bridges {
		if err := cm.removeBridge(bridgeName); err != nil {
			cm.logger.Error().Err(err).Str("bridge", bridgeName).Msg("Failed to remove bridge")
			// Don't append error as network cleanup is best-effort
		}
	}

	// Remove state directories
	for _, dir := range cm.stateDirs {
		if err := cm.removeStateDir(dir); err != nil {
			cm.logger.Error().Err(err).Str("dir", dir).Msg("Failed to remove state directory")
			// Don't append error as directory cleanup is best-effort
		}
	}

	cm.logger.Info().Msg("Cleanup completed")

	if len(errs) > 0 {
		return fmt.Errorf("cleanup encountered %d error(s)", len(errs))
	}

	return nil
}

// killProcess kills a process gracefully or forcefully
func (cm *CleanupManager) killProcess(proc *os.Process) error {
	cm.logger.Debug().Int("pid", proc.Pid).Msg("Killing process")

	// Try graceful shutdown first
	if err := proc.Signal(syscall.SIGTERM); err == nil {
		// Wait up to 5 seconds for graceful shutdown
		done := make(chan error, 1)
		go func() {
			_, err := proc.Wait()
			done <- err
		}()

		select {
		case <-done:
			cm.logger.Debug().Int("pid", proc.Pid).Msg("Process terminated gracefully")
			return nil
		case <-time.After(5 * time.Second):
			cm.logger.Debug().Int("pid", proc.Pid).Msg("Process did not terminate gracefully, killing")
		}
	}

	// Force kill if graceful shutdown failed or timed out
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("failed to kill process %d: %w", proc.Pid, err)
	}

	return nil
}

// removeNetworkInterface removes a network interface
func (cm *CleanupManager) removeNetworkInterface(ifName string) error {
	cm.logger.Debug().Str("interface", ifName).Msg("Removing network interface")

	// Check if interface exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifName)); os.IsNotExist(err) {
		cm.logger.Debug().Str("interface", ifName).Msg("Interface does not exist, skipping")
		return nil
	}

	// Bring interface down
	cmd := exec.Command("ip", "link", "set", ifName, "down")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring down interface %s: %w", ifName, err)
	}

	// Delete interface
	cmd = exec.Command("ip", "link", "delete", ifName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete interface %s: %w", ifName, err)
	}

	return nil
}

// removeBridge removes a network bridge
func (cm *CleanupManager) removeBridge(bridgeName string) error {
	cm.logger.Debug().Str("bridge", bridgeName).Msg("Removing bridge")

	// Check if bridge exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", bridgeName)); os.IsNotExist(err) {
		cm.logger.Debug().Str("bridge", bridgeName).Msg("Bridge does not exist, skipping")
		return nil
	}

	// Bring bridge down
	cmd := exec.Command("ip", "link", "set", bridgeName, "down")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to bring down bridge %s: %w", bridgeName, err)
	}

	// Delete bridge
	cmd = exec.Command("ip", "link", "delete", bridgeName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete bridge %s: %w", bridgeName, err)
	}

	return nil
}

// removeStateDir removes a state directory
func (cm *CleanupManager) removeStateDir(dir string) error {
	cm.logger.Debug().Str("dir", dir).Msg("Removing state directory")

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		cm.logger.Debug().Str("dir", dir).Msg("Directory does not exist, skipping")
		return nil
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove directory %s: %w", dir, err)
	}

	return nil
}

// CleanupTempDirs cleans up temporary directories matching a pattern
func (cm *CleanupManager) CleanupTempDirs(pattern string) error {
	cm.logger.Debug().Str("pattern", pattern).Msg("Cleaning up temp directories")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to glob pattern %s: %w", pattern, err)
	}

	for _, match := range matches {
		cm.logger.Debug().Str("dir", match).Msg("Removing temp directory")
		if err := os.RemoveAll(match); err != nil {
			cm.logger.Error().Err(err).Str("dir", match).Msg("Failed to remove temp directory")
		}
	}

	return nil
}

// KillProcessesByName kills all processes matching a name
func (cm *CleanupManager) KillProcessesByName(name string) error {
	cm.logger.Debug().Str("name", name).Msg("Killing processes by name")

	// Find all processes matching the name
	cmd := exec.Command("pgrep", "-x", name)
	output, err := cmd.Output()
	if err != nil {
		// pgrep returns exit code 1 if no processes found
		return nil
	}

	pids := parsePIDs(output)
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			cm.logger.Error().Err(err).Int("pid", pid).Msg("Failed to send SIGTERM")
		} else {
			cm.logger.Debug().Int("pid", pid).Msg("Sent SIGTERM to process")
		}
	}

	return nil
}

// parsePIDs parses pids from pgrep output
func parsePIDs(output []byte) []int {
	pids := make([]int, 0)
	for _, line := range splitLines(output) {
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// splitLines splits output into lines
func splitLines(output []byte) []string {
	lines := make([]string, 0)
	current := make([]byte, 0)

	for _, b := range output {
		if b == '\n' {
			lines = append(lines, string(current))
			current = current[:0]
		} else {
			current = append(current, b)
		}
	}

	if len(current) > 0 {
		lines = append(lines, string(current))
	}

	return lines
}

// WithCleanup runs a function with automatic cleanup
func WithCleanup(ctx context.Context, fn func(*CleanupManager) error) error {
	cm := NewCleanupManager()
	defer cm.Cleanup(ctx)

	return fn(cm)
}
