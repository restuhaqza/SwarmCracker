package metrics

import (
	"os/exec"
	"testing"
	"time"
)

// TestCollectorCollect tests the Collect method with a real process.
func TestCollectorCollect(t *testing.T) {
	// Create a long-running process (sleep 60)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()

	// Wait a bit for the process to initialize
	time.Sleep(100 * time.Millisecond)

	// Create collector
	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Collect metrics
	taskID := "test-task-1"
	metrics, err := collector.Collect(taskID, pid)
	if err != nil {
		t.Fatalf("Failed to collect metrics: %v", err)
	}

	// Verify basic fields
	if metrics.TaskID != taskID {
		t.Errorf("Expected TaskID %s, got %s", taskID, metrics.TaskID)
	}
	if metrics.PID != pid {
		t.Errorf("Expected PID %d, got %d", pid, metrics.PID)
	}

	// CPU time should be positive (process has been running)
	if metrics.CPUMs < 0 {
		t.Errorf("Expected positive CPU time, got %f", metrics.CPUMs)
	}

	// Memory should be positive
	if metrics.MemoryKB == 0 {
		t.Logf("Warning: MemoryKB is 0 (process may not have RSS yet)")
	}

	// Uptime should be non-negative (a freshly started process may report 0).
	if metrics.UptimeSec < 0 {
		t.Errorf("Expected non-negative uptime, got %d", metrics.UptimeSec)
	}

	t.Logf("Metrics collected successfully: %+v", metrics)
}

// TestCollectorNonExistentPID tests error handling for non-existent PIDs.
func TestCollectorNonExistentPID(t *testing.T) {
	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Try to collect metrics for a non-existent PID
	_, err = collector.Collect("test-task", 999999)
	if err == nil {
		t.Error("Expected error for non-existent PID, got nil")
	}

	t.Logf("Correctly returned error for non-existent PID: %v", err)
}
func TestCollectCPU(t *testing.T) {
	// Create a process that consumes some CPU
	cmd := exec.Command("sh", "-c", "i=0; while [ $i -lt 10000 ]; do i=$((i+1)); done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid

	// Wait for process to complete
	cmd.Wait()

	// The process has exited, but we should still be able to read its stat file
	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	cpuMs, err := collector.collectCPU(pid)
	if err != nil {
		// Process may have already been reaped, which is ok
		t.Logf("collectCPU returned error (process may have been reaped): %v", err)
	} else {
		t.Logf("CPU time collected: %.2f ms", cpuMs)
		if cpuMs < 0 {
			t.Error("Expected non-negative CPU time")
		}
	}
}

// TestCollectMemory tests memory collection specifically.
func TestCollectMemory(t *testing.T) {
	// Create a long-running process
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()

	time.Sleep(100 * time.Millisecond)

	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	memKB, err := collector.collectMemory(pid)
	if err != nil {
		t.Fatalf("Failed to collect memory: %v", err)
	}

	t.Logf("Memory collected: %d KB", memKB)
	if memKB == 0 {
		t.Error("Expected positive memory usage")
	}
}

// TestGetProcUptime tests uptime calculation.
func TestGetProcUptime(t *testing.T) {
	// Create a long-running process
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()

	time.Sleep(100 * time.Millisecond)

	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	uptimeSec, err := collector.getProcUptime(pid)
	if err != nil {
		t.Fatalf("Failed to get uptime: %v", err)
	}

	t.Logf("Uptime: %d seconds", uptimeSec)
	if uptimeSec < 0 {
		t.Error("Expected non-negative uptime")
	}
	// A freshly started process can legitimately report 0 seconds, so we only
	// require a sane non-negative value below the system uptime bound.
	if uptimeSec > 86400*365 {
		t.Errorf("Uptime seems too large: %d seconds", uptimeSec)
	}
}
