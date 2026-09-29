package cni

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultCommandExecutor_Execute tests basic command execution
func TestDefaultCommandExecutor_Execute(t *testing.T) {
	// echo is not usable: Execute passes no args. Use an argv-free script that
	// writes a fixed string.
	script := filepath.Join(t.TempDir(), "say.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf test\n"), 0o755))

	stdout, stderr, err := NewDefaultCommandExecutor().Execute(context.Background(), script, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "test", string(stdout))
	assert.Empty(t, stderr)
}

// TestDefaultCommandExecutor_Execute_WithContext tests context cancellation
func TestDefaultCommandExecutor_Execute_WithContext(t *testing.T) {
	script := filepath.Join(t.TempDir(), "sleep.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755))

	executor := &DefaultCommandExecutor{Timeout: 50 * time.Millisecond}
	_, _, err := executor.Execute(context.Background(), script, nil, nil)
	require.Error(t, err)
}

// TestDefaultCommandExecutor_Execute_Stdin tests stdin handling
func TestDefaultCommandExecutor_Execute_Stdin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exec test in short mode")
	}

	executor := NewDefaultCommandExecutor()
	ctx := context.Background()

	// Execute cat with stdin
	stdout, stderr, err := executor.Execute(ctx, "cat", []byte("hello from stdin"), nil)
	require.NoError(t, err)
	assert.Equal(t, "hello from stdin", string(stdout))
	assert.Empty(t, stderr)
}

// TestDefaultCommandExecutor_Execute_Env tests environment variable handling
func TestDefaultCommandExecutor_Execute_Env(t *testing.T) {
	stdout, _, err := NewDefaultCommandExecutor().Execute(context.Background(), "env", nil, []string{"MY_VAR=test123"})
	require.NoError(t, err)
	assert.Contains(t, string(stdout), "MY_VAR=test123")
}

// TestDefaultCommandExecutor_Execute_NonexistentCommand tests error handling
func TestDefaultCommandExecutor_Execute_NonexistentCommand(t *testing.T) {
	executor := NewDefaultCommandExecutor()
	ctx := context.Background()

	_, _, err := executor.Execute(ctx, "/nonexistent/command", nil, nil)
	require.Error(t, err)
}

// TestDefaultCommandExecutor_Execute_CommandFailure tests command that exits with error
func TestDefaultCommandExecutor_Execute_CommandFailure(t *testing.T) {
	executor := NewDefaultCommandExecutor()
	ctx := context.Background()

	// Execute false command (always exits with 1)
	_, stderr, err := executor.Execute(ctx, "false", nil, nil)
	require.Error(t, err)
	_ = stderr // stderr may be empty for false
}

// TestCommandExecutorFunc tests function adapter
func TestCommandExecutorFunc(t *testing.T) {
	expectedStdout := []byte("mock stdout")
	expectedStderr := []byte("mock stderr")
	expectedErr := errors.New("mock error")

	executor := CommandExecutorFunc(func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
		return expectedStdout, expectedStderr, expectedErr
	})

	stdout, stderr, err := executor.Execute(context.Background(), "test", nil, nil)
	assert.Equal(t, expectedStdout, stdout)
	assert.Equal(t, expectedStderr, stderr)
	assert.Equal(t, expectedErr, err)
}

// TestCommandExecutorFunc_Success tests successful mock
func TestCommandExecutorFunc_Success(t *testing.T) {
	executor := CommandExecutorFunc(func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
		return []byte("success"), nil, nil
	})

	stdout, stderr, err := executor.Execute(context.Background(), "test", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "success", string(stdout))
	assert.Empty(t, stderr)
}

// TestNewDefaultCommandExecutor_ReturnsNotNil tests constructor
func TestNewDefaultCommandExecutor_ReturnsNotNil(t *testing.T) {
	executor := NewDefaultCommandExecutor()
	require.NotNil(t, executor)

	// Verify it's DefaultCommandExecutor
	_, ok := executor.(*DefaultCommandExecutor)
	assert.True(t, ok)
}

// TestDefaultCommandExecutor_InterfaceCompliance tests interface compliance
func TestDefaultCommandExecutor_InterfaceCompliance(t *testing.T) {
	var _ = NewDefaultCommandExecutor()
	var _ CommandExecutor = CommandExecutorFunc(nil)
}

// TestDefaultCommandExecutor_CustomTimeout tests custom timeout
func TestDefaultCommandExecutor_CustomTimeout(t *testing.T) {
	executor := &DefaultCommandExecutor{Timeout: 60 * time.Second}
	assert.Equal(t, 60*time.Second, executor.Timeout)
}

// TestDefaultCommandExecutor_ZeroTimeout tests zero timeout (no timeout)
func TestDefaultCommandExecutor_ZeroTimeout(t *testing.T) {
	executor := &DefaultCommandExecutor{Timeout: 0}
	ctx := context.Background()

	// Zero timeout means no timeout override
	_, _, err := executor.Execute(ctx, "echo", []byte("test"), nil)
	require.NoError(t, err)
}

// TestDefaultCommandExecutor_ContextAlreadyCancelled tests cancelled context
func TestDefaultCommandExecutor_ContextAlreadyCancelled(t *testing.T) {
	executor := NewDefaultCommandExecutor()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, _, err := executor.Execute(ctx, "echo", []byte("test"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}
