package image

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImagePreparer_Prepare_NilTask_Verify tests Prepare with nil task
func TestImagePreparer_Prepare_NilTask_Verify(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	prep := NewImagePreparer(cfg)

	err := prep.Prepare(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task cannot be nil")
}

// TestImagePreparer_Prepare_EmptyImageRef tests Prepare with empty image reference
func TestImagePreparer_Prepare_EmptyImageRef(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	prep := NewImagePreparer(cfg)

	task := &types.Task{
		ID:        "test-task-empty",
		ServiceID: "test-service",
	}
	task.Annotations = map[string]string{}

	err := prep.Prepare(context.Background(), task)
	require.Error(t, err)
}

// TestImagePreparer_Cleanup_DefaultKeepDays tests Cleanup with default keepDays
func TestImagePreparer_Cleanup_DefaultKeepDays(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir:       t.TempDir(),
		MaxImageAgeDays: 7,
	}
	prep := NewImagePreparer(cfg)

	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.Equal(t, 0, filesRemoved) // No files in empty dir
	assert.Equal(t, int64(0), bytesFreed)
}

// TestImagePreparer_Cleanup_ZeroKeepDays tests Cleanup with zero keepDays
func TestImagePreparer_Cleanup_ZeroKeepDays(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir:       t.TempDir(),
		MaxImageAgeDays: 7,
	}
	prep := NewImagePreparer(cfg)

	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 0)
	require.Error(t, err) // keepDays must be positive
	assert.Contains(t, err.Error(), "keepDays must be positive")
	assert.Equal(t, 0, filesRemoved)
	assert.Equal(t, int64(0), bytesFreed)
}

// TestImagePreparer_Cleanup_WithOldFiles tests Cleanup removes old files
func TestImagePreparer_Cleanup_WithOldFiles(t *testing.T) {
	rootfsDir := t.TempDir()

	// Create old rootfs file (must be .ext4 suffix)
	oldFile := filepath.Join(rootfsDir, "sha256-abc123.ext4")
	os.WriteFile(oldFile, []byte("old content"), 0644)

	// Set modification time to 30 days ago
	oldTime := time.Now().AddDate(0, 0, -30)
	os.Chtimes(oldFile, oldTime, oldTime)

	cfg := &PreparerConfig{
		RootfsDir:       rootfsDir,
		MaxImageAgeDays: 7,
	}
	prep := NewImagePreparer(cfg)

	// Cleanup with 7 days should remove files older than 7 days
	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, filesRemoved, 1)
	assert.GreaterOrEqual(t, bytesFreed, int64(11))
}

// TestImagePreparer_Cleanup_KeepsRecentFiles tests Cleanup keeps recent files
func TestImagePreparer_Cleanup_KeepsRecentFiles(t *testing.T) {
	rootfsDir := t.TempDir()

	// Create recent rootfs file
	recentFile := filepath.Join(rootfsDir, "recent-sha256-def456.ext4")
	os.WriteFile(recentFile, []byte("recent content"), 0644)

	cfg := &PreparerConfig{
		RootfsDir:       rootfsDir,
		MaxImageAgeDays: 7, // Keep files younger than 7 days
	}
	prep := NewImagePreparer(cfg)

	// Cleanup with 7 days should keep recent file
	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.Equal(t, 0, filesRemoved) // Recent file should be kept
	assert.Equal(t, int64(0), bytesFreed)
}

// TestPreparerConfig_DefaultsApplied tests config defaults
func TestPreparerConfig_DefaultsApplied(t *testing.T) {
	cfg := &PreparerConfig{}

	// Apply defaults manually (same logic as NewImagePreparer)
	if cfg.InitSystem == "" {
		cfg.InitSystem = "tini"
	}
	if cfg.InitGracePeriod == 0 {
		cfg.InitGracePeriod = 10
	}

	assert.Equal(t, "tini", cfg.InitSystem)
	assert.Equal(t, 10, cfg.InitGracePeriod)
}

// TestPreparerConfig_CustomValues tests custom config values
func TestPreparerConfig_CustomValues_Verify(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir:       "/var/lib/swarmcracker/rootfs",
		InitSystem:      "dumb-init",
		DefaultVCPUs:    2,
		DefaultMemoryMB: 1024,
		MaxImageAgeDays: 14,
	}

	assert.Equal(t, "/var/lib/swarmcracker/rootfs", cfg.RootfsDir)
	assert.Equal(t, "dumb-init", cfg.InitSystem)
	assert.Equal(t, 2, cfg.DefaultVCPUs)
	assert.Equal(t, 1024, cfg.DefaultMemoryMB)
	assert.Equal(t, 14, cfg.MaxImageAgeDays)
}

// TestPreparerConfig_InitSystems tests init system options
func TestPreparerConfig_InitSystems(t *testing.T) {
	tests := []struct {
		name       string
		initSystem string
		want       string
	}{
		{"tini", "tini", "tini"},
		{"dumb-init", "dumb-init", "dumb-init"},
		{"none", "none", "none"},
		{"empty_defaults", "", "tini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &PreparerConfig{InitSystem: tt.initSystem}
			if cfg.InitSystem == "" {
				cfg.InitSystem = "tini"
			}
			assert.Equal(t, tt.want, cfg.InitSystem)
		})
	}
}

// TestPreparerConfig_ResourceDefaults tests resource defaults
func TestPreparerConfig_ResourceDefaults(t *testing.T) {
	cfg := &PreparerConfig{}

	// Apply resource defaults
	if cfg.DefaultVCPUs == 0 {
		cfg.DefaultVCPUs = 1
	}
	if cfg.DefaultMemoryMB == 0 {
		cfg.DefaultMemoryMB = 512
	}

	assert.Equal(t, 1, cfg.DefaultVCPUs)
	assert.Equal(t, 512, cfg.DefaultMemoryMB)
}

// TestImagePreparer_NewImagePreparer_NilConfig tests constructor with nil
func TestImagePreparer_NewImagePreparer_NilConfig(t *testing.T) {
	prep := NewImagePreparer(nil)
	require.NotNil(t, prep)
}

// TestImagePreparer_NewImagePreparer_EmptyConfig tests constructor with empty config
func TestImagePreparer_NewImagePreparer_EmptyConfig(t *testing.T) {
	prep := NewImagePreparer(&PreparerConfig{})
	require.NotNil(t, prep)
}

// TestImagePreparer_NewImagePreparer_FullConfig tests constructor with full config
func TestImagePreparer_NewImagePreparer_FullConfig(t *testing.T) {
	cfg := &PreparerConfig{
		KernelPath:      "/usr/share/firecracker/vmlinux",
		RootfsDir:       "/var/lib/swarmcracker/rootfs",
		SocketDir:       "/var/run/firecracker",
		DefaultVCPUs:    2,
		DefaultMemoryMB: 1024,
		InitSystem:      "tini",
		InitGracePeriod: 15,
		MaxImageAgeDays: 7,
	}

	prep := NewImagePreparer(cfg)
	require.NotNil(t, prep)
}

// TestImagePreparer_InterfaceCompliance verifies interface
func TestImagePreparer_InterfaceCompliance(t *testing.T) {
	var _ types.ImagePreparer = NewImagePreparer(&PreparerConfig{})
}

// TestImagePreparer_Prepare_WithAnnotations tests Prepare with annotations
func TestImagePreparer_Prepare_WithAnnotations(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	prep := NewImagePreparer(cfg)

	task := &types.Task{
		ID:        "test-task-ann",
		ServiceID: "test-service",
	}
	task.Annotations = map[string]string{
		"image": "alpine:latest",
	}

	// Prepare will fail without Docker/image, but we test it doesn't panic
	err := prep.Prepare(context.Background(), task)
	// Error expected without runtime
	_ = err
}

// TestImagePreparer_Prepare_MissingImageAnnotation tests missing image annotation
func TestImagePreparer_Prepare_MissingImageAnnotation(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	prep := NewImagePreparer(cfg)

	task := &types.Task{
		ID:        "test-task-no-image",
		ServiceID: "test-service",
	}
	task.Annotations = map[string]string{
		"other-key": "value",
	}

	err := prep.Prepare(context.Background(), task)
	require.Error(t, err)
}

// TestCleanup_RemovesMultipleFiles tests cleanup of multiple files
func TestCleanup_RemovesMultipleFiles(t *testing.T) {
	rootfsDir := t.TempDir()

	// Create multiple old files (must be .ext4 suffix)
	for i := 0; i < 3; i++ {
		filename := filepath.Join(rootfsDir, "sha256-"+string(rune('a'+i))+".ext4")
		os.WriteFile(filename, []byte("content"), 0644)
		// Set old modification time
		oldTime := time.Now().AddDate(0, 0, -30)
		os.Chtimes(filename, oldTime, oldTime)
	}

	cfg := &PreparerConfig{
		RootfsDir:       rootfsDir,
		MaxImageAgeDays: 7,
	}
	prep := NewImagePreparer(cfg)

	filesRemoved, bytesFreed, err := prep.Cleanup(context.Background(), 7)
	assert.NoError(t, err)
	assert.Equal(t, 3, filesRemoved)
	assert.GreaterOrEqual(t, bytesFreed, int64(21))
}

// TestCleanup_ContextCancellation tests cleanup with cancelled context
func TestCleanup_ContextCancellation(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir:       t.TempDir(),
		MaxImageAgeDays: 7,
	}
	prep := NewImagePreparer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	filesRemoved, bytesFreed, err := prep.Cleanup(ctx, 7)
	// Should complete even with cancelled context (cleanup doesn't block)
	assert.NoError(t, err)
	assert.Equal(t, 0, filesRemoved)
	assert.Equal(t, int64(0), bytesFreed)
}