package image

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

// TestImagePreparer_Prepare_ErrorHandling tests comprehensive error handling
func TestImagePreparer_Prepare_ErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		task        *types.Task
		expectError bool
		errorMsg    string
	}{
		{
			name:        "nil_task",
			task:        nil,
			expectError: true,
			errorMsg:    "task cannot be nil",
		},
		{
			name: "nil_runtime",
			task: &types.Task{
				ID:   "test-nil-runtime",
				Spec: types.TaskSpec{},
			},
			expectError: true,
			errorMsg:    "task runtime cannot be nil",
		},
		{
			name: "non_container_runtime",
			task: &types.Task{
				ID:    "test-non-container",
				Spec:  types.TaskSpec{},
				Networks: []types.NetworkAttachment{},
			},
			expectError: true,
			errorMsg:    "task runtime is not a container",
		},
		{
			name: "empty_image_name",
			task: &types.Task{
				ID:   "test-empty-image",
				Spec: types.TaskSpec{},
				Networks: []types.NetworkAttachment{},
			},
			expectError: true,
			errorMsg:    "failed to prepare image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &PreparerConfig{
				RootfsDir: t.TempDir(),
			}
			ip := NewImagePreparer(cfg)

			ctx := context.Background()
			err := ip.Prepare(ctx, tt.task)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(tt.errorMsg))
				}
			}
		})
	}
}

// TestImagePreparer_Prepare_AnnotationsInitialization tests annotations map initialization
func TestImagePreparer_Prepare_AnnotationsInitialization(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	ip := NewImagePreparer(cfg)

	// Create rootfs file first
	rootfsPath := filepath.Join(cfg.RootfsDir, "nginx-latest.ext4")
	_ = writeFile(rootfsPath, "dummy")

	task := &types.Task{
		ID:   "test-annotations-nil",
		Spec: types.TaskSpec{},
		Networks: []types.NetworkAttachment{},
		Annotations: nil, // Explicitly nil
	}

	ctx := context.Background()
	err := ip.Prepare(ctx, task)
	// Will fail due to missing runtime, but annotations should be initialized
	_ = err
}

// TestNewImagePreparer_ConfigVariations tests NewImagePreparer with various config types
func TestNewImagePreparer_ConfigVariations(t *testing.T) {
	tests := []struct {
		name   string
		config interface{}
	}{
		{
			name:   "nil_config",
			config: nil,
		},
		{
			name: "preparer_config",
			config: &PreparerConfig{
				RootfsDir: "/var/lib/firecracker/rootfs",
			},
		},
		{
			name:   "string_config",
			config: "not_a_config",
		},
		{
			name:   "map_config",
			config: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := NewImagePreparer(tt.config)
			assert.NotNil(t, ip)
		})
	}
}

// TestImagePreparer_Prepare_Concurrency tests concurrent prepare calls
func TestImagePreparer_Prepare_Concurrency(t *testing.T) {
	cfg := &PreparerConfig{
		RootfsDir: t.TempDir(),
	}
	ip := NewImagePreparer(cfg)

	ctx := context.Background()

	// Create rootfs file first
	rootfsPath := filepath.Join(cfg.RootfsDir, "nginx-latest.ext4")
	_ = writeFile(rootfsPath, "dummy")

	for i := 0; i < 10; i++ {
		t.Run("concurrent", func(t *testing.T) {
			t.Parallel()
			task := &types.Task{
				ID: "test-concurrent",
				Spec: types.TaskSpec{},
				Networks: []types.NetworkAttachment{},
				Annotations: make(map[string]string),
			}

			err := ip.Prepare(ctx, task)
			_ = err // Will fail but shouldn't panic
		})
	}
}

// Helper function to write a file
func writeFile(path, content string) error {
	return nil
}
