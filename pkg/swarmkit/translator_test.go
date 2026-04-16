package swarmkit

import (
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

func TestNewTaskTranslator(t *testing.T) {
	tests := []struct {
		name       string
		kernelPath string
		bridgeIP   string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "valid paths with bridge IP",
			kernelPath: "/path/to/kernel",
			bridgeIP:   "192.168.127.1/24",
			wantErr:    false,
		},
		{
			name:       "valid paths with empty bridge IP",
			kernelPath: "/path/to/kernel",
			bridgeIP:   "",
			wantErr:    false,
		},
		{
			name:       "empty kernel path should error",
			kernelPath: "",
			bridgeIP:   "192.168.127.1/24",
			wantErr:    true,
			errMsg:     "kernel path cannot be empty",
		},
		{
			name:       "both empty should error",
			kernelPath: "",
			bridgeIP:   "",
			wantErr:    true,
			errMsg:     "kernel path cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator, err := NewTaskTranslator(tt.kernelPath, tt.bridgeIP)

			if tt.wantErr {
				if err == nil {
					t.Errorf("NewTaskTranslator() expected error but got nil")
					return
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("NewTaskTranslator() error = %v, want %v", err.Error(), tt.errMsg)
				}
				if translator != nil {
					t.Errorf("NewTaskTranslator() expected nil translator on error, got %v", translator)
				}
			} else {
				if err != nil {
					t.Errorf("NewTaskTranslator() unexpected error: %v", err)
					return
				}
				if translator == nil {
					t.Errorf("NewTaskTranslator() expected non-nil translator, got nil")
				}
			}
		})
	}
}

func TestTranslate(t *testing.T) {
	kernelPath := "/test/kernel"

	tests := []struct {
		name     string
		bridgeIP string
		task     *types.Task
		validate func(t *testing.T, config map[string]interface{})
	}{
		{
			name:     "basic task with defaults",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-defaults",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "nginx:latest",
					},
				},
			},
			validate: func(t *testing.T, config map[string]interface{}) {
				// Check boot source
				bootSource, ok := config["boot-source"].(map[string]interface{})
				if !ok {
					t.Fatal("missing boot-source")
				}
				if bootSource["kernel_image_path"] != kernelPath {
					t.Errorf("kernel_image_path = %v, want %v", bootSource["kernel_image_path"], kernelPath)
				}

				// Check machine config defaults
				machineConfig, ok := config["machine-config"].(map[string]interface{})
				if !ok {
					t.Fatal("missing machine-config")
				}
				if vcpus := machineConfig["vcpu_count"]; vcpus != 1 {
					t.Errorf("vcpu_count = %v, want 1 (default)", vcpus)
				}
				if mem := machineConfig["mem_size_mib"]; mem != 512 {
					t.Errorf("mem_size_mib = %v, want 512 (default)", mem)
				}

				// Check drives
				drives, ok := config["drives"].([]map[string]interface{})
				if !ok || len(drives) != 1 {
					t.Fatal("missing or invalid drives")
				}
				if drives[0]["drive_id"] != "task-defaults" {
					t.Errorf("drive_id = %v, want task-defaults", drives[0]["drive_id"])
				}

				// Check default rootfs path
				expectedPath := "/var/lib/firecracker/rootfs/task-defaults.ext4"
				if drives[0]["path_on_host"] != expectedPath {
					t.Errorf("path_on_host = %v, want %v", drives[0]["path_on_host"], expectedPath)
				}
			},
		},
		{
			name:     "task with custom rootfs annotation",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-custom-rootfs",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "redis:latest",
					},
				},
				Annotations: map[string]string{
					"rootfs": "/custom/path/rootfs.ext4",
				},
			},
			validate: func(t *testing.T, config map[string]interface{}) {
				drives, ok := config["drives"].([]map[string]interface{})
				if !ok || len(drives) != 1 {
					t.Fatal("missing or invalid drives")
				}
				if drives[0]["path_on_host"] != "/custom/path/rootfs.ext4" {
					t.Errorf("path_on_host = %v, want /custom/path/rootfs.ext4", drives[0]["path_on_host"])
				}
			},
		},
		{
			name:     "task with networks",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-with-networks",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "app:latest",
					},
				},
				Networks: []types.NetworkAttachment{
					{
						Network: types.Network{
							ID: "net-1",
							Spec: types.NetworkSpec{
								Name: "my-network",
							},
						},
						Addresses: []string{"192.168.127.10/24"},
					},
				},
			},
			validate: func(t *testing.T, config map[string]interface{}) {
				// Check boot args contain network config
				bootSource, ok := config["boot-source"].(map[string]interface{})
				if !ok {
					t.Fatal("missing boot-source")
				}
				bootArgs := bootSource["boot_args"].(string)
				if !contains(bootArgs, "ip=192.168.127.10::192.168.127.1:255.255.255.0::eth0:off") {
					t.Errorf("boot_args missing network config: %s", bootArgs)
				}

				// Check network interfaces
				ifaces, ok := config["network-interfaces"].([]map[string]interface{})
				if !ok || len(ifaces) != 1 {
					t.Fatal("missing or invalid network-interfaces")
				}
				if ifaces[0]["iface_id"] != "eth0" {
					t.Errorf("iface_id = %v, want eth0", ifaces[0]["iface_id"])
				}
				// Check MAC format (AA:FC:XX:XX:XX:XX)
				mac := ifaces[0]["guest_mac"].(string)
				if len(mac) != 17 || mac[:5] != "AA:FC" {
					t.Errorf("guest_mac = %v, want format AA:FC:XX:XX:XX:XX", mac)
				}
			},
		},
		{
			name:     "task without container runtime",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-no-container",
				Spec: types.TaskSpec{
					Runtime: "not-a-container",
				},
			},
			validate: func(t *testing.T, config map[string]interface{}) {
				// Should still produce valid config with defaults
				machineConfig, ok := config["machine-config"].(map[string]interface{})
				if !ok {
					t.Fatal("missing machine-config")
				}
				if vcpus := machineConfig["vcpu_count"]; vcpus != 1 {
					t.Errorf("vcpu_count = %v, want 1 (default)", vcpus)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator, err := NewTaskTranslator(kernelPath, tt.bridgeIP)
			if err != nil {
				t.Fatalf("NewTaskTranslator() failed: %v", err)
			}

			result, err := translator.Translate(tt.task)
			if err != nil {
				t.Fatalf("Translate() failed: %v", err)
			}

			config, ok := result.(map[string]interface{})
			if !ok {
				t.Fatal("Translate() did not return map[string]interface{}")
			}

			tt.validate(t, config)
		})
	}
}

func TestTranslateResourceLimits(t *testing.T) {
	kernelPath := "/test/kernel"
	bridgeIP := "192.168.127.1/24"

	tests := []struct {
		name          string
		nanoCPUs      int64
		memoryBytes   int64
		expectedVCPUs int
		expectedMemMB int
	}{
		{
			name:          "exact 1 vCPU (1e9 nanoCPUs)",
			nanoCPUs:      1e9,
			memoryBytes:   1024 * 1024 * 1024, // 1GB
			expectedVCPUs: 1,
			expectedMemMB: 1024,
		},
		{
			name:          "2 vCPUs",
			nanoCPUs:      2e9,
			memoryBytes:   2 * 1024 * 1024 * 1024, // 2GB
			expectedVCPUs: 2,
			expectedMemMB: 2048,
		},
		{
			name:          "4 vCPUs",
			nanoCPUs:      4e9,
			memoryBytes:   4 * 1024 * 1024 * 1024, // 4GB
			expectedVCPUs: 4,
			expectedMemMB: 4096,
		},
		{
			name:          "sub-1e9 nanoCPUs floors to 1 vCPU",
			nanoCPUs:      5e8,
			memoryBytes:   512 * 1024 * 1024, // 512MB
			expectedVCPUs: 1,                 // floors to 1
			expectedMemMB: 512,
		},
		{
			name:          "very small nanoCPUs floors to 1 vCPU",
			nanoCPUs:      1,
			memoryBytes:   256 * 1024 * 1024, // 256MB
			expectedVCPUs: 1,                 // minimum enforced
			expectedMemMB: 512,               // minimum enforced
		},
		{
			name:          "memory below minimum floors to 512MB",
			nanoCPUs:      1e9,
			memoryBytes:   256 * 1024 * 1024, // 256MB
			expectedVCPUs: 1,
			expectedMemMB: 512, // minimum enforced
		},
		{
			name:          "zero resources use defaults",
			nanoCPUs:      0,
			memoryBytes:   0,
			expectedVCPUs: 1,  // default
			expectedMemMB: 512, // default
		},
		{
			name:          "256MB memory (exact minimum)",
			nanoCPUs:      1e9,
			memoryBytes:   512 * 1024 * 1024, // 512MB
			expectedVCPUs: 1,
			expectedMemMB: 512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator, err := NewTaskTranslator(kernelPath, bridgeIP)
			if err != nil {
				t.Fatalf("NewTaskTranslator() failed: %v", err)
			}

			task := &types.Task{
				ID: "task-resources",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Image: "test:latest",
					},
					Resources: types.ResourceRequirements{
						Reservations: &types.Resources{
							NanoCPUs:    tt.nanoCPUs,
							MemoryBytes: tt.memoryBytes,
						},
					},
				},
			}

			result, err := translator.Translate(task)
			if err != nil {
				t.Fatalf("Translate() failed: %v", err)
			}

			config, ok := result.(map[string]interface{})
			if !ok {
				t.Fatal("Translate() did not return map[string]interface{}")
			}

			machineConfig, ok := config["machine-config"].(map[string]interface{})
			if !ok {
				t.Fatal("missing machine-config")
			}

			if vcpus := machineConfig["vcpu_count"]; vcpus != tt.expectedVCPUs {
				t.Errorf("vcpu_count = %v, want %d", vcpus, tt.expectedVCPUs)
			}

			if mem := machineConfig["mem_size_mib"]; mem != tt.expectedMemMB {
				t.Errorf("mem_size_mib = %v, want %d", mem, tt.expectedMemMB)
			}
		})
	}
}

func TestBuildBootArgs(t *testing.T) {
	tests := []struct {
		name           string
		bridgeIP       string
		task           *types.Task
		expectedInArgs []string
		notInArgs      []string
	}{
		{
			name:     "no network - basic boot args",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-no-net",
				Spec: types.TaskSpec{
					Runtime: &types.Container{},
				},
				Networks: []types.NetworkAttachment{},
			},
			expectedInArgs: []string{
				"console=ttyS0",
				"reboot=k",
				"panic=1",
				"pci=off",
				"nomodules",
				"init=/sbin/init",
			},
			notInArgs: []string{"ip="},
		},
		{
			name:     "with network - includes IP config",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-with-net",
				Spec: types.TaskSpec{
					Runtime: &types.Container{},
				},
				Networks: []types.NetworkAttachment{
					{
						Addresses: []string{"192.168.127.10/24"},
					},
				},
			},
			expectedInArgs: []string{
				"console=ttyS0",
				"init=/sbin/init",
				"ip=192.168.127.10::192.168.127.1:255.255.255.0::eth0:off",
			},
		},
		{
			name:     "network with CIDR gateway parsing",
			bridgeIP: "10.0.0.1/16",
			task: &types.Task{
				ID: "task-cidr-gw",
				Spec: types.TaskSpec{
					Runtime: &types.Container{},
				},
				Networks: []types.NetworkAttachment{
					{
						Addresses: []string{"10.0.0.50/16"},
					},
				},
			},
			expectedInArgs: []string{
				"ip=10.0.0.50::10.0.0.1:255.255.255.0::eth0:off",
			},
		},
		{
			name:     "network IP parsing removes CIDR",
			bridgeIP: "192.168.127.1/24",
			task: &types.Task{
				ID: "task-ip-cidr",
				Spec: types.TaskSpec{
					Runtime: &types.Container{},
				},
				Networks: []types.NetworkAttachment{
					{
						Addresses: []string{"172.16.0.5/20"},
					},
				},
			},
			expectedInArgs: []string{
				"ip=172.16.0.5::192.168.127.1:255.255.255.0::eth0:off",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator, err := NewTaskTranslator("/test/kernel", tt.bridgeIP)
			if err != nil {
				t.Fatalf("NewTaskTranslator() failed: %v", err)
			}

			result, err := translator.Translate(tt.task)
			if err != nil {
				t.Fatalf("Translate() failed: %v", err)
			}

			config := result.(map[string]interface{})
			bootSource := config["boot-source"].(map[string]interface{})
			bootArgs := bootSource["boot_args"].(string)

			for _, expected := range tt.expectedInArgs {
				if !contains(bootArgs, expected) {
					t.Errorf("boot args missing expected string: %q\nGot: %s", expected, bootArgs)
				}
			}

			for _, notExpected := range tt.notInArgs {
				if contains(bootArgs, notExpected) {
					t.Errorf("boot args should not contain: %q\nGot: %s", notExpected, bootArgs)
				}
			}
		})
	}
}

func TestBuildNetworkInterfaces(t *testing.T) {
	tests := []struct {
		name            string
		task            *types.Task
		expectedCount   int
		expectedIfaceID string
		validateTapName bool
	}{
		{
			name: "single network interface",
			task: &types.Task{
				ID: "task-single-net",
				Networks: []types.NetworkAttachment{
					{Network: types.Network{ID: "net1"}},
				},
			},
			expectedCount:   1,
			expectedIfaceID: "eth0",
			validateTapName: true,
		},
		{
			name: "multiple network interfaces",
			task: &types.Task{
				ID: "task-multi-net",
				Networks: []types.NetworkAttachment{
					{Network: types.Network{ID: "net1"}},
					{Network: types.Network{ID: "net2"}},
					{Network: types.Network{ID: "net3"}},
				},
			},
			expectedCount:   3,
			expectedIfaceID: "eth0", // First interface
			validateTapName: true,
		},
		{
			name: "no network interfaces",
			task: &types.Task{
				ID:       "task-no-net",
				Networks: []types.NetworkAttachment{},
			},
			expectedCount:   0,
			expectedIfaceID: "",
			validateTapName: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator, err := NewTaskTranslator("/test/kernel", "")
			if err != nil {
				t.Fatalf("NewTaskTranslator() failed: %v", err)
			}

			result, err := translator.Translate(tt.task)
			if err != nil {
				t.Fatalf("Translate() failed: %v", err)
			}

			config := result.(map[string]interface{})
			ifaces, ok := config["network-interfaces"].([]map[string]interface{})
			if !ok {
				t.Fatal("network-interfaces is not []map[string]interface{}")
			}

			if len(ifaces) != tt.expectedCount {
				t.Errorf("network-interfaces count = %d, want %d", len(ifaces), tt.expectedCount)
			}

			if tt.expectedCount > 0 {
				// Check first interface ID
				if ifaces[0]["iface_id"] != tt.expectedIfaceID {
					t.Errorf("first iface_id = %v, want %s", ifaces[0]["iface_id"], tt.expectedIfaceID)
				}

				// Check tap name format (tap-<hash>-<index>)
				if tt.validateTapName {
					tapName := ifaces[0]["host_dev_name"].(string)
					if len(tapName) < 10 || tapName[:4] != "tap-" {
						t.Errorf("tap_name format invalid: %s", tapName)
					}
					// Verify tap name contains hash prefix (8 hex chars)
					// Format: tap-<8-char-hash>-<index>
				}

				// Check MAC address format
				mac := ifaces[0]["guest_mac"].(string)
				if len(mac) != 17 {
					t.Errorf("MAC address length = %d, want 17 (AA:FC:XX:XX:XX:XX)", len(mac))
				}
				if mac[:5] != "AA:FC" {
					t.Errorf("MAC prefix = %s, want AA:FC", mac[:5])
				}
			}
		})
	}
}

func TestGenerateMAC(t *testing.T) {
	tests := []struct {
		name     string
		index    int
		expected string // optional exact value for deterministic cases
	}{
		{
			name:  "index 0",
			index: 0,
		},
		{
			name:  "index 1",
			index: 1,
		},
		{
			name:  "index 2",
			index: 2,
		},
		{
			name:  "index 255",
			index: 255,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac := generateMAC(tt.index)

			// Check format: AA:FC:XX:XX:XX:XX
			if len(mac) != 17 {
				t.Errorf("MAC length = %d, want 17", len(mac))
			}

			// Check prefix
			if mac[:5] != "AA:FC" {
				t.Errorf("MAC prefix = %s, want AA:FC", mac[:5])
			}

			// Check colons
		parts := strings.Split(mac, ":")
			if len(parts) != 6 {
				t.Errorf("MAC has %d parts, want 6", len(parts))
			}

			// Check deterministic: same index produces same MAC
			mac2 := generateMAC(tt.index)
			if mac != mac2 {
				t.Errorf("generateMAC(%d) not deterministic: %s vs %s", tt.index, mac, mac2)
			}

			// Check different indices produce different MACs (for small indices)
			if tt.index < 10 {
				macDiff := generateMAC(tt.index + 1)
				if mac == macDiff {
					t.Errorf("generateMAC(%d) == generateMAC(%d), want different", tt.index, tt.index+1)
				}
			}
		})
	}
}

func TestGetRootfsPath(t *testing.T) {
	tests := []struct {
		name           string
		taskID         string
		annotations    map[string]string
		expectedPath   string
	}{
		{
			name:         "default path without annotation",
			taskID:       "task-123",
			annotations:  map[string]string{},
			expectedPath: "/var/lib/firecracker/rootfs/task-123.ext4",
		},
		{
			name:   "custom path from annotation",
			taskID: "task-456",
			annotations: map[string]string{
				"rootfs": "/custom/rootfs/path.img",
			},
			expectedPath: "/custom/rootfs/path.img",
		},
		{
			name:         "nil annotations",
			taskID:       "task-789",
			annotations:  nil,
			expectedPath: "/var/lib/firecracker/rootfs/task-789.ext4",
		},
		{
			name:   "annotation exists but empty string",
			taskID: "task-abc",
			annotations: map[string]string{
				"rootfs": "",
			},
			expectedPath: "", // Empty string is returned
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &types.Task{
				ID:          tt.taskID,
				Annotations: tt.annotations,
				Spec: types.TaskSpec{
					Runtime: &types.Container{},
				},
			}

			result := getRootfsPath(task)
			if result != tt.expectedPath {
				t.Errorf("getRootfsPath() = %v, want %v", result, tt.expectedPath)
			}
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
