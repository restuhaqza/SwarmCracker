// Package swarmkit provides SwarmKit executor integration for SwarmCracker.
//
// This package implements the SwarmKit executor interface to run containers
// as Firecracker microVMs instead of traditional containers.
package swarmkit

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	swarmkit_exec "github.com/moby/swarmkit/v2/agent/exec"
	"github.com/moby/swarmkit/v2/api"
	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	zerolog_log "github.com/rs/zerolog/log"
)

// Executor implements SwarmKit's executor interface backed by SwarmCracker.
type Executor struct {
	config        *Config
	imagePrep     types.ImagePreparer
	networkMgr    types.NetworkManager
	vmmMgr        *VMMManager
	controllers   map[string]*Controller
	executorMu    sync.RWMutex
	cleanupCancel context.CancelFunc
	cleanupDone   chan struct{}
}

// Config holds the SwarmKit integration configuration.
type Config struct {
	FirecrackerPath  string `yaml:"firecracker_path"`
	KernelPath       string `yaml:"kernel_path"`
	RootfsDir        string `yaml:"rootfs_dir"`
	SocketDir        string `yaml:"socket_dir"`
	DefaultVCPUs     int    `yaml:"default_vcpus"`
	DefaultMemoryMB  int    `yaml:"default_memory_mb"`
	BridgeName       string `yaml:"bridge_name"`
	Subnet           string `yaml:"subnet"`
	BridgeIP         string `yaml:"bridge_ip"`
	IPMode           string `yaml:"ip_mode"`
	NATEnabled       bool   `yaml:"nat_enabled"`
	VXLANEnabled     bool   `yaml:"vxlan_enabled"`
	VXLANPeers       []string `yaml:"vxlan_peers"`
	Debug            bool   `yaml:"debug"`
	ReservedCPUs     int    `yaml:"reserved_cpus"`
	ReservedMemoryMB int    `yaml:"reserved_memory_mb"`
	MaxImageAgeDays  int    `yaml:"max_image_age_days"` // Maximum age of rootfs images before cleanup (default 7)
}

// NewExecutor creates a new SwarmKit executor backed by SwarmCracker.
func NewExecutor(config *Config) (*Executor, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Set defaults
	if config.FirecrackerPath == "" {
		config.FirecrackerPath = "firecracker"
	}
	if config.KernelPath == "" {
		config.KernelPath = "/usr/share/firecracker/vmlinux"
	}
	if config.RootfsDir == "" {
		config.RootfsDir = "/var/lib/firecracker/rootfs"
	}
	if config.SocketDir == "" {
		config.SocketDir = "/var/run/firecracker"
	}
	if config.DefaultVCPUs == 0 {
		config.DefaultVCPUs = 1
	}
	if config.DefaultMemoryMB == 0 {
		config.DefaultMemoryMB = 512
	}
	if config.BridgeName == "" {
		config.BridgeName = "swarm-br0"
	}
	if config.Subnet == "" {
		config.Subnet = "192.168.127.0/24"
	}
	if config.BridgeIP == "" {
		config.BridgeIP = "192.168.127.1/24"
	}
	if config.IPMode == "" {
		config.IPMode = "static"
	}

	// Create image preparer
	imageCfg := &image.PreparerConfig{
		RootfsDir: config.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	// Create network manager
	netCfg := types.NetworkConfig{
		BridgeName:    config.BridgeName,
		Subnet:        config.Subnet,
		BridgeIP:      config.BridgeIP,
		IPMode:        config.IPMode,
		NATEnabled:    config.NATEnabled,
		VXLANEnabled:  config.VXLANEnabled,
		VXLANPeers:    config.VXLANPeers,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	// Create VMM manager
	vmmMgr, err := NewVMMManager(config.FirecrackerPath, config.SocketDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create VMM manager: %w", err)
	}

	// Create context for cleanup goroutine
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())

	exec := &Executor{
		config:        config,
		imagePrep:     imagePrep,
		networkMgr:    networkMgr,
		vmmMgr:        vmmMgr,
		controllers:   make(map[string]*Controller),
		cleanupCancel: cleanupCancel,
		cleanupDone:   make(chan struct{}),
	}

	// Start periodic cleanup goroutine
	go exec.periodicCleanup(cleanupCtx)

	return exec, nil
}

// periodicCleanup runs image cleanup every 24 hours.
func (e *Executor) periodicCleanup(ctx context.Context) {
	defer close(e.cleanupDone)

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// Run initial cleanup after a short delay (to avoid startup churn)
	select {
	case <-time.After(5 * time.Minute):
		e.runCleanup(ctx)
	case <-ctx.Done():
		return
	}

	for {
		select {
		case <-ticker.C:
			e.runCleanup(ctx)
		case <-ctx.Done():
			zerolog_log.Debug().Msg("Periodic cleanup goroutine stopping")
			return
		}
	}
}

// runCleanup executes the image cleanup.
func (e *Executor) runCleanup(ctx context.Context) {
	zerolog_log.Info().Msg("Running periodic image cleanup")

	// Get MaxImageAgeDays from config, default to 7
	maxAgeDays := 7
	if e.config.MaxImageAgeDays > 0 {
		maxAgeDays = e.config.MaxImageAgeDays
	}

	// Cleanup now returns filesRemoved and bytesFreed
	filesRemoved, bytesFreed, err := e.imagePrep.Cleanup(ctx, maxAgeDays)
	if err != nil {
		zerolog_log.Error().Err(err).Msg("Periodic cleanup failed")
		return
	}

	if filesRemoved > 0 {
		zerolog_log.Info().
			Int("files_removed", filesRemoved).
			Int64("bytes_freed", bytesFreed).
			Msg("Periodic cleanup completed")
	} else {
		zerolog_log.Debug().Msg("Periodic cleanup: no old images to remove")
	}
}

// Describe returns the node description for this executor.
func (e *Executor) Describe(ctx context.Context) (*api.NodeDescription, error) {
	log.G(ctx).Debug("Describing executor")

	// Get system resources
	nanoCPUs := e.getCPUs()
	memoryBytes := e.getMemory()

	// Build generic resources
	genericResources := []*api.GenericResource{}

	// Only report Firecracker if KVM is available
	if e.kvmAvailable() {
		genericResources = append(genericResources, &api.GenericResource{
			Resource: &api.GenericResource_NamedResourceSpec{
				NamedResourceSpec: &api.NamedGenericResource{
					Kind:  "Firecracker",
					Value: "available",
				},
			},
		})
		log.G(ctx).Info("KVM available: reporting Firecracker resource")
	} else {
		log.G(ctx).Warning("KVM not available: not reporting Firecracker resource")
	}

	return &api.NodeDescription{
		Hostname: hostname(),
		Resources: &api.Resources{
			NanoCPUs:    nanoCPUs,
			MemoryBytes: memoryBytes,
			Generic:     genericResources,
		},
	}, nil
}

// Configure configures the executor with node state.
func (e *Executor) Configure(ctx context.Context, node *api.Node) error {
	log.G(ctx).WithField("node.id", node.ID).Debug("Configuring executor")
	// Nothing to configure for now
	return nil
}

// Controller returns a controller for the given task.
func (e *Executor) Controller(t *api.Task) (swarmkit_exec.Controller, error) {
	e.executorMu.Lock()
	defer e.executorMu.Unlock()

	// Check if controller already exists
	if ctrl, ok := e.controllers[t.ID]; ok {
		return ctrl, nil
	}

	// Create new controller
	ctrl, err := NewController(t, e.config, e.imagePrep, e.networkMgr, e.vmmMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to create controller: %w", err)
	}

	// Set up deregistration callback to remove controller from map when removed
	ctrl.OnRemove = func() {
		e.executorMu.Lock()
		defer e.executorMu.Unlock()
		delete(e.controllers, t.ID)
		zerolog_log.Debug().Str("task_id", t.ID).Msg("Controller deregistered from executor")
	}

	e.controllers[t.ID] = ctrl
	return ctrl, nil
}

// SetNetworkBootstrapKeys sets network encryption keys.
func (e *Executor) SetNetworkBootstrapKeys(keys []*api.EncryptionKey) error {
	zerolog_log.Debug().Msgf("Setting network bootstrap keys: %d keys", len(keys))
	// TODO: Implement network key management
	return nil
}

// Controller implements SwarmKit's controller interface for a single task.
type Controller struct {
	task       *api.Task
	config     *Config
	imagePrep  types.ImagePreparer
	networkMgr types.NetworkManager
	vmmMgr     *VMMManager
	trans      types.TaskTranslator
	mu         sync.Mutex
	prepared   bool
	started    bool

	// internalTask holds the prepared internal task with annotations
	internalTask *types.Task

	process    *os.Process
	socketPath string
	cancel     context.CancelFunc
	logger     zerolog.Logger

	// OnRemove is called when the controller is removed from the executor
	OnRemove func()
}

// NewController creates a new task controller.
func NewController(
	task *api.Task,
	config *Config,
	imagePrep types.ImagePreparer,
	networkMgr types.NetworkManager,
	vmmMgr *VMMManager,
) (*Controller, error) {
	trans, err := NewTaskTranslator(config.KernelPath, config.BridgeIP)
	if err != nil {
		return nil, fmt.Errorf("failed to create translator: %w", err)
	}

	return &Controller{
		task:       task,
		config:     config,
		imagePrep:  imagePrep,
		networkMgr: networkMgr,
		vmmMgr:     vmmMgr,
		trans:      trans,
		socketPath: filepath.Join(config.SocketDir, task.ID+".sock"),
		logger:     zerolog_log.With().Str("task_id", task.ID).Logger(),
	}, nil
}

// Update updates the task definition.
func (c *Controller) Update(ctx context.Context, t *api.Task) error {
	c.logger.Info().Msg("Updating task")

	c.mu.Lock()
	defer c.mu.Unlock()

	// Can't update if already started
	if c.started {
		return fmt.Errorf("cannot update started task")
	}

	c.task = t
	return nil
}

// Prepare prepares the task for execution.
func (c *Controller) Prepare(ctx context.Context) error {
	c.logger.Info().Msg("Preparing task")

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.prepared {
		return nil // Already prepared
	}

	// Convert SwarmKit task to internal type
	task := c.convertTask()

	// Prepare image
	if err := c.imagePrep.Prepare(ctx, task); err != nil {
		return fmt.Errorf("image preparation failed: %w", err)
	}

	// Store the prepared task with annotations
	c.internalTask = task

	// Prepare network
	if err := c.networkMgr.PrepareNetwork(ctx, task); err != nil {
		return fmt.Errorf("network preparation failed: %w", err)
	}

	c.prepared = true
	c.logger.Info().Msg("Task prepared")
	return nil
}

// Start starts the task.
func (c *Controller) Start(ctx context.Context) error {
	c.logger.Info().Msg("Starting task")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.prepared {
		return fmt.Errorf("task not prepared")
	}

	if c.started {
		return nil // Already started
	}

	// Use the prepared internal task (has annotations like rootfs path)
	task := c.internalTask
	if task == nil {
		return fmt.Errorf("internal task not prepared")
	}

	// Translate to VM config
	vmConfig, err := c.trans.Translate(task)
	if err != nil {
		return fmt.Errorf("translation failed: %w", err)
	}

	// Start VM
	if err := c.vmmMgr.Start(ctx, task, vmConfig); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	c.started = true
	c.logger.Info().Msg("Task started")
	return nil
}

// Wait waits for the task to exit.
func (c *Controller) Wait(ctx context.Context) error {
	c.logger.Debug().Msg("Waiting for task")

	task := c.convertTask()

	status, err := c.vmmMgr.Wait(ctx, task)
	if err != nil {
		return err
	}

	if status.Err != nil {
		return status.Err
	}

	return nil
}

// Shutdown gracefully shuts down the task.
func (c *Controller) Shutdown(ctx context.Context) error {
	c.logger.Info().Msg("Shutting down task gracefully")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		c.logger.Debug().Msg("Task not started, nothing to shut down")
		return nil
	}

	task := c.convertTask()

	// Create a shutdown context with 30 second timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Attempt graceful shutdown via VMM manager
	if err := c.vmmMgr.Stop(shutdownCtx, task); err != nil {
		c.logger.Error().Err(err).Msg("Graceful shutdown failed")
		return fmt.Errorf("failed to shutdown VM: %w", err)
	}

	// Cleanup network after VM stops
	if err := c.networkMgr.CleanupNetwork(shutdownCtx, task); err != nil {
		c.logger.Warn().Err(err).Msg("Failed to cleanup network after shutdown")
	}

	// Mark as not started
	c.started = false

	c.logger.Info().Msg("Task shut down successfully")
	return nil
}

// Terminate forcefully terminates the task.
func (c *Controller) Terminate(ctx context.Context) error {
	c.logger.Info().Msg("Forcefully terminating task")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		c.logger.Debug().Msg("Task not started, nothing to terminate")
		return nil
	}

	task := c.convertTask()

	// Force kill immediately without grace period
	if err := c.vmmMgr.Stop(ctx, task); err != nil {
		c.logger.Error().Err(err).Msg("Force terminate failed")
		return fmt.Errorf("failed to force terminate VM: %w", err)
	}

	// Mark as not started
	c.started = false

	c.logger.Info().Msg("Task terminated forcefully")
	return nil
}

// Remove removes all task resources.
func (c *Controller) Remove(ctx context.Context) error {
	c.logger.Info().Msg("Removing task resources")

	c.mu.Lock()
	defer c.mu.Unlock()

	task := c.convertTask()

	// Cleanup network (includes dnsmasq entries)
	if err := c.networkMgr.CleanupNetwork(ctx, task); err != nil {
		c.logger.Error().Err(err).Msg("Failed to cleanup network")
	}

	// Remove VM (stops if running and removes socket)
	if err := c.vmmMgr.Remove(ctx, task); err != nil {
		c.logger.Error().Err(err).Msg("Failed to remove VM")
	}

	// Clean up rootfs image
	rootfsPath := filepath.Join(c.config.RootfsDir, task.ID+".ext4")
	if err := os.Remove(rootfsPath); err != nil && !os.IsNotExist(err) {
		c.logger.Warn().Err(err).Str("path", rootfsPath).Msg("Failed to remove rootfs image")
	} else if err == nil {
		c.logger.Debug().Str("path", rootfsPath).Msg("Removed rootfs image")
	}

	// Clean up socket file (should be removed by vmmMgr.Remove, but ensure it)
	socketPath := filepath.Join(c.config.SocketDir, task.ID+".sock")
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		c.logger.Warn().Err(err).Str("path", socketPath).Msg("Failed to remove socket file")
	}

	c.started = false
	c.prepared = false

	c.logger.Info().Msg("Task resources removed")

	// Call deregistration callback if set
	if c.OnRemove != nil {
		c.logger.Debug().Msg("Calling OnRemove callback")
		c.OnRemove()
	}

	return nil
}

// Close closes ephemeral resources.
func (c *Controller) Close() error {
	c.logger.Debug().Msg("Closing controller")

	// Remove from executor's controller map
	// (This is handled by the executor)

	return nil
}

// convertTask converts SwarmKit task to internal task type.
func (c *Controller) convertTask() *types.Task {
	containerSpec, ok := c.task.Spec.Runtime.(*api.TaskSpec_Container)
	if !ok || containerSpec.Container == nil {
		// Return minimal task spec
		return &types.Task{
			ID:        c.task.ID,
			ServiceID: c.task.ServiceID,
			NodeID:    c.task.NodeID,
			Spec: types.TaskSpec{
				Runtime: &types.Container{},
			},
		}
	}

	container := &types.Container{
		Image:   containerSpec.Container.Image,
		Command: containerSpec.Container.Command,
		Args:    containerSpec.Container.Args,
		Env:     containerSpec.Container.Env,
	}

	// Convert mounts
	var mounts []types.Mount
	for _, m := range containerSpec.Container.Mounts {
		mounts = append(mounts, types.Mount{
			Target:   m.Target,
			Source:   m.Source,
			ReadOnly: m.ReadOnly,
		})
	}
	container.Mounts = mounts

	// Convert resources
	resources := &types.ResourceRequirements{}
	if c.task.Spec.Resources != nil && c.task.Spec.Resources.Reservations != nil {
		resources.Reservations = &types.Resources{
			NanoCPUs:    c.task.Spec.Resources.Reservations.NanoCPUs,
			MemoryBytes: c.task.Spec.Resources.Reservations.MemoryBytes,
		}
	}

	// Convert networks
	var networks []types.NetworkAttachment
	for _, n := range c.task.Networks {
		if n == nil || n.Network == nil {
			continue
		}

		driver := "bridge" // default
		if n.Network.DriverState != nil {
			driver = n.Network.DriverState.Name
		} else if n.Network.Spec.DriverConfig != nil {
			driver = n.Network.Spec.DriverConfig.Name
		}

		// Convert DriverConfig - bridge name comes from Options map in SwarmKit API
		var driverConfig *types.DriverConfig
		if n.Network.Spec.DriverConfig != nil && n.Network.Spec.DriverConfig.Options != nil {
			driverConfig = &types.DriverConfig{}
			// Check for bridge name in options (key may vary)
			if bridgeName, ok := n.Network.Spec.DriverConfig.Options["bridge"]; ok {
				driverConfig.Bridge = &types.BridgeConfig{
					Name: bridgeName,
				}
			}
		}

		netSpec := types.NetworkSpec{
			Name:         n.Network.Spec.Annotations.Name,
			Driver:       driver,
			DriverConfig: driverConfig,
		}

		networks = append(networks, types.NetworkAttachment{
			Network: types.Network{
				ID:   n.Network.ID,
				Spec: netSpec,
			},
			Addresses: n.Addresses,
		})
	}

	return &types.Task{
		ID:        c.task.ID,
		ServiceID: c.task.ServiceID,
		NodeID:    c.task.NodeID,
		Spec: types.TaskSpec{
			Runtime:   container,
			Resources: *resources,
		},
		Networks:    networks,
	}
}

// Helper functions

func hostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "localhost"
}

// kvmAvailable checks if /dev/kvm exists and is accessible
func (e *Executor) kvmAvailable() bool {
	_, err := os.Stat("/dev/kvm")
	return err == nil
}

// getCPUs returns the available CPU count in nanocpus (SwarmKit format)
func (e *Executor) getCPUs() int64 {
	totalCPUs := runtime.NumCPU()
	zerolog_log.Debug().Msgf("Detected %d total CPUs", totalCPUs)

	// Determine reserved CPUs
	reservedCPUs := e.config.ReservedCPUs
	if reservedCPUs == 0 {
		// Default: reserve 1 CPU or 10%, whichever is greater
		reservedByPercent := totalCPUs / 10
		if reservedByPercent < 1 {
			reservedByPercent = 1
		}
		reservedCPUs = reservedByPercent
	}

	// Ensure we don't reserve all CPUs
	if reservedCPUs >= totalCPUs {
		reservedCPUs = totalCPUs - 1
		if reservedCPUs < 1 {
			reservedCPUs = 1
		}
	}

	availableCPUs := totalCPUs - reservedCPUs
	zerolog_log.Info().Msgf("CPU resources: %d total, %d reserved, %d available for tasks",
		totalCPUs, reservedCPUs, availableCPUs)

	// Convert to nanocpus (SwarmKit format: 1 CPU = 1e9 nanocpus)
	return int64(availableCPUs) * 1e9
}

// getMemory returns available memory in bytes
func (e *Executor) getMemory() int64 {
	totalMemory := e.readMeminfo()
	if totalMemory == 0 {
		// Fallback to safe default if meminfo unavailable
		zerolog_log.Warn().Msg("Could not read /proc/meminfo, using default 8GB")
		totalMemory = 8 * 1024 * 1024 * 1024
	}

	// Determine reserved memory
	reservedMemoryMB := e.config.ReservedMemoryMB
	if reservedMemoryMB == 0 {
		// Default: reserve 512MB or 10%, whichever is greater
		reservedByPercent := (totalMemory / 1024 / 1024) / 10
		if reservedByPercent < 512 {
			reservedByPercent = 512
		}
		reservedMemoryMB = int(reservedByPercent)
	}

	reservedMemory := int64(reservedMemoryMB) * 1024 * 1024

	// Ensure we don't reserve all memory
	if reservedMemory >= totalMemory {
		reservedMemory = totalMemory / 10
		if reservedMemory < (512 * 1024 * 1024) {
			reservedMemory = 512 * 1024 * 1024
		}
	}

	availableMemory := totalMemory - reservedMemory
	zerolog_log.Info().Msgf("Memory resources: %d MB total, %d MB reserved, %d MB available for tasks",
		totalMemory/1024/1024, reservedMemory/1024/1024, availableMemory/1024/1024)

	return availableMemory
}

// readMeminfo reads memory information from /proc/meminfo
// Returns available memory in bytes, or 0 if unavailable
func (e *Executor) readMeminfo() int64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		zerolog_log.Error().Err(err).Msg("Failed to open /proc/meminfo")
		return 0
	}
	defer file.Close()

	var memTotal, memAvailable int64
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()

		// Simple parsing
		if len(line) > 10 {
			switch line[:9] {
			case "MemTotal:":
				memTotal = parseMeminfoLine(line)
			case "MemAvaila": // "MemAvailable:"
				memAvailable = parseMeminfoLine(line)
			}
		}
		if memTotal > 0 && memAvailable > 0 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		zerolog_log.Error().Err(err).Msg("Error reading /proc/meminfo")
		return 0
	}

	// If MemAvailable is not available, fall back to MemTotal * 0.9
	if memAvailable == 0 && memTotal > 0 {
		zerolog_log.Debug().Msg("MemAvailable not found, using 90% of MemTotal")
		memAvailable = int64(float64(memTotal) * 0.9)
	}

	// Convert kB to bytes
	return memAvailable * 1024
}

// parseMeminfoLine parses a /proc/meminfo line and returns the value in kB
// Input: "MemTotal:       16384000 kB"
// Output: 16384000
func parseMeminfoLine(line string) int64 {
	// Format: "Key:       value kB"
	// Find the first digit
	for i := 0; i < len(line); i++ {
		if line[i] >= '0' && line[i] <= '9' {
			// Extract the number
			j := i
			for j < len(line) && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			val, err := strconv.ParseInt(line[i:j], 10, 64)
			if err != nil {
				return 0
			}
			return val
		}
	}
	return 0
}
