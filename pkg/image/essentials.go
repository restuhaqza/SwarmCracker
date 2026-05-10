// Package image provides essential system files injection for rootfs.
package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog/log"
)

// injectEssentialFiles injects DNS, hosts, nsswitch, machine-id, and directories.
// These files are required for proper operation of container images in Firecracker VMs.
func injectEssentialFiles(tmpDir string, imageID string) error {
	// Inject /etc/resolv.conf
	if err := injectResolvConf(tmpDir); err != nil {
		return fmt.Errorf("failed to inject resolv.conf: %w", err)
	}

	// Inject /etc/hosts
	if err := injectHosts(tmpDir); err != nil {
		return fmt.Errorf("failed to inject hosts: %w", err)
	}

	// Inject /etc/nsswitch.conf (only for glibc-based images)
	if err := injectNsswitch(tmpDir); err != nil {
		// Non-critical, just log warning
		// This is informational only
	}

	// Inject /etc/machine-id
	if err := injectMachineID(tmpDir, imageID); err != nil {
		return fmt.Errorf("failed to inject machine-id: %w", err)
	}

	// Create essential directories
	if err := createEssentialDirs(tmpDir); err != nil {
		return fmt.Errorf("failed to create essential directories: %w", err)
	}

	return nil
}

// injectResolvConf creates /etc/resolv.conf if it doesn't exist.
func injectResolvConf(tmpDir string) error {
	resolvPath := filepath.Join(tmpDir, "etc", "resolv.conf")

	// Don't overwrite existing file
	if _, err := os.Stat(resolvPath); err == nil {
		return nil
	}

	// Ensure /etc directory exists
	if err := os.MkdirAll(filepath.Dir(resolvPath), 0755); err != nil {
		return err
	}

	// Create with Google DNS nameservers (common default)
	content := `nameserver 8.8.8.8
nameserver 8.8.4.4
options timeout:2 attempts:3
`

	return os.WriteFile(resolvPath, []byte(content), 0644)
}

// injectHosts creates /etc/hosts if it doesn't exist.
func injectHosts(tmpDir string) error {
	hostsPath := filepath.Join(tmpDir, "etc", "hosts")

	// Don't overwrite existing file
	if _, err := os.Stat(hostsPath); err == nil {
		return nil
	}

	// Ensure /etc directory exists
	if err := os.MkdirAll(filepath.Dir(hostsPath), 0755); err != nil {
		return err
	}

	// Create with localhost entries
	content := `127.0.0.1 localhost localhost.localdomain
::1       localhost
`

	return os.WriteFile(hostsPath, []byte(content), 0644)
}

// injectNsswitch creates /etc/nsswitch.conf if it doesn't exist AND image is glibc-based.
func injectNsswitch(tmpDir string) error {
	nsswitchPath := filepath.Join(tmpDir, "etc", "nsswitch.conf")

	// Don't overwrite existing file
	if _, err := os.Stat(nsswitchPath); err == nil {
		return nil
	}

	// Only create for glibc-based images (musl/Alpine doesn't use nsswitch.conf)
	if !isGlibc(tmpDir) {
		return nil
	}

	// Ensure /etc directory exists
	if err := os.MkdirAll(filepath.Dir(nsswitchPath), 0755); err != nil {
		return err
	}

	// Create nsswitch.conf for glibc
	content := `hosts:     files dns myhostname
`

	return os.WriteFile(nsswitchPath, []byte(content), 0644)
}

// injectMachineID creates /etc/machine-id if it doesn't exist.
// Uses SHA256 hash of imageID (first 16 bytes = 32 hex chars).
func injectMachineID(tmpDir string, imageID string) error {
	machineIDPath := filepath.Join(tmpDir, "etc", "machine-id")

	// Don't overwrite existing file
	if _, err := os.Stat(machineIDPath); err == nil {
		return nil
	}

	// Ensure /etc directory exists
	if err := os.MkdirAll(filepath.Dir(machineIDPath), 0755); err != nil {
		return err
	}

	// Generate machine-id from imageID
	// SHA256 hash, first 16 bytes = 32 hex characters
	hash := sha256.Sum256([]byte(imageID))
	machineID := hex.EncodeToString(hash[:16])

	return os.WriteFile(machineIDPath, []byte(machineID+"\n"), 0444)
}

// createEssentialDirs creates essential directories if they don't exist.
func createEssentialDirs(tmpDir string) error {
	// Essential directories with their permissions
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{"/tmp", 01777},       // sticky bit for temp
		{"/run", 0755},
		{"/var/run", 0755},
		{"/var/log", 0755},
		{"/var/tmp", 01777},   // sticky bit
		{"/var/cache", 0755},
		{"/root", 0700},       // root home, private
	}

	for _, d := range dirs {
		dirPath := filepath.Join(tmpDir, d.path)

		// Check if exists
		if fi, err := os.Stat(dirPath); err == nil {
			// Directory exists, ensure correct permissions
			if fi.IsDir() {
				// Always chmod to ensure correct permissions (including sticky bit)
				if err := syscall.Chmod(dirPath, uint32(d.mode)); err != nil {
					// Non-critical, continue
					log.Debug().Err(err).Str("path", d.path).Msg("Failed to chmod directory")
				}
			}
			continue
		}

		// Create directory
		if err := os.MkdirAll(dirPath, d.mode); err != nil {
			// Non-critical for some dirs, critical for /tmp
			if d.path == "/tmp" {
				return fmt.Errorf("failed to create %s: %w", d.path, err)
			}
			// Continue for others
			continue
		}

		// MkdirAll may not set sticky bit correctly, so chmod explicitly
		if err := syscall.Chmod(dirPath, uint32(d.mode)); err != nil {
			if d.path == "/tmp" {
				return fmt.Errorf("failed to chmod %s: %w", d.path, err)
			}
			// Continue for others
		}
	}

	return nil
}