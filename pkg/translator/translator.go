package translator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TaskTranslator converts SwarmKit tasks to Firecracker VM configurations.
type TaskTranslator struct {
	kernelPath     string
	initrdPath     string
	defaultVCPUs   int
	defaultMemMB   int
}

// Config holds translator configuration.
type Config struct {
	KernelPath     string
	InitrdPath     string
	DefaultVCPUs   int
	DefaultMemMB   int
}

// NewTaskTranslator creates a new TaskTranslator.
func NewTaskTranslator(config interface{}) *TaskTranslator {
	// For now, we accept a generic config and extract what we need
	var kernelPath, initrdPath string
	var vcpus, memMB int

	// Accept *executor.Config or similar
	// For simplicity, we'll use defaults if config doesn't match
	kernelPath = "/usr/share/firecracker/vmlinux"
	initrdPath = ""
	vcpus = 1
	memMB = 512

	return &TaskTranslator{
		kernelPath:   kernelPath,
		initrdPath:   initrdPath,
		defaultVCPUs: vcpus,
		defaultMemMB: memMB,
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
	IfaceID         string
	HostDevName     string
	MacAddress      string
	RxQueueSize     int
	TxQueueSize     int
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

	// Add container command
	if len(container.Command) > 0 {
		args = append(args, "--")
		args = append(args, container.Command...)
	}

	// Add container args
	if len(container.Args) > 0 {
		args = append(args, container.Args...)
	}

	return strings.Join(args, " ")
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
	if strings.HasPrefix(driveID, "-") {
		driveID = driveID[1:]
	}

	return Drive{
		DriveID:      driveID,
		IsRootDevice: false,
		PathOnHost:   mount.Source,
		IsReadOnly:   mount.ReadOnly,
	}
}
