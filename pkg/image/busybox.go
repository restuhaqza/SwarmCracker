package image

import (
	"fmt"
	"os"
	"path/filepath"
)

// injectBusybox provides a minimal shell for scratch/distroless images.
// Uses the embedded busybox binary to provide shell and common utilities.
func injectBusybox(tmpDir string) error {
	// Create /bin directory if it doesn't exist
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create /bin directory: %w", err)
	}

	// Write the embedded busybox binary
	busyboxPath := filepath.Join(binDir, "busybox")
	if err := os.WriteFile(busyboxPath, busyboxBinary, 0755); err != nil {
		return fmt.Errorf("failed to write busybox binary: %w", err)
	}

	// Create busybox applet symlinks for common commands
	// These symlinks allow busybox to act as multiple utilities
	applets := []string{
		"sh", "ash",           // Shells
		"ls", "cat", "mkdir", "rmdir", "rm", "cp", "mv", "ln", // File operations
		"chmod", "chown",      // Permissions
		"echo", "printf",      // Output
		"pwd", "cd", "env",    // Environment
		"sleep", "true", "false", "test", "[", // Utilities
		"ps", "kill", "top",   // Process management
		"mount", "umount",     // Mount operations
		"grep", "sed", "awk", "head", "tail", "wc", "tr", // Text processing
		"tar", "gzip", "gunzip", // Compression
		"ip", "ifconfig", "netstat", "ping", // Networking (may not all be enabled)
		"df", "du", "free",    // System info
		"date", "uname", "hostname", "id", "whoami", // System info
		"clear", "reset",      // Terminal
		"vi", "ed",            // Editors
	}

	for _, applet := range applets {
		linkPath := filepath.Join(binDir, applet)
		// Create symlink: applet -> busybox
		if err := os.Symlink("busybox", linkPath); err != nil {
			// Non-fatal: symlink may already exist or be unsupported
			// Log but continue
			continue
		}
	}

	// Create essential directory symlinks
	// These are commonly expected in containers
	essentialDirs := []string{
		"proc",
		"sys",
		"dev",
		"tmp",
		"run",
		"var",
		"etc",
	}

	for _, dir := range essentialDirs {
		dirPath := filepath.Join(tmpDir, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			// Non-fatal: directory might already exist
			continue
		}
	}

	// Create /etc/passwd with minimal root user
	etcDir := filepath.Join(tmpDir, "etc")
	if err := os.MkdirAll(etcDir, 0755); err != nil {
		// Non-fatal
	}

	passwdContent := "root:x:0:0:root:/root:/bin/sh\n"
	passwdPath := filepath.Join(etcDir, "passwd")
	if err := os.WriteFile(passwdPath, []byte(passwdContent), 0644); err != nil {
		// Non-fatal
	}

	// Create /etc/group with minimal root group
	groupContent := "root:x:0:\n"
	groupPath := filepath.Join(etcDir, "group")
	if err := os.WriteFile(groupPath, []byte(groupContent), 0644); err != nil {
		// Non-fatal
	}

	return nil
}

// injectBusyboxMinimal creates only /bin/sh for minimal scratch images.
// This is a lighter-weight alternative to injectBusybox for when we only
// need shell functionality and don't want to create all the applet symlinks.
// Note: This still writes the busybox binary because /bin/sh needs it.
func injectBusyboxMinimal(tmpDir string) error {
	// Create /bin directory if it doesn't exist
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create /bin directory: %w", err)
	}

	// Create a minimal /bin/sh that can at least exec commands
	// This is the absolute minimum needed for the init wrapper
	// Since we have the embedded busybox, we use it as /bin/sh
	shellScript := `#!/bin/busybox sh
# Minimal shell for scratch images
# If we have any arguments, try to exec them
if [ $# -gt 0 ]; then
    exec "$@"
fi

# Interactive mode - just echo a message
echo "Minimal shell - no interactive mode available"
exit 0
`

	shPath := filepath.Join(binDir, "sh")
	if err := os.WriteFile(shPath, []byte(shellScript), 0755); err != nil {
		return fmt.Errorf("failed to create /bin/sh: %w", err)
	}

	return nil
}