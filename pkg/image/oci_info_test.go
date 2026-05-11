package image

import (
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOCIImageInfo_StructCreation tests basic struct creation
func TestOCIImageInfo_StructCreation(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected OCIImageInfo
	}{
		{
			name: "full config",
			info: &OCIImageInfo{
				Entrypoint:   []string{"/usr/bin/nginx"},
				Cmd:          []string{"-g", "daemon off;"},
				Env:          []string{"PATH=/usr/bin", "HOME=/root"},
				User:         "nginx",
				WorkDir:      "/var/www",
				StopSignal:   "SIGQUIT",
				OS:           "linux",
				Architecture: "amd64",
				ImageRef:     "nginx:latest",
			},
			expected: OCIImageInfo{
				Entrypoint:   []string{"/usr/bin/nginx"},
				Cmd:          []string{"-g", "daemon off;"},
				Env:          []string{"PATH=/usr/bin", "HOME=/root"},
				User:         "nginx",
				WorkDir:      "/var/www",
				StopSignal:   "SIGQUIT",
				OS:           "linux",
				Architecture: "amd64",
				ImageRef:     "nginx:latest",
			},
		},
		{
			name: "minimal config",
			info: &OCIImageInfo{
				StopSignal: DefaultStopSignal,
				ImageRef:   "alpine:latest",
			},
			expected: OCIImageInfo{
				StopSignal: DefaultStopSignal,
				ImageRef:   "alpine:latest",
			},
		},
		{
			name:     "nil info",
			info:     nil,
			expected: OCIImageInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.info == nil {
				assert.Nil(t, tt.info)
				return
			}
			assert.Equal(t, tt.expected.Entrypoint, tt.info.Entrypoint)
			assert.Equal(t, tt.expected.Cmd, tt.info.Cmd)
			assert.Equal(t, tt.expected.Env, tt.info.Env)
			assert.Equal(t, tt.expected.User, tt.info.User)
			assert.Equal(t, tt.expected.WorkDir, tt.info.WorkDir)
			assert.Equal(t, tt.expected.StopSignal, tt.info.StopSignal)
			assert.Equal(t, tt.expected.OS, tt.info.OS)
			assert.Equal(t, tt.expected.Architecture, tt.info.Architecture)
			assert.Equal(t, tt.expected.ImageRef, tt.info.ImageRef)
		})
	}
}

// TestOCIImageInfo_DefaultStopSignal tests default StopSignal value
func TestOCIImageInfo_DefaultStopSignal(t *testing.T) {
	// Test default constant
	assert.Equal(t, "SIGTERM", DefaultStopSignal)

	// Test struct with default
	info := &OCIImageInfo{
		StopSignal: DefaultStopSignal,
	}
	assert.Equal(t, "SIGTERM", info.StopSignal)
}

// TestFullCommand tests the FullCommand helper
func TestFullCommand(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected []string
	}{
		{
			name: "entrypoint only",
			info: &OCIImageInfo{
				Entrypoint: []string{"/usr/bin/app"},
			},
			expected: []string{"/usr/bin/app"},
		},
		{
			name: "cmd only",
			info: &OCIImageInfo{
				Cmd: []string{"/bin/sh", "-c", "echo hello"},
			},
			expected: []string{"/bin/sh", "-c", "echo hello"},
		},
		{
			name: "both entrypoint and cmd",
			info: &OCIImageInfo{
				Entrypoint: []string{"/usr/bin/nginx"},
				Cmd:        []string{"-g", "daemon off;"},
			},
			expected: []string{"/usr/bin/nginx", "-g", "daemon off;"},
		},
		{
			name: "neither entrypoint nor cmd",
			info: &OCIImageInfo{
				Env: []string{"PATH=/usr/bin"},
			},
			expected: []string{"/bin/sh"},
		},
		{
			name:     "nil info",
			info:     nil,
			expected: []string{"/bin/sh"},
		},
		{
			name: "empty entrypoint and cmd",
			info: &OCIImageInfo{
				Entrypoint: []string{},
				Cmd:        []string{},
			},
			expected: []string{"/bin/sh"},
		},
		{
			name: "entrypoint with multiple args",
			info: &OCIImageInfo{
				Entrypoint: []string{"python", "-u", "app.py"},
			},
			expected: []string{"python", "-u", "app.py"},
		},
		{
			name: "shell form cmd",
			info: &OCIImageInfo{
				Cmd: []string{"sh", "-c", "nginx -g 'daemon off;'"},
			},
			expected: []string{"sh", "-c", "nginx -g 'daemon off;'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FullCommand(tt.info)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFullCommand_NilSafety ensures FullCommand handles nil gracefully
func TestFullCommand_NilSafety(t *testing.T) {
	// Should never panic
	result := FullCommand(nil)
	assert.NotNil(t, result)
	assert.Equal(t, []string{"/bin/sh"}, result)
}

// TestParseOCIImageConfig tests parsing from go-containerregistry ConfigFile
func TestParseOCIImageConfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *v1.ConfigFile
		imageRef string
		expected *OCIImageInfo
	}{
		{
			name: "full config file",
			cfg: &v1.ConfigFile{
				Config: v1.Config{
					Entrypoint: []string{"/docker-entrypoint.sh"},
					Cmd:        []string{"nginx", "-g", "daemon off;"},
					Env:        []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NGINX_VERSION=1.25.0"},
					User:       "nginx",
					WorkingDir: "/var/cache/nginx",
					StopSignal: "SIGQUIT",
				},
				OS:           "linux",
				Architecture: "amd64",
			},
			imageRef: "nginx:alpine",
			expected: &OCIImageInfo{
				Entrypoint:   []string{"/docker-entrypoint.sh"},
				Cmd:          []string{"nginx", "-g", "daemon off;"},
				Env:          []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NGINX_VERSION=1.25.0"},
				User:         "nginx",
				WorkDir:      "/var/cache/nginx",
				StopSignal:   "SIGQUIT",
				OS:           "linux",
				Architecture: "amd64",
				ImageRef:     "nginx:alpine",
			},
		},
		{
			name: "config without StopSignal",
			cfg: &v1.ConfigFile{
				Config: v1.Config{
					Entrypoint: []string{"/app"},
					Cmd:        []string{"--port", "8080"},
				},
				OS:           "linux",
				Architecture: "arm64",
			},
			imageRef: "myapp:v1",
			expected: &OCIImageInfo{
				Entrypoint:   []string{"/app"},
				Cmd:          []string{"--port", "8080"},
				StopSignal:   DefaultStopSignal, // Should default to SIGTERM
				OS:           "linux",
				Architecture: "arm64",
				ImageRef:     "myapp:v1",
			},
		},
		{
			name:     "nil config file",
			cfg:      nil,
			imageRef: "alpine:latest",
			expected: &OCIImageInfo{
				StopSignal: DefaultStopSignal,
				ImageRef:   "alpine:latest",
			},
		},
		{
			name: "empty config",
			cfg: &v1.ConfigFile{
				Config: v1.Config{},
				OS:     "linux",
			},
			imageRef: "empty:latest",
			expected: &OCIImageInfo{
				StopSignal: DefaultStopSignal,
				OS:         "linux",
				ImageRef:   "empty:latest",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseOCIImageConfig(tt.cfg, tt.imageRef)
			require.NotNil(t, result)

			assert.Equal(t, tt.expected.Entrypoint, result.Entrypoint)
			assert.Equal(t, tt.expected.Cmd, result.Cmd)
			assert.Equal(t, tt.expected.Env, result.Env)
			assert.Equal(t, tt.expected.User, result.User)
			assert.Equal(t, tt.expected.WorkDir, result.WorkDir)
			assert.Equal(t, tt.expected.StopSignal, result.StopSignal)
			assert.Equal(t, tt.expected.OS, result.OS)
			assert.Equal(t, tt.expected.Architecture, result.Architecture)
			assert.Equal(t, tt.expected.ImageRef, result.ImageRef)
		})
	}
}

// TestOCIImageInfo_HasEntrypoint tests HasEntrypoint method
func TestOCIImageInfo_HasEntrypoint(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected bool
	}{
		{
			name: "has entrypoint",
			info: &OCIImageInfo{
				Entrypoint: []string{"/app"},
			},
			expected: true,
		},
		{
			name: "no entrypoint",
			info: &OCIImageInfo{
				Cmd: []string{"/bin/sh"},
			},
			expected: false,
		},
		{
			name: "empty entrypoint",
			info: &OCIImageInfo{
				Entrypoint: []string{},
			},
			expected: false,
		},
		{
			name:     "nil info",
			info:     nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.info.HasEntrypoint()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestOCIImageInfo_HasCmd tests HasCmd method
func TestOCIImageInfo_HasCmd(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected bool
	}{
		{
			name: "has cmd",
			info: &OCIImageInfo{
				Cmd: []string{"/bin/bash"},
			},
			expected: true,
		},
		{
			name: "no cmd",
			info: &OCIImageInfo{
				Entrypoint: []string{"/app"},
			},
			expected: false,
		},
		{
			name: "empty cmd",
			info: &OCIImageInfo{
				Cmd: []string{},
			},
			expected: false,
		},
		{
			name:     "nil info",
			info:     nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.info.HasCmd()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestOCIImageInfo_IsEmpty tests IsEmpty method
func TestOCIImageInfo_IsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected bool
	}{
		{
			name:     "empty - nil",
			info:     nil,
			expected: true,
		},
		{
			name: "empty - no config",
			info: &OCIImageInfo{
				StopSignal: DefaultStopSignal,
				ImageRef:   "test:latest",
			},
			expected: true,
		},
		{
			name: "not empty - has entrypoint",
			info: &OCIImageInfo{
				Entrypoint: []string{"/app"},
			},
			expected: false,
		},
		{
			name: "not empty - has cmd",
			info: &OCIImageInfo{
				Cmd: []string{"/bin/sh"},
			},
			expected: false,
		},
		{
			name: "not empty - has env",
			info: &OCIImageInfo{
				Env: []string{"PATH=/usr/bin"},
			},
			expected: false,
		},
		{
			name: "not empty - has all",
			info: &OCIImageInfo{
				Entrypoint: []string{"/app"},
				Cmd:        []string{"--help"},
				Env:        []string{"DEBUG=1"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.info.IsEmpty()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFullCommand_RealWorldImages tests common image patterns
func TestFullCommand_RealWorldImages(t *testing.T) {
	tests := []struct {
		name     string
		info     *OCIImageInfo
		expected []string
	}{
		{
			name: "nginx official image",
			info: &OCIImageInfo{
				Entrypoint: []string{"/docker-entrypoint.sh"},
				Cmd:        []string{"nginx", "-g", "daemon off;"},
			},
			expected: []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"},
		},
		{
			name: "alpine base image",
			info: &OCIImageInfo{
				Cmd: []string{"/bin/sh"},
			},
			expected: []string{"/bin/sh"},
		},
		{
			name: "debian/ubuntu base",
			info: &OCIImageInfo{
				Cmd: []string{"bash"},
			},
			expected: []string{"bash"},
		},
		{
			name: "python official image",
			info: &OCIImageInfo{
				Entrypoint: []string{"python3"},
				Cmd:        []string{},
			},
			expected: []string{"python3"},
		},
		{
			name: "custom app with entrypoint script",
			info: &OCIImageInfo{
				Entrypoint: []string{"/entrypoint.sh"},
				Cmd:        []string{"/app/server"},
			},
			expected: []string{"/entrypoint.sh", "/app/server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FullCommand(tt.info)
			assert.Equal(t, tt.expected, result)
		})
	}
}
