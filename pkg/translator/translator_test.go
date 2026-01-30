package translator

import (
	"encoding/json"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTaskTranslator(t *testing.T) {
	tests := []struct {
		name    string
		config  interface{}
		wantNil bool
	}{
		{
			name:   "nil config",
			config: nil,
			wantNil: false, // Should still return translator with defaults
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewTaskTranslator(tt.config)
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestTaskTranslator_Translate(t *testing.T) {
	tests := []struct {
		name    string
		task    *types.Task
		wantErr bool
	}{
		{
			name: "simple container",
			task: &types.Task{
				ID:        "task-1",
				ServiceID: "service-1",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image:   "nginx:latest",
						Command: []string{"nginx"},
						Args:    []string{"-g", "daemon off;"},
					},
					Resources: types.ResourceRequirements{
						Limits: &types.Resources{
							NanoCPUs:    2e9, // 2 CPUs
							MemoryBytes: 1024 * 1024 * 1024, // 1GB
						},
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "network-1",
							Spec: types.NetworkSpec{
								DriverConfig: &types.DriverConfig{
									Bridge: &types.BridgeConfig{
										Name: "swarm-br0",
									},
								},
							},
						},
						Addresses: []string{"10.0.0.2/24"},
					},
				},
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
				},
			},
			wantErr: false,
		},
		{
			name: "container with volume mounts",
			task: &types.Task{
				ID:        "task-2",
				ServiceID: "service-2",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "redis:alpine",
						Mounts: []types.Mount{
							{
								Target:   "/data",
								Source:   "/srv/redis/data",
								ReadOnly: false,
							},
							{
								Target:   "/config",
								Source:   "/etc/redis.conf",
								ReadOnly: true,
							},
						},
					},
				},
				Annotations: map[string]string{
					"rootfs": "/var/lib/firecracker/rootfs/redis.ext4",
				},
			},
			wantErr: false,
		},
		{
			name:    "nil task",
			task:    nil,
			wantErr: true,
		},
		{
			name: "task without container runtime",
			task: &types.Task{
				ID:   "task-3",
				Spec: types.TaskSpec{
					Runtime: "not-a-container",
				},
			},
			wantErr: true,
		},
		{
			name: "task without rootfs annotation",
			task: &types.Task{
				ID:        "task-4",
				ServiceID: "service-4",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
				Annotations: map[string]string{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewTaskTranslator(nil)

			got, err := translator.Translate(tt.task)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)

				// Verify the result is a JSON string
				configJSON, ok := got.(string)
				require.True(t, ok, "Result should be JSON string")

				// Verify it's valid JSON
				var config VMMConfig
				require.NoError(t, json.Unmarshal([]byte(configJSON), &config), "Should be valid JSON")

				// Verify boot source
				assert.Equal(t, "/usr/share/firecracker/vmlinux", config.BootSource.KernelImagePath)
				assert.Contains(t, config.BootSource.BootArgs, "console=ttyS0")

				// Verify machine config
				assert.Greater(t, config.MachineConfig.VcpuCount, 0)
				assert.Greater(t, config.MachineConfig.MemSizeMib, 0)

				// Verify drives (at least rootfs)
				assert.NotEmpty(t, config.Drives, "Should have at least rootfs drive")

				// Verify rootfs drive
				rootfsFound := false
				for _, drive := range config.Drives {
					if drive.IsRootDevice {
						rootfsFound = true
						assert.Equal(t, "rootfs", drive.DriveID)
						assert.False(t, drive.IsReadOnly)
					}
				}
				assert.True(t, rootfsFound, "Rootfs drive should be present")

				// Verify network interfaces (if task has networks)
				if len(tt.task.Networks) > 0 {
					assert.NotEmpty(t, config.NetworkInterfaces)
					assert.Equal(t, len(tt.task.Networks), len(config.NetworkInterfaces))
				}

				// Verify volume mounts (if task has mounts)
				if container, ok := tt.task.Spec.Runtime.(*types.Container); ok {
					if len(container.Mounts) > 0 {
						// Should have rootfs + volumes
						expectedDrives := 1 + len(container.Mounts)
						assert.Equal(t, expectedDrives, len(config.Drives))
					}
				}
			}
		})
	}
}

func TestTaskTranslator_buildBootArgs(t *testing.T) {
	tests := []struct {
		name      string
		container *types.Container
		wantArgs  []string
	}{
		{
			name: "container with command and args",
			container: &types.Container{
				Command: []string{"/bin/sh"},
				Args:    []string{"-c", "echo hello"},
			},
			wantArgs: []string{
				"console=ttyS0",
				"reboot=k",
				"panic=1",
				"pci=off",
				"random.trust_cpu=on",
				"ip=dhcp",
				"--",
				"/bin/sh",
				"-c",
				"echo hello",
			},
		},
		{
			name: "container with command only",
			container: &types.Container{
				Command: []string{"nginx"},
			},
			wantArgs: []string{
				"console=ttyS0",
				"reboot=k",
				"panic=1",
				"pci=off",
				"random.trust_cpu=on",
				"ip=dhcp",
				"--",
				"nginx",
			},
		},
		{
			name: "container with no command or args",
			container: &types.Container{},
			wantArgs: []string{
				"console=ttyS0",
				"reboot=k",
				"panic=1",
				"pci=off",
				"random.trust_cpu=on",
				"ip=dhcp",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewTaskTranslator(nil)
			got := translator.buildBootArgs(tt.container)
			
			// Check that all expected args are present
			for _, arg := range tt.wantArgs {
				assert.Contains(t, got, arg)
			}
		})
	}
}

func TestTaskTranslator_applyResources(t *testing.T) {
	tests := []struct {
		name      string
		limits    *types.Resources
		wantVCPUs int
		wantMemMB int
	}{
		{
			name: "2 CPUs, 1GB RAM",
			limits: &types.Resources{
				NanoCPUs:    2e9,
				MemoryBytes: 1024 * 1024 * 1024,
			},
			wantVCPUs: 2,
			wantMemMB: 1024,
		},
		{
			name: "0.5 CPUs, 512MB RAM",
			limits: &types.Resources{
				NanoCPUs:    500_000_000,
				MemoryBytes: 512 * 1024 * 1024,
			},
			wantVCPUs: 1, // Minimum 1 vCPU
			wantMemMB: 512,
		},
		{
			name: "4 CPUs, 4GB RAM",
			limits: &types.Resources{
				NanoCPUs:    4e9,
				MemoryBytes: 4 * 1024 * 1024 * 1024,
			},
			wantVCPUs: 4,
			wantMemMB: 4096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewTaskTranslator(nil)

			config := &VMMConfig{
				MachineConfig: MachineConfig{
					VcpuCount:  1,
					MemSizeMib: 512,
				},
			}

			translator.applyResources(config, tt.limits)

			assert.Equal(t, tt.wantVCPUs, config.MachineConfig.VcpuCount)
			assert.Equal(t, tt.wantMemMB, config.MachineConfig.MemSizeMib)
		})
	}
}

func TestTaskTranslator_buildNetworkInterface(t *testing.T) {
	tests := []struct {
		name   string
		network types.NetworkAttachment
		index  int
		wantID string
	}{
		{
			name: "first interface",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "net-1",
				},
			},
			index:  0,
			wantID: "eth0",
		},
		{
			name: "second interface",
			network: types.NetworkAttachment{
				Network: types.Network{
					ID: "net-2",
				},
			},
			index:  1,
			wantID: "eth1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewTaskTranslator(nil)
			got := translator.buildNetworkInterface(tt.network, tt.index)

			assert.Equal(t, tt.wantID, got.IfaceID)
			assert.Contains(t, got.HostDevName, "tap")
			assert.Equal(t, 256, got.RxQueueSize)
			assert.Equal(t, 256, got.TxQueueSize)
		})
	}
}

func TestTaskTranslator_buildVolumeDrive(t *testing.T) {
	tests := []struct {
		name         string
		mount        types.Mount
		wantDriveID  string
		wantReadOnly bool
	}{
		{
			name: "read-write mount",
			mount: types.Mount{
				Target:   "/data",
				Source:   "/srv/data",
				ReadOnly: false,
			},
			wantDriveID:  "data",
			wantReadOnly: false,
		},
		{
			name: "read-only mount",
			mount: types.Mount{
				Target:   "/config/redis.conf",
				Source:   "/etc/redis.conf",
				ReadOnly: true,
			},
			wantDriveID:  "config-redis.conf",
			wantReadOnly: true,
		},
		{
			name: "mount with leading slash",
			mount: types.Mount{
				Target:   "/app/data",
				Source:   "/host/data",
				ReadOnly: false,
			},
			wantDriveID:  "app-data",
			wantReadOnly: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewTaskTranslator(nil)
			got := translator.buildVolumeDrive(tt.mount)

			assert.Equal(t, tt.wantDriveID, got.DriveID)
			assert.Equal(t, tt.mount.Source, got.PathOnHost)
			assert.Equal(t, tt.wantReadOnly, got.IsReadOnly)
			assert.False(t, got.IsRootDevice)
		})
	}
}

// Benchmark translation
func BenchmarkTaskTranslator_Translate(b *testing.B) {
	task := &types.Task{
		ID:        "task-bench",
		ServiceID: "service-bench",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image:   "nginx:latest",
				Command: []string{"nginx"},
				Mounts: []types.Mount{
					{Target: "/data", Source: "/data", ReadOnly: false},
				},
			},
			Resources: types.ResourceRequirements{
				Limits: &types.Resources{
					NanoCPUs:    2e9,
					MemoryBytes: 1024 * 1024 * 1024,
				},
			},
		},
		Networks: []types.NetworkAttachment{
			{
				Network: types.Network{ID: "net-1"},
				Addresses: []string{"10.0.0.2"},
			},
		},
		Annotations: map[string]string{
			"rootfs": "/var/lib/firecracker/rootfs/nginx.ext4",
		},
	}

	translator := NewTaskTranslator(nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = translator.Translate(task)
	}
}
