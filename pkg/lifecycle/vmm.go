// Package lifecycle provides VM lifecycle management for Firecracker.
// This is a stub implementation for development.
package lifecycle

import (
	"context"
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// VMMManager manages Firecracker VM lifecycle.
type VMMManager struct {
	config interface{} // Can be *executor.Config when needed
}

// NewVMMManager creates a new VMMManager.
func NewVMMManager(config interface{}) types.VMMManager {
	return &VMMManager{
		config: config,
	}
}

// Start starts a VM for the given task.
func (vm *VMMManager) Start(ctx context.Context, task *types.Task, config interface{}) error {
	// TODO: Implement actual VM startup
	return fmt.Errorf("not implemented: VMMManager.Start")
}

// Stop stops a running VM.
func (vm *VMMManager) Stop(ctx context.Context, task *types.Task) error {
	// TODO: Implement actual VM stop
	return fmt.Errorf("not implemented: VMMManager.Stop")
}

// Wait waits for a VM to exit.
func (vm *VMMManager) Wait(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	// TODO: Implement actual VM wait
	return &types.TaskStatus{
		State: types.TaskState_RUNNING,
	}, nil
}

// Describe describes the current state of a VM.
func (vm *VMMManager) Describe(ctx context.Context, task *types.Task) (*types.TaskStatus, error) {
	// TODO: Implement actual VM status check
	return &types.TaskStatus{
		State:   types.TaskState_ORPHANED,
		Message: "VM not found",
	}, nil
}

// Remove removes a VM and cleans up resources.
func (vm *VMMManager) Remove(ctx context.Context, task *types.Task) error {
	// TODO: Implement actual VM cleanup
	return fmt.Errorf("not implemented: VMMManager.Remove")
}
