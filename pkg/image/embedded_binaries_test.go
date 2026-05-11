package image

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedTiniBinary verifies that the tini binary is embedded and valid.
func TestEmbeddedTiniBinary(t *testing.T) {
	// Verify tiniBinary is not empty
	if len(tiniBinary) == 0 {
		t.Fatal("tiniBinary is empty - binary not embedded correctly")
	}

	// Verify minimum size (tini-static is ~850KB)
	if len(tiniBinary) < 100000 {
		t.Errorf("tiniBinary appears too small: %d bytes (expected ~850KB)", len(tiniBinary))
	}

	// Verify ELF magic bytes (0x7f ELF)
	if len(tiniBinary) < 4 {
		t.Fatal("tiniBinary too small to check ELF header")
	}

	// ELF magic: 0x7f, 'E', 'L', 'F'
	if tiniBinary[0] != 0x7f || tiniBinary[1] != 'E' || tiniBinary[2] != 'L' || tiniBinary[3] != 'F' {
		t.Errorf("tiniBinary does not start with ELF magic header: got %x", tiniBinary[:4])
	}
}

// TestEmbeddedBusyboxBinary verifies that the busybox binary is embedded and valid.
func TestEmbeddedBusyboxBinary(t *testing.T) {
	// Verify busyboxBinary is not empty
	if len(busyboxBinary) == 0 {
		t.Fatal("busyboxBinary is empty - binary not embedded correctly")
	}

	// Verify minimum size (busybox static is ~1.1MB)
	if len(busyboxBinary) < 100000 {
		t.Errorf("busyboxBinary appears too small: %d bytes (expected ~1.1MB)", len(busyboxBinary))
	}

	// Verify ELF magic bytes (0x7f ELF)
	if len(busyboxBinary) < 4 {
		t.Fatal("busyboxBinary too small to check ELF header")
	}

	// ELF magic: 0x7f, 'E', 'L', 'F'
	if busyboxBinary[0] != 0x7f || busyboxBinary[1] != 'E' || busyboxBinary[2] != 'L' || busyboxBinary[3] != 'F' {
		t.Errorf("busyboxBinary does not start with ELF magic header: got %x", busyboxBinary[:4])
	}
}

// TestHasEmbeddedBinaries verifies the helper function returns true.
func TestHasEmbeddedBinaries(t *testing.T) {
	if !HasEmbeddedBinaries() {
		t.Error("HasEmbeddedBinaries() returned false, expected true")
	}
}

// TestGetTiniBinary verifies the getter returns the embedded binary.
func TestGetTiniBinary(t *testing.T) {
	bin := GetTiniBinary()
	if len(bin) == 0 {
		t.Error("GetTiniBinary() returned empty slice")
	}
	if len(bin) != len(tiniBinary) {
		t.Errorf("GetTiniBinary() returned different length: got %d, want %d", len(bin), len(tiniBinary))
	}
}

// TestGetBusyboxBinary verifies the getter returns the embedded binary.
func TestGetBusyboxBinary(t *testing.T) {
	bin := GetBusyboxBinary()
	if len(bin) == 0 {
		t.Error("GetBusyboxBinary() returned empty slice")
	}
	if len(bin) != len(busyboxBinary) {
		t.Errorf("GetBusyboxBinary() returned different length: got %d, want %d", len(bin), len(busyboxBinary))
	}
}

// TestEmbeddedBinariesAreExecutable verifies binaries can be written and made executable.
func TestEmbeddedBinariesAreExecutable(t *testing.T) {
	tmpDir := t.TempDir()

	// Test tini
	tiniPath := filepath.Join(tmpDir, "tini")
	if err := os.WriteFile(tiniPath, tiniBinary, 0755); err != nil {
		t.Fatalf("Failed to write tini binary: %v", err)
	}
	if fi, err := os.Stat(tiniPath); err != nil {
		t.Fatalf("Failed to stat tini binary: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Error("tini binary not executable")
	}

	// Test busybox
	busyboxPath := filepath.Join(tmpDir, "busybox")
	if err := os.WriteFile(busyboxPath, busyboxBinary, 0755); err != nil {
		t.Fatalf("Failed to write busybox binary: %v", err)
	}
	if fi, err := os.Stat(busyboxPath); err != nil {
		t.Fatalf("Failed to stat busybox binary: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Error("busybox binary not executable")
	}
}

// TestInjectTiniIntoDirWritesRealBinary verifies that injectTiniIntoDir writes a real binary.
func TestInjectTiniIntoDirWritesRealBinary(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()

	// Create init injector
	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	})

	// Inject tini (pass nil for OCIImageInfo as it's not needed for this test)
	if err := injector.injectTiniIntoDir(tmpDir, nil); err != nil {
		t.Fatalf("injectTiniIntoDir failed: %v", err)
	}

	// Verify /sbin/tini exists and is a real binary
	tiniPath := filepath.Join(tmpDir, "sbin", "tini")
	fi, err := os.Stat(tiniPath)
	if err != nil {
		t.Fatalf("tini binary not found: %v", err)
	}

	// Verify size is > 1000 bytes (not just a script)
	if fi.Size() < 1000 {
		t.Errorf("tini file too small (%d bytes) - expected a real binary", fi.Size())
	}

	// Read and verify ELF header
	content, err := os.ReadFile(tiniPath)
	if err != nil {
		t.Fatalf("Failed to read tini binary: %v", err)
	}

	// Verify ELF magic
	if len(content) < 4 {
		t.Fatal("tini file too small to check ELF header")
	}
	if content[0] != 0x7f || content[1] != 'E' || content[2] != 'L' || content[3] != 'F' {
		t.Errorf("tini file is not an ELF binary: got magic %x", content[:4])
	}

	// Verify /sbin/init exists
	initPath := filepath.Join(tmpDir, "sbin", "init")
	if _, err := os.Stat(initPath); err != nil {
		t.Errorf("init wrapper not found: %v", err)
	}

	// Verify /init symlink exists
	initLink := filepath.Join(tmpDir, "init")
	linkTarget, err := os.Readlink(initLink)
	if err != nil {
		t.Errorf("/init symlink not found: %v", err)
	} else if linkTarget != "/sbin/init" {
		t.Errorf("/init symlink points to %s, expected /sbin/init", linkTarget)
	}
}

// TestInjectBusyboxWritesRealBinary verifies that injectBusybox writes a real binary.
func TestInjectBusyboxWritesRealBinary(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()

	// Inject busybox
	if err := injectBusybox(tmpDir); err != nil {
		t.Fatalf("injectBusybox failed: %v", err)
	}

	// Verify /bin/busybox exists and is a real binary
	busyboxPath := filepath.Join(tmpDir, "bin", "busybox")
	fi, err := os.Stat(busyboxPath)
	if err != nil {
		t.Fatalf("busybox binary not found: %v", err)
	}

	// Verify size is > 1000 bytes (not just a script)
	if fi.Size() < 1000 {
		t.Errorf("busybox file too small (%d bytes) - expected a real binary", fi.Size())
	}

	// Read and verify ELF header
	content, err := os.ReadFile(busyboxPath)
	if err != nil {
		t.Fatalf("Failed to read busybox binary: %v", err)
	}

	// Verify ELF magic
	if len(content) < 4 {
		t.Fatal("busybox file too small to check ELF header")
	}
	if content[0] != 0x7f || content[1] != 'E' || content[2] != 'L' || content[3] != 'F' {
		t.Errorf("busybox file is not an ELF binary: got magic %x", content[:4])
	}

	// Verify /bin/sh symlink exists
	shPath := filepath.Join(tmpDir, "bin", "sh")
	linkTarget, err := os.Readlink(shPath)
	if err != nil {
		t.Errorf("/bin/sh symlink not found: %v", err)
	} else if linkTarget != "busybox" {
		t.Errorf("/bin/sh symlink points to %s, expected busybox", linkTarget)
	}
}

// TestInjectBusyboxCreatesAppletSymlinks verifies busybox applet symlinks are created.
func TestInjectBusyboxCreatesAppletSymlinks(t *testing.T) {
	// Create a temp directory
	tmpDir := t.TempDir()

	// Inject busybox
	if err := injectBusybox(tmpDir); err != nil {
		t.Fatalf("injectBusybox failed: %v", err)
	}

	// Verify some common applet symlinks exist
	expectedApplets := []string{"sh", "ls", "cat", "mkdir", "echo"}
	for _, applet := range expectedApplets {
		appletPath := filepath.Join(tmpDir, "bin", applet)
		linkTarget, err := os.Readlink(appletPath)
		if err != nil {
			t.Errorf("applet symlink /bin/%s not found: %v", applet, err)
		} else if linkTarget != "busybox" {
			t.Errorf("applet /bin/%s points to %s, expected busybox", applet, linkTarget)
		}
	}
}
