package network

import (
	"context"
	"os/exec"
)

// CommandExecutor defines the interface for executing system commands.
type CommandExecutor interface {
	// Command runs a command and returns the result
	Command(name string, args ...string) CmdExecutor
	// CommandContext runs a command with context
	CommandContext(ctx context.Context, name string, args ...string) CmdExecutor
}

// CmdExecutor represents a running command
type CmdExecutor interface {
	Run() error
	Output() ([]byte, error)
	CombinedOutput() ([]byte, error)
	Start() error
	Wait() error
}

// RealCommandExecutor implements CommandExecutor using os/exec
type RealCommandExecutor struct{}

// Command creates a new command
func (r *RealCommandExecutor) Command(name string, args ...string) CmdExecutor {
	return &realCmd{cmd: exec.Command(name, args...)}
}

// CommandContext creates a new command with context
func (r *RealCommandExecutor) CommandContext(ctx context.Context, name string, args ...string) CmdExecutor {
	return &realCmd{cmd: exec.CommandContext(ctx, name, args...)}
}

// realCmd wraps exec.Cmd
type realCmd struct {
	cmd *exec.Cmd
}

func (r *realCmd) Run() error {
	return r.cmd.Run()
}

func (r *realCmd) Output() ([]byte, error) {
	return r.cmd.Output()
}

func (r *realCmd) CombinedOutput() ([]byte, error) {
	return r.cmd.CombinedOutput()
}

func (r *realCmd) Start() error {
	return r.cmd.Start()
}

func (r *realCmd) Wait() error {
	return r.cmd.Wait()
}

// MockCommandExecutor is a mock implementation for testing
type MockCommandExecutor struct {
	// Commands maps command name to mock results
	Commands map[string]MockCommandResult
	// CommandHandlers allows custom handling based on command and arguments
	CommandHandlers map[string]func(args []string) MockCommandResult
	// Call tracking
	Calls []CommandCall
}

// MockCommandResult represents the result of a mocked command
type MockCommandResult struct {
	Output []byte
	Err    error
}

// CommandCall records a command invocation
type CommandCall struct {
	Name string
	Args []string
}

// Command creates a mock command
func (m *MockCommandExecutor) Command(name string, args ...string) CmdExecutor {
	m.Calls = append(m.Calls, CommandCall{Name: name, Args: args})

	// Check for custom handler first
	if m.CommandHandlers != nil {
		if handler, ok := m.CommandHandlers[name]; ok {
			result := handler(args)
			return &mockCmd{output: result.Output, err: result.Err}
		}
	}

	// Fall back to simple command map
	result := m.Commands[name]
	return &mockCmd{output: result.Output, err: result.Err}
}

// CommandContext creates a mock command with context
func (m *MockCommandExecutor) CommandContext(ctx context.Context, name string, args ...string) CmdExecutor {
	m.Calls = append(m.Calls, CommandCall{Name: name, Args: args})

	// Check for custom handler first
	if m.CommandHandlers != nil {
		if handler, ok := m.CommandHandlers[name]; ok {
			result := handler(args)
			return &mockCmd{output: result.Output, err: result.Err}
		}
	}

	// Fall back to simple command map
	result := m.Commands[name]
	return &mockCmd{output: result.Output, err: result.Err}
}

// mockCmd is a mock command implementation
type mockCmd struct {
	output []byte
	err    error
}

func (m *mockCmd) Run() error {
	return m.err
}

func (m *mockCmd) Output() ([]byte, error) {
	return m.output, m.err
}

func (m *mockCmd) CombinedOutput() ([]byte, error) {
	return m.output, m.err
}

func (m *mockCmd) Start() error {
	return m.err
}

func (m *mockCmd) Wait() error {
	return m.err
}

// NewMockCommandExecutor creates a new mock command executor
func NewMockCommandExecutor() *MockCommandExecutor {
	return &MockCommandExecutor{
		Commands:        make(map[string]MockCommandResult),
		CommandHandlers: make(map[string]func(args []string) MockCommandResult),
		Calls:           make([]CommandCall, 0),
	}
}
