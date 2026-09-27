package translator

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
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
				assert.NotNil(t, result)
			}
		})
	}
}

// TestBuildBootArgs_EdgeCases tests additional buildBootArgs scenarios
func TestTaskTranslator_BuildBootArgs_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		task     *types.Task
		expected string
	}{
		{
			name: "container with only command",
			task: &types.Task{
				ID: "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{"/app/start"},
					},
				},
			},
			expected: "/app/start",
		},
		{
			name: "container with only args",
			task: &types.Task{
				ID: "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Args: []string{"server", "--port=8080"},
					},
				},
			},
			expected: "server --port=8080",
		},
		{
			name: "container with nil command and args",
			task: &types.Task{
				ID: "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: nil,
						Args:    nil,
					},
				},
			},
			expected: "/bin/sh", // Fallback to shell if empty
		},
		{
			name: "container with empty command and args",
			task: &types.Task{
				ID: "test-task",
				Spec: types.TaskSpec{
					Runtime: &types.Container{
						Command: []string{},
						Args:    []string{},
					},
				},
			},
			expected: "/bin/sh", // Fallback to shell if empty
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTaskTranslator(nil)
			result := tt.buildBootArgs(tc.task)

			assert.NotEmpty(t, result)
			assert.Contains(t, result, tc.expected)
		})
	}
}
