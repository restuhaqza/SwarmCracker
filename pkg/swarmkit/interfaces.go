// Package swarmkit provides interfaces for VMM management.
// This file defines interfaces to enable mocking in tests without requiring real Firecracker processes.

package swarmkit

import (
	"context"
	"os/exec"

	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
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
	CheckVMAPIHealth(ctx context.Context, taskID string) bool
	// IsRunning checks if the VM process is still running
	IsRunning(taskID string) bool
	// GetRunningProcesses returns all running VM processes
	GetRunningProcesses() map[string]*exec.Cmd
	// RemoveProcess removes a process from the manager's tracking
	RemoveProcess(taskID string)
	// RemoveVMCgroup removes a task's cgroup, if any. Callers must only use it
	// once the task's VMM process is confirmed gone.
	RemoveVMCgroup(taskID string)
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

// RestoredInstanceStore is the node's map from a task to the microVM it inherited
// from a migration, as a teardown uses it: read to find which VM the task runs,
// deleted once that VM is confirmed gone.
//
// A task SwarmKit started from a migration artifact runs a VM whose Firecracker
// state names the source task's TAP device, vsock muxer and disk, so the record is
// the only thing that says which process belongs to it. *runtime.RestoredRegistry
// is the production implementation; the interface keeps a teardown testable
// without a filesystem.
type RestoredInstanceStore interface {
	// Get answers (record, true, nil) for a task this node restored,
	// (zero, false, nil) for one it never did, and an error for a record it holds
	// but cannot use.
	Get(taskID string) (scruntime.RestoredInstance, bool, error)
	// Delete forgets a task's record. Deleting a missing record is success: a
	// teardown can run twice.
	Delete(taskID string) error
}

// RestoredVMStopper stops the Firecracker process a restore started. That process
// is not the executor's — pkg/snapshot started it and holds the handle — so only
// the manager that owns it can signal it. *snapshot.Manager is the production
// implementation, and it answers snapshot.ErrNoRestoredVM for a task it tracks no
// process for, which a stop treats as the end state it asked for.
type RestoredVMStopper interface {
	StopRestored(taskID string) error
}
