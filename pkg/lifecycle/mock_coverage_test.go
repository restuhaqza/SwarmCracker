package lifecycle

import (
	"bytes"
	"context"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockProcessExecutor_Constructor tests mock process executor constructor
func TestMockProcessExecutor_Constructor(t *testing.T) {
	mock := NewMockProcessExecutor()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Processes)
	assert.NotNil(t, mock.Commands)
	assert.NotNil(t, mock.Binaries)
	assert.NotNil(t, mock.Calls)
}

// TestMockProcessExecutor_AllMethods tests all mock process executor methods
func TestMockProcessExecutor_AllMethods(t *testing.T) {
	mock := NewMockProcessExecutor()
	ctx := context.Background()

	// Setup mock data
	mockProcess := &mockProcess{pid: 12345}
	mock.Processes[12345] = mockProcess
	mock.Binaries["ls"] = "/bin/ls"
	mock.Commands["ls"] = struct {
		Output []byte
		Err    error
	}{Output: []byte("file1\nfile2\n"), Err: nil}

	// Test FindProcess
	proc, err := mock.FindProcess(12345)
	require.NoError(t, err)
	assert.Equal(t, 12345, proc.Pid())

	// Test LookPath
	path, err := mock.LookPath("ls")
	require.NoError(t, err)
	assert.Equal(t, "/bin/ls", path)
	assert.Contains(t, mock.Calls, "LookPath:ls")

	// Test Command
	cmd := mock.Command("ls", "-la")
	require.NotNil(t, cmd)

	// Test CommandContext
	cmd = mock.CommandContext(ctx, "ls", "-la")
	require.NotNil(t, cmd)

	// Test StartProcess
	cmdForStart := &mockCmd{process: mockProcess, executor: mock}
	proc, err = mock.StartProcess(cmdForStart)
	require.NoError(t, err)
	assert.NotNil(t, proc)
}

// TestMockProcessExecutor_Errors tests process executor errors
func TestMockProcessExecutor_Errors(t *testing.T) {
	mock := NewMockProcessExecutor()

	// FindProcess for non-existent process
	proc, err := mock.FindProcess(99999)
	require.Error(t, err)
	assert.Nil(t, proc)

	// LookPath for non-existent binary
	path, err := mock.LookPath("nonexistent")
	require.Error(t, err)
	assert.Empty(t, path)
}

// TestMockProcess_AllMethods tests all mock process methods
func TestMockProcess_AllMethods(t *testing.T) {
	proc := &mockProcess{
		pid:     12345,
		signals: make([]syscall.Signal, 0),
	}

	// Test Pid
	assert.Equal(t, 12345, proc.Pid())

	// Test Kill
	err := proc.Kill()
	require.NoError(t, err)

	// Test Signal
	err = proc.Signal(syscall.SIGTERM)
	require.NoError(t, err)
}

// TestMockProcess_SignalZero tests signal 0 behavior
func TestMockProcess_SignalZero(t *testing.T) {
	proc := &mockProcess{
		pid:     12345,
		signals: make([]syscall.Signal, 0),
	}

	// Signal(0) should error
	err := proc.Signal(syscall.Signal(0))
	require.Error(t, err)
}

// TestMockCmd_AllMethods tests all mock cmd methods
func TestMockCmd_AllMethods(t *testing.T) {
	mock := NewMockProcessExecutor()
	mock.Commands["echo"] = struct {
		Output []byte
		Err    error
	}{Output: []byte("hello\n"), Err: nil}

	cmd := &mockCmd{
		output:   []byte("test output"),
		err:      nil,
		executor: mock,
	}

	// Test Start
	err := cmd.Start()
	require.NoError(t, err)

	// Test Wait
	err = cmd.Wait()
	require.NoError(t, err)

	// Test Run
	err = cmd.Run()
	require.NoError(t, err)

	// Test Output
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, []byte("test output"), output)

	// Test CombinedOutput
	output, err = cmd.CombinedOutput()
	require.NoError(t, err)
	assert.Equal(t, []byte("test output"), output)

	// Test SetStdin
	var stdin bytes.Buffer
	cmd.SetStdin(&stdin)

	// Test SetStdout
	var stdout bytes.Buffer
	cmd.SetStdout(&stdout)

	// Test SetStderr
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)

	// Test Process
	proc := cmd.Process()
	_ = proc // May be nil
}

// TestMockCmd_WithErrors tests mock cmd with errors
func TestMockCmd_WithErrors(t *testing.T) {
	cmd := &mockCmd{
		err: assert.AnError,
	}

	err := cmd.Start()
	require.Error(t, err)

	err = cmd.Wait()
	require.Error(t, err)

	err = cmd.Run()
	require.Error(t, err)

	_, err = cmd.Output()
	require.Error(t, err)

	_, err = cmd.CombinedOutput()
	require.Error(t, err)
}

// TestMockHTTPClient tests mock HTTP client
func TestMockHTTPClient(t *testing.T) {
	mock := NewMockHTTPClient()
	require.NotNil(t, mock)
	assert.NotNil(t, mock.Responses)
	assert.NotNil(t, mock.Calls)
	assert.NotNil(t, mock.Errors)
}

// TestNewMockProcessExecutor tests NewMockProcessExecutor constructor
func TestNewMockProcessExecutor_IsNotNil(t *testing.T) {
	mock := NewMockProcessExecutor()
	assert.NotNil(t, mock)
}