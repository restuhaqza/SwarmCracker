// Package swarmkit provides task translation for SwarmKit integration.
package swarmkit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

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

	// Build boot args with network config if available
	bootArgs := t.buildBootArgs(task)

	config := map[string]interface{}{
		"boot-source": map[string]interface{}{
			"kernel_image_path": t.kernelPath,
			"boot_args":         bootArgs,
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
			"smt":          false,
		},
		"network-interfaces": t.buildNetworkInterfaces(task),
	}

	return config, nil
}

// buildBootArgs builds kernel boot arguments with network config.
func (t *taskTranslatorImpl) buildBootArgs(task *types.Task) string {
	baseArgs := "console=ttyS0 reboot=k panic=1 pci=off nomodules init=/sbin/init"

	// Add network config if task has IP addresses
	if len(task.Networks) > 0 && len(task.Networks[0].Addresses) > 0 {
		// Parse IP from Addresses (format: "192.168.127.2/24")
		addr := task.Networks[0].Addresses[0]
		ipPart := addr
		if idx := strings.Index(addr, "/"); idx > 0 {
			ipPart = addr[:idx]
		}

		// Kernel IP config format: ip=<ip>::<gw>:<netmask>::<iface>:off
		// Gateway is bridge IP (192.168.127.1)
		gw := "192.168.127.1"
		mask := "255.255.255.0"

		ipArg := fmt.Sprintf("ip=%s::%s:%s::eth0:off", ipPart, gw, mask)
		baseArgs = baseArgs + " " + ipArg
	}

	return baseArgs
}

// buildNetworkInterfaces creates network interface configs from task attachments.
func (t *taskTranslatorImpl) buildNetworkInterfaces(task *types.Task) []map[string]interface{} {
	interfaces := []map[string]interface{}{}

	// Generate TAP name hash (must match network manager logic)
	hash := sha256.Sum256([]byte(task.ID))
	hashStr := hex.EncodeToString(hash[:])

	for i := range task.Networks {
		ifaceID := fmt.Sprintf("eth%d", i)
		tapName := fmt.Sprintf("tap-%s-%d", hashStr[:8], i)

		iface := map[string]interface{}{
			"iface_id":      ifaceID,
			"host_dev_name": tapName,
			"guest_mac":     generateMAC(i),
		}

		interfaces = append(interfaces, iface)
	}

	return interfaces
}

// generateMAC creates a MAC address for a network interface.
func generateMAC(index int) string {
	// Generate a unique MAC address based on index
	// Format: AA:FC:XX:XX:XX:XX where XX is derived from index
	return fmt.Sprintf("AA:FC:%02X:%02X:%02X:%02X",
		(byte(index)>>4)&0xFF,
		byte(index)&0xFF,
		(byte(index)>>2)&0xFF,
		(byte(index)*3)&0xFF)
}

// getRootfsPath returns the rootfs path for a task.
func getRootfsPath(task *types.Task) string {
	if rootfs, ok := task.Annotations["rootfs"]; ok {
		return rootfs
	}
	// Default path
	return "/var/lib/firecracker/rootfs/" + task.ID + ".ext4"
}
