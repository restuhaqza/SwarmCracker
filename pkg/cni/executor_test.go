package cni

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultCommandExecutor_Execute tests basic command execution
func TestDefaultCommandExecutor_Execute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exec test in short mode")
	}

	executor := NewDefaultCommandExecutor()
	ctx := context.Background()

	// Execute echo command
	stdout, stderr, err := executor.Execute(ctx, "echo", []byte("test"), nil)
	require.NoError(t, err)
	assert.Contains(t, string(stdout), "test")
	assert.Empty(t, stderr)
}

// TestDefaultCommandExecutor_Execute_WithContext tests context cancellation
func TestDefaultCommandExecutor_Execute_WithContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exec test in short mode")
	}

	executor := &DefaultCommandExecutor{Timeout: 1 * time.Millisecond}
	ctx := context.Background()

	// Execute sleep with short timeout - should timeout
	_, _, err := executor.Execute(ctx, "sleep", []byte{}, []string{"DURATION=10"})
	require.Error(t, err) // Should timeout
	assert.Contains(t, err.Error(), "context deadline exceeded")
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
	if testing.Short() {
		t.Skip("skipping exec test in short mode")
	}

	executor := NewDefaultCommandExecutor()
	ctx := context.Background()

	// Execute env to check env vars
	stdout, _, err := executor.Execute(ctx, "sh", []byte{}, []string{"MY_VAR=test123"})
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
	if testing.Short() {
		t.Skip("skipping exec test in short mode")
	}

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
	var _ CommandExecutor = NewDefaultCommandExecutor()
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
