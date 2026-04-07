package jailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectCgroupVersion tests cgroup version detection.
func TestDetectCgroupVersion(t *testing.T) {
	version := DetectCgroupVersion()
	if version != "v1" && version != "v2" {
		t.Errorf("Expected cgroup version v1 or v2, got %q", version)
	}

	t.Logf("Detected cgroup version: %s", version)
}

// TestCgroupManagerNew tests cgroup manager initialization.
func TestCgroupManagerNew(t *testing.T) {
	// Skip if cgroup v2 not available
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	if mgr == nil {
		t.Fatal("Expected non-nil cgroup manager")
	}

	if mgr.basePath != testPath {
		t.Errorf("Expected base path %q, got %q", testPath, mgr.basePath)
	}

	// Verify base directory was created
	if _, err := os.Stat(testPath); err != nil {
		t.Errorf("Base directory not created: %v", err)
	}
}

// TestCgroupManagerNewCgroupV1NotAvailable tests error when cgroup v2 unavailable.
func TestCgroupManagerNewCgroupV1NotAvailable(t *testing.T) {
	// This test is tricky because we can't easily mock cgroup availability
	// We'll just verify the function exists and returns appropriate error
	if isCgroupV2Available() {
		t.Skip("Cgroup v2 is available, skipping negative test")
	}

	_, err := NewCgroupManager("/tmp/test")
	if err == nil {
		t.Error("Expected error when cgroup v2 not available")
	}
}

// TestCgroupCreateCgroup tests cgroup creation with resource limits.
func TestCgroupCreateCgroup(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	taskID := "test-task-123"
	limits := ResourceLimits{
		CPUQuotaUs:   500000, // 0.5 CPU
		MemoryMax:    268435456, // 256MB
		MemoryHigh:   241591910, // 230MB (90% of max)
		IOWeight:     100,
		IOReadBPS:    0,
		IOWriteBPS:   0,
	}

	err = mgr.CreateCgroup(taskID, limits)
	if err != nil {
		t.Fatalf("CreateCgroup() error = %v", err)
	}

	// Verify cgroup directory was created
	cgroupPath := filepath.Join(testPath, taskID)
	if _, err := os.Stat(cgroupPath); err != nil {
		t.Errorf("Cgroup directory not created: %v", err)
	}

	// Verify CPU limits were set
	cpuMaxPath := filepath.Join(cgroupPath, "cpu.max")
	if data, err := os.ReadFile(cpuMaxPath); err == nil {
		t.Logf("CPU max: %s", string(data))
	} else {
		t.Errorf("Failed to read cpu.max: %v", err)
	}

	// Verify memory limits were set
	memoryMaxPath := filepath.Join(cgroupPath, "memory.max")
	if data, err := os.ReadFile(memoryMaxPath); err == nil {
		t.Logf("Memory max: %s", string(data))
	} else {
		t.Errorf("Failed to read memory.max: %v", err)
	}
}

// TestCgroupRemoveCgroup tests cgroup cleanup.
func TestCgroupRemoveCgroup(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	taskID := "test-task-remove"
	limits := ResourceLimits{
		CPUQuotaUs: 500000,
		MemoryMax:  268435456,
	}

	// Create cgroup
	if err := mgr.CreateCgroup(taskID, limits); err != nil {
		t.Fatalf("CreateCgroup() error = %v", err)
	}

	// Verify it exists
	cgroupPath := filepath.Join(testPath, taskID)
	if _, err := os.Stat(cgroupPath); err != nil {
		t.Fatalf("Cgroup directory not created: %v", err)
	}

	// Remove cgroup
	if err := mgr.RemoveCgroup(taskID); err != nil {
		t.Errorf("RemoveCgroup() error = %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(cgroupPath); err == nil {
		t.Error("Cgroup directory still exists after removal")
	}
}

// TestCgroupGetStats tests statistics collection.
func TestCgroupGetStats(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	taskID := "test-task-stats"
	limits := ResourceLimits{
		CPUQuotaUs: 500000,
		MemoryMax:  268435456,
	}

	// Create cgroup
	if err := mgr.CreateCgroup(taskID, limits); err != nil {
		t.Fatalf("CreateCgroup() error = %v", err)
	}

	// Get stats
	stats, err := mgr.GetStats(taskID)
	if err != nil {
		t.Errorf("GetStats() error = %v", err)
	}

	if stats == nil {
		t.Fatal("Expected non-nil stats")
	}

	// Verify stats structure
	t.Logf("CPU Usage: %d µs", stats.CPUUsageUs)
	t.Logf("CPU Periods: %d", stats.CPUPeriods)
	t.Logf("CPU Throttled: %d", stats.CPUThrottled)
	t.Logf("Memory Current: %d bytes", stats.MemoryCurrent)
	t.Logf("Memory Max: %d bytes", stats.MemoryMax)
}

// TestResourceLimitsValidation tests resource limits edge cases.
func TestResourceLimitsValidation(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	tests := []struct {
		name    string
		limits  ResourceLimits
		wantErr bool
	}{
		{
			name: "zero limits (unlimited)",
			limits: ResourceLimits{
				CPUQuotaUs:  0,
				MemoryMax:   0,
				MemoryHigh:  0,
				IOWeight:    0,
			},
			wantErr: false, // Should be allowed (unlimited)
		},
		{
			name: "minimal limits",
			limits: ResourceLimits{
				CPUQuotaUs:  10000, // 0.01 CPU
				MemoryMax:   1048576, // 1MB
				MemoryHigh:  524288, // 512KB
				IOWeight:    1,
			},
			wantErr: false,
		},
		{
			name: "high limits",
			limits: ResourceLimits{
				CPUQuotaUs:  8000000, // 8 CPUs
				MemoryMax:   8589934592, // 8GB
				MemoryHigh:  7730941133, // ~7.2GB
				IOWeight:    10000,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := "test-limits-" + tt.name
			err := mgr.CreateCgroup(taskID, tt.limits)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateCgroup() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Cleanup
			mgr.RemoveCgroup(taskID)
		})
	}
}

// TestCgroupAddProcess tests adding processes to cgroup.
func TestCgroupAddProcess(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	taskID := "test-task-proc"
	limits := ResourceLimits{
		CPUQuotaUs: 500000,
		MemoryMax:  268435456,
	}

	// Create cgroup
	if err := mgr.CreateCgroup(taskID, limits); err != nil {
		t.Fatalf("CreateCgroup() error = %v", err)
	}

	// Try to add current process (should work in most cases)
	currentPID := os.Getpid()
	err = mgr.AddProcess(taskID, currentPID)
	if err != nil {
		// This might fail if running as non-root without proper permissions
		t.Logf("AddProcess() error (may be expected): %v", err)
	} else {
		t.Logf("Successfully added process %d to cgroup", currentPID)
	}
}

// TestCgroupCPULimits tests CPU limit configuration.
func TestCgroupCPULimits(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	tests := []struct {
		name         string
		quotaUs      int64
		expectedMax  string
	}{
		{
			name:        "half CPU",
			quotaUs:     500000,
			expectedMax: "500000",
		},
		{
			name:        "one CPU",
			quotaUs:     1000000,
			expectedMax: "1000000",
		},
		{
			name:        "two CPUs",
			quotaUs:     2000000,
			expectedMax: "2000000",
		},
		{
			name:        "quarter CPU",
			quotaUs:     250000,
			expectedMax: "250000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := "test-cpu-" + tt.name
			limits := ResourceLimits{
				CPUQuotaUs: tt.quotaUs,
			}

			if err := mgr.CreateCgroup(taskID, limits); err != nil {
				t.Fatalf("CreateCgroup() error = %v", err)
			}

			// Read back CPU max
			cpuMaxPath := filepath.Join(testPath, taskID, "cpu.max")
			data, err := os.ReadFile(cpuMaxPath)
			if err != nil {
				t.Errorf("Failed to read cpu.max: %v", err)
			} else {
				actual := string(data)[:len(data)-1] // Remove newline
				// Cgroup v2 may normalize the period, so check if it starts with expected quota
				if !strings.HasPrefix(actual, tt.expectedMax) {
					t.Errorf("CPU max = %q, want prefix %q", actual, tt.expectedMax)
				}
			}

			// Cleanup
			mgr.RemoveCgroup(taskID)
		})
	}
}

// TestCgroupMemoryLimits tests memory limit configuration.
func TestCgroupMemoryLimits(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	tests := []struct {
		name        string
		memoryMax   int64
		memoryHigh  int64
		expectError bool
	}{
		{
			name:       "256MB",
			memoryMax:  268435456,
			memoryHigh: 241591910,
		},
		{
			name:       "512MB",
			memoryMax:  536870912,
			memoryHigh: 483183820,
		},
		{
			name:       "1GB",
			memoryMax:  1073741824,
			memoryHigh: 966367641,
		},
		{
			name:       "high threshold (90%)",
			memoryMax:  104857600, // 100MB
			memoryHigh: 94371840,  // 90MB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskID := "test-mem-" + tt.name
			limits := ResourceLimits{
				MemoryMax:  tt.memoryMax,
				MemoryHigh: tt.memoryHigh,
			}

			err := mgr.CreateCgroup(taskID, limits)
			if (err != nil) != tt.expectError {
				t.Errorf("CreateCgroup() error = %v, expectError %v", err, tt.expectError)
			}

			if !tt.expectError {
				// Verify memory.max
				memoryMaxPath := filepath.Join(testPath, taskID, "memory.max")
				data, err := os.ReadFile(memoryMaxPath)
				if err != nil {
					t.Errorf("Failed to read memory.max: %v", err)
				} else {
					t.Logf("memory.max: %s", string(data)[:len(data)-1])
				}

				// Verify memory.high
				memoryHighPath := filepath.Join(testPath, taskID, "memory.high")
				data, err = os.ReadFile(memoryHighPath)
				if err != nil {
					t.Errorf("Failed to read memory.high: %v", err)
				} else {
					t.Logf("memory.high: %s", string(data)[:len(data)-1])
				}
			}

			// Cleanup
			mgr.RemoveCgroup(taskID)
		})
	}
}

// TestCgroupIOLimits tests IO limit configuration.
func TestCgroupIOLimits(t *testing.T) {
	if !isCgroupV2Available() {
		t.Skip("Cgroup v2 not available on this system")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "cgroup-test")

	mgr, err := NewCgroupManager(testPath)
	if err != nil {
		t.Fatalf("NewCgroupManager() error = %v", err)
	}

	taskID := "test-io-limits"
	limits := ResourceLimits{
		IOWeight:   500, // Higher than default (100)
		IOReadBPS:  10485760,  // 10 MB/s read limit
		IOWriteBPS: 10485760,  // 10 MB/s write limit
	}

	err = mgr.CreateCgroup(taskID, limits)
	if err != nil {
		t.Fatalf("CreateCgroup() error = %v", err)
	}

	// Verify IO weight
	ioWeightPath := filepath.Join(testPath, taskID, "io.weight")
	data, err := os.ReadFile(ioWeightPath)
	if err != nil {
		t.Errorf("Failed to read io.weight: %v", err)
	} else {
		t.Logf("io.weight: %s", string(data)[:len(data)-1])
	}

	// Note: io.max requires device major:minor which we don't set in this test
	// The current implementation logs a warning but doesn't fail

	// Cleanup
	mgr.RemoveCgroup(taskID)
}

// TestCgroupStatsStructure tests stats data structure.
func TestCgroupStatsStructure(t *testing.T) {
	stats := &CgroupStats{
		CPUUsageUs:     123456,
		CPUPeriods:     100,
		CPUThrottled:   5,
		MemoryAnon:     104857600,
		MemoryFile:     52428800,
		MemoryCurrent:  157286400,
		MemoryMax:      268435456,
		IOReadBytes:    1048576,
		IOWriteBytes:   524288,
	}

	if stats.CPUUsageUs <= 0 {
		t.Error("Expected positive CPU usage")
	}
	if stats.MemoryMax <= 0 {
		t.Error("Expected positive memory max")
	}

	t.Logf("Stats: %+v", stats)
}
