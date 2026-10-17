// Package swarmkit provides task translation for SwarmKit integration.
package swarmkit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/image"
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

	// A VM is a hard allocation: size it from the hard cap (Limits) when the
	// user declared one, else the reservation (Reservations), else the 1 vCPU /
	// 512 MiB defaults. A declared value is honoured as declared.
	vcpus, memoryMB := 1, 512
	if _, err := task.Spec.GetContainer(); err == nil {
		vcpus, memoryMB = resolveSize(&task.Spec.Resources)
	}

	// A prebuilt golden image may pin its own kernel through a profile.
	kernelPath, err := t.resolveKernelPath(task)
	if err != nil {
		return nil, err
	}

	// The memory-balloon target is a per-service annotation, and the device is
	// created before boot (it cannot be added to a running VM), so a target that
	// is malformed or not smaller than the VM must fail here rather than be
	// silently dropped.
	balloon, err := resolveBalloon(task, memoryMB)
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
		// The vsock device makes the guest agent reachable from the host. The
		// uds_path is derived from the API socket in configureVM, which is the
		// first place that knows the socket path.
		"vsock": map[string]interface{}{
			"guest_cid": agent.GuestCID,
			"vsock_id":  "vsock0",
		},
	}

	// Only a task that requested a balloon gets the device; an unconfigured task
	// produces exactly the map it did before.
	if balloon != nil {
		config["balloon"] = map[string]interface{}{
			"amount_mib":               balloon.AmountMib,
			"deflate_on_oom":           balloon.DeflateOnOOM,
			"stats_polling_interval_s": 0,
		}
	}

	return config, nil
}

// resolveSize returns the vCPU count and memory (MiB) a VM should be booted
// with: the hard cap (Limits) when the user declared one, else the soft
// reservation (Reservations), else the 1 vCPU / 512 MiB defaults. A declared
// value is returned as declared — there is no silent floor, so an explicit
// 256 MiB request boots a 256 MiB VM.
func resolveSize(res *types.ResourceRequirements) (vcpus int, memoryMB int) {
	return types.ResolveVMSize(res, 1, 512)
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
	if container, err := task.Spec.GetContainer(); err == nil {
		args = append(args, guestOverrideBootArgs(container, task)...)
	}
	return strings.Join(args, " ")
}

// guestOverrideBootArgs returns kernel boot arguments that the guest init
// wrapper reads from /proc/cmdline to apply per-task overrides (--hostname,
// --dns). The CLI validates these values (hostname syntax, IP addresses) so they
// never contain spaces.
//
// The resolver defaults to the task's bridge gateway (published by the network
// manager as AnnotationResolver) so a guest can resolve service names through
// the node-local dnsmasq; an explicit --dns always wins.
func guestOverrideBootArgs(container *types.Container, task *types.Task) []string {
	if container == nil {
		return nil
	}
	var args []string
	if h := strings.TrimSpace(container.Hostname); h != "" {
		args = append(args, "sc.hostname="+h)
	}
	switch {
	case len(container.DNS) > 0:
		args = append(args, "sc.dns="+strings.Join(container.DNS, ","))
	case task != nil:
		if r := strings.TrimSpace(task.Annotations[types.AnnotationResolver]); r != "" {
			args = append(args, "sc.dns="+r)
		}
	}
	return args
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

// networkBootArgs returns one kernel ip= argument per attached network that has
// a static address, or nil when none is assigned. The guest configures every
// NIC from these arguments, so a service attached to several networks gets an
// address on each interface (eth0, eth1, ...).
//
// Only the first configured interface gets a gateway (a default route);
// additional interfaces carry their address/mask only, so their per-network
// gateways do not compete for the default route.
func (t *taskTranslatorImpl) networkBootArgs(task *types.Task) []string {
	args := make([]string, 0, len(task.Networks))
	gatewayAssigned := false
	for i := range task.Networks {
		att := task.Networks[i]
		if len(att.Addresses) == 0 {
			continue
		}

		// Parse IP from Addresses (format: "192.168.127.2/24").
		addr := att.Addresses[0]
		ipPart := addr
		if idx := strings.Index(addr, "/"); idx > 0 {
			ipPart = addr[:idx]
		}

		mask := "255.255.255.0"
		// Use the network's mask when it is not a /24.
		if subnet := att.Network.Spec.Subnet; subnet != "" {
			if _, ipNet, err := net.ParseCIDR(subnet); err == nil {
				mask = net.IP(ipNet.Mask).String()
			}
		}

		gw := ""
		if !gatewayAssigned {
			// Gateway is the network's own gateway for a user-defined network,
			// otherwise the default bridge IP from config.
			gw = t.bridgeIP
			if g := strings.TrimSpace(att.Network.Spec.Gateway); g != "" {
				gw = g
			}
			if idx := strings.Index(gw, "/"); idx > 0 {
				gw = gw[:idx] // Remove CIDR if present
			}
			gatewayAssigned = true
		}

		// Kernel IP config format: ip=<ip>::<gw>:<netmask>::<iface>:off
		args = append(args, fmt.Sprintf("ip=%s::%s:%s::eth%d:off", ipPart, gw, mask, i))
	}
	return args
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

// resolveBalloon reads the memory-balloon target from a task's annotation and
// validates it against the VM's memory size. It returns nil when the task does
// not request a balloon, so an unconfigured VM's configuration is unchanged. A
// malformed or unsatisfiable target is a hard error, never ignored.
func resolveBalloon(task *types.Task, memSizeMib int) (*types.BalloonConfig, error) {
	raw := ""
	if task != nil {
		raw = strings.TrimSpace(task.Annotations[types.BalloonLabel])
	}
	if raw == "" {
		return nil, nil
	}

	amountMib, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid memory balloon target %q (%s): must be a positive integer MiB: %w", raw, types.BalloonLabel, err)
	}
	if amountMib <= 0 {
		return nil, fmt.Errorf("invalid memory balloon target %q (%s): must be a positive integer MiB", raw, types.BalloonLabel)
	}
	if amountMib >= int64(memSizeMib) {
		return nil, fmt.Errorf("invalid memory balloon target %d MiB (%s): must be less than the VM memory size %d MiB", amountMib, types.BalloonLabel, memSizeMib)
	}

	// deflate_on_oom is always true: the guest may reclaim ballooned memory when
	// it comes under pressure instead of being OOM-killed.
	return &types.BalloonConfig{AmountMib: amountMib, DeflateOnOOM: true}, nil
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
	// Default path: the task's own copy, under the per-task directory. The image
	// caches live one level up and are never a task's rootfs.
	return image.TaskRootfsPath("/var/lib/firecracker/rootfs", task.ID)
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
