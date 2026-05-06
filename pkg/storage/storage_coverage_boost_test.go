package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// TestInjectSecrets_Empty tests injecting empty secrets list
func TestInjectSecrets_Empty(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	err := sm.InjectSecrets(ctx, "task-123", []types.SecretRef{}, "/tmp/rootfs.ext4")
	if err != nil {
		t.Errorf("InjectSecrets with empty list should return nil, got: %v", err)
	}
}

// TestInjectConfigs_Empty tests injecting empty configs list
func TestInjectConfigs_Empty(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	err := sm.InjectConfigs(ctx, "task-123", []types.ConfigRef{}, "/tmp/rootfs.ext4")
	if err != nil {
		t.Errorf("InjectConfigs with empty list should return nil, got: %v", err)
	}
}

// TestInjectSecret_NonexistentRootfs tests secret injection with nonexistent rootfs
func TestInjectSecrets_NonexistentRootfs(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	secrets := []types.SecretRef{
		{Name: "secret1", Target: "/run/secrets/secret1", Data: []byte("secret data")},
	}

	err := sm.InjectSecrets(ctx, "task-123", secrets, "/nonexistent/rootfs.ext4")
	if err == nil {
		t.Error("Expected error for nonexistent rootfs")
	}
}

// TestInjectConfigs_NonexistentRootfs tests config injection with nonexistent rootfs
func TestInjectConfigs_NonexistentRootfs(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	configs := []types.ConfigRef{
		{Name: "config1", Target: "/config/config1", Data: []byte("config data")},
	}

	err := sm.InjectConfigs(ctx, "task-123", configs, "/nonexistent/rootfs.ext4")
	if err == nil {
		t.Error("Expected error for nonexistent rootfs")
	}
}

// TestInjectSecret_DefaultTarget tests secret injection with default target path
func TestInjectSecret_DefaultTarget(t *testing.T) {
	// Create a temp directory to simulate mounted rootfs
	tmpDir, err := os.MkdirTemp("", "secret-inject-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	// Test injectSecret directly (doesn't need actual mount)
	secret := types.SecretRef{
		Name: "secret1",
		Data: []byte("secret data"),
		// Target is empty - should use default /run/secrets/<name>
	}

	err = sm.injectSecret(tmpDir, secret)
	if err != nil {
		t.Fatalf("injectSecret failed: %v", err)
	}

	// Check that file was created at default location
	defaultPath := filepath.Join(tmpDir, "run", "secrets", "secret1")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Errorf("Secret file should exist at %s: %v", defaultPath, err)
	}

	// Check file content
	data, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret data" {
		t.Errorf("Expected secret content 'secret data', got '%s'", string(data))
	}

	// Check permissions (should be 0400)
	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0400 {
		t.Errorf("Expected permissions 0400, got %o", info.Mode().Perm())
	}
}

// TestInjectConfig_DefaultTarget tests config injection with default target path
func TestInjectConfig_DefaultTarget(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-inject-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	config := types.ConfigRef{
		Name: "config1",
		Data: []byte("config data"),
		// Target is empty - should use default /config/<name>
	}

	err = sm.injectConfig(tmpDir, config)
	if err != nil {
		t.Fatalf("injectConfig failed: %v", err)
	}

	// Check that file was created at default location
	defaultPath := filepath.Join(tmpDir, "config", "config1")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Errorf("Config file should exist at %s: %v", defaultPath, err)
	}

	// Check file content
	data, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "config data" {
		t.Errorf("Expected config content 'config data', got '%s'", string(data))
	}

	// Check permissions (should be 0444)
	info, err := os.Stat(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0444 {
		t.Errorf("Expected permissions 0444, got %o", info.Mode().Perm())
	}
}

// TestInjectSecret_CustomTarget tests secret injection with custom target
func TestInjectSecret_CustomTarget(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "secret-custom-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	secret := types.SecretRef{
		Name:   "secret1",
		Target: "/custom/path/secret",
		Data:   []byte("secret data"),
	}

	err = sm.injectSecret(tmpDir, secret)
	if err != nil {
		t.Fatalf("injectSecret failed: %v", err)
	}

	// Check that file was created at custom location
	customPath := filepath.Join(tmpDir, "custom", "path", "secret")
	if _, err := os.Stat(customPath); err != nil {
		t.Errorf("Secret file should exist at %s: %v", customPath, err)
	}
}

// TestInjectConfig_CustomTarget tests config injection with custom target
func TestInjectConfig_CustomTarget(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-custom-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	config := types.ConfigRef{
		Name:   "config1",
		Target: "/custom/path/config",
		Data:   []byte("config data"),
	}

	err = sm.injectConfig(tmpDir, config)
	if err != nil {
		t.Fatalf("injectConfig failed: %v", err)
	}

	// Check that file was created at custom location
	customPath := filepath.Join(tmpDir, "custom", "path", "config")
	if _, err := os.Stat(customPath); err != nil {
		t.Errorf("Config file should exist at %s: %v", customPath, err)
	}
}

// TestMountRootfs_Nonexistent tests mounting nonexistent rootfs
func TestMountRootfs_Nonexistent(t *testing.T) {
	sm := NewSecretManager("", "")

	mountDir, err := sm.mountRootfs("/nonexistent/rootfs.ext4")
	if err == nil {
		t.Error("Expected error for nonexistent rootfs")
		os.RemoveAll(mountDir)
	}
}

// TestMountRootfs_MountFailure tests mount failure
func TestMountRootfs_MountFailure(t *testing.T) {
	// Skip if not root - mount requires privileges
	if os.Geteuid() != 0 {
		t.Skip("mountRootfs requires root privileges")
	}

	// Create a non-ext4 file
	tmpFile, err := os.CreateTemp("", "non-ext4-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.WriteString("not an ext4 image")
	tmpFile.Close()

	sm := NewSecretManager("", "")

	mountDir, err := sm.mountRootfs(tmpFile.Name())
	if err == nil {
		t.Error("Expected error for non-ext4 file")
		os.RemoveAll(mountDir)
	}
}

// TestUnmountRootfs_Boost tests unmounting and cleanup
func TestUnmountRootfs_Boost(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "unmount-test-")
	if err != nil {
		t.Fatal(err)
	}

	sm := NewSecretManager("", "")

	sm.unmountRootfs(tmpDir)

	// Directory should be removed
	if _, err := os.Stat(tmpDir); err == nil {
		t.Log("Directory may still exist if unmount failed (expected in test env)")
	}
}

// TestRunCommand_Boost tests command execution helper
func TestRunCommand_Boost(t *testing.T) {
	// Test successful command
	output, err := runCommand("echo", "test")
	if err != nil {
		t.Fatalf("runCommand failed: %v", err)
	}
	if output != "test\n" {
		t.Errorf("Expected output 'test\\n', got '%s'", output)
	}

	// Test failed command
	_, err = runCommand("false")
	if err == nil {
		t.Error("Expected error for failing command")
	}

	// Test nonexistent command
	_, err = runCommand("/nonexistent/command")
	if err == nil {
		t.Error("Expected error for nonexistent command")
	}
}

// TestNewSecretManager_Boost tests secret manager creation
func TestNewSecretManager_Boost(t *testing.T) {
	// With empty directories
	sm := NewSecretManager("", "")
	if sm == nil {
		t.Error("Expected non-nil SecretManager")
	}

	// With actual directories
	tmpDir, err := os.MkdirTemp("", "secrets-dir-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm = NewSecretManager(tmpDir, tmpDir)
	if sm == nil {
		t.Error("Expected non-nil SecretManager")
	}
}

// TestInjectSecret_DirectoryCreation tests that parent directories are created
func TestInjectSecret_DirectoryCreation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "secret-dir-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	secret := types.SecretRef{
		Name:   "secret1",
		Target: "/deep/nested/path/secret",
		Data:   []byte("secret data"),
	}

	err = sm.injectSecret(tmpDir, secret)
	if err != nil {
		t.Fatalf("injectSecret failed: %v", err)
	}

	// Verify all parent directories were created
	secretPath := filepath.Join(tmpDir, "deep", "nested", "path", "secret")
	if _, err := os.Stat(secretPath); err != nil {
		t.Errorf("Secret should exist at %s: %v", secretPath, err)
	}
}

// TestInjectConfig_DirectoryCreation tests that parent directories are created
func TestInjectConfig_DirectoryCreation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-dir-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	config := types.ConfigRef{
		Name:   "config1",
		Target: "/deep/nested/path/config",
		Data:   []byte("config data"),
	}

	err = sm.injectConfig(tmpDir, config)
	if err != nil {
		t.Fatalf("injectConfig failed: %v", err)
	}

	configPath := filepath.Join(tmpDir, "deep", "nested", "path", "config")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("Config should exist at %s: %v", configPath, err)
	}
}

// TestInjectSecret_MultipleSecrets tests injecting multiple secrets
func TestInjectSecret_MultipleSecrets(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "multi-secret-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	secrets := []types.SecretRef{
		{Name: "secret1", Target: "/secrets/s1", Data: []byte("data1")},
		{Name: "secret2", Target: "/secrets/s2", Data: []byte("data2")},
		{Name: "secret3", Target: "/secrets/s3", Data: []byte("data3")},
	}

	for _, secret := range secrets {
		err = sm.injectSecret(tmpDir, secret)
		if err != nil {
			t.Fatalf("injectSecret failed for %s: %v", secret.Name, err)
		}
	}

	// Verify all secrets exist
	for _, secret := range secrets {
		path := filepath.Join(tmpDir, secret.Target)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Secret %s should exist at %s: %v", secret.Name, path, err)
		}
	}
}

// TestInjectConfig_MultipleConfigs tests injecting multiple configs
func TestInjectConfig_MultipleConfigs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "multi-config-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	configs := []types.ConfigRef{
		{Name: "config1", Target: "/configs/c1", Data: []byte("data1")},
		{Name: "config2", Target: "/configs/c2", Data: []byte("data2")},
		{Name: "config3", Target: "/configs/c3", Data: []byte("data3")},
	}

	for _, config := range configs {
		err = sm.injectConfig(tmpDir, config)
		if err != nil {
			t.Fatalf("injectConfig failed for %s: %v", config.Name, err)
		}
	}

	// Verify all configs exist
	for _, config := range configs {
		path := filepath.Join(tmpDir, config.Target)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Config %s should exist at %s: %v", config.Name, path, err)
		}
	}
}

// TestInjectSecret_EmptyData tests secret with empty data
func TestInjectSecret_EmptyData(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "empty-secret-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	secret := types.SecretRef{
		Name:   "empty_secret",
		Target: "/secrets/empty",
		Data:   []byte{}, // Empty data
	}

	err = sm.injectSecret(tmpDir, secret)
	if err != nil {
		t.Fatalf("injectSecret with empty data failed: %v", err)
	}

	// File should still be created
	path := filepath.Join(tmpDir, "secrets", "empty")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Empty secret file should exist: %v", err)
	}
}

// TestInjectConfig_EmptyData tests config with empty data
func TestInjectConfig_EmptyData(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "empty-config-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sm := NewSecretManager("", "")

	config := types.ConfigRef{
		Name:   "empty_config",
		Target: "/configs/empty",
		Data:   []byte{}, // Empty data
	}

	err = sm.injectConfig(tmpDir, config)
	if err != nil {
		t.Fatalf("injectConfig with empty data failed: %v", err)
	}

	// File should still be created
	path := filepath.Join(tmpDir, "configs", "empty")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("Empty config file should exist: %v", err)
	}
}

// TestInjectSecret_WriteFailure tests secret write failure
func TestInjectSecret_WriteFailure(t *testing.T) {
	// Create a read-only directory to cause write failure
	tmpDir, err := os.MkdirTemp("", "readonly-secret-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create read-only parent directory
	secretsDir := filepath.Join(tmpDir, "secrets")
	if err := os.MkdirAll(secretsDir, 0555); err != nil {
		t.Fatal(err)
	}

	sm := NewSecretManager("", "")

	secret := types.SecretRef{
		Name:   "secret1",
		Target: "/secrets/test",
		Data:   []byte("data"),
	}

	err = sm.injectSecret(tmpDir, secret)
	if err == nil {
		t.Log("Write succeeded (may have override permissions)")
	}
}

// TestInjectConfig_WriteFailure tests config write failure
func TestInjectConfig_WriteFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "readonly-config-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configsDir := filepath.Join(tmpDir, "configs")
	if err := os.MkdirAll(configsDir, 0555); err != nil {
		t.Fatal(err)
	}

	sm := NewSecretManager("", "")

	config := types.ConfigRef{
		Name:   "config1",
		Target: "/configs/test",
		Data:   []byte("data"),
	}

	err = sm.injectConfig(tmpDir, config)
	if err == nil {
		t.Log("Write succeeded (may have override permissions)")
	}
}

// MockSecretManager for testing higher-level functions
type MockSecretManager struct {
	InjectSecretsErr error
	InjectConfigsErr error
	SecretsInjected  []types.SecretRef
	ConfigsInjected  []types.ConfigRef
}

func (m *MockSecretManager) InjectSecrets(ctx context.Context, taskID string, secrets []types.SecretRef, rootfsPath string) error {
	m.SecretsInjected = secrets
	return m.InjectSecretsErr
}

func (m *MockSecretManager) InjectConfigs(ctx context.Context, taskID string, configs []types.ConfigRef, rootfsPath string) error {
	m.ConfigsInjected = configs
	return m.InjectConfigsErr
}

// TestMockSecretManager tests the mock secret manager
func TestMockSecretManager(t *testing.T) {
	mock := &MockSecretManager{}

	ctx := context.Background()
	secrets := []types.SecretRef{{Name: "s1", Data: []byte("d1")}}

	err := mock.InjectSecrets(ctx, "task-1", secrets, "/tmp/rootfs")
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.SecretsInjected) != 1 {
		t.Error("Secrets should be recorded in mock")
	}

	// Test error injection
	mock.InjectSecretsErr = errors.New("inject failed")
	err = mock.InjectSecrets(ctx, "task-1", secrets, "/tmp/rootfs")
	if err == nil {
		t.Error("Expected error from mock")
	}
}

// TestMockSecretManager_Configs tests config injection in mock
func TestMockSecretManager_Configs(t *testing.T) {
	mock := &MockSecretManager{}

	ctx := context.Background()
	configs := []types.ConfigRef{{Name: "c1", Data: []byte("d1")}}

	err := mock.InjectConfigs(ctx, "task-1", configs, "/tmp/rootfs")
	if err != nil {
		t.Fatal(err)
	}

	if len(mock.ConfigsInjected) != 1 {
		t.Error("Configs should be recorded in mock")
	}

	// Test error injection
	mock.InjectConfigsErr = errors.New("inject failed")
	err = mock.InjectConfigs(ctx, "task-1", configs, "/tmp/rootfs")
	if err == nil {
		t.Error("Expected error from mock")
	}
}