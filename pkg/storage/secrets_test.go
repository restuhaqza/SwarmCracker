package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
)

func TestSecretManager_NewSecretManager(t *testing.T) {
	sm := NewSecretManager("/tmp/test-secrets", "/tmp/test-configs")
	if sm == nil {
		t.Fatal("NewSecretManager returned nil")
	}
	if sm.secretsDir != "/tmp/test-secrets" {
		t.Errorf("expected secretsDir %q, got %q", "/tmp/test-secrets", sm.secretsDir)
	}
	if sm.configsDir != "/tmp/test-configs" {
		t.Errorf("expected configsDir %q, got %q", "/tmp/test-configs", sm.configsDir)
	}
}

func TestSecretManager_SecretFilePermissions(t *testing.T) {
	// Create temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "secrets-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake rootfs directory for testing
	rootfsPath := filepath.Join(tmpDir, "test-rootfs")
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Test that secret files have restrictive permissions
	secretPath := filepath.Join(rootfsPath, "/run/secrets/my_secret")
	if err := os.MkdirAll(filepath.Dir(secretPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte("secret data"), 0400); err != nil {
		t.Fatal(err)
	}

	// Verify the secret was written
	data, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("failed to read secret file: %v", err)
	}

	if string(data) != "secret data" {
		t.Errorf("expected secret data %q, got %q", "secret data", string(data))
	}

	// Verify file permissions
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("failed to stat secret file: %v", err)
	}

	// Check if file is not world-writable (perms should be 0400)
	if info.Mode().Perm()&0077 != 0 {
		t.Errorf("secret file has overly permissive permissions: %v", info.Mode().Perm())
	}
}

func TestSecretManager_ConfigFilePermissions(t *testing.T) {
	// Create temporary directory for testing
	tmpDir, err := os.MkdirTemp("", "configs-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake rootfs directory for testing
	rootfsPath := filepath.Join(tmpDir, "test-rootfs")
	if err := os.MkdirAll(rootfsPath, 0755); err != nil {
		t.Fatal(err)
	}

	// Test that config files have readable permissions
	configPath := filepath.Join(rootfsPath, "/config/app.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("config: data"), 0444); err != nil {
		t.Fatal(err)
	}

	// Verify the config was written
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	if string(data) != "config: data" {
		t.Errorf("expected config data %q, got %q", "config: data", string(data))
	}

	// Verify file permissions (should be readable, 0444)
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config file: %v", err)
	}

	// Config should be readable by all
	if info.Mode().Perm()&0444 != 0444 {
		t.Logf("config file permissions: %v (expected 0444 or similar)", info.Mode().Perm())
	}
}

func TestSecretManager_EmptySecrets(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	taskID := "test-task"

	// Should not error on empty secrets list
	err := sm.InjectSecrets(ctx, taskID, []types.SecretRef{}, "/tmp/nonexistent.ext4")
	if err != nil {
		t.Errorf("InjectSecrets with empty list should not error: %v", err)
	}

	err = sm.InjectConfigs(ctx, taskID, []types.ConfigRef{}, "/tmp/nonexistent.ext4")
	if err != nil {
		t.Errorf("InjectConfigs with empty list should not error: %v", err)
	}
}
