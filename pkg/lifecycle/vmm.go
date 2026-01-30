// Package lifecycle provides VM lifecycle management for Firecracker.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// VMMManager manages Firecracker VM lifecycle.
type VMMManager struct {
	config     *ManagerConfig
	vms        map[string]*VMInstance
	mu         sync.RWMutex
	socketDir  string
}

// ManagerConfig holds VMM manager configuration.
type ManagerConfig struct {
	KernelPath     string
	RootfsDir      string
	SocketDir      string
	DefaultVCPUs   int
	DefaultMemoryMB int
	EnableJailer    bool
}

// VMInstance represents a running Firecracker VM.
type VMInstance struct {
	ID        string
	PID       int
	Config    interface{}
	State     VMState
	CreatedAt time.Time
	SocketPath string
}

// VMState represents the state of a VM.
type VMState string

const (
	VMStateNew      VMState = "new"
	VMStateStarting VMState = "starting"
	VMStateRunning  VMState = "running"
	VMStateStopping VMState = "stopping"
	VMStateStopped  VMState = "stopped"
	VMStateCrashed  VMState = "crashed"
)

// Firecracker API types
type ActionsType struct {
	ActionType string `json:"action_type"`
}

// NewVMMManager creates a new VMMManager.
func NewVMMManager(config interface{}) types.VMMManager {
	var cfg *ManagerConfig
	if c, ok := config.(*ManagerConfig); ok {
		cfg = c
	} else {
		cfg = &ManagerConfig{
			SocketDir: "/var/run/firecracker",
		}
	}

	// Ensure socket directory exists
	os.MkdirAll(cfg.SocketDir, 0755)

	return &VMMManager{
		config:    cfg,
		vms:       make(map[string]*VMInstance),
		socketDir: cfg.SocketDir,
	}
}

// Start starts a VM for the given task.
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	log.Info().
		Str("task_id", task.ID).
		Msg("Starting VM")

	socketPath := filepath.Join(vm.socketDir, task.ID+".sock")

	// Create config JSON string
	configStr, ok := config.(string)
	if !ok {
		// If config is not already a string, try to marshal it
		configBytes, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}
		configStr = string(configBytes)
	}

	// Start Firecracker process
	cmd := exec.Command("firecracker-v1.0.0",
		"--api-sock", socketPath,
		"--config-file", "/dev/stdin",
	)

	cmd.Stdin = strings.NewReader(configStr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait for API server to be ready
	if err := waitForAPIServer(socketPath, 10*time.Second); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("firecracker API server not ready: %w", err)
	}

	// Start the VM instance
	client := &http.Client{Timeout: 5 * time.Second}
	actions := ActionsType{ActionType: "InstanceStart"}

	body, _ := json.Marshal(actions)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		"http://unix"+socketPath+"/actions",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to start instance: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		cmd.Process.Kill()
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Store VM instance
	vmInstance := &VMInstance{
		ID:         task.ID,
		PID:        cmd.Process.Pid,
		Config:     config,
		State:      VMStateRunning,
		CreatedAt:  time.Now(),
		SocketPath: socketPath,
	}

	vm.vms[task.ID] = vmInstance

	log.Info().
		Str("task_id", task.ID).
		Str("vm_id", task.ID).
		Int("pid", cmd.Process.Pid).
		Msg("VM started successfully")

	return nil
}

// Stop stops a running VM.
func (vm *VMMManager) Stop(ctx context.Context, task *types.Task) error {
	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM not found for task %s", task.ID)
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("Stopping VM")

	socketPath := vmInstance.SocketPath

	// Send shutdown signal
	client := &http.Client{Timeout: 5 * time.Second}
	actions := ActionsType{ActionType: "SendCtrlAltDel"}

	body, _ := json.Marshal(actions)
	req, _ := http.NewRequestWithContext(ctx, "PUT",
		"http://unix"+socketPath+"/actions",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// Force kill if graceful shutdown fails
		return vm.forceKillVM(vmInstance)
	}
	defer resp.Body.Close()

	// Wait for VM to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		_, err := waitForShutdown(socketPath, 30*time.Second)
		done <- err
	}()

	select {
	case <-done:
		vmInstance.State = VMStateStopped
	case <-time.After(30 * time.Second):
		// Force kill on timeout
		vm.forceKillVM(vmInstance)
	}

	log.Info().
		Str("task_id", task.ID).
		Msg("VM stopped")

	return nil
}

// Wait waits for a VM to exit.
func (vm *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return &types.TaskStatus{
			State:   types.TaskState_ORPHANED,
			Message: "VM not found",
		}, nil
	}

	// Check if process is still running
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		return &types.TaskStatus{
			State:   types.TaskState_COMPLETE,
			Message: "Process not found",
		}, nil
	}

	// Send signal 0 to check if process is alive
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return &types.TaskStatus{
			State:   types.TaskState_COMPLETE,
			Message: "Process exited",
		}, nil
	}

	return &types.TaskStatus{
		State: types.TaskState_RUNNING,
		RuntimeStatus: map[string]interface{}{
			"vm_id": vmInstance.ID,
			"pid":   vmInstance.PID,
			"state": string(vmInstance.State),
		},
	}, nil
}

// Describe describes the current state of a VM.
func (vm *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return &types.TaskStatus{
			State:   types.TaskState_ORPHANED,
			Message: "VM not found",
		}, nil
	}

	// Check process status
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		return &types.TaskStatus{
			State:   types.TaskState_FAILED,
			Message: fmt.Sprintf("VM process error: %v", err),
		}, nil
	}

	// Check if still running
	if err := process.Signal(syscall.Signal(0)); err != nil {
		vmInstance.State = VMStateStopped
		return &types.TaskStatus{
			State:   types.TaskState_COMPLETE,
			Message: "VM has stopped",
		}, nil
	}

	return &types.TaskStatus{
		State: types.TaskState_RUNNING,
		RuntimeStatus: map[string]interface{}{
			"vm_id":   vmInstance.ID,
			"pid":     vmInstance.PID,
			"state":   string(vmInstance.State),
			"uptime":  time.Since(vmInstance.CreatedAt).String(),
		},
	}, nil
}

// Remove removes a VM and cleans up resources.
func (vm *VMMManager) Remove(ctx context.Context, task *types.Task) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	log.Info().
		Str("task_id", task.ID).
		Msg("Removing VM")

	vmInstance, exists := vm.vms[task.ID]
	if !exists {
		return nil
	}

	// Stop VM if running
	if vmInstance.State == VMStateRunning {
		process, _ := os.FindProcess(vmInstance.PID)
		if process != nil {
			process.Kill()
		}
	}

	// Remove socket file
	if vmInstance.SocketPath != "" {
		os.Remove(vmInstance.SocketPath)
	}

	// Remove from map
	delete(vm.vms, task.ID)

	log.Info().
		Str("task_id", task.ID).
		Msg("VM removed")

	return nil
}

// forceKillVM forcibly kills a VM process.
func (vm *VMMManager) forceKillVM(vmInstance *VMInstance) error {
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		return err
	}

	return process.Kill()
}

// waitForAPIServer waits for the Firecracker API server to be ready.
func waitForAPIServer(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			// Socket exists, try to connect
			client := &http.Client{Timeout: 100 * time.Millisecond}
			resp, err := client.Get("http://unix" + socketPath + "/")
			if err == nil {
				resp.Body.Close()
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("API server not ready within timeout")
}

// waitForShutdown waits for a VM to shutdown.
func waitForShutdown(socketPath string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); os.IsNotExist(err) {
			return true, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return false, fmt.Errorf("shutdown timeout")
}
