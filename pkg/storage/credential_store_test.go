package storage

import (
	"context"
	"fmt"
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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
			errContains: "failed to mount rootfs",
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

// TestInjectSecret tests the injectSecret method (unexported).
func TestInjectSecret(t *testing.T) {
	tests := []struct {
		name       string
		secret     types.SecretRef
		setupMount func(t *testing.T) string
		wantErr    bool
		verify     func(t *testing.T, mountDir string, secret types.SecretRef)
	}{
		{
			name: "secret with custom target",
			secret: types.SecretRef{
				ID:     "secret-1",
				Name:   "my_secret",
				Target: "/custom/path/secret.txt",
				Data:   []byte("secret value"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, secret types.SecretRef) {
				fullPath := filepath.Join(mountDir, secret.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read secret file: %v", err)
					return
				}
				if string(data) != string(secret.Data) {
					t.Errorf("Secret data = %q, want %q", data, secret.Data)
				}

				// Check file permissions (should be 0400)
				info, err := os.Stat(fullPath)
				if err != nil {
					t.Errorf("Failed to stat secret file: %v", err)
					return
				}
				if info.Mode().Perm() != 0400 {
					t.Errorf("Secret file permissions = %v, want 0400", info.Mode().Perm())
				}
			},
		},
		{
			name: "secret with default target",
			secret: types.SecretRef{
				ID:   "secret-2",
				Name: "default_secret",
				Data: []byte("default data"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, secret types.SecretRef) {
				expectedPath := filepath.Join(mountDir, "/run/secrets", secret.Name)
				data, err := os.ReadFile(expectedPath)
				if err != nil {
					t.Errorf("Failed to read secret file: %v", err)
					return
				}
				if string(data) != string(secret.Data) {
					t.Errorf("Secret data = %q, want %q", data, secret.Data)
				}
			},
		},
		{
			name: "secret with nested directories",
			secret: types.SecretRef{
				ID:     "secret-3",
				Name:   "nested_secret",
				Target: "/run/secrets/app/config/db",
				Data:   []byte("nested: value"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, secret types.SecretRef) {
				fullPath := filepath.Join(mountDir, secret.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read secret file: %v", err)
					return
				}
				if string(data) != string(secret.Data) {
					t.Errorf("Secret data = %q, want %q", data, secret.Data)
				}
			},
		},
		{
			name: "secret with empty data",
			secret: types.SecretRef{
				ID:     "secret-4",
				Name:   "empty_secret",
				Target: "/run/secrets/empty",
				Data:   []byte(""),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, secret types.SecretRef) {
				fullPath := filepath.Join(mountDir, secret.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read secret file: %v", err)
					return
				}
				if len(data) != 0 {
					t.Errorf("Secret data length = %d, want 0", len(data))
				}
			},
		},
		{
			name: "secret with binary data",
			secret: types.SecretRef{
				ID:     "secret-5",
				Name:   "binary_secret",
				Target: "/run/secrets/binary",
				Data:   []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD},
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, secret types.SecretRef) {
				fullPath := filepath.Join(mountDir, secret.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read secret file: %v", err)
					return
				}
				if len(data) != len(secret.Data) {
					t.Errorf("Secret data length = %d, want %d", len(data), len(secret.Data))
				}
				for i := range data {
					if data[i] != secret.Data[i] {
						t.Errorf("Secret data byte[%d] = %v, want %v", i, data[i], secret.Data[i])
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mountDir := tt.setupMount(t)
			sm := NewSecretManager("", "")

			err := sm.injectSecret(mountDir, tt.secret)

			if (err != nil) != tt.wantErr {
				t.Errorf("injectSecret() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.verify != nil {
				tt.verify(t, mountDir, tt.secret)
			}
		})
	}
}

// TestInjectConfig tests the injectConfig method (unexported).
func TestInjectConfig(t *testing.T) {
	tests := []struct {
		name       string
		config     types.ConfigRef
		setupMount func(t *testing.T) string
		wantErr    bool
		verify     func(t *testing.T, mountDir string, config types.ConfigRef)
	}{
		{
			name: "config with custom target",
			config: types.ConfigRef{
				ID:     "config-1",
				Name:   "app_config",
				Target: "/custom/path/config.yaml",
				Data:   []byte("key: value\n"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, config types.ConfigRef) {
				fullPath := filepath.Join(mountDir, config.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
					return
				}
				if string(data) != string(config.Data) {
					t.Errorf("Config data = %q, want %q", data, config.Data)
				}

				// Check file permissions (should be 0444)
				info, err := os.Stat(fullPath)
				if err != nil {
					t.Errorf("Failed to stat config file: %v", err)
					return
				}
				if info.Mode().Perm() != 0444 {
					t.Errorf("Config file permissions = %v, want 0444", info.Mode().Perm())
				}
			},
		},
		{
			name: "config with default target",
			config: types.ConfigRef{
				ID:   "config-2",
				Name: "default_config",
				Data: []byte("default: config\n"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, config types.ConfigRef) {
				expectedPath := filepath.Join(mountDir, "/config", config.Name)
				data, err := os.ReadFile(expectedPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
					return
				}
				if string(data) != string(config.Data) {
					t.Errorf("Config data = %q, want %q", data, config.Data)
				}
			},
		},
		{
			name: "config with nested directories",
			config: types.ConfigRef{
				ID:     "config-3",
				Name:   "nested_config",
				Target: "/config/app/production/database.conf",
				Data:   []byte("db.host=localhost\n"),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, config types.ConfigRef) {
				fullPath := filepath.Join(mountDir, config.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
					return
				}
				if string(data) != string(config.Data) {
					t.Errorf("Config data = %q, want %q", data, config.Data)
				}
			},
		},
		{
			name: "config with JSON data",
			config: types.ConfigRef{
				ID:     "config-4",
				Name:   "json_config",
				Target: "/config/app/settings.json",
				Data:   []byte(`{"app": {"name": "test", "port": 8080}}`),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, config types.ConfigRef) {
				fullPath := filepath.Join(mountDir, config.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
					return
				}
				if string(data) != string(config.Data) {
					t.Errorf("Config data = %q, want %q", data, config.Data)
				}
			},
		},
		{
			name: "config with empty data",
			config: types.ConfigRef{
				ID:     "config-5",
				Name:   "empty_config",
				Target: "/config/empty",
				Data:   []byte(""),
			},
			setupMount: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: false,
			verify: func(t *testing.T, mountDir string, config types.ConfigRef) {
				fullPath := filepath.Join(mountDir, config.Target)
				data, err := os.ReadFile(fullPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
					return
				}
				if len(data) != 0 {
					t.Errorf("Config data length = %d, want 0", len(data))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mountDir := tt.setupMount(t)
			sm := NewSecretManager("", "")

			err := sm.injectConfig(mountDir, tt.config)

			if (err != nil) != tt.wantErr {
				t.Errorf("injectConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.verify != nil {
				tt.verify(t, mountDir, tt.config)
			}
		})
	}
}

// TestMountRootfs tests the mountRootfs method (unexported).
func TestMountRootfs(t *testing.T) {
	// Note: This test requires root privileges to actually mount filesystems.
	// We'll test the error cases and use a marker for mocked rootfs.

	t.Run("invalid rootfs path", func(t *testing.T) {
		sm := NewSecretManager("", "")
		_, err := sm.mountRootfs("/nonexistent/path/to/rootfs.img")

		if err == nil {
			t.Error("Expected error for nonexistent rootfs path, got nil")
		}
	})

	t.Run("empty rootfs path", func(t *testing.T) {
		sm := NewSecretManager("", "")
		_, err := sm.mountRootfs("")

		if err == nil {
			t.Error("Expected error for empty rootfs path, got nil")
		}
	})
}

// TestUnmountRootfs tests the unmountRootfs method (unexported).
func TestUnmountRootfs(t *testing.T) {
	t.Run("unmount non-existent directory", func(t *testing.T) {
		sm := NewSecretManager("", "")
		// This should not panic, just log a warning
		sm.unmountRootfs("/nonexistent/mount/point")
	})

	t.Run("unmount empty path", func(t *testing.T) {
		sm := NewSecretManager("", "")
		// This should not panic
		sm.unmountRootfs("")
	})

	t.Run("unmount temp directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		sm := NewSecretManager("", "")
		// This should not panic (directory exists but is not mounted)
		sm.unmountRootfs(tmpDir)
	})
}

// TestRunCommand tests the runCommand helper function.
func TestRunCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		args      []string
		wantErr   bool
		verifyOut func(t *testing.T, output string)
	}{
		{
			name:    "echo command",
			command: "echo",
			args:    []string{"hello", "world"},
			wantErr: false,
			verifyOut: func(t *testing.T, output string) {
				if output != "hello world\n" {
					t.Errorf("Output = %q, want %q", output, "hello world\n")
				}
			},
		},
		{
			name:    "true command",
			command: "true",
			args:    []string{},
			wantErr: false,
			verifyOut: func(t *testing.T, output string) {
				if output != "" {
					t.Errorf("Output = %q, want empty", output)
				}
			},
		},
		{
			name:    "false command",
			command: "false",
			args:    []string{},
			wantErr: true,
			verifyOut: func(t *testing.T, output string) {
				// false exits with non-zero status but no output
			},
		},
		{
			name:    "nonexistent command",
			command: "nonexistent-command-xyz",
			args:    []string{},
			wantErr: true,
			verifyOut: func(t *testing.T, output string) {
				// Some systems may not provide error output for nonexistent commands
				// Just verify that an error was returned (checked by wantErr)
			},
		},
		{
			name:    "cat with stdin redirect simulation",
			command: "printf",
			args:    []string{"test"},
			wantErr: false,
			verifyOut: func(t *testing.T, output string) {
				if output != "test" {
					t.Errorf("Output = %q, want %q", output, "test")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := runCommand(tt.command, tt.args...)

			if (err != nil) != tt.wantErr {
				t.Errorf("runCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.verifyOut != nil {
				tt.verifyOut(t, output)
			}
		})
	}
}

// TestConcurrentSecretInjection tests concurrent secret injection safety.
func TestConcurrentSecretInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	mountDir := t.TempDir()
	sm := NewSecretManager("", "")

	// Create multiple secrets
	numSecrets := 10
	secrets := make([]types.SecretRef, numSecrets)
	for i := 0; i < numSecrets; i++ {
		secrets[i] = types.SecretRef{
			ID:     fmt.Sprintf("secret-%d", i),
			Name:   fmt.Sprintf("secret_%d", i),
			Target: fmt.Sprintf("/run/secrets/secret_%d", i),
			Data:   []byte(fmt.Sprintf("data-%d", i)),
		}
	}

	// Inject secrets concurrently
	errChan := make(chan error, numSecrets)
	for _, secret := range secrets {
		go func(s types.SecretRef) {
			errChan <- sm.injectSecret(mountDir, s)
		}(secret)
	}

	// Collect results
	for i := 0; i < numSecrets; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("Concurrent injectSecret failed: %v", err)
		}
	}

	// Verify all secrets were written
	for _, secret := range secrets {
		fullPath := filepath.Join(mountDir, secret.Target)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("Failed to read secret file %s: %v", fullPath, err)
			continue
		}
		if string(data) != string(secret.Data) {
			t.Errorf("Secret data mismatch at %s: got %q, want %q", fullPath, data, secret.Data)
		}
	}
}

// TestConcurrentConfigInjection tests concurrent config injection safety.
func TestConcurrentConfigInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	mountDir := t.TempDir()
	sm := NewSecretManager("", "")

	// Create multiple configs
	numConfigs := 10
	configs := make([]types.ConfigRef, numConfigs)
	for i := 0; i < numConfigs; i++ {
		configs[i] = types.ConfigRef{
			ID:     fmt.Sprintf("config-%d", i),
			Name:   fmt.Sprintf("config_%d", i),
			Target: fmt.Sprintf("/config/config_%d.yaml", i),
			Data:   []byte(fmt.Sprintf("key: value%d\n", i)),
		}
	}

	// Inject configs concurrently
	errChan := make(chan error, numConfigs)
	for _, config := range configs {
		go func(c types.ConfigRef) {
			errChan <- sm.injectConfig(mountDir, c)
		}(config)
	}

	// Collect results
	for i := 0; i < numConfigs; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("Concurrent injectConfig failed: %v", err)
		}
	}

	// Verify all configs were written
	for _, config := range configs {
		fullPath := filepath.Join(mountDir, config.Target)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Errorf("Failed to read config file %s: %v", fullPath, err)
			continue
		}
		if string(data) != string(config.Data) {
			t.Errorf("Config data mismatch at %s: got %q, want %q", fullPath, data, config.Data)
		}
	}
}

// Helper functions for testing

// containsString checks if a string contains a substring.
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
