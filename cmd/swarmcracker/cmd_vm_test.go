package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestVMCommandStructure verifies that VM commands are registered
func TestVMCommandStructure(t *testing.T) {
	rootCmd := newVMCommand()

	if rootCmd.Use != "vm" {
		t.Errorf("Expected vm command to have use 'vm', got '%s'", rootCmd.Use)
	}

	// Check that expected subcommands are registered
	expectedCommands := []string{
		"create",
		"list", // 'ls' has alias 'list' but main name is 'list'
		"stop",
		"logs",
		"snapshot",
		"attach",
	}

	for _, cmd := range expectedCommands {
		found := false
		for _, subCmd := range rootCmd.Commands() {
			if subCmd.Name() == cmd {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("VM command '%s' not found", cmd)
		}
	}
}

// TestVMCreateCommand verifies the create command
func TestVMCreateCommand(t *testing.T) {
	cmd := newVMCreateCommand()

	if cmd.Name() != "create" {
		t.Errorf("Expected command name 'create', got '%s'", cmd.Name())
	}

	// Check required args
	if cmd.Args == nil {
		t.Error("Expected command to have Args validator")
	}

	// Check flags exist
	expectedFlags := []string{"name", "cpu", "memory", "network", "detach", "env"}
	for _, flag := range expectedFlags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("Expected flag '%s' not found", flag)
		}
	}
}

// TestVMCreateCommandFlagParsing tests flag parsing for create command
func TestVMCreateCommandFlagParsing(t *testing.T) {
	cmd := newVMCreateCommand()

	// Test --name flag
	nameFlag := cmd.Flags().Lookup("name")
	if nameFlag == nil {
		t.Error("name flag not found")
	}
	if nameFlag.Shorthand != "n" {
		t.Errorf("Expected name shorthand 'n', got '%s'", nameFlag.Shorthand)
	}

	// Test --cpu flag (no shorthand due to conflict with global -c/--config)
	cpuFlag := cmd.Flags().Lookup("cpu")
	if cpuFlag == nil {
		t.Error("cpu flag not found")
	}
	// Note: 'c' shorthand is reserved for global --config flag, so cpu has no shorthand
	if cpuFlag.Shorthand != "" {
		t.Errorf("Expected cpu shorthand to be empty (reserved for global config), got '%s'", cpuFlag.Shorthand)
	}

	// Test --memory flag
	memoryFlag := cmd.Flags().Lookup("memory")
	if memoryFlag == nil {
		t.Error("memory flag not found")
	}
	if memoryFlag.Shorthand != "m" {
		t.Errorf("Expected memory shorthand 'm', got '%s'", memoryFlag.Shorthand)
	}

	// Test --detach flag
	detachFlag := cmd.Flags().Lookup("detach")
	if detachFlag == nil {
		t.Error("detach flag not found")
	}
	if detachFlag.Shorthand != "d" {
		t.Errorf("Expected detach shorthand 'd', got '%s'", detachFlag.Shorthand)
	}
}

// TestVMCreateCommandDefaults tests default values for flags
func TestVMCreateCommandDefaults(t *testing.T) {
	cmd := newVMCreateCommand()

	// Check default values
	cpuFlag := cmd.Flags().Lookup("cpu")
	if cpuFlag != nil && cpuFlag.DefValue != "1" {
		t.Errorf("Expected default cpu '1', got '%s'", cpuFlag.DefValue)
	}

	memoryFlag := cmd.Flags().Lookup("memory")
	if memoryFlag != nil && memoryFlag.DefValue != "512" {
		t.Errorf("Expected default memory '512', got '%s'", memoryFlag.DefValue)
	}

	detachFlag := cmd.Flags().Lookup("detach")
	if detachFlag != nil && detachFlag.DefValue != "false" {
		t.Errorf("Expected default detach 'false', got '%s'", detachFlag.DefValue)
	}
}

// TestVMCreateCommandErrorHandling tests error handling for missing args
func TestVMCreateCommandErrorHandling(t *testing.T) {
	cmd := newVMCreateCommand()

	// Test with no args - should error
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Error("Expected error when no args provided")
	}

	// Test with too many args - should error
	err = cmd.Args(cmd, []string{"image1", "image2"})
	if err == nil {
		t.Error("Expected error when too many args provided")
	}

	// Test with exactly one arg - should pass
	err = cmd.Args(cmd, []string{"alpine:latest"})
	if err != nil {
		t.Errorf("Unexpected error with valid args: %v", err)
	}
}

// TestVMListCommand verifies the list command
func TestVMListCommand(t *testing.T) {
	cmd := newVMListCommand()

	// 'list' is the name, 'ls' is an alias
	if cmd.Name() != "list" {
		t.Errorf("Expected command name 'list', got '%s'", cmd.Name())
	}
}

// TestVMStopCommand verifies the stop command
func TestVMStopCommand(t *testing.T) {
	cmd := newVMStopCommand()

	if cmd.Name() != "stop" {
		t.Errorf("Expected command name 'stop', got '%s'", cmd.Name())
	}
}

// TestVMLogsCommand verifies the logs command
func TestVMLogsCommand(t *testing.T) {
	cmd := newVMLogsCommand()

	if cmd.Name() != "logs" {
		t.Errorf("Expected command name 'logs', got '%s'", cmd.Name())
	}
}

// TestVMSnapshotCommand verifies the snapshot command
func TestVMSnapshotCommand(t *testing.T) {
	cmd := newVMSnapshotCommand()

	if cmd.Name() != "snapshot" {
		t.Errorf("Expected command name 'snapshot', got '%s'", cmd.Name())
	}
}

// TestVMAttachCommand verifies the attach command
func TestVMAttachCommand(t *testing.T) {
	cmd := newVMAttachCommand()

	if cmd.Name() != "attach" {
		t.Errorf("Expected command name 'attach', got '%s'", cmd.Name())
	}

	// Requires exactly one <vm> argument.
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("Expected attach to require an argument")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("Expected attach to reject more than one argument")
	}
	if err := cmd.Args(cmd, []string{"abc"}); err != nil {
		t.Errorf("Expected attach to accept one argument, got %v", err)
	}

	if cmd.Flags().Lookup("socket-dir") == nil {
		t.Error("Expected flag 'socket-dir' not found")
	}
}

// chunkReader returns data in fixed-size chunks, to exercise reads that split
// multi-byte sequences across calls.
type chunkReader struct {
	data []byte
	size int
	off  int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := r.size
	if n > len(p) {
		n = len(p)
	}
	if r.off+n > len(r.data) {
		n = len(r.data) - r.off
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

func TestPumpStdinForwardsInput(t *testing.T) {
	var out bytes.Buffer
	if err := pumpStdin(strings.NewReader("hello"), &out); err != nil {
		t.Fatalf("pumpStdin returned %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("Expected 'hello', got %q", out.String())
	}
}

func TestPumpStdinInterceptsDetach(t *testing.T) {
	var out bytes.Buffer
	err := pumpStdin(strings.NewReader("a\x10\x11b"), &out)
	if !errors.Is(err, errDetach) {
		t.Fatalf("Expected errDetach, got %v", err)
	}
	if out.String() != "a" {
		t.Errorf("Expected only 'a' before detach, got %q", out.String())
	}
}

func TestPumpStdinDetachAcrossReads(t *testing.T) {
	var out bytes.Buffer
	err := pumpStdin(&chunkReader{data: []byte("\x10\x11"), size: 1}, &out)
	if !errors.Is(err, errDetach) {
		t.Fatalf("Expected errDetach, got %v", err)
	}
}

func TestPumpStdinPassesLoneCtrlP(t *testing.T) {
	var out bytes.Buffer
	if err := pumpStdin(strings.NewReader("a\x10b"), &out); err != nil {
		t.Fatalf("pumpStdin returned %v", err)
	}
	if out.String() != "a\x10b" {
		t.Errorf("Expected lone Ctrl-P to be forwarded, got %q", out.String())
	}
}
