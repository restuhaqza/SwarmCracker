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
		// Alpine-family images ship a busybox /sbin/init which only reads
		// /etc/inittab. That inittab does not launch the OCI ENTRYPOINT/CMD,
		// so preserving it leaves the microVM running with no workload.
		// Install our OCI-aware tini wrapper instead.
		if initIsBusybox(tmpDir) {
			log.Info().Msg("busybox-style init detected; injecting tini wrapper to run OCI command")
			return ii.injectTiniIntoDir(tmpDir, info)
		}
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
	// Remove any pre-existing file/symlink so we never write through it.
	_ = os.Remove(tiniPath)
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
		args := make([]string, 0, 2+len(containerArgs))
		args = append(args, "/sbin/tini", "--")
		args = append(args, containerArgs...)
		return args
	case InitSystemDumbInit:
		// dumb-init runs as: dumb-init <command> <args...>
		args := make([]string, 0, 1+len(containerArgs))
		args = append(args, "/sbin/dumb-init")
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
func (ii *InitInjector) GetGracePeriod() int {
	return ii.config.GracePeriodSec
}

// IsEnabled returns true if an init system is configured.
func (ii *InitInjector) IsEnabled() bool {
	return ii.config.Type != InitSystemNone
}
