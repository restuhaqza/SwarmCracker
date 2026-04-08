package jailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJailerNew tests jailer instance creation with various configurations.
func TestJailerNew(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "config cannot be nil",
		},
		{
			name: "missing FirecrackerPath",
			config: &Config{
				JailerPath:    "/usr/local/bin/jailer",
				ChrootBaseDir: "/tmp/jailer-test",
				UID:           1000,
				GID:           1000,
			},
			wantErr: true,
			errMsg:  "FirecrackerPath is required",
		},
		{
			name: "missing JailerPath",
			config: &Config{
				FirecrackerPath: "/usr/local/bin/firecracker",
				ChrootBaseDir:   "/tmp/jailer-test",
				UID:             1000,
				GID:             1000,
			},
			wantErr: true,
			errMsg:  "JailerPath is required",
		},
		{
			name: "missing ChrootBaseDir",
			config: &Config{
				FirecrackerPath: "/usr/local/bin/firecracker",
				JailerPath:      "/usr/local/bin/jailer",
				UID:             1000,
				GID:             1000,
			},
			wantErr: true,
			errMsg:  "ChrootBaseDir is required",
		},
		{
			name: "missing UID",
			config: &Config{
				FirecrackerPath: "/usr/local/bin/firecracker",
				JailerPath:      "/usr/local/bin/jailer",
				ChrootBaseDir:   "/tmp/jailer-test",
				GID:             1000,
			},
			wantErr: true,
			errMsg:  "UID must be non-zero",
		},
		{
			name: "missing GID",
			config: &Config{
				FirecrackerPath: "/usr/local/bin/firecracker",
				JailerPath:      "/usr/local/bin/jailer",
				ChrootBaseDir:   "/tmp/jailer-test",
				UID:             1000,
			},
			wantErr: true,
			errMsg:  "GID must be non-zero",
		},
		{
			name: "valid config with non-existent binaries",
			config: &Config{
				FirecrackerPath: "/nonexistent/firecracker",
				JailerPath:      "/nonexistent/jailer",
				ChrootBaseDir:   "/tmp/jailer-test",
				UID:             1000,
				GID:             1000,
			},
			wantErr: true,
			errMsg:  "Firecracker binary not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := New(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("New() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
			if j != nil && !tt.wantErr {
				defer j.Close()
			}
		})
	}
}

// TestJailerValidateVMConfig tests VM configuration validation.
func TestJailerValidateVMConfig(t *testing.T) {
	// Create temporary files for testing
	tmpDir := t.TempDir()
	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.img")

	// Create dummy files
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0644); err != nil {
		t.Fatalf("Failed to create kernel file: %v", err)
	}
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0644); err != nil {
		t.Fatalf("Failed to create rootfs file: %v", err)
	}

	tests := []struct {
		name    string
		config  VMConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "missing TaskID",
			config: VMConfig{
				VcpuCount:  1,
				MemoryMB:   512,
				KernelPath: kernelPath,
				RootfsPath: rootfsPath,
			},
			wantErr: true,
			errMsg:  "TaskID is required",
		},
		{
			name: "invalid VcpuCount",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  0,
				MemoryMB:   512,
				KernelPath: kernelPath,
				RootfsPath: rootfsPath,
			},
			wantErr: true,
			errMsg:  "VcpuCount must be positive",
		},
		{
			name: "invalid MemoryMB",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  1,
				MemoryMB:   0,
				KernelPath: kernelPath,
				RootfsPath: rootfsPath,
			},
			wantErr: true,
			errMsg:  "MemoryMB must be positive",
		},
		{
			name: "missing KernelPath",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  1,
				MemoryMB:   512,
				RootfsPath: rootfsPath,
			},
			wantErr: true,
			errMsg:  "KernelPath is required",
		},
		{
			name: "missing RootfsPath",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  1,
				MemoryMB:   512,
				KernelPath: kernelPath,
			},
			wantErr: true,
			errMsg:  "RootfsPath is required",
		},
		{
			name: "non-existent kernel",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  1,
				MemoryMB:   512,
				KernelPath: "/nonexistent/kernel",
				RootfsPath: rootfsPath,
			},
			wantErr: true,
			errMsg:  "kernel not found",
		},
		{
			name: "non-existent rootfs",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  1,
				MemoryMB:   512,
				KernelPath: kernelPath,
				RootfsPath: "/nonexistent/rootfs",
			},
			wantErr: true,
			errMsg:  "rootfs not found",
		},
		{
			name: "valid config",
			config: VMConfig{
				TaskID:     "test-vm",
				VcpuCount:  2,
				MemoryMB:   1024,
				KernelPath: kernelPath,
				RootfsPath: rootfsPath,
				BootArgs:   "console=ttyS0",
				HtEnabled:  false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &Jailer{
				config: &Config{
					FirecrackerPath: "/usr/local/bin/firecracker",
					JailerPath:      "/usr/local/bin/jailer",
					ChrootBaseDir:   tmpDir,
					UID:             1000,
					GID:             1000,
				},
			}

			err := j.validateVMConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVMConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil && tt.errMsg != "" {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("validateVMConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestJailerBuildJailerCommand tests jailer command construction.
func TestJailerBuildJailerCommand(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dummy binaries
	firecrackerPath := filepath.Join(tmpDir, "firecracker")
	jailerPath := filepath.Join(tmpDir, "jailer")
	if err := os.WriteFile(firecrackerPath, []byte("fake"), 0755); err != nil {
		t.Fatalf("Failed to create firecracker binary: %v", err)
	}
	if err := os.WriteFile(jailerPath, []byte("fake"), 0755); err != nil {
		t.Fatalf("Failed to create jailer binary: %v", err)
	}

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.img")
	os.WriteFile(kernelPath, []byte("kernel"), 0644)
	os.WriteFile(rootfsPath, []byte("rootfs"), 0644)

	j := &Jailer{
		config: &Config{
			FirecrackerPath: firecrackerPath,
			JailerPath:      jailerPath,
			ChrootBaseDir:   filepath.Join(tmpDir, "jailer"),
			UID:             1000,
			GID:             1000,
			CgroupVersion:   "v2",
			EnableSeccomp:   false,
		},
	}

	cfg := VMConfig{
		TaskID:     "test-vm-123",
		VcpuCount:  2,
		MemoryMB:   1024,
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
	}

	cmd, socketPath, err := j.buildJailerCommand(cfg)
	if err != nil {
		t.Fatalf("buildJailerCommand() error = %v", err)
	}

	// Verify command
	if cmd.Path != jailerPath {
		t.Errorf("Expected jailer path %q, got %q", jailerPath, cmd.Path)
	}

	// Verify arguments contain expected flags
	args := cmd.Args
	checkArg := func(expected string) {
		found := false
		for _, arg := range args {
			if arg == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected argument %q not found in %v", expected, args)
		}
	}

	checkArg("--id")
	checkArg("test-vm-123")
	checkArg("--exec-file")
	checkArg(firecrackerPath)
	checkArg("--uid")
	checkArg("1000")
	checkArg("--gid")
	checkArg("1000")
	checkArg("--chroot-base-dir")
	checkArg(filepath.Join(tmpDir, "jailer"))
	checkArg("--cgroup-version")
	checkArg("v2")
	checkArg("--") // Separator

	// Verify socket path
	expectedSocket := filepath.Join(tmpDir, "jailer", "test-vm-123", "run", "firecracker", "test-vm-123.sock")
	if socketPath != expectedSocket {
		t.Errorf("Expected socket path %q, got %q", expectedSocket, socketPath)
	}
}

// TestJailerBuildJailerCommandWithSeccomp tests seccomp policy generation.
func TestJailerBuildJailerCommandWithSeccomp(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dummy binaries in separate directory
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	firecrackerPath := filepath.Join(binDir, "firecracker")
	jailerPath := filepath.Join(binDir, "jailer")
	os.WriteFile(firecrackerPath, []byte("fake"), 0755)
	os.WriteFile(jailerPath, []byte("fake"), 0755)

	kernelPath := filepath.Join(tmpDir, "vmlinux")
	rootfsPath := filepath.Join(tmpDir, "rootfs.img")
	os.WriteFile(kernelPath, []byte("kernel"), 0644)
	os.WriteFile(rootfsPath, []byte("rootfs"), 0644)

	chrootDir := filepath.Join(tmpDir, "chroot")
	j := &Jailer{
		config: &Config{
			FirecrackerPath: firecrackerPath,
			JailerPath:      jailerPath,
			ChrootBaseDir:   chrootDir,
			UID:             1000,
			GID:             1000,
			CgroupVersion:   "v2",
			EnableSeccomp:   true,
		},
	}

	cfg := VMConfig{
		TaskID:     "test-vm-seccomp",
		VcpuCount:  1,
		MemoryMB:   512,
		KernelPath: kernelPath,
		RootfsPath: rootfsPath,
	}

	cmd, socketPath, err := j.buildJailerCommand(cfg)
	if err != nil {
		t.Fatalf("buildJailerCommand() error = %v", err)
	}

	// Verify command was built
	if cmd == nil { //nolint:staticcheck // checked for nil before dereference
		t.Fatal("Expected non-nil command")
	}
	cmdArgs := cmd.Args //nolint:staticcheck // t.Fatal terminates test

	// Verify socket path is in chroot directory
	expectedSocketPattern := filepath.Join(chrootDir, "test-vm-seccomp", "run", "firecracker")
	if !strings.Contains(socketPath, expectedSocketPattern) {
		t.Errorf("Socket path %q should contain %q", socketPath, expectedSocketPattern)
	}

	// Verify seccomp flag is present
	foundSeccomp := false
	var policyPath string
	for i, arg := range cmdArgs {
		if arg == "--seccomp" && i+1 < len(cmdArgs) {
			foundSeccomp = true
			policyPath = cmdArgs[i+1]
			break
		}
	}

	if !foundSeccomp {
		t.Logf("Command args: %v", cmdArgs)
		t.Error("Expected --seccomp flag in jailer command")
	} else if policyPath != "" {
		// Verify policy file exists and is valid JSON
		if _, err := os.Stat(policyPath); err != nil {
			t.Errorf("Seccomp policy file not found at %q: %v", policyPath, err)
		} else {
			// Try to read and parse the policy
			data, err := os.ReadFile(policyPath)
			if err != nil {
				t.Errorf("Failed to read seccomp policy: %v", err)
			} else if len(data) == 0 {
				t.Error("Seccomp policy file is empty")
			} else {
				t.Logf("Seccomp policy created at %q (%d bytes)", policyPath, len(data))
			}
		}
	}
}

// TestJailerListProcesses tests process listing.
func TestJailerListProcesses(t *testing.T) {
	j := &Jailer{
		config: &Config{
			FirecrackerPath: "/usr/local/bin/firecracker",
			JailerPath:      "/usr/local/bin/jailer",
			ChrootBaseDir:   "/tmp/jailer-test",
			UID:             1000,
			GID:             1000,
		},
		processes: make(map[string]*Process),
	}

	// Add some fake processes
	j.processes["vm-1"] = &Process{TaskID: "vm-1", Pid: 1001}
	j.processes["vm-2"] = &Process{TaskID: "vm-2", Pid: 1002}
	j.processes["vm-3"] = &Process{TaskID: "vm-3", Pid: 1003}

	taskIDs := j.ListProcesses()
	if len(taskIDs) != 3 {
		t.Errorf("Expected 3 processes, got %d", len(taskIDs))
	}

	// Verify all task IDs are present
	expected := map[string]bool{"vm-1": true, "vm-2": true, "vm-3": true}
	for _, id := range taskIDs {
		if !expected[id] {
			t.Errorf("Unexpected task ID: %q", id)
		}
	}
}

// TestJailerGetProcess tests process retrieval.
func TestJailerGetProcess(t *testing.T) {
	j := &Jailer{
		config: &Config{
			FirecrackerPath: "/usr/local/bin/firecracker",
			JailerPath:      "/usr/local/bin/jailer",
			ChrootBaseDir:   "/tmp/jailer-test",
			UID:             1000,
			GID:             1000,
		},
		processes: make(map[string]*Process),
	}

	// Add a fake process
	expectedProcess := &Process{TaskID: "vm-1", Pid: 1001}
	j.processes["vm-1"] = expectedProcess

	// Test existing process
	process, ok := j.GetProcess("vm-1")
	if !ok {
		t.Error("Expected to find process vm-1")
	}
	if process != expectedProcess {
		t.Error("Returned process doesn't match expected")
	}

	// Test non-existing process
	_, ok = j.GetProcess("vm-nonexistent")
	if ok {
		t.Error("Expected to not find nonexistent process")
	}
}

// TestJailerClose tests cleanup.
func TestJailerClose(t *testing.T) {
	tmpDir := t.TempDir()

	j := &Jailer{
		config: &Config{
			FirecrackerPath: "/usr/local/bin/firecracker",
			JailerPath:      "/usr/local/bin/jailer",
			ChrootBaseDir:   tmpDir,
			UID:             1000,
			GID:             1000,
		},
		processes: make(map[string]*Process),
	}

	// Note: We can't actually test process killing without real processes,
	// but we can test the cleanup logic
	err := j.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Verify processes map is empty (should be after close)
	if len(j.processes) != 0 {
		t.Errorf("Expected processes map to be empty, got %d entries", len(j.processes))
	}
}

// TestWaitForSocket tests socket waiting logic.
func TestWaitForSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")

	j := &Jailer{
		config: &Config{
			FirecrackerPath: "/usr/local/bin/firecracker",
			JailerPath:      "/usr/local/bin/jailer",
			ChrootBaseDir:   tmpDir,
			UID:             1000,
			GID:             1000,
		},
	}

	// Test timeout when socket doesn't exist
	err := j.waitForSocket(socketPath, 100*time.Millisecond)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	// Test success when socket is created
	go func() {
		time.Sleep(50 * time.Millisecond)
		os.WriteFile(socketPath, []byte("socket"), 0644)
	}()

	err = j.waitForSocket(socketPath, 500*time.Millisecond)
	if err != nil {
		t.Errorf("waitForSocket() error = %v", err)
	}
}

// TestJailerConfigDefaults tests default configuration values.
func TestJailerConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dummy binaries in separate directory
	binDir := filepath.Join(tmpDir, "bin")
	os.MkdirAll(binDir, 0755)
	firecrackerPath := filepath.Join(binDir, "firecracker")
	jailerPath := filepath.Join(binDir, "jailer")
	os.WriteFile(firecrackerPath, []byte("fake"), 0755)
	os.WriteFile(jailerPath, []byte("fake"), 0755)

	// Use separate directory for chroot (not conflicting with binary paths)
	chrootDir := filepath.Join(tmpDir, "chroot")

	config := &Config{
		FirecrackerPath: firecrackerPath,
		JailerPath:      jailerPath,
		ChrootBaseDir:   chrootDir,
		UID:             1000,
		GID:             1000,
		// CgroupVersion intentionally empty to test default
	}

	j, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer j.Close()

	// Verify default cgroup version is set (should auto-detect)
	if j.config.CgroupVersion == "" {
		t.Error("Expected default CgroupVersion to be set")
	}

	// Verify it's either v1 or v2
	if j.config.CgroupVersion != "v1" && j.config.CgroupVersion != "v2" {
		t.Errorf("Expected CgroupVersion to be v1 or v2, got %q", j.config.CgroupVersion)
	}

	// Verify chroot directory was created
	if _, err := os.Stat(chrootDir); err != nil {
		t.Errorf("Chroot directory not created: %v", err)
	}

	t.Logf("Default cgroup version: %s", j.config.CgroupVersion)
}

// contains checks if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestMain sets up test fixtures.
func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}
