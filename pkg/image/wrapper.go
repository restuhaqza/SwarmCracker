// Package image provides generic init wrapper generation using OCI config.
package image

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

	// Write to /sbin/init. Remove any pre-existing file or symlink first:
	// Alpine images symlink /sbin/init -> /bin/busybox, and writing through
	// that symlink would overwrite the busybox binary itself.
	initPath := filepath.Join(sbinDir, "init")
	_ = os.Remove(initPath)
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
	lines = append(lines, "mount -t devtmpfs devtmpfs /dev 2>/dev/null || true")
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

	// Network configuration.
	// The VM is given a static address via the kernel `ip=` boot parameter
	// (set by the executor's translator). Configure it explicitly here so we
	// do not depend on CONFIG_IP_PNP or a DHCP server being present.
	lines = append(lines, "# Configure network from kernel ip= parameter")
	lines = append(lines, "ip link set lo up 2>/dev/null || true")
	lines = append(lines, "_IPARG=$(tr ' ' '\\n' < /proc/cmdline 2>/dev/null | grep '^ip=' | head -n1 | cut -d= -f2-)")
	lines = append(lines, "if [ -n \"$_IPARG\" ]; then")
	lines = append(lines, "    _IP=$(echo \"$_IPARG\" | cut -d: -f1)")
	lines = append(lines, "    _GW=$(echo \"$_IPARG\" | cut -d: -f3)")
	lines = append(lines, "    _MASK=$(echo \"$_IPARG\" | cut -d: -f4)")
	lines = append(lines, "    _DEV=$(echo \"$_IPARG\" | cut -d: -f6)")
	lines = append(lines, "    [ -z \"$_DEV\" ] && _DEV=eth0")
	lines = append(lines, "    [ -z \"$_MASK\" ] && _MASK=255.255.255.0")
	lines = append(lines, "    if [ -n \"$_IP\" ]; then")
	lines = append(lines, "        ifconfig \"$_DEV\" \"$_IP\" netmask \"$_MASK\" up 2>/dev/null || \\")
	lines = append(lines, "            ip addr add \"$_IP/24\" dev \"$_DEV\" 2>/dev/null || true")
	lines = append(lines, "        ip link set \"$_DEV\" up 2>/dev/null || true")
	lines = append(lines, "        [ -n \"$_GW\" ] && route add default gw \"$_GW\" 2>/dev/null || true")
	lines = append(lines, "    fi")
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

	// Build tini arguments.
	// tini flags are boolean: -s (become a subreaper) and -g (send signals to
	// the whole process group). There is NO numeric grace-period option; the
	// old "-g <seconds>" form made tini treat the number as the program name,
	// which then failed with "exec N failed: No such file or directory" and
	// crashed PID 1.
	tiniArgs := "-s -g"

	// Handle StopSignal if specified. tini's -e flag expects a signal
	// *number*, so translate the OCI signal name (e.g. SIGQUIT) to one.
	stopSignal := ""
	if info != nil && info.StopSignal != "" && info.StopSignal != DefaultStopSignal {
		if n, ok := signalNumber(info.StopSignal); ok {
			stopSignal = fmt.Sprintf("-e %d", n)
		}
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

// signalNumbers maps signal names (without the SIG prefix) to numbers.
// tini's -e option requires a numeric signal.
var signalNumbers = map[string]int{
	"HUP": 1, "INT": 2, "QUIT": 3, "ILL": 4, "TRAP": 5, "ABRT": 6, "IOT": 6,
	"BUS": 7, "FPE": 8, "KILL": 9, "USR1": 10, "SEGV": 11, "USR2": 12,
	"PIPE": 13, "ALRM": 14, "TERM": 15, "STKFLT": 16, "CHLD": 17, "CONT": 18,
	"STOP": 19, "TSTP": 20, "TTIN": 21, "TTOU": 22, "URG": 23, "XCPU": 24,
	"XFSZ": 25, "VTALRM": 26, "PROF": 27, "WINCH": 28, "IO": 29, "PWR": 30,
	"SYS": 31,
}

// signalNumber converts a signal name (with or without "SIG" prefix) or a
// numeric string to a signal number.
func signalNumber(name string) (int, bool) {
	name = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "SIG")
	if n, ok := signalNumbers[name]; ok {
		return n, true
	}
	if n, err := strconv.Atoi(name); err == nil && n > 0 {
		return n, true
	}
	return 0, false
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

	parts := make([]string, 0, len(cmd))
	for _, part := range cmd {
		parts = append(parts, shellEscape(part))
	}
	return strings.Join(parts, " ")
}
