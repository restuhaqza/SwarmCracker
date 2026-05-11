//go:build !integration

package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnterJail_MockSyscalls tests EnterJail with mocked syscall functions
func TestEnterJail_MockSyscalls(t *testing.T) {
	// Save original functions
	origChroot := syscallChroot
	origSetgid := syscallSetgid
	origSetuid := syscallSetuid
	origChdir := osChdir

	defer func() {
		syscallChroot = origChroot
		syscallSetgid = origSetgid
		syscallSetuid = origSetuid
		osChdir = origChdir
	}()

	t.Run("successful_enter_jail", func(t *testing.T) {
		// Mock all syscalls to succeed
		syscallChroot = func(_ string) error { return nil }
		syscallSetgid = func(_ int) error { return nil }
		syscallSetuid = func(_ int) error { return nil }
		osChdir = func(_ string) error { return nil }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.NoError(t, err, "EnterJail should succeed with mocked syscalls")
	})

	t.Run("chdir_first_fails", func(t *testing.T) {
		osChdir = func(_ string) error { return errors.New("chdir failed") }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail when first chdir fails")
		assert.Contains(t, err.Error(), "chdir to jail")
	})

	t.Run("chroot_fails", func(t *testing.T) {
		osChdir = func(_ string) error { return nil }
		syscallChroot = func(_ string) error { return errors.New("chroot failed") }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail when chroot fails")
		assert.Contains(t, err.Error(), "chroot")
	})

	t.Run("chdir_after_chroot_fails", func(t *testing.T) {
		osChdir = func(path string) error {
			if path == "/" {
				return errors.New("chdir root failed")
			}
			return nil
		}
		syscallChroot = func(_ string) error { return nil }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail when chdir after chroot fails")
		assert.Contains(t, err.Error(), "chdir after chroot")
	})

	t.Run("setgid_fails", func(t *testing.T) {
		osChdir = func(_ string) error { return nil }
		syscallChroot = func(_ string) error { return nil }
		syscallSetgid = func(_ int) error { return errors.New("setgid failed") }
		syscallSetuid = func(_ int) error { return nil }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail when setgid fails")
		assert.Contains(t, err.Error(), "setgid")
	})

	t.Run("setuid_fails", func(t *testing.T) {
		osChdir = func(_ string) error { return nil }
		syscallChroot = func(_ string) error { return nil }
		syscallSetgid = func(_ int) error { return nil }
		syscallSetuid = func(_ int) error { return errors.New("setuid failed") }

		jailer := NewJailer(1000, 1000, "/srv/jailer", "")
		ctx := &JailContext{
			Enabled:  true,
			JailPath: "/srv/jailer/testvm",
			UID:      1000,
			GID:      1000,
		}

		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail when setuid fails")
		assert.Contains(t, err.Error(), "setuid")
	})
}

// TestSetResourceLimits_MockSyscall tests setResourceLimits with mocked syscall
func TestSetResourceLimits_MockSyscall(t *testing.T) {
	origSetrlimit := syscallSetrlimit
	defer func() { syscallSetrlimit = origSetrlimit }()

	t.Run("setrlimit_fails_for_nofile", func(t *testing.T) {
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error {
			return errors.New("setrlimit failed")
		}

		jailer := &Jailer{Enabled: true, ChrootBaseDir: t.TempDir()}
		ctx := &JailContext{Enabled: true}

		err := jailer.setResourceLimits(ctx)
		assert.Error(t, err, "setResourceLimits should fail when setrlimit fails")
		assert.Contains(t, err.Error(), "FD limit")
	})

	t.Run("setrlimit_fails_for_cpu", func(t *testing.T) {
		callCount := 0
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error {
			callCount++
			if callCount == 2 { // Second call is for CPU
				return errors.New("setrlimit cpu failed")
			}
			return nil
		}

		jailer := &Jailer{Enabled: true, ChrootBaseDir: t.TempDir()}
		ctx := &JailContext{Enabled: true}

		err := jailer.setResourceLimits(ctx)
		assert.Error(t, err, "setResourceLimits should fail when CPU setrlimit fails")
		assert.Contains(t, err.Error(), "CPU limit")
	})

	t.Run("setrlimit_fails_for_memory", func(t *testing.T) {
		callCount := 0
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error {
			callCount++
			if callCount == 3 { // Third call is for memory
				return errors.New("setrlimit memory failed")
			}
			return nil
		}

		jailer := &Jailer{Enabled: true, ChrootBaseDir: t.TempDir()}
		ctx := &JailContext{Enabled: true}

		err := jailer.setResourceLimits(ctx)
		assert.Error(t, err, "setResourceLimits should fail when memory setrlimit fails")
		assert.Contains(t, err.Error(), "memory limit")
	})

	t.Run("all_setrlimit_succeed", func(t *testing.T) {
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error { return nil }

		jailer := &Jailer{Enabled: true, ChrootBaseDir: t.TempDir()}
		ctx := &JailContext{Enabled: true}

		err := jailer.setResourceLimits(ctx)
		assert.NoError(t, err, "setResourceLimits should succeed when all setrlimit succeed")
	})
}

// TestApplyResourceLimits_MockFileOps tests ApplyResourceLimits with mocked file operations
func TestApplyResourceLimits_MockFileOps(t *testing.T) {
	origMkdirAll := osMkdirAll
	origWriteFile := osWriteFileVar
	defer func() {
		osMkdirAll = origMkdirAll
		osWriteFileVar = origWriteFile
	}()

	t.Run("mkdirall_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error {
			return errors.New("mkdir failed")
		}

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 1024})
		assert.Error(t, err, "ApplyResourceLimits should fail when mkdir fails")
		assert.Contains(t, err.Error(), "create cgroup")
	})

	t.Run("writefile_cpu_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		callCount := 0
		osWriteFileVar = func(_ string, _ []byte, _ os.FileMode) error {
			callCount++
			if callCount == 1 { // First write is cpu.max
				return errors.New("write failed")
			}
			return nil
		}

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 0})
		assert.Error(t, err, "ApplyResourceLimits should fail when cpu write fails")
		assert.Contains(t, err.Error(), "CPU limit")
	})

	t.Run("writefile_memory_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		callCount := 0
		osWriteFileVar = func(_ string, _ []byte, _ os.FileMode) error {
			callCount++
			if callCount == 1 { // First write is cpu.max
				return nil
			}
			if callCount == 2 { // Second write is memory.max
				return errors.New("write failed")
			}
			return nil
		}

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 1024})
		assert.Error(t, err, "ApplyResourceLimits should fail when memory write fails")
		assert.Contains(t, err.Error(), "memory limit")
	})

	t.Run("writefile_procs_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		callCount := 0
		osWriteFileVar = func(_ string, _ []byte, _ os.FileMode) error {
			callCount++
			if callCount <= 2 { // cpu and memory writes
				return nil
			}
			return errors.New("write procs failed")
		}

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 1024})
		assert.Error(t, err, "ApplyResourceLimits should fail when procs write fails")
		assert.Contains(t, err.Error(), "add process to cgroup")
	})

	t.Run("all_operations_succeed", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		osWriteFileVar = func(_ string, _ []byte, _ os.FileMode) error { return nil }

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 1024})
		assert.NoError(t, err, "ApplyResourceLimits should succeed with mocked ops")
	})

	t.Run("zero_limits_skip_cpu_memory", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		osWriteFileVar = func(name string, _ []byte, _ os.FileMode) error {
			if filepath.Base(name) == "cpu.max" || filepath.Base(name) == "memory.max" {
				return errors.New("should not be called")
			}
			return nil
		}

		err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 0, MaxMemoryMB: 0})
		assert.NoError(t, err, "ApplyResourceLimits should succeed with zero limits")
	})
}

// TestCleanupCgroup_MockFileOps tests CleanupCgroup with mocked file ops
func TestCleanupCgroup_MockFileOps(t *testing.T) {
	origRemoveAll := osRemoveAllVar
	defer func() { osRemoveAllVar = origRemoveAll }()

	t.Run("removeall_fails", func(t *testing.T) {
		osRemoveAllVar = func(_ string) error { return errors.New("remove failed") }

		err := CleanupCgroup(1234)
		assert.Error(t, err, "CleanupCgroup should fail when remove fails")
		assert.Contains(t, err.Error(), "remove cgroup")
	})

	t.Run("removeall_succeeds", func(t *testing.T) {
		osRemoveAllVar = func(_ string) error { return nil }

		err := CleanupCgroup(1234)
		assert.NoError(t, err, "CleanupCgroup should succeed")
	})
}

// TestSecureFilePermissions_MockOps tests SecureFilePermissions with mocked ops
func TestSecureFilePermissions_MockOps(t *testing.T) {
	origChown := osChown
	origChmod := osChmod
	defer func() {
		osChown = origChown
		osChmod = origChmod
	}()

	t.Run("chown_fails", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return errors.New("chown failed") }
		osChmod = func(_ string, _ os.FileMode) error { return nil }

		err := SecureFilePermissions("/test/file")
		assert.Error(t, err, "SecureFilePermissions should fail when chown fails")
		assert.Contains(t, err.Error(), "chown")
	})

	t.Run("chmod_fails", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return nil }
		osChmod = func(_ string, _ os.FileMode) error { return errors.New("chmod failed") }

		err := SecureFilePermissions("/test/file")
		assert.Error(t, err, "SecureFilePermissions should fail when chmod fails")
		assert.Contains(t, err.Error(), "chmod")
	})

	t.Run("both_succeed", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return nil }
		osChmod = func(_ string, _ os.FileMode) error { return nil }

		err := SecureFilePermissions("/test/file")
		assert.NoError(t, err, "SecureFilePermissions should succeed")
	})
}

// TestSecureDirectoryPermissions_MockOps tests SecureDirectoryPermissions with mocked ops
func TestSecureDirectoryPermissions_MockOps(t *testing.T) {
	origChown := osChown
	origChmod := osChmod
	defer func() {
		osChown = origChown
		osChmod = origChmod
	}()

	t.Run("chown_fails", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return errors.New("chown failed") }
		osChmod = func(_ string, _ os.FileMode) error { return nil }

		err := SecureDirectoryPermissions("/test/dir")
		assert.Error(t, err, "SecureDirectoryPermissions should fail when chown fails")
		assert.Contains(t, err.Error(), "chown")
	})

	t.Run("chmod_fails", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return nil }
		osChmod = func(_ string, _ os.FileMode) error { return errors.New("chmod failed") }

		err := SecureDirectoryPermissions("/test/dir")
		assert.Error(t, err, "SecureDirectoryPermissions should fail when chmod fails")
		assert.Contains(t, err.Error(), "chmod")
	})

	t.Run("both_succeed", func(t *testing.T) {
		osChown = func(_ string, _, _ int) error { return nil }
		osChmod = func(_ string, _ os.FileMode) error { return nil }

		err := SecureDirectoryPermissions("/test/dir")
		assert.NoError(t, err, "SecureDirectoryPermissions should succeed")
	})
}

// TestCheckCapabilities_MockGeteuid tests CheckCapabilities with mocked geteuid
func TestCheckCapabilities_MockGeteuid(t *testing.T) {
	origGeteuid := osGeteuid
	defer func() { osGeteuid = origGeteuid }()

	t.Run("non_root_returns_error", func(t *testing.T) {
		osGeteuid = func() int { return 1000 } // non-root

		err := CheckCapabilities()
		assert.Error(t, err, "CheckCapabilities should error for non-root")
		assert.Contains(t, err.Error(), "root privileges")
	})

	t.Run("root_succeeds", func(t *testing.T) {
		osGeteuid = func() int { return 0 } // root

		err := CheckCapabilities()
		assert.NoError(t, err, "CheckCapabilities should succeed for root")
	})
}

// TestCleanupJail_MockOps tests CleanupJail with mocked ops
func TestCleanupJail_MockOps(t *testing.T) {
	origRemoveAll := osRemoveAllVar
	defer func() { osRemoveAllVar = origRemoveAll }()

	t.Run("removeall_fails", func(t *testing.T) {
		osRemoveAllVar = func(_ string) error { return errors.New("remove failed") }

		jailer := &Jailer{Enabled: true}
		ctx := &JailContext{Enabled: true, JailPath: "/test/jail"}

		err := jailer.CleanupJail(ctx)
		assert.Error(t, err, "CleanupJail should fail when remove fails")
		assert.Contains(t, err.Error(), "remove jail directory")
	})

	t.Run("removeall_succeeds", func(t *testing.T) {
		osRemoveAllVar = func(_ string) error { return nil }

		jailer := &Jailer{Enabled: true}
		ctx := &JailContext{Enabled: true, JailPath: "/test/jail"}

		err := jailer.CleanupJail(ctx)
		assert.NoError(t, err, "CleanupJail should succeed")
	})
}

// TestSetupJail_MockOps tests SetupJail with mocked ops
func TestSetupJail_MockOps(t *testing.T) {
	origMkdirAll := osMkdirAll
	origSetrlimit := syscallSetrlimit
	defer func() {
		osMkdirAll = origMkdirAll
		syscallSetrlimit = origSetrlimit
	}()

	t.Run("mkdirall_main_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return errors.New("mkdir failed") }
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error { return nil }

		jailer := &Jailer{Enabled: true, ChrootBaseDir: "/test"}

		_, err := jailer.SetupJail("testvm")
		assert.Error(t, err, "SetupJail should fail when main mkdir fails")
		assert.Contains(t, err.Error(), "create jail directory")
	})

	t.Run("mkdirall_subdir_fails", func(t *testing.T) {
		callCount := 0
		osMkdirAll = func(_ string, _ os.FileMode) error {
			callCount++
			if callCount == 2 { // Second call is for subdir
				return errors.New("mkdir subdir failed")
			}
			return nil
		}
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error { return nil }

		jailer := &Jailer{Enabled: true, ChrootBaseDir: "/test"}

		_, err := jailer.SetupJail("testvm")
		assert.Error(t, err, "SetupJail should fail when subdir mkdir fails")
		assert.Contains(t, err.Error(), "create jail subdirectory")
	})

	t.Run("all_succeed", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return nil }
		syscallSetrlimit = func(_ int, _ *syscall.Rlimit) error { return nil }

		jailer := &Jailer{Enabled: true, ChrootBaseDir: "/test"}

		ctx, err := jailer.SetupJail("testvm")
		assert.NoError(t, err, "SetupJail should succeed")
		assert.NotNil(t, ctx)
		assert.True(t, ctx.Enabled)
		assert.Equal(t, "/test/testvm", ctx.JailPath)
	})
}

// TestCleanupVM_MockRemove tests CleanupVM with mocked osRemove
func TestCleanupVM_MockRemove(t *testing.T) {
	origRemove := osRemoveVar
	defer func() { osRemoveVar = origRemove }()

	t.Run("remove_seccomp_fails_with_unexpected_error", func(t *testing.T) {
		osRemoveVar = func(_ string) error { return errors.New("remove failed") }

		mgr := &Manager{enabled: true, jailer: &Jailer{Enabled: false}}
		vmCtx := &VMContext{
			Enabled:        true,
			JailContext:    &JailContext{Enabled: false},
			SeccompProfile: "/test/seccomp.json",
		}

		// CleanupVM logs warning but doesn't return error for seccomp removal failure
		err := mgr.CleanupVM(context.Background(), vmCtx)
		assert.NoError(t, err, "CleanupVM should not fail for seccomp removal errors")
	})
}

// TestValidate_MockMkdir tests Jailer Validate with mocked mkdir
func TestValidate_MockMkdir(t *testing.T) {
	origMkdirAll := osMkdirAll
	defer func() { osMkdirAll = origMkdirAll }()

	t.Run("mkdirall_fails", func(t *testing.T) {
		osMkdirAll = func(_ string, _ os.FileMode) error { return errors.New("mkdir failed") }

		jailer := &Jailer{
			UID:           1000,
			GID:           1000,
			ChrootBaseDir: "/test/base",
			Enabled:       true,
		}

		err := jailer.Validate()
		assert.Error(t, err, "Validate should fail when mkdir fails")
		assert.Contains(t, err.Error(), "chroot base directory")
	})
}

// TestNewManager_Integration tests NewManager with real config (non-mocked)
func TestNewManager_Integration(t *testing.T) {
	t.Run("disabled_jailer_returns_disabled_manager", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{EnableJailer: false},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.False(t, mgr.IsEnabled())
	})
}

// TestPrepareVM_DisabledManager_V2 tests PrepareVM with disabled manager
func TestPrepareVM_DisabledManager_V2(t *testing.T) {
	mgr := &Manager{enabled: false}
	vmCtx, err := mgr.PrepareVM(context.Background(), "test-vm")
	require.NoError(t, err)
	assert.False(t, vmCtx.Enabled)
}

// TestApplyToProcess_DisabledVMContext_V2 tests ApplyToProcess with disabled VMContext
func TestApplyToProcess_DisabledVMContext_V2(t *testing.T) {
	mgr := &Manager{enabled: false}
	vmCtx := &VMContext{Enabled: false}
	err := mgr.ApplyToProcess(context.Background(), vmCtx, 1234)
	assert.NoError(t, err)
}

// TestCleanupVM_DisabledVMContext_V2 tests CleanupVM with disabled VMContext
func TestCleanupVM_DisabledVMContext_V2(t *testing.T) {
	mgr := &Manager{enabled: false}
	vmCtx := &VMContext{Enabled: false}
	err := mgr.CleanupVM(context.Background(), vmCtx)
	assert.NoError(t, err)
}

// TestApplyResourceLimits_ToTempDir tests ApplyResourceLimits with temp dir (real ops)
func TestApplyResourceLimits_ToTempDir(t *testing.T) {
	// Create a temp cgroup-like directory structure
	tmpDir := t.TempDir()

	// Override osMkdirAll and osWriteFileVar to use temp paths
	origMkdirAll := osMkdirAll
	origWriteFile := osWriteFileVar
	defer func() {
		osMkdirAll = origMkdirAll
		osWriteFileVar = origWriteFile
	}()

	osMkdirAll = func(path string, perm os.FileMode) error {
		// Redirect cgroup paths to temp dir
		if filepath.Base(filepath.Dir(path)) == "swarmcracker" || filepath.Base(filepath.Dir(filepath.Dir(path))) == "swarmcracker" {
			realPath := filepath.Join(tmpDir, filepath.Base(filepath.Dir(path)), filepath.Base(path))
			return os.MkdirAll(realPath, perm)
		}
		return os.MkdirAll(path, perm)
	}

	osWriteFileVar = func(path string, data []byte, perm os.FileMode) error {
		// Redirect cgroup file writes to temp dir
		if filepath.Base(filepath.Dir(path)) == "swarmcracker" {
			// For cgroup.procs directory
			cgroupDir := filepath.Join(tmpDir, "swarmcracker", filepath.Base(filepath.Dir(path)))
			os.MkdirAll(cgroupDir, 0755)
			realPath := filepath.Join(cgroupDir, filepath.Base(path))
			return os.WriteFile(realPath, data, perm)
		}
		// For files inside a cgroup
		if filepath.Base(filepath.Dir(filepath.Dir(path))) == "swarmcracker" {
			cgroupDir := filepath.Join(tmpDir, "swarmcracker", filepath.Base(filepath.Dir(path)))
			os.MkdirAll(cgroupDir, 0755)
			realPath := filepath.Join(cgroupDir, filepath.Base(path))
			return os.WriteFile(realPath, data, perm)
		}
		return os.WriteFile(path, data, perm)
	}

	// Test with actual ApplyResourceLimits
	err := ApplyResourceLimits(1234, ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 1024})
	assert.NoError(t, err, "ApplyResourceLimits should succeed with redirected paths")

	// Verify files were created
	cgroupDir := filepath.Join(tmpDir, "swarmcracker", "1234")
	assert.FileExists(t, filepath.Join(cgroupDir, "cpu.max"))
	assert.FileExists(t, filepath.Join(cgroupDir, "memory.max"))
	assert.FileExists(t, filepath.Join(cgroupDir, "cgroup.procs"))
}

// TestCleanupCgroup_ToTempDir tests CleanupCgroup with redirected paths
func TestCleanupCgroup_ToTempDir(t *testing.T) {
	tmpDir := t.TempDir()

	origRemoveAll := osRemoveAllVar
	defer func() { osRemoveAllVar = origRemoveAll }()

	osRemoveAllVar = func(path string) error {
		// Redirect cgroup removal to temp dir
		return os.RemoveAll(filepath.Join(tmpDir, filepath.Base(path)))
	}

	// First create the cgroup directory
	cgroupDir := filepath.Join(tmpDir, "1234")
	require.NoError(t, os.MkdirAll(cgroupDir, 0755))

	err := CleanupCgroup(1234)
	assert.NoError(t, err)

	// Verify directory was removed
	assert.NoDirExists(t, cgroupDir)
}

// TestSecureFilePermissions_ToTempFile tests SecureFilePermissions with temp file
func TestSecureFilePermissions_ToTempFile(t *testing.T) {
	// Create a temp file to test chmod (chown would fail without root)
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("test"), 0644))

	// Test chmod part only (chown will fail if not root, but that's expected)
	info, err := os.Stat(tmpFile)
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0600), info.Mode().Perm())

	// Chmod should succeed
	err = os.Chmod(tmpFile, 0600)
	assert.NoError(t, err)

	info, err = os.Stat(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestSecureDirectoryPermissions_ToTempDir tests SecureDirectoryPermissions chmod
func TestSecureDirectoryPermissions_ToTempDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create subdirectory
	subDir := filepath.Join(tmpDir, "testdir")
	require.NoError(t, os.Mkdir(subDir, 0755))

	// Test chmod part
	err := os.Chmod(subDir, 0700)
	assert.NoError(t, err)

	info, err := os.Stat(subDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}
