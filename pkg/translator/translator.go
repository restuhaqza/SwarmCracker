package translator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TaskTranslator converts SwarmKit tasks to Firecracker VM configurations.
type TaskTranslator struct {
	kernelPath    string
	initrdPath    string
	defaultVCPUs  int
	defaultMemMB  int
	initSystem    string // "none", "tini", "dumb-init"
	initPath      string // Path to init binary
	networkConfig types.NetworkConfig
}

// Config holds translator configuration.
type Config struct {
	KernelPath    string
	InitrdPath    string
	DefaultVCPUs  int
	DefaultMemMB  int
	InitSystem    string
	NetworkConfig types.NetworkConfig
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
	container, ok := task.Spec.Runtime.(*types.Container)
	if !ok {
		return nil, fmt.Errorf("task runtime is not a container")
	}

	config := &VMMConfig{
		MachineConfig: MachineConfig{
			VcpuCount:  tt.defaultVCPUs,
			MemSizeMib: tt.defaultMemMB,
			Smt:        false,
		},
		BootSource: BootSourceConfig{
			KernelImagePath: tt.kernelPath,
			BootArgs:        tt.buildBootArgs(task),
			InitrdPath:      tt.initrdPath,
		},
		NetworkInterfaces: []NetworkInterface{},
		Drives:            []Drive{},
	}

	// Apply resource limits from task spec
	if task.Spec.Resources.Limits != nil {
		tt.applyResources(config, task.Spec.Resources.Limits)
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

	// Add volume mounts
	for _, mount := range container.Mounts {
		drive := tt.buildVolumeDrive(mount)
		config.Drives = append(config.Drives, drive)
	}

	// Return as map for direct consumption by vmm.go
	return tt.configToMap(config)
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

// configToJSON converts VMMConfig to JSON string (kept for compatibility).
func (tt *TaskTranslator) configToJSON(config *VMMConfig) (string, error) {
	bytes, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	return string(bytes), nil
}

// buildBootArgs constructs kernel boot arguments.
func (tt *TaskTranslator) buildBootArgs(task *types.Task) string {
	container := task.Spec.Runtime.(*types.Container)

	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"random.trust_cpu=on",
		"init=/init", // Use custom init script at root
	}

	// Check if we have an allocated IP address
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
	args = append(args, ipArg)

	// Pass MTU as a kernel argument (can be used by init scripts)
	args = append(args, fmt.Sprintf("mtu=%d", mtu))

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

// buildInitArgs builds init arguments wrapping the container command.
func (tt *TaskTranslator) buildInitArgs(containerCmd []string) []string {
	switch tt.initSystem {
	case "tini":
		// tini -- <command> <args...>
		args := []string{tt.initPath, "--"}
		args = append(args, containerCmd...)
		return args
	case "dumb-init":
		// dumb-init <command> <args...>
		args := []string{tt.initPath}
		args = append(args, containerCmd...)
		return args
	default:
		// No init system
		return containerCmd
	}
}

// applyResources applies resource limits to the VM configuration.
func (tt *TaskTranslator) applyResources(config *VMMConfig, limits *types.Resources) {
	if limits.MemoryBytes > 0 {
		// Convert bytes to MiB
		config.MachineConfig.MemSizeMib = int(limits.MemoryBytes / 1024 / 1024)
	}

	if limits.NanoCPUs > 0 {
		// Convert nano CPUs to vCPUs (1 vCPU = 1e9 nano CPUs)
		vcpus := int(limits.NanoCPUs / 1e9)
		if vcpus < 1 {
			vcpus = 1
		}
		config.MachineConfig.VcpuCount = vcpus
	}
}

// buildNetworkInterface creates a network interface configuration.
func (tt *TaskTranslator) buildNetworkInterface(_ types.NetworkAttachment, index int, taskID string) NetworkInterface {
	ifaceID := fmt.Sprintf("eth%d", index)

	// Generate TAP name: tap-<hash[:8]>-<index>
	hash := sha256.Sum256([]byte(taskID))
	hashStr := hex.EncodeToString(hash[:])
	tapName := fmt.Sprintf("tap-%s-%d", hashStr[:8], index)

	return NetworkInterface{
		IfaceID:     ifaceID,
		HostDevName: tapName,
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

// buildVolumeDrive creates a volume drive configuration.
func (tt *TaskTranslator) buildVolumeDrive(mount types.Mount) Drive {
	driveID := strings.ReplaceAll(mount.Target, "/", "-")
	driveID = strings.TrimPrefix(driveID, "-")

	return Drive{
		DriveID:      driveID,
		IsRootDevice: false,
		PathOnHost:   mount.Source,
		IsReadOnly:   mount.ReadOnly,
	}
}
