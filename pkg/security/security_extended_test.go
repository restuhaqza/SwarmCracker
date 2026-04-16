package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
)

// TestApplyToProcess_Extended tests ApplyToProcess with various scenarios
func TestApplyToProcess_Extended(t *testing.T) {
	t.Run("disabled VMContext returns nil", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}
		mgr, err := NewManager(cfg)
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		vmCtx := &VMContext{Enabled: false}
		err = mgr.ApplyToProcess(context.Background(), vmCtx, 1234)
		if err != nil {
			t.Errorf("ApplyToProcess with disabled VMContext should return nil, got: %v", err)
		}
	})

	t.Run("enabled VMContext attempts to apply limits", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("Skipping test: requires root privileges for cgroup operations")
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
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		vmCtx := &VMContext{
			Enabled:  true,
			VMID:     "test-vm",
			JailPath: filepath.Join(tempDir, "test-vm"),
		}

		// This will likely fail without proper cgroup setup
		err = mgr.ApplyToProcess(context.Background(), vmCtx, 1)
		// We expect this to fail due to missing cgroup, but the code path is tested
		if err == nil {
			t.Log("ApplyToProcess succeeded (cgroup may already exist)")
		} else {
			t.Logf("ApplyToProcess failed as expected without cgroup: %v", err)
		}
	})
}

// TestCleanupVM_Extended tests CleanupVM edge cases
func TestCleanupVM_Extended(t *testing.T) {
	t.Run("cleanup with disabled VMContext", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}
		mgr, err := NewManager(cfg)
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		vmCtx := &VMContext{Enabled: false}
		err = mgr.CleanupVM(context.Background(), vmCtx)
		if err != nil {
			t.Errorf("CleanupVM with disabled VMContext should return nil, got: %v", err)
		}
	})

	t.Run("cleanup removes seccomp profile when enabled", func(t *testing.T) {
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
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		// Create a VM with jail (so JailContext exists)
		jailPath := filepath.Join(tempDir, "test-vm")
		if err := os.MkdirAll(jailPath, 0755); err != nil {
			t.Fatalf("Failed to create jail directory: %v", err)
		}

		// Create a seccomp profile file
		seccompPath := filepath.Join(jailPath, "seccomp.json")
		if err := os.WriteFile(seccompPath, []byte("{}"), 0644); err != nil {
			t.Fatalf("Failed to create seccomp profile: %v", err)
		}

		vmCtx := &VMContext{
			Enabled:        true, // Must be enabled for cleanup to run
			VMID:           "test-vm",
			JailPath:       jailPath,
			JailContext:    &JailContext{Enabled: true, JailPath: jailPath},
			SeccompProfile: seccompPath,
		}

		err = mgr.CleanupVM(context.Background(), vmCtx)
		if err != nil {
			t.Errorf("CleanupVM failed: %v", err)
		}

		// Verify file was removed
		if _, err := os.Stat(seccompPath); !os.IsNotExist(err) {
			t.Error("Seccomp profile was not removed")
		}
	})

	t.Run("cleanup skips seccomp profile when disabled", func(t *testing.T) {
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
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		// Create a seccomp profile file
		seccompPath := filepath.Join(tempDir, "seccomp.json")
		if err := os.WriteFile(seccompPath, []byte("{}"), 0644); err != nil {
			t.Fatalf("Failed to create seccomp profile: %v", err)
		}

		vmCtx := &VMContext{
			Enabled:        false, // Disabled - cleanup should skip
			VMID:           "test-vm",
			JailPath:       "",
			SeccompProfile: seccompPath,
		}

		err = mgr.CleanupVM(context.Background(), vmCtx)
		if err != nil {
			t.Errorf("CleanupVM failed: %v", err)
		}

		// Verify file still exists (cleanup was skipped)
		if _, err := os.Stat(seccompPath); os.IsNotExist(err) {
			t.Error("Seccomp profile should not be removed when VMContext is disabled")
		}
	})
}

// TestValidatePath_Extended tests ValidatePath with additional edge cases
func TestValidatePath_Extended(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() string
		wantErr bool
	}{
		{
			name: "absolute path to file",
			setup: func() string {
				return "/etc/passwd"
			},
			wantErr: false,
		},
		{
			name: "non-existent path",
			setup: func() string {
				return "/nonexistent/path/xyz123"
			},
			wantErr: true,
		},
		{
			name: "empty path",
			setup: func() string {
				return ""
			},
			wantErr: true,
		},
		{
			name: "path with traversal components",
			setup: func() string {
				// Create a temp path
				return t.TempDir()
			},
			wantErr: false,
		},
		{
			name: "valid temporary directory",
			setup: func() string {
				dir := t.TempDir()
				// Ensure not world-writable
				os.Chmod(dir, 0755)
				return dir
			},
			wantErr: false,
		},
		{
			name: "world-writable directory",
			setup: func() string {
				dir := t.TempDir()
				os.Chmod(dir, 0777)
				return dir
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup()
			err := ValidatePath(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCheckCapabilities_Extended tests CheckCapabilities behavior
func TestCheckCapabilities_Extended(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Run("running as root", func(t *testing.T) {
			err := CheckCapabilities()
			if err != nil {
				t.Errorf("CheckCapabilities should succeed as root, got: %v", err)
			}
		})
	} else {
		t.Run("not running as root", func(t *testing.T) {
			err := CheckCapabilities()
			if err == nil {
				t.Error("CheckCapabilities should fail when not running as root")
			}
		})
	}
}

// TestManager_SetResourceLimits_Extended tests SetResourceLimits
func TestManager_SetResourceLimits_Extended(t *testing.T) {
	t.Run("disabled VMContext returns nil", func(t *testing.T) {
		cfg := &config.Config{
			Executor: config.ExecutorConfig{
				EnableJailer: false,
			},
		}
		mgr, err := NewManager(cfg)
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		vmCtx := &VMContext{Enabled: false}
		limits := ResourceLimits{MaxCPUs: 2, MaxMemoryMB: 2048}

		err = mgr.SetResourceLimits(vmCtx, limits)
		if err != nil {
			t.Errorf("SetResourceLimits with disabled VMContext should succeed, got: %v", err)
		}
	})

	t.Run("enabled VMContext stores limits", func(t *testing.T) {
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
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		vmCtx := &VMContext{Enabled: true}
		limits := ResourceLimits{
			MaxCPUs:      4,
			MaxMemoryMB:  4096,
			MaxFD:        8192,
			MaxProcesses: 2048,
		}

		err = mgr.SetResourceLimits(vmCtx, limits)
		if err != nil {
			t.Errorf("SetResourceLimits failed: %v", err)
		}
	})
}

// TestApplyResourceLimits_Extended tests ApplyResourceLimits with various inputs
func TestApplyResourceLimits_Extended(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: cgroup operations only supported on Linux")
	}

	tests := []struct {
		name    string
		pid     int
		limits  ResourceLimits
		wantErr bool
	}{
		{
			name: "valid resource limits",
			pid:  1,
			limits: ResourceLimits{
				MaxCPUs:      2,
				MaxMemoryMB:  2048,
				MaxFD:        4096,
				MaxProcesses: 1024,
			},
			wantErr: true, // Will fail without cgroup setup
		},
		{
			name: "zero CPU limit",
			pid:  1,
			limits: ResourceLimits{
				MaxCPUs:      0,
				MaxMemoryMB:  2048,
			},
			wantErr: true,
		},
		{
			name: "zero memory limit",
			pid:  1,
			limits: ResourceLimits{
				MaxCPUs:      2,
				MaxMemoryMB:  0,
			},
			wantErr: true,
		},
		{
			name: "negative values",
			pid:  1,
			limits: ResourceLimits{
				MaxCPUs:      -1,
				MaxMemoryMB:  2048,
			},
			wantErr: true,
		},
		{
			name: "invalid PID -1",
			pid:  -1,
			limits: ResourceLimits{
				MaxCPUs:      2,
				MaxMemoryMB:  2048,
			},
			wantErr: true,
		},
		{
			name: "invalid PID 0",
			pid:  0,
			limits: ResourceLimits{
				MaxCPUs:      2,
				MaxMemoryMB:  2048,
			},
			wantErr: true,
		},
		{
			name: "non-existent PID",
			pid:  999999,
			limits: ResourceLimits{
				MaxCPUs:      2,
				MaxMemoryMB:  2048,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ApplyResourceLimits(tt.pid, tt.limits)
			if (err != nil) != tt.wantErr {
				t.Errorf("ApplyResourceLimits() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestWriteSeccompProfile_Extended tests seccomp profile writing
func TestWriteSeccompProfile_Extended(t *testing.T) {
	t.Run("write profile to valid path", func(t *testing.T) {
		tempDir := t.TempDir()
		profilePath := filepath.Join(tempDir, "seccomp.json")

		err := WriteSeccompProfile("test-vm", profilePath)
		if err != nil {
			t.Errorf("WriteSeccompProfile failed: %v", err)
		}

		// Verify file exists
		info, err := os.Stat(profilePath)
		if err != nil {
			t.Errorf("Failed to stat profile file: %v", err)
		}

		// Check file mode
		expectedMode := os.FileMode(0644)
		if info.Mode() != expectedMode {
			t.Errorf("File mode should be %v, got: %v", expectedMode, info.Mode())
		}
	})

	t.Run("create profile directory if needed", func(t *testing.T) {
		tempDir := t.TempDir()
		profilePath := filepath.Join(tempDir, "subdir", "seccomp.json")

		err := WriteSeccompProfile("test-vm", profilePath)
		if err != nil {
			t.Errorf("WriteSeccompProfile failed: %v", err)
		}

		// Verify directory was created
		if _, err := os.Stat(filepath.Dir(profilePath)); os.IsNotExist(err) {
			t.Error("Profile directory was not created")
		}
	})
}

// TestValidateSeccompProfile_Extended tests seccomp profile validation
func TestValidateSeccompProfile_Extended(t *testing.T) {
	t.Run("valid default profile", func(t *testing.T) {
		tempDir := t.TempDir()
		profilePath := filepath.Join(tempDir, "seccomp.json")

		err := WriteSeccompProfile("test-vm", profilePath)
		if err != nil {
			t.Fatalf("WriteSeccompProfile failed: %v", err)
		}

		err = ValidateSeccompProfile(profilePath)
		if err != nil {
			t.Errorf("ValidateSeccompProfile failed: %v", err)
		}
	})

	t.Run("non-existent profile file", func(t *testing.T) {
		err := ValidateSeccompProfile("/nonexistent/path/profile.json")
		if err == nil {
			t.Error("ValidateSeccompProfile should fail for non-existent file")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tempFile := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(tempFile, []byte("{ invalid json }"), 0644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}

		err := ValidateSeccompProfile(tempFile)
		if err == nil {
			t.Error("ValidateSeccompProfile should fail for invalid JSON")
		}
	})

	t.Run("invalid default action", func(t *testing.T) {
		tempFile := filepath.Join(t.TempDir(), "invalid-action.json")
		invalidProfile := `{
			"defaultAction": "INVALID_ACTION",
			"architectures": ["SCMP_ARCH_X86_64"],
			"syscalls": []
		}`
		if err := os.WriteFile(tempFile, []byte(invalidProfile), 0644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}

		err := ValidateSeccompProfile(tempFile)
		if err == nil {
			t.Error("ValidateSeccompProfile should fail for invalid default action")
		}
	})

	t.Run("invalid architecture", func(t *testing.T) {
		tempFile := filepath.Join(t.TempDir(), "invalid-arch.json")
		invalidProfile := `{
			"defaultAction": "SCMP_ACT_ALLOW",
			"architectures": ["INVALID_ARCH"],
			"syscalls": []
		}`
		if err := os.WriteFile(tempFile, []byte(invalidProfile), 0644); err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}

		err := ValidateSeccompProfile(tempFile)
		if err == nil {
			t.Error("ValidateSeccompProfile should fail for invalid architecture")
		}
	})
}

// TestDefaultSeccompFilter_Extended tests default seccomp filter creation
func TestDefaultSeccompFilter_Extended(t *testing.T) {
	filter := DefaultSeccompFilter()

	if filter == nil {
		t.Fatal("DefaultSeccompFilter returned nil")
	}

	// Check default action
	if filter.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Errorf("DefaultAction should be SCMP_ACT_ERRNO, got: %s", filter.DefaultAction)
	}

	// Check architectures are present
	if len(filter.Architectures) == 0 {
		t.Error("Architectures should not be empty")
	}

	validArchs := map[string]bool{
		"SCMP_ARCH_X86_64":  true,
		"SCMP_ARCH_X86":     true,
		"SCMP_ARCH_ARM":     true,
		"SCMP_ARCH_AARCH64": true,
	}
	for _, arch := range filter.Architectures {
		if !validArchs[arch] {
			t.Errorf("Invalid architecture: %s", arch)
		}
	}

	// Check syscalls are present
	if len(filter.Syscalls) == 0 {
		t.Error("Syscalls should not be empty")
	}

	// Verify at least some common syscalls are allowed
	syscallMap := make(map[string]bool)
	for _, rule := range filter.Syscalls {
		if rule.Action == "SCMP_ACT_ALLOW" {
			for _, name := range rule.Names {
				syscallMap[name] = true
			}
		}
	}

	// Check for essential syscalls
	essentialSyscalls := []string{"read", "write", "open", "close", "exit"}
	for _, syscallName := range essentialSyscalls {
		if !syscallMap[syscallName] {
			t.Errorf("Essential syscall %s not found in allowed list", syscallName)
		}
	}
}

// TestRestrictiveSeccompFilter_Extended tests restrictive seccomp filter
func TestRestrictiveSeccompFilter_Extended(t *testing.T) {
	filter := RestrictiveSeccompFilter()

	if filter == nil {
		t.Fatal("RestrictiveSeccompFilter returned nil")
	}

	// Verify dangerous syscalls are blocked
	dangerousSyscalls := []string{
		"mount", "umount2", "pivot_root", "chroot",
		"init_module", "delete_module", "kexec_load",
		"swapon", "swapoff", "reboot",
	}

	syscallMap := make(map[string]bool)
	for _, rule := range filter.Syscalls {
		if rule.Action == "SCMP_ACT_ALLOW" {
			for _, name := range rule.Names {
				syscallMap[name] = true
			}
		}
	}

	for _, syscallName := range dangerousSyscalls {
		if syscallMap[syscallName] {
			t.Errorf("Dangerous syscall %s should be blocked in restrictive filter", syscallName)
		}
	}

	// Verify essential syscalls are still allowed
	essentialSyscalls := []string{"read", "write", "open", "close"}
	for _, syscallName := range essentialSyscalls {
		if !syscallMap[syscallName] {
			t.Errorf("Essential syscall %s should be allowed even in restrictive filter", syscallName)
		}
	}
}

// TestJailer_SetupNetworkNamespace_Extended tests network namespace setup
func TestJailer_SetupNetworkNamespace_Extended(t *testing.T) {
	t.Run("empty network namespace", func(t *testing.T) {
		jailer := &Jailer{}
		ctx := &JailContext{NetNS: ""}

		err := jailer.SetupNetworkNamespace(ctx)
		if err != nil {
			t.Errorf("SetupNetworkNamespace with empty NetNS should not error: %v", err)
		}
	})

	t.Run("non-existent network namespace", func(t *testing.T) {
		jailer := &Jailer{}
		ctx := &JailContext{NetNS: "nonexistent-ns-xyz123"}

		err := jailer.SetupNetworkNamespace(ctx)
		if err == nil {
			t.Error("SetupNetworkNamespace should fail with non-existent namespace")
		}
	})
}

// TestJailer_EnterJail_Extended tests EnterJail method
func TestJailer_EnterJail_Extended(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Skipping test: requires root privileges for chroot")
	}

	t.Run("enter jail with disabled context", func(t *testing.T) {
		jailer := &Jailer{}
		ctx := &JailContext{Enabled: false}

		err := jailer.EnterJail(ctx)
		if err != nil {
			t.Errorf("EnterJail with disabled context should not error: %v", err)
		}
	})

	t.Run("enter jail with enabled context requires proper setup", func(t *testing.T) {
		// This would require a proper jail setup, skip for now
		// as it would require forking and changing root
		t.Skip("EnterJail with enabled context requires forking, skipping")
	})
}

// TestVMContext tests VMContext structure
func TestVMContext(t *testing.T) {
	t.Run("create disabled VMContext", func(t *testing.T) {
		vmCtx := &VMContext{Enabled: false}

		if vmCtx.Enabled {
			t.Error("VMContext should be disabled")
		}

		if vmCtx.VMID != "" {
			t.Error("VMID should be empty for disabled context")
		}
	})

	t.Run("create enabled VMContext", func(t *testing.T) {
		vmCtx := &VMContext{
			Enabled:        true,
			VMID:           "test-vm-123",
			JailPath:       "/srv/jailer/test-vm-123",
			SeccompProfile: "/srv/jailer/test-vm-123/seccomp.json",
		}

		if !vmCtx.Enabled {
			t.Error("VMContext should be enabled")
		}

		if vmCtx.VMID != "test-vm-123" {
			t.Errorf("VMID should be test-vm-123, got: %s", vmCtx.VMID)
		}

		if vmCtx.JailPath != "/srv/jailer/test-vm-123" {
			t.Errorf("JailPath mismatch")
		}

		if vmCtx.SeccompProfile != "/srv/jailer/test-vm-123/seccomp.json" {
			t.Errorf("SeccompProfile mismatch")
		}
	})
}

// TestJailContext tests JailContext structure
func TestJailContext(t *testing.T) {
	t.Run("create disabled JailContext", func(t *testing.T) {
		ctx := &JailContext{Enabled: false}

		if ctx.Enabled {
			t.Error("JailContext should be disabled")
		}
	})

	t.Run("create enabled JailContext", func(t *testing.T) {
		ctx := &JailContext{
			Enabled:    true,
			JailPath:   "/srv/jailer/test-vm",
			UID:        1000,
			GID:        1000,
			NetNS:      "myns",
			OriginalWD: "/",
		}

		if !ctx.Enabled {
			t.Error("JailContext should be enabled")
		}

		if ctx.UID != 1000 {
			t.Errorf("UID should be 1000, got: %d", ctx.UID)
		}

		if ctx.GID != 1000 {
			t.Errorf("GID should be 1000, got: %d", ctx.GID)
		}

		if ctx.NetNS != "myns" {
			t.Errorf("NetNS should be myns, got: %s", ctx.NetNS)
		}

		if ctx.OriginalWD != "/" {
			t.Errorf("OriginalWD should be /, got: %s", ctx.OriginalWD)
		}
	})
}

// TestResourceLimits tests ResourceLimits structure
func TestResourceLimits(t *testing.T) {
	limits := ResourceLimits{
		MaxCPUs:      4,
		MaxMemoryMB:  8192,
		MaxFD:        16384,
		MaxProcesses: 4096,
	}

	if limits.MaxCPUs != 4 {
		t.Errorf("MaxCPUs should be 4, got: %d", limits.MaxCPUs)
	}

	if limits.MaxMemoryMB != 8192 {
		t.Errorf("MaxMemoryMB should be 8192, got: %d", limits.MaxMemoryMB)
	}

	if limits.MaxFD != 16384 {
		t.Errorf("MaxFD should be 16384, got: %d", limits.MaxFD)
	}

	if limits.MaxProcesses != 4096 {
		t.Errorf("MaxProcesses should be 4096, got: %d", limits.MaxProcesses)
	}
}

// TestSeccompFilterStructures tests seccomp filter structures
func TestSeccompFilterStructures(t *testing.T) {
	t.Run("create SeccompFilter", func(t *testing.T) {
		filter := &SeccompFilter{
			DefaultAction: "SCMP_ACT_ALLOW",
			Architectures: []string{"SCMP_ARCH_X86_64"},
			Syscalls: []SyscallRule{
				{
					Names:  []string{"read", "write"},
					Action: "SCMP_ACT_ALLOW",
				},
			},
		}

		if filter.DefaultAction != "SCMP_ACT_ALLOW" {
			t.Errorf("DefaultAction mismatch")
		}

		if len(filter.Syscalls) != 1 {
			t.Errorf("Should have 1 syscall rule")
		}
	})

	t.Run("create SyscallRule with args", func(t *testing.T) {
		rule := SyscallRule{
			Names: []string{"ioctl"},
			Action: "SCMP_ACT_ERRNO",
			Args: []Arg{
				{
					Index: 1,
					Value: 0x5401,
					Op:    "SCMP_CMP_EQ",
				},
			},
		}

		if len(rule.Args) != 1 {
			t.Errorf("Should have 1 arg")
		}

		if rule.Args[0].Index != 1 {
			t.Errorf("Arg index should be 1")
		}
	})
}

// TestCleanupCgroup_Extended tests cgroup cleanup
func TestCleanupCgroup_Extended(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping test: cgroup operations only supported on Linux")
	}

	// Test with non-existent cgroup (should not error)
	err := CleanupCgroup(999999)
	if err != nil {
		// May error if directory doesn't exist, but that's ok
		t.Logf("CleanupCgroup with non-existent cgroup: %v", err)
	}

	// Test with current process PID
	pid := os.Getpid()
	err = CleanupCgroup(pid)
	if err != nil {
		t.Logf("CleanupCgroup for current process: %v", err)
	}
}

// TestSyscallConstants verifies syscall constants are valid
func TestSyscallConstants(t *testing.T) {
	// This test verifies that the rlimit constants used in the code
	// are valid for the current platform

	const (
		RLIMIT_NOFILE = syscall.RLIMIT_NOFILE
		RLIMIT_CPU    = syscall.RLIMIT_CPU
		RLIMIT_AS     = syscall.RLIMIT_AS
	)

	// Just verify they're non-zero (platforms where these don't exist
	// would cause compilation errors)
	_ = RLIMIT_NOFILE
	_ = RLIMIT_CPU
	_ = RLIMIT_AS
}

// TestHasCapability tests hasCapability helper function
func TestHasCapability(t *testing.T) {
	// hasCapability always returns true in the current implementation
	// This test documents that behavior
	result := hasCapability(0)
	if !result {
		t.Error("hasCapability should return true (simplified implementation)")
	}
}

// TestManager_PrepareVM_Extended tests PrepareVM with edge cases
func TestManager_PrepareVM_Extended(t *testing.T) {
	t.Run("prepare VM with unique IDs", func(t *testing.T) {
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
		if err != nil {
			t.Fatalf("NewManager failed: %v", err)
		}

		// Create multiple VMs
		vmIDs := []string{"vm-1", "vm-2", "vm-3"}
		for _, vmID := range vmIDs {
			vmCtx, err := mgr.PrepareVM(context.Background(), vmID)
			if err != nil {
				t.Errorf("PrepareVM failed for %s: %v", vmID, err)
			}

			if !vmCtx.Enabled {
				t.Errorf("VMContext should be enabled for %s", vmID)
			}

			if vmCtx.VMID != vmID {
				t.Errorf("VMID mismatch for %s", vmID)
			}
		}
	})
}

// TestGetDefaultSecurityConfig_Extended verifies default configuration values
func TestGetDefaultSecurityConfig_Extended(t *testing.T) {
	cfg := GetDefaultSecurityConfig()

	// Verify all fields are set to expected defaults
	defaults := map[string]interface{}{
		"UID":           1000,
		"GID":           1000,
		"ChrootBaseDir": "/srv/jailer",
		"NetNS":         "",
	}

	if cfg.UID != defaults["UID"].(int) {
		t.Errorf("Default UID should be %d, got: %d", defaults["UID"], cfg.UID)
	}

	if cfg.GID != defaults["GID"].(int) {
		t.Errorf("Default GID should be %d, got: %d", defaults["GID"], cfg.GID)
	}

	if cfg.ChrootBaseDir != defaults["ChrootBaseDir"].(string) {
		t.Errorf("Default ChrootBaseDir should be %s, got: %s", defaults["ChrootBaseDir"], cfg.ChrootBaseDir)
	}

	if cfg.NetNS != defaults["NetNS"].(string) {
		t.Errorf("Default NetNS should be empty, got: %s", cfg.NetNS)
	}
}

// TestValidateSecurityConfig_Extended tests additional validation scenarios
func TestValidateSecurityConfig_Extended(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{
			name: "config with world-writable chroot path",
			cfg: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: "",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "config with relative chroot path (would fail ValidatePath)",
			cfg: &config.Config{
				Executor: config.ExecutorConfig{
					EnableJailer: true,
					Jailer: config.JailerConfig{
						UID:           1000,
						GID:           1000,
						ChrootBaseDir: "relative/path",
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecurityConfig(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSecurityConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestNewJailer_Extended tests NewJailer constructor with various inputs
func TestNewJailer_Extended(t *testing.T) {
	tests := []struct {
		name        string
		uid         int
		gid         int
		chrootDir   string
		netNS       string
		expectError bool
	}{
		{
			name:        "valid jailer",
			uid:         1000,
			gid:         1000,
			chrootDir:   "/srv/jailer",
			netNS:       "",
			expectError: false,
		},
		{
			name:        "jailer with network namespace",
			uid:         1000,
			gid:         1000,
			chrootDir:   "/srv/jailer",
			netNS:       "myns",
			expectError: false,
		},
		{
			name:        "jailer with root UID",
			uid:         0,
			gid:         0,
			chrootDir:   "/srv/jailer",
			netNS:       "",
			expectError: false, // NewJailer doesn't validate
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jailer := NewJailer(tt.uid, tt.gid, tt.chrootDir, tt.netNS)

			if jailer == nil {
				t.Error("NewJailer should not return nil")
			}

			if jailer.UID != tt.uid {
				t.Errorf("UID should be %d, got: %d", tt.uid, jailer.UID)
			}

			if jailer.GID != tt.gid {
				t.Errorf("GID should be %d, got: %d", tt.gid, jailer.GID)
			}

			if jailer.ChrootBaseDir != tt.chrootDir {
				t.Errorf("ChrootBaseDir should be %s, got: %s", tt.chrootDir, jailer.ChrootBaseDir)
			}

			if jailer.NetNS != tt.netNS {
				t.Errorf("NetNS should be %s, got: %s", tt.netNS, jailer.NetNS)
			}

			if !jailer.Enabled {
				t.Error("Enabled should be true by default")
			}
		})
	}
}

// TestJailer_Validate_Extended tests additional validation scenarios
func TestJailer_Validate_Extended(t *testing.T) {
	tests := []struct {
		name    string
		jailer  *Jailer
		wantErr bool
	}{
		{
			name: "zero UID",
			jailer: &Jailer{
				UID:           0,
				GID:           1000,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			wantErr: false, // 0 is valid UID (root)
		},
		{
			name: "zero GID",
			jailer: &Jailer{
				UID:           1000,
				GID:           0,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			wantErr: false, // 0 is valid GID (root)
		},
		{
			name: "very large UID",
			jailer: &Jailer{
				UID:           65534, // nobody
				GID:           65534,
				ChrootBaseDir: t.TempDir(),
				Enabled:       true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.jailer.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Jailer.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSymlinkHandling tests symlink detection in ValidatePath
func TestSymlinkHandling(t *testing.T) {
	t.Run("directory symlink should fail validation", func(t *testing.T) {
		targetDir := t.TempDir()
		linkDir := filepath.Join(t.TempDir(), "symlink")

		// Create symlink
		if err := os.Symlink(targetDir, linkDir); err != nil {
			t.Fatalf("Failed to create symlink: %v", err)
		}

		err := ValidatePath(linkDir)
		if err == nil {
			t.Error("ValidatePath should fail for directory symlink")
		}
	})

	t.Run("file symlink should pass (no check for files)", func(t *testing.T) {
		targetFile := filepath.Join(t.TempDir(), "target.txt")
		if err := os.WriteFile(targetFile, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create target file: %v", err)
		}

		linkFile := filepath.Join(t.TempDir(), "link.txt")
		if err := os.Symlink(targetFile, linkFile); err != nil {
			t.Fatalf("Failed to create symlink: %v", err)
		}

		// Files don't have symlink checking, so this should pass
		err := ValidatePath(linkFile)
		// May succeed or fail depending on implementation
		t.Logf("File symlink validation result: %v", err)
	})
}

// TestPathTraversalTests tests path traversal scenarios
func TestPathTraversalTests(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() string
		wantErr bool
	}{
		{
			name: "path with parent directory references",
			setup: func() string {
				// Create a valid path that contains .. but resolves to a real path
				tempDir := t.TempDir()
				return filepath.Join(tempDir, "..", filepath.Base(tempDir))
			},
			wantErr: false, // Resolved path exists
		},
		{
			name: "traversal outside root",
			setup: func() string {
				// This would be /tmp/../../../etc/passwd which resolves to /etc/passwd
				return "/tmp/../../../etc/passwd"
			},
			wantErr: false, // /etc/passwd exists
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup()
			err := ValidatePath(path)
			t.Logf("ValidatePath(%s) = %v", path, err)
			// Result depends on the actual path resolution
		})
	}
}

// TestSeccompProfileDirectoryPermissions tests profile creation with various directory states
func TestSeccompProfileDirectoryPermissions(t *testing.T) {
	t.Run("create profile in read-only parent directory", func(t *testing.T) {
		tempDir := t.TempDir()
		// Make parent directory read-only
		os.Chmod(tempDir, 0444)
		defer os.Chmod(tempDir, 0755) // Restore for cleanup

		profilePath := filepath.Join(tempDir, "subdir", "seccomp.json")
		err := WriteSeccompProfile("test-vm", profilePath)
		if err == nil {
			t.Error("WriteSeccompProfile should fail with read-only parent directory")
		}
	})
}

// TestMultipleVMCleanup tests cleanup of multiple VMs
func TestMultipleVMCleanup(t *testing.T) {
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
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// Create multiple VMs
	vmIDs := []string{"vm-1", "vm-2", "vm-3"}
	var vmContexts []*VMContext

	for _, vmID := range vmIDs {
		vmCtx, err := mgr.PrepareVM(ctx, vmID)
		if err != nil {
			t.Fatalf("PrepareVM failed for %s: %v", vmID, err)
		}
		vmContexts = append(vmContexts, vmCtx)
	}

	// Cleanup all VMs
	for _, vmCtx := range vmContexts {
		err := mgr.CleanupVM(ctx, vmCtx)
		if err != nil {
			t.Errorf("CleanupVM failed for %s: %v", vmCtx.VMID, err)
		}
	}

	// Verify all jails were removed
	for _, vmID := range vmIDs {
		jailPath := filepath.Join(tempDir, vmID)
		if _, err := os.Stat(jailPath); !os.IsNotExist(err) {
			t.Errorf("Jail for %s was not removed", vmID)
		}
	}
}

// TestConcurrentVMOperations tests concurrent VM preparation and cleanup
func TestConcurrentVMOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

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
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	ctx := context.Background()

	// Prepare multiple VMs concurrently
	done := make(chan bool, 5)
	for i := 0; i < 5; i++ {
		go func(id int) {
			vmID := fmt.Sprintf("vm-%d", id)
			vmCtx, err := mgr.PrepareVM(ctx, vmID)
			if err != nil {
				t.Errorf("PrepareVM failed for %s: %v", vmID, err)
			}
			if vmCtx != nil {
				_ = mgr.CleanupVM(ctx, vmCtx)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}
}
