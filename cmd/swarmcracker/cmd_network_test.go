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

// TestParseVXLANPeers verifies the VXLAN output parser.
func TestParseVXLANPeers(t *testing.T) {
	out := `5: vxlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc noqueue state UNKNOWN mode DEFAULT group default
    link/ether 1e:2c:3a:4b:5c:6d brd ff:ff:ff:ff:ff:ff promiscuity 0
    vxlan id 42 local 192.168.18.25 remote 192.168.18.26 dev eth0 port 4789
`
	peers := parseVXLANPeers(out)
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	p := peers[0]
	if p.Interface != "vxlan0" || p.VNI != "42" || p.Local != "192.168.18.25" || p.Remote != "192.168.18.26" {
		t.Errorf("unexpected peer: %+v", p)
	}
	if got := parseVXLANPeers(""); len(got) != 0 {
		t.Errorf("expected no peers for empty input, got %d", len(got))
	}
}
