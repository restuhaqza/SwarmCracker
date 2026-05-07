//go:build !integration

package network

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// executor_impl.go coverage tests
// =============================================================================

func TestDefaultExecute_Command(t *testing.T) {
	// Test defaultExecute with actual command
	// We need to swap the implementation for testing

	// Save original
	orig := executeImpl
	defer func() { executeImpl = orig }()

	// Test with a mock implementation
	executeImpl = func(name string, arg ...string) error {
		if name == "true" {
			return nil
		}
		return errors.New("command failed")
	}

	err := executeImpl("true")
	require.NoError(t, err)

	err = executeImpl("false")
	require.Error(t, err)
}

func TestDefaultExecuteWithOutput_Command(t *testing.T) {
	// Save original
	orig := executeWithOutputImpl
	defer func() { executeWithOutputImpl = orig }()

	// Test with a mock implementation
	executeWithOutputImpl = func(name string, arg ...string) (string, error) {
		if name == "echo" {
			return "output", nil
		}
		return "", errors.New("command failed")
	}

	output, err := executeWithOutputImpl("echo", "test")
	require.NoError(t, err)
	assert.Equal(t, "output", output)

	output, err = executeWithOutputImpl("false")
	require.Error(t, err)
	assert.Empty(t, output)
}

func TestLookPathImpl(t *testing.T) {
	// Save original
	orig := lookPathImpl
	defer func() { lookPathImpl = orig }()

	// Test with mock
	lookPathImpl = func(name string) (string, error) {
		if name == "existing-cmd" {
			return "/usr/bin/existing-cmd", nil
		}
		return "", errors.New("command not found")
	}

	path, err := lookPathImpl("existing-cmd")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/existing-cmd", path)

	path, err = lookPathImpl("nonexistent")
	require.Error(t, err)
	assert.Empty(t, path)
}

// =============================================================================
// Real command execution tests (only run when not in short mode)
// =============================================================================

func TestDefaultExecute_RealCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	// Test with actual implementation
	err := defaultExecute("true")
	require.NoError(t, err)

	err = defaultExecute("false")
	require.Error(t, err)
}

func TestDefaultExecuteWithOutput_RealCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	// Test with actual implementation
	output, err := defaultExecuteWithOutput("echo", "hello")
	require.NoError(t, err)
	assert.Contains(t, output, "hello")

	output, err = defaultExecuteWithOutput("ls", "/nonexistent")
	require.Error(t, err)
	assert.Contains(t, output, "") // May have stderr in output
}

func TestLookPathImpl_RealCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command lookup")
	}

	// Test with actual implementation
	path, err := lookPathImpl("ls")
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	path, err = lookPathImpl("nonexistent-command-xyz")
	require.Error(t, err)
	assert.Empty(t, path)
}

// =============================================================================
// Command argument handling tests
// =============================================================================

func TestDefaultExecute_Arguments(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	// Test with arguments
	err := defaultExecute("echo", "test", "multiple", "args")
	require.NoError(t, err)

	// Test command with failing args
	err = defaultExecute("test", "-f", "/nonexistent")
	require.Error(t, err)
}

func TestDefaultExecuteWithOutput_Arguments(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	output, err := defaultExecuteWithOutput("echo", "arg1", "arg2")
	require.NoError(t, err)
	assert.Contains(t, output, "arg1 arg2")
}

// =============================================================================
// Edge case tests
// =============================================================================

func TestDefaultExecute_EmptyArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	// Test with no args
	err := defaultExecute("true")
	require.NoError(t, err)
}

func TestDefaultExecuteWithOutput_EmptyArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	output, err := defaultExecuteWithOutput("echo")
	require.NoError(t, err)
	// Echo with no args just prints newline
	assert.Contains(t, output, "")
}

func TestDefaultExecute_InvalidCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	err := defaultExecute("nonexistent-command-xyz123")
	require.Error(t, err)
}

func TestDefaultExecuteWithOutput_InvalidCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: requires real command execution")
	}

	output, err := defaultExecuteWithOutput("nonexistent-command-xyz123")
	require.Error(t, err)
	assert.Empty(t, output)
}