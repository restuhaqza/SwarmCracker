// Package swarmkit provides VMM management for SwarmKit integration.
package swarmkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// VMMManager manages Firecracker VM processes.
type VMMManager struct {
	firecrackerPath string
	socketDir       string
	processes       map[string]*exec.Cmd
	processMutex    sync.Mutex
	logger          zerolog.Logger
}

// NewVMMManager creates a new VMM manager.
func NewVMMManager(firecrackerPath, socketDir string) *VMMManager {
	return &VMMManager{
		firecrackerPath: firecrackerPath,
		socketDir:       socketDir,
		processes:       make(map[string]*exec.Cmd),
		logger:          log.With().Str("component", "vmm-manager").Logger(),
	}
}

// Start starts a Firecracker VM for the given task.
func (v *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Msg("Starting Firecracker VM")

	// Create socket directory
	if err := os.MkdirAll(v.socketDir, 0755); err != nil {
		return fmt.Errorf("failed to create socket dir: %w", err)
	}

	socketPath := filepath.Join(v.socketDir, task.ID+".sock")

	// Start Firecracker process
	cmd := exec.CommandContext(ctx, v.firecrackerPath,
		"--api-sock", socketPath,
		"--id", task.ID,
	)

	cmd.Stdout = &logWriter{logger: v.logger}
	cmd.Stderr = &logWriter{logger: v.logger}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Store process reference
	v.processMutex.Lock()
	v.processes[task.ID] = cmd
	v.processMutex.Unlock()

	// Wait for socket to be created
	if err := v.waitForSocket(socketPath, 10*time.Second); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("socket not created: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Str("socket", socketPath).
		Msg("Firecracker VM started")

	return nil
}

// waitForSocket waits for the Firecracker socket to be created.
func (v *VMMManager) waitForSocket(socketPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				return nil
			}
		}
	}
}

// Stop stops the Firecracker VM for the given task.
func (v *VMMManager) Stop(ctx context.Context, task *types.Task) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Msg("Stopping Firecracker VM")

	v.processMutex.Lock()
	defer v.processMutex.Unlock()

	cmd, ok := v.processes[task.ID]
	if !ok {
		return fmt.Errorf("task not found")
	}

	// Send SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		v.logger.Error().Err(err).Msg("Failed to send SIGTERM")
	}

	// Wait for process to exit or kill it after timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			v.logger.Error().Err(err).Msg("Process exited with error")
		}
	case <-time.After(10 * time.Second):
		v.logger.Warn().Msg("Process did not exit gracefully, killing")
		cmd.Process.Kill()
	}

	delete(v.processes, task.ID)
	v.logger.Info().Str("task_id", task.ID).Msg("Firecracker VM stopped")

	return nil
}

// Wait waits for the Firecracker VM to exit.
func (v *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()

	if !ok {
		return &types.TaskStatus{
			State:   types.TaskState_COMPLETE,
			Message: "Task completed",
		}, nil
	}

	// Wait for process
	err := cmd.Wait()

	status := &types.TaskStatus{
		Timestamp: time.Now().Unix(),
	}

	if err != nil {
		status.State = types.TaskState_FAILED
		status.Err = err
		status.Message = err.Error()
	} else {
		status.State = types.TaskState_COMPLETE
		status.Message = "Task completed successfully"
	}

	return status, nil
}

// Remove removes the VM resources.
func (v *VMMManager) Remove(ctx context.Context, task *types.Task) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Msg("Removing VM resources")

	// Remove socket if exists
	socketPath := filepath.Join(v.socketDir, task.ID+".sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		v.logger.Warn().Err(err).Msg("Failed to remove socket")
	}

	v.processMutex.Lock()
	delete(v.processes, task.ID)
	v.processMutex.Unlock()

	return nil
}

// Describe returns the current status of the VM.
func (v *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	v.processMutex.Lock()
	_, ok := v.processes[task.ID]
	v.processMutex.Unlock()

	if !ok {
		return &types.TaskStatus{
			State:   types.TaskState_COMPLETE,
			Message: "Task not running",
		}, nil
	}

	return &types.TaskStatus{
		State:   types.TaskState_RUNNING,
		Message: "Task is running",
	}, nil
}

// logWriter writes log lines to zerolog.
type logWriter struct {
	logger zerolog.Logger
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.logger.Debug().Msg(string(p))
	return len(p), nil
}
