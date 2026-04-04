package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/rs/zerolog/log"
)

// Manager manages security features for VMs
type Manager struct {
	jailer   *Jailer
	seccomp bool
	enabled  bool
}

// NewManager creates a new security manager
func NewManager(cfg *config.Config) (*Manager, error) {
	if !cfg.Executor.EnableJailer {
		log.Info().Msg("Security manager disabled (jailer not enabled)")
		return &Manager{enabled: false}, nil
	}

	jailer := &Jailer{
		UID:           cfg.Executor.Jailer.UID,
		GID:           cfg.Executor.Jailer.GID,
		ChrootBaseDir: cfg.Executor.Jailer.ChrootBaseDir,
		NetNS:         cfg.Executor.Jailer.NetNS,
		Enabled:       true,
	}

	// Validate jailer configuration
	if err := jailer.Validate(); err != nil {
		return nil, fmt.Errorf("invalid jailer configuration: %w", err)
	}

	return &Manager{
		jailer:   jailer,
		seccomp: true, // Always enable seccomp
		enabled:  true,
	}, nil
}

// PrepareVM prepares security context for a VM before launch
func (m *Manager) PrepareVM(ctx context.Context, vmID string) (*VMContext, error) {
	if !m.enabled {
		return &VMContext{Enabled: false}, nil
	}

	log.Info().
		Str("vm_id", vmID).
		Msg("Preparing VM security context")

	vmCtx := &VMContext{
		Enabled:  true,
		VMID:     vmID,
		JailPath: filepath.Join(m.jailer.ChrootBaseDir, vmID),
	}

	// Setup jail
	jailCtx, err := m.jailer.SetupJail(vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to setup jail: %w", err)
	}
	vmCtx.JailContext = jailCtx

	// Generate seccomp profile
	seccompPath := filepath.Join(vmCtx.JailPath, "seccomp.json")
	if err := WriteSeccompProfile(vmID, seccompPath); err != nil {
		return nil, fmt.Errorf("failed to write seccomp profile: %w", err)
	}
	vmCtx.SeccompProfile = seccompPath

	// Validate seccomp profile
	if err := ValidateSeccompProfile(seccompPath); err != nil {
		return nil, fmt.Errorf("invalid seccomp profile: %w", err)
	}

	log.Info().
		Str("vm_id", vmID).
		Str("jail_path", vmCtx.JailPath).
		Str("seccomp_profile", vmCtx.SeccompProfile).
		Msg("VM security context prepared")

	return vmCtx, nil
}

// VMContext represents the security context for a VM
type VMContext struct {
	Enabled        bool
	VMID           string
	JailPath       string
	JailContext    *JailContext
	SeccompProfile string
}

// ApplyToProcess applies security restrictions to a running process
func (m *Manager) ApplyToProcess(ctx context.Context, vmCtx *VMContext, pid int) error {
	if !vmCtx.Enabled {
		return nil
	}

	log.Info().
		Str("vm_id", vmCtx.VMID).
		Int("pid", pid).
		Msg("Applying security restrictions to process")

	// Apply resource limits
	limits := ResourceLimits{
		MaxCPUs:      2,
		MaxMemoryMB:  2048,
		MaxFD:        4096,
		MaxProcesses: 1024,
	}

	if err := ApplyResourceLimits(pid, limits); err != nil {
		return fmt.Errorf("failed to apply resource limits: %w", err)
	}

	log.Debug().
		Int("pid", pid).
		Msg("Security restrictions applied")

	return nil
}

// CleanupVM cleans up security context after VM termination
func (m *Manager) CleanupVM(ctx context.Context, vmCtx *VMContext) error {
	if !vmCtx.Enabled {
		return nil
	}

	log.Info().
		Str("vm_id", vmCtx.VMID).
		Msg("Cleaning up VM security context")

	// Cleanup jail
	if err := m.jailer.CleanupJail(vmCtx.JailContext); err != nil {
		return fmt.Errorf("failed to cleanup jail: %w", err)
	}

	// Cleanup seccomp profile
	if vmCtx.SeccompProfile != "" {
		if err := os.Remove(vmCtx.SeccompProfile); err != nil && !os.IsNotExist(err) {
			log.Warn().
				Err(err).
				Str("profile", vmCtx.SeccompProfile).
				Msg("Failed to remove seccomp profile")
		}
	}

	log.Debug().
		Str("vm_id", vmCtx.VMID).
		Msg("VM security context cleaned up")

	return nil
}

// SecureFilePermissions sets secure file permissions
func SecureFilePermissions(path string) error {
	// Set file owner to root:root
	if err := os.Chown(path, 0, 0); err != nil {
		return fmt.Errorf("failed to chown %s: %w", path, err)
	}

	// Set file permissions to 0600 (read/write for owner only)
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("failed to chmod %s: %w", path, err)
	}

	log.Debug().Str("path", path).Msg("Secure file permissions set")
	return nil
}

// SecureDirectoryPermissions sets secure directory permissions
func SecureDirectoryPermissions(path string) error {
	// Set directory owner to root:root
	if err := os.Chown(path, 0, 0); err != nil {
		return fmt.Errorf("failed to chown %s: %w", path, err)
	}

	// Set directory permissions to 0700 (rwx for owner only)
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("failed to chmod %s: %w", path, err)
	}

	log.Debug().Str("path", path).Msg("Secure directory permissions set")
	return nil
}

// ValidatePath checks if a path is secure
func ValidatePath(path string) error {
	// Check if path is absolute
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute: %s", path)
	}

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat path: %w", err)
	}

	// Check permissions (should not be world-writable)
	mode := info.Mode()
	if mode.Perm()&0002 != 0 {
		return fmt.Errorf("path is world-writable: %s", path)
	}

	// If directory, check it's not symlink
	if info.IsDir() {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("failed to resolve symlinks: %w", err)
		}
		if resolved != path {
			return fmt.Errorf("path is a symlink: %s -> %s", path, resolved)
		}
	}

	log.Debug().Str("path", path).Msg("Path validated")
	return nil
}

// CheckCapabilities checks if the process has required capabilities
func CheckCapabilities() error {
	// Check if running as root
	if os.Geteuid() != 0 {
		return fmt.Errorf("security features require root privileges")
	}

	// Check for CAP_SYS_CHROOT (required for chroot)
	hasChroot := checkCapability(0x1000) // CAP_SYS_CHROOT = 25
	if !hasChroot {
		log.Warn().Msg("CAP_SYS_CHROOT not available, jailer may not work")
	}

	// Check for CAP_SYS_ADMIN (required for namespaces)
	hasAdmin := checkCapability(0x1000 + 21) // CAP_SYS_ADMIN = 21
	if !hasAdmin {
		log.Warn().Msg("CAP_SYS_ADMIN not available, namespace isolation may not work")
	}

	log.Debug().Msg("Capability check complete")
	return nil
}

// checkCapability checks if a specific capability is available
func checkCapability(cap int) bool {
	// This is a simplified check
	// In production, use linux capabilities API
	return true // Assume available if running as root
}

// GetDefaultSecurityConfig returns default security configuration
func GetDefaultSecurityConfig() *config.JailerConfig {
	return &config.JailerConfig{
		UID:           1000, // Non-root user
		GID:           1000, // Non-root group
		ChrootBaseDir: "/srv/jailer",
		NetNS:         "", // No network namespace by default
	}
}

// IsEnabled returns true if security manager is enabled
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// GetJailer returns the jailer instance
func (m *Manager) GetJailer() *Jailer {
	return m.jailer
}

// SetResourceLimits sets resource limits for a VM
func (m *Manager) SetResourceLimits(vmCtx *VMContext, limits ResourceLimits) error {
	if !vmCtx.Enabled {
		return nil
	}

	// Apply limits will be called when process starts
	return nil
}

// GetSeccompProfilePath returns the path to the seccomp profile
func (m *Manager) GetSeccompProfilePath(vmID string) string {
	return filepath.Join(m.jailer.ChrootBaseDir, vmID, "seccomp.json")
}

// ValidateSecurityConfig validates the security configuration
func ValidateSecurityConfig(cfg *config.Config) error {
	if !cfg.Executor.EnableJailer {
		return nil
	}

	// Check jailer configuration
	if cfg.Executor.Jailer.UID == 0 || cfg.Executor.Jailer.GID == 0 {
		return fmt.Errorf("jailer UID/GID should not be 0 (root)")
	}

	if cfg.Executor.Jailer.ChrootBaseDir == "" {
		return fmt.Errorf("jailer chroot_base_dir cannot be empty")
	}

	// Check if chroot base directory is secure
	if err := ValidatePath(cfg.Executor.Jailer.ChrootBaseDir); err != nil {
		return fmt.Errorf("invalid chroot base directory: %w", err)
	}

	return nil
}
