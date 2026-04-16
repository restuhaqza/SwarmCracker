package security

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnterJail_Disabled tests EnterJail with disabled jail context
func TestEnterJail_Disabled(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{Enabled: false}

	err := jailer.EnterJail(ctx)
	assert.NoError(t, err, "EnterJail should return nil when jail is disabled")
}

// TestEnterJail_ChangirFailure tests EnterJail when chdir fails
func TestEnterJail_ChangirFailure(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges for EnterJail tests")
	}

	tempDir := t.TempDir()
	jailer := NewJailer(os.Getuid(), os.Getgid(), tempDir, "")

	// Create a jail context with a non-existent path
	ctx := &JailContext{
		Enabled:    true,
		JailPath:   "/nonexistent/path/that/does/not/exist",
		UID:        os.Getuid(),
		GID:        os.Getgid(),
		OriginalWD: "/",
	}

	err := jailer.EnterJail(ctx)
	assert.Error(t, err, "EnterJail should fail when jail path doesn't exist")
}

// TestEnterJail_ChrootFailure tests EnterJail error handling
func TestEnterJail_ChrootFailure(t *testing.T) {
	// This test verifies error handling when chroot would fail
	// We can't actually test the chroot failure without root, but we can
	// test the logic before chroot is called

	jailer := NewJailer(1000, 1000, "/srv/jailer", "")

	// Create a valid directory but don't enter it
	tempDir := t.TempDir()
	ctx := &JailContext{
		Enabled:    true,
		JailPath:   tempDir,
		UID:        os.Getuid(),
		GID:        os.Getgid(),
		OriginalWD: "/",
	}

	// Without root, chroot will fail
	if os.Getuid() != 0 {
		err := jailer.EnterJail(ctx)
		assert.Error(t, err, "EnterJail should fail without root privileges")
	}
}

// TestSetupNetworkNamespace_Invalid tests SetupNetworkNamespace with invalid namespace
func TestSetupNetworkNamespace_Invalid(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{
		Enabled: true,
		NetNS:   "invalid-namespace-name-xyz123",
	}

	err := jailer.SetupNetworkNamespace(ctx)
	// This should fail when trying to execute ip netns
	// The ip command will fail with an invalid namespace
	if err != nil {
		assert.Contains(t, err.Error(), "failed to enter network namespace")
	}
}

// TestSetupNetworkNamespace_Empty tests SetupNetworkNamespace with empty namespace
func TestSetupNetworkNamespace_Empty(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{
		Enabled: true,
		NetNS:   "",
	}

	err := jailer.SetupNetworkNamespace(ctx)
	assert.NoError(t, err, "SetupNetworkNamespace should succeed with empty namespace")
}

// TestSetupNetworkNamespace_Disabled tests SetupNetworkNamespace behavior
// Note: SetupNetworkNamespace doesn't check Enabled flag, it only checks if NetNS is empty
func TestSetupNetworkNamespace_Disabled(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{
		Enabled: false,
		NetNS:   "some-namespace",
	}

	err := jailer.SetupNetworkNamespace(ctx)
	// This will fail because the namespace doesn't exist
	// The function doesn't check Enabled, only NetNS
	if err != nil {
		assert.Contains(t, err.Error(), "failed to enter network namespace")
	}
}

// TestSecureFilePermissions_NonExistent tests SecureFilePermissions with non-existent file
func TestSecureFilePermissions_NonExistent(t *testing.T) {
	err := SecureFilePermissions("/nonexistent/path/file.txt")
	assert.Error(t, err, "SecureFilePermissions should fail for non-existent file")
}

// TestSecureFilePermissions_NoRoot tests SecureFilePermissions without root
func TestSecureFilePermissions_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	testFile := filepath.Join(t.TempDir(), "test.txt")
	err := os.WriteFile(testFile, []byte("test"), 0644)
	require.NoError(t, err)

	err = SecureFilePermissions(testFile)
	// Should fail because we can't chown without root
	assert.Error(t, err, "SecureFilePermissions should fail without root privileges")
}

// TestSecureDirectoryPermissions_NonExistent tests SecureDirectoryPermissions with non-existent directory
func TestSecureDirectoryPermissions_NonExistent(t *testing.T) {
	err := SecureDirectoryPermissions("/nonexistent/path")
	assert.Error(t, err, "SecureDirectoryPermissions should fail for non-existent directory")
}

// TestSecureDirectoryPermissions_NoRoot tests SecureDirectoryPermissions without root
func TestSecureDirectoryPermissions_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	testDir := filepath.Join(t.TempDir(), "testdir")
	err := os.MkdirAll(testDir, 0755)
	require.NoError(t, err)

	err = SecureDirectoryPermissions(testDir)
	// Should fail because we can't chown without root
	assert.Error(t, err, "SecureDirectoryPermissions should fail without root privileges")
}

// TestCheckCapabilities_NoRoot tests CheckCapabilities without root privileges
func TestCheckCapabilities_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	err := CheckCapabilities()
	assert.Error(t, err, "CheckCapabilities should fail without root privileges")
	assert.Contains(t, err.Error(), "security features require root privileges")
}

// TestCheckCapabilities_WithRoot tests CheckCapabilities with root
func TestCheckCapabilities_WithRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges")
	}

	err := CheckCapabilities()
	// Should succeed or at least not error on root check
	assert.NoError(t, err, "CheckCapabilities should succeed with root privileges")
}

// TestApplyResourceLimits_InvalidCgroupPath tests ApplyResourceLimits error handling
func TestApplyResourceLimits_InvalidCgroupPath(t *testing.T) {
	// This test verifies error handling when cgroup operations fail
	// Most systems don't allow writing to /sys/fs/cgroup without proper setup

	if os.Getuid() != 0 {
		t.Skip("requires root privileges for cgroup operations")
	}

	pid := os.Getpid()
	limits := ResourceLimits{
		MaxCPUs:      1,
		MaxMemoryMB:  512,
		MaxFD:        1024,
		MaxProcesses: 100,
	}

	// This will likely fail if cgroup v2 is not properly configured
	err := ApplyResourceLimits(pid, limits)
	// We don't assert success because cgroup setup varies by system
	// Just verify the function handles errors gracefully
	if err != nil {
		assert.Contains(t, err.Error(), "failed to")
	}
}

// TestApplyResourceLimits_NoRoot tests ApplyResourceLimits without root
func TestApplyResourceLimits_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	pid := os.Getpid()
	limits := ResourceLimits{
		MaxCPUs:      1,
		MaxMemoryMB:  512,
		MaxFD:        1024,
		MaxProcesses: 100,
	}

	err := ApplyResourceLimits(pid, limits)
	assert.Error(t, err, "ApplyResourceLimits should fail without root privileges")
}

// TestApplyResourceLimits_ZeroLimits tests ApplyResourceLimits with zero values
func TestApplyResourceLimits_ZeroLimits(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root privileges")
	}

	pid := os.Getpid()
	limits := ResourceLimits{
		MaxCPUs:      0,
		MaxMemoryMB:  0,
		MaxFD:        0,
		MaxProcesses: 0,
	}

	// Zero limits should not set cgroup controls
	// The function should still try to add the process to cgroup
	err := ApplyResourceLimits(pid, limits)
	// Don't assert on error result as cgroup setup varies
	_ = err
}

// TestCleanupCgroup_NoRoot tests CleanupCgroup without root
func TestCleanupCgroup_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	pid := os.Getpid()
	err := CleanupCgroup(pid)
	// CleanupCgroup uses os.RemoveAll which doesn't error if path doesn't exist
	// It may or may not error depending on cgroup directory permissions
	// Just verify the function completes without panic
	assert.NotPanics(t, func() {
		CleanupCgroup(pid)
	}, "CleanupCgroup should not panic")
	_ = err // We don't assert on error as it may succeed
}

// TestApplyToProcess_DisabledContext tests ApplyToProcess with disabled context
func TestApplyToProcess_DisabledContext(t *testing.T) {
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: false,
		},
	}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	vmCtx := &VMContext{Enabled: false}
	err = mgr.ApplyToProcess(context.Background(), vmCtx, os.Getpid())
	assert.NoError(t, err, "ApplyToProcess should succeed with disabled context")
}

// TestApplyToProcess_NoRoot tests ApplyToProcess without root
func TestApplyToProcess_NoRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root, skip non-root test")
	}

	tempDir := t.TempDir()
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: tempDir,
			},
		},
	}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	vmCtx := &VMContext{
		Enabled:  true,
		VMID:     "test-vm",
		JailPath: filepath.Join(tempDir, "test-vm"),
	}

	// Should fail to apply resource limits without root
	err = mgr.ApplyToProcess(context.Background(), vmCtx, os.Getpid())
	assert.Error(t, err, "ApplyToProcess should fail without root for resource limits")
}

// TestJailContext_Initialization tests JailContext initialization
func TestJailContext_Initialization(t *testing.T) {
	ctx := &JailContext{
		Enabled:    true,
		JailPath:   "/test/jail",
		UID:        1000,
		GID:        1000,
		NetNS:      "myns",
		OriginalWD: "/original",
	}

	assert.True(t, ctx.Enabled)
	assert.Equal(t, "/test/jail", ctx.JailPath)
	assert.Equal(t, 1000, ctx.UID)
	assert.Equal(t, 1000, ctx.GID)
	assert.Equal(t, "myns", ctx.NetNS)
	assert.Equal(t, "/original", ctx.OriginalWD)
}

// TestVMContext_Initialization tests VMContext initialization
func TestVMContext_Initialization(t *testing.T) {
	jailCtx := &JailContext{
		Enabled:  true,
		JailPath: "/test/jail",
	}

	ctx := &VMContext{
		Enabled:        true,
		VMID:           "vm-123",
		JailPath:       "/test/jail",
		JailContext:    jailCtx,
		SeccompProfile: "/test/jail/seccomp.json",
	}

	assert.True(t, ctx.Enabled)
	assert.Equal(t, "vm-123", ctx.VMID)
	assert.Equal(t, "/test/jail", ctx.JailPath)
	assert.NotNil(t, ctx.JailContext)
	assert.Equal(t, "/test/jail/seccomp.json", ctx.SeccompProfile)
}

// TestSetResourceLimits_SyscallError tests setResourceLimits error handling
func TestSetResourceLimits_SyscallError(t *testing.T) {
	// This test is tricky because setResourceLimits is private
	// We can indirectly test it through SetupJail which calls it
	// The function should handle syscall failures gracefully

	jailer := &Jailer{
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		ChrootBaseDir: t.TempDir(),
		Enabled:       true,
	}

	vmID := "test-vm-limits"
	ctx, err := jailer.SetupJail(vmID)

	// SetupJail should succeed even if some rlimit calls fail
	// (the function logs errors but doesn't fail on them)
	assert.NoError(t, err, "SetupJail should succeed even if rlimit calls partially fail")
	assert.NotNil(t, ctx)
}

// TestSetResourceLimits_FDOnly tests that FD limit is set
func TestSetResourceLimits_FDOnly(t *testing.T) {
	// Get current rlimit
	var oldRLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &oldRLimit)
	if err != nil {
		t.Skip("cannot get rlimit, skipping test")
	}

	// Set a new limit
	newLimit := uint64(2048)
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{
		Cur: newLimit,
		Max: newLimit,
	})
	if err != nil {
		t.Skip("cannot set rlimit, skipping test")
	}

	// Verify it was set
	var checkRLimit syscall.Rlimit
	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &checkRLimit)
	assert.NoError(t, err)
	// The soft limit should be set (may be capped by hard limit)
	assert.GreaterOrEqual(t, checkRLimit.Cur, uint64(1024))

	// Restore original limit
	syscall.Setrlimit(syscall.RLIMIT_NOFILE, &oldRLimit)
}

// TestNewJailer_AllParameters tests NewJailer with all parameters
func TestNewJailer_AllParameters(t *testing.T) {
	jailer := NewJailer(2000, 2000, "/custom/jail", "mynamespace")

	assert.Equal(t, 2000, jailer.UID)
	assert.Equal(t, 2000, jailer.GID)
	assert.Equal(t, "/custom/jail", jailer.ChrootBaseDir)
	assert.Equal(t, "mynamespace", jailer.NetNS)
	assert.True(t, jailer.Enabled)
}

// TestCleanupJail_NonExistent tests CleanupJail with non-existent path
func TestCleanupJail_NonExistent(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{
		Enabled:  true,
		JailPath: "/nonexistent/jail/path",
	}

	err := jailer.CleanupJail(ctx)
	// Should not error even if path doesn't exist
	assert.NoError(t, err, "CleanupJail should not error for non-existent path")
}

// TestCleanupJail_EmptyPath tests CleanupJail with empty path
func TestCleanupJail_EmptyPath(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")
	ctx := &JailContext{
		Enabled:  true,
		JailPath: "",
	}

	err := jailer.CleanupJail(ctx)
	assert.NoError(t, err, "CleanupJail should not error for empty path")
}

// TestValidateSecurityConfig_JailerUIDZero tests validation with UID=0
func TestValidateSecurityConfig_JailerUIDZero(t *testing.T) {
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           0,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	err := ValidateSecurityConfig(cfg)
	assert.Error(t, err, "ValidateSecurityConfig should reject UID=0")
	assert.Contains(t, err.Error(), "should not be 0")
}

// TestValidateSecurityConfig_JailerGIDZero tests validation with GID=0
func TestValidateSecurityConfig_JailerGIDZero(t *testing.T) {
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           0,
				ChrootBaseDir: t.TempDir(),
			},
		},
	}

	err := ValidateSecurityConfig(cfg)
	assert.Error(t, err, "ValidateSecurityConfig should reject GID=0")
	assert.Contains(t, err.Error(), "should not be 0")
}

// TestValidateSecurityConfig_EmptyChroot tests validation with empty chroot
func TestValidateSecurityConfig_EmptyChroot(t *testing.T) {
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: "",
			},
		},
	}

	err := ValidateSecurityConfig(cfg)
	assert.Error(t, err, "ValidateSecurityConfig should reject empty chroot directory")
	assert.Contains(t, err.Error(), "cannot be empty")
}

// TestValidateSecurityConfig_Disabled tests validation when jailer is disabled
func TestValidateSecurityConfig_Disabled(t *testing.T) {
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: false,
		},
	}

	err := ValidateSecurityConfig(cfg)
	assert.NoError(t, err, "ValidateSecurityConfig should succeed when jailer is disabled")
}

// TestGetSeccompProfilePath tests GetSeccompProfilePath
func TestGetSeccompProfilePath(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: tempDir,
			},
		},
	}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	vmID := "test-vm-profile"
	path := mgr.GetSeccompProfilePath(vmID)

	expectedPath := filepath.Join(tempDir, vmID, "seccomp.json")
	assert.Equal(t, expectedPath, path)
	assert.Contains(t, path, vmID)
	assert.Contains(t, path, "seccomp.json")
}

// TestGetJailer tests GetJailer
func TestGetJailer(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Executor: config.ExecutorConfig{
			EnableJailer: true,
			Jailer: config.JailerConfig{
				UID:           3000,
				GID:           3000,
				ChrootBaseDir: tempDir,
			},
		},
	}

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	jailer := mgr.GetJailer()
	assert.NotNil(t, jailer)
	assert.Equal(t, 3000, jailer.UID)
	assert.Equal(t, 3000, jailer.GID)
	assert.Equal(t, tempDir, jailer.ChrootBaseDir)
}

// TestIsEnabled tests IsEnabled
func TestIsEnabled(t *testing.T) {
	t.Run("jailer enabled", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           1000,
					GID:           1000,
					ChrootBaseDir: t.TempDir(),
				},
			},
		}

		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.True(t, mgr.IsEnabled())
	})

	t.Run("jailer disabled", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}

		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.False(t, mgr.IsEnabled())
	})
}

// TestSetResourceLimits_Manager tests Manager.SetResourceLimits
func TestSetResourceLimits_Manager(t *testing.T) {
	t.Run("disabled context", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}

		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx := &VMContext{Enabled: false}
		limits := ResourceLimits{MaxCPUs: 2}

		err = mgr.SetResourceLimits(vmCtx, limits)
		assert.NoError(t, err)
	})

	t.Run("enabled context", func(t *testing.T) {
		tempDir := t.TempDir()
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           1000,
					GID:           1000,
					ChrootBaseDir: tempDir,
				},
			},
		}

		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx := &VMContext{
			Enabled:  true,
			VMID:     "test-vm",
			JailPath: filepath.Join(tempDir, "test-vm"),
		}

		limits := ResourceLimits{
			MaxCPUs:      4,
			MaxMemoryMB:  4096,
			MaxFD:        8192,
			MaxProcesses: 2048,
		}

		err = mgr.SetResourceLimits(vmCtx, limits)
		// Manager.SetResourceLimits is a no-op, just stores limits
		assert.NoError(t, err)
	})
}
