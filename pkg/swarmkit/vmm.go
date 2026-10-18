// Package swarmkit provides VMM management for SwarmKit integration.
package swarmkit

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

	"github.com/restuhaqza/swarmcracker/pkg/console"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
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
	cgroupMgr       jailer.CgroupOps
	// cgroupErr records why the cgroup manager could not be created, so a task
	// that requests a cpuset can be rejected with the reason instead of the
	// request being silently ignored.
	cgroupErr error
	// hugepagesPoolPath is the /proc/meminfo-style file read for a hugepages
	// pre-flight check. Empty means the production default ("/proc/meminfo");
	// tests point it at a fixture.
	hugepagesPoolPath string
	processes         map[string]*exec.Cmd
	processWaits      map[string]*processWait
	processMutex      sync.Mutex
	consoles          map[string]*console.Server
	consoleMutex      sync.Mutex
	logFiles          map[string]*os.File
	logMutex          sync.Mutex
	// logDir, when non-empty, is where each VM's console output is mirrored as
	// <logDir>/<task-id>.log so `swarmcracker vm logs <task-id>` can read it.
	logDir string
	logger zerolog.Logger
}

// processWait tracks a single Wait call per task so that concurrent
// Stop/ForceStop/Wait/Remove calls never double-Wait on the same process.
type processWait struct {
	once sync.Once
	err  error
}

// VMMManagerConfig holds VMM manager configuration.
type VMMManagerConfig struct {
	FirecrackerPath string
	JailerPath      string
	SocketDir       string
	// LogDir mirrors each VM's console output to <LogDir>/<task-id>.log. Empty
	// disables file logging (console is still mirrored to the daemon logger).
	LogDir          string
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

// toBool converts an interface{} value to bool.
func toBool(v interface{}) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// toStr converts an interface{} value to string.
func toStr(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
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
		processWaits:    make(map[string]*processWait),
		consoles:        make(map[string]*console.Server),
		logFiles:        make(map[string]*os.File),
		logDir:          cfg.LogDir,
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

		v.logger.Info().
			Str("jailer_path", jailerPath).
			Str("chroot_dir", jailerChrootDir).
			Str("cgroup_version", cgroupVersion).
			Msg("Jailer initialized")
	}

	// Create the cgroup manager for *both* start paths. A construction failure
	// is recorded rather than returned: a task that requests no cpuset can still
	// run without cgroups (as before), while one that does request a cpuset is
	// rejected from Start with this reason. Nothing is touched when cgroups are
	// disabled.
	if cfg.EnableCgroups {
		mgr, err := jailer.NewCgroupManager("")
		if err != nil {
			v.cgroupErr = err
			v.logger.Warn().Err(err).Msg("Failed to create cgroup manager, cgroup limits are unavailable")
		} else {
			v.cgroupMgr = mgr
		}
	}

	return v, nil
}

// Start starts a Firecracker VM for the given task.
func (v *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	v.logger.Info().
		Str("task_id", task.ID).
		Bool("jailer", v.useJailer).
		Msg("Starting Firecracker VM")

	// A hugepage request is a hard promise: an undersized host pool makes the
	// VM fault with SIGBUS after it starts. Check it here, before either start
	// path creates a process or a cgroup, so both paths share exactly one
	// check and a refusal leaves nothing behind. A task that asks for no
	// hugepages never reads the host pool.
	if err := v.checkHugepages(task, config); err != nil {
		return err
	}

	if v.useJailer {
		return v.startWithJailer(ctx, task, config)
	}
	return v.startDirect(ctx, task, config)
}

// startDirect starts Firecracker without jailer (legacy mode).
func (v *VMMManager) startDirect(ctx context.Context, task *types.Task, config interface{}) (startErr error) {
	// Create socket directory
	if err := os.MkdirAll(v.socketDir, 0755); err != nil {
		return fmt.Errorf("failed to create socket dir: %w", err)
	}

	// Apply the task's cgroup before the VMM exists: a refused cpuset must fail
	// the task without ever starting a process. The VM process is attached to
	// the cgroup once it has a pid, and the cgroup is torn down if startup fails.
	vcpus, memoryMB := machineSize(config)
	cgrouped, err := v.createVMCgroup(task, vcpus, memoryMB)
	if err != nil {
		return err
	}
	if cgrouped {
		defer func() {
			if startErr != nil {
				v.removeVMCgroup(task.ID)
			}
		}()
	}

	socketPath := filepath.Join(v.socketDir, task.ID+".sock")

	// Ensure socket is cleaned up on error
	var socketCleanupNeeded bool
	defer func() {
		if socketCleanupNeeded {
			os.Remove(socketPath)
		}
	}()

	// Start Firecracker as a long-lived process. Use an independent context:
	// the caller's context is typically canceled as soon as Start() returns,
	// which would immediately SIGKILL the VM. Process lifetime is managed
	// explicitly via Stop/ForceStop/Remove.
	cmd := exec.CommandContext(context.Background(), v.firecrackerPath,
		"--api-sock", socketPath,
		"--id", task.ID,
	)

	// Bridge the guest's serial console to a per-VM Unix socket so a user can
	// attach with `swarmcracker vm attach`. Firecracker wires guest ttyS0 to
	// its stdin/stdout, so giving it a pipe pair makes the console interactive.
	//
	// When a log directory is configured, also mirror that output to
	// <logDir>/<task-id>.log (the same convention the CLI uses), so guest logs
	// survive the VM and are readable with `swarmcracker vm logs <task-id>`.
	mirror := io.Writer(&logWriter{logger: v.logger})
	logFile, logErr := v.openLogFile(task.ID)
	if logErr != nil {
		v.logger.Warn().Err(logErr).Str("task_id", task.ID).
			Msg("Could not open VM log file; console will only be mirrored to the daemon log")
	} else if logFile != nil {
		v.setLogFile(task.ID, logFile)
		mirror = io.MultiWriter(logFile, &logWriter{logger: v.logger})
	}

	vmConsole, err := console.New(console.Config{
		SocketDir: v.socketDir,
		TaskID:    task.ID,
		Mirror:    mirror,
		Logger:    &v.logger,
	})
	if err != nil {
		v.closeLogFile(task.ID)
		socketCleanupNeeded = true
		return fmt.Errorf("failed to create VM console: %w", err)
	}
	cmd.Stdin = vmConsole.Stdin()
	cmd.Stdout = vmConsole.Stdout()
	cmd.Stderr = &logWriter{logger: v.logger}

	if err := cmd.Start(); err != nil {
		vmConsole.Close()
		v.closeLogFile(task.ID)
		socketCleanupNeeded = true
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	vmConsole.Start()
	v.setConsole(task.ID, vmConsole)

	// Store process reference
	v.processMutex.Lock()
	v.processes[task.ID] = cmd
	v.processMutex.Unlock()

	// Attach the VMM to its cgroup now that it has a pid. A cpuset that could
	// not be attached never took effect, so that fails the VM.
	if err := v.addVMCgroupProcess(task, cgrouped, cmd.Process.Pid); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			log.Warn().Err(killErr).Msg("Failed to kill process after cgroup error")
		}
		v.closeConsole(task.ID)
		socketCleanupNeeded = true
		return err
	}

	// Wait for socket to be created
	if err := v.waitForSocket(ctx, socketPath, 10*time.Second); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			log.Warn().Err(killErr).Msg("Failed to kill process after socket timeout")
		}
		v.closeConsole(task.ID)
		socketCleanupNeeded = true
		return fmt.Errorf("socket not created: %w", err)
	}

	// Configure VM via Firecracker HTTP API
	if err := v.configureVM(ctx, task, socketPath, config); err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			log.Warn().Err(killErr).Msg("Failed to kill process after configure error")
		}
		v.closeConsole(task.ID)
		socketCleanupNeeded = true
		return fmt.Errorf("failed to configure VM: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Str("socket", socketPath).
		Msg("Firecracker VM started")

	return nil
}

// jailerVMConfig builds the reduced config a jailed Firecracker is configured
// with: all paths are chroot-relative. The caller's balloon target is carried
// over, because the device can only be created before boot — without it a
// jailed service VM would silently ignore its configured balloon annotation.
func jailerVMConfig(cfg, machineConfig, bootSource map[string]interface{}) map[string]interface{} {
	jailed := map[string]interface{}{
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

	if balloon, ok := cfg["balloon"]; ok {
		jailed["balloon"] = balloon
	}

	return jailed
}

// startWithJailer starts Firecracker inside jailer with isolation.
func (v *VMMManager) startWithJailer(ctx context.Context, task *types.Task, config interface{}) (startErr error) {
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
		KernelPath: toStr(bootSource["kernel_image_path"]),
		RootfsPath: rootfsPath,
		BootArgs:   toStr(bootSource["boot_args"]),
		HtEnabled:  toBool(machineConfig["smt"]), // Extract SMT from machine config
	}

	// Create the task's cgroup before the jailer (and thus Firecracker) starts, so
	// a refused cpuset fails the task without a process to clean up. The jailer's
	// pid is added once it exists.
	cgrouped, err := v.createVMCgroup(task, jailerCfg.VcpuCount, jailerCfg.MemoryMB)
	if err != nil {
		return err
	}
	if cgrouped {
		defer func() {
			if startErr != nil {
				v.removeVMCgroup(task.ID)
			}
		}()
	}

	// Start jailed VM
	process, err := v.jailer.Start(ctx, jailerCfg)
	if err != nil {
		return fmt.Errorf("failed to start jailed VM: %w", err)
	}

	// Attach the jailer (and through it Firecracker) to its cgroup. A cpuset
	// that could not be applied never took effect, so that fails the VM.
	if err := v.addVMCgroupProcess(task, cgrouped, process.Pid); err != nil {
		if stopErr := v.jailer.Stop(ctx, task.ID); stopErr != nil {
			v.logger.Warn().Err(stopErr).Msg("Failed to stop jailer after cgroup error")
		}
		return err
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
	jailerConfig := jailerVMConfig(cfg, machineConfig, bootSource)

	if err := v.configureVM(ctx, task, process.SocketPath, jailerConfig); err != nil {
		v.logger.Error().Err(err).Msg("Failed to configure jailed VM")
		if stopErr := v.jailer.Stop(ctx, task.ID); stopErr != nil {
			v.logger.Warn().Err(stopErr).Msg("Failed to stop jailer after configure error")
		}
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
	if err := v.putAPI(ctx, socketPath, "/machine-config", machineConfig); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	// 2. Set boot source
	bootSource, ok := cfg["boot-source"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing boot-source in config")
	}
	if err := v.putAPI(ctx, socketPath, "/boot-source", bootSource); err != nil {
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
		if err := v.putAPI(ctx, socketPath, "/drives/"+driveID, drive); err != nil {
			return fmt.Errorf("failed to set drive %s: %w", driveID, err)
		}
		v.logger.Info().Str("drive_id", driveID).Msg("Drive configured")
	}

	// 4. Set network interfaces (if any)
	// Handle both []interface{} (from JSON) and []map[string]interface{} (from translator)
	var networkInterfacesRaw []interface{}
	switch ifaces := cfg["network-interfaces"].(type) {
	case []interface{}:
		networkInterfacesRaw = ifaces
	case []map[string]interface{}:
		for _, m := range ifaces {
			networkInterfacesRaw = append(networkInterfacesRaw, m)
		}
	}
	if len(networkInterfacesRaw) > 0 {
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
			if err := v.putAPI(ctx, socketPath, "/network-interfaces/"+ifaceID, iface); err != nil {
				return fmt.Errorf("failed to set network interface %s: %w", ifaceID, err)
			}
		}
	} else {
		v.logger.Warn().Msg("No network interfaces in config")
	}

	// 5. Configure the vsock device so the guest agent is reachable from the
	// host. Firecracker rejects /vsock once the VM has started, so this must
	// precede the InstanceStart action.
	if vsock, ok := cfg["vsock"].(map[string]interface{}); ok {
		vsock["uds_path"] = vsockUDSPath(socketPath)
		if err := v.putAPI(ctx, socketPath, "/vsock", vsock); err != nil {
			return fmt.Errorf("failed to set vsock: %w", err)
		}
	}

	// 6. Configure the virtio-balloon device when the task requested one.
	// Firecracker rejects /balloon once the VM has started, so this must
	// precede the InstanceStart action as well.
	if balloonRaw, ok := cfg["balloon"]; ok {
		device, err := parseBalloonConfig(balloonRaw)
		if err != nil {
			return fmt.Errorf("failed to set balloon: %w", err)
		}
		if err := v.putAPI(ctx, socketPath, "/balloon", device); err != nil {
			return fmt.Errorf("failed to set balloon: %w", err)
		}
	}

	// 7. Start the VM
	action := Action{
		ActionType: "InstanceStart",
	}
	if err := v.putAPI(ctx, socketPath, "/actions", action); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	v.logger.Info().
		Str("task_id", task.ID).
		Msg("VM configured and started via API")

	return nil
}

// waitForSocket waits for the Firecracker socket to be created.
func (v *VMMManager) waitForSocket(ctx context.Context, socketPath string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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

// waitForProcess waits for a Firecracker process exactly once, even when
// Stop, ForceStop, Wait, and Remove are called concurrently. The first
// caller runs cmd.Wait(); subsequent callers block on the same sync.Once
// and observe the same result.
func (v *VMMManager) waitForProcess(taskID string, cmd *exec.Cmd) error {
	v.processMutex.Lock()
	if v.processWaits == nil {
		v.processWaits = make(map[string]*processWait)
	}
	pw, ok := v.processWaits[taskID]
	if !ok {
		pw = &processWait{}
		v.processWaits[taskID] = pw
	}
	v.processMutex.Unlock()

	pw.once.Do(func() { pw.err = cmd.Wait() })
	return pw.err
}

// forgetProcess removes process bookkeeping for a finished task.
func (v *VMMManager) forgetProcess(taskID string) {
	v.processMutex.Lock()
	delete(v.processes, taskID)
	delete(v.processWaits, taskID)
	v.processMutex.Unlock()
	v.closeConsole(taskID)
}

// setConsole records the console server for a running task.
func (v *VMMManager) setConsole(taskID string, srv *console.Server) {
	v.consoleMutex.Lock()
	if v.consoles == nil {
		v.consoles = make(map[string]*console.Server)
	}
	v.consoles[taskID] = srv
	v.consoleMutex.Unlock()
}

// closeConsole stops and forgets the console server for a task, if any.
func (v *VMMManager) closeConsole(taskID string) {
	v.consoleMutex.Lock()
	srv := v.consoles[taskID]
	delete(v.consoles, taskID)
	v.consoleMutex.Unlock()
	if srv != nil {
		srv.Close()
	}
	v.closeLogFile(taskID)
}

// openLogFile opens (truncating) the console log file for a task. It returns
// (nil, nil) when file logging is disabled (no log directory configured).
func (v *VMMManager) openLogFile(taskID string) (*os.File, error) {
	if v.logDir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(v.logDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create log directory %s: %w", v.logDir, err)
	}
	f, err := os.OpenFile(filepath.Join(v.logDir, taskID+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	return f, nil
}

// setLogFile records the open console log file for a task.
func (v *VMMManager) setLogFile(taskID string, f *os.File) {
	v.logMutex.Lock()
	if v.logFiles == nil {
		v.logFiles = make(map[string]*os.File)
	}
	v.logFiles[taskID] = f
	v.logMutex.Unlock()
}

// closeLogFile closes and forgets the console log file for a task, if any.
func (v *VMMManager) closeLogFile(taskID string) {
	v.logMutex.Lock()
	f := v.logFiles[taskID]
	delete(v.logFiles, taskID)
	v.logMutex.Unlock()
	if f != nil {
		_ = f.Close()
	}
}

// cleanupVMSockets closes the task's console and removes its Firecracker API
// and console socket files, along with the per-task network metadata the CLI
// reads. It is safe to call when they are already gone.
func (v *VMMManager) cleanupVMSockets(taskID string) {
	v.closeConsole(taskID)

	for _, path := range []string{
		filepath.Join(v.socketDir, taskID+".sock"),
		filepath.Join(v.socketDir, taskID+".console.sock"),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			v.logger.Warn().Err(err).Str("socket", path).Msg("Failed to remove VM socket")
		}
	}

	// The metadata file lives next to the socket and is only meaningful while
	// the VM is running, so drop it with the sockets.
	if err := scruntime.RemoveVMMetadata(v.socketDir, taskID); err != nil {
		v.logger.Warn().Err(err).Str("task_id", taskID).Msg("Failed to remove VM metadata")
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
	}

	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()
	if !ok {
		// The cgroup must not outlive the VM, on either start path, even when
		// the process bookkeeping is already gone.
		v.removeVMCgroup(task.ID)
		return fmt.Errorf("task not found")
	}

	// Send SIGTERM for graceful shutdown
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		v.logger.Error().Err(err).Msg("Failed to send SIGTERM")
	}

	// Wait for process to exit or kill it after timeout. waitForProcess
	// guarantees cmd.Wait() is called exactly once per task.
	done := make(chan error, 1)
	go func() {
		done <- v.waitForProcess(task.ID, cmd)
	}()

	select {
	case err := <-done:
		if err != nil {
			v.logger.Error().Err(err).Msg("Process exited with error")
		}
	case <-time.After(10 * time.Second):
		v.logger.Warn().Msg("Process did not exit gracefully, killing")
		if killErr := cmd.Process.Kill(); killErr != nil {
			v.logger.Warn().Err(killErr).Msg("Failed to kill process after timeout")
		}
		<-done // reap the waiter goroutine
	}

	// Remove the cgroup only after the VMM process is gone: RemoveCgroup
	// migrates a live process back to the root cgroup, which would silently
	// drop the quota and the pin for the rest of a timed-out stop.
	v.removeVMCgroup(task.ID)

	v.forgetProcess(task.ID)
	v.cleanupVMSockets(task.ID)
	v.logger.Info().Str("task_id", task.ID).Msg("Firecracker VM stopped")

	return nil
}

// ForceStop forcefully terminates the Firecracker VM without grace period.
func (v *VMMManager) ForceStop(ctx context.Context, task *types.Task) error {
	v.logger.Warn().
		Str("task_id", task.ID).
		Msg("Force stopping Firecracker VM")

	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()
	if !ok {
		return fmt.Errorf("task not found")
	}

	// Force kill immediately without waiting
	if err := cmd.Process.Kill(); err != nil {
		v.logger.Error().Err(err).Msg("Failed to kill process")
		return fmt.Errorf("failed to kill process: %w", err)
	}

	// Wait for process to actually exit (should be immediate).
	// waitForProcess ensures Wait is called exactly once per task.
	_ = v.waitForProcess(task.ID, cmd)

	// A force-stopped VM's cgroup must not outlive it, exactly like Stop/Remove:
	// this is the only remaining teardown path the health monitor reaches.
	v.removeVMCgroup(task.ID)

	v.forgetProcess(task.ID)
	v.cleanupVMSockets(task.ID)
	v.logger.Info().Str("task_id", task.ID).Msg("Firecracker VM force stopped")

	return nil
}

// errTaskNotTracked is returned by Wait when the manager has no process
// bookkeeping for the task. It is deliberately not the clean-exit signal: the
// bookkeeping is removed by a teardown (Stop/ForceStop/Remove call
// forgetProcess), so an untracked Wait cannot say how the VM exited. Only a
// completed waitForProcess may report a clean exit; a reaped task must be
// classified as an unexplained exit, not a stop.
var errTaskNotTracked = errors.New("task is not tracked by the VMM manager")

// Wait waits for the Firecracker VM to exit.
func (v *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	v.processMutex.Lock()
	cmd, ok := v.processes[task.ID]
	v.processMutex.Unlock()

	if !ok {
		return nil, errTaskNotTracked
	}

	// Wait for process (exactly once per task). Respect context cancellation so
	// the SwarmKit task manager can re-dispatch (e.g. to process a Shutdown or
	// Remove desired state) instead of blocking here until the VM exits on its
	// own. waitForProcess still calls cmd.Wait() exactly once per task; the
	// goroutine completes later even if this call returns early.
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- v.waitForProcess(task.ID, cmd)
	}()

	var err error
	select {
	case err = <-waitDone:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

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
func (v *VMMManager) CheckVMAPIHealth(ctx context.Context, taskID string) bool {
	socketPath := filepath.Join(v.socketDir, taskID+".sock")

	// Check if socket file exists
	if _, err := os.Stat(socketPath); err != nil {
		v.logger.Debug().Str("task_id", taskID).Err(err).Msg("API socket not found")
		return false
	}

	// Try to query the machine configuration via API
	client := createFirecrackerHTTPClient(socketPath)
	url := "http://localhost/machine-config"

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
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
	if err != nil {
		return false
	}

	// A zombie still answers Signal(0), so /proc/<pid>/stat exposes the real
	// state. If it's 'Z' the process has exited but not been reaped yet.
	return v.processStateRunning(cmd.Process.Pid)
}

// processStateRunning reports whether the given PID is alive and not a zombie
// by reading /proc/<pid>/stat. State 'Z' (zombie) or a missing entry means the
// process is not meaningfully running.
func (v *VMMManager) processStateRunning(pid int) bool {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return false // Process gone or unreadable
	}
	// Format: pid (comm) state ... — the state is the char after the parenthesized
	// comm field. Find the final ')' and read the next non-space token.
	idx := strings.LastIndex(string(data), ")")
	if idx < 0 || idx+2 >= len(data) {
		return false
	}
	state := data[idx+2]
	// 'Z' = zombie, 'X'/'x' = dead
	return state != 'Z' && state != 'X' && state != 'x'
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
			// Wait for the process to exit (exactly once per task)
			if err := v.waitForProcess(task.ID, cmd); err != nil {
				v.logger.Warn().Err(err).Msg("Error waiting for process during removal")
			}
		}
	}

	// Remove the task's cgroup on either start path, and only after the VMM
	// process has exited: RemoveCgroup migrates a live process to the root
	// cgroup, which would drop its quota and pin.
	v.removeVMCgroup(task.ID)

	// Remove socket if exists
	socketPath := filepath.Join(v.socketDir, task.ID+".sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		v.logger.Warn().Err(err).Msg("Failed to remove socket")
	} else if err == nil {
		v.logger.Debug().Str("socket", socketPath).Msg("Removed socket file")
	}

	v.forgetProcess(task.ID)
	v.cleanupVMSockets(task.ID)

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
	ActionType string `json:"action_type"`
	// TimeoutMS is the Firecracker action timeout in milliseconds.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// vsockUDSPath derives the Firecracker vsock Unix-socket path from the API
// socket path: <dir>/<id>.sock becomes <dir>/<id>.vsock. The host reaches the
// guest agent by dialing that muxer socket itself and sending "CONNECT <port>"
// (see pkg/agent.Dial); "<uds_path>_<port>" is only used for guest-initiated
// connections.
func vsockUDSPath(socketPath string) string {
	return strings.TrimSuffix(socketPath, ".sock") + ".vsock"
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
func (v *VMMManager) putAPI(ctx context.Context, socketPath, path string, data interface{}) error {
	client := createFirecrackerHTTPClient(socketPath)

	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	url := fmt.Sprintf("http://localhost%s", path)
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(jsonData))
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

// parseBalloonConfig normalises the "balloon" config entry into a /balloon
// request body. The amount can arrive as int64 (from the translator's map) or
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

// GetRunningProcesses returns a map of all running Firecracker processes.
func (v *VMMManager) GetRunningProcesses() map[string]*exec.Cmd {
	v.processMutex.Lock()
	defer v.processMutex.Unlock()

	// Return a copy to avoid race conditions
	result := make(map[string]*exec.Cmd)
	for k, cmd := range v.processes {
		result[k] = cmd
	}
	return result
}

// RemoveProcess removes a process from the tracked processes map.
func (v *VMMManager) RemoveProcess(taskID string) {
	v.processMutex.Lock()
	delete(v.processes, taskID)
	delete(v.processWaits, taskID)
	v.processMutex.Unlock()
	v.closeConsole(taskID)
	v.logger.Debug().Str("task_id", taskID).Msg("Process removed from tracking")
}
