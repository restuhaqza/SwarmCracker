package jailer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	if mgr == nil { //nolint:staticcheck // checked for nil before dereference
		t.Fatal("Expected non-nil cgroup manager")
	}
	basePath := mgr.basePath //nolint:staticcheck // t.Fatal terminates test
	if basePath != testPath {
		t.Errorf("Expected base path %q, got %q", testPath, basePath)
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
		CPUQuotaUs: 500000,    // 0.5 CPU
		MemoryMax:  268435456, // 256MB
		MemoryHigh: 241591910, // 230MB (90% of max)
		IOWeight:   100,
		IOReadBPS:  0,
		IOWriteBPS: 0,
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

	if stats == nil { //nolint:staticcheck // checked for nil before dereference
		t.Fatal("Expected non-nil stats")
	}
	cpuUsage := stats.CPUUsageUs       //nolint:staticcheck // t.Fatal terminates test
	cpuPeriods := stats.CPUPeriods     //nolint:staticcheck
	cpuThrottled := stats.CPUThrottled //nolint:staticcheck
	memCurrent := stats.MemoryCurrent  //nolint:staticcheck
	memMax := stats.MemoryMax          //nolint:staticcheck

	// Verify stats structure
	t.Logf("CPU Usage: %d µs", cpuUsage)
	t.Logf("CPU Periods: %d", cpuPeriods)
	t.Logf("CPU Throttled: %d", cpuThrottled)
	t.Logf("Memory Current: %d bytes", memCurrent)
	t.Logf("Memory Max: %d bytes", memMax)
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
				CPUQuotaUs: 0,
				MemoryMax:  0,
				MemoryHigh: 0,
				IOWeight:   0,
			},
			wantErr: false, // Should be allowed (unlimited)
		},
		{
			name: "minimal limits",
			limits: ResourceLimits{
				CPUQuotaUs: 10000,   // 0.01 CPU
				MemoryMax:  1048576, // 1MB
				MemoryHigh: 524288,  // 512KB
				IOWeight:   1,
			},
			wantErr: false,
		},
		{
			name: "high limits",
			limits: ResourceLimits{
				CPUQuotaUs: 8000000,    // 8 CPUs
				MemoryMax:  8589934592, // 8GB
				MemoryHigh: 7730941133, // ~7.2GB
				IOWeight:   10000,
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
		name        string
		quotaUs     int64
		expectedMax string
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
		IOWeight:   500,      // Higher than default (100)
		IOReadBPS:  10485760, // 10 MB/s read limit
		IOWriteBPS: 10485760, // 10 MB/s write limit
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
		CPUUsageUs:    123456,
		CPUPeriods:    100,
		CPUThrottled:  5,
		MemoryAnon:    104857600,
		MemoryFile:    52428800,
		MemoryCurrent: 157286400,
		MemoryMax:     268435456,
		IOReadBytes:   1048576,
		IOWriteBytes:  524288,
	}

	if stats.CPUUsageUs <= 0 {
		t.Error("Expected positive CPU usage")
	}
	if stats.MemoryMax <= 0 {
		t.Error("Expected positive memory max")
	}

	t.Logf("Stats: %+v", stats)
}

// --- cgroup v2 primitives: cpuset, controllers, io.weight -------------------

// newTempCgroupManager builds a manager rooted in a temp dir. CreateCgroup,
// EnsureControllers and OnlineCPUs never touch /sys through this path, so the
// tests below need neither root nor a cgroup v2 host.
func newTempCgroupManager(t *testing.T) (*CgroupManager, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "swarmcracker")
	require.NoError(t, os.MkdirAll(base, 0o755))
	return &CgroupManager{basePath: base, logger: zerolog.Nop()}, base
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	return string(data)
}

// TestCreateCgroup_WritesCanonicalV2Files asserts the exact contents of every
// file the manager writes, including the canonical cpuset values and the v2
// io.weight syntax ("default <weight>", not a bare number).
func TestCreateCgroup_WritesCanonicalV2Files(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-full"

	require.NoError(t, mgr.CreateCgroup(taskID, ResourceLimits{
		CPUQuotaUs: 500000,
		MemoryMax:  268435456,
		MemoryHigh: 241591910,
		IOWeight:   500,
		CpusetCPUs: "3,1-2",
		CpusetMems: "2,0",
	}))

	cgroupPath := filepath.Join(base, taskID)
	want := map[string]string{
		"cpuset.cpus": "1-3",
		"cpuset.mems": "0,2",
		"cpu.max":     "500000 1000000",
		"memory.max":  "268435456",
		"memory.high": "241591910",
		"io.weight":   "default 500",
	}
	for file, value := range want {
		assert.Equal(t, value, readFileString(t, filepath.Join(cgroupPath, file)), file)
	}
}

// TestCreateCgroup_NoCpusetWritesWhenUnset makes sure the cpuset controller is
// left alone when the caller did not ask for pinning.
func TestCreateCgroup_NoCpusetWritesWhenUnset(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-unpinned"

	require.NoError(t, mgr.CreateCgroup(taskID, ResourceLimits{CPUQuotaUs: 250000}))

	for _, file := range []string{"cpuset.cpus", "cpuset.mems"} {
		_, err := os.Stat(filepath.Join(base, taskID, file))
		assert.True(t, os.IsNotExist(err), "%s should not be written", file)
	}
}

// TestCreateCgroup_CpusetMemsResolution covers the three mems sources:
// explicit, inherited from the parent cgroup, and the host's online nodes.
func TestCreateCgroup_CpusetMemsResolution(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		mgr, base := newTempCgroupManager(t)
		require.NoError(t, mgr.CreateCgroup("task-mems-explicit", ResourceLimits{
			CpusetCPUs: "2",
			CpusetMems: "2,0",
		}))
		assert.Equal(t, "0,2", readFileString(t, filepath.Join(base, "task-mems-explicit", "cpuset.mems")))
	})

	t.Run("inherited from parent cgroup", func(t *testing.T) {
		mgr, base := newTempCgroupManager(t)
		require.NoError(t, os.WriteFile(filepath.Join(base, "cpuset.mems"), []byte("0-1\n"), 0o644))
		// A broken host fallback proves the parent value was used.
		mgr.nodeOnlinePath = filepath.Join(t.TempDir(), "missing-nodes")

		require.NoError(t, mgr.CreateCgroup("task-mems-parent", ResourceLimits{CpusetCPUs: "2"}))
		assert.Equal(t, "0-1", readFileString(t, filepath.Join(base, "task-mems-parent", "cpuset.mems")))
	})

	t.Run("host fallback", func(t *testing.T) {
		mgr, base := newTempCgroupManager(t)
		nodes := filepath.Join(t.TempDir(), "node-online")
		require.NoError(t, os.WriteFile(nodes, []byte("1,0-2\n"), 0o644))
		mgr.nodeOnlinePath = nodes

		require.NoError(t, mgr.CreateCgroup("task-mems-host", ResourceLimits{CpusetCPUs: "2"}))
		assert.Equal(t, "0-2", readFileString(t, filepath.Join(base, "task-mems-host", "cpuset.mems")))
	})
}

// TestCreateCgroup_MemsBeforeProcs asserts the ordering guarantee: when mems
// cannot be resolved, CreateCgroup fails without touching cgroup.procs, so no
// process can ever land in a cpuset cgroup with an empty mems list.
func TestCreateCgroup_MemsBeforeProcs(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-order"
	cgroupPath := filepath.Join(base, taskID)
	require.NoError(t, os.MkdirAll(cgroupPath, 0o755))

	procsPath := filepath.Join(cgroupPath, "cgroup.procs")
	require.NoError(t, os.WriteFile(procsPath, []byte("sentinel"), 0o644))

	// No CpusetMems, no ancestor cpuset.mems, no host node list.
	mgr.nodeOnlinePath = filepath.Join(t.TempDir(), "missing-node-online")

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetCPUs: "0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cpuset.mems")
	assert.Contains(t, err.Error(), "missing-node-online")

	assert.Equal(t, "sentinel", readFileString(t, procsPath),
		"cgroup.procs must not be written when cpuset.mems cannot be resolved")
	_, statErr := os.Stat(filepath.Join(cgroupPath, "cpuset.mems"))
	assert.True(t, os.IsNotExist(statErr), "cpuset.mems must not exist after a resolution failure")
}

// TestCreateCgroup_CpusetRejectionNamesRequestAndOnline asserts a rejected
// cpuset reports both what was asked for and what the host actually has.
func TestCreateCgroup_CpusetRejectionNamesRequestAndOnline(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-reject"
	cgroupPath := filepath.Join(base, taskID)
	// A directory in place of cpuset.cpus makes the write fail, which is the
	// temp-dir equivalent of a kernel rejection.
	require.NoError(t, os.MkdirAll(filepath.Join(cgroupPath, "cpuset.cpus"), 0o755))

	online := filepath.Join(t.TempDir(), "cpu-online")
	require.NoError(t, os.WriteFile(online, []byte("0-3\n"), 0o644))
	mgr.cpuOnlinePath = online

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetCPUs: "4-7", CpusetMems: "0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"4-7"`, "error must name the requested cpuset")
	assert.Contains(t, err.Error(), "0-3", "error must name the host's online CPUs")
}

// TestCreateCgroup_InvalidCpusetRejected asserts a malformed cpuset is
// rejected up front rather than written and left to the kernel.
func TestCreateCgroup_InvalidCpusetRejected(t *testing.T) {
	tests := []struct {
		name   string
		limits ResourceLimits
		want   string
	}{
		{name: "reversed cpus", limits: ResourceLimits{CpusetCPUs: "5-3"}, want: "CpusetCPUs"},
		{name: "malformed cpus", limits: ResourceLimits{CpusetCPUs: "1,,2"}, want: "CpusetCPUs"},
		{name: "empty element mems", limits: ResourceLimits{CpusetCPUs: "0", CpusetMems: "1-"}, want: "CpusetMems"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, _ := newTempCgroupManager(t)
			err := mgr.CreateCgroup("task-invalid", tt.limits)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Contains(t, err.Error(), "invalid cpuset")
		})
	}
}

// TestCreateCgroup_IOWeightValidation covers the IOWeight guard: a negative
// value is rejected before anything is created, while 0 stays "unset" (no
// io.weight written) and a positive value is written verbatim.
func TestCreateCgroup_IOWeightValidation(t *testing.T) {
	tests := []struct {
		name     string
		weight   int
		wantErr  bool
		wantFile string // "" means io.weight must not be written
	}{
		{name: "zero is unset", weight: 0, wantFile: ""},
		{name: "positive is written", weight: 500, wantFile: "default 500"},
		{name: "negative rejected", weight: -1, wantErr: true},
		{name: "large negative rejected", weight: -500, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, base := newTempCgroupManager(t)
			taskID := "task-io-weight"

			err := mgr.CreateCgroup(taskID, ResourceLimits{IOWeight: tt.weight})
			if !tt.wantErr {
				require.NoError(t, err)
				ioWeightPath := filepath.Join(base, taskID, "io.weight")
				if tt.wantFile == "" {
					_, statErr := os.Stat(ioWeightPath)
					assert.True(t, os.IsNotExist(statErr), "io.weight must not be written when IOWeight is 0")
				} else {
					assert.Equal(t, tt.wantFile, readFileString(t, ioWeightPath))
				}
				return
			}

			require.Error(t, err, "a negative IOWeight must be rejected, not skipped")
			assert.Contains(t, err.Error(), "IOWeight", "error must name the field")
			assert.Contains(t, err.Error(), fmt.Sprintf("%d", tt.weight), "error must name the offending value")
			_, statErr := os.Stat(filepath.Join(base, taskID))
			assert.True(t, os.IsNotExist(statErr), "a rejected request must not create a cgroup directory")
		})
	}
}

// TestOnlineCPUs covers the /sys/devices/system/cpu/online reader.
func TestOnlineCPUs(t *testing.T) {
	t.Run("canonicalises", func(t *testing.T) {
		mgr, _ := newTempCgroupManager(t)
		path := filepath.Join(t.TempDir(), "online")
		require.NoError(t, os.WriteFile(path, []byte("3,0-2\n"), 0o644))
		mgr.cpuOnlinePath = path

		got, err := mgr.OnlineCPUs()
		require.NoError(t, err)
		assert.Equal(t, "0-3", got)
	})

	t.Run("unreadable", func(t *testing.T) {
		mgr, _ := newTempCgroupManager(t)
		mgr.cpuOnlinePath = filepath.Join(t.TempDir(), "missing-online")

		_, err := mgr.OnlineCPUs()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read online CPUs")
		assert.Contains(t, err.Error(), "missing-online")
	})

	t.Run("malformed", func(t *testing.T) {
		mgr, _ := newTempCgroupManager(t)
		path := filepath.Join(t.TempDir(), "online")
		require.NoError(t, os.WriteFile(path, []byte("a\n"), 0o644))
		mgr.cpuOnlinePath = path

		_, err := mgr.OnlineCPUs()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid online CPU list")
	})
}

// newFakeControllerHierarchy builds a two-level fake cgroup tree in a temp dir:
// parent (usually the cgroup root) and base (the manager's base path). Both
// advertise the same controllers and start with nothing enabled.
func newFakeControllerHierarchy(t *testing.T) (mgr *CgroupManager, parent, base string) {
	t.Helper()
	parent = filepath.Join(t.TempDir(), "cgroup")
	base = filepath.Join(parent, "swarmcracker")
	require.NoError(t, os.MkdirAll(base, 0o755))
	for _, dir := range []string{parent, base} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.controllers"),
			[]byte("cpu cpuset memory io pids\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), nil, 0o644))
	}
	return &CgroupManager{basePath: base, logger: zerolog.Nop()}, parent, base
}

func controllerSet(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	set, err := readControllerSet(path)
	require.NoError(t, err)
	return set
}

// TestEnsureControllers_EnablesAtBaseAndParent covers the happy path plus
// idempotency: a second call must not rewrite the files.
func TestEnsureControllers_EnablesAtBaseAndParent(t *testing.T) {
	mgr, parent, base := newFakeControllerHierarchy(t)
	requested := []string{"cpu", "memory", "io", "cpuset"}

	require.NoError(t, mgr.EnsureControllers(requested...))

	for _, dir := range []string{parent, base} {
		path := filepath.Join(dir, "cgroup.subtree_control")
		assert.ElementsMatch(t, requested, keysOf(controllerSet(t, path)), "controllers at %s", dir)
		assert.NotContains(t, readFileString(t, path), "-", "nothing may be disabled")
	}

	before := map[string]string{
		parent: readFileString(t, filepath.Join(parent, "cgroup.subtree_control")),
		base:   readFileString(t, filepath.Join(base, "cgroup.subtree_control")),
	}
	require.NoError(t, mgr.EnsureControllers(requested...))
	for dir, content := range before {
		assert.Equal(t, content, readFileString(t, filepath.Join(dir, "cgroup.subtree_control")),
			"second call must be a no-op for %s", dir)
	}
}

// TestEnsureControllers_Additive keeps controllers that were enabled by someone
// else (pids here) and only adds the requested ones.
func TestEnsureControllers_Additive(t *testing.T) {
	mgr, parent, base := newFakeControllerHierarchy(t)
	for _, dir := range []string{parent, base} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+pids"), 0o644))
	}

	require.NoError(t, mgr.EnsureControllers("cpu", "cpuset", "cpu"))

	for _, dir := range []string{parent, base} {
		path := filepath.Join(dir, "cgroup.subtree_control")
		assert.ElementsMatch(t, []string{"pids", "cpu", "cpuset"}, keysOf(controllerSet(t, path)), "controllers at %s", dir)
		assert.NotContains(t, readFileString(t, path), "-", "nothing may be disabled")
	}
}

// TestEnsureControllers_Unavailable asserts a controller the kernel does not
// advertise is reported in the typed error and never written.
func TestEnsureControllers_Unavailable(t *testing.T) {
	mgr, parent, base := newFakeControllerHierarchy(t)
	// rdma is not advertised anywhere in the fake hierarchy.
	require.NoError(t, os.WriteFile(filepath.Join(base, "cgroup.controllers"),
		[]byte("cpu cpuset memory io pids\n"), 0o644))

	err := mgr.EnsureControllers("cpu", "rdma")
	require.Error(t, err)

	var cerr *ControllersError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, []string{"cpu", "rdma"}, cerr.Requested)
	assert.Equal(t, []string{"rdma"}, cerr.Unavailable)
	assert.Contains(t, err.Error(), "not advertised by the kernel")

	// cpu was still enabled; rdma was never written.
	assert.Contains(t, keysOf(controllerSet(t, filepath.Join(base, "cgroup.subtree_control"))), "cpu")
	assert.NotContains(t, readFileString(t, filepath.Join(parent, "cgroup.subtree_control")), "rdma")
	assert.NotContains(t, readFileString(t, filepath.Join(base, "cgroup.subtree_control")), "rdma")
}

// TestEnsureControllers_MissingBaseControllers asserts a base path that is not
// a cgroup directory fails explicitly instead of panicking.
func TestEnsureControllers_MissingBaseControllers(t *testing.T) {
	mgr, _, base := newFakeControllerHierarchy(t)
	require.NoError(t, os.Remove(filepath.Join(base, "cgroup.controllers")))

	err := mgr.EnsureControllers("cpu")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cgroup.controllers")
	assert.Contains(t, err.Error(), base)
}

// TestEnsureControllers_ParentUnwritable asserts a parent that cannot be
// written is reported per controller, naming the parent, and leaves the base
// untouched.
func TestEnsureControllers_ParentUnwritable(t *testing.T) {
	mgr, parent, base := newFakeControllerHierarchy(t)
	require.NoError(t, os.Remove(filepath.Join(parent, "cgroup.subtree_control")))

	err := mgr.EnsureControllers("cpu")
	require.Error(t, err)

	var cerr *ControllersError
	require.ErrorAs(t, err, &cerr)
	assert.Contains(t, cerr.Failures["cpu"], parent)
	assert.Contains(t, cerr.Failures["cpu"], "cannot delegate through parent")
	assert.Empty(t, readFileString(t, filepath.Join(base, "cgroup.subtree_control")),
		"base must not be touched when the parent cannot be delegated")
}

// TestEnsureControllers_ParentDoesNotAdvertise asserts a controller the parent
// does not advertise is reported as unavailable rather than written anyway.
func TestEnsureControllers_ParentDoesNotAdvertise(t *testing.T) {
	mgr, parent, base := newFakeControllerHierarchy(t)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "cgroup.controllers"),
		[]byte("cpu memory\n"), 0o644))

	err := mgr.EnsureControllers("cpuset")
	require.Error(t, err)

	var cerr *ControllersError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, []string{"cpuset"}, cerr.Unavailable)
	assert.Contains(t, err.Error(), "not advertised by the kernel")
	assert.Empty(t, readFileString(t, filepath.Join(base, "cgroup.subtree_control")))
	assert.Empty(t, readFileString(t, filepath.Join(parent, "cgroup.subtree_control")))
}

// TestEnsureControllers_InputValidation asserts empty/ill-formed controller
// names are rejected before anything is written.
func TestEnsureControllers_InputValidation(t *testing.T) {
	tests := []struct {
		name    string
		request []string
	}{
		{name: "empty name", request: []string{""}},
		{name: "write syntax", request: []string{"+cpu"}},
		{name: "space inside", request: []string{"cpu memory"}},
		{name: "uppercase", request: []string{"CPU"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, parent, base := newFakeControllerHierarchy(t)
			err := mgr.EnsureControllers(tt.request...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid cgroup controller name")
			for _, dir := range []string{parent, base} {
				assert.Empty(t, readFileString(t, filepath.Join(dir, "cgroup.subtree_control")))
			}
		})
	}

	t.Run("no controllers requested", func(t *testing.T) {
		mgr, _, _ := newFakeControllerHierarchy(t)
		assert.NoError(t, mgr.EnsureControllers())
	})
}

// keysOf is a small helper for set assertions.
func keysOf(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// TestCreateCgroup_MemsRejectionNamesMemoryNodes pins the axis of a cpuset.mems
// failure hint: the host list that matters there is NUMA memory nodes, not CPUs.
func TestCreateCgroup_MemsRejectionNamesMemoryNodes(t *testing.T) {
	mgr, base := newTempCgroupManager(t)
	taskID := "task-mems-reject"
	// A directory in place of cpuset.mems makes the write fail, which is the
	// temp-dir equivalent of a kernel rejection.
	require.NoError(t, os.MkdirAll(filepath.Join(base, taskID, "cpuset.mems"), 0o755))

	nodes := filepath.Join(t.TempDir(), "node-online")
	require.NoError(t, os.WriteFile(nodes, []byte("0-1\n"), 0o644))
	mgr.nodeOnlinePath = nodes

	err := mgr.CreateCgroup(taskID, ResourceLimits{CpusetMems: "0"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "online memory nodes: 0-1")
	assert.NotContains(t, err.Error(), "online CPUs")
}
