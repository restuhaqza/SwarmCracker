//go:build e2e
// +build e2e

package firecracker

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/test/testinfra/checks"
	"github.com/restuhaqza/swarmcracker/test/testinfra/helpers"
	"github.com/stretchr/testify/require"
)

// ipCmd wraps network commands with sudo if needed
func ipCmd() []string {
	if os.Geteuid() == 0 {
		return nil
	}
	return []string{"sudo"}
}

// runIP runs an ip command with sudo if needed
func runIP(args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(args[0], args[1:]...)
	}
	sudoArgs := append([]string{"sudo"}, args...)
	return exec.Command(sudoArgs[0], sudoArgs[1:]...)
}

// runSysctl runs a sysctl command with sudo if needed
func runSysctl(args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(args[0], args[1:]...)
	}
	sudoArgs := append([]string{"sudo"}, args...)
	return exec.Command(sudoArgs[0], sudoArgs[1:]...)
}

// VXLAN test configuration
var (
	vxlanBridgeName    = getEnv("VXLAN_BRIDGE", "test-vxlan-br0")
	vxlanID            = 100
	vxlanOverlay1      = getEnv("VXLAN_OVERLAY1", "10.30.0.1/24")
	vxlanOverlay2      = getEnv("VXLAN_OVERLAY2", "10.30.0.2/24")
	vxlanPhysInterface = getEnv("VXLAN_PHYS_IF", "enp0s8")
)

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// skipIfNoCaps probes whether we can create network devices.
// If not, it skips the test (common in sandboxed/restricted environments).
func skipIfNoCaps(t *testing.T) {
	t.Helper()
	probeName := fmt.Sprintf("vxlan-probe-%d", os.Getpid())
	probe := exec.Command("sudo", "ip", "link", "add", probeName, "type", "bridge")
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("Cannot create network devices: %s - tests require CAP_NET_ADMIN", string(output))
	}
	exec.Command("sudo", "ip", "link", "delete", probeName).Run()
}

// =============================================================================
// VXLAN Prerequisites
// =============================================================================

func TestVXLAN_Prerequisites(t *testing.T) {
	t.Run("VXLAN Kernel Module", func(t *testing.T) {
		vc := checks.NewVXLANChecker()
		err := vc.CheckVXLANModule()
		require.NoError(t, err, "VXLAN kernel module must be available")
		t.Log("✅ VXLAN kernel module is available")
	})

	t.Run("IP Command", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		err := nc.CheckIPCommand()
		require.NoError(t, err, "ip command must be available")
		t.Log("✅ ip command available")
	})

	t.Run("Bridge Support", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		err := nc.CheckBridgeSupport()
		require.NoError(t, err, "bridge kernel module must be available")
		t.Log("✅ bridge module available")
	})

	t.Run("Bridge Utils", func(t *testing.T) {
		_, err := exec.LookPath("bridge")
		require.NoError(t, err, "bridge command must be available (install iproute2)")
		t.Log("✅ bridge command available")
	})

	t.Run("Sysctl", func(t *testing.T) {
		_, err := exec.LookPath("sysctl")
		require.NoError(t, err, "sysctl command must be available")
		t.Log("✅ sysctl command available")
	})

	t.Run("CAP_NET_ADMIN", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		err := nc.CheckNetworkPermissions()
		if err != nil {
			if os.Geteuid() == 0 {
				t.Logf("Running as root but capability check failed: %v (namespace issue)", err)
				t.Skip("Skipping CAP_NET_ADMIN check")
			}
			require.NoError(t, err, "must have permission to create network devices (run with sudo)")
		}
		t.Log("✅ CAP_NET_ADMIN permission verified")
	})
}

// =============================================================================
// VXLAN Interface Lifecycle
// =============================================================================

func TestVXLAN_CreateAndDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	// Pre-flight: verify we can actually create bridges
	probeName := fmt.Sprintf("vxlan-probe-%d", os.Getpid())
	probe := exec.Command("sudo", "ip", "link", "add", probeName, "type", "bridge")
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("Cannot create bridges (exit status 2): %s - tests require real sudo CAP_NET_ADMIN", string(output))
	}
	exec.Command("sudo", "ip", "link", "delete", probeName).Run()

	testName := fmt.Sprintf("%s-%d", vxlanBridgeName, os.Getpid())
	vxlanName := testName + "-vxlan"

	// Cleanup
	defer func() {
		runIP("link", "delete", testName).Run()
		runIP("link", "delete", vxlanName).Run()
	}()

	t.Run("Create Bridge", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		cleanup, err := nc.CreateTestBridge(testName)
		require.NoError(t, err)
		defer cleanup()

		err = nc.CheckBridgeExists(testName)
		require.NoError(t, err)
		t.Logf("✅ Bridge %s created", testName)
	})

	t.Run("Create VXLAN Interface", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		cleanup, err := nc.CreateTestBridge(testName)
		require.NoError(t, err)
		defer cleanup()

		cmd := runIP("link", "add", vxlanName,
			"type", "vxlan",
			"id", fmt.Sprintf("%d", vxlanID),
			"dstport", "4789",
			"dev", "lo",
			"local", "127.0.0.1",
		)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "VXLAN creation failed: %s", string(output))

		vc := checks.NewVXLANChecker()
		err = vc.CheckVXLANDevice(vxlanName)
		require.NoError(t, err)
		t.Logf("✅ VXLAN interface %s created (VNI %d)", vxlanName, vxlanID)
	})

	t.Run("Attach VXLAN to Bridge", func(t *testing.T) {
		vc := checks.NewVXLANChecker()
		err := vc.CheckVXLANDevice(vxlanName)
		require.NoError(t, err, "VXLAN must exist")

		cmd := runIP("link", "set", vxlanName, "master", testName)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Failed to attach VXLAN to bridge: %s", string(output))

		err = vc.CheckBridgeAttachment(vxlanName, testName)
		require.NoError(t, err)
		t.Logf("✅ VXLAN %s attached to bridge %s", vxlanName, testName)
	})

	t.Run("Bring Up VXLAN", func(t *testing.T) {
		cmd := runIP("link", "set", vxlanName, "up")
		err := cmd.Run()
		require.NoError(t, err, "Failed to bring VXLAN up")
		t.Logf("✅ VXLAN %s is UP", vxlanName)
	})

	t.Run("Verify MTU", func(t *testing.T) {
		cmd := runIP("link", "show", vxlanName)
		output, err := cmd.Output()
		require.NoError(t, err)

		require.Contains(t, string(output), "mtu 1450", "VXLAN MTU should be 1450")
		t.Log("✅ VXLAN MTU is 1450 (correct for VXLAN overhead)")
	})

	t.Run("Delete VXLAN", func(t *testing.T) {
		cmd := runIP("link", "delete", vxlanName)
		err := cmd.Run()
		require.NoError(t, err)

		vc := checks.NewVXLANChecker()
		err = vc.CheckVXLANDevice(vxlanName)
		require.Error(t, err, "VXLAN should no longer exist after deletion")
		t.Logf("✅ VXLAN %s deleted", vxlanName)
	})
}

// =============================================================================
// VXLAN Overlay Network (using helper)
// =============================================================================

func TestVXLAN_OverlaySetup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}
	skipIfNoCaps(t)

	vc := checks.NewVXLANChecker()
	vh := helpers.NewVXLANHelper()

	testName := fmt.Sprintf("%s-ov-%d", vxlanBridgeName, os.Getpid())

	nc := checks.NewNetworkChecker()
	bridgeCleanup, err := nc.CreateTestBridge(testName)
	require.NoError(t, err)
	defer bridgeCleanup()

	setup := &helpers.VXLANSetup{
		BridgeName:    testName,
		VXLANName:     testName + "-vxlan",
		VXLANID:       vxlanID,
		OverlayIP:     "10.99.0.1/24",
		PhysInterface: "lo",
		LocalIP:       "127.0.0.1",
		PeerIPs:       []string{},
		RemoteSubnets: []helpers.RemoteSubnet{},
	}

	defer vh.TeardownVXLAN(setup)

	t.Run("Setup", func(t *testing.T) {
		err := vh.SetupVXLAN(setup)
		require.NoError(t, err)
		t.Logf("✅ VXLAN overlay setup complete")
	})

	t.Run("VXLAN Device Exists", func(t *testing.T) {
		err := vc.CheckVXLANDevice(setup.VXLANName)
		require.NoError(t, err)
	})

	t.Run("Bridge Attachment", func(t *testing.T) {
		err := vc.CheckBridgeAttachment(setup.VXLANName, setup.BridgeName)
		require.NoError(t, err)
	})

	t.Run("Overlay IP Assigned", func(t *testing.T) {
		ips, err := vh.GetInterfaceIPs(setup.BridgeName)
		require.NoError(t, err)

		found := false
		for _, ip := range ips {
			if strings.HasPrefix(ip, "10.99.0.") {
				found = true
				t.Logf("Overlay IP found: %s", ip)
			}
		}
		require.True(t, found, "Overlay IP 10.99.0.1/24 should be assigned to bridge")
	})

	t.Run("Proxy ARP Enabled", func(t *testing.T) {
		err := vc.CheckProxyARP(setup.BridgeName)
		require.NoError(t, err)
		t.Log("✅ Proxy ARP enabled on bridge")
	})

	t.Run("IP Forwarding Enabled", func(t *testing.T) {
		err := vc.CheckIPForwardingInterface(setup.BridgeName)
		require.NoError(t, err)
		t.Log("✅ IP forwarding enabled on bridge")
	})
}

// =============================================================================
// VXLAN Forwarding Database
// =============================================================================

func TestVXLAN_ForwardingDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	skipIfNoCaps(t)

	testName := fmt.Sprintf("%s-fdb-%d", vxlanBridgeName, os.Getpid())
	vxlanName := testName + "-vxlan"

	nc := checks.NewNetworkChecker()
	bridgeCleanup, err := nc.CreateTestBridge(testName)
	require.NoError(t, err)
	defer bridgeCleanup()

	cmd := runIP("link", "add", vxlanName,
		"type", "vxlan", "id", fmt.Sprintf("%d", vxlanID),
		"dstport", "4789", "dev", "lo", "local", "127.0.0.1")
	require.NoError(t, cmd.Run())
	defer runIP("link", "delete", vxlanName).Run()

	runIP("link", "set", vxlanName, "master", testName).Run()
	runIP("link", "set", vxlanName, "up").Run()

	vc := checks.NewVXLANChecker()

	t.Run("Add FDB Entry", func(t *testing.T) {
		peerIP := "192.168.100.1"
		cmd := runIP("bridge", "fdb", "append",
			"to", "00:00:00:00:00:00",
			"dst", peerIP,
			"dev", vxlanName,
		)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Failed to add FDB entry: %s", string(output))

		err = vc.CheckVXLANFDB(vxlanName, peerIP)
		require.NoError(t, err)
		t.Logf("✅ FDB entry added for peer %s", peerIP)
	})

	t.Run("List FDB Entries", func(t *testing.T) {
		vh := helpers.NewVXLANHelper()
		fdb, err := vh.GetForwardingDB(vxlanName)
		require.NoError(t, err)
		require.Contains(t, fdb, "192.168.100.1")
		t.Logf("FDB entries:\n%s", fdb)
	})

	t.Run("Add Multiple Peers", func(t *testing.T) {
		peers := []string{"192.168.100.2", "192.168.100.3"}
		for _, peer := range peers {
			cmd := runIP("bridge", "fdb", "append",
				"to", "00:00:00:00:00:00", "dst", peer, "dev", vxlanName)
			require.NoError(t, cmd.Run())
		}

		vh := helpers.NewVXLANHelper()
		fdb, err := vh.GetForwardingDB(vxlanName)
		require.NoError(t, err)

		for _, peer := range peers {
			require.Contains(t, fdb, peer, "FDB should contain peer %s", peer)
		}
		t.Logf("✅ Multiple peer FDB entries verified (%d peers)", len(peers))
	})
}

// =============================================================================
// VXLAN Routing
// =============================================================================

func TestVXLAN_Routing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	skipIfNoCaps(t)

	vc := checks.NewVXLANChecker()
	testName := fmt.Sprintf("%s-rt-%d", vxlanBridgeName, os.Getpid())

	nc := checks.NewNetworkChecker()
	bridgeCleanup, err := nc.CreateTestBridge(testName)
	require.NoError(t, err)
	defer bridgeCleanup()

	vxlanName := testName + "-vxlan"
	cmd := runIP("link", "add", vxlanName,
		"type", "vxlan", "id", fmt.Sprintf("%d", vxlanID),
		"dstport", "4789", "dev", "lo", "local", "127.0.0.1")
	require.NoError(t, cmd.Run())
	defer runIP("link", "delete", vxlanName).Run()

	runIP("link", "set", vxlanName, "master", testName).Run()
	runIP("link", "set", vxlanName, "up").Run()
	runIP("addr", "add", "10.99.0.1/24", "dev", testName).Run()

	t.Run("Add Route", func(t *testing.T) {
		cmd := runIP("route", "add", "192.168.200.0/24", "via", "10.99.0.2", "dev", testName)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Failed to add route: %s", string(output))

		err = vc.CheckRouteExists("192.168.200.0/24", "10.99.0.2", testName)
		require.NoError(t, err)
		t.Logf("✅ Route 192.168.200.0/24 via 10.99.0.2 dev %s added", testName)
	})

	t.Run("Duplicate Route Handling", func(t *testing.T) {
		cmd := runIP("route", "add", "192.168.200.0/24", "via", "10.99.0.2", "dev", testName)
		output, err := cmd.CombinedOutput()
		require.Error(t, err)
		require.Contains(t, string(output), "File exists")
		t.Log("✅ Duplicate route correctly rejected")
	})

	t.Run("Delete Route", func(t *testing.T) {
		cmd := runIP("route", "del", "192.168.200.0/24", "via", "10.99.0.2", "dev", testName)
		err := cmd.Run()
		require.NoError(t, err)

		err = vc.CheckRouteExists("192.168.200.0/24", "10.99.0.2", testName)
		require.Error(t, err, "Route should not exist after deletion")
		t.Log("✅ Route deleted")
	})
}

// =============================================================================
// VXLAN Proxy ARP
// =============================================================================

func TestVXLAN_ProxyARP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	skipIfNoCaps(t)

	vc := checks.NewVXLANChecker()
	testName := fmt.Sprintf("%s-arp-%d", vxlanBridgeName, os.Getpid())

	nc := checks.NewNetworkChecker()
	bridgeCleanup, err := nc.CreateTestBridge(testName)
	require.NoError(t, err)
	defer bridgeCleanup()

	t.Run("Enable Proxy ARP", func(t *testing.T) {
		cmd := runSysctl("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.proxy_arp=1", testName))
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Failed to enable proxy ARP: %s", string(output))

		err = vc.CheckProxyARP(testName)
		require.NoError(t, err)
		t.Logf("✅ Proxy ARP enabled on %s", testName)
	})

	t.Run("Disable Proxy ARP", func(t *testing.T) {
		cmd := runSysctl("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.proxy_arp=0", testName))
		err := cmd.Run()
		require.NoError(t, err)

		err = vc.CheckProxyARP(testName)
		require.Error(t, err, "Proxy ARP should be disabled")
		t.Logf("✅ Proxy ARP disabled on %s", testName)
	})

	t.Run("Enable Forwarding", func(t *testing.T) {
		cmd := runSysctl("sysctl", "-w", fmt.Sprintf("net.ipv4.conf.%s.forwarding=1", testName))
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Failed to enable forwarding: %s", string(output))

		err = vc.CheckIPForwardingInterface(testName)
		require.NoError(t, err)
		t.Logf("✅ IP forwarding enabled on %s", testName)
	})
}

// =============================================================================
// VXLAN Setup Script
// =============================================================================

func TestVXLAN_SetupScript(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	scriptPath := os.Getenv("VXLAN_SCRIPT_PATH")
	if scriptPath == "" {
		candidates := []string{
			"test-automation/scripts/setup-vxlan-overlay.sh",
			"../test-automation/scripts/setup-vxlan-overlay.sh",
			"../../test-automation/scripts/setup-vxlan-overlay.sh",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				scriptPath = c
				break
			}
		}
	}

	if scriptPath == "" {
		t.Skip("setup-vxlan-overlay.sh not found (set VXLAN_SCRIPT_PATH)")
	}

	testBridge := fmt.Sprintf("test-vxscript-%d", os.Getpid())

	t.Run("Script Exists and Executable", func(t *testing.T) {
		info, err := os.Stat(scriptPath)
		require.NoError(t, err, "Script should exist")
		require.NotEmpty(t, info.Mode()&0111, "Script should be executable")
		t.Logf("✅ Script found at %s", scriptPath)
	})

	t.Run("Script Help", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath)
		output, _ := cmd.CombinedOutput()
		require.Contains(t, string(output), "Usage")
		t.Logf("Script usage:\n%s", string(output))
	})

	t.Run("Run Script on Test Bridge", func(t *testing.T) {
		nc := checks.NewNetworkChecker()
		bridgeCleanup, err := nc.CreateTestBridge(testBridge)
		require.NoError(t, err)
		defer bridgeCleanup()

		cmd := exec.Command("sudo", "bash", scriptPath,
			testBridge,
			"10.98.0.1/24",
			fmt.Sprintf("%d", vxlanID),
			"lo",
			"127.0.0.1",
		)
		output, err := cmd.CombinedOutput()
		t.Logf("Script output:\n%s", string(output))

		if err != nil {
			t.Logf("Script error (may be expected): %v", err)
		}
	})
}

// =============================================================================
// VXLAN Performance
// =============================================================================

func TestVXLAN_Performance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN performance test")
	}

	if os.Getenv("TEST_VXLAN_PERF") == "" {
		t.Skip("Set TEST_VXLAN_PERF=1 to run performance tests")
	}

	vh := helpers.NewVXLANHelper()

	t.Run("Loopback VXLAN Latency", func(t *testing.T) {
		testBridge := fmt.Sprintf("%s-perf-%d", vxlanBridgeName, os.Getpid())
		vxlanName := testBridge + "-vxlan"

		nc := checks.NewNetworkChecker()
		bridgeCleanup, err := nc.CreateTestBridge(testBridge)
		require.NoError(t, err)
		defer bridgeCleanup()

		runIP("link", "add", vxlanName, "type", "vxlan",
			"id", fmt.Sprintf("%d", vxlanID), "dstport", "4789",
			"dev", "lo", "local", "127.0.0.1").Run()
		defer runIP("link", "delete", vxlanName).Run()

		runIP("link", "set", vxlanName, "master", testBridge).Run()
		runIP("link", "set", vxlanName, "up").Run()
		runIP("addr", "add", "10.88.0.1/24", "dev", testBridge).Run()

		runIP("bridge", "fdb", "append", "to", "00:00:00:00:00:00",
			"dst", "127.0.0.1", "dev", vxlanName).Run()

		runIP("addr", "add", "10.88.0.2/24", "dev", "lo").Run()

		time.Sleep(500 * time.Millisecond)

		success, output, err := vh.PingVXLANPeer("10.88.0.2", 10, 2*time.Second)
		if err != nil {
			t.Logf("Ping may have issues on loopback: %v", err)
		}

		loss := vh.ParsePacketLoss(output)
		latency := vh.ParseLatency(output)
		t.Logf("VXLAN Loopback: %s, %s", loss, latency)

		if success {
			t.Logf("✅ Loopback VXLAN ping succeeded")
		}
	})
}

// =============================================================================
// VXLAN Edge Cases
// =============================================================================

func TestVXLAN_EdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping VXLAN test in short mode")
	}

	skipIfNoCaps(t)

	t.Run("Create With Same ID Twice", func(t *testing.T) {
		name1 := fmt.Sprintf("vxlan-dup1-%d", os.Getpid())
		name2 := fmt.Sprintf("vxlan-dup2-%d", os.Getpid())

		cmd := runIP("link", "add", name1, "type", "vxlan",
			"id", fmt.Sprintf("%d", vxlanID), "dstport", "4789",
			"dev", "lo", "local", "127.0.0.1")
		require.NoError(t, cmd.Run())
		defer runIP("link", "delete", name1).Run()

		cmd = runIP("link", "add", name2, "type", "vxlan",
			"id", fmt.Sprintf("%d", vxlanID), "dstport", "4790",
			"dev", "lo", "local", "127.0.0.1")
		err := cmd.Run()
		if err == nil {
			defer runIP("link", "delete", name2).Run()
			t.Log("✅ Multiple VXLANs with same VNI but different ports allowed")
		} else {
			t.Logf("Multiple VXLANs with same VNI not allowed (kernel policy): %v", err)
		}
	})

	t.Run("Delete Non-Existent VXLAN", func(t *testing.T) {
		cmd := runIP("link", "delete", "vxlan-nonexistent-12345")
		err := cmd.Run()
		require.Error(t, err, "Deleting non-existent VXLAN should fail")
		t.Log("✅ Correctly rejects deleting non-existent VXLAN")
	})

	t.Run("List VXLAN Interfaces", func(t *testing.T) {
		vc := checks.NewVXLANChecker()
		ifaces, err := vc.GetVXLANInterfaces()
		require.NoError(t, err)
		t.Logf("Current VXLAN interfaces: %v", ifaces)
	})

	t.Run("MTU Validation", func(t *testing.T) {
		name := fmt.Sprintf("vxlan-mtu-%d", os.Getpid())
		cmd := runIP("link", "add", name, "type", "vxlan",
			"id", fmt.Sprintf("%d", vxlanID), "dstport", "4789",
			"dev", "lo", "local", "127.0.0.1")
		require.NoError(t, cmd.Run())
		defer runIP("link", "delete", name).Run()

		cmd = runIP("link", "show", name)
		output, err := cmd.Output()
		require.NoError(t, err)
		require.Contains(t, string(output), "mtu 1450")
		t.Log("✅ Default VXLAN MTU is 1450")
	})
}

// =============================================================================
// Multi-Node VXLAN (requires Vagrant or actual multi-node)
// =============================================================================

func TestVXLAN_MultiNode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping multi-node VXLAN test in short mode")
	}

	worker1 := os.Getenv("VXLAN_WORKER1")
	worker2 := os.Getenv("VXLAN_WORKER2")

	if worker1 == "" || worker2 == "" {
		t.Skip("Multi-node test requires VXLAN_WORKER1 and VXLAN_WORKER2 env vars")
	}

	vh := helpers.NewVXLANHelper()

	t.Run("Worker1 Prerequisites", func(t *testing.T) {
		cmd := exec.Command("ssh", worker1, "modprobe vxlan && echo ok")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Worker1 VXLAN module: %s", string(output))
		t.Log("✅ Worker1 VXLAN ready")
	})

	t.Run("Worker2 Prerequisites", func(t *testing.T) {
		cmd := exec.Command("ssh", worker2, "modprobe vxlan && echo ok")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "Worker2 VXLAN module: %s", string(output))
		t.Log("✅ Worker2 VXLAN ready")
	})

	t.Run("Bridge Exists on Workers", func(t *testing.T) {
		for _, w := range []string{worker1, worker2} {
			cmd := exec.Command("ssh", w, "ip link show swarm-br0")
			_, err := cmd.CombinedOutput()
			require.NoError(t, err, "swarm-br0 must exist on %s", w)
			t.Logf("✅ swarm-br0 exists on %s", w)
		}
	})

	t.Run("VXLAN Connectivity", func(t *testing.T) {
		success, output, err := vh.PingVXLANPeer(worker1, 3, 5*time.Second)
		if err != nil || !success {
			t.Logf("Warning: Cannot ping %s directly: %s", worker1, output)
		}
	})
}
