package image

import (
	"os"
	"path/filepath"
	"testing"
)

// --- InjectIntoDir tests ---

func TestInjectIntoDir_Tini(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create /bin/sh so it's not detected as scratch
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh\n"), 0755)

	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	// Verify /sbin/tini exists and is executable
	tiniPath := filepath.Join(tmpDir, "sbin", "tini")
	if fi, err := os.Stat(tiniPath); err != nil {
		t.Fatalf("/sbin/tini not found: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatal("/sbin/tini is not executable")
	}

	// Verify /sbin/init exists and is executable
	initPath := filepath.Join(tmpDir, "sbin", "init")
	if fi, err := os.Stat(initPath); err != nil {
		t.Fatalf("/sbin/init not found: %v", err)
	} else if fi.Mode()&0111 == 0 {
		t.Fatal("/sbin/init is not executable")
	}

	// Verify /init symlink points to /sbin/init
	initLink := filepath.Join(tmpDir, "init")
	target, err := os.Readlink(initLink)
	if err != nil {
		t.Fatalf("/init symlink not found: %v", err)
	}
	if target != "/sbin/init" {
		t.Fatalf("/init symlink points to %q, want /sbin/init", target)
	}
}

func TestInjectIntoDir_DumbInit(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create /sbin/dumb-init so detection finds it
	sbinDir := filepath.Join(tmpDir, "sbin")
	os.MkdirAll(sbinDir, 0755)
	diPath := filepath.Join(sbinDir, "dumb-init")
	os.WriteFile(diPath, []byte("#!/bin/sh\n"), 0755)

	// Pre-create /bin/sh so it's not detected as scratch
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh\n"), 0755)

	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemDumbInit,
		GracePeriodSec: 10,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	// Verify /init symlink (detection should preserve existing init)
	initLink := filepath.Join(tmpDir, "init")
	target, err := os.Readlink(initLink)
	if err != nil {
		t.Fatalf("/init symlink not found: %v", err)
	}
	if target != "/sbin/init" {
		t.Fatalf("/init symlink points to %q, want /sbin/init", target)
	}
}

func TestInjectIntoDir_NoneDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	injector := NewInitInjector(&InitSystemConfig{
		Type: InitSystemNone,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir with none should not fail: %v", err)
	}

	// Should NOT create any init files
	if _, err := os.Stat(filepath.Join(tmpDir, "sbin")); err == nil {
		t.Fatal("/sbin should not exist when init is none")
	}
	if _, err := os.Lstat(filepath.Join(tmpDir, "init")); err == nil {
		t.Fatal("/init should not exist when init is none")
	}
}

func TestInjectIntoDir_DefaultConfig(t *testing.T) {
	tmpDir := t.TempDir()
	injector := NewInitInjector(nil) // nil config → default tini

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir with default config failed: %v", err)
	}

	// Should have tini since default is tini
	tiniPath := filepath.Join(tmpDir, "sbin", "tini")
	if _, err := os.Stat(tiniPath); err != nil {
		t.Fatalf("default config should inject tini: %v", err)
	}
}

func TestInjectIntoDir_OverwritesExistingInit(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create /init as a regular file
	initLink := filepath.Join(tmpDir, "init")
	os.WriteFile(initLink, []byte("old"), 0644)

	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 5,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	// /init should now be a symlink
	target, err := os.Readlink(initLink)
	if err != nil {
		t.Fatalf("/init should be a symlink: %v", err)
	}
	if target != "/sbin/init" {
		t.Fatalf("/init points to %q, want /sbin/init", target)
	}
}

func TestInjectIntoDir_InitScriptContent(t *testing.T) {
	tmpDir := t.TempDir()
	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	// Verify init wrapper references tini
	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}
	initStr := string(content)
	if !containsSubstring(initStr, "tini") {
		t.Fatal("init wrapper should reference tini")
	}
	if !containsSubstring(initStr, "/bin/sh") {
		t.Fatal("init wrapper should exec /bin/sh")
	}
}

func TestInjectIntoDir_SbinDirCreated(t *testing.T) {
	tmpDir := t.TempDir()
	injector := NewInitInjector(&InitSystemConfig{
		Type: InitSystemTini,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	sbinDir := filepath.Join(tmpDir, "sbin")
	fi, err := os.Stat(sbinDir)
	if err != nil {
		t.Fatalf("/sbin dir not created: %v", err)
	}
	if !fi.IsDir() {
		t.Fatal("/sbin is not a directory")
	}
}

func TestInjectIntoDir_FilesAreExecutable(t *testing.T) {
	tmpDir := t.TempDir()
	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	})

	err := injector.InjectIntoDir(tmpDir, nil)
	if err != nil {
		t.Fatalf("InjectIntoDir failed: %v", err)
	}

	files := []string{
		filepath.Join(tmpDir, "sbin", "tini"),
		filepath.Join(tmpDir, "sbin", "init"),
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if fi.Mode()&0111 == 0 {
			t.Errorf("%s is not executable (mode=%s)", f, fi.Mode())
		}
	}
}

// --- Helper ---

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && containsSubstr(s, sub)))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
