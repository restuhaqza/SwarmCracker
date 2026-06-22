package main

import (
	"fmt"
	"os"
	"time"

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
		KernelPath:    execConfig.KernelPath,
		InitrdPath:    execConfig.InitrdPath,
		DefaultVCPUs:  execConfig.DefaultVCPUs,
		DefaultMemMB:  execConfig.DefaultMemoryMB,
		InitSystem:    "tini",
		NetworkConfig: execConfig.Network,
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
		ID:        fmt.Sprintf("task-%d", time.Now().Unix()),
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

func goVersion() string  { return fmt.Sprintf("%s (%s/%s)", getGoVersion(), getGOOS(), getGOARCH()) }
func getGoVersion() string { return "1.21" }
func getGOOS() string      { return "linux" }
func getGOARCH() string    { return "amd64" }

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
