package main

import (
	"testing"
)

// TestNodeCommandStructure verifies that all node commands are properly registered
func TestNodeCommandStructure(t *testing.T) {
	rootCmd := newNodeCommand()

	if rootCmd.Use != "node" {
		t.Errorf("Expected node command to have use 'node', got '%s'", rootCmd.Use)
	}

	// Check that all subcommands are registered
	expectedCommands := []string{
		"ls",
		"inspect",
		"drain",
		"activate",
		"promote",
		"rm",
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
			t.Errorf("Node command '%s' not found", cmd)
		}
	}
}

// TestNodeLsCommand verifies the node list command
func TestNodeLsCommand(t *testing.T) {
	cmd := newNodeListCommand()

	if cmd.Name() != "ls" {
		t.Errorf("Expected command name 'ls', got '%s'", cmd.Name())
	}

	// Check flags
	flags := []string{"format", "filter", "quiet"}
	for _, flag := range flags {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("Expected flag '%s' not found", flag)
		}
	}
}

// TestNodeInspectCommand verifies the inspect command
func TestNodeInspectCommand(t *testing.T) {
	cmd := newNodeInspectCommand()

	if cmd.Name() != "inspect" {
		t.Errorf("Expected command name 'inspect', got '%s'", cmd.Name())
	}
}

// TestNodeDrainCommand verifies the drain command
func TestNodeDrainCommand(t *testing.T) {
	cmd := newNodeDrainCommand()

	if cmd.Name() != "drain" {
		t.Errorf("Expected command name 'drain', got '%s'", cmd.Name())
	}
}

// TestNodeActivateCommand verifies the activate command
func TestNodeActivateCommand(t *testing.T) {
	cmd := newNodeActivateCommand()

	if cmd.Name() != "activate" {
		t.Errorf("Expected command name 'activate', got '%s'", cmd.Name())
	}
}

// TestNodePromoteCommand verifies the promote command
func TestNodePromoteCommand(t *testing.T) {
	cmd := newNodePromoteCommand()

	if cmd.Name() != "promote" {
		t.Errorf("Expected command name 'promote', got '%s'", cmd.Name())
	}
}

// TestNodeRemoveCommand verifies the rm command
func TestNodeRemoveCommand(t *testing.T) {
	cmd := newNodeRemoveCommand()

	if cmd.Name() != "rm" {
		t.Errorf("Expected command name 'rm', got '%s'", cmd.Name())
	}
}
