// +build integration

package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSecureFilePermissions_RootIntegration tests the full success path with root
func TestSecureFilePermissions_RootIntegration(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	testFile := filepath.Join(t.TempDir(), "integration_test.txt")
	err := os.WriteFile(testFile, []byte("test content for integration"), 0644)
	require.NoError(t, err)

	// This should succeed and cover all lines in SecureFilePermissions
	err = SecureFilePermissions(testFile)
	assert.NoError(t, err, "SecureFilePermissions should succeed with root")

	// Verify the permissions
	info, err := os.Stat(testFile)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "File should have 0600 permissions")
}

// TestSecureDirectoryPermissions_RootIntegration tests the full success path with root
func TestSecureDirectoryPermissions_RootIntegration(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	testDir := filepath.Join(t.TempDir(), "integration_test_dir")
	err := os.MkdirAll(testDir, 0755)
	require.NoError(t, err)

	// This should succeed and cover all lines in SecureDirectoryPermissions
	err = SecureDirectoryPermissions(testDir)
	assert.NoError(t, err, "SecureDirectoryPermissions should succeed with root")

	// Verify the permissions
	info, err := os.Stat(testDir)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(), "Directory should have 0700 permissions")
}

// TestCheckCapabilities_RootIntegration tests the full capability check with root
func TestCheckCapabilities_RootIntegration(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	// This should succeed and cover all lines in CheckCapabilities
	err := CheckCapabilities()
	assert.NoError(t, err, "CheckCapabilities should succeed with root")

	// The function should have logged about capabilities
	// We can't directly verify logs but we verify no error occurred
}

// TestEnterJail_RootIntegration tests the full EnterJail success path
// Note: This test cannot actually execute EnterJail as it would chroot the test process
// Instead, we verify the jail context is properly prepared
func TestEnterJail_RootIntegration(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tempDir := t.TempDir()
	jailer := NewJailer(0, 0, tempDir, "")

	// Setup jail (this calls setResourceLimits which is partially untested)
	ctx, err := jailer.SetupJail("integration-test-vm")
	require.NoError(t, err)
	require.NotNil(t, ctx)

	// Verify jail is ready
	assert.True(t, ctx.Enabled)
	assert.NotEmpty(t, ctx.JailPath)
	assert.DirExists(t, ctx.JailPath)

	// Note: We cannot call EnterJail here as it would chroot the test process
	// In a real integration test, this would run in a forked child process
}

// TestSecureFilePermissions_RootMultiplePaths tests securing multiple files
func TestSecureFilePermissions_RootMultiplePaths(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tempDir := t.TempDir()

	// Create multiple test files with different initial permissions
	testCases := []struct {
		name     string
		content  string
		perm     os.FileMode
	}{
		{"file1.txt", "content1", 0644},
		{"file2.txt", "content2", 0666},
		{"file3.txt", "content3", 0600},
		{"file4.txt", "content4", 0400},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testFile := filepath.Join(tempDir, tc.name)
			err := os.WriteFile(testFile, []byte(tc.content), tc.perm)
			require.NoError(t, err)

			err = SecureFilePermissions(testFile)
			assert.NoError(t, err)

			info, _ := os.Stat(testFile)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
		})
	}
}

// TestSecureDirectoryPermissions_RootMultipleDirs tests securing multiple directories
func TestSecureDirectoryPermissions_RootMultipleDirs(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tempDir := t.TempDir()

	// Create multiple test directories with different initial permissions
	testCases := []struct {
		name string
		perm os.FileMode
	}{
		{"dir1", 0755},
		{"dir2", 0777},
		{"dir3", 0700},
		{"dir4", 0500},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testDir := filepath.Join(tempDir, tc.name)
			err := os.MkdirAll(testDir, tc.perm)
			require.NoError(t, err)

			err = SecureDirectoryPermissions(testDir)
			assert.NoError(t, err)

			info, _ := os.Stat(testDir)
			assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
		})
	}
}

// TestCheckCapabilities_RootRepeatedCalls tests calling CheckCapabilities multiple times
func TestCheckCapabilities_RootRepeatedCalls(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	// Call multiple times to ensure consistency and hit all code paths
	for i := 0; i < 10; i++ {
		err := CheckCapabilities()
		assert.NoError(t, err, "CheckCapabilities should consistently succeed as root (iteration %d)", i)
	}
}

// TestSecureFilePermissions_RootWithSymlinks tests securing files that are symlinks
func TestSecureFilePermissions_RootWithSymlinks(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tempDir := t.TempDir()
	realFile := filepath.Join(tempDir, "real.txt")
	linkFile := filepath.Join(tempDir, "link.txt")

	err := os.WriteFile(realFile, []byte("real content"), 0644)
	require.NoError(t, err)

	err = os.Symlink(realFile, linkFile)
	require.NoError(t, err)

	// Should work on the symlink (changes the real file)
	err = SecureFilePermissions(linkFile)
	assert.NoError(t, err)

	info, err := os.Stat(realFile)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestSecureDirectoryPermissions_RootWithSymlinks tests securing directories that are symlinks
func TestSecureDirectoryPermissions_RootWithSymlinks(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tempDir := t.TempDir()
	realDir := filepath.Join(tempDir, "real_dir")
	linkDir := filepath.Join(tempDir, "link_dir")

	err := os.MkdirAll(realDir, 0755)
	require.NoError(t, err)

	err = os.Symlink(realDir, linkDir)
	require.NoError(t, err)

	// Should work on the symlink
	err = SecureDirectoryPermissions(linkDir)
	assert.NoError(t, err)

	info, err := os.Stat(realDir)
	assert.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

// TestSecureFilePermissions_RootInodes tests securing files that test inode handling
func TestSecureFilePermissions_RootInodes(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	// Test that chown+chmod works correctly on the same file
	testFile := filepath.Join(t.TempDir(), "inode_test.txt")

	// Create with minimal permissions
	err := os.WriteFile(testFile, []byte("test"), 0000)
	require.NoError(t, err)

	// Apply secure permissions
	err = SecureFilePermissions(testFile)
	assert.NoError(t, err)

	// Verify
	info, _ := os.Stat(testFile)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestSecureDirectoryPermissions_RootInodes tests securing directories
func TestSecureDirectoryPermissions_RootInodes(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	// Test that chown+chmod works correctly on the same directory
	testDir := filepath.Join(t.TempDir(), "inode_test_dir")

	// Create with minimal permissions
	err := os.MkdirAll(testDir, 0000)
	require.NoError(t, err)

	// Apply secure permissions
	err = SecureDirectoryPermissions(testDir)
	assert.NoError(t, err)

	// Verify
	info, _ := os.Stat(testDir)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}
