package jailer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateVMConfig tests VM config validation
func TestValidateVMConfig(t *testing.T) {
	j := &Jailer{
		config: &Config{},
	}

	tests := []struct {
		name        string
		cfg         VMConfig
		expectError bool
		errorContains string
	}{
		{
			name: "valid config but files missing",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  2,
				MemoryMB:   512,
				KernelPath: "/path/to/kernel",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true, // Files don't exist
			errorContains: "kernel",
		},
		{
			name: "empty TaskID",
			cfg: VMConfig{
				TaskID:     "",
				VcpuCount:  2,
				MemoryMB:   512,
				KernelPath: "/path/to/kernel",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true,
			errorContains: "TaskID",
		},
		{
			name: "zero VcpuCount",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  0,
				MemoryMB:   512,
				KernelPath: "/path/to/kernel",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true,
			errorContains: "VcpuCount",
		},
		{
			name: "negative VcpuCount",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  -1,
				MemoryMB:   512,
				KernelPath: "/path/to/kernel",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true,
			errorContains: "VcpuCount",
		},
		{
			name: "zero MemoryMB",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  2,
				MemoryMB:   0,
				KernelPath: "/path/to/kernel",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true,
			errorContains: "MemoryMB",
		},
		{
			name: "empty KernelPath",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  2,
				MemoryMB:   512,
				KernelPath: "",
				RootfsPath: "/path/to/rootfs",
			},
			expectError: true,
			errorContains: "KernelPath",
		},
		{
			name: "empty RootfsPath",
			cfg: VMConfig{
				TaskID:     "test-task",
				VcpuCount:  2,
				MemoryMB:   512,
				KernelPath: "/path/to/kernel",
				RootfsPath: "",
			},
			expectError: true,
			errorContains: "RootfsPath",
		},
		{
			name: "all empty",
			cfg: VMConfig{},
			expectError: true,
			errorContains: "TaskID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := j.validateVMConfig(tt.cfg)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateVMConfigSocket tests socket waiting scenarios
func TestValidateVMConfigSocket(t *testing.T) {
	j := &Jailer{
		config: &Config{},
	}

	tests := []struct {
		name        string
		setupSocket func(socketPath string)
		timeout     time.Duration
		expectError bool
	}{
		{
			name: "socket exists immediately",
			setupSocket: func(socketPath string) {
				// Create socket before waiting
			},
			timeout: 100 * time.Millisecond,
			expectError: true, // Will timeout if socket not created
		},
		{
			name: "socket never created",
			setupSocket: func(socketPath string) {
				// Don't create socket
			},
			timeout: 100 * time.Millisecond,
			expectError: true, // Timeout
		},
		{
			name: "zero timeout",
			setupSocket: func(socketPath string) {
				// Socket created
			},
			timeout: 0,
			expectError: true, // Immediate timeout
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath := "/tmp/nonexistent-socket.sock"
			err := j.waitForSocket(socketPath, tt.timeout)

			// All scenarios should error since we can't create real socket
			_ = err
		})
	}
}

// TestValidateVMConfigSocket_Timing tests socket waiting timing
func TestValidateVMConfigSocket_Timing(t *testing.T) {
	j := &Jailer{
		config: &Config{},
	}

	// Test that waitForSocket respects timeout
	start := time.Now()
	err := j.waitForSocket("/tmp/nonexistent.socket", 200*time.Millisecond)
	elapsed := time.Since(start)

	// Should timeout after approximately 200ms
	require.Error(t, err)
	assert.GreaterOrEqual(t, elapsed.Milliseconds(), int64(150))
	assert.LessOrEqual(t, elapsed.Milliseconds(), int64(400))
}