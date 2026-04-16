package image

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetInitArgs tests init argument generation
func TestGetInitArgs(t *testing.T) {
	tests := []struct {
		name         string
		initType     InitSystemType
		containerArgs []string
		expected     []string
	}{
		{
			name:         "tini with args",
			initType:     InitSystemTini,
			containerArgs: []string{"nginx", "-g", "daemon off;"},
			expected:     []string{"/sbin/tini", "--", "nginx", "-g", "daemon off;"},
		},
		{
			name:         "dumb-init with args",
			initType:     InitSystemDumbInit,
			containerArgs: []string{"python", "app.py"},
			expected:     []string{"/sbin/dumb-init", "python", "app.py"},
		},
		{
			name:         "none returns original",
			initType:     InitSystemNone,
			containerArgs: []string{"sh", "-c", "echo test"},
			expected:     []string{"sh", "-c", "echo test"},
		},
		{
			name:         "empty container args with tini",
			initType:     InitSystemTini,
			containerArgs: []string{},
			expected:     []string{"/sbin/tini", "--"},
		},
		{
			name:         "empty container args with dumb-init",
			initType:     InitSystemDumbInit,
			containerArgs: []string{},
			expected:     []string{"/sbin/dumb-init"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injector := NewInitInjector(&InitSystemConfig{
				Type: tt.initType,
			})

			result := injector.GetInitArgs(tt.containerArgs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestGetGracePeriod tests grace period getter
func TestGetGracePeriod(t *testing.T) {
	tests := []struct {
		name     string
		config   *InitSystemConfig
		expected int
	}{
		{
			name: "configured grace period",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 30,
			},
			expected: 30,
		},
		{
			name: "zero grace period gets default when enabled",
			config: &InitSystemConfig{
				Type:           InitSystemTini,
				GracePeriodSec: 0,
			},
			expected: 10, // Default from NewInitInjector
		},
		{
			name: "nil config gets defaults",
			config: nil,
			expected: 10, // Default
		},
		{
			name: "none type with zero grace period",
			config: &InitSystemConfig{
				Type:           InitSystemNone,
				GracePeriodSec: 0,
			},
			expected: 0, // No grace period for none
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injector := NewInitInjector(tt.config)
			result := injector.GetGracePeriod()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestIsEnabled tests init system enabled check
func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name           string
		initType       InitSystemType
		expectedEnabled bool
	}{
		{
			name:           "tini enabled",
			initType:       InitSystemTini,
			expectedEnabled: true,
		},
		{
			name:           "dumb-init enabled",
			initType:       InitSystemDumbInit,
			expectedEnabled: true,
		},
		{
			name:           "none disabled",
			initType:       InitSystemNone,
			expectedEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injector := NewInitInjector(&InitSystemConfig{
				Type: tt.initType,
			})
			result := injector.IsEnabled()
			assert.Equal(t, tt.expectedEnabled, result)
		})
	}
}

// TestNewInitInjector_Defaults tests default configuration
func TestNewInitInjector_Defaults(t *testing.T) {
	injector := NewInitInjector(nil)
	require.NotNil(t, injector)

	// Should have default tini config
	assert.Equal(t, InitSystemTini, injector.config.Type)
	assert.Equal(t, 10, injector.config.GracePeriodSec)
	assert.True(t, injector.IsEnabled())
}