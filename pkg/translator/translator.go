package translator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TaskTranslator converts SwarmKit tasks to Firecracker VM configurations.
type TaskTranslator struct {
	kernelPath   string
	initrdPath   string
	defaultVCPUs int
	defaultMemMB int
	initSystem   string // "none", "tini", "dumb-init"
	initPath     string // Path to init binary
}

// Config holds translator configuration.
type Config struct {
	KernelPath   string
	InitrdPath   string
	DefaultVCPUs int
	DefaultMemMB int
	InitSystem   string
}

// NewTaskTranslator creates a new TaskTranslator.
func NewTaskTranslator(config interface{}) *TaskTranslator {
	// For now, we accept a generic config and extract what we need
	var kernelPath, initrdPath, initSystem string
	var vcpus, memMB int

	// Accept *lifecycle.ManagerConfig or similar
	kernelPath = "/usr/share/firecracker/vmlinux"
	initrdPath = ""
	vcpus = 1
	memMB = 512
	initSystem = "tini" // Default init system

	// Try to extract from config
	if cfg, ok := config.(*lifecycle.ManagerConfig); ok {
		kernelPath = cfg.KernelPath
		vcpus = cfg.DefaultVCPUs
		memMB = cfg.DefaultMemoryMB
	}

	tt := &TaskTranslator{
		kernelPath:   kernelPath,
		initrdPath:   initrdPath,
		defaultVCPUs: vcpus,
		defaultMemMB: memMB,
		initSystem:   initSystem,
		initPath:     getInitPath(initSystem),
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
type VMMConfig struct {
	BootSource        BootSourceConfig
	MachineConfig     MachineConfig
	NetworkInterfaces []NetworkInterface
	Drives            []Drive
	Vsock             *VsockConfig
}

// BootSourceConfig specifies boot configuration.
type BootSourceConfig struct {
	KernelImagePath string
	BootArgs        string
	InitrdPath      string
}

// MachineConfig specifies VM resources.
type MachineConfig struct {
	VcpuCount  int
	MemSizeMib int
	HtEnabled  bool
}

// NetworkInterface specifies network configuration.
type NetworkInterface struct {
	IfaceID     string
	HostDevName string
	MacAddress  string
	RxQueueSize int
	TxQueueSize int
}

// Drive specifies block device configuration.
type Drive struct {
	DriveID      string
	IsRootDevice bool
	PathOnHost   string
	IsReadOnly   bool
}

// VsockConfig specifies vsock configuration.
type VsockConfig struct {
	VsockID  string
	GuestCID int
}

// Translate converts a SwarmKit task to a Firecracker VMM configuration JSON string.
func (tt *TaskTranslator) Translate(task *types.Task) (interface{}, error) {
	if task == nil {
		return nil, fmt.Errorf("task cannot be nil")
	}

	// Extract container from task runtime
	container, ok := task.Spec.Runtime.(*types.Container)
	if !ok {
		return nil, fmt.Errorf("task runtime is not a container")
	}

	config := &VMMConfig{
		MachineConfig: MachineConfig{
			VcpuCount:  tt.defaultVCPUs,
			MemSizeMib: tt.defaultMemMB,
			HtEnabled:  false,
		},
		BootSource: BootSourceConfig{
			KernelImagePath: tt.kernelPath,
			BootArgs:        tt.buildBootArgs(container),
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
		iface := tt.buildNetworkInterface(network, i)
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

	// Return as JSON string for easier consumption
	return tt.configToJSON(config)
}

// configToJSON converts VMMConfig to JSON string.
func (tt *TaskTranslator) configToJSON(config *VMMConfig) (string, error) {
	bytes, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("failed to marshal config: %w", err)
	}
	return string(bytes), nil
}

// buildBootArgs constructs kernel boot arguments.
func (tt *TaskTranslator) buildBootArgs(container *types.Container) string {
	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"pci=off",
		"random.trust_cpu=on",
		"ip=dhcp",
	}

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
func (tt *TaskTranslator) buildNetworkInterface(network types.NetworkAttachment, index int) NetworkInterface {
	ifaceID := fmt.Sprintf("eth%d", index)

	return NetworkInterface{
		IfaceID:     ifaceID,
		HostDevName: fmt.Sprintf("tap%s", ifaceID),
		MacAddress:  "", // Will be generated by Firecracker
		RxQueueSize: 256,
		TxQueueSize: 256,
	}
}

// buildRootDrive creates the root filesystem drive configuration.
func (tt *TaskTranslator) buildRootDrive(container *types.Container, task *types.Task) (Drive, error) {
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
