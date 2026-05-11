package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newAssetCommand creates the asset command group
func newAssetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "asset",
		Short: "Manage Firecracker assets",
		Long: `Manage Firecracker assets like kernels and rootfs images.

Provides commands for downloading, verifying, and managing VM assets.`,
	}

	// Add subcommands
	cmd.AddCommand(newAssetKernelCommand())
	cmd.AddCommand(newAssetRootfsCommand())

	return cmd
}

// newAssetKernelCommand creates the kernel subcommand
func newAssetKernelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Manage Firecracker kernels",
		Long:  `Manage Firecracker kernel images for VMs.`,
	}

	cmd.AddCommand(newKernelListCommand())
	cmd.AddCommand(newKernelVerifyCommand())

	return cmd
}

// newKernelListCommand lists available kernels
func newKernelListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List available kernels",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listKernels()
		},
	}
}

// newKernelVerifyCommand verifies a kernel
func newKernelVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <kernel-path>",
		Short: "Verify kernel integrity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return verifyKernel(args[0])
		},
	}
}

// newAssetRootfsCommand creates the rootfs subcommand
func newAssetRootfsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rootfs",
		Short: "Manage rootfs images",
		Long:  `Manage rootfs images for Firecracker VMs.`,
	}

	cmd.AddCommand(newRootfsListCommand())

	return cmd
}

// newRootfsListCommand lists available rootfs
func newRootfsListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List available rootfs images",
		RunE: func(cmd *cobra.Command, args []string) error {
			return listRootfs()
		},
	}
}

// Asset helper functions

func listKernels() error {
	kernelPath := "/usr/share/firecracker/vmlinux"
	if envKernel := os.Getenv("KERNEL_PATH"); envKernel != "" {
		kernelPath = envKernel
	}

	// Check if kernel exists
	if _, err := os.Stat(kernelPath); err != nil {
		fmt.Printf("No kernel found at %s\n", kernelPath)
		fmt.Println("Download with: curl -fsSL https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155 -o /usr/share/firecracker/vmlinux")
		//nolint:nilerr
		return nil
	}

	// Get kernel info
	info, err := os.Stat(kernelPath)
	if err != nil {
		return err
	}

	fmt.Printf("Kernel: %s\n", kernelPath)
	fmt.Printf("Size: %.2f MB\n", float64(info.Size())/(1024*1024))
	fmt.Printf("Modified: %s\n", info.ModTime().Format("2006-01-02 15:04"))

	return nil
}

func verifyKernel(kernelPath string) error {
	if _, err := os.Stat(kernelPath); err != nil {
		return fmt.Errorf("kernel not found: %s", kernelPath)
	}

	// Check minimum size
	info, err := os.Stat(kernelPath)
	if err != nil {
		return err
	}

	if info.Size() < 1*1024*1024 {
		return fmt.Errorf("kernel too small (%d bytes) - may be corrupted", info.Size())
	}

	fmt.Printf("✅ Kernel verified: %s\n", kernelPath)
	fmt.Printf("Size: %.2f MB\n", float64(info.Size())/(1024*1024))

	return nil
}

func listRootfs() error {
	rootfsDir := "/var/lib/firecracker/rootfs"
	if envRootfs := os.Getenv("ROOTFS_DIR"); envRootfs != "" {
		rootfsDir = envRootfs
	}

	// Check if directory exists
	if _, err := os.Stat(rootfsDir); err != nil {
		fmt.Printf("No rootfs directory found at %s\n", rootfsDir)
		//nolint:nilerr
		return nil
	}

	// List rootfs files
	entries, err := os.ReadDir(rootfsDir)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Printf("No rootfs images found in %s\n", rootfsDir)
		return nil
	}

	fmt.Printf("Rootfs directory: %s\n", rootfsDir)
	fmt.Printf("\nImages:\n")
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".ext4" {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			fmt.Printf("  %s (%.2f MB)\n", entry.Name(), float64(info.Size())/(1024*1024))
		}
	}

	return nil
}
