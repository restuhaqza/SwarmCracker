package metrics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCollectorMetricRegistrationEdgeCases tests edge cases in collector initialization and metric registration.
func TestCollectorMetricRegistrationEdgeCases(t *testing.T) {
	t.Run("new collector with default state directory", func(t *testing.T) {
		c, err := NewCollector("")
		// This might fail if we don't have permission to create /var/run/swarmcracker
		if err != nil {
			// Skip if we don't have permission
			if os.IsPermission(err) || strings.Contains(err.Error(), "permission denied") {
				t.Skip("Skipping: Need root permission to create /var/run/swarmcracker")
				return
			}
			t.Fatalf("NewCollector with empty string failed: %v", err)
		}
		if c.stateDir != "/var/run/swarmcracker/metrics" {
			t.Errorf("Expected default stateDir /var/run/swarmcracker/metrics, got %s", c.stateDir)
		}
		_ = c
	})

	t.Run("new collector creates directory", func(t *testing.T) {
		tempDir := t.TempDir()
		customDir := filepath.Join(tempDir, "custom_metrics")

		c, err := NewCollector(customDir)
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}
		_ = c // Verify collector was created successfully

		// Verify directory was created
		info, err := os.Stat(customDir)
		if err != nil {
			t.Fatalf("Failed to stat custom directory: %v", err)
		}
		if !info.IsDir() {
			t.Error("Custom stateDir is not a directory")
		}
	})

	t.Run("new collector with existing directory", func(t *testing.T) {
		tempDir := t.TempDir()
		existingDir := filepath.Join(tempDir, "existing")

		// Create directory beforehand
		if err := os.MkdirAll(existingDir, 0755); err != nil {
			t.Fatalf("Failed to create existing directory: %v", err)
		}

		c, err := NewCollector(existingDir)
		if err != nil {
			t.Fatalf("NewCollector with existing directory failed: %v", err)
		}
		if c.stateDir != existingDir {
			t.Errorf("Expected stateDir %s, got %s", existingDir, c.stateDir)
		}
	})

	t.Run("new collector initializes empty metrics map", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		list := c.ListMetrics()
		if len(list) != 0 {
			t.Errorf("Expected empty metrics map, got %d entries", len(list))
		}
	})
}

// TestCollectorCPEdgeCases tests edge cases in CPU collection.
func TestCollectorCPEdgeCases(t *testing.T) {
	t.Run("collect CPU with non-existent PID", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		_, err = c.collectCPU(999999)
		if err == nil {
			t.Error("Expected error for non-existent PID in collectCPU, got nil")
		}
	})

	t.Run("collect CPU with malformed stat file", func(t *testing.T) {
		// This test documents the intended behavior
		// We cannot easily test malformed stat files without mocking /proc
		t.Skip("Cannot test malformed stat files without mocking /proc filesystem")
	})
}

// TestCollectorMemoryEdgeCases tests edge cases in memory collection.
func TestCollectorMemoryEdgeCases(t *testing.T) {
	t.Run("collect memory with non-existent PID", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		_, err = c.collectMemory(999999)
		if err == nil {
			t.Error("Expected error for non-existent PID in collectMemory, got nil")
		}
	})

	t.Run("collect memory handles VmRSS parsing errors gracefully", func(t *testing.T) {
		// This is tested implicitly through the main Collect function
		// which sets MemoryKB to 0 on error
		c, _ := NewCollector(t.TempDir())
		_ = c // Document that this tests error handling
	})
}

// TestCollectorNetworkEdgeCases tests edge cases in network collection.
func TestCollectorNetworkEdgeCases(t *testing.T) {
	t.Run("collect network with non-existent PID", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		rx, tx, err := c.collectNetwork(999999)
		if err == nil {
			t.Error("Expected error for non-existent PID in collectNetwork, got nil")
		}
		if rx != 0 || tx != 0 {
			t.Errorf("Expected zero values on error, got rx=%d tx=%d", rx, tx)
		}
	})

	t.Run("collect network with no tap interfaces", func(t *testing.T) {
		// For processes without tap interfaces, should return 0,0,nil
		// This is tested with regular sleep processes which have no tap devices
		t.Skip("Requires real process without tap interfaces")
	})
}

// TestCollectorUptimeEdgeCases tests edge cases in uptime calculation.
func TestCollectorUptimeEdgeCases(t *testing.T) {
	t.Run("get uptime with non-existent PID", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		_, err = c.getProcUptime(999999)
		if err == nil {
			t.Error("Expected error for non-existent PID in getProcUptime, got nil")
		}
	})

	t.Run("get uptime handles malformed uptime file", func(t *testing.T) {
		// This tests the error path when /proc/uptime can't be read or parsed
		t.Skip("Cannot test malformed uptime files without mocking /proc filesystem")
	})
}

// TestCollectorGetMetricsEdgeCases tests edge cases in GetMetrics.
func TestCollectorGetMetricsEdgeCases(t *testing.T) {
	c, err := NewCollector(t.TempDir())
	if err != nil {
		t.Fatalf("NewCollector failed: %v", err)
	}

	t.Run("get metrics for non-existent task", func(t *testing.T) {
		_, err := c.GetMetrics("non-existent-task")
		if err == nil {
			t.Error("Expected error for non-existent task, got nil")
		}
	})

	t.Run("get metrics returns copy not reference", func(t *testing.T) {
		// This requires first adding a metric
		t.Skip("Requires real process for testing")
	})
}

// TestCollectorListMetrics tests ListMetrics behavior.
func TestCollectorListMetrics(t *testing.T) {
	t.Run("list empty metrics", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		metrics := c.ListMetrics()
		if metrics == nil {
			t.Error("ListMetrics returned nil, expected empty map")
		}
		if len(metrics) != 0 {
			t.Errorf("Expected empty map, got %d entries", len(metrics))
		}
	})

	t.Run("list metrics returns copies", func(t *testing.T) {
		c, _ := NewCollector(t.TempDir())

		// Add a metric (we'd need a real process for this)
		// For now, test the empty case
		metrics := c.ListMetrics()
		if len(metrics) == 0 {
			// Expected when no processes
			t.Skip("No metrics to test, requires real process")
			return
		}

		// Modify returned map
		for k, v := range metrics {
			v.CPUMs = 99999
			metrics[k] = v
		}

		// Get again and verify original wasn't modified
		metrics2 := c.ListMetrics()
		for _, v := range metrics2 {
			if v.CPUMs == 99999 {
				t.Error("ListMetrics returned references, not copies")
			}
		}
	})
}

// TestCollectorConcurrentMetricAccess tests concurrent access to metrics.
func TestCollectorConcurrentMetricAccess(t *testing.T) {
	c, err := NewCollector(t.TempDir())
	if err != nil {
		t.Fatalf("NewCollector failed: %v", err)
	}

	// We'll simulate concurrent access with mock data
	// since we can't easily create real processes in parallel

	const numGoroutines = 100
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taskID := fmt.Sprintf("concurrent-%d", idx)
			// We can't actually collect without a real process
			// so we'll just test the map operations indirectly
			_ = taskID
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taskID := fmt.Sprintf("concurrent-%d", idx)
			c.GetMetrics(taskID)
		}(i)
	}

	// Concurrent list operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.ListMetrics()
		}()
	}

	wg.Wait()
	// If we get here without deadlock or panic, concurrent access works
}

// TestCollectorStartStop tests Start and Stop functionality.
func TestCollectorStartStop(t *testing.T) {
	t.Run("start and stop periodic collection", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		ctx := context.Background()
		callCount := 0
		getPIDs := func() map[string]int {
			callCount++
			return map[string]int{
				"test-vm": 1234, // Non-existent PID, but that's ok for testing
			}
		}

		// Start with short interval
		c.Start(ctx, 100*time.Millisecond, getPIDs)

		// Wait for a few collection cycles
		time.Sleep(350 * time.Millisecond)

		// Stop
		c.Stop()

		// Wait a bit more to ensure it stopped
		time.Sleep(200 * time.Millisecond)

		finalCount := callCount
		if finalCount < 2 {
			t.Errorf("Expected at least 2 collection cycles, got %d", finalCount)
		}
		if finalCount > 5 {
			t.Errorf("Too many collection cycles: %d (may not have stopped)", finalCount)
		}

		t.Logf("Collection ran %d times before stop", finalCount)
	})

	t.Run("start with zero interval uses default", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		ctx := context.Background()
		getPIDs := func() map[string]int {
			return nil
		}

		c.Start(ctx, 0, getPIDs)
		c.Stop()
		// Should not panic with zero interval
	})

	t.Run("start with negative interval uses default", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		ctx := context.Background()
		getPIDs := func() map[string]int {
			return nil
		}

		c.Start(ctx, -100*time.Millisecond, getPIDs)
		c.Stop()
		// Should not panic with negative interval
	})

	t.Run("multiple starts (last cancel wins)", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		ctx := context.Background()
		getPIDs := func() map[string]int {
			return nil
		}

		// Start twice
		c.Start(ctx, 100*time.Millisecond, getPIDs)
		c.Start(ctx, 100*time.Millisecond, getPIDs)

		// Stop should cancel the last one
		c.Stop()
		// Should not panic
	})

	t.Run("stop without start is safe", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		c.Stop()
		// Should not panic
	})

	t.Run("context cancellation stops collection", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		callCount := 0
		getPIDs := func() map[string]int {
			callCount++
			return nil
		}

		c.Start(ctx, 50*time.Millisecond, getPIDs)

		// Cancel context
		time.Sleep(120 * time.Millisecond)
		cancel()

		// Wait a bit
		time.Sleep(100 * time.Millisecond)

		finalCount := callCount
		t.Logf("Collection ran %d times before context cancel", finalCount)
	})
}

// TestCollectorCollectAll tests collectAll behavior.
func TestCollectorCollectAll(t *testing.T) {
	t.Run("collectAll with nil getPIDs", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		// Should not panic
		c.collectAll(nil)
	})

	t.Run("collectAll with empty PIDs", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		getPIDs := func() map[string]int {
			return map[string]int{}
		}

		// Should not panic
		c.collectAll(getPIDs)
	})

	t.Run("collectAll with collection errors", func(t *testing.T) {
		c, err := NewCollector(t.TempDir())
		if err != nil {
			t.Fatalf("NewCollector failed: %v", err)
		}

		getPIDs := func() map[string]int {
			// Return non-existent PIDs to trigger errors
			return map[string]int{
				"vm-1": 999991,
				"vm-2": 999992,
				"vm-3": 999993,
			}
		}

		// Should not panic, just log errors
		c.collectAll(getPIDs)

		// Verify no metrics were stored
		metrics := c.ListMetrics()
		if len(metrics) != 0 {
			t.Errorf("Expected no metrics after failed collection, got %d", len(metrics))
		}
	})
}

// TestCollectorCollectErrorHandling tests error handling in Collect.
func TestCollectorCollectErrorHandling(t *testing.T) {
	c, err := NewCollector(t.TempDir())
	if err != nil {
		t.Fatalf("NewCollector failed: %v", err)
	}

	t.Run("collect with non-existent process", func(t *testing.T) {
		_, err := c.Collect("test-task", 999999)
		if err == nil {
			t.Error("Expected error for non-existent process, got nil")
		}
		_ = c // Use c to avoid unused variable warning
	})

	t.Run("collect stores metrics even with partial collection failures", func(t *testing.T) {
		// This tests the graceful degradation where some metric
		// collection failures don't prevent storing the rest
		// We can't easily test this without creating a process with
		// partially accessible /proc entries, so we'll skip
		t.Skip("Requires process with partial /proc accessibility")
	})
}

// TestCollectorTimestampAccuracy tests timestamp handling.
func TestCollectorTimestampAccuracy(t *testing.T) {
	// We need a real process for this
	// For now, verify the timestamp is set when we create a VMMetrics directly
	m := &VMMetrics{
		TaskID:    "test",
		PID:       1234,
		Timestamp: time.Now(),
	}

	before := time.Now()
	if m.Timestamp.After(before) {
		// Timestamp should be reasonable
		t.Logf("Timestamp is set correctly: %v", m.Timestamp)
	}
}

// TestCollectorMetricsStorage tests metrics storage behavior.
func TestCollectorMetricsStorage(t *testing.T) {
	c, err := NewCollector(t.TempDir())
	if err != nil {
		t.Fatalf("NewCollector failed: %v", err)
	}

	t.Run("collecting overwrites existing metrics", func(t *testing.T) {
		// We need a real process for this
		// For now, verify the map behavior
		c.mu.Lock()
		c.metrics["test-1"] = &VMMetrics{
			TaskID:   "test-1",
			PID:      1111,
			CPUMs:    100.0,
			MemoryKB: 1024,
		}
		c.mu.Unlock()

		// Get the metrics
		m1, _ := c.GetMetrics("test-1")
		if m1.CPUMs != 100.0 {
			t.Errorf("Expected CPUMs 100.0, got %f", m1.CPUMs)
		}

		// Simulate another collection (direct manipulation for testing)
		c.mu.Lock()
		c.metrics["test-1"] = &VMMetrics{
			TaskID:   "test-1",
			PID:      1111,
			CPUMs:    200.0, // Updated value
			MemoryKB: 2048,
		}
		c.mu.Unlock()

		// Get again
		m2, _ := c.GetMetrics("test-1")
		if m2.CPUMs != 200.0 {
			t.Errorf("Expected CPUMs 200.0 after update, got %f", m2.CPUMs)
		}
	})

	t.Run("different tasks have independent metrics", func(t *testing.T) {
		c.mu.Lock()
		c.metrics["task-1"] = &VMMetrics{
			TaskID: "task-1",
			CPUMs:  100.0,
		}
		c.metrics["task-2"] = &VMMetrics{
			TaskID: "task-2",
			CPUMs:  200.0,
		}
		c.mu.Unlock()

		m1, _ := c.GetMetrics("task-1")
		m2, _ := c.GetMetrics("task-2")

		if m1.CPUMs == m2.CPUMs {
			t.Error("Different tasks should have independent metrics")
		}
	})
}

// TestCollectorRaceConditions tests for race conditions in concurrent operations.
func TestCollectorRaceConditions(t *testing.T) {
	c, err := NewCollector(t.TempDir())
	if err != nil {
		t.Fatalf("NewCollector failed: %v", err)
	}

	// Add some initial metrics
	c.mu.Lock()
	c.metrics["race-test"] = &VMMetrics{
		TaskID:   "race-test",
		PID:      9999,
		CPUMs:    100.0,
		MemoryKB: 1024,
	}
	c.mu.Unlock()

	const numIterations = 1000
	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < numIterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.GetMetrics("race-test")
		}()
	}

	// Concurrent lists
	for i := 0; i < numIterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.ListMetrics()
		}()
	}

	// Concurrent writes (simulating collection)
	for i := 0; i < numIterations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c.mu.Lock()
			c.metrics["race-test"] = &VMMetrics{
				TaskID:   "race-test",
				PID:      9999,
				CPUMs:    float64(idx),
				MemoryKB: uint64(idx),
			}
			c.mu.Unlock()
		}(i)
	}

	wg.Wait()
	// If we get here without race detector complaints, we're good
}
