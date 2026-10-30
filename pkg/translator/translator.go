package translator

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	scruntime "github.com/restuhaqza/swarmcracker/pkg/runtime"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TaskTranslator converts SwarmKit tasks to Firecracker VM configurations.
type TaskTranslator struct {
	kernelPath     string
	kernelProfiles map[string]string
	initrdPath     string
	defaultVCPUs   int
	defaultMemMB   int
	initSystem     string // "none", "tini", "dumb-init"
	initPath       string // Path to init binary
	networkConfig  types.NetworkConfig
}

// Config holds translator configuration.
type Config struct {
	KernelPath string
	// KernelProfiles maps a golden-image kernel profile to a kernel path,
	// overriding KernelPath for prebuilt images that declare one.
	KernelProfiles map[string]string
	InitrdPath     string
	DefaultVCPUs   int
	DefaultMemMB   int
	InitSystem     string
	NetworkConfig  types.NetworkConfig
}

// NewTaskTranslator creates a new TaskTranslator.
func NewTaskTranslator(config interface{}) *TaskTranslator {
	// Defaults
	tt := &TaskTranslator{
		kernelPath:   "/usr/share/firecracker/vmlinux",
		initrdPath:   "",
		defaultVCPUs: 1,
		defaultMemMB: 512,
		initSystem:   "tini",
		initPath:     getInitPath("tini"),
	}

	// Try to extract from translator.Config (preferred)
	if cfg, ok := config.(*Config); ok {
		tt.kernelPath = cfg.KernelPath
		tt.kernelProfiles = cfg.KernelProfiles
		tt.initrdPath = cfg.InitrdPath
		tt.defaultVCPUs = cfg.DefaultVCPUs
		tt.defaultMemMB = cfg.DefaultMemMB
		tt.initSystem = cfg.InitSystem
		tt.initPath = getInitPath(cfg.InitSystem)
		tt.networkConfig = cfg.NetworkConfig
	} else if cfg, ok := config.(*lifecycle.ManagerConfig); ok {
		// Fallback for legacy calls (though we should migrate)
		tt.kernelPath = cfg.KernelPath
		tt.defaultVCPUs = cfg.DefaultVCPUs
		tt.defaultMemMB = cfg.DefaultMemoryMB
	}

	return tt
}

// getInitPath returns the init binary path for the given init system.
func getInitPath(initSystem string) string {
	switch initSystem {
	case "tini":
		return "/sbin/tini"
	case "dumb-init":
		return "/sbin/dumb-init"
	default:
		return ""
	}
}

// VMMConfig represents the Firecracker VMM configuration.
// JSON keys use kebab-case to match Firecracker API expectations.
type VMMConfig struct {
	BootSource        BootSourceConfig   `json:"boot-source"`
	MachineConfig     MachineConfig      `json:"machine-config"`
	NetworkInterfaces []NetworkInterface `json:"network-interfaces"`
	Drives            []Drive            `json:"drives"`
	Vsock             *VsockConfig       `json:"vsock,omitempty"`
	// Balloon is set only when the task requests a memory-balloon target; the
	// omitempty tag keeps an unconfigured VM's config byte-identical to before.
	Balloon map[string]interface{} `json:"balloon,omitempty"`
}

// BootSourceConfig specifies boot configuration.
type BootSourceConfig struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
	InitrdPath      string `json:"initrd_path,omitempty"`
}

// MachineConfig specifies VM resources.
type MachineConfig struct {
	VcpuCount  int  `json:"vcpu_count"`
	MemSizeMib int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
	// HugePages is Firecracker's huge_pages value ("2M") when the task asked
	// for hugepage-backed memory. omitempty keeps an unconfigured VM's JSON
	// byte-identical to before (Firecracker's other value, "None", is the
	// default).
	HugePages string `json:"huge_pages,omitempty"`
}

// NetworkInterface specifies network configuration.
type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	MacAddress  string `json:"mac_address,omitempty"`
	RxQueueSize int    `json:"rx_queue_size"`
	TxQueueSize int    `json:"tx_queue_size"`
}

// Drive specifies block device configuration.
type Drive struct {
	DriveID      string `json:"drive_id"`
	IsRootDevice bool   `json:"is_root_device"`
	PathOnHost   string `json:"path_on_host"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// VsockConfig specifies vsock configuration.
type VsockConfig struct {
	VsockID  string `json:"vsock_id"`
	GuestCID int    `json:"guest_cid"`
}

// Translate converts a SwarmKit task to a Firecracker VMM configuration JSON string.
func (tt *TaskTranslator) Translate(task *types.Task) (interface{}, error) {
	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	log.Info().
		Str("task_id", task.ID).
		Int("networks", len(task.Networks)).
		Msg("Translator received task")

	// Extract container from task runtime
	container, err := task.Spec.GetContainer()
	if err != nil {
		return nil, fmt.Errorf("task runtime is not a container: %w", err)
	}

	kernelPath, err := tt.resolveKernelPath(task)
	if err != nil {
		return nil, err
	}

	config := &VMMConfig{
		MachineConfig: MachineConfig{
			VcpuCount:  tt.defaultVCPUs,
			MemSizeMib: tt.defaultMemMB,
			Smt:        false,
		},
		BootSource: BootSourceConfig{
			KernelImagePath: kernelPath,
			BootArgs:        tt.buildBootArgs(task),
			InitrdPath:      tt.initrdPath,
		},
		NetworkInterfaces: []NetworkInterface{},
		Drives:            []Drive{},
		// The vsock device makes the guest agent reachable from the host. The
		// uds_path is derived from the API socket in the lifecycle manager,
		// which is the first place that knows the socket path.
		Vsock: &VsockConfig{
			VsockID:  "vsock0",
			GuestCID: agent.GuestCID,
		},
	}

	// Size the VM from the declared resources. Precedence is
	// Limits -> Reservations -> this translator's configured defaults, the same
	// rule the daemon translator (pkg/swarmkit) applies, so the two producers
	// cannot disagree about where the size comes from.
	tt.applyResources(config, &task.Spec.Resources)

	// Hugepage backing is a per-task annotation. Resolve it after sizing so an
	// odd memory size (which cannot be mapped with 2M pages) is rejected here
	// rather than by a Firecracker start failure. An unconfigured task leaves
	// HugePages empty, keeping the machine-config JSON unchanged.
	if err := applyHugepages(config, task); err != nil {
		return nil, err
	}

	// The memory-balloon target is a per-service annotation, and the device is
	// created before boot (it cannot be added to a running VM), so a target that
	// is malformed or not smaller than the VM must fail here rather than be
	// silently dropped.
	balloon, err := resolveBalloon(task, config.MachineConfig.MemSizeMib)
	if err != nil {
		return nil, err
	}
	if balloon != nil {
		config.Balloon = map[string]interface{}{
			"amount_mib":               balloon.AmountMib,
			"deflate_on_oom":           balloon.DeflateOnOOM,
			"stats_polling_interval_s": 0,
		}
	}

	// Add network interfaces
	for i, network := range task.Networks {
		log.Info().
			Str("task_id", task.ID).
			Int("index", i).
			Str("network_id", network.Network.ID).
			Int("addresses", len(network.Addresses)).
			Msg("Building network interface from task.Networks")
		iface := tt.buildNetworkInterface(network, i, task.ID)
		config.NetworkInterfaces = append(config.NetworkInterfaces, iface)
	}

	// Add root filesystem drive
	rootDrive, err := tt.buildRootDrive(container, task)
	if err != nil {
		return nil, fmt.Errorf("failed to build root drive: %w", err)
	}
	config.Drives = append(config.Drives, rootDrive)

	// Mounts are materialized into the rootfs by the image preparer (volumes
	// are mounted and bind mounts copied before boot), so no Firecracker drive
	// is added here. Emitting a drive whose path_on_host is a volume reference
	// ("volume://name") or a plain host path made Firecracker fail to open it.
	for i, mount := range container.Mounts {
		if err := validateMountPath(mount.Source); err != nil {
			return nil, fmt.Errorf("mount %d source invalid: %w", i, err)
		}
		if err := validateMountPath(mount.Target); err != nil {
			return nil, fmt.Errorf("mount %d target invalid: %w", i, err)
		}
	}

	// Return as map for direct consumption by vmm.go
	return tt.configToMap(config)
}

// resolveKernelPath picks the kernel image for a task. A prebuilt golden image
// may declare a kernel profile; it is mapped to a concrete path through the
// configured registry so a runtime kernel can be used per image. An unknown
// profile falls back to the default kernel (with a warning); a mapped profile
// that points at a missing file is a hard error, since silently booting the
// wrong kernel would break the image's expected features (e.g. macvlan).
func (tt *TaskTranslator) resolveKernelPath(task *types.Task) (string, error) {
	profile := ""
	if task != nil {
		profile = strings.TrimSpace(task.Annotations[types.AnnotationKernelProfile])
	}
	if profile == "" {
		return tt.kernelPath, nil
	}

	path, ok := tt.kernelProfiles[profile]
	if !ok || strings.TrimSpace(path) == "" {
		log.Warn().
			Str("task_id", task.ID).
			Str("kernel_profile", profile).
			Str("kernel_path", tt.kernelPath).
			Msg("Unknown kernel profile for prebuilt image; using default kernel")
		return tt.kernelPath, nil
	}

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("kernel profile %q maps to %s, which is not available: %w", profile, path, err)
	}
	return path, nil
}

// configToMap converts VMMConfig to map[string]interface{}.
func (tt *TaskTranslator) configToMap(config *VMMConfig) (map[string]interface{}, error) {
	// Use JSON marshal/unmarshal to convert struct to map
	bytes, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bytes, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return result, nil
}
func (tt *TaskTranslator) buildBootArgs(task *types.Task) string {
	// Golden images boot their own init (systemd/OpenRC) and supply extra
	// kernel arguments via the recipe; the OCI command is not run directly.
	if task.UsesPrebuiltRootfs() {
		return tt.buildPrebuiltBootArgs(task)
	}

	container, err := task.Spec.GetContainer()
	if err != nil {
		log.Warn().Str("task_id", task.ID).Msg("task runtime is not a Container, using default boot args")
		return "console=ttyS0 reboot=k panic=1 pci=off init=/init"
	}
	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"random.trust_cpu=on",
		"init=/init", // Use custom init script at root
	}

	args = append(args, tt.networkBootArgs(task)...)
	args = append(args, guestOverrideBootArgs(container)...)

	// Build container command line
	var containerCmd []string
	if len(container.Command) > 0 {
		containerCmd = append(containerCmd, container.Command...)
	}
	if len(container.Args) > 0 {
		containerCmd = append(containerCmd, container.Args...)
	}

	// If no command specified, use shell as fallback
	if len(containerCmd) == 0 {
		containerCmd = []string{"/bin/sh"}
	}

	// Wrap with init system if configured
	if tt.initSystem != "none" && tt.initPath != "" {
		args = append(args, "--")
		args = append(args, tt.buildInitArgs(containerCmd)...)
	} else {
		// No init system, run container command directly
		args = append(args, "--")
		args = append(args, containerCmd...)
	}

	return strings.Join(args, " ")
}

// buildPrebuiltBootArgs builds boot arguments for a prebuilt golden image: the
// guest boots its own init and the recipe's extra arguments are appended.
func (tt *TaskTranslator) buildPrebuiltBootArgs(task *types.Task) string {
	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"nomodules",
		"random.trust_cpu=on",
		"init=/sbin/init",
	}
	args = append(args, tt.networkBootArgs(task)...)

	if extra := strings.TrimSpace(task.Annotations[types.AnnotationBootArgs]); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}

	return strings.Join(args, " ")
}

// networkBootArgs returns the kernel ip= parameter and the MTU argument.
func (tt *TaskTranslator) networkBootArgs(task *types.Task) []string {
	ipArg := "ip=dhcp"
	mtu := 1500

	// Check for overlay network to adjust MTU
	if len(task.Networks) > 0 && task.Networks[0].Network.Spec.Driver == "overlay" {
		mtu = 1450
	}

	if len(task.Networks) > 0 && len(task.Networks[0].Addresses) > 0 {
		// Parse IP/Mask from "192.168.1.2/24"
		// Format: ip=<client-ip>:<server-ip>:<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>

		cidr := task.Networks[0].Addresses[0]
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			clientIP := ip.String()

			// Calculate netmask
			mask := net.IP(ipNet.Mask)
			netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])

			// Determine gateway
			gateway := ""
			if tt.networkConfig.BridgeIP != "" {
				// Use configured bridge IP as gateway
				gwIP, _, err := net.ParseCIDR(tt.networkConfig.BridgeIP)
				if err == nil {
					gateway = gwIP.String()
				}
			}

			// Fallback gateway logic if config missing
			if gateway == "" {
				// Assume .1
				ipParts := strings.Split(clientIP, ".")
				if len(ipParts) == 4 {
					gateway = fmt.Sprintf("%s.%s.%s.1", ipParts[0], ipParts[1], ipParts[2])
				}
			}

			// ip=<client-ip>:<server-ip>:<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
			// server-ip is empty (no NFS root)
			// hostname is empty (allow kernel to set or use task ID?)
			// device is eth0
			// autoconf is off
			ipArg = fmt.Sprintf("ip=%s::%s:%s::eth0:off", clientIP, gateway, netmask)
		}
	}

	// Pass MTU as a kernel argument (can be used by init scripts)
	return []string{ipArg, fmt.Sprintf("mtu=%d", mtu)}
}

// guestOverrideBootArgs returns kernel boot arguments that the guest init
// wrapper reads from /proc/cmdline to apply per-task overrides. The CLI
// validates these values (hostname syntax, IP addresses) so they never contain
// spaces.
func guestOverrideBootArgs(container *types.Container) []string {
	var args []string
	if container == nil {
		return args
	}
	if h := strings.TrimSpace(container.Hostname); h != "" {
		args = append(args, "sc.hostname="+h)
	}
	if len(container.DNS) > 0 {
		args = append(args, "sc.dns="+strings.Join(container.DNS, ","))
	}
	return args
}

// buildInitArgs builds init arguments wrapping the container command.
func (tt *TaskTranslator) buildInitArgs(containerCmd []string) []string {
	switch tt.initSystem {
	case "tini":
		// tini -- <command> <args...>
		args := make([]string, 0, 2+len(containerCmd))
		args = append(args, tt.initPath, "--")
		args = append(args, containerCmd...)
		return args
	case "dumb-init":
		// dumb-init <command> <args...>
		args := make([]string, 0, 1+len(containerCmd))
		args = append(args, tt.initPath)
		args = append(args, containerCmd...)
		return args
	default:
		// No init system
		return containerCmd
	}
}

// applyResources sizes the VM from the task's declared resources. It uses the
// same precedence as the daemon translator (pkg/swarmkit): the hard cap
// (Limits) when one is declared, else the soft reservation (Reservations), else
// this translator's configured defaults. A declared value is honoured as
// declared — there is no silent floor, so an explicit 256 MiB request boots a
// 256 MiB VM.
func (tt *TaskTranslator) applyResources(config *VMMConfig, res *types.ResourceRequirements) {
	vcpus, memoryMB := types.ResolveVMSize(res, tt.defaultVCPUs, tt.defaultMemMB)
	config.MachineConfig.VcpuCount = vcpus
	config.MachineConfig.MemSizeMib = memoryMB
}

// applyHugepages turns a task's hugepages annotation into the Firecracker
// machine-config value. Firecracker supports only 2M pages, so the value is
// "2M" or absent; a malformed request or a memory size that cannot be expressed
// in whole 2M pages is a hard error rather than a silent fallback to 4K memory.
func applyHugepages(config *VMMConfig, task *types.Task) error {
	raw := ""
	if task != nil {
		raw = task.Annotations[types.HugepagesLabel]
	}

	// The request is resolved by types.ResolveHugepages, the same helper the
	// daemon's translator uses, so both start paths emit one identical rule.
	value, err := types.ResolveHugepages(raw, config.MachineConfig.MemSizeMib)
	if err != nil {
		return err
	}

	config.MachineConfig.HugePages = value
	return nil
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

// buildNetworkInterface creates a network interface configuration.
func (tt *TaskTranslator) buildNetworkInterface(_ types.NetworkAttachment, index int, taskID string) NetworkInterface {
	ifaceID := fmt.Sprintf("eth%d", index)

	return NetworkInterface{
		IfaceID: ifaceID,
		// The name must match the device the network manager creates and the
		// one a migration restore recreates, so the rule lives in pkg/runtime.
		HostDevName: scruntime.TapName(taskID, index),
		MacAddress:  "", // Will be generated by Firecracker
		RxQueueSize: 256,
		TxQueueSize: 256,
	}
}

// buildRootDrive creates the root filesystem drive configuration.
func (tt *TaskTranslator) buildRootDrive(_ *types.Container, task *types.Task) (Drive, error) {
	// Get rootfs path from task annotations (set by image preparer)
	rootfsPath, ok := task.Annotations["rootfs"]
	if !ok {
		return Drive{}, fmt.Errorf("rootfs not found in task annotations")
	}

	return Drive{
		DriveID:      "rootfs",
		IsRootDevice: true,
		PathOnHost:   rootfsPath,
		IsReadOnly:   false,
	}, nil
}

// validateMountPath validates that a mount path is safe for use and does not
// contain path traversal or null byte injection vectors.
func validateMountPath(path string) error {
	if path == "" {
		return fmt.Errorf("mount path cannot be empty")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("mount path contains parent directory reference: %q", path)
	}
	if strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("mount path contains null byte: %q", path)
	}
	return nil
}
