// Package storage provides secret and config management for SwarmCracker.
package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog/log"
)

// Injectable function variables for testing
var (
	execCommand      = exec.Command
	osMkdirTemp      = os.MkdirTemp
	osMkdirAllStore  = os.MkdirAll
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
		if err := osMkdirAllStore(secretsDir, 0700); err != nil {
			log.Warn().Err(err).Msg("Failed to create secrets dir")
		}
	}
	if configsDir != "" {
		if err := osMkdirAllStore(configsDir, 0755); err != nil {
			log.Warn().Err(err).Msg("Failed to create configs dir")
		}
	}

	// Check that debugfs is available for secret/config injection
	if _, err := exec.LookPath("debugfs"); err != nil {
		log.Warn().Msg("debugfs not found in PATH — secret/config injection will fail. Install e2fsprogs.")
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

	for _, secret := range secrets {
		mode := secret.Mode
		if mode == 0 {
			mode = 0400
		}
		if err := sm.injectFileViaDebugfs(rootfsPath, secret.Target, "/run/secrets/"+secret.Name, secret.Data, mode, secret.UID, secret.GID); err != nil {
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

	sm.repairExt4(rootfsPath)

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

	for _, config := range configs {
		mode := config.Mode
		if mode == 0 {
			mode = 0444
		}
		if err := sm.injectFileViaDebugfs(rootfsPath, config.Target, "/config/"+config.Name, config.Data, mode, config.UID, config.GID); err != nil {
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

	sm.repairExt4(rootfsPath)

	return nil
}
func (sm *SecretManager) injectFileViaDebugfs(ext4Path, target, defaultName string, data []byte, mode os.FileMode, uid, gid string) error {
	targetPath := target
	if targetPath == "" {
		targetPath = defaultName
	}

	// Validate target path to prevent traversal attacks
	if err := validateInjectionPath(targetPath); err != nil {
		return fmt.Errorf("invalid target path: %w", err)
	}

	// Write to temp file first
	tmpFile, err := osMkdirTemp("", "swarmcracker-inject-")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = osRemoveAllStore(tmpFile) }()

	filePath := filepath.Join(tmpFile, filepath.Base(targetPath))
	if err := osWriteFileStore(filePath, data, mode); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Ensure parent directory exists in the ext4 image
	parentDir := filepath.Dir(targetPath)
	if parentDir != "/" && parentDir != "." {
		mkdirCmd := execCommand("debugfs", "-w", "-R",
			fmt.Sprintf("mkdir %s", parentDir),
			ext4Path)
		mkdirOutput, mkdirErr := mkdirCmd.CombinedOutput()
		if mkdirErr != nil {
			mkdirStr := string(mkdirOutput)
			// mkdir fails with "File exists" if the directory already exists — ignore that
			if !strings.Contains(mkdirStr, "File exists") {
				return fmt.Errorf("debugfs mkdir %s failed: %s: %w", parentDir, mkdirStr, mkdirErr)
			}
		}
	}

	// Use debugfs to write into ext4 without mounting
	cmd := execCommand("debugfs", "-w", "-R",
		fmt.Sprintf("write %s %s", filePath, targetPath),
		ext4Path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("debugfs write failed: %s: %w", string(output), err)
	}

	// debugfs exits with code 0 even for certain errors (quirk)
	// Check output for error indicators
	outputStr := string(output)
	if strings.Contains(outputStr, "Filesystem not open") ||
		strings.Contains(outputStr, "No such file or directory") ||
		strings.Contains(outputStr, "while trying to open") {
		return fmt.Errorf("debugfs write failed: %s", outputStr)
	}

	// debugfs write creates the file with default permissions and does not
	// honor the source file's mode, so set the inode attributes explicitly.
	sm.applyInodeAttributes(ext4Path, targetPath, mode, uid, gid)

	log.Debug().
		Str("target", targetPath).
		Int("size", len(data)).
		Msg("File injected via debugfs")

	return nil
}

// applyInodeAttributes sets the mode (and optionally uid/gid) on a file that
// was just written into an ext4 image. Each field is set with a separate
// debugfs set_inode_field call. Failures are logged but not fatal: a file with
// default permissions is still preferable to no file at all.
func (sm *SecretManager) applyInodeAttributes(ext4Path, target string, mode os.FileMode, uid, gid string) {
	perm := mode.Perm()
	if perm == 0 {
		perm = 0400
	}
	// Regular file type (0o100000) | permission bits.
	fullMode := uint32(0o100000) | uint32(perm)
	sm.debugfsSetInodeField(ext4Path, target, "mode", fmt.Sprintf("0%o", fullMode))
	if uid != "" {
		sm.debugfsSetInodeField(ext4Path, target, "uid", uid)
	}
	if gid != "" {
		sm.debugfsSetInodeField(ext4Path, target, "gid", gid)
	}
}

func (sm *SecretManager) debugfsSetInodeField(ext4Path, target, field, value string) {
	cmd := execCommand("debugfs", "-w", "-R", fmt.Sprintf("sif %s %s %s", target, field, value), ext4Path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Warn().
			Str("target", target).
			Str("field", field).
			Str("value", value).
			Str("output", string(output)).
			Err(err).
			Msg("Failed to set inode attribute via debugfs")
	}
}

// repairExt4 replays the ext4 journal and repairs metadata checksums after a
// debugfs write. debugfs modifies the filesystem directly and does not update
// the journal; a guest that boots the image then replays a stale journal (or
// validates a stale bitmap checksum) and reports corruption. Running e2fsck -fy
// makes the image self-consistent before it is booted.
func (sm *SecretManager) repairExt4(rootfsPath string) {
	if rootfsPath == "" {
		return
	}
	cmd := execCommand("e2fsck", "-fy", rootfsPath)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	// e2fsck exits 1 when it corrected errors; that is success for us.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return
	}
	log.Warn().
		Err(err).
		Str("rootfs", rootfsPath).
		Str("output", string(output)).
		Msg("e2fsck repair after secret/config injection failed")
}
func validateInjectionPath(path string) error {
	// Reject null bytes
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("path contains null bytes")
	}

	// Reject whitespace and control characters (newline, tab, CR, etc.).
	// debugfs -R parses commands separated by newlines, so a newline in a
	// target path would let an attacker inject a second debugfs command
	// (e.g. "write /run/secrets/x\nrm /"). Spaces/tabs also break debugfs
	// argument tokenization, so reject all whitespace.
	for _, r := range path {
		if unicode.IsSpace(r) || r == 0x7f {
			return fmt.Errorf("path contains whitespace or control character: %q", path)
		}
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
