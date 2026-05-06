package cni

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// CommandExecutor defines the interface for executing external commands.
// This allows mocking exec.Command for unit tests.
type CommandExecutor interface {
	// Execute runs a command with the given parameters.
	Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error)
}

// DefaultCommandExecutor is the default implementation using real exec.Command.
type DefaultCommandExecutor struct {
	Timeout time.Duration
}

// NewDefaultCommandExecutor creates a new default command executor.
func NewDefaultCommandExecutor() CommandExecutor {
	return &DefaultCommandExecutor{
		Timeout: 30 * time.Second,
	}
}

// Execute runs a command using exec.CommandContext.
func (e *DefaultCommandExecutor) Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
	// Set timeout
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}

	// Create command
	cmd := exec.CommandContext(ctx, name)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)

	// Execute
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// CommandExecutorFunc is a function adapter for CommandExecutor.
type CommandExecutorFunc func(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error)

// Execute implements CommandExecutor.
func (f CommandExecutorFunc) Execute(ctx context.Context, name string, stdin []byte, env []string) ([]byte, []byte, error) {
	return f(ctx, name, stdin, env)
}