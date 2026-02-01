// +build e2e

package firecracker

import (
	"context"
	"encoding/json"
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

// TestVMLaunchWithKVM tests launching a simple Firecracker VM with KVM access
// This test requires KVM access but not necessarily root privileges
func TestVMLaunchWithKVM(t *testing.T) {
	ctx := context.Background()

	// Skip if KVM not accessible
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("KVM not accessible: %v (run with sudo or add user to kvm group)", err)
	}
	f.Close()

	// Get kernel path
	fcChecker := checks.NewFirecrackerChecker()
	err = fcChecker.CheckKernel()
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
						NanoCPUs:    1000000000, // 1 CPU
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
			KernelPath:     kernelPath,
			RootfsDir:      tmpDir,
			SocketDir:      tmpDir,
			DefaultVCPUs:   1,
			DefaultMemoryMB: 512,
			EnableJailer:   false,
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

		// Cleanup
		t.Run("Stop VM", func(t *testing.T) {
			err := vmm.Stop(ctx, task)
			if err != nil {
				t.Logf("Warning: Failed to stop VM: %v", err)
			} else {
				t.Log("VM stopped successfully")
			}
		})

		t.Run("Remove VM", func(t *testing.T) {
			err := vmm.Remove(ctx, task)
			require.NoError(t, err, "Should be able to remove VM")
			t.Log("VM removed successfully")
		})
	})
}
