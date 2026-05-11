// Package network provides TAP device executor interface for mocking
package network

import (
	"context"
	"os/exec"
)

// TAPExecutor defines the interface for TAP device operations.
// This allows mocking exec.Command calls for unit testing.
type TAPExecutor interface {
	// Command creates a new exec.Cmd
	Command(name string, arg ...string) *exec.Cmd

	// CommandContext creates a new exec.Cmd with context
	CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd

	// Run executes the command and returns error
	Run(cmd *exec.Cmd) error

	// Output executes the command and returns stdout
	Output(cmd *exec.Cmd) ([]byte, error)

	// CombinedOutput executes the command and returns combined stdout/stderr
	CombinedOutput(cmd *exec.Cmd) ([]byte, error)
}

// DefaultTAPExecutor is the real implementation that uses exec.Command
type DefaultTAPExecutor struct{}

// NewDefaultTAPExecutor creates a new default TAP executor
func NewDefaultTAPExecutor() TAPExecutor {
	return &DefaultTAPExecutor{}
}

// Command creates a new exec.Cmd
func (e *DefaultTAPExecutor) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// CommandContext creates a new exec.Cmd with context
func (e *DefaultTAPExecutor) CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, arg...)
}

// Run executes the command
func (e *DefaultTAPExecutor) Run(cmd *exec.Cmd) error {
	return cmd.Run()
}

// Output executes the command and returns stdout
func (e *DefaultTAPExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	return cmd.Output()
}

// CombinedOutput executes the command and returns combined output
func (e *DefaultTAPExecutor) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}
