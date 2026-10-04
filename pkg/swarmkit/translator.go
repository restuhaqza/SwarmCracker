// Package swarmkit provides task translation for SwarmKit integration.
package swarmkit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// taskTranslatorImpl translates SwarmKit tasks to Firecracker VM configs.
type taskTranslatorImpl struct {
	kernelPath     string
	bridgeIP       string
	kernelProfiles map[string]string
}

// NewTaskTranslator creates a new task translator.
func NewTaskTranslator(kernelPath, bridgeIP string) (types.TaskTranslator, error) {
	return newTaskTranslator(kernelPath, bridgeIP, nil)
}

// NewTaskTranslatorWithProfiles creates a task translator that can map a
// golden image's kernel profile to a concrete kernel path.
func NewTaskTranslatorWithProfiles(kernelPath, bridgeIP string, kernelProfiles map[string]string) (types.TaskTranslator, error) {
	return newTaskTranslator(kernelPath, bridgeIP, kernelProfiles)
}

func newTaskTranslator(kernelPath, bridgeIP string, kernelProfiles map[string]string) (types.TaskTranslator, error) {
	if kernelPath == "" {
		return nil, fmt.Errorf("kernel path cannot be empty")
	}

	return &taskTranslatorImpl{
		kernelPath:     kernelPath,
		bridgeIP:       bridgeIP,
		kernelProfiles: kernelProfiles,
	}, nil
}

// Translate converts a task to Firecracker VM configuration.
func (t *taskTranslatorImpl) Translate(task *types.Task) (interface{}, error) {
	// Validate task ID to prevent path traversal and injection
	if err := validateTaskID(task.ID); err != nil {
		return nil, fmt.Errorf("invalid task ID: %w", err)
	}

	// For now, return a simple config structure
	// This will be expanded to use the full translator package

	vcpus := 1
	memoryMB := 512

	// Try to get resource specifications if task has a container runtime
	if _, err := task.Spec.GetContainer(); err == nil {
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
	}

	// A prebuilt golden image may pin its own kernel through a profile.
	kernelPath, err := t.resolveKernelPath(task)
	if err != nil {
		return nil, err
	}

	// Build boot args with network config if available
	bootArgs := t.buildBootArgs(task)

	config := map[string]interface{}{
		"boot-source": map[string]interface{}{
			"kernel_image_path": kernelPath,
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
	// A prebuilt golden image boots its own init (systemd/OpenRC) and carries
	// extra kernel arguments; the container init wrapper does not apply.
	if task.UsesPrebuiltRootfs() {
		return t.buildPrebuiltBootArgs(task)
	}

	// Use /sbin/init (wrapper that calls tini with entrypoint)
	// The preparer creates /sbin/init as a wrapper script
	netArgs := t.networkBootArgs(task)
	args := make([]string, 0, 6+len(netArgs))
	args = append(args, "console=ttyS0", "reboot=k", "panic=1", "pci=off", "nomodules", "init=/sbin/init")
	args = append(args, netArgs...)
	return strings.Join(args, " ")
}

// buildPrebuiltBootArgs builds boot arguments for a prebuilt golden image: the
// guest boots its own init and the recipe's extra arguments are appended.
func (t *taskTranslatorImpl) buildPrebuiltBootArgs(task *types.Task) string {
	netArgs := t.networkBootArgs(task)
	args := make([]string, 0, 8+len(netArgs))
	args = append(args, "console=ttyS0", "reboot=k", "panic=1", "pci=off", "nomodules", "random.trust_cpu=on", "init=/sbin/init")
	args = append(args, netArgs...)
	if extra := strings.TrimSpace(task.Annotations[types.AnnotationBootArgs]); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	return strings.Join(args, " ")
}

// networkBootArgs returns the kernel ip= argument for the task's first network
// attachment, or nil when no static address is assigned.
func (t *taskTranslatorImpl) networkBootArgs(task *types.Task) []string {
	if len(task.Networks) == 0 || len(task.Networks[0].Addresses) == 0 {
		return nil
	}

	// Parse IP from Addresses (format: "192.168.127.2/24")
	addr := task.Networks[0].Addresses[0]
	ipPart := addr
	if idx := strings.Index(addr, "/"); idx > 0 {
		ipPart = addr[:idx]
	}

	// Kernel IP config format: ip=<ip>::<gw>:<netmask>::<iface>:off
	// Gateway is bridge IP from config
	gw := t.bridgeIP
	if idx := strings.Index(gw, "/"); idx > 0 {
		gw = gw[:idx] // Remove CIDR if present
	}
	mask := "255.255.255.0"

	return []string{fmt.Sprintf("ip=%s::%s:%s::eth0:off", ipPart, gw, mask)}
}

// resolveKernelPath picks the kernel image for a task. A prebuilt golden image
// may declare a kernel profile; it is mapped to a concrete path through the
// configured registry so a runtime kernel can be used per image. An unknown
// profile falls back to the default kernel (with a warning); a mapped profile
// that points at a missing file is a hard error, since silently booting the
// wrong kernel would break the image's expected features (e.g. macvlan).
func (t *taskTranslatorImpl) resolveKernelPath(task *types.Task) (string, error) {
	profile := ""
	if task != nil {
		profile = strings.TrimSpace(task.Annotations[types.AnnotationKernelProfile])
	}
	if profile == "" {
		return t.kernelPath, nil
	}

	path, ok := t.kernelProfiles[profile]
	if !ok || strings.TrimSpace(path) == "" {
		log.Warn().
			Str("task_id", task.ID).
			Str("kernel_profile", profile).
			Str("kernel_path", t.kernelPath).
			Msg("Unknown kernel profile for prebuilt image; using default kernel")
		return t.kernelPath, nil
	}

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("kernel profile %q maps to %s, which is not available: %w", profile, path, err)
	}
	return path, nil
}

// buildNetworkInterfaces creates network interface configs from task attachments.
func (t *taskTranslatorImpl) buildNetworkInterfaces(task *types.Task) []map[string]interface{} {
	interfaces := make([]map[string]interface{}, 0, len(task.Networks))

	// Generate TAP name hash (must match network manager logic)
	hash := sha256.Sum256([]byte(task.ID))
	hashStr := hex.EncodeToString(hash[:])

	for i := range task.Networks {
		ifaceID := fmt.Sprintf("eth%d", i)
		tapName := fmt.Sprintf("tap-%s-%d", hashStr[:8], i)

		iface := map[string]interface{}{
			"iface_id":      ifaceID,
			"host_dev_name": tapName,
			"guest_mac":     generateMAC(task.ID, i),
		}

		interfaces = append(interfaces, iface)
	}

	return interfaces
}

// generateMAC creates a deterministic, locally-administered unicast MAC for a
// task's network interface.
//
// The MAC must be unique per task and per interface. Every microVM across every
// node shares a single L2 segment over the VXLAN overlay, so deriving the MAC
// only from the interface index (as before) made every VM's first NIC identical
// (AA:FC:00:00:00:00). Duplicate MACs in a shared bridge domain cause the Linux
// bridge FDB to flap between the local TAP and the VXLAN peer, which breaks
// cross-host traffic. Hashing the task ID together with the interface index
// keeps MACs globally unique, deterministic, and stable across restarts.
func generateMAC(taskID string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", taskID, index)))
	return fmt.Sprintf("AA:FC:%02X:%02X:%02X:%02X", sum[0], sum[1], sum[2], sum[3])
}

// getRootfsPath returns the rootfs path for a task.
func getRootfsPath(task *types.Task) string {
	if rootfs, ok := task.Annotations["rootfs"]; ok {
		return rootfs
	}
	// Default path
	return "/var/lib/firecracker/rootfs/" + task.ID + ".ext4"
}

// validateTaskID validates that a task ID is safe for use in filesystem paths
// and does not contain path traversal or injection characters.
func validateTaskID(id string) error {
	if id == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	if len(id) > 255 {
		return fmt.Errorf("task ID exceeds 255 characters")
	}
	if strings.Contains(id, "/") {
		return fmt.Errorf("task ID contains path separator")
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("task ID contains parent directory reference")
	}
	if strings.ContainsRune(id, '\x00') {
		return fmt.Errorf("task ID contains null byte")
	}
	if strings.Contains(id, "\\") {
		return fmt.Errorf("task ID contains backslash")
	}
	return nil
}
