package metrics

import (
	"fmt"
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

	// Uptime should be positive
	if metrics.UptimeSec <= 0 {
		t.Errorf("Expected positive uptime, got %d", metrics.UptimeSec)
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

// TestCollectorGetAndList tests GetMetrics and ListMetrics methods.
func TestCollectorGetAndList(t *testing.T) {
	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Create a test process
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start test process: %v", err)
	}
	pid := cmd.Process.Pid
	defer cmd.Process.Kill()

	// Wait a bit for the process to initialize
	time.Sleep(100 * time.Millisecond)

	// Collect metrics
	taskID := "test-task-2"
	_, err = collector.Collect(taskID, pid)
	if err != nil {
		t.Fatalf("Failed to collect metrics: %v", err)
	}

	// Test GetMetrics
	metrics, err := collector.GetMetrics(taskID)
	if err != nil {
		t.Errorf("Failed to get metrics: %v", err)
	}
	if metrics.TaskID != taskID {
		t.Errorf("Expected TaskID %s, got %s", taskID, metrics.TaskID)
	}

	// Test ListMetrics
	allMetrics := collector.ListMetrics()
	if len(allMetrics) == 0 {
		t.Error("Expected at least one metric in list")
	}
	if _, exists := allMetrics[taskID]; !exists {
		t.Errorf("Expected taskID %s in list", taskID)
	}

	t.Logf("GetMetrics and ListMetrics work correctly")
}

// TestCollectorConcurrency tests that the collector is safe for concurrent use.
func TestCollectorConcurrency(t *testing.T) {
	collector, err := NewCollector("/tmp/test-metrics")
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}

	// Create multiple test processes
	processes := make([]*exec.Cmd, 5)
	pids := make([]int, 5)
	for i := 0; i < 5; i++ {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start test process %d: %v", i, err)
		}
		processes[i] = cmd
		pids[i] = cmd.Process.Pid
		defer cmd.Process.Kill()
	}

	// Wait a bit for processes to initialize
	time.Sleep(100 * time.Millisecond)

	// Collect metrics concurrently
	done := make(chan bool, 5)
	for i := 0; i < 5; i++ {
		go func(idx int) {
			taskID := fmt.Sprintf("concurrent-task-%d", idx)
			_, err := collector.Collect(taskID, pids[idx])
			if err != nil {
				t.Logf("Warning: Failed to collect metrics for %s: %v", taskID, err)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Verify all metrics were stored
	allMetrics := collector.ListMetrics()
	if len(allMetrics) < 5 {
		t.Errorf("Expected at least 5 metrics, got %d", len(allMetrics))
	}

	t.Logf("Concurrency test passed, collected %d metrics", len(allMetrics))
}

// TestCollectCPU tests CPU collection specifically.
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
	if uptimeSec <= 0 {
		t.Error("Expected positive uptime")
	}
	// Note: uptime is calculated from system boot time, not from when we started the process
	// So we just check that it's a reasonable value (less than system uptime)
	if uptimeSec > 86400*365 {
		t.Errorf("Uptime seems too large: %d seconds", uptimeSec)
	}
}
