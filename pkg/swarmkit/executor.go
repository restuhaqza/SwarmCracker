// Package swarmkit provides SwarmKit executor integration for SwarmCracker.
//
// This package implements the SwarmKit executor interface to run containers
// as Firecracker microVMs instead of traditional containers.
package swarmkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/moby/swarmkit/v2/api"
	swarmkit_exec "github.com/moby/swarmkit/v2/agent/exec"
	"github.com/moby/swarmkit/v2/log"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	zerolog_log "github.com/rs/zerolog/log"
)

// Executor implements SwarmKit's executor interface backed by SwarmCracker.
type Executor struct {
	config      *Config
	imagePrep   types.ImagePreparer
	networkMgr  types.NetworkManager
	vmmMgr      *VMMManager
	controllers map[string]*Controller
	executorMu  sync.RWMutex
}

// Config holds the SwarmKit integration configuration.
type Config struct {
	FirecrackerPath string `yaml:"firecracker_path"`
	KernelPath      string `yaml:"kernel_path"`
	RootfsDir       string `yaml:"rootfs_dir"`
	SocketDir       string `yaml:"socket_dir"`
	DefaultVCPUs    int    `yaml:"default_vcpus"`
	DefaultMemoryMB int    `yaml:"default_memory_mb"`
	BridgeName      string `yaml:"bridge_name"`
	Debug           bool   `yaml:"debug"`
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

	// Create image preparer
	imageCfg := &image.PreparerConfig{
		RootfsDir: config.RootfsDir,
	}
	imagePrep := image.NewImagePreparer(imageCfg)

	// Create network manager
	netCfg := types.NetworkConfig{
		BridgeName: config.BridgeName,
	}
	networkMgr := network.NewNetworkManager(netCfg)

	// Create VMM manager
	vmmMgr, err := NewVMMManager(config.FirecrackerPath, config.SocketDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create VMM manager: %w", err)
	}

	return &Executor{
		config:      config,
		imagePrep:  imagePrep,
		networkMgr: networkMgr,
		vmmMgr:     vmmMgr,
		controllers: make(map[string]*Controller),
	}, nil
}

// Describe returns the node description for this executor.
func (e *Executor) Describe(ctx context.Context) (*api.NodeDescription, error) {
	log.G(ctx).Debug("Describing executor")

	return &api.NodeDescription{
		Hostname: hostname(),
		Resources: &api.Resources{
			NanoCPUs:    getCPUs(),
			MemoryBytes: getMemory(),
			Generic: []*api.GenericResource{
				{
					Resource: &api.GenericResource_NamedResourceSpec{
						NamedResourceSpec: &api.NamedGenericResource{
							Kind:  "Firecracker",
							Value: "available",
						},
					},
				},
			},
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
	process    *os.Process
	socketPath string
	cancel     context.CancelFunc
	logger     zerolog.Logger
}

// NewController creates a new task controller.
func NewController(
	task *api.Task,
	config *Config,
	imagePrep types.ImagePreparer,
	networkMgr types.NetworkManager,
	vmmMgr *VMMManager,
) (*Controller, error) {
	trans, err := NewTaskTranslator(config.KernelPath)
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

	// Convert SwarmKit task to internal type
	task := c.convertTask()

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
	c.logger.Info().Msg("Shutting down task")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return nil
	}

	task := c.convertTask()

	// TODO: Implement graceful shutdown
	// For now, just terminate
	return c.vmmMgr.Stop(ctx, task)
}

// Terminate forcefully terminates the task.
func (c *Controller) Terminate(ctx context.Context) error {
	c.logger.Info().Msg("Terminating task")

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return nil
	}

	task := c.convertTask()

	return c.vmmMgr.Stop(ctx, task)
}

// Remove removes all task resources.
func (c *Controller) Remove(ctx context.Context) error {
	c.logger.Info().Msg("Removing task")

	c.mu.Lock()
	defer c.mu.Unlock()

	task := c.convertTask()

	// Cleanup network
	if err := c.networkMgr.CleanupNetwork(ctx, task); err != nil {
		c.logger.Error().Err(err).Msg("Failed to cleanup network")
	}

	// Remove VM
	if err := c.vmmMgr.Remove(ctx, task); err != nil {
		return fmt.Errorf("failed to remove VM: %w", err)
	}

	c.started = false
	c.prepared = false
	c.logger.Info().Msg("Task removed")
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

	return &types.Task{
		ID:        c.task.ID,
		ServiceID: c.task.ServiceID,
		NodeID:    c.task.NodeID,
		Spec: types.TaskSpec{
			Runtime:   container,
			Resources: *resources,
		},
	}
}

// Helper functions

func hostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "localhost"
}

func getCPUs() int64 {
	// TODO: Get actual CPU count
	return 4e9 // 4 CPUs
}

func getMemory() int64 {
	// TODO: Get actual memory
	return 8 * 1024 * 1024 * 1024 // 8 GB
}
