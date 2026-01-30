// Package image prepares OCI images as root filesystems for Firecracker VMs.
// This is a stub implementation for development.
package image

import (
	"context"
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// ImagePreparer prepares OCI images as root filesystems.
type ImagePreparer struct {
	config interface{} // Can be *executor.Config when needed
}

// NewImagePreparer creates a new ImagePreparer.
func NewImagePreparer(config interface{}) types.ImagePreparer {
	return &ImagePreparer{
		config: config,
	}
}

// Prepare prepares an OCI image for the given task.
func (ip *ImagePreparer) Prepare(ctx context.Context, task *types.Task) error {
	// TODO: Implement actual image preparation
	// Steps:
	// 1. Pull OCI image
	// 2. Extract to root filesystem
	// 3. Create ext4 image
	// 4. Store path in task.Annotations["rootfs"]
	return fmt.Errorf("not implemented: ImagePreparer.Prepare")
}
