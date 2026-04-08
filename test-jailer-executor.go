// +build ignore

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
)

func main() {
	// Create jailer config
	cfg := &jailer.Config{
		FirecrackerPath: "/usr/local/bin/firecracker",
		JailerPath:      "/usr/local/bin/jailer",
		ChrootBaseDir:   "/var/lib/swarmcracker/jailer",
		UID:             998,
		GID:             998,
		CgroupVersion:   "v2",
		EnableSeccomp:   true,
	}

	// Create jailer instance
	j, err := jailer.New(cfg)
	if err != nil {
		fmt.Printf("Failed to create jailer: %v\n", err)
		os.Exit(1)
	}

	// Create VM config
	vmCfg := jailer.VMConfig{
		TaskID:     "jailer-test-vm",
		VcpuCount:  1,
		MemoryMB:   256,
		KernelPath: "/usr/share/firecracker/vmlinux",
		RootfsPath: "/var/lib/firecracker/rootfs/alpine-latest.ext4",
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off",
	}

	fmt.Println("Starting jailed Firecracker VM...")

	// Start jailed VM
	process, err := j.Start(context.Background(), vmCfg)
	if err != nil {
		fmt.Printf("Failed to start jailed VM: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Jailed VM started successfully!\n")
	fmt.Printf("  PID: %d\n", process.Pid)
	fmt.Printf("  Socket: %s\n", process.SocketPath)
	fmt.Printf("  TaskID: %s\n", process.TaskID)

	// Wait a bit
	fmt.Println("\nVM running for 10 seconds...")
	time.Sleep(10 * time.Second)

	// Stop the VM
	fmt.Println("\nStopping VM...")
	if err := j.Stop(context.Background(), vmCfg.TaskID); err != nil {
		fmt.Printf("Failed to stop VM: %v\n", err)
	}

	fmt.Println("VM stopped successfully!")
}