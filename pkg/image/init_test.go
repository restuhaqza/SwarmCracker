package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitInjector_Tini tests tini init system injection.
func TestInitInjector_Tini(t *testing.T) {
	config := &InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	}

	injector := NewInitInjector(config)

	assert.True(t, injector.IsEnabled())
	assert.Equal(t, "/sbin/tini", injector.GetInitPath())
	assert.Equal(t, 10, injector.GetGracePeriod())

	// Test init args wrapping
	containerArgs := []string{"/usr/bin/nginx", "-g", "daemon off;"}
	initArgs := injector.GetInitArgs(containerArgs)

	expected := []string{"/sbin/tini", "--", "/usr/bin/nginx", "-g", "daemon off;"}
	assert.Equal(t, expected, initArgs)
}

// TestInitInjector_DumbInit tests dumb-init init system injection.
func TestInitInjector_DumbInit(t *testing.T) {
	config := &InitSystemConfig{
		Type:           InitSystemDumbInit,
		GracePeriodSec: 5,
	}

	injector := NewInitInjector(config)

	assert.True(t, injector.IsEnabled())
	assert.Equal(t, "/sbin/dumb-init", injector.GetInitPath())
	assert.Equal(t, 5, injector.GetGracePeriod())

	// Test init args wrapping
	containerArgs := []string{"/usr/bin/redis-server"}
	initArgs := injector.GetInitArgs(containerArgs)

	expected := []string{"/sbin/dumb-init", "/usr/bin/redis-server"}
	assert.Equal(t, expected, initArgs)
}

// TestInitInjector_None tests no init system.
func TestInitInjector_None(t *testing.T) {
	config := &InitSystemConfig{
		Type: InitSystemNone,
	}

	injector := NewInitInjector(config)

	assert.False(t, injector.IsEnabled())
	assert.Equal(t, "", injector.GetInitPath())
	assert.Equal(t, 0, injector.GetGracePeriod())

	// Test that args are not wrapped
	containerArgs := []string{"/bin/bash"}
	initArgs := injector.GetInitArgs(containerArgs)

	assert.Equal(t, containerArgs, initArgs)
}

// TestInitInjector_DefaultConfig tests default configuration.
func TestInitInjector_DefaultConfig(t *testing.T) {
	// Nil config should use defaults
	injector := NewInitInjector(nil)

	assert.True(t, injector.IsEnabled())
	assert.Equal(t, InitSystemTini, injector.config.Type)
	assert.Equal(t, 10, injector.GetGracePeriod())
}

// TestInitInjector_CreateMinimalInit tests minimal init script creation.
func TestInitInjector_CreateMinimalInit(t *testing.T) {
	config := &InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	}

	injector := NewInitInjector(config)

	// Create temp directory
	tempDir := t.TempDir()

	// Create minimal init
	err := injector.createMinimalInit(tempDir, "tini")
	require.NoError(t, err)

	// Check that init script was created
	initPath := filepath.Join(tempDir, "sbin", "tini")
	assert.FileExists(t, initPath)

	// Check that symlink was created
	initLink := filepath.Join(tempDir, "init")
	if _, err := os.Lstat(initLink); err == nil {
		// Symlink exists, verify it points to the right place
		target, err := os.Readlink(initLink)
		assert.NoError(t, err)
		assert.Equal(t, "/sbin/tini", target)
	}
}

// TestImagePreparer_InitSystemIntegration tests init system in preparer.
func TestImagePreparer_InitSystemIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &PreparerConfig{
		RootfsDir:       tmpDir,
		InitSystem:      "tini",
		InitGracePeriod: 15,
	}

	preparer := NewImagePreparer(cfg).(*ImagePreparer)

	// Verify init injector is configured
	assert.NotNil(t, preparer.initInjector)
	assert.True(t, preparer.initInjector.IsEnabled())
	assert.Equal(t, 15, preparer.initInjector.GetGracePeriod())
	assert.Equal(t, "/sbin/tini", preparer.initInjector.GetInitPath())
}

// TestImagePreparer_InitSystemDisabled tests when init system is disabled.
func TestImagePreparer_InitSystemDisabled(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &PreparerConfig{
		RootfsDir:       tmpDir,
		InitSystem:      "none",
		InitGracePeriod: 10,
	}

	preparer := NewImagePreparer(cfg).(*ImagePreparer)

	// Verify init injector is disabled
	assert.NotNil(t, preparer.initInjector)
	assert.False(t, preparer.initInjector.IsEnabled())
	assert.Equal(t, "", preparer.initInjector.GetInitPath())
}

// TestImagePreparer_DefaultInitSystem tests default init system.
func TestImagePreparer_DefaultInitSystem(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &PreparerConfig{
		RootfsDir: tmpDir,
		// InitSystem not specified, should default to tini
	}

	preparer := NewImagePreparer(cfg).(*ImagePreparer)

	// Verify defaults
	assert.NotNil(t, preparer.initInjector)
	assert.True(t, preparer.initInjector.IsEnabled())
	assert.Equal(t, InitSystemTini, preparer.initInjector.config.Type)
	assert.Equal(t, 10, preparer.initInjector.GetGracePeriod())
}
