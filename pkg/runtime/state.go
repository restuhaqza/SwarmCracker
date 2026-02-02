// Package runtime provides VM state tracking and persistence.
package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// VMState represents the state of a running VM.
type VMState struct {
	ID         string    `json:"id"`
	PID        int       `json:"pid"`
	SocketPath string    `json:"socket_path"`
	Image      string    `json:"image"`
	Status     string    `json:"status"`
	StartTime  time.Time `json:"start_time"`
	Command    []string  `json:"command,omitempty"`

	// Additional metadata
	VCPUs      int    `json:"vcpus,omitempty"`
	MemoryMB   int    `json:"memory_mb,omitempty"`
	KernelPath string `json:"kernel_path,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	LogPath    string `json:"log_path,omitempty"`

	// Network info
	NetworkID   string   `json:"network_id,omitempty"`
	IPAddresses []string `json:"ip_addresses,omitempty"`

	// Error tracking
	LastError string    `json:"last_error,omitempty"`
	ErrorTime time.Time `json:"error_time,omitempty"`
}

// StateManager manages VM state persistence.
type StateManager struct {
	mu        sync.RWMutex
	states    map[string]*VMState
	stateFile string
}

// NewStateManager creates a new state manager.
func NewStateManager(stateDir string) (*StateManager, error) {
	// Determine state file location
	var stateFile string
	if os.Geteuid() == 0 {
		// Running as root
		stateFile = filepath.Join("/var/run/swarmcracker", "state.json")
	} else {
		// Running as non-root
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine home directory: %w", err)
		}
		stateFile = filepath.Join(homeDir, ".swarmcracker", "state.json")
	}

	// Ensure directory exists
	stateDirPath := filepath.Dir(stateFile)
	if err := os.MkdirAll(stateDirPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	sm := &StateManager{
		states:    make(map[string]*VMState),
		stateFile: stateFile,
	}

	// Load existing state
	if err := sm.Load(); err != nil {
		// If file doesn't exist, that's ok - start fresh
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load state: %w", err)
		}
	}

	return sm, nil
}

// Add adds a new VM state.
func (sm *StateManager) Add(state *VMState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if state.ID == "" {
		return fmt.Errorf("VM ID cannot be empty")
	}

	// Set start time if not set
	if state.StartTime.IsZero() {
		state.StartTime = time.Now()
	}

	// Set default status
	if state.Status == "" {
		state.Status = "running"
	}

	sm.states[state.ID] = state

	// Persist to disk
	if err := sm.save(); err != nil {
		delete(sm.states, state.ID)
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// Get retrieves a VM state by ID.
func (sm *StateManager) Get(id string) (*VMState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	state, exists := sm.states[id]
	if !exists {
		return nil, fmt.Errorf("VM not found: %s", id)
	}

	// Return a copy to prevent concurrent modification
	stateCopy := *state
	return &stateCopy, nil
}

// List returns all VM states.
func (sm *StateManager) List() []*VMState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*VMState, 0, len(sm.states))
	for _, state := range sm.states {
		stateCopy := *state
		result = append(result, &stateCopy)
	}

	return result
}

// UpdateStatus updates the status of a VM.
func (sm *StateManager) UpdateStatus(id, status string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, exists := sm.states[id]
	if !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	state.Status = status

	// Persist to disk
	if err := sm.save(); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// UpdateError updates the error information for a VM.
func (sm *StateManager) UpdateError(id, errMsg string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, exists := sm.states[id]
	if !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	state.LastError = errMsg
	state.ErrorTime = time.Now()
	state.Status = "error"

	// Persist to disk
	if err := sm.save(); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// Remove removes a VM state.
func (sm *StateManager) Remove(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.states[id]; !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	delete(sm.states, id)

	// Persist to disk
	if err := sm.save(); err != nil {
		// Restore the state on error
		// (though this won't help if we already deleted it)
		return fmt.Errorf("failed to save state: %w", err)
	}

	return nil
}

// Load loads state from disk.
func (sm *StateManager) Load() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.stateFile)
	if err != nil {
		return err
	}

	if len(data) == 0 {
		// Empty file, nothing to load
		return nil
	}

	var states []*VMState
	if err := json.Unmarshal(data, &states); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	// Rebuild the map
	sm.states = make(map[string]*VMState, len(states))
	for _, state := range states {
		sm.states[state.ID] = state
	}

	return nil
}

// save saves state to disk (must be called with lock held).
func (sm *StateManager) save() error {
	// Convert map to slice
	states := make([]*VMState, 0, len(sm.states))
	for _, state := range sm.states {
		states = append(states, state)
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to temporary file first
	tmpFile := sm.stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, sm.stateFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// GetStateFile returns the state file path.
func (sm *StateManager) GetStateFile() string {
	return sm.stateFile
}

// GetLogDir returns the log directory for VMs.
func (sm *StateManager) GetLogDir() string {
	// Use same directory as state file
	return filepath.Dir(sm.stateFile)
}
