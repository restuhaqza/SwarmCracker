// Package image provides init system injection for container images.
package image

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
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

// tiniBinary is declared in embedded_binaries.go via go:embed.

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

// Inject has been removed. Use InjectIntoDir instead.
// This stub exists only for backward compatibility with test code.
// It logs a deprecation warning and returns nil (no-op).
func (ii *InitInjector) Inject(rootfsPath string) error {
	log.Warn().Str("rootfs", rootfsPath).Msg("InitInjector.Inject is deprecated and is a no-op; use InjectIntoDir before ext4 creation")
	return nil
}

// InjectIntoDir injects the init system directly into a directory.
// This should be called BEFORE createExt4Image() so that the init files
// are included in the final rootfs image.
// It first detects the existing init system type and decides what action to take:
// - Incompatible (systemd): returns error
// - Scratch: injects busybox first, then tini
// - Existing init (tini, dumb-init, OpenRC, sysvinit): preserves and creates /init symlink
// - None/Unknown: injects tini
// OCIImageInfo is used to generate the correct init wrapper with ENTRYPOINT/CMD/ENV/USER/WORKDIR.
func (ii *InitInjector) InjectIntoDir(tmpDir string, info *OCIImageInfo) error {
	if !ii.IsEnabled() {
		return nil
	}

	// Detect init type
	result := DetectInitType(tmpDir)
	log.Info().Str("type", string(result.Type)).Msg("Detected init type")

	switch result.Type {
	case InitTypeIncompatible:
		return fmt.Errorf("incompatible image: %s", result.Message)
	case InitTypeScratch:
		if err := injectBusybox(tmpDir); err != nil {
			return fmt.Errorf("failed to inject busybox: %w", err)
		}
		// Fall through to inject tini
		fallthrough
	case InitTypeNone, InitTypeUnknown:
		return ii.injectTiniIntoDir(tmpDir, info)
	case InitTypeTini, InitTypeDumbInit, InitTypeOpenRC, InitTypeSysvinit:
		// Preserve existing init, just create /init symlink
		initLink := filepath.Join(tmpDir, "init")
		_ = os.Remove(initLink)
		return os.Symlink("/sbin/init", initLink)
	default:
		return ii.injectTiniIntoDir(tmpDir, info)
	}
}

// injectTiniIntoDir injects tini into the directory with OCI-aware wrapper.
func (ii *InitInjector) injectTiniIntoDir(tmpDir string, info *OCIImageInfo) error {
	// Ensure /sbin directory exists
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return fmt.Errorf("failed to create sbin directory: %w", err)
	}

	// Write the embedded tini binary to /sbin/tini
	tiniPath := filepath.Join(sbinDir, "tini")
	if err := os.WriteFile(tiniPath, tiniBinary, 0755); err != nil {
		return fmt.Errorf("failed to write tini binary: %w", err)
	}

	// Create /sbin/init wrapper using OCI config
	if err := createGenericInitWrapper(tmpDir, info, ii.config.GracePeriodSec); err != nil {
		return fmt.Errorf("failed to create generic init wrapper: %w", err)
	}

	// Create /init symlink -> /sbin/init
	initLink := filepath.Join(tmpDir, "init")
	// Remove existing symlink if present
	_ = os.Remove(initLink)
	if err := os.Symlink("/sbin/init", initLink); err != nil {
		return fmt.Errorf("failed to create /init symlink: %w", err)
	}

	return nil
}

// injectDumbInitIntoDir injects dumb-init into the directory.
func (ii *InitInjector) injectDumbInitIntoDir(tmpDir string) error {
	// Ensure /sbin directory exists
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return fmt.Errorf("failed to create sbin directory: %w", err)
	}

	// Create dumb-init script (similar to tini)
	dumbInitScript := `#!/bin/sh
# Minimal dumb-init compatible init
# Reaps zombies and forwards signals

trap 'kill -TERM -$PID 2>/dev/null; wait $PID; exit $?' TERM INT
trap 'kill -HUP -$PID 2>/dev/null' HUP

exec "$@"
`
	dumbInitPath := filepath.Join(sbinDir, "dumb-init")
	if err := os.WriteFile(dumbInitPath, []byte(dumbInitScript), 0755); err != nil {
		return fmt.Errorf("failed to write dumb-init script: %w", err)
	}

	// Create /sbin/init wrapper
	initScript := `#!/bin/sh
exec /sbin/dumb-init -- /bin/sh
`
	initPath := filepath.Join(sbinDir, "init")
	if err := os.WriteFile(initPath, []byte(initScript), 0755); err != nil {
		return fmt.Errorf("failed to write init wrapper: %w", err)
	}

	// Create /init symlink -> /sbin/init
	initLink := filepath.Join(tmpDir, "init")
	_ = os.Remove(initLink)
	if err := os.Symlink("/sbin/init", initLink); err != nil {
		return fmt.Errorf("failed to create /init symlink: %w", err)
	}

	return nil
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

// injectTini and injectDumbInit have been removed.
// These stubs exist only for backward compatibility with test code.

func (ii *InitInjector) injectTini(rootfsPath string) error {
	log.Warn().Str("rootfs", rootfsPath).Msg("injectTini is deprecated (no-op); use injectTiniIntoDir via InjectIntoDir")
	return nil
}

func (ii *InitInjector) injectDumbInit(rootfsPath string) error {
	log.Warn().Str("rootfs", rootfsPath).Msg("injectDumbInit is deprecated (no-op); use injectDumbInitIntoDir via InjectIntoDir")
	return nil
}

// mountRootfs is deprecated — it never actually mounted the ext4 image.
// Stub exists for backward compatibility with test code.
func (ii *InitInjector) mountRootfs(imagePath string) (string, error) {
	log.Warn().Str("image", imagePath).Msg("mountRootfs is deprecated (no-op); use InjectIntoDir before ext4 creation")
	return os.MkdirTemp("", "swarmcracker-deprecated-mount-")
}

// unmountRootfs is deprecated. Stub for backward compatibility.
func (ii *InitInjector) unmountRootfs(mountDir string) error {
	return os.RemoveAll(mountDir)
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