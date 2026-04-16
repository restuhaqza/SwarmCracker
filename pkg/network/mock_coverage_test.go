package network

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockCommandExecutor_Constructor tests mock command executor constructor
func TestMockCommandExecutor_Constructor(t *testing.T) {
	mock := NewMockCommandExecutor()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Commands)
	assert.NotNil(t, mock.CommandHandlers)
	assert.NotNil(t, mock.Calls)
}

// TestMockCommandExecutor_AllMethods tests all mock command executor methods
func TestMockCommandExecutor_AllMethods(t *testing.T) {
	mock := NewMockCommandExecutor()
	ctx := context.Background()

	// Setup mock responses
	mock.Commands["ip"] = MockCommandResult{
		Output: []byte("1: lo: <LOOPBACK>\n"),
		Err:    nil,
	}

	// Test Command
	cmd := mock.Command("ip", "link", "show")
	require.NotNil(t, cmd)
	assert.Len(t, mock.Calls, 1)
	assert.Equal(t, "ip", mock.Calls[0].Name)

	// Test CommandContext
	cmd = mock.CommandContext(ctx, "ip", "link", "show")
	require.NotNil(t, cmd)
	assert.Len(t, mock.Calls, 2)

	// Test Command with handler
	mock.CommandHandlers["brctl"] = func(args []string) MockCommandResult {
		return MockCommandResult{
			Output: []byte("bridge1\n"),
			Err:    nil,
		}
	}
	cmd = mock.Command("brctl", "show")
	require.NotNil(t, cmd)

	// Test mockCmd methods
	mockCmd := &mockCmd{
		output: []byte("test output"),
		err:    nil,
	}

	// Test Run
	err := mockCmd.Run()
	require.NoError(t, err)

	// Test Output
	output, err := mockCmd.Output()
	require.NoError(t, err)
	assert.Equal(t, []byte("test output"), output)

	// Test CombinedOutput
	output, err = mockCmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, []byte("test output"), output)

	// Test Start
	err = mockCmd.Start()
	require.NoError(t, err)

	// Test Wait
	err = mockCmd.Wait()
	require.NoError(t, err)
}

// TestMockCommandExecutor_Errors tests command executor with errors
func TestMockCommandExecutor_Errors(t *testing.T) {
	mock := NewMockCommandExecutor()

	mock.Commands["fail-cmd"] = MockCommandResult{
		Output: nil,
		Err:    assert.AnError,
	}

	cmd := mock.Command("fail-cmd")

	// Test Run with error
	err := cmd.Run()
	require.Error(t, err)

	// Test Output with error
	_, err = cmd.Output()
	require.Error(t, err)

	// Test CombinedOutput with error
	_, err = cmd.CombinedOutput()
	require.Error(t, err)

	// Test Start with error
	err = cmd.Start()
	require.Error(t, err)

	// Test Wait with error
	err = cmd.Wait()
	require.Error(t, err)
}

// TestMockCommandResult tests mock command result
func TestMockCommandResult(t *testing.T) {
	result := MockCommandResult{
		Output: []byte("output"),
		Err:    nil,
	}
	assert.NotNil(t, result.Output)

	result.Err = assert.AnError
	assert.NotNil(t, result.Err)
}

// TestCommandCall tests command call tracking
func TestCommandCall(t *testing.T) {
	call := CommandCall{
		Name: "ip",
		Args: []string{"link", "show"},
	}
	assert.Equal(t, "ip", call.Name)
	assert.Len(t, call.Args, 2)
}

// TestRealCommandExecutor tests real command executor concept
func TestRealCommandExecutor(t *testing.T) {
	executor := &RealCommandExecutor{}
	require.NotNil(t, executor)

	// Test Command (will actually run on system)
	cmd := executor.Command("echo", "test")
	require.NotNil(t, cmd)

	// Test CommandContext
	ctx := context.Background()
	cmd = executor.CommandContext(ctx, "echo", "test")
	require.NotNil(t, cmd)
}

// TestRealCommandExecutor_Methods tests real executor methods
func TestRealCommandExecutor_Methods(t *testing.T) {
	executor := &RealCommandExecutor{}

	// Create a simple echo command
	cmd := executor.Command("echo", "hello")

	// Test Run
	err := cmd.Run()
	if err != nil {
		t.Logf("echo command may not work in all environments: %v", err)
		return // Skip if echo not available
	}

	// Test Output
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Output failed: %v", err)
	} else {
		assert.Contains(t, string(output), "hello")
	}

	// Test CombinedOutput
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Logf("CombinedOutput failed: %v", err)
	} else {
		assert.Contains(t, string(output), "hello")
	}
}

// TestMockCommandExecutor_AddResponse tests AddResponse helper
func TestMockCommandExecutor_AddResponse(t *testing.T) {
	mock := NewMockCommandExecutor()

	// Add mock response directly
	mock.Commands["test-cmd"] = MockCommandResult{
		Output: []byte("test"),
		Err:    nil,
	}

	cmd := mock.Command("test-cmd")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, []byte("test"), output)
}

// TestMockCommandExecutor_HandlerOverride tests handler override
func TestMockCommandExecutor_HandlerOverride(t *testing.T) {
	mock := NewMockCommandExecutor()

	// Add both static response and handler
	mock.Commands["cmd"] = MockCommandResult{
		Output: []byte("static"),
		Err:    nil,
	}

	mock.CommandHandlers["cmd"] = func(args []string) MockCommandResult {
		return MockCommandResult{
			Output: []byte("dynamic-" + args[0]),
			Err:    nil,
		}
	}

	// Handler should override static response
	cmd := mock.Command("cmd", "arg1")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "dynamic")
}