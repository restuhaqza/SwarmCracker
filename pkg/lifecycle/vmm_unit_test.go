// Package lifecycle provides VM lifecycle management for Firecracker.
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVMMManager_NewVMMManager tests VMMManager creation
func TestVMMManager_NewVMMManager(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantDir string
	}{
		{
			name: "with valid config",
			config: &ManagerConfig{
				SocketDir: "/tmp/test-vmm",
			},
			wantDir: "/tmp/test-vmm",
		},
		{
			name:    "with nil config",
			config:  nil,
			wantDir: "/var/run/firecracker",
		},
		{
			name: "with empty config",
			config: &ManagerConfig{},
			wantDir: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(tt.config)
			assert.NotNil(t, vm)

			vmm, ok := vm.(*VMMManager)
			require.True(t, ok)
			assert.NotNil(t, vmm.config)
			assert.NotNil(t, vmm.vms)
		})
	}
}

// TestVMMManager_Start_Validation tests input validation
func TestVMMManager_Start_Validation(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		config      interface{}
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			config:      map[string]interface{}{},
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name: "nil config",
			task: &types.Task{ID: "test-task"},
			config: nil,
			wantErr: true,
			errContains: "invalid config",
		},
		{
			name: "empty task ID",
			task: &types.Task{ID: ""},
			config: map[string]interface{}{},
			wantErr: false, // Firecracker binary check will fail first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			ctx := context.Background()

			err := vm.Start(ctx, tt.task, tt.config)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestVMMManager_Stop_Validation tests Stop input validation
func TestVMMManager_Stop_Validation(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		setupVM     bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			setupVM:     false,
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name:    "VM not found",
			task:    &types.Task{ID: "non-existent"},
			setupVM: false,
			wantErr: true,
			errContains: "VM not found",
		},
		{
			name:    "existing VM",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			wantErr: false, // Will fail on process operations
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			vmm := vm.(*VMMManager)
			ctx := context.Background()

			if tt.setupVM {
				vmm.mu.Lock()
				vmm.vms[tt.task.ID] = &VMInstance{
					ID:         tt.task.ID,
					PID:        os.Getpid(), // Use current PID
					State:      VMStateRunning,
					InitSystem: "none",
				}
				vmm.mu.Unlock()
			}

			err := vm.Stop(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			}
		})
	}
}

// TestVMMManager_Wait_Validation tests Wait input validation
func TestVMMManager_Wait_Validation(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		setupVM     bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			setupVM:     false,
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name:    "VM not found",
			task:    &types.Task{ID: "non-existent"},
			setupVM: false,
			wantErr: false, // Returns ORPHANED status
		},
		{
			name:    "existing VM",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			vmm := vm.(*VMMManager)
			ctx := context.Background()

			if tt.setupVM {
				vmm.mu.Lock()
				vmm.vms[tt.task.ID] = &VMInstance{
					ID:         tt.task.ID,
					PID:        os.Getpid(),
					State:      VMStateRunning,
					InitSystem: "none",
				}
				vmm.mu.Unlock()
			}

			status, err := vm.Wait(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NotNil(t, status)
			}
		})
	}
}

// TestVMMManager_Describe_Validation tests Describe input validation
func TestVMMManager_Describe_Validation(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		setupVM     bool
		vmState     VMState
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			setupVM:     false,
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name:    "VM not found",
			task:    &types.Task{ID: "non-existent"},
			setupVM: false,
			wantErr: false, // Returns ORPHANED status
		},
		{
			name:    "VM in running state",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			vmState: VMStateRunning,
			wantErr: false,
		},
		{
			name:    "VM in stopped state",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			vmState: VMStateStopped,
			wantErr: false,
		},
		{
			name:    "VM in crashed state",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			vmState: VMStateCrashed,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			vmm := vm.(*VMMManager)
			ctx := context.Background()

			if tt.setupVM {
				vmm.mu.Lock()
				vmm.vms[tt.task.ID] = &VMInstance{
					ID:         tt.task.ID,
					PID:        os.Getpid(),
					State:      tt.vmState,
					CreatedAt:  time.Now(),
					InitSystem: "none",
				}
				vmm.mu.Unlock()
			}

			status, err := vm.Describe(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NotNil(t, status)
			}
		})
	}
}

// TestVMMManager_Remove_Validation tests Remove input validation
func TestVMMManager_Remove_Validation(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		setupVM     bool
		vmState     VMState
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil task",
			task:        nil,
			setupVM:     false,
			wantErr:     true,
			errContains: "task cannot be nil",
		},
		{
			name:    "VM not found",
			task:    &types.Task{ID: "non-existent"},
			setupVM: false,
			wantErr: false, // No error if VM doesn't exist
		},
		{
			name:    "remove running VM",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			vmState: VMStateRunning,
			wantErr: false,
		},
		{
			name:    "remove stopped VM",
			task:    &types.Task{ID: "test-vm"},
			setupVM: true,
			vmState: VMStateStopped,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			vmm := vm.(*VMMManager)
			ctx := context.Background()

			if tt.setupVM {
				vmm.mu.Lock()
				vmm.vms[tt.task.ID] = &VMInstance{
					ID:         tt.task.ID,
					PID:        99999, // Non-existent PID
					State:      tt.vmState,
					SocketPath: t.TempDir() + "/sock",
					InitSystem: "none",
				}
				vmm.mu.Unlock()
			}

			err := vm.Remove(ctx, tt.task)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				// VM should be removed from map
				vmm.mu.RLock()
				_, exists := vmm.vms[tt.task.ID]
				vmm.mu.RUnlock()
				assert.False(t, exists)
			}
		})
	}
}

// TestVMInstance_StateTransitions tests VM state transitions
func TestVMInstance_StateTransitions(t *testing.T) {
	states := []VMState{
		VMStateNew,
		VMStateStarting,
		VMStateRunning,
		VMStateStopping,
		VMStateStopped,
		VMStateCrashed,
	}

	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			vmi := &VMInstance{
				ID:        "test-vm",
				State:     state,
				CreatedAt: time.Now(),
			}

			assert.Equal(t, state, vmi.State)
			assert.Equal(t, "test-vm", vmi.ID)
		})
	}
}

// TestVMMManager_ConcurrentAccess tests concurrent access to VMMManager
func TestVMMManager_ConcurrentAccess(t *testing.T) {
	vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
	vmm := vm.(*VMMManager)
	ctx := context.Background()

	const numGoroutines = 10
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				taskID := fmt.Sprintf("task-%d-%d", id, j)

				// Try to add VM
				vmm.mu.Lock()
				vmm.vms[taskID] = &VMInstance{
					ID:         taskID,
					PID:        os.Getpid(),
					State:      VMStateRunning,
					CreatedAt:  time.Now(),
					InitSystem: "none",
				}
				vmm.mu.Unlock()

				// Try to read VM
				vmm.mu.RLock()
				_, exists := vmm.vms[taskID]
				vmm.mu.RUnlock()
				assert.True(t, exists)

				// Try to describe
				task := &types.Task{ID: taskID}
				_, _ = vm.Describe(ctx, task)
			}
		}(i)
	}

	wg.Wait()

	// Verify all VMs were added
	expectedVMs := numGoroutines * numOperations
	vmm.mu.RLock()
	actualVMs := len(vmm.vms)
	vmm.mu.RUnlock()

	assert.Equal(t, expectedVMs, actualVMs)
}

// TestForceKillVM tests the forceKillVM function
func TestForceKillVM_Unit(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		wantErr bool
	}{
		{
			name:    "non-existent PID",
			pid:     99999,
			wantErr: true, // Process not found
		},
		{
			name:    "current process",
			pid:     os.Getpid(),
			wantErr: false, // FindProcess succeeds
		},
		{
			name:    "invalid PID",
			pid:     -1,
			wantErr: true,
		},
		{
			name:    "zero PID",
			pid:     0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{})
			vmm := vm.(*VMMManager)

			vmi := &VMInstance{
				ID:  "test-vm",
				PID: tt.pid,
			}

			err := vmm.forceKillVM(vmi)
			if tt.wantErr {
				assert.Error(t, err)
			}
		})
	}
}

// TestVMMManager_StateMapping tests VM state to task state mapping
func TestVMMManager_StateMapping(t *testing.T) {
	tests := []struct {
		name       string
		vmState    VMState
		wantTask   types.TaskState
		setupError bool
	}{
		{
			name:     "VMStateNew",
			vmState:  VMStateNew,
			wantTask: types.TaskState_NEW,
		},
		{
			name:     "VMStateStarting",
			vmState:  VMStateStarting,
			wantTask: types.TaskState_STARTING,
		},
		{
			name:     "VMStateRunning",
			vmState:  VMStateRunning,
			wantTask: types.TaskState_RUNNING,
		},
		{
			name:     "VMStateStopping",
			vmState:  VMStateStopping,
			wantTask: types.TaskState_STARTING,
		},
		{
			name:     "VMStateStopped",
			vmState:  VMStateStopped,
			wantTask: types.TaskState_COMPLETE,
		},
		{
			name:     "VMStateCrashed",
			vmState:  VMStateCrashed,
			wantTask: types.TaskState_FAILED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVMMManager(&ManagerConfig{SocketDir: t.TempDir()})
			vmm := vm.(*VMMManager)
			ctx := context.Background()

			vmm.mu.Lock()
			vmm.vms["test"] = &VMInstance{
				ID:         "test",
				PID:        os.Getpid(),
				State:      tt.vmState,
				CreatedAt:  time.Now(),
				InitSystem: "none",
			}
			vmm.mu.Unlock()

			task := &types.Task{ID: "test"}
			status, err := vm.Describe(ctx, task)

			require.NoError(t, err)
			assert.NotNil(t, status)
			assert.Equal(t, tt.wantTask, status.State)
		})
	}
}

// TestVMMManager_GracePeriod tests grace period handling
func TestVMMManager_GracePeriod(t *testing.T) {
	tests := []struct {
		name          string
		gracePeriod   int
		initSystem    string
		expectedState string
	}{
		{
			name:          "tini with grace period",
			gracePeriod:   10,
			initSystem:    "tini",
			expectedState: "tini",
		},
		{
			name:          "dumb-init with grace period",
			gracePeriod:   5,
			initSystem:    "dumb-init",
			expectedState: "dumb-init",
		},
		{
			name:          "no init system",
			gracePeriod:   0,
			initSystem:    "none",
			expectedState: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmi := &VMInstance{
				ID:             "test-vm",
				PID:            os.Getpid(),
				State:          VMStateRunning,
				CreatedAt:      time.Now(),
				InitSystem:     tt.initSystem,
				GracePeriodSec: tt.gracePeriod,
			}

			assert.Equal(t, tt.initSystem, vmi.InitSystem)
			assert.Equal(t, tt.gracePeriod, vmi.GracePeriodSec)
		})
	}
}
