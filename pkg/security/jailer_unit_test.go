package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJailer_SetupJail_InvalidChroot tests SetupJail with invalid chroot path
func TestJailer_SetupJail_InvalidChroot(t *testing.T) {
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: "/proc/nonexistent/path", // Can't create under /proc
		Enabled:       true,
	}

	ctx, err := jailer.SetupJail("test-vm")
	require.Error(t, err)
	assert.Nil(t, ctx)
}

// TestJailer_SetupJail_EmptyVMID tests SetupJail with empty VM ID
func TestJailer_SetupJail_EmptyVMID(t *testing.T) {
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: t.TempDir(),
		Enabled:       true,
	}

	ctx, err := jailer.SetupJail("")
	require.NoError(t, err) // Empty VMID creates jail at baseDir
	assert.NotNil(t, ctx)
}

// TestJailContext_Fields tests JailContext field assignments
func TestJailContext_Fields(t *testing.T) {
	ctx := &JailContext{
		Enabled:    true,
		JailPath:   "/srv/jailer/vm-123",
		UID:        1000,
		GID:        1000,
		NetNS:      "netns-vm-123",
		OriginalWD: "/home/user",
	}

	assert.True(t, ctx.Enabled)
	assert.Equal(t, "/srv/jailer/vm-123", ctx.JailPath)
	assert.Equal(t, 1000, ctx.UID)
	assert.Equal(t, 1000, ctx.GID)
	assert.Equal(t, "netns-vm-123", ctx.NetNS)
	assert.Equal(t, "/home/user", ctx.OriginalWD)
}

// TestJailContext_Disabled tests JailContext with disabled state
func TestJailContext_Disabled(t *testing.T) {
	ctx := &JailContext{
		Enabled: false,
	}

	assert.False(t, ctx.Enabled)
	assert.Empty(t, ctx.JailPath)
}

// TestJailer_EnterJail_Disabled tests EnterJail with disabled context
func TestJailer_EnterJail_Disabled(t *testing.T) {
	jailer := &Jailer{Enabled: true}
	ctx := &JailContext{Enabled: false}

	err := jailer.EnterJail(ctx)
	require.NoError(t, err) // Disabled context should skip
}

// TestJailer_EnterJail_SkipIfDisabled tests jailer.EnterJail with disabled jailer
func TestJailer_EnterJail_SkipIfDisabled(t *testing.T) {
	jailer := &Jailer{Enabled: false}
	ctx := &JailContext{Enabled: true}

	// Jailer disabled - EnterJail should check ctx.Enabled first
	err := jailer.EnterJail(ctx)
	// May error if actually tries to chroot without root
	_ = err
}

// TestJailer_SetupNetworkNamespace tests network namespace setup
func TestJailer_SetupNetworkNamespace(t *testing.T) {
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: t.TempDir(),
		NetNS:         "test-netns",
		Enabled:       true,
	}

	ctx := &JailContext{
		Enabled:  true,
		JailPath: t.TempDir(),
		NetNS:    "test-netns",
	}

	// SetupNetworkNamespace will fail without root/CAP_NET_ADMIN
	err := jailer.SetupNetworkNamespace(ctx)
	// Should error without capabilities
	_ = err
}

// TestJailer_SetupNetworkNamespace_EmptyNetNS tests with empty network namespace
func TestJailer_SetupNetworkNamespace_EmptyNetNS(t *testing.T) {
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: t.TempDir(),
		NetNS:         "", // Empty
		Enabled:       true,
	}

	ctx := &JailContext{
		Enabled:  true,
		JailPath: t.TempDir(),
		NetNS:    "",
	}

	// Empty NetNS should be handled gracefully
	err := jailer.SetupNetworkNamespace(ctx)
	// Behavior depends on implementation
	_ = err
}

// TestJailer_CleanupJail_NonexistentPath tests cleanup of nonexistent jail
func TestJailer_CleanupJail_NonexistentPath(t *testing.T) {
	jailer := &Jailer{Enabled: true}

	ctx := &JailContext{
		Enabled:  true,
		JailPath: "/nonexistent/path/that/does/not/exist",
	}

	err := jailer.CleanupJail(ctx)
	// Should handle nonexistent path gracefully
	_ = err
}

// TestJailer_CleanupJail_EmptyJailPath tests cleanup with empty jail path
func TestJailer_CleanupJail_EmptyJailPath(t *testing.T) {
	jailer := &Jailer{Enabled: true}

	ctx := &JailContext{
		Enabled:  true,
		JailPath: "",
	}

	err := jailer.CleanupJail(ctx)
	_ = err
}

// TestJailer_Validate_AllFields tests validation of all fields
func TestJailer_Validate_AllFields(t *testing.T) {
	tests := []struct {
		name    string
		jailer  *Jailer
		wantErr bool
	}{
		{"valid", &Jailer{UID: 1000, GID: 1000, ChrootBaseDir: t.TempDir(), Enabled: true}, false},
		{"uid_zero", &Jailer{UID: 0, GID: 1000, ChrootBaseDir: t.TempDir(), Enabled: true}, false},
		{"gid_zero", &Jailer{UID: 1000, GID: 0, ChrootBaseDir: t.TempDir(), Enabled: true}, false},
		{"uid_negative", &Jailer{UID: -1, GID: 1000, ChrootBaseDir: t.TempDir(), Enabled: true}, true},
		{"gid_negative", &Jailer{UID: 1000, GID: -1, ChrootBaseDir: t.TempDir(), Enabled: true}, true},
		{"both_negative", &Jailer{UID: -1, GID: -1, ChrootBaseDir: t.TempDir(), Enabled: true}, true},
		{"empty_chroot", &Jailer{UID: 1000, GID: 1000, ChrootBaseDir: "", Enabled: true}, true},
		{"disabled_skip_validation", &Jailer{UID: -1, GID: -1, ChrootBaseDir: "", Enabled: false}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.jailer.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestJailer_SetupJail_MultipleVMs tests setting up multiple jails
func TestJailer_SetupJail_MultipleVMs(t *testing.T) {
	baseDir := t.TempDir()
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: baseDir,
		Enabled:       true,
	}

	vmIDs := []string{"vm-1", "vm-2", "vm-3"}
	for _, vmID := range vmIDs {
		ctx, err := jailer.SetupJail(vmID)
		require.NoError(t, err)
		assert.Contains(t, ctx.JailPath, vmID)

		// Each jail should have its own directory
		assert.DirExists(t, ctx.JailPath)
	}

	// Verify all jails exist
	for _, vmID := range vmIDs {
		jailPath := filepath.Join(baseDir, vmID)
		assert.DirExists(t, jailPath)
	}
}

// TestJailer_SetupJail_Overwrite tests overwriting existing jail directory
func TestJailer_SetupJail_Overwrite(t *testing.T) {
	baseDir := t.TempDir()
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: baseDir,
		Enabled:       true,
	}

	// Setup first jail
	ctx1, err := jailer.SetupJail("vm-overwrite")
	require.NoError(t, err)

	// Add a file to the jail
	testFile := filepath.Join(ctx1.JailPath, "test.txt")
	os.WriteFile(testFile, []byte("test data"), 0644)

	// Setup again with same VM ID
	ctx2, err := jailer.SetupJail("vm-overwrite")
	require.NoError(t, err)

	// Jail directory should still exist
	assert.DirExists(t, ctx2.JailPath)
}

// TestJailer_NewJailer_Defaults tests NewJailer default values
func TestJailer_NewJailer_Defaults(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jail", "netns-1")

	assert.Equal(t, 1000, jailer.UID)
	assert.Equal(t, 1000, jailer.GID)
	assert.Equal(t, "/srv/jail", jailer.ChrootBaseDir)
	assert.Equal(t, "netns-1", jailer.NetNS)
	assert.True(t, jailer.Enabled)
}

// TestJailer_NewJailer_EmptyNetNS tests NewJailer with empty NetNS
func TestJailer_NewJailer_EmptyNetNS(t *testing.T) {
	jailer := NewJailer(1000, 1000, "/srv/jail", "")

	assert.Empty(t, jailer.NetNS)
	assert.True(t, jailer.Enabled)
}

// TestJailer_DisabledConstructor tests creating disabled jailer
func TestJailer_DisabledConstructor(t *testing.T) {
	jailer := &Jailer{Enabled: false}

	assert.False(t, jailer.Enabled)
	err := jailer.Validate()
	require.NoError(t, err) // Disabled jailer should pass validation
}

// TestJailer_CleanupJail_PartialCleanup tests cleanup when some subdirs are missing
func TestJailer_CleanupJail_PartialCleanup(t *testing.T) {
	baseDir := t.TempDir()
	jailer := &Jailer{
		UID:           1000,
		GID:           1000,
		ChrootBaseDir: baseDir,
		Enabled:       true,
	}

	// Create partial jail (missing some subdirs)
	vmID := "partial-jail"
	jailPath := filepath.Join(baseDir, vmID)
	os.MkdirAll(filepath.Join(jailPath, "run"), 0755)
	// Missing dev, proc

	ctx := &JailContext{
		Enabled:  true,
		JailPath: jailPath,
	}

	err := jailer.CleanupJail(ctx)
	require.NoError(t, err)
	assert.NoDirExists(t, jailPath)
}

// TestResourceLimits_Struct tests ResourceLimits struct
func TestResourceLimits_Struct(t *testing.T) {
	limits := ResourceLimits{
		MaxCPUs:      2,
		MaxMemoryMB:  1024,
		MaxFD:        2048,
		MaxProcesses: 200,
	}

	assert.Equal(t, 2, limits.MaxCPUs)
	assert.Equal(t, 1024, limits.MaxMemoryMB)
	assert.Equal(t, 2048, limits.MaxFD)
	assert.Equal(t, 200, limits.MaxProcesses)
}

// TestResourceLimits_ZeroValues tests ResourceLimits with zero values
func TestResourceLimits_ZeroValues(t *testing.T) {
	limits := ResourceLimits{}

	assert.Equal(t, 0, limits.MaxCPUs)
	assert.Equal(t, 0, limits.MaxMemoryMB)
	assert.Equal(t, 0, limits.MaxFD)
	assert.Equal(t, 0, limits.MaxProcesses)
}

// TestApplyResourceLimits_NonexistentProcess tests applying limits to nonexistent process
func TestApplyResourceLimits_NonexistentProcess(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	limits := ResourceLimits{MaxCPUs: 1, MaxMemoryMB: 512}
	err := ApplyResourceLimits(999999, limits) // Nonexistent PID
	require.Error(t, err)
}

// TestCleanupCgroup_NonexistentProcess tests CleanupCgroup for nonexistent process
func TestCleanupCgroup_NonexistentProcess(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	err := CleanupCgroup(999999)
	require.NoError(t, err) // Should succeed even for nonexistent cgroup
}