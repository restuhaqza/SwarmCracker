package main

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/runtime"
)

// TestEnrichVMs_ReadsDaemonMetadata verifies the CLI fills in the guest IP from
// the per-task metadata the daemon writes next to the VM socket — the only
// source of the IP for fallback-allocated service tasks.
func TestEnrichVMs_ReadsDaemonMetadata(t *testing.T) {
	dir := t.TempDir()

	orig := vmSocketDir
	vmSocketDir = dir
	defer func() { vmSocketDir = orig }()

	// Force the SwarmKit index lookup to fail fast instead of reaching a real
	// cluster that may be running on the host.
	t.Setenv("SWARM_STATE_DIR", t.TempDir())

	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{
		ID:          "svc-task-1",
		NetworkID:   "net-1",
		IPAddresses: []string{"192.168.127.165/24"},
	}); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	vm := &runtime.VMState{ID: "svc-task-1", Status: "running"}
	enrichVMs([]*runtime.VMState{vm})

	if vm.NetworkID != "net-1" {
		t.Errorf("NetworkID = %q, want %q", vm.NetworkID, "net-1")
	}
	if len(vm.IPAddresses) != 1 || vm.IPAddresses[0] != "192.168.127.165/24" {
		t.Errorf("IPAddresses = %v, want [192.168.127.165/24]", vm.IPAddresses)
	}
}

// TestEnrichVMs_ExistingIPWins ensures existing state (e.g. CLI-created VMs) is
// not overwritten by daemon metadata.
func TestEnrichVMs_ExistingIPWins(t *testing.T) {
	dir := t.TempDir()

	orig := vmSocketDir
	vmSocketDir = dir
	defer func() { vmSocketDir = orig }()

	t.Setenv("SWARM_STATE_DIR", t.TempDir())

	if err := runtime.WriteVMMetadata(dir, &runtime.VMMetadata{
		ID:          "vm-2",
		IPAddresses: []string{"10.9.9.9/24"},
	}); err != nil {
		t.Fatalf("WriteVMMetadata failed: %v", err)
	}

	vm := &runtime.VMState{
		ID:          "vm-2",
		Status:      "running",
		NetworkID:   "network-1",
		IPAddresses: []string{"10.0.0.2/24"},
	}
	enrichVMs([]*runtime.VMState{vm})

	if len(vm.IPAddresses) != 1 || vm.IPAddresses[0] != "10.0.0.2/24" {
		t.Errorf("existing IP should win, got %v", vm.IPAddresses)
	}
	if vm.NetworkID != "network-1" {
		t.Errorf("existing NetworkID should win, got %q", vm.NetworkID)
	}
}
