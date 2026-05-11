package image

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// --- injectEssentialFiles tests ---

func TestInjectEssentialFiles_ResolvConf(t *testing.T) {
	tmpDir := t.TempDir()

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify /etc/resolv.conf exists
	resolvPath := filepath.Join(tmpDir, "etc", "resolv.conf")
	content, err := os.ReadFile(resolvPath)
	if err != nil {
		t.Fatalf("/etc/resolv.conf not found: %v", err)
	}

	// Should contain Google DNS nameservers
	str := string(content)
	if !containsSubstring(str, "8.8.8.8") {
		t.Error("resolv.conf should contain 8.8.8.8")
	}
	if !containsSubstring(str, "8.8.4.4") {
		t.Error("resolv.conf should contain 8.8.4.4")
	}
	if !containsSubstring(str, "timeout:2") {
		t.Error("resolv.conf should have timeout option")
	}
	if !containsSubstring(str, "attempts:3") {
		t.Error("resolv.conf should have attempts option")
	}
}

func TestInjectEssentialFiles_Hosts(t *testing.T) {
	tmpDir := t.TempDir()

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify /etc/hosts exists
	hostsPath := filepath.Join(tmpDir, "etc", "hosts")
	content, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("/etc/hosts not found: %v", err)
	}

	// Should contain localhost entries
	str := string(content)
	if !containsSubstring(str, "127.0.0.1") {
		t.Error("hosts should contain 127.0.0.1")
	}
	if !containsSubstring(str, "localhost") {
		t.Error("hosts should contain localhost")
	}
	if !containsSubstring(str, "::1") {
		t.Error("hosts should contain ::1 for IPv6 localhost")
	}
}

func TestInjectEssentialFiles_Nsswitch_Glibc(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate glibc-based image by creating libc.so.6
	libDir := filepath.Join(tmpDir, "lib", "x86_64-linux-gnu")
	os.MkdirAll(libDir, 0755)
	os.WriteFile(filepath.Join(libDir, "libc.so.6"), []byte(""), 0644)

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify /etc/nsswitch.conf exists for glibc
	nsswitchPath := filepath.Join(tmpDir, "etc", "nsswitch.conf")
	content, err := os.ReadFile(nsswitchPath)
	if err != nil {
		t.Fatalf("/etc/nsswitch.conf not found for glibc image: %v", err)
	}

	// Should have hosts lookup order
	str := string(content)
	if !containsSubstring(str, "hosts:") {
		t.Error("nsswitch.conf should have hosts directive")
	}
	if !containsSubstring(str, "files") {
		t.Error("nsswitch.conf should have files lookup")
	}
	if !containsSubstring(str, "dns") {
		t.Error("nsswitch.conf should have dns lookup")
	}
}

func TestInjectEssentialFiles_Nsswitch_Musl(t *testing.T) {
	tmpDir := t.TempDir()

	// No glibc libraries - simulates musl/Alpine image
	// (Don't create any libc.so.6)

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// /etc/nsswitch.conf should NOT be created for musl images
	nsswitchPath := filepath.Join(tmpDir, "etc", "nsswitch.conf")
	if _, err := os.Stat(nsswitchPath); err == nil {
		t.Fatal("/etc/nsswitch.conf should NOT be created for musl images")
	}
}

func TestInjectEssentialFiles_MachineID(t *testing.T) {
	tmpDir := t.TempDir()

	imageID := "nginx-latest"

	err := injectEssentialFiles(tmpDir, imageID)
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify /etc/machine-id exists
	machineIDPath := filepath.Join(tmpDir, "etc", "machine-id")
	content, err := os.ReadFile(machineIDPath)
	if err != nil {
		t.Fatalf("/etc/machine-id not found: %v", err)
	}

	// Should be 32 hex characters (16 bytes of SHA256)
	idStr := string(content)
	idStr = trimNewline(idStr)
	if len(idStr) != 32 {
		t.Errorf("machine-id length = %d, want 32", len(idStr))
	}

	// Should be stable based on imageID
	hash := sha256.Sum256([]byte(imageID))
	expected := hex.EncodeToString(hash[:16])
	if idStr != expected {
		t.Errorf("machine-id = %q, want %q", idStr, expected)
	}

	// Should be read-only (mode 0444)
	fi, err := os.Stat(machineIDPath)
	if err != nil {
		t.Fatalf("stat machine-id: %v", err)
	}
	if fi.Mode() != 0444 {
		t.Errorf("machine-id mode = %s, want -r--r--r--", fi.Mode())
	}
}

func TestInjectEssentialFiles_Directories(t *testing.T) {
	tmpDir := t.TempDir()

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify essential directories exist
	dirs := []struct {
		path       string
		wantPerms  os.FileMode // permission bits (without directory type)
		wantSticky bool
	}{
		{"/tmp", 0777, true}, // sticky + 777
		{"/run", 0755, false},
		{"/var/run", 0755, false},
		{"/var/log", 0755, false},
		{"/var/tmp", 0777, true}, // sticky + 777
		{"/var/cache", 0755, false},
		{"/root", 0700, false}, // private
	}

	for _, d := range dirs {
		dirPath := filepath.Join(tmpDir, d.path)
		fi, err := os.Stat(dirPath)
		if err != nil {
			t.Errorf("directory %s not found: %v", d.path, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", d.path)
			continue
		}
		// Check permissions (just the permission bits, not the directory type)
		perms := fi.Mode().Perm()
		if perms != d.wantPerms {
			t.Errorf("%s perms = %s, want %s", d.path, perms, d.wantPerms)
		}
		// Check sticky bit
		if d.wantSticky && fi.Mode()&os.ModeSticky == 0 {
			t.Errorf("%s should have sticky bit set", d.path)
		}
		if !d.wantSticky && fi.Mode()&os.ModeSticky != 0 {
			t.Errorf("%s should not have sticky bit set", d.path)
		}
	}
}

func TestInjectEssentialFiles_DontOverwrite(t *testing.T) {
	tmpDir := t.TempDir()

	// Create existing files with custom content
	etcDir := filepath.Join(tmpDir, "etc")
	os.MkdirAll(etcDir, 0755)

	// Custom resolv.conf
	customResolv := "nameserver 192.168.1.1\n"
	os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte(customResolv), 0644)

	// Custom hosts
	customHosts := "127.0.0.1 myhost\n"
	os.WriteFile(filepath.Join(etcDir, "hosts"), []byte(customHosts), 0644)

	// Custom machine-id
	customMachineID := "abc123def456\n"
	os.WriteFile(filepath.Join(etcDir, "machine-id"), []byte(customMachineID), 0444)

	// Custom nsswitch.conf
	customNsswitch := "hosts: files dns\n"
	os.WriteFile(filepath.Join(etcDir, "nsswitch.conf"), []byte(customNsswitch), 0644)

	// Simulate glibc so nsswitch check works
	libDir := filepath.Join(tmpDir, "lib", "x86_64-linux-gnu")
	os.MkdirAll(libDir, 0755)
	os.WriteFile(filepath.Join(libDir, "libc.so.6"), []byte(""), 0644)

	err := injectEssentialFiles(tmpDir, "test-image")
	if err != nil {
		t.Fatalf("injectEssentialFiles failed: %v", err)
	}

	// Verify existing files were NOT overwritten
	resolvContent, _ := os.ReadFile(filepath.Join(etcDir, "resolv.conf"))
	if string(resolvContent) != customResolv {
		t.Error("resolv.conf should not be overwritten")
	}

	hostsContent, _ := os.ReadFile(filepath.Join(etcDir, "hosts"))
	if string(hostsContent) != customHosts {
		t.Error("hosts should not be overwritten")
	}

	machineIDContent, _ := os.ReadFile(filepath.Join(etcDir, "machine-id"))
	if string(machineIDContent) != customMachineID {
		t.Error("machine-id should not be overwritten")
	}

	nsswitchContent, _ := os.ReadFile(filepath.Join(etcDir, "nsswitch.conf"))
	if string(nsswitchContent) != customNsswitch {
		t.Error("nsswitch.conf should not be overwritten")
	}
}

func TestInjectResolvConf_CreatesEtcDir(t *testing.T) {
	tmpDir := t.TempDir()

	// No /etc directory exists initially
	err := injectResolvConf(tmpDir)
	if err != nil {
		t.Fatalf("injectResolvConf failed: %v", err)
	}

	// /etc should be created
	etcDir := filepath.Join(tmpDir, "etc")
	if _, err := os.Stat(etcDir); err != nil {
		t.Errorf("/etc directory not created: %v", err)
	}
}

func TestInjectHosts_CreatesEtcDir(t *testing.T) {
	tmpDir := t.TempDir()

	err := injectHosts(tmpDir)
	if err != nil {
		t.Fatalf("injectHosts failed: %v", err)
	}

	// /etc should be created
	etcDir := filepath.Join(tmpDir, "etc")
	if _, err := os.Stat(etcDir); err != nil {
		t.Errorf("/etc directory not created: %v", err)
	}
}

func TestInjectMachineID_CreatesEtcDir(t *testing.T) {
	tmpDir := t.TempDir()

	err := injectMachineID(tmpDir, "test")
	if err != nil {
		t.Fatalf("injectMachineID failed: %v", err)
	}

	// /etc should be created
	etcDir := filepath.Join(tmpDir, "etc")
	if _, err := os.Stat(etcDir); err != nil {
		t.Errorf("/etc directory not created: %v", err)
	}
}

func TestCreateEssentialDirs_TmpStickyBit(t *testing.T) {
	tmpDir := t.TempDir()

	err := createEssentialDirs(tmpDir)
	if err != nil {
		t.Fatalf("createEssentialDirs failed: %v", err)
	}

	// /tmp should have sticky bit (mode 01777)
	tmpPath := filepath.Join(tmpDir, "tmp")
	fi, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatalf("/tmp not found: %v", err)
	}

	// Check sticky bit is set
	if fi.Mode()&os.ModeSticky == 0 {
		t.Errorf("/tmp mode = %s, should have sticky bit", fi.Mode())
	}
}

func TestCreateEssentialDirs_VarTmpStickyBit(t *testing.T) {
	tmpDir := t.TempDir()

	err := createEssentialDirs(tmpDir)
	if err != nil {
		t.Fatalf("createEssentialDirs failed: %v", err)
	}

	// /var/tmp should have sticky bit
	varTmpPath := filepath.Join(tmpDir, "var", "tmp")
	fi, err := os.Stat(varTmpPath)
	if err != nil {
		t.Fatalf("/var/tmp not found: %v", err)
	}

	if fi.Mode()&os.ModeSticky == 0 {
		t.Errorf("/var/tmp mode = %s, should have sticky bit", fi.Mode())
	}
}

func TestCreateEssentialDirs_RootPrivate(t *testing.T) {
	tmpDir := t.TempDir()

	err := createEssentialDirs(tmpDir)
	if err != nil {
		t.Fatalf("createEssentialDirs failed: %v", err)
	}

	// /root should be private (mode 0700)
	rootPath := filepath.Join(tmpDir, "root")
	fi, err := os.Stat(rootPath)
	if err != nil {
		t.Fatalf("/root not found: %v", err)
	}

	if fi.Mode().Perm() != 0700 {
		t.Errorf("/root mode = %s, want drwx------", fi.Mode())
	}
}

// --- Helper functions ---

func trimNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}
