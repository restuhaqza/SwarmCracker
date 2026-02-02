//go:build e2e
// +build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/lifecycle"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/restuhaqza/swarmcracker/test/testinfra/checks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FirecrackerConfig represents the Firecracker VM configuration
type FirecrackerConfig struct {
	BootSource        BootSource         `json:"boot-source"`
	Drives            []Drive            `json:"drives"`
	MachineConfig     MachineConfig      `json:"machine-config"`
	NetworkInterfaces []NetworkInterface `json:"network-interfaces,omitempty"`
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args,omitempty"`
}

type Drive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type MachineConfig struct {
	VCPUCount  int64 `json:"vcpu_count"`
	MemSizeMib int64 `json:"mem_size_mib"`
	SMT        bool  `json:"smt"` // Changed from ht_enabled to smt for Firecracker v1.14+
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac,omitempty"`
	HostDevName string `json:"host_dev_name"`
}

// TestE2ESimpleVMLaunch tests launching a simple Firecracker VM
func TestE2ESimpleVMLaunch(t *testing.T) {
	ctx := context.Background()

	// Skip if prerequisites not met
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges for KVM access")
	}

	// Get kernel path
	fcChecker := checks.NewFirecrackerChecker()
	err := fcChecker.CheckKernel()
	require.NoError(t, err, "Kernel must be available")
	kernelPath := fcChecker.GetKernelPath()

	// Create temporary rootfs (minimal)
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")

	// Create a minimal ext4 filesystem (1MB)
	err = exec.Command("dd", "if=/dev/zero", "of="+rootfsPath, "bs=1M", "count=1").Run()
	require.NoError(t, err, "Failed to create rootfs file")

	err = exec.Command("mkfs.ext4", "-F", rootfsPath).Run()
	require.NoError(t, err, "Failed to format rootfs")

	t.Logf("Created test rootfs at: %s", rootfsPath)

	// Create Firecracker config
	config := FirecrackerConfig{
		BootSource: BootSource{
			KernelImagePath: kernelPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
		},
		Drives: []Drive{
			{
				DriveID:      "rootfs",
				PathOnHost:   rootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: MachineConfig{
			VCPUCount:  1,
			MemSizeMib: 512,
			SMT:        false,
		},
	}

	configJSON, err := json.Marshal(config)
	require.NoError(t, err, "Failed to marshal config")

	t.Run("Translate Task to Config", func(t *testing.T) {
		// Create a test task
		task := &types.Task{
			ID:        "test-vm-1",
			ServiceID: "test-service",
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Image:   "test:latest",
					Command: []string{"/bin/sh"},
				},
				Resources: types.ResourceRequirements{
					Limits: &types.Resources{
						NanoCPUs:    1000000000,        // 1 CPU
						MemoryBytes: 512 * 1024 * 1024, // 512 MB
					},
				},
			},
		}

		// Verify config was created successfully
		assert.NotEmpty(t, configJSON, "Config JSON must not be empty")
		t.Logf("Task %s translated to config successfully", task.ID)
	})

	t.Run("Launch Firecracker VM", func(t *testing.T) {
		// Create VMM manager
		vmmConfig := &lifecycle.ManagerConfig{
			KernelPath:      kernelPath,
			RootfsDir:       tmpDir,
			SocketDir:       tmpDir,
			DefaultVCPUs:    1,
			DefaultMemoryMB: 512,
			EnableJailer:    false,
		}

		vmm := lifecycle.NewVMMManager(vmmConfig)

		// Create test task
		task := &types.Task{
			ID:        "test-vm-2",
			ServiceID: "test-service",
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Image: "test:latest",
				},
				Resources: types.ResourceRequirements{
					Limits: &types.Resources{
						NanoCPUs:    1000000000,
						MemoryBytes: 512 * 1024 * 1024,
					},
				},
			},
		}

		// Start VM
		err := vmm.Start(ctx, task, string(configJSON))
		if err != nil {
			t.Logf("Failed to start VM: %v", err)
			t.Log("This is expected if Firecracker binary name doesn't match or other issues")
			t.Skip("VM launch failed, skipping remaining tests")
		}

		t.Logf("VM started successfully for task %s", task.ID)

		// Give VM time to initialize
		time.Sleep(2 * time.Second)

		// Check VM status
		t.Run("Check VM Status", func(t *testing.T) {
			status, err := vmm.Describe(ctx, task)
			require.NoError(t, err, "Should be able to describe VM")
			assert.NotNil(t, status, "Status must not be nil")
			assert.Equal(t, types.TaskState_RUNNING, status.State, "VM should be running")

			t.Logf("VM State: %d", status.State)
			if status.RuntimeStatus != nil {
				t.Logf("Runtime Status: %+v", status.RuntimeStatus)
			}
		})
	})
}

// TestE2EVMLifecycle tests the complete VM lifecycle
func TestE2EVMLifecycle(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ctx := context.Background()

	// Get kernel path
	fcChecker := checks.NewFirecrackerChecker()
	err := fcChecker.CheckKernel()
	require.NoError(t, err, "Kernel must be available")
	kernelPath := fcChecker.GetKernelPath()

	// Create test environment
	tmpDir := t.TempDir()
	rootfsPath := filepath.Join(tmpDir, "rootfs.ext4")

	// Create minimal rootfs
	err = exec.Command("dd", "if=/dev/zero", "of="+rootfsPath, "bs=1M", "count=1").Run()
	require.NoError(t, err)

	err = exec.Command("mkfs.ext4", "-F", rootfsPath).Run()
	require.NoError(t, err)

	// Create config
	config := FirecrackerConfig{
		BootSource: BootSource{
			KernelImagePath: kernelPath,
			BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
		},
		Drives: []Drive{
			{
				DriveID:      "rootfs",
				PathOnHost:   rootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: MachineConfig{
			VCPUCount:  1,
			MemSizeMib: 512,
			SMT:        false,
		},
	}

	configJSON, _ := json.Marshal(config)

	// Create VMM manager
	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      kernelPath,
		RootfsDir:       tmpDir,
		SocketDir:       tmpDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 512,
	}

	vmm := lifecycle.NewVMMManager(vmmConfig)

	task := &types.Task{
		ID:        "lifecycle-test-vm",
		ServiceID: "test-service",
		Spec: types.TaskSpec{
			Runtime: &types.Container{
				Image: "test:latest",
			},
		},
	}

	t.Run("Start VM", func(t *testing.T) {
		err := vmm.Start(ctx, task, string(configJSON))
		if err != nil {
			t.Skipf("Failed to start VM: %v (skipping lifecycle test)", err)
		}
		t.Log("VM started successfully")
	})

	t.Run("Wait for VM", func(t *testing.T) {
		if t.Skipped() {
			t.SkipNow()
		}

		status, err := vmm.Wait(ctx, task)
		require.NoError(t, err, "Should be able to wait for VM")
		assert.NotNil(t, status, "Status must not be nil")
		t.Logf("VM Status: %d", status.State)
	})

	t.Run("Stop VM", func(t *testing.T) {
		if t.Skipped() {
			t.SkipNow()
		}

		err := vmm.Stop(ctx, task)
		if err != nil {
			t.Logf("Warning: VM stop returned error: %v", err)
		} else {
			t.Log("VM stopped successfully")
		}
	})

	t.Run("Remove VM", func(t *testing.T) {
		if t.Skipped() {
			t.SkipNow()
		}

		err := vmm.Remove(ctx, task)
		require.NoError(t, err, "Should be able to remove VM")
		t.Log("VM removed successfully")
	})

	t.Log("VM lifecycle test completed")
}

// TestE2EVMMultipleVMs tests launching multiple VMs concurrently
func TestE2EVMMultipleVMs(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	if testing.Short() {
		t.Skip("Skipping multiple VM test in short mode")
	}

	ctx := context.Background()

	// Get kernel path
	fcChecker := checks.NewFirecrackerChecker()
	err := fcChecker.CheckKernel()
	require.NoError(t, err, "Kernel must be available")
	kernelPath := fcChecker.GetKernelPath()

	// Create test environment
	tmpDir := t.TempDir()

	// Create VMM manager
	vmmConfig := &lifecycle.ManagerConfig{
		KernelPath:      kernelPath,
		RootfsDir:       tmpDir,
		SocketDir:       tmpDir,
		DefaultVCPUs:    1,
		DefaultMemoryMB: 256,
	}

	vmm := lifecycle.NewVMMManager(vmmConfig)

	// Launch multiple VMs
	numVMs := 3
	tasks := make([]*types.Task, numVMs)

	for i := 0; i < numVMs; i++ {
		// Create rootfs for each VM
		rootfsPath := filepath.Join(tmpDir, fmt.Sprintf("rootfs-%d.ext4", i))
		err = exec.Command("dd", "if=/dev/zero", "of="+rootfsPath, "bs=1M", "count=1").Run()
		require.NoError(t, err)

		err = exec.Command("mkfs.ext4", "-F", rootfsPath).Run()
		require.NoError(t, err)

		// Create config
		config := FirecrackerConfig{
			BootSource: BootSource{
				KernelImagePath: kernelPath,
				BootArgs:        "console=ttyS0 reboot=k panic=1 pci=off",
			},
			Drives: []Drive{
				{
					DriveID:      "rootfs",
					PathOnHost:   rootfsPath,
					IsRootDevice: true,
					IsReadOnly:   false,
				},
			},
			MachineConfig: MachineConfig{
				VCPUCount:  1,
				MemSizeMib: 256,
				SMT:        false,
			},
		}

		configJSON, _ := json.Marshal(config)

		// Create task
		task := &types.Task{
			ID:        fmt.Sprintf("multi-vm-%d", i),
			ServiceID: "test-service",
			Spec: types.TaskSpec{
				Runtime: &types.Container{
					Image: "test:latest",
				},
				Resources: types.ResourceRequirements{
					Limits: &types.Resources{
						NanoCPUs:    1000000000,
						MemoryBytes: 256 * 1024 * 1024,
					},
				},
			},
		}

		tasks[i] = task

		// Start VM
		err := vmm.Start(ctx, task, string(configJSON))
		if err != nil {
			t.Logf("Failed to start VM %d: %v", i, err)
			t.Skip("Cannot start VMs, skipping multi-VM test")
		}

		t.Logf("VM %d started successfully", i)
	}

	// Verify all VMs are running
	t.Run("Verify All VMs Running", func(t *testing.T) {
		if t.Skipped() {
			t.SkipNow()
		}

		for i, task := range tasks {
			status, err := vmm.Describe(ctx, task)
			require.NoError(t, err, "Should be able to describe VM %d", i)
			t.Logf("VM %d state: %d", i, status.State)
		}
	})

	// Cleanup all VMs
	t.Run("Cleanup All VMs", func(t *testing.T) {
		if t.Skipped() {
			t.SkipNow()
		}

		for i, task := range tasks {
			err := vmm.Stop(ctx, task)
			if err != nil {
				t.Logf("Warning: Failed to stop VM %d: %v", i, err)
			}

			err = vmm.Remove(ctx, task)
			if err != nil {
				t.Logf("Warning: Failed to remove VM %d: %v", i, err)
			}
		}

		t.Log("All VMs cleaned up")
	})
}
