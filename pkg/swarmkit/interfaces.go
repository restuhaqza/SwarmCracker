// Package swarmkit provides interfaces for VMM management.
// This file defines interfaces to enable mocking in tests without requiring real Firecracker processes.

package swarmkit

import (
	"context"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// VMMManagerInterface defines the interface for VMM management operations.
type VMMManagerInterface interface {
	// Start starts a Firecracker VM for the given task
	Start(ctx context.Context, task *types.Task, config interface{}) error
	// Stop gracefully stops a VM
	Stop(ctx context.Context, task *types.Task) error
	// ForceStop forcefully stops a VM
	ForceStop(ctx context.Context, task *types.Task) error
	// Wait waits for a VM to exit and returns its status
	Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error)
	// Remove removes VM resources
	Remove(ctx context.Context, task *types.Task) error
	// GetPID returns the process ID for a task
	GetPID(taskID string) int
	// CheckVMAPIHealth checks if the VM API is responding
	CheckVMAPIHealth(taskID string) bool
	// IsRunning checks if the VM process is still running
	IsRunning(taskID string) bool
}

// ImagePreparerInterface defines the interface for image preparation.
type ImagePreparerInterface interface {
	Prepare(ctx context.Context, task *types.Task) error
	Cleanup(ctx context.Context, taskID string) error
}

// NetworkManagerInterface defines the interface for network management.
type NetworkManagerInterface interface {
	PrepareNetwork(ctx context.Context, task *types.Task) error
	CleanupNetwork(ctx context.Context, task *types.Task) error
}

// VolumeManagerInterface defines the interface for volume management.
type VolumeManagerInterface interface {
	PrepareVolumes(ctx context.Context, task *types.Task) error
	CleanupVolumes(ctx context.Context, task *types.Task) error
}

// SecretManagerInterface defines the interface for secret/config injection.
type SecretManagerInterface interface {
	InjectSecrets(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error
	InjectConfigs(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error
}