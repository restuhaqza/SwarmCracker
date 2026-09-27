package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestNewSecretManager tests creating a new SecretManager.
func TestNewSecretManager(t *testing.T) {
	tests := []struct {
		name       string
		secretsDir string
		configsDir string
		wantCreate bool
	}{
		{
			name:       "creates both directories",
			secretsDir: "test-secrets",
			configsDir: "test-configs",
			wantCreate: true,
		},
		{
			name:       "empty directories",
			secretsDir: "",
			configsDir: "",
			wantCreate: false,
		},
		{
			name:       "only secrets directory",
			secretsDir: "test-secrets-only",
			configsDir: "",
			wantCreate: true,
		},
		{
			name:       "only configs directory",
			secretsDir: "",
			configsDir: "test-configs-only",
			wantCreate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use temp directory for test isolation
			tmpDir := t.TempDir()

			var secretsPath, configsPath string
			if tt.secretsDir != "" {
				secretsPath = filepath.Join(tmpDir, tt.secretsDir)
			}
			if tt.configsDir != "" {
				configsPath = filepath.Join(tmpDir, tt.configsDir)
			}

			sm := NewSecretManager(secretsPath, configsPath)

			if sm == nil {
				t.Fatal("NewSecretManager returned nil")
			}

			if sm.secretsDir != secretsPath {
				t.Errorf("secretsDir = %q, want %q", sm.secretsDir, secretsPath)
			}

			if sm.configsDir != configsPath {
				t.Errorf("configsDir = %q, want %q", sm.configsDir, configsPath)
			}

			// Verify directories were created
			if tt.wantCreate {
				if secretsPath != "" {
					if info, err := os.Stat(secretsPath); err != nil {
						t.Errorf("secrets directory not created: %v", err)
					} else if !info.IsDir() {
						t.Error("secrets path is not a directory")
					}
				}
				if configsPath != "" {
					if info, err := os.Stat(configsPath); err != nil {
						t.Errorf("configs directory not created: %v", err)
					} else if !info.IsDir() {
						t.Error("configs path is not a directory")
					}
				}
			}
		})
	}
}

// TestInjectSecrets tests the InjectSecrets method.
func TestInjectSecrets(t *testing.T) {
	tests := []struct {
		name        string
		secrets     []types.SecretRef
		setupRootfs func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name:    "no secrets - success",
			secrets: []types.SecretRef{},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr: false,
		},
		{
			name: "single secret - needs root",
			secrets: []types.SecretRef{
				{
					ID:     "secret-1",
					Name:   "my_secret",
					Target: "/run/secrets/my_secret",
					Data:   []byte("secret data"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "multiple secrets - needs root",
			secrets: []types.SecretRef{
				{
					ID:     "secret-1",
					Name:   "db_password",
					Target: "/run/secrets/db_password",
					Data:   []byte("password123"),
				},
				{
					ID:     "secret-2",
					Name:   "api_key",
					Target: "/run/secrets/api_key",
					Data:   []byte("key-xyz-789"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "secret with default target - needs root",
			secrets: []types.SecretRef{
				{
					ID:   "secret-1",
					Name: "default_secret",
					Data: []byte("data"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "secret with nested path - needs root",
			secrets: []types.SecretRef{
				{
					ID:     "secret-1",
					Name:   "nested_secret",
					Target: "/run/secrets/app/config/db_password",
					Data:   []byte("nested data"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfsPath := tt.setupRootfs(t)
			defer cleanupFakeRootfs(t, rootfsPath)

			sm := NewSecretManager("", "")
			ctx := context.Background()

			err := sm.InjectSecrets(ctx, "task-123", tt.secrets, rootfsPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("InjectSecrets() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errContains)
				} else if !containsString(err.Error(), tt.errContains) {
					t.Errorf("Error = %q, want containing %q", err, tt.errContains)
				}
			}

			// Verify secrets were written correctly
			if !tt.wantErr {
				for _, secret := range tt.secrets {
					targetPath := secret.Target
					if targetPath == "" {
						targetPath = filepath.Join("/run/secrets", secret.Name)
					}

					// Since we're using a fake rootfs with directories, check if file exists
					// In real scenario, this would be in the mounted rootfs
					if !isMockedRootfs(rootfsPath) {
						// Only verify for real filesystems
						fullPath := filepath.Join(rootfsPath, targetPath)
						if _, err := os.Stat(fullPath); err != nil {
							t.Errorf("Secret file not created at %s: %v", fullPath, err)
						}
					}
				}
			}
		})
	}
}

// TestInjectConfigs tests the InjectConfigs method.
func TestInjectConfigs(t *testing.T) {
	tests := []struct {
		name        string
		configs     []types.ConfigRef
		setupRootfs func(t *testing.T) string
		wantErr     bool
		errContains string
	}{
		{
			name:    "no configs - success",
			configs: []types.ConfigRef{},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr: false,
		},
		{
			name: "single config - needs root",
			configs: []types.ConfigRef{
				{
					ID:     "config-1",
					Name:   "app_config",
					Target: "/config/app.yaml",
					Data:   []byte("key: value\n"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "multiple configs - needs root",
			configs: []types.ConfigRef{
				{
					ID:     "config-1",
					Name:   "nginx_conf",
					Target: "/config/nginx/nginx.conf",
					Data:   []byte("server {\n  listen 80;\n}\n"),
				},
				{
					ID:     "config-2",
					Name:   "app_yaml",
					Target: "/config/app/app.yaml",
					Data:   []byte("app:\n  port: 8080\n"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "config with default target - needs root",
			configs: []types.ConfigRef{
				{
					ID:   "config-1",
					Name: "default_config",
					Data: []byte("default data"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
		{
			name: "config with nested path - needs root",
			configs: []types.ConfigRef{
				{
					ID:     "config-1",
					Name:   "nested_config",
					Target: "/config/app/production/database.conf",
					Data:   []byte("db.host=localhost\n"),
				},
			},
			setupRootfs: func(t *testing.T) string {
				return createFakeRootfs(t)
			},
			wantErr:     true,
			errContains: "debugfs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootfsPath := tt.setupRootfs(t)
			defer cleanupFakeRootfs(t, rootfsPath)

			sm := NewSecretManager("", "")
			ctx := context.Background()

			err := sm.InjectConfigs(ctx, "task-456", tt.configs, rootfsPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("InjectConfigs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errContains)
				} else if !containsString(err.Error(), tt.errContains) {
					t.Errorf("Error = %q, want containing %q", err, tt.errContains)
				}
			}

			// Verify configs were written correctly
			if !tt.wantErr {
				for _, config := range tt.configs {
					targetPath := config.Target
					if targetPath == "" {
						targetPath = filepath.Join("/config", config.Name)
					}

					if !isMockedRootfs(rootfsPath) {
						fullPath := filepath.Join(rootfsPath, targetPath)
						if _, err := os.Stat(fullPath); err != nil {
							t.Errorf("Config file not created at %s: %v", fullPath, err)
						}
					}
				}
			}
		})
	}
}
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

// createFakeRootfs creates a fake rootfs directory structure for testing.
// Returns the path to the fake rootfs.
func createFakeRootfs(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	// Create marker file to indicate this is a fake rootfs
	markerPath := filepath.Join(tmpDir, ".fake-rootfs")
	if err := os.WriteFile(markerPath, []byte("fake"), 0644); err != nil {
		t.Fatalf("Failed to create fake rootfs marker: %v", err)
	}

	return tmpDir
}

// cleanupFakeRootfs cleans up a fake rootfs directory.
func cleanupFakeRootfs(t *testing.T, rootfsPath string) {
	t.Helper()
	// TempDir is automatically cleaned up by t.TempDir()
	// This function exists for API compatibility
}

// isMockedRootfs checks if the rootfs is a fake/mocked one.
func isMockedRootfs(rootfsPath string) bool {
	markerPath := filepath.Join(rootfsPath, ".fake-rootfs")
	_, err := os.Stat(markerPath)
	return err == nil
}
