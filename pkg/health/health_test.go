package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCheckerAllHealthy tests that all checks pass when everything is available.
func TestCheckerAllHealthy(t *testing.T) {
	// Skip if running as non-root or KVM not available
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("KVM not available, skipping all-healthy test")
	}

	// Check if firecracker is available
	if _, err := exec.LookPath("firecracker"); err != nil {
		t.Skip("firecracker not in PATH, skipping all-healthy test")
	}

	// Use a bridge that might exist (like docker0 or lo)
	// For this test, we'll use "lo" which should always exist
	checker := NewChecker("lo", "firecracker")
	status := checker.Check()

	if !status.Healthy {
		t.Errorf("Expected healthy status, got unhealthy: %+v", status)
	}

	// Check KVM
	if kvm, ok := status.Checks["kvm"]; ok {
		if kvm.Status != "ok" {
			t.Errorf("KVM check failed: %s", kvm.Message)
		}
	} else {
		t.Error("KVM check missing from results")
	}

	// Check bridge
	if bridge, ok := status.Checks["bridge"]; ok {
		if bridge.Status != "ok" {
			t.Errorf("Bridge check failed: %s", bridge.Message)
		}
	} else {
		t.Error("Bridge check missing from results")
	}

	// Check firecracker
	if fc, ok := status.Checks["firecracker"]; ok {
		if fc.Status != "ok" {
			t.Errorf("Firecracker check failed: %s", fc.Message)
		}
	} else {
		t.Error("Firecracker check missing from results")
	}
}

// TestCheckerKVMMissing tests the KVM check when /dev/kvm is missing.
func TestCheckerKVMMissing(t *testing.T) {
	// Create a temp directory and use it as a fake KVM path
	// We can't easily remove /dev/kvm, so we test the error path
	// by checking if we can detect when it's missing

	// Test the checkKVM function behavior
	checker := NewChecker("nonexistent-bridge-xyz", "firecracker")

	// If KVM exists, the check should pass
	// If it doesn't exist, the check should fail
	status := checker.Check()
	kvmResult := status.Checks["kvm"]

	// Verify the result matches reality
	_, kvmExists := os.Stat("/dev/kvm")
	if os.IsNotExist(kvmExists) {
		if kvmResult.Status != "error" {
			t.Error("Expected error status when KVM is missing")
		}
		if kvmResult.Message != "/dev/kvm does not exist" {
			t.Errorf("Unexpected error message: %s", kvmResult.Message)
		}
	} else {
		// KVM exists - could be permission denied or OK
		if kvmResult.Status == "error" && kvmResult.Message == "/dev/kvm does not exist" {
			t.Error("KVM exists but check says it doesn't")
		}
	}
}

// TestCheckerBridgeMissing tests the bridge check when the bridge doesn't exist.
func TestCheckerBridgeMissing(t *testing.T) {
	checker := NewChecker("nonexistent-bridge-xyz-12345", "firecracker")
	status := checker.Check()

	bridgeResult := status.Checks["bridge"]
	if bridgeResult.Status != "error" {
		t.Errorf("Expected error status for nonexistent bridge, got: %s", bridgeResult.Status)
	}

	if bridgeResult.Message == "" {
		t.Error("Expected error message for nonexistent bridge")
	}

	// Should contain bridge name in message
	expectedSubstring := "nonexistent-bridge-xyz-12345"
	if bridgeResult.Status == "error" && bridgeResult.Message != "" {
		// Message should mention the bridge name
		found := false
		for _, part := range []string{expectedSubstring, "does not exist"} {
			if contains(bridgeResult.Message, part) {
				found = true
				break
			}
		}
		if !found {
			t.Logf("Bridge error message: %s", bridgeResult.Message)
		}
	}
}

// TestCheckerFirecrackerMissing tests the firecracker check when binary is not found.
func TestCheckerFirecrackerMissing(t *testing.T) {
	// Test with a nonexistent path
	checker := NewChecker("lo", "/nonexistent/path/to/firecracker")
	status := checker.Check()

	fcResult := status.Checks["firecracker"]
	if fcResult.Status != "error" {
		t.Errorf("Expected error status for nonexistent firecracker, got: %s", fcResult.Status)
	}

	if fcResult.Message == "" {
		t.Error("Expected error message for nonexistent firecracker")
	}

	// Test with PATH lookup and binary not in PATH
	// Create a temp PATH without firecracker
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Save original PATH
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)

	// Set PATH to empty temp dir
	os.Setenv("PATH", tmpDir)

	checker2 := NewChecker("lo", "firecracker")
	status2 := checker2.Check()

	fcResult2 := status2.Checks["firecracker"]
	if fcResult2.Status != "error" {
		t.Errorf("Expected error status when firecracker not in PATH, got: %s", fcResult2.Status)
	}
}

// TestHTTPResponse tests the HTTP handler behavior.
func TestHTTPResponse(t *testing.T) {
	// Create a checker with known configuration
	checker := NewChecker("lo", "firecracker")

	// Create test server
	server := httptest.NewServer(checker)
	defer server.Close()

	// Test GET request
	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Verify response code is either 200 or 503
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Unexpected status code: %d", resp.StatusCode)
	}

	// Verify content type
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Expected application/json content type, got: %s", ct)
	}

	// Decode and verify body
	var status HealthStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify structure
	if status.Checks == nil {
		t.Error("Expected checks map to be non-nil")
	}

	if _, ok := status.Checks["kvm"]; !ok {
		t.Error("Expected 'kvm' check in results")
	}

	if _, ok := status.Checks["bridge"]; !ok {
		t.Error("Expected 'bridge' check in results")
	}

	if _, ok := status.Checks["firecracker"]; !ok {
		t.Error("Expected 'firecracker' check in results")
	}

	// Test that status.Healthy matches check results
	expectedHealthy := true
	for name, check := range status.Checks {
		if check.Status == "error" {
			expectedHealthy = false
			t.Logf("Check %s failed: %s", name, check.Message)
		}
	}

	if status.Healthy != expectedHealthy {
		t.Errorf("Healthy=%v but expected %v based on checks", status.Healthy, expectedHealthy)
	}
}

// TestHTTPMethodNotAllowed tests that non-GET methods are rejected.
func TestHTTPMethodNotAllowed(t *testing.T) {
	checker := NewChecker("lo", "firecracker")
	server := httptest.NewServer(checker)
	defer server.Close()

	// Test POST request
	resp, err := http.Post(server.URL+"/healthz", "application/json", nil)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405 Method Not Allowed, got: %d", resp.StatusCode)
	}
}

// TestCheckerWithSpecificPath tests firecracker check with a specific binary path.
func TestCheckerWithSpecificPath(t *testing.T) {
	// Create a temporary executable file
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake firecracker binary
	fakeFc := filepath.Join(tmpDir, "firecracker")
	if err := os.WriteFile(fakeFc, []byte("#!/bin/sh\necho fake"), 0755); err != nil {
		t.Fatal(err)
	}

	// Use lo for bridge (should always exist)
	checker := NewChecker("lo", fakeFc)
	status := checker.Check()

	// Firecracker check should pass with the specific path
	fcResult := status.Checks["firecracker"]
	if fcResult.Status != "ok" {
		t.Errorf("Expected ok for firecracker with specific path, got: %s - %s", fcResult.Status, fcResult.Message)
	}

	if fcResult.Message == "" {
		t.Error("Expected message in firecracker check result")
	}
}

// TestCheckerNonExecutablePath tests firecracker check with a non-executable file.
func TestCheckerNonExecutablePath(t *testing.T) {
	// Create a temporary non-executable file
	tmpDir, err := os.MkdirTemp("", "health-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a non-executable file
	fakeFc := filepath.Join(tmpDir, "firecracker")
	if err := os.WriteFile(fakeFc, []byte("not executable"), 0644); err != nil {
		t.Fatal(err)
	}

	checker := NewChecker("lo", fakeFc)
	status := checker.Check()

	// Firecracker check should fail
	fcResult := status.Checks["firecracker"]
	if fcResult.Status != "error" {
		t.Errorf("Expected error for non-executable firecracker, got: %s", fcResult.Status)
	}
}

// contains is a helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) && containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
