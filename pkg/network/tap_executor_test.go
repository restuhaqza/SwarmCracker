package network

import (
	"context"
	"os/exec"
	"sync"

	"github.com/stretchr/testify/assert"
)

// MockTAPExecutor implements TAPExecutor for testing
type MockTAPExecutor struct {
	mu sync.Mutex

	// Track commands executed
	Commands []struct {
		Name string
		Args []string
	}

	// Configure responses
	RunError       error
	OutputResult   []byte
	OutputError    error
	CombinedResult []byte
	CombinedError  error

	// Per-command errors (by command name)
	RunErrors      map[string]error
	OutputErrors   map[string]error
	OutputResults  map[string][]byte
	CombinedErrors map[string]error
	CombinedResults map[string][]byte
}

// NewMockTAPExecutor creates a new mock executor
func NewMockTAPExecutor() *MockTAPExecutor {
	return &MockTAPExecutor{
		RunErrors:      make(map[string]error),
		OutputErrors:   make(map[string]error),
		OutputResults:  make(map[string][]byte),
		CombinedErrors: make(map[string]error),
		CombinedResults: make(map[string][]byte),
	}
}

// Command creates a mock exec.Cmd
func (m *MockTAPExecutor) Command(name string, arg ...string) *exec.Cmd {
	m.mu.Lock()
	m.Commands = append(m.Commands, struct {
		Name string
		Args []string
	}{Name: name, Args: arg})
	m.mu.Unlock()

	// Return a real exec.Cmd (it won't actually run in tests)
	return exec.Command(name, arg...)
}

// CommandContext creates a mock exec.Cmd with context
func (m *MockTAPExecutor) CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	m.mu.Lock()
	m.Commands = append(m.Commands, struct {
		Name string
		Args []string
	}{Name: name, Args: arg})
	m.mu.Unlock()

	return exec.CommandContext(ctx, name, arg...)
}

// Run executes mock command
func (m *MockTAPExecutor) Run(cmd *exec.Cmd) error {
	m.mu.Lock()
	name := cmd.Path
	m.mu.Unlock()

	if m.RunError != nil {
		return m.RunError
	}
	if err, ok := m.RunErrors[name]; ok {
		return err
	}
	return nil
}

// Output executes mock command and returns result
func (m *MockTAPExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	m.mu.Lock()
	name := cmd.Path
	m.mu.Unlock()

	if m.OutputError != nil {
		return m.OutputResult, m.OutputError
	}
	if err, ok := m.OutputErrors[name]; ok {
		return m.OutputResults[name], err
	}
	if result, ok := m.OutputResults[name]; ok {
		return result, nil
	}
	return m.OutputResult, nil
}

// CombinedOutput executes mock command and returns result
func (m *MockTAPExecutor) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	m.mu.Lock()
	name := cmd.Path
	m.mu.Unlock()

	if m.CombinedError != nil {
		return m.CombinedResult, m.CombinedError
	}
	if err, ok := m.CombinedErrors[name]; ok {
		return m.CombinedResults[name], err
	}
	if result, ok := m.CombinedResults[name]; ok {
		return result, nil
	}
	return m.CombinedResult, nil
}

// GetCommands returns all recorded commands
func (m *MockTAPExecutor) GetCommands() []struct {
	Name string
	Args []string
} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Commands
}

// ClearCommands clears recorded commands
func (m *MockTAPExecutor) ClearCommands() {
	m.mu.Lock()
	m.Commands = nil
	m.mu.Unlock()
}

// SetRunError sets error for Run calls
func (m *MockTAPExecutor) SetRunError(err error) {
	m.RunError = err
}

// SetOutputResult sets result for Output calls
func (m *MockTAPExecutor) SetOutputResult(result []byte) {
	m.OutputResult = result
}

// SetCombinedError sets error for CombinedOutput calls
func (m *MockTAPExecutor) SetCombinedError(err error) {
	m.CombinedError = err
}

// SetCombinedResult sets result for CombinedOutput calls
func (m *MockTAPExecutor) SetCombinedResult(result []byte) {
	m.CombinedResult = result
}

// AssertCommandCalled asserts a command was called
func (m *MockTAPExecutor) AssertCommandCalled(t assert.TestingT, name string, args ...string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cmd := range m.Commands {
		if cmd.Name == name && len(cmd.Args) >= len(args) {
			match := true
			for i, arg := range args {
				if cmd.Args[i] != arg {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return assert.Fail(t, "Command %s with args %v was not called", name, args)
}