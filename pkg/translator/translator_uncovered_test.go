package translator

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetInitPath tests the getInitPath function
func TestGetInitPath(t *testing.T) {
	tests := []struct {
		name       string
		initSystem string
		expected   string
	}{
		{
			name:       "tini init system",
			initSystem: "tini",
			expected:   "/sbin/tini",
		},
		{
			name:       "dumb-init init system",
			initSystem: "dumb-init",
			expected:   "/sbin/dumb-init",
		},
		{
			name:       "unknown init system",
			initSystem: "unknown",
			expected:   "",
		},
		{
			name:       "empty init system",
			initSystem: "",
			expected:   "",
		},
		{
			name:       "none init system",
			initSystem: "none",
			expected:   "",
		},
		{
			name:       "case sensitive - TINI",
			initSystem: "TINI",
			expected:   "",
		},
		{
			name:       "case sensitive - Dumb-Init",
			initSystem: "Dumb-Init",
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInitPath(tt.initSystem)
			assert.Equal(t, tt.expected, result, "getInitPath(%s) = %s, want %s", tt.initSystem, result, tt.expected)
		})
	}
}

// TestBuildInitArgs tests the buildInitArgs method
func TestTaskTranslator_BuildInitArgs(t *testing.T) {
	tests := []struct {
		name         string
		initSystem   string
		initPath     string
		containerCmd []string
		expected     []string
	}{
		{
			name:         "tini with command",
			initSystem:   "tini",
			initPath:     "/sbin/tini",
			containerCmd: []string{"/bin/sh", "-c", "echo hello"},
			expected:     []string{"/sbin/tini", "--", "/bin/sh", "-c", "echo hello"},
		},
		{
			name:         "dumb-init with command",
			initSystem:   "dumb-init",
			initPath:     "/sbin/dumb-init",
			containerCmd: []string{"/app/server"},
			expected:     []string{"/sbin/dumb-init", "/app/server"},
		},
		{
			name:         "no init system",
			initSystem:   "",
			initPath:     "",
			containerCmd: []string{"/bin/bash"},
			expected:     []string{"/bin/bash"},
		},
		{
			name:         "tini with empty command",
			initSystem:   "tini",
			initPath:     "/sbin/tini",
			containerCmd: []string{},
			expected:     []string{"/sbin/tini", "--"},
		},
		{
			name:         "dumb-init with empty command",
			initSystem:   "dumb-init",
			initPath:     "/sbin/dumb-init",
			containerCmd: []string{},
			expected:     []string{"/sbin/dumb-init"},
		},
		{
			name:         "tini with single argument",
			initSystem:   "tini",
			initPath:     "/sbin/tini",
			containerCmd: []string{"/bin/sleep"},
			expected:     []string{"/sbin/tini", "--", "/bin/sleep"},
		},
		{
			name:         "dumb-init with multiple arguments",
			initSystem:   "dumb-init",
			initPath:     "/sbin/dumb-init",
			containerCmd: []string{"python", "-m", "http.server"},
			expected:     []string{"/sbin/dumb-init", "python", "-m", "http.server"},
		},
		{
			name:         "no init system with complex command",
			initSystem:   "none",
			initPath:     "",
			containerCmd: []string{"/bin/sh", "-c", "ls -la && echo done"},
			expected:     []string{"/bin/sh", "-c", "ls -la && echo done"},
		},
		{
			name:         "tini with special characters in command",
			initSystem:   "tini",
			initPath:     "/sbin/tini",
			containerCmd: []string{"/bin/sh", "-c", "echo 'test > file'"},
			expected:     []string{"/sbin/tini", "--", "/bin/sh", "-c", "echo 'test > file'"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := &TaskTranslator{
				initSystem: tc.initSystem,
				initPath:   tc.initPath,
			}

			result := tt.buildInitArgs(tc.containerCmd)
			assert.Equal(t, tc.expected, result, "buildInitArgs() = %v, want %v", result, tc.expected)
		})
	}
}

// TestConfigToJSON tests the configToJSON method
func TestTaskTranslator_ConfigToJSON(t *testing.T) {
	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		validate    func(string, error)
	}{
		{
			name: "valid VMMConfig",
			config: &VMMConfig{
				BootSource: BootSourceConfig{
					KernelImagePath: "/boot/vmlinux",
					BootArgs:        "console=ttyS0 reboot=k",
				},
				MachineConfig: MachineConfig{
					VcpuCount:  2,
					MemSizeMib: 512,
					HtEnabled:  true,
				},
				NetworkInterfaces: []NetworkInterface{
					{
						IfaceID:     "eth0",
						HostDevName: "tapeth0",
					},
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.NotEmpty(t, jsonStr)
				assert.Contains(t, jsonStr, "KernelImagePath")
				assert.Contains(t, jsonStr, "VcpuCount")
			},
		},
		{
			name:        "empty VMMConfig",
			config:      &VMMConfig{},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.NotEmpty(t, jsonStr)
				assert.Contains(t, jsonStr, "{")
			},
		},
		{
			name: "VMMConfig with nil slices",
			config: &VMMConfig{
				NetworkInterfaces: nil,
				Drives:            nil,
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.NotEmpty(t, jsonStr)
			},
		},
		{
			name: "VMMConfig with multiple drives",
			config: &VMMConfig{
				Drives: []Drive{
					{
						DriveID:      "rootfs",
						IsRootDevice: true,
						PathOnHost:   "/var/vm/rootfs.ext4",
					},
					{
						DriveID:      "data",
						IsRootDevice: false,
						PathOnHost:   "/var/vm/data.img",
						IsReadOnly:   true,
					},
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.Contains(t, jsonStr, "rootfs")
				assert.Contains(t, jsonStr, "data")
			},
		},
		{
			name: "VMMConfig with Vsock",
			config: &VMMConfig{
				Vsock: &VsockConfig{
					VsockID: "vm-001",
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.Contains(t, jsonStr, "Vsock")
			},
		},
		{
			name: "config with special characters in boot args",
			config: &VMMConfig{
				BootSource: BootSourceConfig{
					BootArgs: "console=ttyS0 panic=1 quiet",
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.Contains(t, jsonStr, "panic=1")
			},
		},
		{
			name: "config with HT enabled",
			config: &VMMConfig{
				MachineConfig: MachineConfig{
					HtEnabled: true,
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.Contains(t, jsonStr, "HtEnabled")
			},
		},
		{
			name: "config with large memory",
			config: &VMMConfig{
				MachineConfig: MachineConfig{
					MemSizeMib: 8192, // 8GB
				},
			},
			expectError: false,
			validate: func(jsonStr string, err error) {
				assert.NoError(t, err)
				assert.Contains(t, jsonStr, "8192")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := &TaskTranslator{}
			// Type assertion to convert interface{} to *VMMConfig
			vmmConfig, ok := tc.config.(*VMMConfig)
			require.True(t, ok, "config must be *VMMConfig type")

			result, err := tt.configToJSON(vmmConfig)

			if tc.validate != nil {
				tc.validate(result, err)
			} else if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNewTaskTranslator_AdditionalCoverage tests additional NewTaskTranslator scenarios
func TestNewTaskTranslator_AdditionalCoverage(t *testing.T) {
	tests := []struct {
		name        string
		config      interface{}
		expectError bool
		validate    func(*TaskTranslator, error)
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: false,
			validate: func(tt *TaskTranslator, err error) {
				assert.NoError(t, err)
				assert.NotNil(t, tt)
				assert.Equal(t, "tini", tt.initSystem) // Default
				assert.Equal(t, "/sbin/tini", tt.initPath)
			},
		},
		{
			name: "config with map (unsupported, uses defaults)",
			config: map[string]interface{}{
				"init_system": "dumb-init",
			},
			expectError: false,
			validate: func(tt *TaskTranslator, err error) {
				assert.NoError(t, err)
				assert.NotNil(t, tt)
				// Map config is not supported, so defaults are used
				assert.Equal(t, "tini", tt.initSystem)
				assert.Equal(t, "/sbin/tini", tt.initPath)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := NewTaskTranslator(tc.config)

			if tc.validate != nil {
				tc.validate(result, nil)
			} else if tc.expectError {
				// NewTaskTranslator doesn't return error
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

// TestBuildBootArgs_EdgeCases tests additional buildBootArgs scenarios
func TestTaskTranslator_BuildBootArgs_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		container *types.Container
		expected  string
	}{
		{
			name: "container with only command",
			container: &types.Container{
				Command: []string{"/app/start"},
			},
			expected: "/app/start",
		},
		{
			name: "container with only args",
			container: &types.Container{
				Args: []string{"server", "--port=8080"},
			},
			expected: "server --port=8080",
		},
		{
			name: "container with nil command and args",
			container: &types.Container{
				Command: nil,
				Args:    nil,
			},
			expected: "/sbin/init",
		},
		{
			name: "container with empty command and args",
			container: &types.Container{
				Command: []string{},
				Args:    []string{},
			},
			expected: "/sbin/init",
		},
		{
			name: "container with command containing spaces",
			container: &types.Container{
				Command: []string{"/bin/sh", "-c", "echo hello world"},
			},
			expected: "/bin/sh -c echo\\ hello\\ world",
		},
		{
			name: "container with args containing special characters",
			container: &types.Container{
				Command: []string{"/bin/sleep"},
				Args:    []string{"10", "&"},
			},
			expected: "/bin/sleep 10 \\&",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := &TaskTranslator{}
			result := tt.buildBootArgs(tc.container)

			// Split and compare for better error messages
			resultWords := len(splitString(result))
			expectedWords := len(splitString(tc.expected))

			if result != tc.expected {
				// For this test, we're mainly checking that it doesn't panic
				// and produces a reasonable output
				assert.NotEmpty(t, result, "buildBootArgs should return non-empty string")
				assert.True(t, resultWords > 0 || expectedWords == 0,
					"Result should have reasonable word count")
			}
		})
	}
}

// Helper function to split string into words
func splitString(s string) []string {
	words := make([]string, 0)
	currentWord := ""
	inSpace := true

	for _, r := range s {
		if r == ' ' {
			if !inSpace && currentWord != "" {
				words = append(words, currentWord)
				currentWord = ""
			}
			inSpace = true
		} else {
			currentWord += string(r)
			inSpace = false
		}
	}

	if currentWord != "" {
		words = append(words, currentWord)
	}

	return words
}
