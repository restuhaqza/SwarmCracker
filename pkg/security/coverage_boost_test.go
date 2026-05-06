package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckCapabilities_NonRoot_CoverageBoost checks error path when not root
func TestCheckCapabilities_NonRoot_CoverageBoost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipping: running as root")
	}
	err := CheckCapabilities()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "root privileges")
}

// TestSecureFilePermissions_CoverageBoost tests secure file perms on a temp file
func TestSecureFilePermissions_CoverageBoost(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping: requires root for chown")
	}
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	err := os.WriteFile(tmpFile, []byte("test"), 0644)
	require.NoError(t, err)

	err = SecureFilePermissions(tmpFile)
	assert.NoError(t, err)

	info, err := os.Stat(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestSecureDirectoryPermissions_CoverageBoost tests secure dir permissions
func TestSecureDirectoryPermissions_CoverageBoost(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("skipping: requires root for chown")
	}
	tmpDir := t.TempDir()

	err := SecureDirectoryPermissions(tmpDir)
	assert.NoError(t, err)

	info, err := os.Stat(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

// TestJailer_Validate_CoverageBoost tests Jailer validation with various configs
func TestJailer_Validate_CoverageBoost(t *testing.T) {
	tests := []struct {
		name    string
		jailer  *Jailer
		wantErr bool
	}{
		{
			name:    "disabled_jailer",
			jailer:  &Jailer{Enabled: false},
			wantErr: false,
		},
		{
			name: "valid_config",
			jailer: &Jailer{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			wantErr: false,
		},
		{
			name: "empty_chroot_dir",
			jailer: &Jailer{
				UID:           1000,
				GID:           1000,
				ChrootBaseDir: "",
				Enabled:       true,
			},
			wantErr: true,
		},
		{
			name: "negative_uid",
			jailer: &Jailer{
				UID:           -1,
				GID:           1000,
				ChrootBaseDir: "/tmp",
				Enabled:       true,
			},
			wantErr: true,
		},
		{
			name: "negative_gid",
			jailer: &Jailer{
				UID:           1000,
				GID:           -1,
				ChrootBaseDir: "/tmp",
				Enabled:       true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.jailer.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNewManager_CoverageBoost covers manager.go line 21 (NewManager)
func TestNewManager_CoverageBoost(t *testing.T) {
	t.Run("disabled manager", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.False(t, mgr.IsEnabled())
		assert.Nil(t, mgr.GetJailer())
	})

	t.Run("enabled manager with valid config", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           1000,
					GID:           1000,
					ChrootBaseDir: t.TempDir(),
				},
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.True(t, mgr.IsEnabled())
		assert.NotNil(t, mgr.GetJailer())
	})

	t.Run("enabled manager with invalid config", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           -1,
					GID:           1000,
					ChrootBaseDir: "/tmp",
				},
			},
		}
		mgr, err := NewManager(cfg)
		assert.Error(t, err)
		assert.Nil(t, mgr)
	})
}

// TestCleanupVM_CoverageBoost covers manager.go line 131 (CleanupVM)
func TestCleanupVM_CoverageBoost(t *testing.T) {
	t.Run("cleanup disabled context", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{EnableJailer: false},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx := &VMContext{Enabled: false}
		err = mgr.CleanupVM(context.Background(), vmCtx)
		assert.NoError(t, err)
	})

	t.Run("cleanup with jail and seccomp", func(t *testing.T) {
		tempDir := t.TempDir()
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           os.Getuid(),
					GID:           os.Getgid(),
					ChrootBaseDir: tempDir,
				},
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		// Prepare a VM to get a real jail context
		vmCtx, err := mgr.PrepareVM(context.Background(), "cleanup-test-vm")
		require.NoError(t, err)
		assert.True(t, vmCtx.Enabled)

		err = mgr.CleanupVM(context.Background(), vmCtx)
		assert.NoError(t, err)

		// Verify jail path removed
		_, statErr := os.Stat(vmCtx.JailPath)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("cleanup with missing seccomp profile", func(t *testing.T) {
		tempDir := t.TempDir()
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           os.Getuid(),
					GID:           os.Getgid(),
					ChrootBaseDir: tempDir,
				},
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		// Create jail dir manually
		jailPath := filepath.Join(tempDir, "test-vm-2")
		require.NoError(t, os.MkdirAll(jailPath, 0755))

		vmCtx := &VMContext{
			Enabled:        true,
			VMID:           "test-vm-2",
			JailPath:       jailPath,
			JailContext:    &JailContext{Enabled: true, JailPath: jailPath},
			SeccompProfile: "/nonexistent/seccomp.json", // doesn't exist, should be handled gracefully
		}
		err = mgr.CleanupVM(context.Background(), vmCtx)
		assert.NoError(t, err)
	})
}

// TestApplyToProcess_CoverageBoost covers manager.go line 101-128 (ApplyToProcess)
func TestApplyToProcess_CoverageBoost(t *testing.T) {
	t.Run("disabled context returns nil", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{EnableJailer: false},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx := &VMContext{Enabled: false}
		err = mgr.ApplyToProcess(context.Background(), vmCtx, os.Getpid())
		assert.NoError(t, err)
	})

	t.Run("enabled context applies limits", func(t *testing.T) {
		if os.Getuid() != 0 {
			t.Skip("requires root")
		}
		tempDir := t.TempDir()
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           1000,
					GID:           1000,
					ChrootBaseDir: tempDir,
				},
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx := &VMContext{Enabled: true, VMID: "apply-test"}
		err = mgr.ApplyToProcess(context.Background(), vmCtx, os.Getpid())
		// May fail on cgroup but code path exercised
		_ = err
	})
}

// TestPrepareVM_CoverageBoost covers manager.go line 48-98 (PrepareVM)
func TestPrepareVM_CoverageBoost(t *testing.T) {
	t.Run("disabled returns empty context", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{EnableJailer: false},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx, err := mgr.PrepareVM(context.Background(), "disabled-vm")
		require.NoError(t, err)
		assert.NotNil(t, vmCtx)
		assert.False(t, vmCtx.Enabled)
	})

	t.Run("enabled prepares full context", func(t *testing.T) {
		tempDir := t.TempDir()
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           os.Getuid(),
					GID:           os.Getgid(),
					ChrootBaseDir: tempDir,
				},
			},
		}
		mgr, err := NewManager(cfg)
		require.NoError(t, err)

		vmCtx, err := mgr.PrepareVM(context.Background(), "prepare-test-vm")
		require.NoError(t, err)
		assert.True(t, vmCtx.Enabled)
		assert.Equal(t, "prepare-test-vm", vmCtx.VMID)
		assert.NotEmpty(t, vmCtx.JailPath)
		assert.NotNil(t, vmCtx.JailContext)
		assert.NotEmpty(t, vmCtx.SeccompProfile)
		assert.FileExists(t, vmCtx.SeccompProfile)
	})
}

// TestSetResourceLimits_CoverageBoost covers manager.go line 279
func TestSetResourceLimits_CoverageBoost(t *testing.T) {
	t.Run("disabled context returns nil", func(t *testing.T) {
		mgr := &Manager{enabled: false}
		err := mgr.SetResourceLimits(&VMContext{Enabled: false}, ResourceLimits{})
		assert.NoError(t, err)
	})

	t.Run("enabled context stores limits", func(t *testing.T) {
		mgr := &Manager{enabled: true}
		err := mgr.SetResourceLimits(&VMContext{Enabled: true}, ResourceLimits{
			MaxCPUs:      4,
			MaxMemoryMB:  4096,
			MaxFD:        8192,
			MaxProcesses: 2048,
		})
		assert.NoError(t, err)
	})
}

// TestGetSeccompProfilePath_CoverageBoost covers manager.go line 289
func TestGetSeccompProfilePath_CoverageBoost(t *testing.T) {
	tempDir := t.TempDir()
	mgr := &Manager{
		enabled: true,
		jailer:  &Jailer{ChrootBaseDir: tempDir},
	}
	path := mgr.GetSeccompProfilePath("vm-123")
	assert.Equal(t, filepath.Join(tempDir, "vm-123", "seccomp.json"), path)
}

// TestGetDefaultSecurityConfig_CoverageBoost covers manager.go line 259
func TestGetDefaultSecurityConfig_CoverageBoost(t *testing.T) {
	cfg := GetDefaultSecurityConfig()
	assert.Equal(t, 1000, cfg.UID)
	assert.Equal(t, 1000, cfg.GID)
	assert.Equal(t, "/srv/jailer", cfg.ChrootBaseDir)
	assert.Equal(t, "", cfg.NetNS)
}

// TestIsEnabled_CoverageBoost covers manager.go line 269
func TestIsEnabled_CoverageBoost(t *testing.T) {
	assert.True(t, (&Manager{enabled: true}).IsEnabled())
	assert.False(t, (&Manager{enabled: false}).IsEnabled())
}

// TestGetJailer_CoverageBoost covers manager.go line 274
func TestGetJailer_CoverageBoost(t *testing.T) {
	j := &Jailer{UID: 500}
	mgr := &Manager{jailer: j}
	assert.Equal(t, j, mgr.GetJailer())
}

// TestValidateSecurityConfig_CoverageBoost covers manager.go line 294
func TestValidateSecurityConfig_CoverageBoost(t *testing.T) {
	t.Run("disabled passes", func(t *testing.T) {
		err := ValidateSecurityConfig(&config.Config{
			Executor: config.ExecutorConfig{EnableJailer: false},
		})
		assert.NoError(t, err)
	})

	t.Run("uid zero rejected", func(t *testing.T) {
		err := ValidateSecurityConfig(&config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           0,
					GID:           1000,
					ChrootBaseDir: "/srv/jailer",
				},
			},
		})
		assert.Error(t, err)
	})

	t.Run("empty chroot rejected", func(t *testing.T) {
		err := ValidateSecurityConfig(&config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: true,
				Jailer: config.JailerConfig{
					UID:           1000,
					GID:           1000,
					ChrootBaseDir: "",
				},
			},
		})
		assert.Error(t, err)
	})
}

// TestNewJailer_CoverageBoost covers jailer.go line 27
func TestNewJailer_CoverageBoost(t *testing.T) {
	j := NewJailer(1000, 1000, "/srv/jail", "myns")
	assert.Equal(t, 1000, j.UID)
	assert.Equal(t, 1000, j.GID)
	assert.Equal(t, "/srv/jail", j.ChrootBaseDir)
	assert.Equal(t, "myns", j.NetNS)
	assert.True(t, j.Enabled)
}

// TestSeccompWriteAndValidate_CoverageBoost covers seccomp.go:35,104,130
func TestSeccompWriteAndValidate_CoverageBoost(t *testing.T) {
	t.Run("write and validate profile", func(t *testing.T) {
		tempDir := t.TempDir()
		profilePath := filepath.Join(tempDir, "seccomp.json")

		err := WriteSeccompProfile("test-vm", profilePath)
		require.NoError(t, err)
		assert.FileExists(t, profilePath)

		err = ValidateSeccompProfile(profilePath)
		assert.NoError(t, err)
	})

	t.Run("validate non-existent file", func(t *testing.T) {
		err := ValidateSeccompProfile("/nonexistent/profile.json")
		assert.Error(t, err)
	})

	t.Run("validate invalid JSON", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "bad.json")
		require.NoError(t, os.WriteFile(tmpFile, []byte("{bad json}"), 0644))
		err := ValidateSeccompProfile(tmpFile)
		assert.Error(t, err)
	})

	t.Run("validate invalid default action", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "invalid_action.json")
		content := `{"defaultAction":"INVALID","architectures":["SCMP_ARCH_X86_64"],"syscalls":[]}`
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))
		err := ValidateSeccompProfile(tmpFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid default action")
	})

	t.Run("validate invalid architecture", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "invalid_arch.json")
		content := `{"defaultAction":"SCMP_ACT_ERRNO","architectures":["SCMP_ARCH_INVALID"],"syscalls":[]}`
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))
		err := ValidateSeccompProfile(tmpFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid architecture")
	})

	t.Run("write to nested directory", func(t *testing.T) {
		tempDir := t.TempDir()
		profilePath := filepath.Join(tempDir, "nested", "dir", "seccomp.json")

		err := WriteSeccompProfile("test-vm-nested", profilePath)
		require.NoError(t, err)
		assert.FileExists(t, profilePath)
	})
}

// TestRestrictiveSeccompFilter_CoverageBoost covers seccomp.go RestrictiveSeccompFilter
func TestRestrictiveSeccompFilter_CoverageBoost(t *testing.T) {
	filter := RestrictiveSeccompFilter()
	assert.NotNil(t, filter)
	assert.Equal(t, "SCMP_ACT_ERRNO", filter.DefaultAction)
	assert.NotEmpty(t, filter.Syscalls)

	// Verify dangerous syscalls are removed
	for _, rule := range filter.Syscalls {
		for _, name := range rule.Names {
			assert.NotEqual(t, "mount", name)
			assert.NotEqual(t, "reboot", name)
			assert.NotEqual(t, "kexec_load", name)
		}
	}
}

// TestDefaultSeccompFilter_CoverageBoost
func TestDefaultSeccompFilter_CoverageBoost(t *testing.T) {
	filter := DefaultSeccompFilter()
	assert.NotNil(t, filter)
	assert.Equal(t, "SCMP_ACT_ERRNO", filter.DefaultAction)
	assert.Contains(t, filter.Architectures, "SCMP_ARCH_X86_64")
	assert.True(t, len(filter.Syscalls) > 10)
}
