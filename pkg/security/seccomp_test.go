package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultSeccompFilter(t *testing.T) {
	filter := DefaultSeccompFilter()

	assert.NotNil(t, filter)
	assert.Equal(t, "SCMP_ACT_ERRNO", filter.DefaultAction)
	assert.NotEmpty(t, filter.Architectures)
	assert.NotEmpty(t, filter.Syscalls)

	// Verify architectures contain common ones
	found := false
	for _, arch := range filter.Architectures {
		if arch == "SCMP_ARCH_X86_64" || arch == "SCMP_ARCH_X86" {
			found = true
			break
		}
	}
	assert.True(t, found, "Should contain x86 architecture")

	// Verify some syscalls are allowed
	allowedSyscalls := []string{"read", "write", "open", "close", "socket"}
	syscallMap := make(map[string]bool)
	for _, rule := range filter.Syscalls {
		if rule.Action == "SCMP_ACT_ALLOW" {
			for _, name := range rule.Names {
				syscallMap[name] = true
			}
		}
	}

	for _, syscall := range allowedSyscalls {
		assert.True(t, syscallMap[syscall], "Should allow %s syscall", syscall)
	}
}

func TestRestrictiveSeccompFilter(t *testing.T) {
	filter := RestrictiveSeccompFilter()

	assert.NotNil(t, filter)
	assert.Equal(t, "SCMP_ACT_ERRNO", filter.DefaultAction)

	// Verify dangerous syscalls are blocked
	blockedSyscalls := []string{"mount", "umount2", "chroot", "init_module"}
	syscallMap := make(map[string]bool)
	for _, rule := range filter.Syscalls {
		if rule.Action == "SCMP_ACT_ALLOW" {
			for _, name := range rule.Names {
				syscallMap[name] = true
			}
		}
	}

	for _, syscall := range blockedSyscalls {
		assert.False(t, syscallMap[syscall], "Should block %s syscall", syscall)
	}
}

func TestWriteSeccompProfile(t *testing.T) {
	vmID := "test-vm-seccomp"
	profilePath := filepath.Join(t.TempDir(), "seccomp.json")

	err := WriteSeccompProfile(vmID, profilePath)
	assert.NoError(t, err)

	// Verify file exists
	assert.FileExists(t, profilePath)

	// Verify file is valid JSON
	data, err := os.ReadFile(profilePath)
	assert.NoError(t, err)

	var filter SeccompFilter
	err = json.Unmarshal(data, &filter)
	assert.NoError(t, err)
	assert.NotNil(t, filter)
}

func TestValidateSeccompProfile(t *testing.T) {
	// Create a valid profile
	vmID := "test-vm-validate"
	profilePath := filepath.Join(t.TempDir(), "seccomp.json")

	err := WriteSeccompProfile(vmID, profilePath)
	require.NoError(t, err)

	// Validate
	err = ValidateSeccompProfile(profilePath)
	assert.NoError(t, err)

	// Test invalid profile
	invalidPath := filepath.Join(t.TempDir(), "invalid.json")
	err = os.WriteFile(invalidPath, []byte("{invalid json"), 0644)
	require.NoError(t, err)

	err = ValidateSeccompProfile(invalidPath)
	assert.Error(t, err)
}

func TestValidateSeccompProfile_InvalidAction(t *testing.T) {
	// Create profile with invalid action
	filter := &SeccompFilter{
		DefaultAction: "INVALID_ACTION",
		Architectures: []string{"SCMP_ARCH_X86_64"},
		Syscalls:      []SyscallRule{},
	}

	data, err := json.Marshal(filter)
	require.NoError(t, err)

	profilePath := filepath.Join(t.TempDir(), "invalid-action.json")
	err = os.WriteFile(profilePath, data, 0644)
	require.NoError(t, err)

	err = ValidateSeccompProfile(profilePath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid default action")
}

func TestValidateSeccompProfile_InvalidArchitecture(t *testing.T) {
	// Create profile with invalid architecture
	filter := &SeccompFilter{
		DefaultAction: "SCMP_ACT_ALLOW",
		Architectures: []string{"INVALID_ARCH"},
		Syscalls:      []SyscallRule{},
	}

	data, err := json.Marshal(filter)
	require.NoError(t, err)

	profilePath := filepath.Join(t.TempDir(), "invalid-arch.json")
	err = os.WriteFile(profilePath, data, 0644)
	require.NoError(t, err)

	err = ValidateSeccompProfile(profilePath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid architecture")
}

func TestSeccompFilter_JSONRoundtrip(t *testing.T) {
	// Create filter, marshal, unmarshal
	original := DefaultSeccompFilter()

	data, err := json.Marshal(original)
	assert.NoError(t, err)

	var restored SeccompFilter
	err = json.Unmarshal(data, &restored)
	assert.NoError(t, err)

	// Verify key fields match
	assert.Equal(t, original.DefaultAction, restored.DefaultAction)
	assert.Equal(t, len(original.Architectures), len(restored.Architectures))
	assert.Equal(t, len(original.Syscalls), len(restored.Syscalls))
}
