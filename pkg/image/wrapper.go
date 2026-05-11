// Package image provides generic init wrapper generation using OCI config.
package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// createGenericInitWrapper creates an /sbin/init script that uses OCI config.
// The wrapper handles filesystems, environment, user, workdir, and executes
// the OCI-defined command via tini as PID 1 signal handler.
func createGenericInitWrapper(tmpDir string, info *OCIImageInfo, gracePeriod int) error {
	// Ensure /sbin directory exists
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		return fmt.Errorf("failed to create sbin directory: %w", err)
	}

	// Generate the wrapper script content
	script := generateWrapperScript(info, gracePeriod)

	// Write to /sbin/init
	initPath := filepath.Join(sbinDir, "init")
	if err := os.WriteFile(initPath, []byte(script), 0755); err != nil {
		return fmt.Errorf("failed to write init wrapper: %w", err)
	}

	return nil
}

// generateWrapperScript generates the shell script content for the init wrapper.
func generateWrapperScript(info *OCIImageInfo, gracePeriod int) string {
	var lines []string

	// Header
	imageRef := "unknown"
	if info != nil && info.ImageRef != "" {
		imageRef = info.ImageRef
	}
	lines = append(lines, "#!/bin/sh")
	lines = append(lines, "# SwarmCracker init wrapper — auto-generated")
	lines = append(lines, "# Image: "+imageRef)
	lines = append(lines, "")

	// Mount essential filesystems
	lines = append(lines, "# Mount essential filesystems")
	lines = append(lines, "mount -t proc proc /proc 2>/dev/null || true")
	lines = append(lines, "mount -t sysfs sysfs /sys 2>/dev/null || true")
	lines = append(lines, "mount -t devpts devpts /dev/pts 2>/dev/null || true")
	lines = append(lines, "mount -t tmpfs tmpfs /dev/shm 2>/dev/null || true")

	// Create /tmp if needed (tmpfs)
	lines = append(lines, "if [ ! -d /tmp ]; then")
	lines = append(lines, "    mount -t tmpfs tmpfs /tmp 2>/dev/null || mkdir -p /tmp")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Fallback: create /dev/urandom if devtmpfs not available
	lines = append(lines, "# Fallback device nodes if devtmpfs failed")
	lines = append(lines, "if [ ! -e /dev/urandom ]; then")
	lines = append(lines, "    mknod /dev/urandom c 1 9 2>/dev/null || true")
	lines = append(lines, "fi")
	lines = append(lines, "")

	// Environment variables
	if info != nil && len(info.Env) > 0 {
		lines = append(lines, "# Environment")
		for _, env := range info.Env {
			// Parse KEY=VALUE format
			key, value := parseEnvVar(env)
			if key != "" {
				lines = append(lines, fmt.Sprintf("export %s=%s", shellEscape(key), shellEscapeValue(value)))
			}
		}
		lines = append(lines, "")
	}

	// Working directory
	if info != nil && info.WorkDir != "" {
		lines = append(lines, "# Working directory")
		workDir := shellEscapeValue(info.WorkDir)
		lines = append(lines, fmt.Sprintf("mkdir -p %s 2>/dev/null || true", workDir))
		lines = append(lines, fmt.Sprintf("cd %s || true", workDir))
		lines = append(lines, "")
	}

	// Handle USER directive
	userCmd := ""
	if info != nil && info.User != "" {
		lines = append(lines, "# User directive")
		// Check for su-exec or gosu availability at runtime
		lines = append(lines, "if command -v su-exec >/dev/null 2>&1; then")
		lines = append(lines, "    USER_CMD=\"su-exec "+shellEscape(info.User)+"\"")
		lines = append(lines, "elif command -v gosu >/dev/null 2>&1; then")
		lines = append(lines, "    USER_CMD=\"gosu "+shellEscape(info.User)+"\"")
		lines = append(lines, "else")
		lines = append(lines, "    echo \"Warning: USER directive set but no su-exec/gosu available, running as root\"")
		lines = append(lines, "    USER_CMD=\"\"")
		lines = append(lines, "fi")
		userCmd = "$USER_CMD "
		lines = append(lines, "")
	}

	// Build the exec command with tini
	cmd := FullCommand(info)
	cmdStr := buildCommandString(cmd)

	// Build tini arguments
	tiniArgs := "-s"
	if gracePeriod > 0 {
		tiniArgs = fmt.Sprintf("-s -g %d", gracePeriod)
	}

	// Handle StopSignal if specified
	stopSignal := ""
	if info != nil && info.StopSignal != "" && info.StopSignal != DefaultStopSignal {
		// Convert signal name to number if needed (tini expects signal number or name)
		signal := info.StopSignal
		if strings.HasPrefix(signal, "SIG") {
			signal = strings.TrimPrefix(signal, "SIG")
		}
		stopSignal = fmt.Sprintf("-e %s", signal)
	}

	// Build the exec line
	tiniCmd := "/sbin/tini"
	if stopSignal != "" {
		tiniCmd = fmt.Sprintf("/sbin/tini %s", stopSignal)
	}

	execLine := fmt.Sprintf("exec %s %s %s -- %s", userCmd, tiniCmd, tiniArgs, cmdStr)
	lines = append(lines, "# Execute")
	lines = append(lines, execLine)

	return strings.Join(lines, "\n")
}

// shellEscape escapes a string for safe shell use (adds quotes if needed).
func shellEscape(s string) string {
	// If already quoted, return as-is
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s
	}

	// If empty or contains no special chars, no escaping needed
	if s == "" || !needsShellEscaping(s) {
		return s
	}

	// Use double quotes and escape internal special chars
	return shellEscapeValue(s)
}

// shellEscapeValue escapes a value for shell, wrapping in double quotes.
func shellEscapeValue(s string) string {
	// Escape backslashes first, then double quotes
	escaped := strings.ReplaceAll(s, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "$", "\\$")
	escaped = strings.ReplaceAll(escaped, "`", "\\`")
	return "\"" + escaped + "\""
}

// needsShellEscaping checks if a string needs shell escaping.
func needsShellEscaping(s string) bool {
	for _, c := range s {
		// Characters that need escaping in shell
		switch c {
		case ' ', '\t', '"', '\'', '\\', '$', '`', '!', '*', '?', '[', ']', '(', ')', '<', '>', '&', '|', ';', '\n':
			return true
		}
	}
	return false
}

// parseEnvVar parses a KEY=VALUE environment variable string.
func parseEnvVar(env string) (key, value string) {
	idx := strings.Index(env, "=")
	if idx < 0 {
		return env, ""
	}
	return env[:idx], env[idx+1:]
}

// buildCommandString builds a shell command string from command parts.
func buildCommandString(cmd []string) string {
	if len(cmd) == 0 {
		return "/bin/sh"
	}

	var parts []string
	for _, part := range cmd {
		parts = append(parts, shellEscape(part))
	}
	return strings.Join(parts, " ")
}
