package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/restuhaqza/swarmcracker/pkg/executor"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/translator"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// loadConfigWithOverrides loads configuration and applies CLI overrides.
func loadConfigWithOverrides(path string) (*config.Config, error) {
	if path == "" {
		path = config.GetDefaultConfigPath()
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("path", path).Msg("Config file not found, using defaults")
			cfg = &config.Config{}
			cfg.SetDefaults()
		} else {
			return nil, err
		}
	}

	if kernelPath != "" {
		cfg.Executor.KernelPath = kernelPath
	}
	if rootfsDir != "" {
		cfg.Executor.RootfsDir = rootfsDir
	}

	cfg.SetDefaults()
	return cfg, nil
}

// createExecutor creates a new Firecracker executor with all dependencies.
func createExecutor(cfg *config.Config) (*executor.FirecrackerExecutor, error) {
	execConfig := &executor.Config{
		KernelPath:      cfg.Executor.KernelPath,
		InitrdPath:      cfg.Executor.InitrdPath,
		RootfsDir:       cfg.Executor.RootfsDir,
		SocketDir:       cfg.Executor.SocketDir,
		DefaultVCPUs:    cfg.Executor.DefaultVCPUs,
		DefaultMemoryMB: cfg.Executor.DefaultMemoryMB,
		EnableJailer:    cfg.Executor.EnableJailer,
		Jailer: executor.JailerConfig{
			UID:           cfg.Executor.Jailer.UID,
			GID:           cfg.Executor.Jailer.GID,
			ChrootBaseDir: cfg.Executor.Jailer.ChrootBaseDir,
			NetNS:         cfg.Executor.Jailer.NetNS,
		},
		Network: types.NetworkConfig{
			BridgeName:       cfg.Network.BridgeName,
			EnableRateLimit:  cfg.Network.EnableRateLimit,
			MaxPacketsPerSec: cfg.Network.MaxPacketsPerSec,
			Subnet:           cfg.Network.Subnet,
			BridgeIP:         cfg.Network.BridgeIP,
			IPMode:           cfg.Network.IPMode,
			NATEnabled:       *cfg.Network.NATEnabled,
		},
	}

	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
		EnableJailer:    execConfig.EnableJailer,
	}

	imageConfig := &image.PreparerConfig{
		KernelPath:      execConfig.KernelPath,
		RootfsDir:       execConfig.RootfsDir,
		SocketDir:       execConfig.SocketDir,
		DefaultVCPUs:    execConfig.DefaultVCPUs,
		DefaultMemoryMB: execConfig.DefaultMemoryMB,
	}

	translatorConfig := &translator.Config{
		KernelPath:     execConfig.KernelPath,
		KernelProfiles: cfg.Executor.KernelProfiles,
		InitrdPath:     execConfig.InitrdPath,
		DefaultVCPUs:   execConfig.DefaultVCPUs,
		DefaultMemMB:   execConfig.DefaultMemoryMB,
		InitSystem:     "tini",
		NetworkConfig:  execConfig.Network,
	}

	vmmManager := lifecycle.NewVMMManager(vmmConfig)
	taskTranslator := translator.NewTaskTranslator(translatorConfig)
	imagePreparer := image.NewImagePreparer(imageConfig)
	networkMgr := network.NewNetworkManager(execConfig.Network)

	exec, err := executor.NewFirecrackerExecutor(
		execConfig, vmmManager, taskTranslator, imagePreparer, networkMgr,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %w", err)
	}
	return exec, nil
}

// createMockTask creates a mock task for CLI-driven VM creation.
func createMockTask(imageRef string, vcpus, memoryMB int, env []string) *types.Task {
	return &types.Task{
		// The VM ID names its sockets, state and private rootfs copy, so it has
		// to be unique: two `vm create` runs in the same second used to share one
		// (and with it, one rootfs).
		ID:        fmt.Sprintf("task-%d", time.Now().UnixNano()),
		ServiceID: "service-cli",
		NodeID:    "node-local",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   imageRef,
				Command: []string{},
				Args:    []string{},
				Env:     env,
				Mounts:  []types.Mount{},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    int64(vcpus * 1e9),
					MemoryBytes: int64(memoryMB * 1024 * 1024),
				},
			},
		},
		Status: types.TaskStatus{State: types.TaskStatePending},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{
					ID: "network-1",
					Spec: types.NetworkSpec{
						DriverConfig: &types.DriverConfig{
							Bridge: &types.BridgeConfig{Name: "swarm-br0"},
						},
					},
				},
			},
		},
	}
}

// setupLogging initializes the logging system.
func setupLogging(level string) {
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		logLevel = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(logLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
}

func goVersion() string {
	return fmt.Sprintf("%s (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// taskStateString converts TaskState to a human-readable string.
func taskStateString(state types.TaskState) string {
	switch state {
	case types.TaskStateNew:
		return "NEW"
	case types.TaskStatePending:
		return "PENDING"
	case types.TaskStateAssigned:
		return "ASSIGNED"
	case types.TaskStateAccepted:
		return "ACCEPTED"
	case types.TaskStatePreparing:
		return "PREPARING"
	case types.TaskStateStarting:
		return "STARTING"
	case types.TaskStateRunning:
		return "RUNNING"
	case types.TaskStateComplete:
		return "COMPLETE"
	case types.TaskStateFailed:
		return "FAILED"
	case types.TaskStateRejected:
		return "REJECTED"
	case types.TaskStateRemove:
		return "REMOVE"
	case types.TaskStateOrphaned:
		return "ORPHANED"
	default:
		return "UNKNOWN"
	}
}

// taskHealth derives a task's HEALTH value from the health-gated task state.
//
// SwarmKit's API carries no health field: api.ContainerStatus is only
// {ContainerID, PID, ExitCode} and api.TaskStatus has no health. The health
// gate (pkg/swarmkit) is therefore the signal: a healthchecked task stays in
// STARTING while the executor waits for the check to pass, reaches RUNNING only
// once healthy, and ends FAILED when the check fails at start or the task goes
// unhealthy while running. Tasks that declare no healthcheck report "-".
//
// This is the fallback for when the daemon's live health cannot be fetched; see
// taskHealthDisplay for the live-overlaid value.
func taskHealth(task *api.Task) string {
	if !taskDeclaresHealthcheck(task) {
		return "-"
	}

	switch task.Status.State {
	case api.TaskStateRunning:
		return "RUNNING"
	case api.TaskStateFailed, api.TaskStateRejected:
		return "FAILED"
	case api.TaskStateCompleted, api.TaskStateShutdown, api.TaskStateRemove, api.TaskStateOrphaned:
		return "-"
	default:
		// NEW, PENDING, ASSIGNED, ACCEPTED, PREPARING, READY, STARTING: the
		// health-gated window before the task is confirmed healthy.
		return "STARTING"
	}
}

// taskDeclaresHealthcheck reports whether a task's container declares a
// healthcheck. Only such a task can have a live health value.
func taskDeclaresHealthcheck(task *api.Task) bool {
	if task == nil {
		return false
	}
	container := task.Spec.GetContainer()
	return container != nil && container.Healthcheck != nil
}
