package image

import (
	"os"
	"path/filepath"
	"testing"
)

// --- DetectInitType tests ---

func TestDetectInitType_Scratch(t *testing.T) {
	// Empty directory → scratch
	tmpDir := t.TempDir()

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeScratch {
		t.Errorf("empty dir: got %s, want %s", result.Type, InitTypeScratch)
	}
}

func TestDetectInitType_ScratchWithLostFound(t *testing.T) {
	// Directory with only lost+found → scratch
	tmpDir := t.TempDir()

	// Create lost+found (common in ext4 filesystems)
	if err := os.MkdirAll(filepath.Join(tmpDir, "lost+found"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeScratch {
		t.Errorf("dir with only lost+found: got %s, want %s", result.Type, InitTypeScratch)
	}
}

func TestDetectInitType_ScratchNoBinSh(t *testing.T) {
	// Directory with files but no /bin/sh and no /bin or /usr/bin → scratch
	tmpDir := t.TempDir()

	// Create some files but no shell
	if err := os.MkdirAll(filepath.Join(tmpDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app", "main"), []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeScratch {
		t.Errorf("dir without /bin/sh: got %s, want %s", result.Type, InitTypeScratch)
	}
}

func TestDetectInitType_Systemd(t *testing.T) {
	// Has /lib/systemd/systemd → incompatible
	tmpDir := t.TempDir()

	// Create systemd binary
	systemdPath := filepath.Join(tmpDir, "lib", "systemd")
	if err := os.MkdirAll(systemdPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemdPath, "systemd"), []byte("systemd binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /bin/sh so it's not detected as scratch
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeIncompatible {
		t.Errorf("systemd dir: got %s, want %s", result.Type, InitTypeIncompatible)
	}
	if result.Message == "" {
		t.Error("incompatible init should have a message")
	}
}

func TestDetectInitType_SystemdViaSymlink(t *testing.T) {
	// /sbin/init -> systemd → incompatible
	tmpDir := t.TempDir()

	// Create /lib/systemd/systemd
	systemdPath := filepath.Join(tmpDir, "lib", "systemd")
	if err := os.MkdirAll(systemdPath, 0755); err != nil {
		t.Fatal(err)
	}
	systemdBin := filepath.Join(systemdPath, "systemd")
	if err := os.WriteFile(systemdBin, []byte("systemd binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/init symlink to systemd
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/lib/systemd/systemd", filepath.Join(sbinDir, "init")); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeIncompatible {
		t.Errorf("systemd symlink: got %s, want %s", result.Type, InitTypeIncompatible)
	}
}

func TestDetectInitType_SystemdUsrLib(t *testing.T) {
	// Has /usr/lib/systemd/systemd → incompatible
	tmpDir := t.TempDir()

	// Create systemd binary in /usr/lib
	systemdPath := filepath.Join(tmpDir, "usr", "lib", "systemd")
	if err := os.MkdirAll(systemdPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemdPath, "systemd"), []byte("systemd binary"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeIncompatible {
		t.Errorf("usr/lib/systemd: got %s, want %s", result.Type, InitTypeIncompatible)
	}
}

func TestDetectInitType_Tini(t *testing.T) {
	// Has /sbin/tini → tini
	tmpDir := t.TempDir()

	// Create /bin/sh (not scratch)
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/tini
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "tini"), []byte("tini binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeTini {
		t.Errorf("tini dir: got %s, want %s", result.Type, InitTypeTini)
	}
}

func TestDetectInitType_TiniUsrBin(t *testing.T) {
	// Has /usr/bin/tini → tini
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /usr/bin/tini
	usrBinDir := filepath.Join(tmpDir, "usr", "bin")
	if err := os.MkdirAll(usrBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usrBinDir, "tini"), []byte("tini binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeTini {
		t.Errorf("usr/bin/tini: got %s, want %s", result.Type, InitTypeTini)
	}
}

func TestDetectInitType_TiniNotExecutable(t *testing.T) {
	// Has tini but not executable → should not detect as tini
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/tini without execute permission
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "tini"), []byte("tini binary"), 0644); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	// Should fall through to none since tini isn't executable
	if result.Type != InitTypeNone {
		t.Errorf("non-executable tini: got %s, want %s", result.Type, InitTypeNone)
	}
}

func TestDetectInitType_DumbInit(t *testing.T) {
	// Has /sbin/dumb-init → dumb-init
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/dumb-init
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "dumb-init"), []byte("dumb-init binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeDumbInit {
		t.Errorf("dumb-init dir: got %s, want %s", result.Type, InitTypeDumbInit)
	}
}

func TestDetectInitType_OpenRC(t *testing.T) {
	// Has /sbin/openrc → OpenRC
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/openrc
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "openrc"), []byte("openrc binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeOpenRC {
		t.Errorf("openrc dir: got %s, want %s", result.Type, InitTypeOpenRC)
	}
}

func TestDetectInitType_OpenRCViaRcBinary(t *testing.T) {
	// Has /sbin/rc → OpenRC
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/rc
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "rc"), []byte("rc binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeOpenRC {
		t.Errorf("rc binary: got %s, want %s", result.Type, InitTypeOpenRC)
	}
}

func TestDetectInitType_Sysvinit(t *testing.T) {
	// Has /sbin/init but not systemd → sysvinit
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create /sbin/init (regular file, not symlink to systemd)
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "init"), []byte("sysvinit binary"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeSysvinit {
		t.Errorf("sysvinit dir: got %s, want %s", result.Type, InitTypeSysvinit)
	}
}

func TestDetectInitType_None(t *testing.T) {
	// Has /bin/sh but no init system → none
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create some other directories to make it look like a real image
	if err := os.MkdirAll(filepath.Join(tmpDir, "usr", "bin"), 0755); err != nil {
		t.Fatal(err)
	}

	result := DetectInitType(tmpDir)
	if result.Type != InitTypeNone {
		t.Errorf("no init dir: got %s, want %s", result.Type, InitTypeNone)
	}
}

// --- isGlibc tests ---

func TestIsGlibc(t *testing.T) {
	// Has glibc libs → true
	tmpDir := t.TempDir()

	// Create glibc path
	libDir := filepath.Join(tmpDir, "lib", "x86_64-linux-gnu")
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libc.so.6"), []byte("glibc"), 0644); err != nil {
		t.Fatal(err)
	}

	if !isGlibc(tmpDir) {
		t.Error("expected isGlibc to return true for glibc image")
	}
}

func TestIsGlibc_Lib64(t *testing.T) {
	// Has glibc in /lib64 → true
	tmpDir := t.TempDir()

	lib64Dir := filepath.Join(tmpDir, "lib64")
	if err := os.MkdirAll(lib64Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib64Dir, "libc.so.6"), []byte("glibc"), 0644); err != nil {
		t.Fatal(err)
	}

	if !isGlibc(tmpDir) {
		t.Error("expected isGlibc to return true for lib64 glibc")
	}
}

func TestIsGlibc_Musl(t *testing.T) {
	// No glibc libs → false (likely musl or static)
	tmpDir := t.TempDir()

	// Create /bin/sh but no glibc
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	if isGlibc(tmpDir) {
		t.Error("expected isGlibc to return false for non-glibc image")
	}
}

func TestIsGlibc_LdLinux(t *testing.T) {
	// Has ld-linux dynamic linker → true
	tmpDir := t.TempDir()

	lib64Dir := filepath.Join(tmpDir, "lib64")
	if err := os.MkdirAll(lib64Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib64Dir, "ld-linux-x86-64.so.2"), []byte("ld-linux"), 0755); err != nil {
		t.Fatal(err)
	}

	if !isGlibc(tmpDir) {
		t.Error("expected isGlibc to return true for ld-linux")
	}
}

// --- validateCriticalSymlinks tests ---

func TestValidateCriticalSymlinks_OK(t *testing.T) {
	// Valid /bin/sh → no error
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	err := validateCriticalSymlinks(tmpDir)
	if err != nil {
		t.Errorf("expected no error for valid /bin/sh, got: %v", err)
	}
}

func TestValidateCriticalSymlinks_Dangling(t *testing.T) {
	// Broken /bin/sh symlink → error
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create dangling symlink
	if err := os.Symlink("/nonexistent/sh", filepath.Join(binDir, "sh")); err != nil {
		t.Fatal(err)
	}

	err := validateCriticalSymlinks(tmpDir)
	if err == nil {
		t.Error("expected error for dangling /bin/sh symlink")
	}
}

func TestValidateCriticalSymlinks_Missing(t *testing.T) {
	// No /bin/sh → error
	tmpDir := t.TempDir()

	err := validateCriticalSymlinks(tmpDir)
	if err == nil {
		t.Error("expected error for missing /bin/sh")
	}
}

func TestValidateCriticalSymlinks_NotExecutable(t *testing.T) {
	// /bin/sh exists but not executable → error
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0644); err != nil {
		t.Fatal(err)
	}

	err := validateCriticalSymlinks(tmpDir)
	if err == nil {
		t.Error("expected error for non-executable /bin/sh")
	}
}

func TestValidateCriticalSymlinks_ValidSymlink(t *testing.T) {
	// /bin/sh is symlink to valid target → no error
	tmpDir := t.TempDir()

	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create bash
	if err := os.WriteFile(filepath.Join(binDir, "bash"), []byte("#!/bin/bash"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create sh -> bash symlink
	if err := os.Symlink("bash", filepath.Join(binDir, "sh")); err != nil {
		t.Fatal(err)
	}

	err := validateCriticalSymlinks(tmpDir)
	if err != nil {
		t.Errorf("expected no error for valid symlink, got: %v", err)
	}
}

// --- injectBusybox tests ---

func TestInjectBusybox_MinimalShell(t *testing.T) {
	// Scratch image gets /bin/sh
	tmpDir := t.TempDir()

	err := injectBusybox(tmpDir)
	if err != nil {
		t.Fatalf("injectBusybox failed: %v", err)
	}

	// Check /bin/sh exists and is executable
	shPath := filepath.Join(tmpDir, "bin", "sh")
	if fi, err := os.Stat(shPath); err != nil {
		t.Fatalf("/bin/sh not found: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatal("/bin/sh is not executable")
	}

	// Check /bin/busybox exists
	busyboxPath := filepath.Join(tmpDir, "bin", "busybox")
	if fi, err := os.Stat(busyboxPath); err != nil {
		t.Fatalf("/bin/busybox not found: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatal("/bin/busybox is not executable")
	}
}

func TestInjectBusybox_CreatesEssentialDirs(t *testing.T) {
	// Scratch image gets essential directories
	tmpDir := t.TempDir()

	err := injectBusybox(tmpDir)
	if err != nil {
		t.Fatalf("injectBusybox failed: %v", err)
	}

	// Check essential directories
	essentialDirs := []string{"proc", "sys", "dev", "tmp", "run", "var", "etc"}
	for _, dir := range essentialDirs {
		dirPath := filepath.Join(tmpDir, dir)
		if fi, err := os.Stat(dirPath); err != nil {
			t.Errorf("directory /%s not found: %v", dir, err)
		} else if !fi.IsDir() {
			t.Errorf("/%s is not a directory", dir)
		}
	}
}

func TestInjectBusybox_CreatesPasswdGroup(t *testing.T) {
	// Scratch image gets /etc/passwd and /etc/group
	tmpDir := t.TempDir()

	err := injectBusybox(tmpDir)
	if err != nil {
		t.Fatalf("injectBusybox failed: %v", err)
	}

	// Check /etc/passwd
	passwdPath := filepath.Join(tmpDir, "etc", "passwd")
	if content, err := os.ReadFile(passwdPath); err != nil {
		t.Fatalf("/etc/passwd not found: %v", err)
	} else if !containsSubstring(string(content), "root") {
		t.Error("/etc/passwd missing root user")
	}

	// Check /etc/group
	groupPath := filepath.Join(tmpDir, "etc", "group")
	if content, err := os.ReadFile(groupPath); err != nil {
		t.Fatalf("/etc/group not found: %v", err)
	} else if !containsSubstring(string(content), "root") {
		t.Error("/etc/group missing root group")
	}
}

func TestInjectBusyboxMinimal(t *testing.T) {
	// Minimal injection creates only /bin/sh
	tmpDir := t.TempDir()

	err := injectBusyboxMinimal(tmpDir)
	if err != nil {
		t.Fatalf("injectBusyboxMinimal failed: %v", err)
	}

	// Check /bin/sh exists
	shPath := filepath.Join(tmpDir, "bin", "sh")
	if fi, err := os.Stat(shPath); err != nil {
		t.Fatalf("/bin/sh not found: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatal("/bin/sh is not executable")
	}

	// Should NOT create busybox (minimal version)
	busyboxPath := filepath.Join(tmpDir, "bin", "busybox")
	if _, err := os.Stat(busyboxPath); err == nil {
		t.Error("injectBusyboxMinimal should not create /bin/busybox")
	}
}

// --- Integration tests ---

func TestDetectInitType_Priority(t *testing.T) {
	// Test that systemd is detected before tini (systemd is incompatible, takes priority)
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create both systemd and tini
	systemdPath := filepath.Join(tmpDir, "lib", "systemd")
	if err := os.MkdirAll(systemdPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemdPath, "systemd"), []byte("systemd"), 0755); err != nil {
		t.Fatal(err)
	}

	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "tini"), []byte("tini"), 0755); err != nil {
		t.Fatal(err)
	}

	// Systemd should be detected first (incompatible)
	result := DetectInitType(tmpDir)
	if result.Type != InitTypeIncompatible {
		t.Errorf("systemd should have priority over tini: got %s", result.Type)
	}
}

func TestDetectInitType_TiniOverOpenRC(t *testing.T) {
	// Tini is detected before OpenRC (existing tini should be preserved)
	tmpDir := t.TempDir()

	// Create /bin/sh
	binDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create both openrc and tini
	sbinDir := filepath.Join(tmpDir, "sbin")
	if err := os.MkdirAll(sbinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "tini"), []byte("tini"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbinDir, "openrc"), []byte("openrc"), 0755); err != nil {
		t.Fatal(err)
	}

	// Tini should be detected first
	result := DetectInitType(tmpDir)
	if result.Type != InitTypeTini {
		t.Errorf("tini should have priority over openrc: got %s", result.Type)
	}
}