// Package metrics provides VM metrics collection for SwarmCracker.
package metrics

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VMMetrics represents metrics collected for a VM.
type VMMetrics struct {
	TaskID      string    `json:"task_id"`
	PID         int       `json:"pid"`
	Timestamp   time.Time `json:"timestamp"`
	CPUMs       float64   `json:"cpu_ms"`       // CPU time in milliseconds
	MemoryKB    uint64    `json:"memory_kb"`    // RSS memory in KB
	NetRxBytes  uint64    `json:"net_rx_bytes"` // Bytes received
	NetTxBytes  uint64    `json:"net_tx_bytes"` // Bytes transmitted
	UptimeSec   int64     `json:"uptime_sec"`   // Seconds since VM started
}

// Collector collects and stores VM metrics.
type Collector struct {
	stateDir string                // For persistence
	mu       sync.RWMutex          // Protects metrics map
	metrics  map[string]*VMMetrics // taskID -> metrics
	cancel   context.CancelFunc    // Cancel periodic collection
}

// NewCollector creates a new metrics collector.
func NewCollector(stateDir string) (*Collector, error) {
	if stateDir == "" {
		// Default to /var/run/swarmcracker/metrics
		stateDir = "/var/run/swarmcracker/metrics"
	}

	// Ensure directory exists
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create metrics directory: %w", err)
	}

	c := &Collector{
		stateDir: stateDir,
		metrics:  make(map[string]*VMMetrics),
	}

	return c, nil
}

// Collect gathers metrics for a specific VM.
func (c *Collector) Collect(taskID string, pid int) (*VMMetrics, error) {
	// Check if process exists first
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); os.IsNotExist(err) {
		return nil, fmt.Errorf("process %d does not exist", pid)
	}

	m := &VMMetrics{
		TaskID:    taskID,
		PID:       pid,
		Timestamp: time.Now(),
	}

	// Collect CPU metrics
	cpuMs, err := c.collectCPU(pid)
	if err != nil {
		// Don't fail entirely, just log and continue
		m.CPUMs = 0
	} else {
		m.CPUMs = cpuMs
	}

	// Collect memory metrics
	memKB, err := c.collectMemory(pid)
	if err != nil {
		m.MemoryKB = 0
	} else {
		m.MemoryKB = memKB
	}

	// Collect network metrics
	rxBytes, txBytes, err := c.collectNetwork(pid)
	if err != nil {
		m.NetRxBytes = 0
		m.NetTxBytes = 0
	} else {
		m.NetRxBytes = rxBytes
		m.NetTxBytes = txBytes
	}

	// Calculate uptime from process start time
	uptimeSec, err := c.getProcUptime(pid)
	if err != nil {
		m.UptimeSec = 0
	} else {
		m.UptimeSec = uptimeSec
	}

	// Store metrics
	c.mu.Lock()
	c.metrics[taskID] = m
	c.mu.Unlock()

	return m, nil
}

// collectCPU reads /proc/<pid>/stat and returns CPU time in milliseconds.
func (c *Collector) collectCPU(pid int) (float64, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)

	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read stat file: %w", err)
	}

	// Parse /proc/pid/stat
	// Format: pid (comm) state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt utime stime ...
	// Fields are space-separated, but comm can contain spaces and is wrapped in parentheses
	content := string(data)

	// Find the closing parenthesis of the command name
	closeParen := strings.LastIndex(content, ")")
	if closeParen == -1 {
		return 0, fmt.Errorf("invalid stat format: no closing parenthesis")
	}

	// Get everything after the command name
	afterComm := strings.TrimSpace(content[closeParen+1:])

	// Split by spaces
	fields := strings.Fields(afterComm)
	if len(fields) < 15 {
		return 0, fmt.Errorf("invalid stat format: expected at least 15 fields after comm")
	}

	// Field 14 (index 13) is utime, field 15 (index 14) is stime
	// These are in clock ticks (jiffies)
	utime, err := strconv.ParseFloat(fields[13], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse utime: %w", err)
	}

	stime, err := strconv.ParseFloat(fields[14], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse stime: %w", err)
	}

	// Convert jiffies to milliseconds
	// Most Linux systems use 100 Hz (SC_CLK_TCK = 100)
	const clkTck = 100.0
	totalJiffies := utime + stime
	totalMs := (totalJiffies / clkTck) * 1000.0

	return totalMs, nil
}

// collectMemory reads /proc/<pid>/status and returns RSS memory in KB.
func (c *Collector) collectMemory(pid int) (uint64, error) {
	statusPath := fmt.Sprintf("/proc/%d/status", pid)

	data, err := os.ReadFile(statusPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read status file: %w", err)
	}

	// Parse VmRSS from status file
	// Format: VmRSS:     12345 kB
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, fmt.Errorf("failed to parse VmRSS: %w", err)
				}
				return kb, nil
			}
		}
	}

	return 0, fmt.Errorf("VmRSS not found in status file")
}

// collectNetwork reads /proc/<pid>/net/dev and returns network stats.
func (c *Collector) collectNetwork(pid int) (uint64, uint64, error) {
	netDevPath := fmt.Sprintf("/proc/%d/net/dev", pid)

	data, err := os.ReadFile(netDevPath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read net/dev file: %w", err)
	}

	var rxBytes, txBytes uint64

	// Parse /proc/pid/net/dev
	// Format: <inter face>: <rx bytes> <rx packets> ... <tx bytes> <tx packets> ...
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		// Skip header lines
		if !strings.Contains(line, ":") {
			continue
		}

		// Split interface name from stats
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		iface := strings.TrimSpace(parts[0])
		stats := strings.Fields(parts[1])

		// Only look for tap-* interfaces (Firecracker uses TAP devices)
		if !strings.HasPrefix(iface, "tap") {
			continue
		}

		// Stats format (simplified):
		// bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop ...
		// We need field 1 (rx bytes, index 0) and field 9 (tx bytes, index 8)
		if len(stats) >= 9 {
			rx, err := strconv.ParseUint(stats[0], 10, 64)
			if err == nil {
				rxBytes += rx
			}

			tx, err := strconv.ParseUint(stats[8], 10, 64)
			if err == nil {
				txBytes += tx
			}
		}
	}

	return rxBytes, txBytes, nil
}

// getProcUptime calculates uptime based on process start time from /proc/<pid>/stat.
func (c *Collector) getProcUptime(pid int) (int64, error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)

	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read stat file: %w", err)
	}

	// Parse /proc/pid/stat to get start time (field 22, index 21 after comm)
	content := string(data)

	// Find the closing parenthesis of the command name
	closeParen := strings.LastIndex(content, ")")
	if closeParen == -1 {
		return 0, fmt.Errorf("invalid stat format: no closing parenthesis")
	}

	// Get everything after the command name
	afterComm := strings.TrimSpace(content[closeParen+1:])

	// Split by spaces
	fields := strings.Fields(afterComm)
	if len(fields) < 22 {
		return 0, fmt.Errorf("invalid stat format: expected at least 22 fields after comm")
	}

	// Field 22 (index 21) is starttime - time the process started after system boot
	// Measured in clock ticks (jiffies)
	starttimeJiffies, err := strconv.ParseUint(fields[21], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse starttime: %w", err)
	}

	// Convert jiffies to milliseconds
	const clkTck = 100.0
	starttimeMs := (float64(starttimeJiffies) / clkTck) * 1000.0

	// We need the system boot time to calculate actual uptime
	// For now, we'll use a simplified approach using /proc/uptime
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("failed to read system uptime: %w", err)
	}

	// /proc/uptime format: seconds.seconds (e.g., "12345.67 0.00")
	uptimeFields := strings.Fields(string(uptimeData))
	if len(uptimeFields) == 0 {
		return 0, fmt.Errorf("invalid /proc/uptime format")
	}

	systemUptimeSec, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse system uptime: %w", err)
	}

	// Process uptime = system uptime - process start time (in seconds)
	processUptimeSec := systemUptimeSec - (starttimeMs / 1000.0)

	return int64(processUptimeSec), nil
}

// GetMetrics returns the latest metrics for a task.
func (c *Collector) GetMetrics(taskID string) (*VMMetrics, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	m, exists := c.metrics[taskID]
	if !exists {
		return nil, fmt.Errorf("metrics not found for task %s", taskID)
	}

	// Return a copy to prevent concurrent modification
	mCopy := *m
	return &mCopy, nil
}

// ListMetrics returns all metrics.
func (c *Collector) ListMetrics() map[string]*VMMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a copy to prevent concurrent modification
	result := make(map[string]*VMMetrics, len(c.metrics))
	for k, v := range c.metrics {
		mCopy := *v
		result[k] = &mCopy
	}

	return result
}

// Start begins periodic metrics collection.
func (c *Collector) Start(ctx context.Context, interval time.Duration, getPIDs func() map[string]int) {
	if interval <= 0 {
		interval = 10 * time.Second // Default 10 seconds
	}

	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logDebug("Periodic metrics collection stopped")
				return
			case <-ticker.C:
				c.collectAll(getPIDs)
			}
		}
	}()

	logDebug("Started periodic metrics collection (interval: %v)", interval)
}

// Stop stops periodic metrics collection.
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

// collectAll collects metrics for all running VMs.
func (c *Collector) collectAll(getPIDs func() map[string]int) {
	if getPIDs == nil {
		return
	}

	pids := getPIDs()
	for taskID, pid := range pids {
		m, err := c.Collect(taskID, pid)
		if err != nil {
			logDebug("Failed to collect metrics for %s (PID %d): %v", taskID, pid, err)
			continue
		}
		logDebug("Collected metrics for %s: CPU=%.2fms, Mem=%dKB, Rx=%d, Tx=%d",
			taskID, m.CPUMs, m.MemoryKB, m.NetRxBytes, m.NetTxBytes)
	}
}

// logDebug is a simple logger that can be replaced with proper logging.
func logDebug(format string, args ...interface{}) {
	// For now, just use fmt.Printf
	// In production, this should use the configured logger
	msg := fmt.Sprintf("[metrics] "+format, args...)
	fmt.Println(msg)
}
