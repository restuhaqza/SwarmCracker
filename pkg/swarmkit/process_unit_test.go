package swarmkit

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_GetRunningProcesses tests GetRunningProcesses method
func TestVMMManager_GetRunningProcesses(t *testing.T) {
	t.Run("empty processes", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		procs := vmm.GetRunningProcesses()
		assert.Empty(t, procs)
	})

	t.Run("single process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		cmd := exec.Command("sleep", "1")
		err := cmd.Start()
		require.NoError(t, err)
		t.Cleanup(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		})

		vmm.processes["task-1"] = cmd

		procs := vmm.GetRunningProcesses()
		assert.Len(t, procs, 1)
		assert.NotNil(t, procs["task-1"])
	})

	t.Run("multiple processes", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Add multiple mock processes
		for i := 1; i <= 3; i++ {
			cmd := exec.Command("sleep", "1")
			err := cmd.Start()
			require.NoError(t, err)
			t.Cleanup(func() {
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
			})
			vmm.processes["task-"+string(rune('0'+i))] = cmd
		}

		procs := vmm.GetRunningProcesses()
		assert.Len(t, procs, 3)
	})

	t.Run("returns copy not reference", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		cmd := exec.Command("sleep", "1")
		err := cmd.Start()
		require.NoError(t, err)
		t.Cleanup(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		})

		vmm.processes["task-1"] = cmd

		procs := vmm.GetRunningProcesses()

		// Modify returned map - should not affect original
		procs["task-2"] = exec.Command("sleep", "1")

		vmm.processMutex.Lock()
		assert.Len(t, vmm.processes, 1)
		vmm.processMutex.Unlock()
	})
}

// TestVMMManager_RemoveProcess tests RemoveProcess method
func TestVMMManager_RemoveProcess(t *testing.T) {
	t.Run("remove existing process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		cmd := exec.Command("sleep", "1")
		err := cmd.Start()
		require.NoError(t, err)
		t.Cleanup(func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		})

		vmm.processes["task-1"] = cmd

		vmm.RemoveProcess("task-1")

		vmm.processMutex.Lock()
		_, exists := vmm.processes["task-1"]
		vmm.processMutex.Unlock()
		assert.False(t, exists)
	})

	t.Run("remove nonexistent process", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		// Should not panic
		vmm.RemoveProcess("task-nonexistent")

		vmm.processMutex.Lock()
		assert.Empty(t, vmm.processes)
		vmm.processMutex.Unlock()
	})

	t.Run("remove from multiple processes", func(t *testing.T) {
		tmpDir := t.TempDir()
		vmm := &VMMManager{
			firecrackerPath: "firecracker",
			socketDir:       tmpDir,
			processes:       make(map[string]*exec.Cmd),
			processMutex:    sync.Mutex{},
		}

		for i := 1; i <= 3; i++ {
			cmd := exec.Command("sleep", "1")
			err := cmd.Start()
			require.NoError(t, err)
			t.Cleanup(func() {
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
			})
			vmm.processes[string(rune('A'+i))] = cmd
		}

		vmm.RemoveProcess("B")

		vmm.processMutex.Lock()
		assert.Len(t, vmm.processes, 2)
		_, exists := vmm.processes["B"]
		vmm.processMutex.Unlock()
		assert.False(t, exists)
	})
}

// TestExecutor_CleanupOrphanedVMs tests cleanupOrphanedVMs method
func TestExecutor_CleanupOrphanedVMs(t *testing.T) {
	t.Run("no vmm manager", func(t *testing.T) {
		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			executorMu:  sync.RWMutex{},
			vmmMgr:      nil,
		}

		ctx := context.Background()
		e.cleanupOrphanedVMs(ctx)
		// Should not panic
	})

	t.Run("no running processes", func(t *testing.T) {
		mockVMM := &mockVMMProcessTracker{
			processes: make(map[string]*exec.Cmd),
		}

		e := &Executor{
			config:      &Config{},
			controllers: make(map[string]*Controller),
			executorMu:  sync.RWMutex{},
			vmmMgr:      mockVMM,
		}

		ctx := context.Background()
		e.cleanupOrphanedVMs(ctx)
		// Should complete without error
	})

	t.Run("all processes are active", func(t *testing.T) {
		mockVMM := &mockVMMProcessTracker{
			processes: map[string]*exec.Cmd{
				"task-1": exec.Command("sleep", "1"),
				"task-2": exec.Command("sleep", "1"),
			},
		}

		e := &Executor{
			config:      &Config{},
			controllers: map[string]*Controller{
				"task-1": &Controller{task: &api.Task{ID: "task-1"}},
				"task-2": &Controller{task: &api.Task{ID: "task-2"}},
			},
			executorMu: sync.RWMutex{},
			vmmMgr:     mockVMM,
		}

		ctx := context.Background()
		e.cleanupOrphanedVMs(ctx)

		// All processes should remain - no orphan cleanup
		assert.Len(t, mockVMM.processes, 2)
	})

	t.Run("orphaned process cleanup", func(t *testing.T) {
		// Create a real process that will be orphaned
		orphanCmd := exec.Command("sleep", "5")
		err := orphanCmd.Start()
		require.NoError(t, err)

		mockVMM := &mockVMMProcessTracker{
			processes: map[string]*exec.Cmd{
				"orphan-task": orphanCmd,
			},
			removedTasks: make([]string, 0),
		}

		socketDir := t.TempDir()
		// Create socket file for orphan
		socketPath := socketDir + "/orphan-task.sock"
		os.WriteFile(socketPath, []byte("dummy"), 0644)

		e := &Executor{
			config: &Config{
				SocketDir: socketDir,
			},
			controllers: map[string]*Controller{
				"active-task": &Controller{task: &api.Task{ID: "active-task"}},
			},
			executorMu: sync.RWMutex{},
			vmmMgr:     mockVMM,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		e.cleanupOrphanedVMs(ctx)

		// Process should be killed
		assert.Len(t, mockVMM.removedTasks, 1)
		assert.Contains(t, mockVMM.removedTasks, "orphan-task")

		// Socket should be cleaned up
		_, err = os.Stat(socketPath)
		assert.True(t, os.IsNotExist(err))
	})

	t.Run("orphaned process signal fails", func(t *testing.T) {
		// Create a process that's already dead (simulates signal failure)
		deadCmd := exec.Command("true")
		deadCmd.Start()
		deadCmd.Wait() // Let it exit

		mockVMM := &mockVMMProcessTracker{
			processes: map[string]*exec.Cmd{
				"dead-task": deadCmd,
			},
			removedTasks: make([]string, 0),
		}

		e := &Executor{
			config:      &Config{SocketDir: t.TempDir()},
			controllers: make(map[string]*Controller), // No active tasks
			executorMu:  sync.RWMutex{},
			vmmMgr:      mockVMM,
		}

		ctx := context.Background()
		e.cleanupOrphanedVMs(ctx)

		// Should still attempt cleanup and remove
		assert.Contains(t, mockVMM.removedTasks, "dead-task")
	})
}

// TestExecutor_CleanupOrphanedVMs_Concurrent tests concurrent safety
func TestExecutor_CleanupOrphanedVMs_Concurrent(t *testing.T) {
	mockVMM := &mockVMMProcessTracker{
		processes: make(map[string]*exec.Cmd),
		removedTasks: make([]string, 0),
		mu: sync.Mutex{},
	}

	// Add some processes
	for i := 0; i < 5; i++ {
		taskID := string(rune('A' + i))
		cmd := exec.Command("sleep", "1")
		cmd.Start()
		mockVMM.processes[taskID] = cmd
	}

	e := &Executor{
		config:      &Config{SocketDir: t.TempDir()},
		controllers: make(map[string]*Controller),
		executorMu:  sync.RWMutex{},
		vmmMgr:      mockVMM,
	}

	// Run cleanup concurrently
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			e.cleanupOrphanedVMs(ctx)
		}()
	}
	wg.Wait()

	// All processes should be orphaned and cleaned up
	// Due to concurrent execution, removedTasks may have duplicates
	// The important thing is that no race condition occurs
	mockVMM.mu.Lock()
	assert.GreaterOrEqual(t, len(mockVMM.removedTasks), 5)
	mockVMM.mu.Unlock()
}

// Mock VMM manager that tracks process removal

type mockVMMProcessTracker struct {
	processes    map[string]*exec.Cmd
	removedTasks []string
	mu           sync.Mutex
}

func (m *mockVMMProcessTracker) Start(ctx context.Context, task *types.Task, config interface{}) error {
	return nil
}

func (m *mockVMMProcessTracker) Stop(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMProcessTracker) ForceStop(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMProcessTracker) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	return &types.TaskStatus{State: types.TaskStateComplete}, nil
}

func (m *mockVMMProcessTracker) Remove(ctx context.Context, task *types.Task) error {
	return nil
}

func (m *mockVMMProcessTracker) GetPID(taskID string) int {
	return 0
}

func (m *mockVMMProcessTracker) CheckVMAPIHealth(taskID string) bool {
	return false
}

func (m *mockVMMProcessTracker) IsRunning(taskID string) bool {
	return false
}

func (m *mockVMMProcessTracker) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	return &types.TaskStatus{State: types.TaskStateComplete}, nil
}

func (m *mockVMMProcessTracker) GetRunningProcesses() map[string]*exec.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return a copy
	result := make(map[string]*exec.Cmd)
	for k, v := range m.processes {
		result[k] = v
	}
	return result
}

func (m *mockVMMProcessTracker) RemoveProcess(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processes, taskID)
	m.removedTasks = append(m.removedTasks, taskID)
}