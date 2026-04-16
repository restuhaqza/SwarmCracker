// Package network provides command execution implementations.
// This file contains the actual implementation functions that can be swapped for testing.

package network

import (
	"os/exec"
)

// These variables hold the actual implementations.
// They can be replaced in tests for mocking.
var (
	lookPathImpl         = exec.LookPath
	executeImpl          = defaultExecute
	executeWithOutputImpl = defaultExecuteWithOutput
)

// defaultExecute runs a command and returns any error.
func defaultExecute(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	return cmd.Run()
}

// defaultExecuteWithOutput runs a command and returns its output.
func defaultExecuteWithOutput(name string, arg ...string) (string, error) {
	cmd := exec.Command(name, arg...)
	output, err := cmd.Output()
	return string(output), err
}