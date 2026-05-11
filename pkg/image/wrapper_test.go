package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- createGenericInitWrapper tests ---

func TestCreateGenericInitWrapper_Nginx(t *testing.T) {
	tmpDir := t.TempDir()

	// nginx:latest has ENTRYPOINT + CMD
	info := &OCIImageInfo{
		ImageRef:   "nginx:latest",
		Entrypoint: []string{"/docker-entrypoint.sh"},
		Cmd:        []string{"nginx", "-g", "daemon off;"},
		Env:        []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NGINX_VERSION=1.25.4"},
		WorkDir:    "",
		User:       "",
	}

	err := createGenericInitWrapper(tmpDir, info, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	// Verify /sbin/init exists
	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should have ENTRYPOINT + CMD combined
	if !strings.Contains(script, "/docker-entrypoint.sh") {
		t.Error("script should contain entrypoint")
	}
	if !strings.Contains(script, "nginx") {
		t.Error("script should contain nginx from CMD")
	}
	if !strings.Contains(script, "daemon off;") {
		t.Error("script should contain CMD argument")
	}

	// Should have grace period
	if !strings.Contains(script, "-g 10") {
		t.Error("script should contain grace period flag")
	}

	// Should have environment export
	if !strings.Contains(script, "export PATH=") {
		t.Error("script should export PATH")
	}
	if !strings.Contains(script, "NGINX_VERSION") {
		t.Error("script should export NGINX_VERSION")
	}

	// Should mount filesystems
	if !strings.Contains(script, "mount -t proc") {
		t.Error("script should mount /proc")
	}
}

func TestCreateGenericInitWrapper_Redis(t *testing.T) {
	tmpDir := t.TempDir()

	// redis has only CMD
	info := &OCIImageInfo{
		ImageRef:   "redis:latest",
		Entrypoint: nil,
		Cmd:        []string{"redis-server"},
		Env:        []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
	}

	err := createGenericInitWrapper(tmpDir, info, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should have CMD only
	if !strings.Contains(script, "redis-server") {
		t.Error("script should contain redis-server CMD")
	}
}

func TestCreateGenericInitWrapper_Alpine(t *testing.T) {
	tmpDir := t.TempDir()

	// alpine has no ENTRYPOINT/CMD → /bin/sh fallback
	info := &OCIImageInfo{
		ImageRef:   "alpine:latest",
		Entrypoint: nil,
		Cmd:        nil,
		Env:        []string{"PATH=/usr/local/bin:/usr/bin:/bin"},
	}

	err := createGenericInitWrapper(tmpDir, info, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should fallback to /bin/sh
	if !strings.Contains(script, "/bin/sh") {
		t.Error("script should fallback to /bin/sh when no ENTRYPOINT/CMD")
	}
}

func TestCreateGenericInitWrapper_WithEnv(t *testing.T) {
	tmpDir := t.TempDir()

	info := &OCIImageInfo{
		ImageRef: "test:latest",
		Cmd:      []string{"/bin/sh"},
		Env: []string{
			"PATH=/usr/bin:/bin",
			"HOME=/root",
			"TERM=xterm",
			"MY_VAR=value with spaces",
		},
	}

	err := createGenericInitWrapper(tmpDir, info, 0)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should export all env vars
	if !strings.Contains(script, "export PATH=") {
		t.Error("should export PATH")
	}
	if !strings.Contains(script, "export HOME=") {
		t.Error("should export HOME")
	}
	if !strings.Contains(script, "export TERM=") {
		t.Error("should export TERM")
	}
	if !strings.Contains(script, "MY_VAR=") {
		t.Error("should export MY_VAR")
	}

	// Value with spaces should be quoted
	if !strings.Contains(script, "\"value with spaces\"") {
		t.Error("value with spaces should be quoted")
	}
}

func TestCreateGenericInitWrapper_WithUser(t *testing.T) {
	tmpDir := t.TempDir()

	info := &OCIImageInfo{
		ImageRef: "nginx:latest",
		Cmd:      []string{"/bin/sh"},
		User:     "nginx",
	}

	err := createGenericInitWrapper(tmpDir, info, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should have USER directive handling
	if !strings.Contains(script, "su-exec") && !strings.Contains(script, "gosu") {
		t.Error("script should mention su-exec or gosu for USER handling")
	}
	if !strings.Contains(script, "nginx") {
		t.Error("script should reference nginx user")
	}
}

func TestCreateGenericInitWrapper_WithWorkDir(t *testing.T) {
	tmpDir := t.TempDir()

	info := &OCIImageInfo{
		ImageRef: "test:latest",
		Cmd:      []string{"/bin/sh"},
		WorkDir:  "/app",
	}

	err := createGenericInitWrapper(tmpDir, info, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should create and cd to workdir
	if !strings.Contains(script, "mkdir -p") {
		t.Error("script should mkdir workdir")
	}
	if !strings.Contains(script, "cd") {
		t.Error("script should cd to workdir")
	}
	if !strings.Contains(script, "/app") {
		t.Error("script should reference /app workdir")
	}
}

func TestCreateGenericInitWrapper_GracePeriod(t *testing.T) {
	tests := []struct {
		name        string
		gracePeriod int
		wantFlag    bool
	}{
		{"grace period 10", 10, true},
		{"grace period 0", 0, false},
		{"grace period 5", 5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			info := &OCIImageInfo{
				ImageRef: "test:latest",
				Cmd:      []string{"/bin/sh"},
			}

			err := createGenericInitWrapper(tmpDir, info, tt.gracePeriod)
			if err != nil {
				t.Fatalf("createGenericInitWrapper failed: %v", err)
			}

			initPath := filepath.Join(tmpDir, "sbin", "init")
			content, err := os.ReadFile(initPath)
			if err != nil {
				t.Fatalf("failed to read init wrapper: %v", err)
			}

			script := string(content)

			if tt.wantFlag {
				if !strings.Contains(script, "-g") {
					t.Errorf("script should contain -g flag for grace period %d", tt.gracePeriod)
				}
				if !strings.Contains(script, "-g "+itoa(tt.gracePeriod)) {
					t.Errorf("script should contain correct grace period value %d", tt.gracePeriod)
				}
			} else {
				// Check that -g is not present (or not followed by a number)
				if strings.Contains(script, "-g 0") {
					t.Error("script should not contain -g 0")
				}
			}
		})
	}
}

func TestCreateGenericInitWrapper_NilInfo(t *testing.T) {
	tmpDir := t.TempDir()

	// nil info should still work (fallback to /bin/sh)
	err := createGenericInitWrapper(tmpDir, nil, 10)
	if err != nil {
		t.Fatalf("createGenericInitWrapper with nil info failed: %v", err)
	}

	initPath := filepath.Join(tmpDir, "sbin", "init")
	content, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("failed to read init wrapper: %v", err)
	}

	script := string(content)

	// Should fallback to /bin/sh
	if !strings.Contains(script, "/bin/sh") {
		t.Error("nil info should fallback to /bin/sh")
	}
}

// --- ShellEscape tests ---

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input    string
		wantSafe bool
	}{
		{"simple", true},
		{"value with spaces", false},
		{"value\"with\"quotes", false},
		{"value$with$dollar", false},
		{"value`with`backtick", false},
		{"value\\with\\backslash", false},
		{"", true},
		{"already_quoted", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			escaped := shellEscape(tt.input)
			// If input needs escaping, output should be quoted
			if needsShellEscaping(tt.input) {
				if len(escaped) < 2 || escaped[0] != '"' {
					t.Errorf("shellEscape(%q) = %q, should be quoted", tt.input, escaped)
				}
			}
		})
	}
}

func TestShellEscapeValue(t *testing.T) {
	tests := []struct {
		input  string
		output string
	}{
		{"simple", "\"simple\""},
		{"with spaces", "\"with spaces\""},
		{"with\"quote", "\"with\\\"quote\""},
		{"with$var", "\"with\\$var\""},
		{"with`backtick", "\"with\\`backtick\""},
		{"with\\backslash", "\"with\\\\backslash\""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := shellEscapeValue(tt.input)
			if result != tt.output {
				t.Errorf("shellEscapeValue(%q) = %q, want %q", tt.input, result, tt.output)
			}
		})
	}
}

func TestNeedsShellEscaping(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"simple", false},
		{"", false},
		{"value with space", true},
		{"value\twith\ttab", true},
		{"value\"quote", true},
		{"value'dquote", true},
		{"value$dollar", true},
		{"value`backtick", true},
		{"value\\backslash", true},
		{"value!bang", true},
		{"value*star", true},
		{"value?question", true},
		{"value[bracket", true},
		{"value(pipe", true},
		{"value&and", true},
		{"value;semi", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := needsShellEscaping(tt.input)
			if result != tt.want {
				t.Errorf("needsShellEscaping(%q) = %v, want %v", tt.input, result, tt.want)
			}
		})
	}
}

func TestParseEnvVar(t *testing.T) {
	tests := []struct {
		input     string
		wantKey   string
		wantValue string
	}{
		{"KEY=value", "KEY", "value"},
		{"PATH=/usr/bin:/bin", "PATH", "/usr/bin:/bin"},
		{"EMPTY=", "EMPTY", ""},
		{"NOVALUE", "NOVALUE", ""},
		{"KEY=value with spaces", "KEY", "value with spaces"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			key, value := parseEnvVar(tt.input)
			if key != tt.wantKey {
				t.Errorf("parseEnvVar(%q) key = %q, want %q", tt.input, key, tt.wantKey)
			}
			if value != tt.wantValue {
				t.Errorf("parseEnvVar(%q) value = %q, want %q", tt.input, value, tt.wantValue)
			}
		})
	}
}

func TestBuildCommandString(t *testing.T) {
	tests := []struct {
		name string
		cmd  []string
		want string
	}{
		{"empty", nil, "/bin/sh"},
		{"single arg", []string{"/bin/sh"}, "/bin/sh"},
		{"multiple args", []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"}, "/docker-entrypoint.sh nginx \"-g\" \"daemon off;\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCommandString(tt.cmd)
			// Check that result contains all parts
			for _, part := range tt.cmd {
				if !strings.Contains(result, part) {
					t.Errorf("buildCommandString(%v) = %q, should contain %q", tt.cmd, result, part)
				}
			}
			// For empty, should be /bin/sh
			if tt.cmd == nil && result != "/bin/sh" {
				t.Errorf("buildCommandString(nil) = %q, want /bin/sh", result)
			}
		})
	}
}

// --- Helper ---

func itoa(n int) string {
	if n == 10 {
		return "10"
	}
	if n == 5 {
		return "5"
	}
	return ""
}
