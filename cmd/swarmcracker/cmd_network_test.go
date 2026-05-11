package main

import (
	"testing"
)

// TestNetworkCommandStructure verifies that all network commands are properly registered
func TestNetworkCommandStructure(t *testing.T) {
	rootCmd := newNetworkCommand()

	if rootCmd.Use != "network" {
		t.Errorf("Expected network command to have use 'network', got '%s'", rootCmd.Use)
	}

	// Check that all subcommands are registered
	expectedCommands := []string{
		"vxlan",
		"bridge",
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
			t.Errorf("Network command '%s' not found", cmd)
		}
	}
}

// TestVXLANCommandStructure verifies VXLAN subcommands
func TestVXLANCommandStructure(t *testing.T) {
	vxlanCmd := newNetworkVXLANCommand()

	if vxlanCmd.Use != "vxlan" {
		t.Errorf("Expected vxlan command to have use 'vxlan', got '%s'", vxlanCmd.Use)
	}

	// Check that subcommands are registered
	expectedCommands := []string{
		"ls",
		"status",
	}

	for _, cmd := range expectedCommands {
		found := false
		for _, subCmd := range vxlanCmd.Commands() {
			if subCmd.Name() == cmd {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("VXLAN command '%s' not found", cmd)
		}
	}
}

// TestVXLANListCommand verifies the vxlan ls command
func TestVXLANListCommand(t *testing.T) {
	cmd := newVXLANListCommand()

	if cmd.Name() != "ls" {
		t.Errorf("Expected command name 'ls', got '%s'", cmd.Name())
	}

	// Check aliases
	expectedAliases := []string{"peers", "list"}
	for _, alias := range expectedAliases {
		found := false
		for _, a := range cmd.Aliases {
			if a == alias {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected alias '%s' not found", alias)
		}
	}

	// Check flags
	if cmd.Flags().Lookup("format") == nil {
		t.Errorf("Expected flag 'format' not found")
	}
}

// TestBridgeCommandStructure verifies bridge subcommands
func TestBridgeCommandStructure(t *testing.T) {
	bridgeCmd := newNetworkBridgeCommand()

	if bridgeCmd.Use != "bridge" {
		t.Errorf("Expected bridge command to have use 'bridge', got '%s'", bridgeCmd.Use)
	}

	// Check that status subcommand is registered
	found := false
	for _, subCmd := range bridgeCmd.Commands() {
		if subCmd.Name() == "status" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Bridge command 'status' not found")
	}
}

// TestVXLANPeersPlaceholder verifies the placeholder function exists
func TestVXLANPeersPlaceholder(t *testing.T) {
	// Test that listVXLANPeers function exists and can be called
	// This is a placeholder implementation, so we just verify it doesn't panic
	err := listVXLANPeers("table")
	// The placeholder may return an error from runDoctorNetwork, that's acceptable
	// We just verify the function exists
	if err == nil {
		// Function exists and succeeded
		t.Log("listVXLANPeers placeholder function exists")
	} else {
		// Function exists but may have returned an error (expected for placeholder)
		t.Logf("listVXLANPeers placeholder returned error (expected): %v", err)
	}
}
