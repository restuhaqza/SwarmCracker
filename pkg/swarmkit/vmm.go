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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// VMMManager manages Firecracker VM processes.
type VMMManager struct {
	firecrackerPath string
	jailerPath      string
	socketDir       string
	useJailer       bool
	jailerConfig    *jailer.Config
	jailer          *jailer.Jailer
	cgroupMgr       *jailer.CgroupManager
	processes       map[string]*exec.Cmd
	processMutex    sync.Mutex
	logger          zerolog.Logger
}

// VMMManagerConfig holds VMM manager configuration.
type VMMManagerConfig struct {
	FirecrackerPath string
	JailerPath      string
	SocketDir       string
	UseJailer       bool
	JailerUID       int
	JailerGID       int
	JailerChrootDir string
	ParentCgroup    string
	CgroupVersion   string
	EnableCgroups   bool
	ResourceLimits  jailer.ResourceLimits
}

// toInt converts an interface{} value to int, handling both int and float64.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}

// NewVMMManager creates a new VMM manager.
func NewVMMManager(firecrackerPath, socketDir string) (*VMMManager, error) {
	return NewVMMManagerWithConfig(&VMMManagerConfig{
		FirecrackerPath: firecrackerPath,
		SocketDir:       socketDir,
		UseJailer:       false,
	})
}

// NewVMMManagerWithConfig creates a VMM manager with advanced configuration.
func NewVMMManagerWithConfig(cfg *VMMManagerConfig) (*VMMManager, error) {
	// Resolve firecracker binary path
	firecrackerPath := cfg.FirecrackerPath
	if firecrackerPath == "" {
		var err error
		firecrackerPath, err = exec.LookPath("firecracker")
		if err != nil {
			return nil, fmt.Errorf("firecracker binary not found: %w", err)
		}
	}

	v := &VMMManager{
		firecrackerPath: firecrackerPath,
		socketDir:       cfg.SocketDir,
		useJailer:       cfg.UseJailer,
		processes:       make(map[string]*exec.Cmd),
		logger:          log.With().Str("component", "vmm-manager").Logger(),
	}

	// Initialize jailer if enabled
	if cfg.UseJailer {
		v.logger.Info().Msg("Jailer mode enabled")

		// Resolve jailer path
		jailerPath := cfg.JailerPath
		if jailerPath == "" {
			var err error
			jailerPath, err = exec.LookPath("jailer")
			if err != nil {
				return nil, fmt.Errorf("jailer binary not found: %w", err)
			}
		}
		v.jailerPath = jailerPath

		// Set defaults for jailer config
		jailerUID := cfg.JailerUID
		if jailerUID == 0 {
			jailerUID = 1000
		}
		jailerGID := cfg.JailerGID
		if jailerGID == 0 {
			jailerGID = 1000
		}
		jailerChrootDir := cfg.JailerChrootDir
		if jailerChrootDir == "" {
			jailerChrootDir = "/var/lib/swarmcracker/jailer"
		}
		cgroupVersion := cfg.CgroupVersion
		if cgroupVersion == "" {
			cgroupVersion = jailer.DetectCgroupVersion()
		}

		v.jailerConfig = &jailer.Config{
			FirecrackerPath: firecrackerPath,
			JailerPath:      jailerPath,
			ChrootBaseDir:   jailerChrootDir,
			UID:             jailerUID,
			GID:             jailerGID,
			ParentCgroup:    cfg.ParentCgroup,
			CgroupVersion:   cgroupVersion,
			EnableSeccomp:   true,
		}

		// Create jailer instance
		var err error
		v.jailer, err = jailer.New(v.jailerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create jailer: %w", err)
		}

		// Create cgroup manager if enabled
		if cfg.EnableCgroups {
			v.cgroupMgr, err = jailer.NewCgroupManager("")
			if err != nil {
				v.logger.Warn().Err(err).Msg("Failed to create cgroup manager, continuing without cgroups")
			}
		}

		v.logger.Info().
			Str("jailer_path", jailerPath).
			Str("chroot_dir", jailerChrootDir).
			Str("cgroup_version", cgroupVersion).
			Msg("Jailer initialized")
	}

	return v, nil
}

// Start starts a Firecracker VM for the given task.
func (v *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Bool("jailer", v.useJailer).
		Msg("Starting Firecracker VM")

	if v.useJailer {
		return v.startWithJailer(ctx, task, config)
	}
	return v.startDirect(ctx, task, config)
}

// startDirect starts Firecracker without jailer (legacy mode).
func (v *VMMManager) startDirect(ctx context.Context, task *types.Task, config interface{}) error {
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
	bgCtx := context.Background()
	cmd := exec.CommandContext(bgCtx, v.firecrackerPath,
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
	if err := v.configureVM(ctx, task, socketPath, config); err != nil {
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

// startWithJailer starts Firecracker inside jailer with isolation.
func (v *VMMManager) startWithJailer(ctx context.Context, task *types.Task, config interface{}) error {
	// Parse config to extract VM settings
	cfg, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config type: %T", config)
	}

	// Extract VM configuration
	machineConfig, ok := cfg["machine-config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing machine-config in config")
	}

	bootSource, ok := cfg["boot-source"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing boot-source in config")
	}

	// Drives can be []interface{} (from JSON) or []map[string]interface{} (from translator)
	var drivesRaw []interface{}
	switch d := cfg["drives"].(type) {
	case []interface{}:
		drivesRaw = d
	case []map[string]interface{}:
		for _, m := range d {
			drivesRaw = append(drivesRaw, m)
		}
	}
	if len(drivesRaw) == 0 {
		return fmt.Errorf("missing drives in config")
	}

	// Find rootfs path from drives
	var rootfsPath string
	for _, driveRaw := range drivesRaw {
		drive, ok := driveRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if isRootDevice, ok := drive["is_root_device"].(bool); ok && isRootDevice {
			rootfsPath, _ = drive["path_on_host"].(string)
			break
		}
	}
	if rootfsPath == "" {
		return fmt.Errorf("rootfs path not found in drives")
	}

	// Build jailer VM config
	jailerCfg := jailer.VMConfig{
		TaskID:     task.ID,
		VcpuCount:  toInt(machineConfig["vcpu_count"]),
		MemoryMB:   toInt(machineConfig["mem_size_mib"]),
		KernelPath: bootSource["kernel_image_path"].(string),
		RootfsPath: rootfsPath,
		BootArgs:   bootSource["boot_args"].(string),
		HtEnabled:  false, // TODO: Extract from machine config
	}

	// Start jailed VM
	process, err := v.jailer.Start(ctx, jailerCfg)
	if err != nil {
		return fmt.Errorf("failed to start jailed VM: %w", err)
	}

	// Apply cgroup limits if enabled
	if v.cgroupMgr != nil && v.jailerConfig != nil {
		// Create cgroup with resource limits
		limits := jailer.ResourceLimits{
			CPUQuotaUs: int64(jailerCfg.VcpuCount * 1000000), // 1 CPU per vcpu
			MemoryMax:  int64(jailerCfg.MemoryMB) * 1024 * 1024,
			MemoryHigh: int64(jailerCfg.MemoryMB) * 1024 * 1024 * 90 / 100, // 90% of max
			IOWeight:   100,
		}

		if err := v.cgroupMgr.CreateCgroup(task.ID, limits); err != nil {
			v.logger.Warn().Err(err).Msg("Failed to create cgroup")
		} else {
			// Add jailer process to cgroup
			if err := v.cgroupMgr.AddProcess(task.ID, process.Pid); err != nil {
				v.logger.Warn().Err(err).Msg("Failed to add process to cgroup")
			}
		}
	}

	// Store process reference (use jailer PID as proxy)
	v.processMutex.Lock()
	// Create a wrapper exec.Cmd for compatibility
	wrapperCmd := &exec.Cmd{
		Process: &os.Process{Pid: process.Pid},
	}
	v.processes[task.ID] = wrapperCmd
	v.processMutex.Unlock()

	// Configure VM via API with chroot-relative paths
	jailerConfig := map[string]interface{}{
		"machine-config": machineConfig,
		"boot-source": map[string]interface{}{
			"kernel_image_path": "/kernel/vmlinux",
			"boot_args":         bootSource["boot_args"],
		},
		"drives": []interface{}{
			map[string]interface{}{
				"drive_id":       "rootfs",
				"path_on_host":   "/drives/rootfs.ext4",
				"is_root_device": true,
				"is_read_only":   false,
			},
		},
	}

	if err := v.configureVM(ctx, task, process.SocketPath, jailerConfig); err != nil {
		v.logger.Error().Err(err).Msg("Failed to configure jailed VM")
		v.jailer.Stop(ctx, task.ID)
		return fmt.Errorf("failed to configure VM: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Str("socket", process.SocketPath).
		Int("pid", process.Pid).
		Msg("Jailed Firecracker VM started and configured")

	return nil
}

// configureVM configures the VM via Firecracker HTTP API
func (v *VMMManager) configureVM(ctx context.Context, task *types.Task, socketPath string, config interface{}) error {
	// Parse config from translator
	cfg, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config type: %T", config)
	}

	// 1. Set machine configuration
	machineConfig, ok := cfg["machine-config"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing machine-config in config")
	}
	if err := v.putAPI(socketPath, "/machine-config", machineConfig); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// 2. Set boot source
	bootSource, ok := cfg["boot-source"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing boot-source in config")
	}
	if err := v.putAPI(socketPath, "/boot-source", bootSource); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	// 3. Set root drive
	// Drives can be []interface{} (from JSON) or []map[string]interface{} (from translator)
	var drivesRaw []interface{}
	switch d := cfg["drives"].(type) {
	case []interface{}:
		drivesRaw = d
	case []map[string]interface{}:
		for _, m := range d {
			drivesRaw = append(drivesRaw, m)
		}
	}
	if len(drivesRaw) == 0 {
		return fmt.Errorf("missing drives in config")
	}
	for _, driveRaw := range drivesRaw {
		drive, ok := driveRaw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid drive type in config")
		}
		driveID, ok := drive["drive_id"].(string)
		if !ok {
			return fmt.Errorf("missing drive_id in drive config")
		}
		if err := v.putAPI(socketPath, "/drives/"+driveID, drive); err != nil {
			return fmt.Errorf("failed to set drive %s: %w", driveID, err)
		}
		v.logger.Info().Str("drive_id", driveID).Msg("Drive configured")
	}

	// 4. Set network interfaces (if any)
	networkInterfacesRaw, ok := cfg["network-interfaces"].([]interface{})
	if ok && len(networkInterfacesRaw) > 0 {
		v.logger.Info().
			Int("count", len(networkInterfacesRaw)).
			Msg("Configuring network interfaces")
		for _, ifaceRaw := range networkInterfacesRaw {
			iface, ok := ifaceRaw.(map[string]interface{})
			if !ok {
				continue
			}
			ifaceID, _ := iface["iface_id"].(string)
			hostDev, _ := iface["host_dev_name"].(string)
			v.logger.Debug().
				Str("iface_id", ifaceID).
				Str("host_dev", hostDev).
				Msg("Adding network interface")
			if err := v.putAPI(socketPath, "/network-interfaces/"+ifaceID, iface); err != nil {
				return fmt.Errorf("failed to set network interface %s: %w", ifaceID, err)
			}
		}
	} else {
		v.logger.Warn().Msg("No network interfaces in config")
	}

	// 5. Start the VM
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

// Stop stops the Firecracker VM for the given task with graceful shutdown.
func (v *VMMManager) Stop(ctx context.Context, task *types.Task) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Bool("jailer", v.useJailer).
		Msg("Stopping Firecracker VM gracefully")

	if v.useJailer && v.jailer != nil {
		// Use jailer's stop method
		if err := v.jailer.Stop(ctx, task.ID); err != nil {
			v.logger.Error().Err(err).Msg("Jailer stop failed")
		}
		// Clean up cgroup if enabled
		if v.cgroupMgr != nil {
			if err := v.cgroupMgr.RemoveCgroup(task.ID); err != nil {
				v.logger.Warn().Err(err).Msg("Failed to remove cgroup")
			}
		}
	}

	v.processMutex.Lock()
	defer v.processMutex.Unlock()

	cmd, ok := v.processes[task.ID]
	if !ok {
		return fmt.Errorf("task not found")
	}

	// Send SIGTERM for graceful shutdown
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

// ForceStop forcefully terminates the Firecracker VM without grace period.
func (v *VMMManager) ForceStop(ctx context.Context, task *types.Task) error {
	v.logger.Warn().
		Str("task_id", task.ID).
		Msg("Force stopping Firecracker VM")

	v.processMutex.Lock()
	defer v.processMutex.Unlock()

	cmd, ok := v.processes[task.ID]
	if !ok {
		return fmt.Errorf("task not found")
	}

	// Force kill immediately without waiting
	if err := cmd.Process.Kill(); err != nil {
		v.logger.Error().Err(err).Msg("Failed to kill process")
		return fmt.Errorf("failed to kill process: %w", err)
	}

	// Wait for process to actually exit (should be immediate)
	_ = cmd.Wait()

	delete(v.processes, task.ID)
	v.logger.Info().Str("task_id", task.ID).Msg("Firecracker VM force stopped")

	return nil
}

// Wait waits for the Firecracker VM to exit.
func (v *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()

	if !ok {
		return &types.TaskStatus{
			State:   types.TaskStateComplete,
			Message: "Task completed",
		}, nil
	}

	// Wait for process
	err := cmd.Wait()

	status := &types.TaskStatus{
		Timestamp: time.Now().Unix(),
	}

	if err != nil {
		status.State = types.TaskStateFailed
		status.Err = err
		status.Message = err.Error()
	} else {
		status.State = types.TaskStateComplete
		status.Message = "Task completed successfully"
	}

	return status, nil
}

// GetPID returns the PID of the Firecracker process for the given task.
// Returns 0 if the task is not found or the process is not running.
func (v *VMMManager) GetPID(taskID string) int {
	v.processMutex.Lock()
	defer v.processMutex.Unlock()

	cmd, ok := v.processes[taskID]
	if !ok || cmd.Process == nil {
		return 0
	}

	return cmd.Process.Pid
}

// CheckVMAPIHealth checks if the VM's API socket is responsive.
// This is a lightweight liveness check that verifies the Firecracker
// API server is responding to requests.
func (v *VMMManager) CheckVMAPIHealth(taskID string) bool {
	socketPath := filepath.Join(v.socketDir, taskID+".sock")

	// Check if socket file exists
	if _, err := os.Stat(socketPath); err != nil {
		v.logger.Debug().Str("task_id", taskID).Err(err).Msg("API socket not found")
		return false
	}

	// Try to query the machine configuration via API
	client := createFirecrackerHTTPClient(socketPath)
	url := fmt.Sprintf("http://localhost/machine-config")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		v.logger.Debug().Str("task_id", taskID).Err(err).Msg("Failed to create API request")
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		v.logger.Debug().Str("task_id", taskID).Err(err).Msg("API request failed")
		return false
	}
	defer resp.Body.Close()

	// Consider 200 OK as healthy
	if resp.StatusCode == http.StatusOK {
		v.logger.Debug().Str("task_id", taskID).Msg("VM API is healthy")
		return true
	}

	v.logger.Debug().Str("task_id", taskID).Int("status", resp.StatusCode).Msg("VM API returned unexpected status")
	return false
}

// IsRunning checks if the Firecracker process for the given task is still running.
func (v *VMMManager) IsRunning(taskID string) bool {
	v.processMutex.Lock()
	cmd, ok := v.processes[taskID]
	v.processMutex.Unlock()

	if !ok || cmd.Process == nil {
		return false
	}

	// Check if process is still alive by sending signal 0
	err := cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Remove removes the VM resources.
func (v *VMMManager) Remove(ctx context.Context, task *types.Task) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Bool("jailer", v.useJailer).
		Msg("Removing VM resources")

	// Clean up jailer resources if enabled
	if v.useJailer && v.jailer != nil {
		// Force stop via jailer
		if err := v.jailer.ForceStop(ctx, task.ID); err != nil {
			v.logger.Warn().Err(err).Msg("Jailer force stop failed")
		}
		// Clean up cgroup
		if v.cgroupMgr != nil {
			if err := v.cgroupMgr.RemoveCgroup(task.ID); err != nil {
				v.logger.Warn().Err(err).Msg("Failed to remove cgroup")
			}
		}
	}

	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()

	// Stop VM if still running (force kill to ensure cleanup)
	if ok {
		v.logger.Debug().Str("task_id", task.ID).Msg("VM still running, stopping before removal")
		if cmd.Process != nil {
			if err := cmd.Process.Kill(); err != nil && !strings.Contains(err.Error(), "process already finished") {
				v.logger.Warn().Err(err).Msg("Failed to kill process during removal")
			}
			// Wait a bit for process to exit
			cmd.Wait()
		}
	}

	// Remove socket if exists
	socketPath := filepath.Join(v.socketDir, task.ID+".sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		v.logger.Warn().Err(err).Msg("Failed to remove socket")
	} else if err == nil {
		v.logger.Debug().Str("socket", socketPath).Msg("Removed socket file")
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
			State:   types.TaskStateComplete,
			Message: "Task not running",
		}, nil
	}

	return &types.TaskStatus{
		State:   types.TaskStateRunning,
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
	VcpuCount       int  `json:"vcpu_count"`
	MemSizeMib      int  `json:"mem_size_mib"`
	HtEnabled       bool `json:"ht_enabled"`
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
	IfaceID     string `json:"iface_id"`
	GuestMac    string `json:"guest_mac,omitempty"`
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
