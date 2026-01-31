// Package swarmkit provides VMM management for SwarmKit integration.
package swarmkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
func NewVMMManager(firecrackerPath, socketDir string) (*VMMManager, error) {
	// Resolve firecracker binary path
	path := firecrackerPath
	if path == "" {
		var err error
		path, err = exec.LookPath("firecracker")
		if err != nil {
			return nil, fmt.Errorf("firecracker binary not found: %w", err)
		}
	}

	return &VMMManager{
		firecrackerPath: path,
		socketDir:       socketDir,
		processes:       make(map[string]*exec.Cmd),
		logger:          log.With().Str("component", "vmm-manager").Logger(),
	}, nil
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

	// Ensure socket is cleaned up on error
	var socketCleanupNeeded bool
	defer func() {
		if socketCleanupNeeded {
			os.Remove(socketPath)
		}
	}()

	// Start Firecracker process
	cmd := exec.CommandContext(ctx, v.firecrackerPath,
		"--api-sock", socketPath,
		"--id", task.ID,
	)

	cmd.Stdout = &logWriter{logger: v.logger}
	cmd.Stderr = &logWriter{logger: v.logger}

	if err := cmd.Start(); err != nil {
		socketCleanupNeeded = true
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Store process reference
	v.processMutex.Lock()
	v.processes[task.ID] = cmd
	v.processMutex.Unlock()

	// Wait for socket to be created
	if err := v.waitForSocket(socketPath, 10*time.Second); err != nil {
		cmd.Process.Kill()
		socketCleanupNeeded = true
		return fmt.Errorf("socket not created: %w", err)
	}

	// Configure VM via Firecracker HTTP API
	if err := v.configureVM(ctx, task, socketPath); err != nil {
		cmd.Process.Kill()
		socketCleanupNeeded = true
		return fmt.Errorf("failed to configure VM: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Str("socket", socketPath).
		Msg("Firecracker VM started")

	return nil
}

// configureVM configures the VM via Firecracker HTTP API
func (v *VMMManager) configureVM(ctx context.Context, task *types.Task, socketPath string) error {
	// 1. Set machine configuration
	machineConfig := MachineConfig{
		VcpuCount:  1,
		MemSizeMib: 512,
		HtEnabled:  false,
	}
	if err := v.putAPI(socketPath, "/machine-config", machineConfig); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// 2. Set boot source
	bootSource := BootSource{
		KernelImagePath: "/vmlinux.bin",
		BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
	}
	if err := v.putAPI(socketPath, "/boot-source", bootSource); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	// 3. Set root drive
	drive := Drive{
		DriveID:      "rootfs",
		IsRootDevice: true,
		IsReadOnly:   false,
		PathOnHost:   filepath.Join(v.socketDir, task.ID+".img"),
	}
	if err := v.putAPI(socketPath, "/drives/rootfs", drive); err != nil {
		return fmt.Errorf("failed to set drive: %w", err)
	}

	// 4. Start the VM
	action := Action{
		ActionType: "InstanceStart",
	}
	if err := v.putAPI(socketPath, "/actions", action); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Msg("VM configured and started via API")

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

// Firecracker API configuration structures

type MachineConfig struct {
	VcpuCount  int    `json:"vcpu_count"`
	MemSizeMib int    `json:"mem_size_mib"`
	HtEnabled  bool   `json:"ht_enabled"`
	TrackDirtyPages bool `json:"track_dirty_pages,omitempty"`
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
	PathOnHost   string `json:"path_on_host"`
}

type NetworkInterface struct {
	IfaceID    string `json:"iface_id"`
	GuestMac   string `json:"guest_mac,omitempty"`
	HostDevName string `json:"host_dev_name,omitempty"`
}

type Action struct {
	ActionType     string `json:"action_type"`
	TimeoutSeconds int    `json:"timeout_ms,omitempty"`
}

// createFirecrackerHTTPClient creates an HTTP client that communicates via Unix socket
func createFirecrackerHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
		Timeout: 10 * time.Second,
	}
}

// putAPI sends a PUT request to the Firecracker API
func (v *VMMManager) putAPI(socketPath, path string, data interface{}) error {
	client := createFirecrackerHTTPClient(socketPath)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	url := fmt.Sprintf("http://localhost%s", path)
	req, err := http.NewRequestWithContext(context.Background(), "PUT", url, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
