// Package testhelpers provides utilities for network integration tests.
// These tests require root/CAP_NET_ADMIN privileges and are skipped when running unprivileged.
//
// Run integration tests with:
//
//	sudo go test ./pkg/network/... -run TestIntegration_ -v
package testhelpers

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// RequireRoot skips the test if not running as root.
// Integration tests should be run with: sudo go test -run TestIntegration_
func RequireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("Skipping: requires root privileges (run with: sudo go test -run TestIntegration_)")
	}
}

// RunCombined executes a command and returns combined output.
func RunCombined(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	return exec.Command(name, args...).CombinedOutput()
}

// RunOutput executes a command and returns stdout.
func RunOutput(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	return exec.Command(name, args...).Output()
}

// CreateNetworkNamespace creates an isolated network namespace using ip netns.
func CreateNetworkNamespace(t *testing.T, prefix string) string {
	t.Helper()

	nsName := fmt.Sprintf("%s-%d", prefix, os.Getpid())
	t.Logf("Creating network namespace: %s", nsName)

	output, err := RunCombined(t, "ip", "netns", "add", nsName)
	if err != nil {
		t.Fatalf("Failed to create netns %s: %s: %v", nsName, string(output), err)
	}

	return nsName
}

// RemoveNetworkNamespace removes a network namespace.
func RemoveNetworkNamespace(t *testing.T, nsName string) {
	t.Helper()
	RunCombined(t, "ip", "netns", "delete", nsName)
}

// RunInNetNS executes a command inside a network namespace.
func RunInNetNS(t *testing.T, nsName, name string, args ...string) ([]byte, error) {
	t.Helper()
	allArgs := append([]string{"netns", "exec", nsName, name}, args...)
	return RunCombined(t, "ip", allArgs...)
}

// CreateVethPair creates a veth pair and moves one end into a namespace.
func CreateVethPair(t *testing.T, hostEnd, nsEnd, nsName string) (string, string) {
	t.Helper()

	t.Logf("Creating veth pair: %s <-> %s (in %s)", hostEnd, nsEnd, nsName)

	// Delete existing if present
	RunCombined(t, "ip", "link", "delete", hostEnd)

	output, err := RunCombined(t, "ip", "link", "add", hostEnd, "type", "veth", "peer", "name", nsEnd)
	if err != nil {
		t.Fatalf("Failed to create veth pair: %s: %v", string(output), err)
	}

	output, err = RunCombined(t, "ip", "link", "set", nsEnd, "netns", nsName)
	if err != nil {
		RunCombined(t, "ip", "link", "delete", hostEnd)
		t.Fatalf("Failed to move %s to netns %s: %s: %v", nsEnd, nsName, string(output), err)
	}

	RunCombined(t, "ip", "link", "set", hostEnd, "up")

	return hostEnd, nsEnd
}

// SetupVethInNS configures the veth end inside the namespace with IP.
func SetupVethInNS(t *testing.T, nsName, vethName, ipAddr string) {
	t.Helper()
	RunInNetNS(t, nsName, "ip", "link", "set", vethName, "up")
	RunInNetNS(t, nsName, "ip", "addr", "add", ipAddr, "dev", vethName)
}

// CleanupLink deletes a network link.
func CleanupLink(t *testing.T, linkName string) {
	t.Helper()
	RunCombined(t, "ip", "link", "delete", linkName)
}

// LinkExists checks if a network link exists.
func LinkExists(t *testing.T, linkName string) bool {
	t.Helper()
	_, err := RunOutput(t, "ip", "link", "show", linkName)
	return err == nil
}

// LinkHasIP checks if a link has a specific IP address.
func LinkHasIP(t *testing.T, linkName, ipAddr string) bool {
	t.Helper()
	output, err := RunOutput(t, "ip", "addr", "show", linkName)
	if err != nil {
		return false
	}
	return strings.Contains(string(output), ipAddr)
}

// BridgeExists checks if a bridge exists.
func BridgeExists(t *testing.T, bridgeName string) bool {
	t.Helper()
	return LinkExists(t, bridgeName)
}

// IptablesRuleExists checks if an iptables rule exists.
func IptablesRuleExists(t *testing.T, table, chain string, args ...string) bool {
	t.Helper()
	checkArgs := []string{"-t", table, "-C", chain}
	checkArgs = append(checkArgs, args...)
	_, err := RunCombined(t, "iptables", checkArgs...)
	return err == nil
}

// CleanupIptablesRule removes an iptables rule.
func CleanupIptablesRule(t *testing.T, table, chain string, args ...string) {
	t.Helper()
	delArgs := []string{"-t", table, "-D", chain}
	delArgs = append(delArgs, args...)
	RunCombined(t, "iptables", delArgs...)
}

// RandomName generates a random name with the given prefix.
func RandomName(prefix string) string {
	// Kernel interface names limited to 15 chars
	name := fmt.Sprintf("%s%d", prefix, os.Getpid()%100000)
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

// MustParseCIDR parses a CIDR string or panics.
func MustParseCIDR(s string) *net.IPNet {
	_, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return ipNet
}
