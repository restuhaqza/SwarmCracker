package testinfra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHelper provides utility functions for infrastructure testing
type TestHelper struct {
	t            *testing.T
	projectRoot  string
	cleanupFuncs []func() error
}

// NewTestHelper creates a new test helper
func NewTestHelper(t *testing.T) *TestHelper {
	return &TestHelper{
		t:            t,
		cleanupFuncs: make([]func() error, 0),
	}
}

// GetProjectRoot finds and caches the project root directory
func (th *TestHelper) GetProjectRoot() string {
	if th.projectRoot != "" {
		return th.projectRoot
	}

	// Start from current directory
	dir, err := os.Getwd()
	require.NoError(th.t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			th.projectRoot = dir
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			th.t.Fatal("Could not find project root (go.mod not found)")
		}
		dir = parent
	}
}

// CreateTempDir creates a temporary directory with automatic cleanup
func (th *TestHelper) CreateTempDir(pattern string) string {
	dir, err := os.MkdirTemp("", pattern)
	require.NoError(th.t, err)

	th.AddCleanup(func() error {
		return os.RemoveAll(dir)
	})

	return dir
}

// AddCleanup adds a cleanup function to be called on test completion
func (th *TestHelper) AddCleanup(fn func() error) {
	th.cleanupFuncs = append(th.cleanupFuncs, fn)
}

// Cleanup runs all registered cleanup functions
func (th *TestHelper) Cleanup() {
	for i := len(th.cleanupFuncs) - 1; i >= 0; i-- {
		if err := th.cleanupFuncs[i](); err != nil {
			th.t.Logf("Cleanup error: %v", err)
		}
	}
}

// RunCommand runs a command and returns its output
func (th *TestHelper) RunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// RunCommandTimeout runs a command with a timeout
func (th *TestHelper) RunCommandTimeout(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// CommandExists checks if a command exists in PATH
func (th *TestHelper) CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// GetGoVersion returns the Go version
func (th *TestHelper) GetGoVersion() string {
	output, err := th.RunCommand("go", "version")
	if err != nil {
		return "unknown"
	}

	// Parse version string
	// Output format: "go version go1.21.0 linux/amd64"
	parts := strings.Split(output, " ")
	if len(parts) >= 3 {
		return parts[2]
	}

	return "unknown"
}

// GetArch returns the system architecture
func (th *TestHelper) GetArch() string {
	return runtime.GOARCH
}

// GetOS returns the operating system
func (th *TestHelper) GetOS() string {
	return runtime.GOOS
}

// CheckFileExists checks if a file exists
func (th *TestHelper) CheckFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CheckDirExists checks if a directory exists
func (th *TestHelper) CheckDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// GetFileSize returns the size of a file
func (th *TestHelper) GetFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ReadFile reads a file and returns its contents
func (th *TestHelper) ReadFile(path string) string {
	data, err := os.ReadFile(path)
	require.NoError(th.t, err)
	return string(data)
}

// WriteFile writes data to a file
func (th *TestHelper) WriteFile(path, data string) {
	err := os.WriteFile(path, []byte(data), 0644)
	require.NoError(th.t, err)
}

// MkdirAll creates a directory with all parent directories
func (th *TestHelper) MkdirAll(path string) {
	err := os.MkdirAll(path, 0755)
	require.NoError(th.t, err)
}

// Chdir changes the current directory and returns a function to restore it
func (th *TestHelper) Chdir(dir string) func() {
	original, err := os.Getwd()
	require.NoError(th.t, err)

	err = os.Chdir(dir)
	require.NoError(th.t, err)

	return func() {
		_ = os.Chdir(original)
	}
}

// WaitFor waits for a condition to be true
func (th *TestHelper) WaitFor(condition func() bool, timeout time.Duration, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			require.Fail(th.t, fmt.Sprintf("%s: timeout after %v", message, timeout))
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

// WaitForFile waits for a file to exist
func (th *TestHelper) WaitForFile(path string, timeout time.Duration) {
	th.WaitFor(func() bool {
		return th.CheckFileExists(path)
	}, timeout, fmt.Sprintf("waiting for file: %s", path))
}

// Retry retries a function until it succeeds or times out
func (th *TestHelper) Retry(fn func() error, maxAttempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			time.Sleep(delay)
		}
	}
	return fmt.Errorf("retry failed after %d attempts: %w", maxAttempts, lastErr)
}

// KillProcessByName kills all processes with the given name
func (th *TestHelper) KillProcessByName(name string) error {
	// Find processes
	cmd := exec.Command("pgrep", "-x", name)
	output, err := cmd.Output()
	if err != nil {
		// No processes found
		return nil
	}

	// Kill them
	pids := strings.Fields(string(output))
	for _, pid := range pids {
		cmd := exec.Command("kill", pid)
		_ = cmd.Run()
	}

	return nil
}

// GetEnv gets an environment variable
func (th *TestHelper) GetEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// SetEnv sets an environment variable and returns a function to restore it
func (th *TestHelper) SetEnv(key, value string) func() {
	original := os.Getenv(key)
	os.Setenv(key, value)

	return func() {
		if original == "" {
			os.Unsetenv(key)
		} else {
			os.Setenv(key, original)
		}
	}
}

// GetFreePort returns a free TCP port
func (th *TestHelper) GetFreePort() int {
	// This is a simplified version
	// In real usage, you'd bind to port 0 and let the OS assign one
	return 0
}

// IsRunningInCI checks if tests are running in CI
func (th *TestHelper) IsRunningInCI() bool {
	return os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != ""
}

// SkipIfShort skips the test if -short flag is set
func (th *TestHelper) SkipIfShort(reason string) {
	if testing.Short() {
		th.t.Skipf("Skipping in short mode: %s", reason)
	}
}

// RequireEnv requires an environment variable to be set
func (th *TestHelper) RequireEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		th.t.Fatalf("Required environment variable not set: %s", key)
	}
	return value
}
