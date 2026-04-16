package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestNewStateManager tests that NewStateManager creates the state file in the correct location.
func TestNewStateManager(t *testing.T) {
	// Set a temporary HOME directory to avoid messing with the actual user's state
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sm, err := NewStateManager("/ignored/path")
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	// Verify state file path is correct (non-root path)
	expectedPath := filepath.Join(tempHome, ".swarmcracker", "state.json")
	if sm.stateFile != expectedPath {
		t.Errorf("Expected state file %q, got %q", expectedPath, sm.stateFile)
	}

	// Verify the directory was created
	if _, err := os.Stat(filepath.Dir(sm.stateFile)); os.IsNotExist(err) {
		t.Error("State directory was not created")
	}
}

// TestAdd tests adding VM states.
func TestAdd(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	tests := []struct {
		name      string
		state     *VMState
		wantErr   bool
		errContains string
	}{
		{
			name: "valid state",
			state: &VMState{
				ID:       "vm-1",
				Image:    "nginx:latest",
				VCPUs:    2,
				MemoryMB: 2048,
			},
			wantErr: false,
		},
		{
			name: "empty ID",
			state: &VMState{
				Image: "nginx:latest",
			},
			wantErr:     true,
			errContains: "VM ID cannot be empty",
		},
		{
			name: "state with all fields",
			state: &VMState{
				ID:         "vm-2",
				PID:        12345,
				SocketPath: "/tmp/vm.sock",
				Image:      "redis:latest",
				Status:     "running",
				Command:    []string{"qemu", "-name", "vm-2"},
				VCPUs:      4,
				MemoryMB:   4096,
				KernelPath: "/boot/vmlinuz",
				RootfsPath: "/var/lib/vm/rootfs",
				LogPath:    "/var/log/vm.log",
				NetworkID:  "net-1",
				IPAddresses: []string{"10.0.0.2", "10.0.0.3"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sm.Add(tt.state)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				} else if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("Error message mismatch: got %q, want %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

// TestAddDefaults tests that Add sets default values for StartTime and Status.
func TestAddDefaults(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	// Create state with zero values
	state := &VMState{
		ID:    "vm-defaults",
		Image: "nginx:latest",
	}

	if err := sm.Add(state); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Check defaults
	if state.StartTime.IsZero() {
		t.Error("StartTime should be set by Add")
	}
	if state.Status != "running" {
		t.Errorf("Status should default to 'running', got %q", state.Status)
	}
}

// TestGet tests retrieving VM states.
func TestGet(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	original := &VMState{
		ID:       "vm-get",
		Image:    "test:latest",
		Status:   "running",
		VCPUs:    2,
		MemoryMB: 1024,
	}

	if err := sm.Add(original); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t.Run("get existing state", func(t *testing.T) {
		got, err := sm.Get("vm-get")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if got.ID != original.ID {
			t.Errorf("ID mismatch: got %q, want %q", got.ID, original.ID)
		}
		if got.Image != original.Image {
			t.Errorf("Image mismatch: got %q, want %q", got.Image, original.Image)
		}
		if got.VCPUs != original.VCPUs {
			t.Errorf("VCPUs mismatch: got %d, want %d", got.VCPUs, original.VCPUs)
		}
	})

	t.Run("get returns copy", func(t *testing.T) {
		got, err := sm.Get("vm-get")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		// Modify the returned copy
		got.Image = "modified-image"
		got.VCPUs = 999

		// Get again to verify original wasn't modified
		got2, err := sm.Get("vm-get")
		if err != nil {
			t.Fatalf("Second Get failed: %v", err)
		}

		if got2.Image == "modified-image" {
			t.Error("Get returned a reference, not a copy")
		}
		if got2.VCPUs == 999 {
			t.Error("Get returned a reference, not a copy")
		}
	})

	t.Run("get non-existent state", func(t *testing.T) {
		_, err := sm.Get("does-not-exist")
		if err == nil {
			t.Error("Expected error for non-existent VM, got nil")
		}
	})
}

// TestList tests listing all VM states.
func TestList(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	// Add multiple states
	states := []*VMState{
		{ID: "vm-1", Image: "nginx:latest"},
		{ID: "vm-2", Image: "redis:latest"},
		{ID: "vm-3", Image: "postgres:latest"},
	}

	for _, state := range states {
		if err := sm.Add(state); err != nil {
			t.Fatalf("Add failed: %v", err)
		}
	}

	t.Run("list all states", func(t *testing.T) {
		got := sm.List()

		if len(got) != len(states) {
			t.Errorf("List returned %d states, want %d", len(got), len(states))
		}

		// Check all IDs are present
		gotIDs := make(map[string]bool)
		for _, s := range got {
			gotIDs[s.ID] = true
		}

		for _, want := range states {
			if !gotIDs[want.ID] {
				t.Errorf("List missing state %q", want.ID)
			}
		}
	})

	t.Run("list returns copies", func(t *testing.T) {
		got := sm.List()

		// Modify a returned state
		if len(got) > 0 {
			got[0].Image = "modified-image"

			// List again to verify original wasn't modified
			got2 := sm.List()
			if got2[0].Image == "modified-image" {
				t.Error("List returned references, not copies")
			}
		}
	})

	t.Run("list empty manager", func(t *testing.T) {
		tempHome2 := t.TempDir()
		t.Setenv("HOME", tempHome2)

		sm2, err := NewStateManager(t.TempDir())
		if err != nil {
			t.Fatalf("NewStateManager failed: %v", err)
		}

		got := sm2.List()
		if len(got) != 0 {
			t.Errorf("List returned %d states from empty manager, want 0", len(got))
		}
	})
}

// TestUpdateStatus tests updating VM status.
func TestUpdateStatus(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	state := &VMState{
		ID:     "vm-status",
		Image:  "test:latest",
		Status: "running",
	}

	if err := sm.Add(state); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t.Run("update status successfully", func(t *testing.T) {
		newStatus := "stopped"
		if err := sm.UpdateStatus("vm-status", newStatus); err != nil {
			t.Fatalf("UpdateStatus failed: %v", err)
		}

		got, err := sm.Get("vm-status")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if got.Status != newStatus {
			t.Errorf("Status not updated: got %q, want %q", got.Status, newStatus)
		}
	})

	t.Run("update non-existent VM", func(t *testing.T) {
		err := sm.UpdateStatus("does-not-exist", "stopped")
		if err == nil {
			t.Error("Expected error for non-existent VM, got nil")
		}
	})
}

// TestUpdateError tests updating VM error information.
func TestUpdateError(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	state := &VMState{
		ID:     "vm-error",
		Image:  "test:latest",
		Status: "running",
	}

	if err := sm.Add(state); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t.Run("update error successfully", func(t *testing.T) {
		errMsg := "VM crashed with exit code 1"
		beforeUpdate := time.Now()

		if err := sm.UpdateError("vm-error", errMsg); err != nil {
			t.Fatalf("UpdateError failed: %v", err)
		}

		got, err := sm.Get("vm-error")
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if got.LastError != errMsg {
			t.Errorf("LastError not set: got %q, want %q", got.LastError, errMsg)
		}
		if got.Status != "error" {
			t.Errorf("Status not set to 'error': got %q, want 'error'", got.Status)
		}
		if got.ErrorTime.Before(beforeUpdate) {
			t.Error("ErrorTime not set or is in the past")
		}
	})

	t.Run("update error on non-existent VM", func(t *testing.T) {
		err := sm.UpdateError("does-not-exist", "some error")
		if err == nil {
			t.Error("Expected error for non-existent VM, got nil")
		}
	})
}

// TestRemove tests removing VM states.
func TestRemove(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	state := &VMState{
		ID:    "vm-remove",
		Image: "test:latest",
	}

	if err := sm.Add(state); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	t.Run("remove existing VM", func(t *testing.T) {
		if err := sm.Remove("vm-remove"); err != nil {
			t.Fatalf("Remove failed: %v", err)
		}

		// Verify it's gone
		_, err := sm.Get("vm-remove")
		if err == nil {
			t.Error("VM still exists after Remove")
		}

		// Verify it's not in List
		list := sm.List()
		for _, s := range list {
			if s.ID == "vm-remove" {
				t.Error("VM still in List after Remove")
			}
		}
	})

	t.Run("remove non-existent VM", func(t *testing.T) {
		err := sm.Remove("does-not-exist")
		if err == nil {
			t.Error("Expected error for non-existent VM, got nil")
		}
	})
}

// TestPersistence tests that state persists across StateManager instances.
func TestPersistence(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create first state manager and add a state
	sm1, err := NewStateManager("/ignored")
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	state1 := &VMState{
		ID:       "vm-persist",
		Image:    "persist-test:latest",
		Status:   "running",
		VCPUs:    4,
		MemoryMB: 8192,
		NetworkID: "net-1",
		IPAddresses: []string{"10.0.0.10", "10.0.0.11"},
	}

	if err := sm1.Add(state1); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Update status
	if err := sm1.UpdateStatus("vm-persist", "paused"); err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	// Create second state manager (should load from disk)
	sm2, err := NewStateManager("/ignored")
	if err != nil {
		t.Fatalf("Second NewStateManager failed: %v", err)
	}

	// Verify the state was loaded
	got, err := sm2.Get("vm-persist")
	if err != nil {
		t.Fatalf("Get from second manager failed: %v", err)
	}

	if got.ID != state1.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, state1.ID)
	}
	if got.Image != state1.Image {
		t.Errorf("Image mismatch: got %q, want %q", got.Image, state1.Image)
	}
	if got.Status != "paused" {
		t.Errorf("Status not persisted: got %q, want 'paused'", got.Status)
	}
	if got.VCPUs != state1.VCPUs {
		t.Errorf("VCPUs not persisted: got %d, want %d", got.VCPUs, state1.VCPUs)
	}
	if got.MemoryMB != state1.MemoryMB {
		t.Errorf("MemoryMB not persisted: got %d, want %d", got.MemoryMB, state1.MemoryMB)
	}
	if got.NetworkID != state1.NetworkID {
		t.Errorf("NetworkID not persisted: got %q, want %q", got.NetworkID, state1.NetworkID)
	}
	if len(got.IPAddresses) != len(state1.IPAddresses) {
		t.Errorf("IPAddresses not persisted: got %d addresses, want %d", len(got.IPAddresses), len(state1.IPAddresses))
	}
}

// TestLoad tests loading state from disk.
func TestLoad(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	t.Run("load from non-existent file", func(t *testing.T) {
		sm, err := NewStateManager("/ignored")
		if err != nil {
			t.Fatalf("NewStateManager failed: %v", err)
		}

		// Should not error, just start empty
		list := sm.List()
		if len(list) != 0 {
			t.Errorf("Expected empty list, got %d states", len(list))
		}
	})

	t.Run("load empty file", func(t *testing.T) {
		sm, err := NewStateManager("/ignored")
		if err != nil {
			t.Fatalf("NewStateManager failed: %v", err)
		}

		// Manually create an empty state file
		stateFile := sm.stateFile
		if err := os.WriteFile(stateFile, []byte{}, 0644); err != nil {
			t.Fatalf("Failed to create empty state file: %v", err)
		}

		// Load should handle empty file gracefully
		if err := sm.Load(); err != nil {
			t.Errorf("Load failed on empty file: %v", err)
		}

		list := sm.List()
		if len(list) != 0 {
			t.Errorf("Expected empty list after loading empty file, got %d states", len(list))
		}
	})

	t.Run("load from valid JSON", func(t *testing.T) {
		sm, err := NewStateManager("/ignored")
		if err != nil {
			t.Fatalf("NewStateManager failed: %v", err)
		}

		// Create a valid state file
		states := []*VMState{
			{
				ID:         "vm-load-1",
				Image:      "load-test:latest",
				Status:     "running",
				VCPUs:      2,
				MemoryMB:   2048,
				IPAddresses: []string{"10.0.0.1"},
			},
			{
				ID:         "vm-load-2",
				Image:      "load-test2:latest",
				Status:     "stopped",
				VCPUs:      4,
				MemoryMB:   4096,
				IPAddresses: []string{"10.0.0.2", "10.0.0.3"},
			},
		}

		data, err := json.MarshalIndent(states, "", "  ")
		if err != nil {
			t.Fatalf("Failed to marshal states: %v", err)
		}

		stateFile := sm.stateFile
		if err := os.WriteFile(stateFile, data, 0644); err != nil {
			t.Fatalf("Failed to write state file: %v", err)
		}

		// Load the states
		if err := sm.Load(); err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		// Verify all states were loaded
		list := sm.List()
		if len(list) != len(states) {
			t.Errorf("Expected %d states, got %d", len(states), len(list))
		}

		for _, want := range states {
			got, err := sm.Get(want.ID)
			if err != nil {
				t.Errorf("Get(%q) failed: %v", want.ID, err)
				continue
			}
			if got.Image != want.Image {
				t.Errorf("Image mismatch for %s: got %q, want %q", want.ID, got.Image, want.Image)
			}
		}
	})

	t.Run("load from corrupt JSON", func(t *testing.T) {
		sm, err := NewStateManager("/ignored")
		if err != nil {
			t.Fatalf("NewStateManager failed: %v", err)
		}

		// Write invalid JSON
		stateFile := sm.stateFile
		corruptJSON := []byte(`{"invalid": json}`)
		if err := os.WriteFile(stateFile, corruptJSON, 0644); err != nil {
			t.Fatalf("Failed to write corrupt state file: %v", err)
		}

		// Load should return error
		if err := sm.Load(); err == nil {
			t.Error("Expected error loading corrupt JSON, got nil")
		}
	})
}

// TestConcurrentAccess tests that StateManager is safe for concurrent use.
func TestConcurrentAccess(t *testing.T) {
	sm, err := NewStateManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	const numGoroutines = 50
	const numOps = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 4) // 4 types of operations

	// Add goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			state := &VMState{
				ID:    fmt.Sprintf("vm-concurrent-%d", id),
				Image: "test:latest",
			}
			sm.Add(state)
		}(i)
	}

	// Get goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			sm.Get(fmt.Sprintf("vm-concurrent-%d", id%numGoroutines))
		}(i)
	}

	// Update goroutines
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			sm.UpdateStatus(fmt.Sprintf("vm-concurrent-%d", id%numGoroutines), "paused")
		}(i)
	}

	// List goroutines
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			sm.List()
		}()
	}

	wg.Wait()

	// Verify final state is consistent
	list := sm.List()
	t.Logf("Final state count: %d", len(list))
}

// TestGetStateFile tests GetStateFile method.
func TestGetStateFile(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sm, err := NewStateManager("/ignored")
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	expectedPath := filepath.Join(tempHome, ".swarmcracker", "state.json")
	if sm.GetStateFile() != expectedPath {
		t.Errorf("GetStateFile() = %q, want %q", sm.GetStateFile(), expectedPath)
	}
}

// TestGetLogDir tests GetLogDir method.
func TestGetLogDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sm, err := NewStateManager("/ignored")
	if err != nil {
		t.Fatalf("NewStateManager failed: %v", err)
	}

	expectedDir := filepath.Join(tempHome, ".swarmcracker")
	if sm.GetLogDir() != expectedDir {
		t.Errorf("GetLogDir() = %q, want %q", sm.GetLogDir(), expectedDir)
	}
}
