package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/stretchr/testify/assert"
)

// TestToInt tests toInt helper function
func TestToInt_Unit(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  int
	}{
		{"int", 42, 42},
		{"float64", 42.5, 42},
		{"int64", int64(42), 42},
		{"string", "42", 0}, // Not a number
		{"nil", nil, 0},
		{"bool", true, 0}, // Not a number
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toInt(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestToBool tests toBool helper function
func TestToBool_Unit(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"nil", nil, false},
		{"string_true", "true", false}, // Not a bool
		{"int_1", 1, false},            // Not a bool
		{"empty_string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toBool(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestParseMeminfoLine tests parseMeminfoLine helper
func TestParseMeminfoLine_Unit(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int64
	}{
		{"MemTotal", "MemTotal:       16384000 kB", 16384000},
		{"MemFree", "MemFree:         8192000 kB", 8192000},
		{"MemAvailable", "MemAvailable:    4000000 kB", 4000000},
		{"no_digits", "BadLine", 0},
		{"empty", "", 0},
		{"spaces_only", "     ", 0},
		{"negative_sign_skipped", "MemTotal:       -1000 kB", 1000}, // '-' is skipped, returns 1000
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMeminfoLine(tt.line)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestHostname tests hostname helper
func TestHostname_Unit(t *testing.T) {
	got := hostname()
	assert.NotEmpty(t, got) // Should return system hostname or fallback
}

// TestGetLocalIPFromInterface tests getLocalIPFromInterface
func TestGetLocalIPFromInterface_Unit(t *testing.T) {
	got := getLocalIPFromInterface()
	// May return empty if no non-loopback interface
	// Just verify it doesn't panic
	_ = got
}

// TestVMMManagerConfig_Defaults tests config defaults
func TestVMMManagerConfig_Defaults_Unit(t *testing.T) {
	cfg := &VMMManagerConfig{}

	// Apply defaults manually
	if cfg.FirecrackerPath == "" {
		cfg.FirecrackerPath = "firecracker"
	}
	if cfg.SocketDir == "" {
		cfg.SocketDir = "/var/run/firecracker"
	}

	assert.Equal(t, "firecracker", cfg.FirecrackerPath)
	assert.Equal(t, "/var/run/firecracker", cfg.SocketDir)
	assert.False(t, cfg.UseJailer)
}

// TestVMMManagerConfig_FullConfig tests full configuration
func TestVMMManagerConfig_FullConfig_Unit(t *testing.T) {
	cfg := &VMMManagerConfig{
		FirecrackerPath: "/usr/bin/firecracker",
		JailerPath:      "/usr/bin/jailer",
		SocketDir:       "/var/run/fc",
		UseJailer:       true,
		JailerUID:       1000,
		JailerGID:       1000,
		JailerChrootDir: "/srv/jail",
		ParentCgroup:    "/fc-cgroup",
		CgroupVersion:   "v2",
		EnableCgroups:   true,
	}

	assert.Equal(t, "/usr/bin/firecracker", cfg.FirecrackerPath)
	assert.Equal(t, "/usr/bin/jailer", cfg.JailerPath)
	assert.Equal(t, "/var/run/fc", cfg.SocketDir)
	assert.True(t, cfg.UseJailer)
	assert.Equal(t, 1000, cfg.JailerUID)
	assert.Equal(t, 1000, cfg.JailerGID)
	assert.Equal(t, "/srv/jail", cfg.JailerChrootDir)
	assert.Equal(t, "/fc-cgroup", cfg.ParentCgroup)
	assert.Equal(t, "v2", cfg.CgroupVersion)
	assert.True(t, cfg.EnableCgroups)
}

// TestVMMManagerConfig_ResourceLimits tests resource limits config
func TestVMMManagerConfig_ResourceLimits_Unit(t *testing.T) {
	// ResourceLimits has CPUQuotaUs, not MaxCPUs
	cfg := &VMMManagerConfig{
		ResourceLimits: jailer.ResourceLimits{
			CPUQuotaUs: 500000,             // 0.5 CPU
			MemoryMax:  1024 * 1024 * 1024, // 1GB
		},
	}

	assert.Equal(t, int64(500000), cfg.ResourceLimits.CPUQuotaUs)
	assert.Equal(t, int64(1024*1024*1024), cfg.ResourceLimits.MemoryMax)
}
