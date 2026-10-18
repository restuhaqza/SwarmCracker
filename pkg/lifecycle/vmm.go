// Package lifecycle provides VM lifecycle management for Firecracker.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/rs/zerolog/log"
)

// VMMManager manages Firecracker VM lifecycle.
type VMMManager struct {
	config        *ManagerConfig
	vms           map[string]*VMInstance
	mu            sync.RWMutex
	socketDir     string
	consoleWriter io.Writer
	// cgroupMgr is created lazily, only for a task that requests a cpuset. It is
	// an interface so tests can inject a fake instead of writing to /sys.
	// cgroupMu guards it: Start creates it, while Stop/Remove read it without
	// holding the manager lock.
	cgroupMu  sync.Mutex
	cgroupMgr jailer.CgroupOps
	// cgroupErr remembers why the real manager could not be created, so a cpuset
	// request can be rejected with the reason.
	cgroupErr error
	// hugepagesPoolPath is the /proc/meminfo-style file read for a hugepages
	// pre-flight check. Empty means the production default; tests point it at a
	// fixture.
	hugepagesPoolPath string
}

// SetConsoleWriter redirects the serial console of VMs started from now on to
// w. When unset, the console is wired to the current process's stdio, which is
// what an attached/foreground caller wants. A detached caller should point this
// at a log file: otherwise the long-lived Firecracker child inherits the
// caller's stdout/stderr and keeps the pipe open, so a pipeline such as
// `swarmcracker vm create -d | tail` never sees EOF.
func (vm *VMMManager) SetConsoleWriter(w io.Writer) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	vm.consoleWriter = w
}

// consoleStdio returns the writers Firecracker's stdout/stderr should use:
// the redirected console writer when set, otherwise this process's stdio.
func (vm *VMMManager) consoleStdio() (io.Writer, io.Writer) {
	if vm.consoleWriter != nil {
		return vm.consoleWriter, vm.consoleWriter
	}
	return os.Stdout, os.Stderr
}

// ManagerConfig holds VMM manager configuration.
type ManagerConfig struct {
	KernelPath      string
	RootfsDir       string
	SocketDir       string
	DefaultVCPUs    int
	DefaultMemoryMB int
	EnableJailer    bool
}

// VMInstance represents a running Firecracker VM.
type VMInstance struct {
	ID             string
	PID            int
	Config         interface{}
	state          VMState // private; use SetState/GetState
	CreatedAt      time.Time
	SocketPath     string
	InitSystem     string
	GracePeriodSec int

	mu sync.RWMutex // protects state
}

// SetState sets the VM state in a thread-safe manner.
func (v *VMInstance) SetState(newState VMState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = newState
}

// GetState returns the VM state in a thread-safe manner.
func (v *VMInstance) GetState() VMState {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state
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

// BootSource represents the boot source configuration.
type BootSource struct {
	KernelImagePath string  `json:"kernel_image_path"`
	BootArgs        string  `json:"boot_args,omitempty"`
	Drives          []Drive `json:"drives,omitempty"`
}

// Drive represents a drive attached to the VM.
type Drive struct {
	DriveID      string `json:"drive_id"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
	PathOnHost   string `json:"path_on_host"`
}

// MachineConfig represents the machine configuration.
type MachineConfig struct {
	VCPUs      int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	HtEnabled  bool `json:"ht_enabled"`
	// HugePages is Firecracker's huge_pages value ("2M"); omitempty keeps an
	// unconfigured VM's request unchanged.
	HugePages string `json:"huge_pages,omitempty"`
}

// balloonDevice is the Firecracker PUT /balloon request body.
type balloonDevice struct {
	AmountMib             int64 `json:"amount_mib"`
	DeflateOnOOM          bool  `json:"deflate_on_oom"`
	StatsPollingIntervalS int   `json:"stats_polling_interval_s"`
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
	// Best-effort: a non-root process may not be able to create the default
	// path; the manager will surface a clearer error when it actually needs it.
	_ = os.MkdirAll(cfg.SocketDir, 0755)

	return &VMMManager{
		config:    cfg,
		vms:       make(map[string]*VMInstance),
		socketDir: cfg.SocketDir,
	}
}

// Start starts a VM for the given task.
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	log.Info().
		Str("task_id", task.ID).
		Msg("Starting VM")

	// Check if VM already exists
	if _, exists := vm.vms[task.ID]; exists {
		return fmt.Errorf("VM already exists for task %s", task.ID)
	}

	socketPath := filepath.Join(vm.socketDir, task.ID+".sock")

	// Clean up socket if it exists from previous run
	os.Remove(socketPath)

	// Validate config
	if config == nil {
		return fmt.Errorf("invalid config: nil configuration")
	}

	// A hugepage request is a hard promise: an undersized host pool makes the
	// VM fault with SIGBUS after it boots. Check it before the cgroup or the
	// process exists, so a refusal leaves nothing behind.
	if err := vm.checkHugepages(task, config); err != nil {
		return err
	}

	// A `vm create` VM is cgrouped only when its task asks to be pinned: with
	// neither a cpuset nor a NUMA node list this makes no cgroup calls and
	// behaves exactly as before. A refused placement fails here, before any
	// process exists.
	cgrouped, err := vm.prepareCgroup(task, config)
	if err != nil {
		return err
	}

	var startErr error
	// Remove the cgroup on every path that does not reach a registered VM. This
	// cannot key on startErr: the early LookPath/cmd.Start failures return before
	// it is set, and a cgroup of a VM that never ran must not leak.
	cgroupKept := false
	if cgrouped {
		defer func() {
			if !cgroupKept {
				vm.removeCgroup(task.ID)
			}
		}()
	}

	// Find Firecracker binary
	fcBinary, err := exec.LookPath("firecracker")
	if err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}

	log.Debug().Str("binary", fcBinary).Msg("Using Firecracker binary")

	// Start Firecracker process (without config file). Pass --id so the
	// process is discoverable via /proc/<pid>/cmdline (runtime.FindFirecrackerPID),
	// which `vm stop`/`vm status` rely on.
	cmd := exec.Command(fcBinary, "--api-sock", socketPath, "--id", task.ID)
	cmd.Stdout, cmd.Stderr = vm.consoleStdio()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Attach the VMM to its cgroup now that it has a pid; a cpuset that could not
	// be attached never took effect, so that fails the VM.
	if cgrouped {
		if err := vm.addCgroupProcess(task, cmd.Process.Pid); err != nil {
			startErr = err
			return startErr
		}
	}

	// Ensure cleanup on failure
	defer func() {
		if startErr != nil {
			if killErr := cmd.Process.Kill(); killErr != nil {
				log.Warn().Err(killErr).Msg("Failed to kill firecracker process on cleanup")
			}
			if err := os.Remove(socketPath); err != nil {
				log.Warn().Err(err).Msg("Failed to remove socket on cleanup")
			}
		}
	}()

	// Wait for API server to be ready
	if err := waitForAPIServer(socketPath, 10*time.Second); err != nil {
		startErr = fmt.Errorf("firecracker API server not ready: %w", err)
		return startErr
	}

	// Configure VM via API
	if err := vm.configureVM(ctx, socketPath, config); err != nil {
		startErr = fmt.Errorf("failed to configure VM: %w", err)
		return startErr
	}

	// Start the VM instance
	client := newUnixClient(socketPath, 5*time.Second)
	actions := ActionsType{ActionType: "InstanceStart"}

	body, err := json.Marshal(actions)
	if err != nil {
		startErr = fmt.Errorf("failed to marshal instance start action: %w", err)
		return startErr
	}
	req, err := http.NewRequestWithContext(ctx, "PUT",
		"http://localhost/actions",
		bytes.NewReader(body),
	)
	if err != nil {
		startErr = fmt.Errorf("failed to create start request: %w", err)
		return startErr
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		startErr = fmt.Errorf("failed to start instance: %w", err)
		return startErr
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		startErr = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		return startErr
	}

	// Clear the error since we succeeded
	startErr = nil

	// Extract init system info from task annotations if available
	initSystem := "none"
	gracePeriod := 10
	if initSys, ok := task.Annotations["init_system"]; ok {
		initSystem = initSys
	}

	vmInstance := &VMInstance{
		ID:             task.ID,
		PID:            cmd.Process.Pid,
		Config:         config,
		CreatedAt:      time.Now(),
		SocketPath:     socketPath,
		InitSystem:     initSystem,
		GracePeriodSec: gracePeriod,
	}
	// Use SetState to properly acquire the mutex
	vmInstance.SetState(VMStateRunning)

	vm.vms[task.ID] = vmInstance
	// The VM is running with its cgroup: from here on teardown owns the removal.
	cgroupKept = true

	log.Info().
		Str("task_id", task.ID).
		Str("vm_id", task.ID).
		Int("pid", cmd.Process.Pid).
		Str("init_system", initSystem).
		Msg("VM started successfully")

	return nil
}

// configureVM configures the VM via Firecracker API.
func (vm *VMMManager) configureVM(ctx context.Context, socketPath string, config interface{}) error {
	client := newUnixClient(socketPath, 5*time.Second)

	// Parse config - could be a map or struct
	var configMap map[string]interface{}

	switch c := config.(type) {
	case map[string]interface{}:
		configMap = c
	case string:
		if err := json.Unmarshal([]byte(c), &configMap); err != nil {
			return fmt.Errorf("failed to unmarshal config json: %w", err)
		}
	default:
		return fmt.Errorf("invalid config type: expected map[string]interface{} or json string")
	}

	// Configure boot source if provided
	if bootSource, ok := configMap["boot-source"].(map[string]interface{}); ok {
		bootSourceJSON, err := json.Marshal(bootSource)
		if err != nil {
			return fmt.Errorf("failed to marshal boot source: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, "PUT",
			"http://localhost/boot-source",
			bytes.NewReader(bootSourceJSON),
		)
		if err != nil {
			return fmt.Errorf("failed to create boot source request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to set boot source: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("boot source returned status: %d", resp.StatusCode)
		}
	}

	// Configure machine if provided
	if machineConfig, ok := configMap["machine-config"].(map[string]interface{}); ok {
		machineConfigJSON, err := json.Marshal(machineConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal machine config: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, "PUT",
			"http://localhost/machine-config",
			bytes.NewReader(machineConfigJSON),
		)
		if err != nil {
			return fmt.Errorf("failed to create machine config request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to set machine config: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("machine config returned status: %d", resp.StatusCode)
		}
	}

	// Configure the virtio-balloon device if the configuration requests one.
	// Firecracker rejects /balloon once the VM has started, so this must run
	// before the InstanceStart action in Start.
	if balloonRaw, ok := configMap["balloon"]; ok {
		device, err := parseBalloonConfig(balloonRaw)
		if err != nil {
			return fmt.Errorf("failed to set balloon: %w", err)
		}
		if err := vm.putAPI(ctx, socketPath, "/balloon", device); err != nil {
			return fmt.Errorf("failed to set balloon: %w", err)
		}
	}

	// Configure drives if provided
	if drivesRaw, ok := configMap["drives"].([]interface{}); ok {
		for _, driveRaw := range drivesRaw {
			drive, ok := driveRaw.(map[string]interface{})
			if !ok {
				continue
			}
			driveID, ok := drive["drive_id"].(string)
			if !ok || driveID == "" {
				continue
			}
			driveJSON, err := json.Marshal(drive)
			if err != nil {
				return fmt.Errorf("failed to marshal drive %s: %w", driveID, err)
			}
			req, err := http.NewRequestWithContext(ctx, "PUT",
				"http://localhost/drives/"+driveID,
				bytes.NewReader(driveJSON),
			)
			if err != nil {
				return fmt.Errorf("failed to create drive request for %s: %w", driveID, err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to set drive %s: %w", driveID, err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("drive %s returned status: %d", driveID, resp.StatusCode)
			}
			log.Info().Str("drive_id", driveID).Msg("Drive configured")
		}
	}

	// Configure vsock if provided, so the guest agent is reachable from the
	// host. Firecracker rejects /vsock once the VM has started, so this must
	// run before the InstanceStart action in Start.
	if vsock, ok := configMap["vsock"].(map[string]interface{}); ok {
		vsock["uds_path"] = vsockUDSPath(socketPath)

		vsockJSON, err := json.Marshal(vsock)
		if err != nil {
			return fmt.Errorf("failed to marshal vsock config: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, "PUT",
			"http://localhost/vsock",
			bytes.NewReader(vsockJSON),
		)
		if err != nil {
			return fmt.Errorf("failed to create vsock request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to set vsock: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("vsock returned status: %d", resp.StatusCode)
		}
	}

	return nil
}

// vsockUDSPath derives the Firecracker vsock Unix-socket path from the API
// socket path: <dir>/<id>.sock becomes <dir>/<id>.vsock. The host reaches the
// guest agent by dialing that muxer socket itself and sending "CONNECT <port>"
// (see pkg/agent.Dial); "<uds_path>_<port>" is only used for guest-initiated
// connections.
func vsockUDSPath(socketPath string) string {
	return strings.TrimSuffix(socketPath, ".sock") + ".vsock"
}

// Stop stops a running VM with graceful shutdown.
func (vm *VMMManager) Stop(ctx context.Context, task *types.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM not found for task %s", task.ID)
	}

	log.Info().
		Str("task_id", task.ID).
		Str("init_system", vmInstance.InitSystem).
		Msg("Stopping VM")

	// Guard against concurrent stops
	currentState := vmInstance.GetState()
	if currentState == VMStateStopping {
		return fmt.Errorf("VM %s is already stopping", task.ID)
	}
	if currentState == VMStateStarting {
		return fmt.Errorf("VM %s is still starting, wait before stopping", task.ID)
	}
	vmInstance.SetState(VMStateStopping)

	// If init system is enabled, try graceful shutdown first
	var stopErr error
	if vmInstance.InitSystem != "none" {
		stopErr = vm.gracefulShutdown(ctx, vmInstance)
	} else {
		// Otherwise, use the old method (SendCtrlAltDel)
		stopErr = vm.hardShutdown(ctx, vmInstance)
	}

	// A cgroup must not outlive its VM; this is a no-op when the VM never had one.
	vm.removeCgroup(task.ID)

	return stopErr
}

// gracefulShutdown attempts to gracefully shutdown the VM using init system.
func (vm *VMMManager) gracefulShutdown(ctx context.Context, vmInstance *VMInstance) error {
	log.Debug().
		Str("task_id", vmInstance.ID).
		Int("grace_period_sec", vmInstance.GracePeriodSec).
		Msg("Attempting graceful shutdown via init")

	// Send SIGTERM to the Firecracker process
	// Firecracker will forward this to the VM, and init will handle it
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		return vm.forceKillVM(vmInstance)
	}

	// Send SIGTERM
	if err := process.Signal(syscall.SIGTERM); err != nil {
		log.Debug().Err(err).Msg("SIGTERM failed, forcing kill")
		return vm.forceKillVM(vmInstance)
	}

	// Wait for graceful shutdown or timeout
	gracePeriod := time.Duration(vmInstance.GracePeriodSec) * time.Second
	shutdownChan := make(chan error, 1)

	go func() {
		// Poll for process exit with context cancellation
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := process.Signal(syscall.Signal(0)); err != nil {
					// Process has exited
					select {
					case shutdownChan <- nil:
					default:
					}
					return
				}
			}
		}
	}()

	select {
	case <-shutdownChan:
		vmInstance.SetState(VMStateStopped)
		log.Info().
			Str("task_id", vmInstance.ID).
			Msg("VM shutdown gracefully")
		return nil
	case <-time.After(gracePeriod):
		// Grace period expired, force kill
		log.Warn().
			Str("task_id", vmInstance.ID).
			Msg("Grace period expired, forcing kill")
		return vm.forceKillVM(vmInstance)
	case <-ctx.Done():
		// Context cancelled
		log.Warn().
			Str("task_id", vmInstance.ID).
			Msg("Context cancelled during shutdown")
		return vm.forceKillVM(vmInstance)
	}
}

// hardShutdown sends SendCtrlAltDel to the VM (legacy method).
func (vm *VMMManager) hardShutdown(ctx context.Context, vmInstance *VMInstance) error {
	socketPath := vmInstance.SocketPath

	// Send shutdown signal
	client := newUnixClient(socketPath, 5*time.Second)
	actions := ActionsType{ActionType: "SendCtrlAltDel"}

	body, err := json.Marshal(actions)
	if err != nil {
		log.Error().Err(err).Str("task_id", vmInstance.ID).Msg("Failed to marshal shutdown action")
		return vm.forceKillVM(vmInstance)
	}
	req, err := http.NewRequestWithContext(ctx, "PUT",
		"http://localhost/actions",
		bytes.NewReader(body),
	)
	if err != nil {
		log.Error().Err(err).Str("task_id", vmInstance.ID).Msg("Failed to create shutdown request")
		return vm.forceKillVM(vmInstance)
	}
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
		vmInstance.SetState(VMStateStopped)
	case <-time.After(30 * time.Second):
		// Force kill on timeout
		if err := vm.forceKillVM(vmInstance); err != nil {
			log.Warn().Err(err).Str("task_id", vmInstance.ID).Msg("Failed to force kill VM on timeout")
		}
	}

	log.Info().
		Str("task_id", vmInstance.ID).
		Msg("VM stopped")

	return nil
}

// Wait waits for a VM to exit.
func (vm *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return &types.TaskStatus{
			State:   types.TaskStateOrphaned,
			Message: "VM not found",
		}, nil
	}

	// Check if process is still running
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		//nolint:nilerr
		return &types.TaskStatus{
			State:   types.TaskStateComplete,
			Message: "Process not found",
		}, nil
	}

	// Send signal 0 to check if process is alive
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return &types.TaskStatus{
			State:   types.TaskStateComplete,
			Message: "Process exited",
		}, nil
	}

	return &types.TaskStatus{
		State: types.TaskStateRunning,
		RuntimeStatus: map[string]interface{}{
			"vm_id": vmInstance.ID,
			"pid":   vmInstance.PID,
			"state": string(vmInstance.GetState()),
		},
	}, nil
}

// Describe describes the current state of a VM.
func (vm *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	vm.mu.RLock()
	vmInstance, exists := vm.vms[task.ID]
	vm.mu.RUnlock()

	if !exists {
		return &types.TaskStatus{
			State:   types.TaskStateOrphaned,
			Message: "VM not found",
		}, nil
	}

	// Check process status
	process, err := os.FindProcess(vmInstance.PID)
	if err != nil {
		return &types.TaskStatus{
			State:   types.TaskStateFailed,
			Message: fmt.Sprintf("VM process error: %v", err),
		}, nil
	}

	// Check if still running
	if err := process.Signal(syscall.Signal(0)); err != nil {
		vmInstance.SetState(VMStateStopped)
		return &types.TaskStatus{
			State:   types.TaskStateComplete,
			Message: "VM has stopped",
		}, nil
	}

	// Map VM state to Task state
	var taskState types.TaskState
	switch vmInstance.GetState() {
	case VMStateNew:
		taskState = types.TaskStateNew
	case VMStateStarting:
		taskState = types.TaskStateStarting
	case VMStateRunning:
		taskState = types.TaskStateRunning
	case VMStateStopping:
		taskState = types.TaskStateStarting // Still in transition
	case VMStateStopped:
		taskState = types.TaskStateComplete
	case VMStateCrashed:
		taskState = types.TaskStateFailed
	default:
		taskState = types.TaskStateRunning // Default to running for safety
	}

	return &types.TaskStatus{
		State: taskState,
		RuntimeStatus: map[string]interface{}{
			"vm_id":  vmInstance.ID,
			"pid":    vmInstance.PID,
			"state":  string(vmInstance.GetState()),
			"uptime": time.Since(vmInstance.CreatedAt).String(),
		},
	}, nil
}

// Remove removes a VM and cleans up resources.
func (vm *VMMManager) Remove(ctx context.Context, task *types.Task) error {
	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}

	vm.mu.Lock()
	defer vm.mu.Unlock()

	log.Info().
		Str("task_id", task.ID).
		Msg("Removing VM")

	vmInstance, exists := vm.vms[task.ID]
	if !exists {
		return nil
	}

	// Guard against removing a VM in a transitional state
	state := vmInstance.GetState()
	if state == VMStateStopping || state == VMStateStarting {
		return fmt.Errorf("cannot remove VM %s in %s state", task.ID, state)
	}

	// Stop VM if running
	if vmInstance.GetState() == VMStateRunning {
		process, _ := os.FindProcess(vmInstance.PID)
		if process != nil {
			if err := process.Kill(); err != nil {
				log.Warn().Err(err).Int("pid", vmInstance.PID).Msg("Failed to kill VM process")
			}
		}
	}

	// Remove socket file
	if vmInstance.SocketPath != "" {
		if err := os.Remove(vmInstance.SocketPath); err != nil {
			log.Warn().Err(err).Msg("Failed to remove VM socket file")
		}
	}

	// Remove from map
	delete(vm.vms, task.ID)

	// A cgroup must not outlive its VM; this is a no-op when the VM never had one.
	vm.removeCgroup(task.ID)

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

	if err := process.Kill(); err != nil {
		// The process exiting on its own between the grace-period check and the
		// kill is still a successful (terminal) kill. Any other error is a real
		// failure and leaves the state untouched.
		if !errors.Is(err, os.ErrProcessDone) {
			return err
		}
	}

	// The forced kill is terminal: the VM is no longer running, so move it out
	// of the transitional "stopping" state instead of leaving it stuck forever.
	vmInstance.SetState(VMStateStopped)

	return nil
}

// waitForAPIServer waits for the Firecracker API server to be ready.
func waitForAPIServer(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := newUnixClient(socketPath, 100*time.Millisecond)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			// Socket exists, try to connect
			resp, err := client.Get("http://localhost/")
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

func newUnixClient(socketPath string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

// putAPI sends a PUT request to the Firecracker API over its Unix socket.
func (vm *VMMManager) putAPI(ctx context.Context, socketPath, path string, data interface{}) error {
	client := newUnixClient(socketPath, 5*time.Second)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON for %s: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent &&
		resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned status %d for %s: %s", resp.StatusCode, path, string(body))
	}

	return nil
}

// parseBalloonConfig normalises the "balloon" config entry into a /balloon
// request body. The amount can arrive as int64 (from a translator's map) or
// float64 (after a JSON round-trip), so the entry is decoded into a typed
// struct rather than type-asserted; any other shape is a wrapped error instead
// of a request Firecracker would reject.
func parseBalloonConfig(raw interface{}) (balloonDevice, error) {
	var device balloonDevice
	if raw == nil {
		return device, fmt.Errorf("invalid balloon config: <nil>")
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return device, fmt.Errorf("invalid balloon config %v: %w", raw, err)
	}
	if err := json.Unmarshal(encoded, &device); err != nil {
		return device, fmt.Errorf("invalid balloon config %v: %w", raw, err)
	}

	return device, nil
}

// Snapshot creates a snapshot of a running VM.
// This is a placeholder — full implementation is in the snapshot package.
func (vm *VMMManager) Snapshot(ctx context.Context, task *types.Task, opts interface{}) (interface{}, error) {
	log.Warn().Str("task_id", task.ID).Msg("Snapshot not implemented in VMMManager; use pkg/snapshot.Manager directly")
	return nil, fmt.Errorf("snapshot not implemented: use pkg/snapshot.Manager")
}

// Restore restores a VM from a snapshot.
// This is a placeholder — full implementation is in the snapshot package.
func (vm *VMMManager) Restore(ctx context.Context, task *types.Task, snap interface{}) error {
	log.Warn().Str("task_id", task.ID).Msg("Restore not implemented in VMMManager; use pkg/snapshot.Manager directly")
	return fmt.Errorf("restore not implemented: use pkg/snapshot.Manager")
}
