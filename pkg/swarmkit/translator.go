// Package swarmkit provides task translation for SwarmKit integration.
package swarmkit

import (
	"fmt"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// taskTranslatorImpl translates SwarmKit tasks to Firecracker VM configs.
type taskTranslatorImpl struct {
	kernelPath string
}

// NewTaskTranslator creates a new task translator.
func NewTaskTranslator(kernelPath string) (types.TaskTranslator, error) {
	if kernelPath == "" {
		return nil, fmt.Errorf("kernel path cannot be empty")
	}

	return &taskTranslatorImpl{
		kernelPath: kernelPath,
	}, nil
}

// Translate converts a task to Firecracker VM configuration.
func (t *taskTranslatorImpl) Translate(task *types.Task) (interface{}, error) {
	// For now, return a simple config structure
	// This will be expanded to use the full translator package

	vcpus := 1
	memoryMB := 512

	// Try to get resource specifications from container
	if container, ok := task.Spec.Runtime.(*types.Container); ok {
		// Resource-based sizing
		if task.Spec.Resources.Reservations != nil {
			res := task.Spec.Resources.Reservations
			// Convert nanoCPUs to vCPUs (1 vCPU = 1e9 nanoCPUs)
			if res.NanoCPUs > 0 {
				vcpus = int(res.NanoCPUs / 1e9)
				if vcpus < 1 {
					vcpus = 1
				}
			}
			// Convert bytes to MB
			if res.MemoryBytes > 0 {
				memoryMB = int(res.MemoryBytes / (1024 * 1024))
				if memoryMB < 512 {
					memoryMB = 512
				}
			}
		}

		_ = container // Use container config in future
	}

	config := map[string]interface{}{
		"boot-source": map[string]interface{}{
			"kernel_image_path": t.kernelPath,
			"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off nomodules",
		},
		"drives": []map[string]interface{}{
			{
				"drive_id":       task.ID,
				"path_on_host":   getRootfsPath(task),
				"is_root_device": true,
				"is_read_only":   false,
			},
		},
		"machine-config": map[string]interface{}{
			"vcpu_count":   vcpus,
			"mem_size_mib": memoryMB,
			"ht_enabled":   false,
		},
		"network-interfaces": []map[string]interface{}{},
	}

	return config, nil
}

// getRootfsPath returns the rootfs path for a task.
func getRootfsPath(task *types.Task) string {
	if rootfs, ok := task.Annotations["rootfs"]; ok {
		return rootfs
	}
	// Default path
	return "/var/lib/firecracker/rootfs/" + task.ID + ".ext4"
}
