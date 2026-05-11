package main

import (
	"testing"
)

// TestClusterCommandStructure verifies that all cluster commands are properly registered
func TestClusterCommandStructure(t *testing.T) {
	rootCmd := newClusterCommand()

	if rootCmd.Use != "cluster" {
		t.Errorf("Expected cluster command to have use 'cluster', got '%s'", rootCmd.Use)
	}

	// Check that all subcommands are registered
	expectedCommands := []string{
		"init",
		"join",
		"leave",
		"token",
		"status",
		"reset",
		"deinit",
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
			t.Errorf("Cluster command '%s' not found", cmd)
		}
	}
}

// TestClusterInitCommand verifies the init command exists
func TestClusterInitCommand(t *testing.T) {
	cmd := newClusterInitCommand()

	if cmd.Name() != "init" {
		t.Errorf("Expected command name 'init', got '%s'", cmd.Name())
	}
}

// TestClusterJoinCommand verifies the join command exists
func TestClusterJoinCommand(t *testing.T) {
	cmd := newClusterJoinCommand()

	if cmd.Name() != "join" {
		t.Errorf("Expected command name 'join', got '%s'", cmd.Name())
	}
}

// TestClusterLeaveCommand verifies the leave command exists
func TestClusterLeaveCommand(t *testing.T) {
	cmd := newClusterLeaveCommand()

	if cmd.Name() != "leave" {
		t.Errorf("Expected command name 'leave', got '%s'", cmd.Name())
	}
}

// TestClusterTokenCommand verifies the token command exists
func TestClusterTokenCommand(t *testing.T) {
	cmd := newClusterTokenCommand()

	if cmd.Name() != "token" {
		t.Errorf("Expected command name 'token', got '%s'", cmd.Name())
	}
}

// TestClusterStatusCommand verifies the status command exists
func TestClusterStatusCommand(t *testing.T) {
	cmd := newClusterStatusCommand()

	if cmd.Name() != "status" {
		t.Errorf("Expected command name 'status', got '%s'", cmd.Name())
	}
}

// TestClusterResetCommand verifies the reset command exists
func TestClusterResetCommand(t *testing.T) {
	cmd := newClusterResetCommand()

	if cmd.Name() != "reset" {
		t.Errorf("Expected command name 'reset', got '%s'", cmd.Name())
	}
}

// TestClusterDeinitCommand verifies the deinit command exists
func TestClusterDeinitCommand(t *testing.T) {
	cmd := newClusterDeinitCommand()

	if cmd.Name() != "deinit" {
		t.Errorf("Expected command name 'deinit', got '%s'", cmd.Name())
	}
}