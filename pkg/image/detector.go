package image

import (
	"fmt"
	"os"
	"path/filepath"
)

// InitType represents the detected init system type.
type InitType string

const (
	InitTypeNone         InitType = "none"         // No init system found
	InitTypeSystemd      InitType = "systemd"      // systemd (INCOMPATIBLE)
	InitTypeOpenRC       InitType = "openrc"       // OpenRC (Alpine)
	InitTypeSysvinit     InitType = "sysvinit"     // sysvinit
	InitTypeTini         InitType = "tini"         // Already has tini
	InitTypeDumbInit     InitType = "dumb-init"    // Already has dumb-init
	InitTypeScratch      InitType = "scratch"      // Empty/scratch image
	InitTypeUnknown      InitType = "unknown"      // Can't determine
	InitTypeIncompatible InitType = "incompatible" // Can't run in Firecracker
)

// InitTypeResult holds the result of init type detection.
type InitTypeResult struct {
	Type    InitType
	Message string // Human-readable explanation
}

// DetectInitType examines the extracted filesystem to determine init type.
// Detection checks in this order:
// 1. Scratch/empty image
// 2. Systemd (incompatible with Firecracker)
// 3. Existing tini
// 4. Existing dumb-init
// 5. OpenRC
// 6. sysvinit
// 7. None (no init system found)
func DetectInitType(tmpDir string) InitTypeResult {
	// Check if it's a scratch/empty image first
	if isScratch(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeScratch,
			Message: "scratch or minimal image with no shell or init system",
		}
	}

	// Check for systemd (incompatible with Firecracker)
	if isSystemd(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeIncompatible,
			Message: "systemd detected: Firecracker cannot run systemd (requires cgroups v2)",
		}
	}

	// Check for existing tini
	if hasTini(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeTini,
			Message: "existing tini init detected, will preserve",
		}
	}

	// Check for existing dumb-init
	if hasDumbInit(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeDumbInit,
			Message: "existing dumb-init detected, will preserve",
		}
	}

	// Check for OpenRC
	if hasOpenRC(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeOpenRC,
			Message: "OpenRC init system detected, will preserve",
		}
	}

	// Check for sysvinit
	if hasSysvinit(tmpDir) {
		return InitTypeResult{
			Type:    InitTypeSysvinit,
			Message: "sysvinit detected, will preserve",
		}
	}

	// No init system found
	return InitTypeResult{
		Type:    InitTypeNone,
		Message: "no init system detected, will inject tini",
	}
}

// isScratch checks if the image is essentially empty (scratch/distroless).
func isScratch(tmpDir string) bool {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return false
	}

	// Count meaningful entries (ignore lost+found and other system dirs)
	meaningfulCount := 0
	for _, entry := range entries {
		name := entry.Name()
		// Ignore common empty filesystem artifacts
		if name == "lost+found" || name == "." || name == ".." {
			continue
		}
		meaningfulCount++
	}

	// If no meaningful entries, it's scratch
	if meaningfulCount == 0 {
		return true
	}

	// Also check if /bin/sh is missing (common for scratch/distroless)
	// This catches images that have some files but no shell
	shPath := filepath.Join(tmpDir, "bin", "sh")
	if _, err := os.Stat(shPath); os.IsNotExist(err) {
		// No /bin/sh - check if this is a minimal image
		// If there's no /bin or /usr/bin, consider it scratch-like
		binDir := filepath.Join(tmpDir, "bin")
		usrBinDir := filepath.Join(tmpDir, "usr", "bin")
		if _, err := os.Stat(binDir); os.IsNotExist(err) {
			if _, err := os.Stat(usrBinDir); os.IsNotExist(err) {
				return true
			}
		}
	}

	return false
}

// isSystemd checks if systemd is the init system.
func isSystemd(tmpDir string) bool {
	// Check for systemd binary locations
	systemdPaths := []string{
		filepath.Join(tmpDir, "lib", "systemd", "systemd"),
		filepath.Join(tmpDir, "usr", "lib", "systemd", "systemd"),
		filepath.Join(tmpDir, "lib64", "systemd", "systemd"),
	}

	for _, p := range systemdPaths {
		if fi, err := os.Stat(p); err == nil {
			// Must be a regular file or symlink that exists
			if fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
				return true
			}
		}
	}

	// Check if /sbin/init is a symlink to systemd
	sbinInit := filepath.Join(tmpDir, "sbin", "init")
	if target, err := os.Readlink(sbinInit); err == nil {
		// Check if symlink points to systemd
		if containsSystemd(target) {
			return true
		}
	}

	// Check for systemd directory structure
	systemdDir := filepath.Join(tmpDir, "etc", "systemd")
	if fi, err := os.Stat(systemdDir); err == nil && fi.IsDir() {
		// Has systemd config directory, check for systemd binary too
		for _, p := range systemdPaths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}

	return false
}

// hasTini checks if tini is already present.
func hasTini(tmpDir string) bool {
	tiniPaths := []string{
		filepath.Join(tmpDir, "sbin", "tini"),
		filepath.Join(tmpDir, "usr", "bin", "tini"),
		filepath.Join(tmpDir, "bin", "tini"),
	}

	for _, p := range tiniPaths {
		if fi, err := os.Stat(p); err == nil {
			// Must be executable
			if fi.Mode()&0111 != 0 {
				return true
			}
		}
	}

	return false
}

// hasDumbInit checks if dumb-init is already present.
func hasDumbInit(tmpDir string) bool {
	dumbInitPaths := []string{
		filepath.Join(tmpDir, "sbin", "dumb-init"),
		filepath.Join(tmpDir, "usr", "bin", "dumb-init"),
		filepath.Join(tmpDir, "bin", "dumb-init"),
	}

	for _, p := range dumbInitPaths {
		if fi, err := os.Stat(p); err == nil {
			// Must be executable
			if fi.Mode()&0111 != 0 {
				return true
			}
		}
	}

	return false
}

// hasOpenRC checks if OpenRC is the init system.
func hasOpenRC(tmpDir string) bool {
	// Check for OpenRC binary
	openrcPaths := []string{
		filepath.Join(tmpDir, "sbin", "openrc"),
		filepath.Join(tmpDir, "sbin", "rc"),
	}

	for _, p := range openrcPaths {
		if fi, err := os.Stat(p); err == nil {
			if fi.Mode()&0111 != 0 {
				return true
			}
		}
	}

	// Check for OpenRC directory structure
	openrcDirs := []string{
		filepath.Join(tmpDir, "etc", "init.d"),
		filepath.Join(tmpDir, "etc", "runlevels"),
	}

	openrcDirCount := 0
	for _, d := range openrcDirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			openrcDirCount++
		}
	}

	// If both init.d and runlevels exist, it's OpenRC (Alpine style)
	if openrcDirCount >= 1 {
		// Verify it's not sysvinit by checking for OpenRC-specific files
		rcStatus := filepath.Join(tmpDir, "sbin", "rc-status")
		if _, err := os.Stat(rcStatus); err == nil {
			return true
		}
		// Or check if /sbin/openrc or /sbin/rc exists
		for _, p := range openrcPaths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}

	return false
}

// hasSysvinit checks if sysvinit is the init system (without systemd).
func hasSysvinit(tmpDir string) bool {
	// First, make sure it's not systemd
	if isSystemd(tmpDir) {
		return false
	}

	// Check for /sbin/init that's not systemd
	sbinInit := filepath.Join(tmpDir, "sbin", "init")
	if fi, err := os.Stat(sbinInit); err == nil {
		// If it's a regular file or symlink (not pointing to systemd)
		if fi.Mode().IsRegular() {
			return true
		}
		// If symlink, check it doesn't point to systemd
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(sbinInit); err == nil {
				if !containsSystemd(target) {
					return true
				}
			}
		}
	}

	// Check for sysvinit directory structure without OpenRC markers
	initDDir := filepath.Join(tmpDir, "etc", "init.d")
	if fi, err := os.Stat(initDDir); err == nil && fi.IsDir() {
		// Has init.d but not OpenRC (no runlevels or rc binary)
		runlevelsDir := filepath.Join(tmpDir, "etc", "runlevels")
		rcBinary := filepath.Join(tmpDir, "sbin", "rc")

		_, hasRunlevels := os.Stat(runlevelsDir)
		_, hasRcBinary := os.Stat(rcBinary)

		if os.IsNotExist(hasRunlevels) && os.IsNotExist(hasRcBinary) {
			return true
		}
	}

	return false
}

// containsSystemd checks if a path contains systemd reference.
func containsSystemd(path string) bool {
	return len(path) >= len("systemd") &&
		(path == "systemd" ||
			(len(path) > len("systemd") &&
				(path[:len("systemd")] == "systemd" ||
					path[len(path)-len("systemd"):] == "systemd" ||
					strContains(path, "/systemd") ||
					strContains(path, "systemd/systemd"))))
}

// strContains checks if s contains sub.
func strContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// isGlibc checks if the image uses glibc (vs musl).
func isGlibc(tmpDir string) bool {
	// Common glibc library paths
	glibcPaths := []string{
		filepath.Join(tmpDir, "lib", "x86_64-linux-gnu", "libc.so.6"),
		filepath.Join(tmpDir, "lib64", "libc.so.6"),
		filepath.Join(tmpDir, "lib", "libc.so.6"),
		filepath.Join(tmpDir, "usr", "lib", "x86_64-linux-gnu", "libc.so.6"),
		filepath.Join(tmpDir, "usr", "lib64", "libc.so.6"),
	}

	for _, p := range glibcPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	// Also check for ld-linux (dynamic linker for glibc)
	ldPaths := []string{
		filepath.Join(tmpDir, "lib64", "ld-linux-x86-64.so.2"),
		filepath.Join(tmpDir, "lib", "x86_64-linux-gnu", "ld-linux-x86-64.so.2"),
		filepath.Join(tmpDir, "lib", "ld-linux-x86-64.so.2"),
	}

	for _, p := range ldPaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}

	return false
}

// validateCriticalSymlinks checks that critical symlinks resolve properly.
// Returns an error if critical symlinks are broken.
func validateCriticalSymlinks(tmpDir string) error {
	// /bin/sh is required for the init wrapper
	shPath := filepath.Join(tmpDir, "bin", "sh")

	fi, err := os.Lstat(shPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("/bin/sh does not exist - required for init wrapper")
		}
		return fmt.Errorf("failed to stat /bin/sh: %w", err)
	}

	// If it's a symlink, check if it resolves
	if fi.Mode()&os.ModeSymlink != 0 {
		// Try to resolve the symlink
		resolved, err := filepath.EvalSymlinks(shPath)
		if err != nil {
			return fmt.Errorf("/bin/sh is a dangling symlink: %w", err)
		}
		// Check if resolved path exists
		if _, err := os.Stat(resolved); err != nil {
			return fmt.Errorf("/bin/sh resolves to non-existent path: %s", resolved)
		}
	}

	// Check /bin/sh is executable
	if fi, err := os.Stat(shPath); err == nil {
		if fi.Mode()&0111 == 0 {
			return fmt.Errorf("/bin/sh exists but is not executable")
		}
	}

	return nil
}

// validateNonCriticalSymlinks checks non-critical symlinks and returns warnings.
// This is informational only and does not block injection.
func validateNonCriticalSymlinks(tmpDir string) []string {
	var warnings []string

	// Check common symlinks that might be broken
	symlinksToCheck := []string{
		filepath.Join(tmpDir, "bin", "sh"),
		filepath.Join(tmpDir, "bin", "bash"),
		filepath.Join(tmpDir, "usr", "bin", "env"),
	}

	for _, linkPath := range symlinksToCheck {
		fi, err := os.Lstat(linkPath)
		if err != nil {
			continue // Doesn't exist, that's fine
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			// It's a symlink, check if it resolves
			_, err := filepath.EvalSymlinks(linkPath)
			if err != nil {
				relPath, _ := filepath.Rel(tmpDir, linkPath)
				warnings = append(warnings, fmt.Sprintf("/%s is a dangling symlink", relPath))
			}
		}
	}

	return warnings
}
