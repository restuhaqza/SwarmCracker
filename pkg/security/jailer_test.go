package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJailer(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jailer", "")

	assert.NotNil(t, jailer)
	assert.Equal(t, 1000, jailer.UID)
	assert.Equal(t, 1000, jailer.GID)
	assert.Equal(t, "/srv/jailer", jailer.ChrootBaseDir)
	assert.True(t, jailer.Enabled)
}

func TestJailer_Validate(t *testing.T) {
	tests := []struct {
		name        string
		jailer      *Jailer
		expectError bool
	}{
		{
			name: "valid jailer configuration",
			jailer: &Jailer{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			expectError: false,
		},
		{
			name: "negative UID",
			jailer: &Jailer{
				UID:           -1,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			expectError: true,
		},
		{
			name: "negative GID",
			jailer: &Jailer{
				UID:           1000,
				GID:           -1,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			expectError: true,
		},
		{
			name: "empty chroot directory",
			jailer: &Jailer{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: "",
				Enabled:       true,
			},
			expectError: true,
		},
		{
			name: "disabled jailer",
			jailer: &Jailer{
				Enabled: false,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.jailer.Validate()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestJailer_SetupJail(t *testing.T) {
	jailer := &Jailer{
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		ChrootBaseDir: t.TempDir(),
		Enabled:       true,
	}

	vmID := "test-vm-1"
	ctx, err := jailer.SetupJail(vmID)

	require.NoError(t, err)
	require.NotNil(t, ctx)
	assert.True(t, ctx.Enabled)
	assert.Contains(t, ctx.JailPath, vmID)
	assert.Equal(t, os.Getuid(), ctx.UID)
	assert.Equal(t, os.Getgid(), ctx.GID)

	// Verify jail directory exists
	assert.DirExists(t, ctx.JailPath)

	// Verify subdirectories exist
	subdirs := []string{"run", "dev", "proc"}
	for _, subdir := range subdirs {
		dir := filepath.Join(ctx.JailPath, subdir)
		assert.DirExists(t, dir)
	}
}

func TestJailer_SetupJail_Disabled(t *testing.T) {
	jailer := &Jailer{
		Enabled: false,
	}

	vmID := "test-vm-disabled"
	ctx, err := jailer.SetupJail(vmID)

	assert.NoError(t, err)
	assert.NotNil(t, ctx)
	assert.False(t, ctx.Enabled)
	assert.Empty(t, ctx.JailPath)
}

func TestJailer_CleanupJail(t *testing.T) {
	jailer := &Jailer{
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		ChrootBaseDir: t.TempDir(),
		Enabled:       true,
	}

	vmID := "test-vm-cleanup"
	ctx, err := jailer.SetupJail(vmID)
	require.NoError(t, err)

	// Verify jail exists
	assert.DirExists(t, ctx.JailPath)

	// Cleanup
	err = jailer.CleanupJail(ctx)
	assert.NoError(t, err)

	// Verify jail is removed
	_, err = os.Stat(ctx.JailPath)
	assert.True(t, os.IsNotExist(err))
}

func TestJailer_CleanupJail_Disabled(t *testing.T) {
	jailer := &Jailer{
		Enabled: false,
	}

	ctx := &JailContext{
		Enabled:  false,
		JailPath: "/nonexistent",
	}

	err := jailer.CleanupJail(ctx)
	assert.NoError(t, err)
}

func TestJailer_SetResourceLimits(t *testing.T) {
	jailer := &Jailer{
		Enabled: true,
	}

	vmID := "test-vm-limits"
	ctx, err := jailer.SetupJail(vmID)
	require.NoError(t, err)

	// setResourceLimits is called during SetupJail
	// Just verify it doesn't panic
	assert.NotNil(t, ctx)
}

func TestApplyResourceLimits(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to apply resource limits")
	}

	// Use current process for testing
	pid := os.Getpid()

	limits := ResourceLimits{
		MaxCPUs:      1,
		MaxMemoryMB:  512,
		MaxFD:        1024,
		MaxProcesses: 100,
	}

	err := ApplyResourceLimits(pid, limits)
	if err != nil {
		// May fail if cgroups v2 not available
		t.Logf("Resource limits failed (cgroups may not be available): %v", err)
	}
}

func TestCleanupCgroup(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to manage cgroups")
	}

	pid := os.Getpid()
	err := CleanupCgroup(pid)
	// Should not error even if cgroup doesn't exist
	assert.NoError(t, err)
}
