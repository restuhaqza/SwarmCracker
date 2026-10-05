package main

import (
	"net"
	"testing"
)

// TestValidateManagerConnectivity ensures the pre-join probe works without any
// external tools (no `nc`, no bash /dev/tcp helper) and correctly rejects an
// unreachable endpoint.
func TestValidateManagerConnectivity(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if err := validateManagerConnectivity(ln.Addr().String()); err != nil {
		t.Fatalf("expected reachable manager to validate, got: %v", err)
	}

	// Open then immediately close a port so nothing is listening on it.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := ln2.Addr().String()
	_ = ln2.Close()

	if err := validateManagerConnectivity(closedAddr); err == nil {
		t.Fatalf("expected closed port %s to fail validation", closedAddr)
	}
}
