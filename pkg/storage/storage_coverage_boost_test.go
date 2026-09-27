package storage

import (
	"context"
	"errors"
	"os"
	"strings"
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

	// With CVR-1.6 debugfs write, nonexistent ext4 should fail with debugfs error
	err := sm.InjectSecrets(ctx, "task-123", secrets, "/nonexistent/rootfs.ext4")
	if err == nil {
		t.Error("Expected error for nonexistent rootfs")
	}
	// Error should contain "debugfs" (not "mount")
	if err != nil && !strings.Contains(err.Error(), "debugfs") {
		t.Errorf("Error should contain 'debugfs', got: %v", err)
	}
}

// TestInjectConfigs_NonexistentRootfs tests config injection with nonexistent rootfs
func TestInjectConfigs_NonexistentRootfs(t *testing.T) {
	sm := NewSecretManager("", "")

	ctx := context.Background()
	configs := []types.ConfigRef{
		{Name: "config1", Target: "/config/config1", Data: []byte("config data")},
	}

	// With CVR-1.6 debugfs write, nonexistent ext4 should fail
	err := sm.InjectConfigs(ctx, "task-123", configs, "/nonexistent/rootfs.ext4")
	if err == nil {
		t.Error("Expected error for nonexistent rootfs")
	}
}
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
