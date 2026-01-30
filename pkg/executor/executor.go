package executor

import (
	"context"
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// FirecrackerExecutor implements the SwarmKit executor interface
// to run tasks as Firecracker microVMs.
type FirecrackerExecutor struct {
	config      *Config
	vmmManager  types.VMMManager
	translator  types.TaskTranslator
	imagePrep   types.ImagePreparer
	networkMgr  types.NetworkManager
	events      chan Event
}

// Config holds the executor configuration.
type Config struct {
	// Kernel path for Firecracker VMs
	KernelPath string `yaml:"kernel_path"`

	// Initrd path (optional)
	InitrdPath string `yaml:"initrd_path"`

	// Directory for root filesystems
	RootfsDir string `yaml:"rootfs_dir"`

	// Directory for Firecracker API sockets
	SocketDir string `yaml:"socket_dir"`

	// Default vCPUs per VM
	DefaultVCPUs int `yaml:"default_vcpus"`

	// Default memory in MB per VM
	DefaultMemoryMB int `yaml:"default_memory_mb"`

	// Enable jailer for additional security
	EnableJailer bool `yaml:"enable_jailer"`

	// Jailer configuration
	Jailer JailerConfig `yaml:"jailer"`

	// Network configuration
	Network types.NetworkConfig `yaml:"network"`
}

// JailerConfig holds jailer-specific settings.
type JailerConfig struct {
	UID           int    `yaml:"uid"`
	GID           int    `yaml:"gid"`
	ChrootBaseDir string `yaml:"chroot_base_dir"`
	NetNS         string `yaml:"netns"`
}

// Event represents an executor event.
type Event struct {
	Task    *types.Task
	Message string
	Err     error
}

// NewFirecrackerExecutor creates a new Firecracker executor with injected dependencies.
func NewFirecrackerExecutor(
	config *Config,
	vmmManager types.VMMManager,
	translator types.TaskTranslator,
	imagePrep types.ImagePreparer,
	networkMgr types.NetworkManager,
) (*FirecrackerExecutor, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	// Set defaults
	if config.DefaultVCPUs == 0 {
		config.DefaultVCPUs = 1
	}
	if config.DefaultMemoryMB == 0 {
		config.DefaultMemoryMB = 512
	}
	if config.SocketDir == "" {
		config.SocketDir = "/var/run/firecracker"
	}
	if config.RootfsDir == "" {
		config.RootfsDir = "/var/lib/firecracker/rootfs"
	}

	return &FirecrackerExecutor{
		config:     config,
		vmmManager: vmmManager,
		translator: translator,
		imagePrep:  imagePrep,
		networkMgr: networkMgr,
		events:     make(chan Event, 100),
	}, nil
}

// Prepare gets a task ready to execute.
func (e *FirecrackerExecutor) Prepare(ctx context.Context, t *types.Task) error {
	log.Info().
		Str("task_id", t.ID).
		Str("service_id", t.ServiceID).
		Msg("Preparing task")

	// 1. Prepare container image (convert to rootfs)
	if err := e.imagePrep.Prepare(ctx, t); err != nil {
		return fmt.Errorf("image preparation failed: %w", err)
	}

	// 2. Prepare network interfaces
	if err := e.networkMgr.PrepareNetwork(ctx, t); err != nil {
		return fmt.Errorf("network preparation failed: %w", err)
	}

	log.Info().
		Str("task_id", t.ID).
		Msg("Task preparation completed")

	return nil
}

// Start begins execution of a task.
func (e *FirecrackerExecutor) Start(ctx context.Context, t *types.Task) error {
	log.Info().
		Str("task_id", t.ID).
		Msg("Starting task")

	// 1. Translate task to VMM config
	config, err := e.translator.Translate(t)
	if err != nil {
		return fmt.Errorf("task translation failed: %w", err)
	}

	// 2. Start the VM
	if err := e.vmmManager.Start(ctx, t, config); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	// 3. Send event
	e.events <- Event{
		Task:    t,
		Message: "VM started successfully",
	}

	log.Info().
		Str("task_id", t.ID).
		Msg("Task started successfully")

	return nil
}

// Wait blocks until the task exits and returns the exit status.
func (e *FirecrackerExecutor) Wait(ctx context.Context, t *types.Task) (*types.TaskStatus, error) {
	return e.vmmManager.Wait(ctx, t)
}

// Stop terminates a running task.
func (e *FirecrackerExecutor) Stop(ctx context.Context, t *types.Task) error {
	log.Info().
		Str("task_id", t.ID).
		Msg("Stopping task")

	return e.vmmManager.Stop(ctx, t)
}

// Remove cleans up any resources associated with the task.
func (e *FirecrackerExecutor) Remove(ctx context.Context, t *types.Task) error {
	log.Info().
		Str("task_id", t.ID).
		Msg("Removing task")

	// 1. Stop VM if running
	e.vmmManager.Stop(ctx, t)

	// 2. Clean up network
	if err := e.networkMgr.CleanupNetwork(ctx, t); err != nil {
		log.Error().Err(err).Msg("Failed to cleanup network")
	}

	// 3. Clean up VM resources
	if err := e.vmmManager.Remove(ctx, t); err != nil {
		return fmt.Errorf("failed to remove VM: %w", err)
	}

	log.Info().
		Str("task_id", t.ID).
		Msg("Task removed successfully")

	return nil
}

// Describe returns the current state of a task.
func (e *FirecrackerExecutor) Describe(ctx context.Context, t *types.Task) (*types.TaskStatus, error) {
	return e.vmmManager.Describe(ctx, t)
}

// Events returns a channel of executor events.
func (e *FirecrackerExecutor) Events(ctx context.Context) (<-chan Event, error) {
	return e.events, nil
}

// Close cleans up executor resources.
func (e *FirecrackerExecutor) Close() error {
	close(e.events)
	return nil
}
