package translator

import (
	"encoding/json"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildRootDrive_MissingRootfs tests error handling when rootfs annotation is missing
func TestTaskTranslator_BuildRootDrive_MissingRootfs(t *testing.T) {
	tt := NewTaskTranslator(nil)

	tests := []struct {
		name        string
		container   *types.Container
		task        *types.Task
		expectError bool
		errorMsg    string
	}{
		{
			name: "missing rootfs annotation",
			container: &types.Container{
				Image: "nginx:latest",
			},
			task: &types.Task{
				ID:         "task-1",
				Annotations: map[string]string{},
			},
			expectError: true,
			errorMsg:    "rootfs not found in task annotations",
		},
		{
			name: "nil annotations map",
			container: &types.Container{
				Image: "nginx:latest",
			},
			task: &types.Task{
				ID:         "task-2",
				Annotations: nil,
			},
			expectError: true,
			errorMsg:    "rootfs not found in task annotations",
		},
		{
			name: "empty rootfs path",
			container: &types.Container{
				Image: "nginx:latest",
			},
			task: &types.Task{
				ID: "task-3",
				Annotations: map[string]string{
					"rootfs": "",
				},
			},
			expectError: false, // Function returns success even with empty path
		},
		{
			name: "valid rootfs path",
			container: &types.Container{
				Image: "nginx:latest",
			},
			task: &types.Task{
				ID: "task-4",
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
				},
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			drive, err := tt.buildRootDrive(tc.container, tc.task)

			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorMsg)
				assert.Equal(t, Drive{}, drive)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "rootfs", drive.DriveID)
				assert.True(t, drive.IsRootDevice)
				assert.False(t, drive.IsReadOnly)
			}
		})
	}
}

// TestConfigToMap_ErrorHandling tests error handling in configToMap
func TestTaskTranslator_ConfigToMap_ErrorHandling(t *testing.T) {
	tt := NewTaskTranslator(nil)

	// Note: configToMap uses json.Marshal which shouldn't fail for valid VMMConfig structs
	// But we test edge cases like nil structs (though Go won't allow marshaling nil pointers directly)
	tests := []struct {
		name        string
		config      *VMMConfig
		expectError bool
		validate    func(map[string]interface{}, error)
	}{
		{
			name: "valid minimal config",
			config: &VMMConfig{
				BootSource: BootSourceConfig{
					KernelImagePath: "/boot/vmlinux",
					BootArgs:        "console=ttyS0",
				},
				MachineConfig: MachineConfig{
					VcpuCount:  1,
					MemSizeMib: 512,
					Smt:        false,
				},
			},
			expectError: false,
			validate: func(result map[string]interface{}, err error) {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Contains(t, result, "boot-source")
				assert.Contains(t, result, "machine-config")
			},
		},
		{
			name: "config with all fields",
			config: &VMMConfig{
				BootSource: BootSourceConfig{
					KernelImagePath: "/vmlinux",
					BootArgs:        "console=ttyS0",
					InitrdPath:      "/initrd",
				},
				MachineConfig: MachineConfig{
					VcpuCount:  2,
					MemSizeMib: 1024,
					Smt:        true,
				},
				NetworkInterfaces: []NetworkInterface{
					{
						IfaceID:     "eth0",
						HostDevName: "tap0",
						MacAddress:  "02:FC:00:00:00:01",
						RxQueueSize: 256,
						TxQueueSize: 256,
					},
				},
				Drives: []Drive{
					{
						DriveID:      "rootfs",
						IsRootDevice: true,
						PathOnHost:   "/rootfs.ext4",
						IsReadOnly:   false,
					},
				},
				Vsock: &VsockConfig{
					VsockID:  "vsock0",
					GuestCID: 3,
				},
			},
			expectError: false,
			validate: func(result map[string]interface{}, err error) {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Contains(t, result, "network-interfaces")
				assert.Contains(t, result, "drives")
				assert.Contains(t, result, "vsock")
			},
		},
		{
			name: "config with unicode in boot args",
			config: &VMMConfig{
				BootSource: BootSourceConfig{
					KernelImagePath: "/vmlinux",
					BootArgs:        "console=ttyS0 locale=en_US.UTF-8",
				},
				MachineConfig: MachineConfig{
					VcpuCount:  1,
					MemSizeMib: 512,
					Smt:        false,
				},
			},
			expectError: false,
			validate: func(result map[string]interface{}, err error) {
				assert.NoError(t, err)
				assert.NotNil(t, result)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tt.configToMap(tc.config)

			if tc.validate != nil {
				tc.validate(result, err)
			} else if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestBuildBootArgs_StaticIPConfig tests static IP configuration with gateway fallback
func TestTaskTranslator_BuildBootArgs_StaticIPConfig(t *testing.T) {
	tests := []struct {
		name              string
		task              *types.Task
		networkConfig     types.NetworkConfig
		expectedIPArg     string
		expectedGateway   string
		expectedNetmask   string
	}{
		{
			name: "static IP with configured gateway",
			task: &types.Task{
				ID: "task-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-1",
							Spec: types.NetworkSpec{
								Driver: "bridge",
							},
						},
						Addresses: []string{"10.0.0.10/24"},
					},
				},
			},
			networkConfig: types.NetworkConfig{
				BridgeIP: "10.0.0.1/24",
			},
			expectedIPArg:   "ip=10.0.0.10::10.0.0.1:255.255.255.0::eth0:off",
			expectedGateway: "10.0.0.1",
			expectedNetmask: "255.255.255.0",
		},
		{
			name: "static IP without gateway - auto fallback to .1",
			task: &types.Task{
				ID: "task-2",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-2",
							Spec: types.NetworkSpec{
								Driver: "bridge",
							},
						},
						Addresses: []string{"192.168.1.50/24"},
					},
				},
			},
			networkConfig:   types.NetworkConfig{},
			expectedIPArg:   "ip=192.168.1.50::192.168.1.1:255.255.255.0::eth0:off",
			expectedGateway: "192.168.1.1",
			expectedNetmask: "255.255.255.0",
		},
		{
			name: "different subnet - fallback to .1",
			task: &types.Task{
				ID: "task-3",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-3",
							Spec: types.NetworkSpec{
								Driver: "bridge",
							},
						},
						Addresses: []string{"172.17.0.5/16"},
					},
				},
			},
			networkConfig:   types.NetworkConfig{},
			expectedIPArg:   "ip=172.17.0.5::172.17.0.1:255.255.0.0::eth0:off",
			expectedGateway: "172.17.0.1",
			expectedNetmask: "255.255.0.0",
		},
		{
			name: "overlay network with MTU 1450",
			task: &types.Task{
				ID: "task-4",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "overlay-net",
							Spec: types.NetworkSpec{
								Driver: "overlay",
							},
						},
						Addresses: []string{"10.255.0.2/24"},
					},
				},
			},
			networkConfig:   types.NetworkConfig{},
			expectedIPArg:   "ip=10.255.0.2::10.255.0.1:255.255.255.0::eth0:off",
			expectedGateway: "10.255.0.1",
			expectedNetmask: "255.255.255.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := &TaskTranslator{
				networkConfig: tc.networkConfig,
			}

			result := tt.buildBootArgs(tc.task)

			// Check for expected IP argument
			assert.Contains(t, result, tc.expectedIPArg)

			// Check for MTU
			if tc.task.Networks[0].Network.Spec.Driver == "overlay" {
				assert.Contains(t, result, "mtu=1450")
			} else {
				assert.Contains(t, result, "mtu=1500")
			}
		})
	}
}

// TestBuildBootArgs_IPParsingErrorHandling tests IP parsing edge cases
func TestTaskTranslator_BuildBootArgs_IPParsingErrorHandling(t *testing.T) {
	tests := []struct {
		name         string
		task         *types.Task
		expectDHCP   bool
		validateArgs []string
	}{
		{
			name: "invalid CIDR format - falls back to DHCP",
			task: &types.Task{
				ID: "task-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-1",
							Spec: types.NetworkSpec{
								Driver: "bridge",
							},
						},
						Addresses: []string{"invalid-ip"},
					},
				},
			},
			expectDHCP: true,
			validateArgs: []string{
				"console=ttyS0",
				"reboot=k",
				"panic=1",
				"pci=off",
				"random.trust_cpu=on",
				"ip=dhcp",
				"mtu=1500",
			},
		},
		{
			name: "empty addresses - uses DHCP",
			task: &types.Task{
				ID: "task-2",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-2",
							Spec: types.NetworkSpec{
								Driver: "bridge",
							},
						},
						Addresses: []string{},
					},
				},
			},
			expectDHCP: true,
			validateArgs: []string{
				"ip=dhcp",
				"mtu=1500",
			},
		},
		{
			name: "no networks - uses DHCP",
			task: &types.Task{
				ID: "task-3",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/sh"},
					},
				},
				Networks: []types.NetworkAttachment{},
			},
			expectDHCP: true,
			validateArgs: []string{
				"ip=dhcp",
				"mtu=1500",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(nil)
			result := tt.buildBootArgs(tc.task)

			if tc.expectDHCP {
				assert.Contains(t, result, "ip=dhcp")
			}

			for _, arg := range tc.validateArgs {
				assert.Contains(t, result, arg)
			}
		})
	}
}

// TestBuildBootArgs_CommandFallback tests fallback to /bin/sh when no command
func TestTaskTranslator_BuildBootArgs_CommandFallback(t *testing.T) {
	tests := []struct {
		name              string
		task              *types.Task
		expectedInCommand bool
		expectedCommand   string
	}{
		{
			name: "nil command and args - falls back to /bin/sh",
			task: &types.Task{
				ID: "task-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: nil,
						Args:    nil,
					},
				},
			},
			expectedInCommand: true,
			expectedCommand:   "/bin/sh",
		},
		{
			name: "empty command and args - falls back to /bin/sh",
			task: &types.Task{
				ID: "task-2",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{},
						Args:    []string{},
					},
				},
			},
			expectedInCommand: true,
			expectedCommand:   "/bin/sh",
		},
		{
			name: "only args specified",
			task: &types.Task{
				ID: "task-3",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Args: []string{"-c", "echo hello"},
					},
				},
			},
			expectedInCommand: true,
			expectedCommand:   "-c",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(nil)
			result := tt.buildBootArgs(tc.task)

			if tc.expectedInCommand {
				assert.Contains(t, result, tc.expectedCommand)
			}

			// Should always have basic kernel args
			assert.Contains(t, result, "console=ttyS0")
			assert.Contains(t, result, "reboot=k")
			assert.Contains(t, result, "panic=1")
		})
	}
}

// TestNewTaskTranslator_LifecycleConfig tests NewTaskTranslator with lifecycle.ManagerConfig
func TestNewTaskTranslator_LifecycleConfig(t *testing.T) {
	tests := []struct {
		name               string
		config             interface{}
		expectedKernelPath string
		expectedVCPUs      int
		expectedMemMB      int
	}{
		{
			name: "lifecycle ManagerConfig",
			config: &lifecycle.ManagerConfig{
				KernelPath:      "/custom/vmlinux",
				DefaultVCPUs:    4,
				DefaultMemoryMB: 2048,
			},
			expectedKernelPath: "/custom/vmlinux",
			expectedVCPUs:      4,
			expectedMemMB:      2048,
		},
		{
			name: "lifecycle ManagerConfig with zero values",
			config: &lifecycle.ManagerConfig{
				KernelPath:      "",
				DefaultVCPUs:    0,
				DefaultMemoryMB: 0,
			},
			expectedKernelPath: "",
			expectedVCPUs:      0,
			expectedMemMB:      0,
		},
		{
			name:               "nil config - uses defaults",
			config:             nil,
			expectedKernelPath: "/usr/share/firecracker/vmlinux",
			expectedVCPUs:      1,
			expectedMemMB:      512,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(tc.config)

			assert.NotNil(t, tt)
			assert.Equal(t, tc.expectedKernelPath, tt.kernelPath)
			assert.Equal(t, tc.expectedVCPUs, tt.defaultVCPUs)
			assert.Equal(t, tc.expectedMemMB, tt.defaultMemMB)
		})
	}
}

// TestTranslate_InitSystemIntegration tests Translate with different init systems
func TestTaskTranslator_Translate_InitSystemIntegration(t *testing.T) {
	tests := []struct {
		name          string
		initSystem    string
		task          *types.Task
		expectedInArg string
	}{
		{
			name:       "tini init system",
			initSystem: "tini",
			task: &types.Task{
				ID: "task-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/app/server"},
						Args:    []string{"--port=8080"},
					},
				},
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/app.ext4",
				},
			},
			expectedInArg: "/sbin/tini",
		},
		{
			name:       "dumb-init init system",
			initSystem: "dumb-init",
			task: &types.Task{
				ID: "task-2",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/app/start"},
					},
				},
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/app.ext4",
				},
			},
			expectedInArg: "/sbin/dumb-init",
		},
		{
			name:       "none init system",
			initSystem: "none",
			task: &types.Task{
				ID: "task-3",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/bin/bash"},
					},
				},
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/app.ext4",
				},
			},
			expectedInArg: "/bin/bash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &Config{
				InitSystem: tc.initSystem,
			}
			tt := NewTaskTranslator(config)

			result, err := tt.Translate(tc.task)
			require.NoError(t, err)
			require.NotNil(t, result)

			resultMap, ok := result.(map[string]interface{})
			require.True(t, ok)

			bootSource, ok := resultMap["boot-source"].(map[string]interface{})
			require.True(t, ok)

			bootArgs := bootSource["boot_args"].(string)
			assert.Contains(t, bootArgs, tc.expectedInArg)
		})
	}
}

// TestTranslate_EmptyNetworkAttachment tests handling of networks with empty attachment
func TestTaskTranslator_Translate_EmptyNetworkAttachment(t *testing.T) {
	tests := []struct {
		name           string
		networks       []types.NetworkAttachment
		expectedIfaces int
	}{
		{
			name:           "no networks",
			networks:       []types.NetworkAttachment{},
			expectedIfaces: 0,
		},
		{
			name: "single network",
			networks: []types.NetworkAttachment{
				{
					Network: types.Network{
						ID: "net-1",
						Spec: types.NetworkSpec{
							Driver: "bridge",
						},
					},
					Addresses: []string{"10.0.0.2/24"},
				},
			},
			expectedIfaces: 1,
		},
		{
			name: "multiple networks",
			networks: []types.NetworkAttachment{
				{
					Network: types.Network{
						ID: "net-1",
						Spec: types.NetworkSpec{
							Driver: "bridge",
						},
					},
					Addresses: []string{"10.0.0.2/24"},
				},
				{
					Network: types.Network{
						ID: "net-2",
						Spec: types.NetworkSpec{
							Driver: "bridge",
						},
					},
					Addresses: []string{"10.0.1.2/24"},
				},
			},
			expectedIfaces: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(nil)

			task := &types.Task{
				ID: "task-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image:   "nginx:latest",
						Command: []string{"nginx"},
					},
				},
				Networks: tc.networks,
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
				},
			}

			result, err := tt.Translate(task)
			require.NoError(t, err)
			require.NotNil(t, result)

			resultMap, ok := result.(map[string]interface{})
			require.True(t, ok)

			networkIfaces, ok := resultMap["network-interfaces"].([]interface{})
			require.True(t, ok)

			assert.Equal(t, tc.expectedIfaces, len(networkIfaces))
		})
	}
}

// TestBuildRootDrive_DirectCall tests direct calls to buildRootDrive
func TestTaskTranslator_BuildRootDrive_DirectCall(t *testing.T) {
	tt := NewTaskTranslator(nil)

	tests := []struct {
		name        string
		container   *types.Container
		task        *types.Task
		expectError bool
		validate    func(Drive, error)
	}{
		{
			name: "valid rootfs",
			container: &types.Container{
				Image: "nginx:latest",
			},
			task: &types.Task{
				ID: "task-1",
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
				},
			},
			expectError: false,
			validate: func(drive Drive, err error) {
				assert.NoError(t, err)
				assert.Equal(t, "rootfs", drive.DriveID)
				assert.True(t, drive.IsRootDevice)
				assert.Equal(t, "/var/lib/firecracker/rootfs/nginx.ext4", drive.PathOnHost)
				assert.False(t, drive.IsReadOnly)
			},
		},
		{
			name: "rootfs with special characters in path",
			container: &types.Container{
				Image: "app:v1.0",
			},
			task: &types.Task{
				ID: "task-2",
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/app_v1.0-final.ext4",
				},
			},
			expectError: false,
			validate: func(drive Drive, err error) {
				assert.NoError(t, err)
				assert.Equal(t, "/var/lib/firecracker/rootfs/app_v1.0-final.ext4", drive.PathOnHost)
			},
		},
		{
			name: "missing annotation",
			container: &types.Container{
				Image: "redis:alpine",
			},
			task: &types.Task{
				ID:         "task-3",
				Annotations: map[string]string{},
			},
			expectError: true,
			validate: func(drive Drive, err error) {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "rootfs not found")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			drive, err := tt.buildRootDrive(tc.container, tc.task)

			if tc.validate != nil {
				tc.validate(drive, err)
			} else if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestConfigToMap_NilSliceHandling tests handling of nil vs empty slices in JSON conversion
func TestTaskTranslator_ConfigToMap_NilSliceHandling(t *testing.T) {
	tt := NewTaskTranslator(nil)

	tests := []struct {
		name  string
		setup func(*VMMConfig)
	}{
		{
			name: "nil network interfaces",
			setup: func(cfg *VMMConfig) {
				cfg.NetworkInterfaces = nil
			},
		},
		{
			name: "empty network interfaces",
			setup: func(cfg *VMMConfig) {
				cfg.NetworkInterfaces = []NetworkInterface{}
			},
		},
		{
			name: "nil drives",
			setup: func(cfg *VMMConfig) {
				cfg.Drives = nil
			},
		},
		{
			name: "empty drives",
			setup: func(cfg *VMMConfig) {
				cfg.Drives = []Drive{}
			},
		},
		{
			name: "nil vsock",
			setup: func(cfg *VMMConfig) {
				cfg.Vsock = nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &VMMConfig{
				BootSource: BootSourceConfig{
					KernelImagePath: "/vmlinux",
					BootArgs:        "console=ttyS0",
				},
				MachineConfig: MachineConfig{
					VcpuCount:  1,
					MemSizeMib: 512,
					Smt:        false,
				},
			}

			tc.setup(config)

			result, err := tt.configToMap(config)
			assert.NoError(t, err)
			assert.NotNil(t, result)

			// Verify it can be converted back to JSON
			jsonBytes, err := json.Marshal(result)
			assert.NoError(t, err)
			assert.NotEmpty(t, jsonBytes)
		})
	}
}
