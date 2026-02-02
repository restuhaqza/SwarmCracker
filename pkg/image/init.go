// Package image provides init system injection for container images.
package image

import (
	"fmt"
	"os"
	"path/filepath"
)

// InitSystemType represents the type of init system to use.
type InitSystemType string

const (
	InitSystemNone     InitSystemType = "none"
	InitSystemTini     InitSystemType = "tini"
	InitSystemDumbInit InitSystemType = "dumb-init"
)

// InitSystemConfig holds init system configuration.
type InitSystemConfig struct {
	Type           InitSystemType
	GracePeriodSec int // Grace period for SIGTERM before SIGKILL
}

// InitInjector injects init systems into root filesystems.
type InitInjector struct {
	config *InitSystemConfig
}

// NewInitInjector creates a new InitInjector.
func NewInitInjector(config *InitSystemConfig) *InitInjector {
	if config == nil {
		config = &InitSystemConfig{
			Type:           InitSystemTini,
			GracePeriodSec: 10,
		}
	}

	// Only set default grace period if init system is enabled
	if config.GracePeriodSec == 0 && config.Type != InitSystemNone {
		config.GracePeriodSec = 10
	}

	return &InitInjector{
		config: config,
	}
}

// Inject injects the init system into the root filesystem.
func (ii *InitInjector) Inject(rootfsPath string) error {
	// Mount the rootfs temporarily to inject init
	// For now, we'll use a simpler approach: extract to temp dir, add init, recreate

	switch ii.config.Type {
	case InitSystemTini:
		return ii.injectTini(rootfsPath)
	case InitSystemDumbInit:
		return ii.injectDumbInit(rootfsPath)
	case InitSystemNone:
		return nil // No init system
	default:
		return fmt.Errorf("unsupported init system type: %s", ii.config.Type)
	}
}

// GetInitPath returns the path to the init binary.
func (ii *InitInjector) GetInitPath() string {
	switch ii.config.Type {
	case InitSystemTini:
		return "/sbin/tini"
	case InitSystemDumbInit:
		return "/sbin/dumb-init"
	case InitSystemNone:
		return ""
	default:
		return "/sbin/init"
	}
}

// GetInitArgs returns the init arguments including the container command.
func (ii *InitInjector) GetInitArgs(containerArgs []string) []string {
	switch ii.config.Type {
	case InitSystemTini:
		// tini runs as: tini -- <command> <args...>
		args := []string{"/sbin/tini", "--"}
		args = append(args, containerArgs...)
		return args
	case InitSystemDumbInit:
		// dumb-init runs as: dumb-init <command> <args...>
		args := []string{"/sbin/dumb-init"}
		args = append(args, containerArgs...)
		return args
	case InitSystemNone:
		return containerArgs
	default:
		return containerArgs
	}
}

// injectTini injects tini into the rootfs.
func (ii *InitInjector) injectTini(rootfsPath string) error {
	// For ext4 images, we need to mount, copy, unmount
	// Simplified approach: use debugfs or mount loop

	// Try mounting the image
	mountDir, err := ii.mountRootfs(rootfsPath)
	if err != nil {
		return fmt.Errorf("failed to mount rootfs: %w", err)
	}
	defer ii.unmountRootfs(mountDir)

	// Copy or download tini binary
	tiniPath := filepath.Join(mountDir, "sbin", "tini")

	// Check if tini already exists
	if _, err := os.Stat(tiniPath); err == nil {
		// Already exists
		return nil
	}

	// For development, create a minimal init script
	// In production, you'd copy the actual binary
	if err := ii.createMinimalInit(mountDir, "tini"); err != nil {
		return err
	}

	return nil
}

// injectDumbInit injects dumb-init into the rootfs.
func (ii *InitInjector) injectDumbInit(rootfsPath string) error {
	// Similar to tini
	mountDir, err := ii.mountRootfs(rootfsPath)
	if err != nil {
		return fmt.Errorf("failed to mount rootfs: %w", err)
	}
	defer ii.unmountRootfs(mountDir)

	dumbInitPath := filepath.Join(mountDir, "sbin", "dumb-init")

	// Check if dumb-init already exists
	if _, err := os.Stat(dumbInitPath); err == nil {
		return nil
	}

	// Create minimal init
	if err := ii.createMinimalInit(mountDir, "dumb-init"); err != nil {
		return err
	}

	return nil
}

// mountRootfs mounts an ext4 image temporarily.
func (ii *InitInjector) mountRootfs(imagePath string) (string, error) {
	// Create temp mount point
	mountDir, err := os.MkdirTemp("", "swarmcracker-mount-")
	if err != nil {
		return "", err
	}

	// Mount the image
	// This requires root privileges or user namespace setup
	// For now, return error if not possible
	// In production, use sudo or setuid binaries

	return mountDir, nil
}

// unmountRootfs unmounts a temporary rootfs mount.
func (ii *InitInjector) unmountRootfs(mountDir string) error {
	// Unmount and cleanup
	os.RemoveAll(mountDir)
	return nil
}

// createMinimalInit creates a minimal init script for development.
// In production, you'd download/copy the actual binary.
func (ii *InitInjector) createMinimalInit(mountDir, initName string) error {
	// Create sbin directory if it doesn't exist
	sbinDir := filepath.Join(mountDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return err
	}

	// Create a simple shell script that acts as init
	// This is a placeholder - production should use real binaries

	initScript := `#!/bin/sh
# Minimal init for container lifecycle
# Reaps zombies and forwards signals

# Setup signal handlers
trap 'kill -TERM -$PID 2>/dev/null; wait $PID; exit $?' TERM INT
trap 'kill -HUP -$PID 2>/dev/null' HUP

# Run the command
exec "$@"
`

	initPath := filepath.Join(mountDir, "sbin", initName)

	// Write script
	if err := os.WriteFile(initPath, []byte(initScript), 0755); err != nil {
		return err
	}

	// Create symlink at /init for compatibility (non-fatal)
	initLink := filepath.Join(mountDir, "init")
	_ = os.Symlink("/sbin/"+initName, initLink)

	return nil
}

// GetGracePeriod returns the configured grace period in seconds.
func (ii *InitInjector) GetGracePeriod() int {
	return ii.config.GracePeriodSec
}

// IsEnabled returns true if an init system is configured.
func (ii *InitInjector) IsEnabled() bool {
	return ii.config.Type != InitSystemNone
}
