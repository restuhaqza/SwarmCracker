// Package jailer provides cgroup v2 management for resource limits.
package jailer

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// CgroupManager manages cgroup v2 resources for jailed VMs.
type CgroupManager struct {
	basePath string // /sys/fs/cgroup/swarmcracker
	logger   zerolog.Logger
}

// ResourceLimits defines CPU/memory/IO constraints for a VM.
type ResourceLimits struct {
	// CPU quota in microseconds (period is typically 1000000 = 1 second)
	// Example: 500000 = 0.5 CPU cores
	CPUQuotaUs int64 `yaml:"cpu_quota_us"`

	// CPU max in format "quota period" (cgroup v2)
	// Example: "500000 1000000" = 0.5 CPU cores
	CPUMax string `yaml:"cpu_max"`

	// Memory limit in bytes
	// Example: 536870912 = 512MB
	MemoryMax int64 `yaml:"memory_max"`

	// Memory throttle threshold (soft limit)
	// VM will be throttled before hitting hard limit
	MemoryHigh int64 `yaml:"memory_high"`

	// IO weight (1-10000, default 100)
	// Higher = more IO bandwidth
	IOWeight uint64 `yaml:"io_weight"`

	// IO read bandwidth limit in bytes per second
	IOReadBPS int64 `yaml:"io_read_bps"`

	// IO write bandwidth limit in bytes per second
	IOWriteBPS int64 `yaml:"io_write_bps"`
}

// NewCgroupManager creates a new cgroup manager.
func NewCgroupManager(basePath string) (*CgroupManager, error) {
	if basePath == "" {
		basePath = "/sys/fs/cgroup/swarmcracker"
	}

	// Verify cgroup v2 is available
	if !isCgroupV2Available() {
		return nil, fmt.Errorf("cgroup v2 not available on this system")
	}

	// Create base directory
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cgroup base dir: %w", err)
	}

	return &CgroupManager{
		basePath: basePath,
		logger:   log.With().Str("component", "cgroup-manager").Logger(),
	}, nil
}

// CreateCgroup creates a cgroup with resource limits for a task.
func (m *CgroupManager) CreateCgroup(taskID string, limits ResourceLimits) error {
	m.logger.Info().
		Str("task_id", taskID).
		Msg("Creating cgroup")

	cgroupPath := filepath.Join(m.basePath, taskID)

	// Create cgroup directory
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("failed to create cgroup dir: %w", err)
	}

	// Set CPU limits
	if err := m.setCPULimits(cgroupPath, limits); err != nil {
		return fmt.Errorf("failed to set CPU limits: %w", err)
	}

	// Set memory limits
	if err := m.setMemoryLimits(cgroupPath, limits); err != nil {
		return fmt.Errorf("failed to set memory limits: %w", err)
	}

	// Set IO limits
	if err := m.setIOLimits(cgroupPath, limits); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to set IO limits, continuing anyway")
	}

	m.logger.Info().
		Str("task_id", taskID).
		Str("path", cgroupPath).
		Msg("Cgroup created successfully")

	return nil
}

// AddProcess adds a process to the cgroup.
func (m *CgroupManager) AddProcess(taskID string, pid int) error {
	cgroupPath := filepath.Join(m.basePath, taskID)
	procsFile := filepath.Join(cgroupPath, "cgroup.procs")

	m.logger.Debug().
		Str("task_id", taskID).
		Int("pid", pid).
		Msg("Adding process to cgroup")

	// Write PID to cgroup.procs
	data := fmt.Sprintf("%d", pid)
	if err := os.WriteFile(procsFile, []byte(data), 0644); err != nil {
		return fmt.Errorf("failed to add process to cgroup: %w", err)
	}

	m.logger.Info().
		Str("task_id", taskID).
		Int("pid", pid).
		Msg("Process added to cgroup")

	return nil
}

// RemoveCgroup removes a cgroup and all its processes.
func (m *CgroupManager) RemoveCgroup(taskID string) error {
	cgroupPath := filepath.Join(m.basePath, taskID)

	m.logger.Info().
		Str("task_id", taskID).
		Msg("Removing cgroup")

	// Move all processes to parent cgroup first
	procsFile := filepath.Join(cgroupPath, "cgroup.procs")
	if data, err := os.ReadFile(procsFile); err == nil {
		pids := strings.TrimSpace(string(data))
		if pids != "" {
			// Move to root cgroup
			rootProcs := "/sys/fs/cgroup/cgroup.procs"
			if err := os.WriteFile(rootProcs, data, 0644); err != nil {
				m.logger.Warn().Err(err).Msg("Failed to move processes to root cgroup")
			}
		}
	}

	// Remove cgroup directory
	if err := os.RemoveAll(cgroupPath); err != nil {
		return fmt.Errorf("failed to remove cgroup dir: %w", err)
	}

	m.logger.Info().
		Str("task_id", taskID).
		Msg("Cgroup removed successfully")

	return nil
}

// GetStats returns cgroup statistics for a task.
func (m *CgroupManager) GetStats(taskID string) (*CgroupStats, error) {
	cgroupPath := filepath.Join(m.basePath, taskID)

	stats := &CgroupStats{}

	// Read CPU stats
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "cpu.stat")); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				switch fields[0] {
				case "usage_usec":
					stats.CPUUsageUs, _ = strconv.ParseInt(fields[1], 10, 64)
				case "nr_periods":
					stats.CPUPeriods, _ = strconv.ParseInt(fields[1], 10, 64)
				case "nr_throttled":
					stats.CPUThrottled, _ = strconv.ParseInt(fields[1], 10, 64)
				}
			}
		}
	}

	// Read memory stats
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.stat")); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				switch fields[0] {
				case "anon":
					stats.MemoryAnon, _ = strconv.ParseInt(fields[1], 10, 64)
				case "file":
					stats.MemoryFile, _ = strconv.ParseInt(fields[1], 10, 64)
				}
			}
		}
	}

	// Read current memory usage
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.current")); err == nil {
		stats.MemoryCurrent, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}

	// Read memory max
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.max")); err == nil {
		val := strings.TrimSpace(string(data))
		if val != "max" {
			stats.MemoryMax, _ = strconv.ParseInt(val, 10, 64)
		} else {
			stats.MemoryMax = -1 // unlimited
		}
	}

	return stats, nil
}

// CgroupStats holds cgroup statistics.
type CgroupStats struct {
	CPUUsageUs     int64 `json:"cpu_usage_us"`
	CPUPeriods     int64 `json:"cpu_periods"`
	CPUThrottled   int64 `json:"cpu_throttled"`
	MemoryAnon     int64 `json:"memory_anon"`
	MemoryFile     int64 `json:"memory_file"`
	MemoryCurrent  int64 `json:"memory_current"`
	MemoryMax      int64 `json:"memory_max"`
	IOReadBytes    int64 `json:"io_read_bytes"`
	IOWriteBytes   int64 `json:"io_write_bytes"`
}

// setCPULimits configures CPU resource limits.
func (m *CgroupManager) setCPULimits(cgroupPath string, limits ResourceLimits) error {
	// Set CPU max (quota period)
	cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
	cpuMax := limits.CPUMax
	if cpuMax == "" && limits.CPUQuotaUs > 0 {
		// Default period is 1000000 (1 second)
		cpuMax = fmt.Sprintf("%d 1000000", limits.CPUQuotaUs)
	}

	if cpuMax != "" {
		m.logger.Debug().
			Str("cpu_max", cpuMax).
			Msg("Setting CPU limits")
		if err := os.WriteFile(cpuMaxPath, []byte(cpuMax), 0644); err != nil {
			return fmt.Errorf("failed to write cpu.max: %w", err)
		}
	}

	return nil
}

// setMemoryLimits configures memory resource limits.
func (m *CgroupManager) setMemoryLimits(cgroupPath string, limits ResourceLimits) error {
	// Set memory max (hard limit)
	if limits.MemoryMax > 0 {
		memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
		m.logger.Debug().
			Int64("memory_max", limits.MemoryMax).
			Msg("Setting memory max limit")
		if err := os.WriteFile(memoryMaxPath, []byte(fmt.Sprintf("%d", limits.MemoryMax)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.max: %w", err)
		}
	}

	// Set memory high (soft limit/throttle threshold)
	if limits.MemoryHigh > 0 {
		memoryHighPath := filepath.Join(cgroupPath, "memory.high")
		m.logger.Debug().
			Int64("memory_high", limits.MemoryHigh).
			Msg("Setting memory high threshold")
		if err := os.WriteFile(memoryHighPath, []byte(fmt.Sprintf("%d", limits.MemoryHigh)), 0644); err != nil {
			return fmt.Errorf("failed to write memory.high: %w", err)
		}
	}

	return nil
}

// setIOLimits configures IO resource limits.
func (m *CgroupManager) setIOLimits(cgroupPath string, limits ResourceLimits) error {
	// Set IO weight
	if limits.IOWeight > 0 {
		ioWeightPath := filepath.Join(cgroupPath, "io.weight")
		m.logger.Debug().
			Uint64("io_weight", limits.IOWeight).
			Msg("Setting IO weight")
		if err := os.WriteFile(ioWeightPath, []byte(fmt.Sprintf("%d", limits.IOWeight)), 0644); err != nil {
			return fmt.Errorf("failed to write io.weight: %w", err)
		}
	}

	// Set IO bandwidth limits (requires device major:minor)
	// This is more complex and may need device discovery
	if limits.IOReadBPS > 0 || limits.IOWriteBPS > 0 {
		m.logger.Debug().
			Int64("read_bps", limits.IOReadBPS).
			Int64("write_bps", limits.IOWriteBPS).
			Msg("IO bandwidth limits configured (device discovery needed)")
		// TODO: Implement device discovery and io.max configuration
	}

	return nil
}

// IsCgroupV2Available checks if cgroup v2 is available.
func IsCgroupV2Available() bool {
	return isCgroupV2Available()
}

// isCgroupV2Available checks if cgroup v2 is available.
func isCgroupV2Available() bool {
	// Check if cgroup2 filesystem is mounted
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return true
	}

	// Alternative check: look for cgroup2 mount
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.Contains(line, "cgroup2") {
			return true
		}
	}

	return false
}

// DetectCgroupVersion returns the cgroup version in use.
func DetectCgroupVersion() string {
	if isCgroupV2Available() {
		return "v2"
	}
	return "v1"
}
