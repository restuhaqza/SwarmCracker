// Package storage provides secret and config management for SwarmCracker.
package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// Injectable function variables for testing
var (
	execCommand     = exec.Command
	osMkdirTemp     = os.MkdirTemp
	osMkdirAllStore = os.MkdirAll
	osWriteFileStore = os.WriteFile
	osRemoveAllStore = os.RemoveAll
)

// SecretManager manages secrets and configs injection into container rootfs.
type SecretManager struct {
	secretsDir string // Directory for persistent secrets storage
	configsDir string // Directory for persistent configs storage
	mu         sync.Mutex
}

// NewSecretManager creates a new SecretManager.
func NewSecretManager(secretsDir, configsDir string) *SecretManager {
	// Create directories if they don't exist
	if secretsDir != "" {
		osMkdirAllStore(secretsDir, 0700)
	}
	if configsDir != "" {
		osMkdirAllStore(configsDir, 0755)
	}

	return &SecretManager{
		secretsDir: secretsDir,
		configsDir: configsDir,
	}
}

// InjectSecrets injects SwarmKit secrets into the container rootfs.
func (sm *SecretManager) InjectSecrets(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error {
	if len(secrets) == 0 {
		log.Debug().Str("task_id", taskID).Msg("No secrets to inject")
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	log.Info().
		Str("task_id", taskID).
		Int("count", len(secrets)).
		Msg("Injecting secrets into rootfs")

	// Mount the rootfs temporarily for injection
	mountDir, err := sm.mountRootfs(rootfsPath)
	if err != nil {
		return fmt.Errorf("failed to mount rootfs: %w", err)
	}
	defer sm.unmountRootfs(mountDir)

	for _, secret := range secrets {
		if err := sm.injectSecret(mountDir, secret); err != nil {
			log.Error().
				Str("task_id", taskID).
				Str("secret", secret.Name).
				Err(err).
				Msg("Failed to inject secret")
			return fmt.Errorf("failed to inject secret %s: %w", secret.Name, err)
		}

		log.Debug().
			Str("task_id", taskID).
			Str("secret", secret.Name).
			Str("target", secret.Target).
			Msg("Secret injected successfully")
	}

	log.Info().
		Str("task_id", taskID).
		Int("count", len(secrets)).
		Msg("All secrets injected successfully")

	return nil
}

// InjectConfigs injects SwarmKit configs into the container rootfs.
func (sm *SecretManager) InjectConfigs(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error {
	if len(configs) == 0 {
		log.Debug().Str("task_id", taskID).Msg("No configs to inject")
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	log.Info().
		Str("task_id", taskID).
		Int("count", len(configs)).
		Msg("Injecting configs into rootfs")

	// Mount the rootfs temporarily for injection
	mountDir, err := sm.mountRootfs(rootfsPath)
	if err != nil {
		return fmt.Errorf("failed to mount rootfs: %w", err)
	}
	defer sm.unmountRootfs(mountDir)

	for _, config := range configs {
		if err := sm.injectConfig(mountDir, config); err != nil {
			log.Error().
				Str("task_id", taskID).
				Str("config", config.Name).
				Err(err).
				Msg("Failed to inject config")
			return fmt.Errorf("failed to inject config %s: %w", config.Name, err)
		}

		log.Debug().
			Str("task_id", taskID).
			Str("config", config.Name).
			Str("target", config.Target).
			Msg("Config injected successfully")
	}

	log.Info().
		Str("task_id", taskID).
		Int("count", len(configs)).
		Msg("All configs injected successfully")

	return nil
}

// injectSecret injects a single secret into the rootfs.
func (sm *SecretManager) injectSecret(mountDir string, secret types.SecretRef) error {
	// Default to /run/secrets if target not specified
	targetPath := secret.Target
	if targetPath == "" {
		targetPath = filepath.Join("/run/secrets", secret.Name)
	}

	// Validate target path to prevent traversal attacks
	if err := validateInjectionPath(targetPath); err != nil {
		return fmt.Errorf("invalid target path: %w", err)
	}

	// Full path on the mounted rootfs
	fullPath := filepath.Join(mountDir, targetPath)

	// Verify the final path stays within mountDir
	cleanMount := filepath.Clean(mountDir)
	cleanFull := filepath.Clean(fullPath)
	if !strings.HasPrefix(cleanFull, cleanMount) {
		return fmt.Errorf("path escapes mount directory")
	}

	// Create parent directories
	if err := osMkdirAllStore(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write secret data with restrictive permissions (0400)
	if err := osWriteFileStore(fullPath, secret.Data, 0400); err != nil {
		return fmt.Errorf("failed to write secret file: %w", err)
	}

	log.Debug().
		Str("path", fullPath).
		Int("size", len(secret.Data)).
		Msg("Secret file written")

	return nil
}

// injectConfig injects a single config into the rootfs.
func (sm *SecretManager) injectConfig(mountDir string, config types.ConfigRef) error {
	// Default to /config if target not specified
	targetPath := config.Target
	if targetPath == "" {
		targetPath = filepath.Join("/config", config.Name)
	}

	// Validate target path to prevent traversal attacks
	if err := validateInjectionPath(targetPath); err != nil {
		return fmt.Errorf("invalid target path: %w", err)
	}

	// Full path on the mounted rootfs
	fullPath := filepath.Join(mountDir, targetPath)

	// Verify the final path stays within mountDir
	cleanMount := filepath.Clean(mountDir)
	cleanFull := filepath.Clean(fullPath)
	if !strings.HasPrefix(cleanFull, cleanMount) {
		return fmt.Errorf("path escapes mount directory")
	}

	// Create parent directories
	if err := osMkdirAllStore(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write config data with readable permissions (0444)
	if err := osWriteFileStore(fullPath, config.Data, 0444); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	log.Debug().
		Str("path", fullPath).
		Int("size", len(config.Data)).
		Msg("Config file written")

	return nil
}

// mountRootfs mounts an ext4 rootfs image temporarily.
func (sm *SecretManager) mountRootfs(rootfsPath string) (string, error) {
	// Create temp mount point
	mountDir, err := osMkdirTemp("", "swarmcracker-secrets-mount-")
	if err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Try to mount the image
	// This requires root privileges or user namespace setup
	if output, err := runCommand("mount", "-o", "loop", rootfsPath, mountDir); err != nil {
		osRemoveAllStore(mountDir)
		return "", fmt.Errorf("mount failed: %s: %w", output, err)
	}

	log.Debug().Str("path", mountDir).Msg("Rootfs mounted for secrets injection")
	return mountDir, nil
}

// unmountRootfs unmounts a temporary mount point and cleans up.
func (sm *SecretManager) unmountRootfs(mountDir string) {
	// Unmount
	if output, err := runCommand("umount", mountDir); err != nil {
		log.Warn().Err(err).Str("output", output).Msg("Failed to unmount rootfs")
	}

	// Cleanup temp dir
	osRemoveAllStore(mountDir)
	log.Debug().Str("path", mountDir).Msg("Rootfs unmounted and cleaned up")
}

// runCommand is a helper to run shell commands.
func runCommand(name string, args ...string) (string, error) {
	cmd := execCommand(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// validateInjectionPath validates a target path for secret/config injection.
// It rejects paths that could escape the target directory through traversal.
func validateInjectionPath(path string) error {
	// Reject null bytes
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("path contains null bytes")
	}

	// Reject paths containing ".." (path traversal)
	cleanPath := filepath.Clean(path)
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("path contains traversal sequence: %s", path)
	}

	// Ensure path is not empty
	if path == "" || cleanPath == "" || cleanPath == "." {
		return fmt.Errorf("path is empty")
	}

	return nil
}
