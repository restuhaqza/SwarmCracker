//go:build integration

package integration

// A sandbox's network on a real KVM host: the first slice of M3 (RFC D5, blocker B5).
//
// What this file proves, and only this: two sandboxes run at once, each in a network
// namespace of its own with its own fixed tap0 and its own /30; neither can reach the
// other, the node's gateway, the internet or a resolver; each guest wears exactly the
// identity the node allocated for its namespace; a teardown leaves no namespace, no
// device, no rule and no record behind; and the bridge path the service VMs use is
// unaffected while all of that runs.
//
// What it deliberately does not prove, because it is not in this slice: egress
// allowlists, an egress proxy, a DNS view answering, a preview proxy, and the sandbox
// control plane that will call these functions (M5). The ruleset here allows nothing
// at all; the allowlist mode adds accepts to the same table.
//
// Prerequisites: Linux, root, /dev/kvm, firecracker, jailer, iproute2, nftables, and a
// bootable guest image that reaches a root shell on ttyS0 and starts the guest agent
// (readiness is the agent's hello, SBX-024). Point the last two at that image:
//
//	SWARMCRACKER_TEST_KERNEL=/var/tmp/m3-netns/assets/vmlinux \
//	SWARMCRACKER_TEST_ROOTFS=/var/tmp/m3-netns/assets/guest-rootfs.ext4 \
//	go test -tags=integration -run TestIntegration_SandboxNetworks -v ./test/integration/
//
// SWARMCRACKER_TEST_SCRATCH moves the working directory (default /var/tmp) and
// SWARMCRACKER_TEST_KEEP=1 keeps it after a failure, where the jail's logs and
// Firecracker's own log are worth reading.
//
// The bridge half of this file creates exactly one bridge and one TAP device, both
// named after this test, through pkg/network's own primitives, and removes both. It
// deliberately does not run NetworkManager.PrepareNetwork or Init: on a shared node
// those program host-global state (dnsmasq on the bridge, masquerade and forwarding
// rules) that a test must not add to somebody else's node. What is exercised is the
// device layer the service path is built from.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/network"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
	"github.com/restuhaqza/swarmcracker/pkg/snapshot"
	"github.com/restuhaqza/swarmcracker/pkg/vminstance"
)

// The sandboxes this test names. The IDs are also the namespace names (sbx-<id>) and
// the registry keys, so they are short, unique to this test, and of the shape the
// launcher accepts for an instance ID (runtime.CheckTaskID: no dots, no separators).
const (
	netnsSandboxA = "m3-a"
	netnsSandboxB = "m3-b"
	netnsSandboxC = "m3-c"
	netnsBridgeVM = "m3-svc"

	// netnsBridgeName and netnsBridgeSubnet are the shared-L2 bridge this test builds
	// for the service-path half, and the pool its one VM is addressed from. Both are
	// named after this test and neither exists on a node that has not run it.
	netnsBridgeName   = "sc-m3-test-br0"
	netnsBridgeSubnet = "10.64.0.0/24"
	netnsBridgeIP     = "10.64.0.1"
	netnsBridgeGuest  = "10.64.0.2"
)

// netnsFixture is one host, one guest image and one set of launchers, shared by the
// tests in this file.
type netnsFixture struct {
	scratch  string
	kernel   string
	rootfs   string
	launcher snapshot.JailedLauncher
	policy   *netpolicy.Manager
	store    *vminstance.Store
}

// sandbox is one running sandbox: the network the node allocated for it, the guest
// behind it, and the ways to ask that guest questions.
type sandbox struct {
	id       string
	net      netpolicy.Network
	launched snapshot.Launched
	guest    *agent.Client
}

// requireSandboxHost skips when this machine cannot run a namespace-per-sandbox VM.
func requireSandboxHost(t *testing.T) {
	t.Helper()

	// The jailed launcher's own prerequisites: Linux, root, KVM, firecracker, jailer.
	requireJailedHost(t)

	// iproute2 and nftables are both load-bearing (netpolicy refuses to build without
	// them), and nftables ships in /usr/sbin, which is not always on PATH.
	for _, binary := range []string{"ip", "nft", "/usr/sbin/nft"} {
		if _, err := exec.LookPath(binary); err == nil {
			return
		}
	}
	t.Skip("neither nft nor /usr/sbin/nft is available: a sandbox cannot be denied")
}

// newNetnsFixture builds the fixture and registers its own cleanup.
func newNetnsFixture(t *testing.T) *netnsFixture {
	t.Helper()

	f := &netnsFixture{
		scratch: jailedScratch(t),
		kernel:  testAsset(t, "SWARMCRACKER_TEST_KERNEL", "/usr/share/firecracker/vmlinux"),
		rootfs: testAsset(t, "SWARMCRACKER_TEST_ROOTFS",
			"/var/lib/firecracker/rootfs/rootfs.ext4", "/tmp/firecracker-rootfs.ext4"),
	}

	// The real manager: the real occupancy probe against this node's own addresses,
	// routes and namespaces, the real ip and nft binaries, and the default pool.
	policy, err := netpolicy.New(netpolicy.Config{})
	require.NoError(t, err)
	f.policy = policy

	store, err := vminstance.NewStore(vminstance.Config{Dir: vminstance.Dir(filepath.Join(f.scratch, "state"))})
	require.NoError(t, err)
	f.store = store

	f.launcher = snapshot.JailedLauncher{
		ChrootBaseDir:    filepath.Join(f.scratch, "jails"),
		FirecrackerPath:  lookPath(t, "firecracker"),
		JailerPath:       lookPath(t, "jailer"),
		ParentCgroup:     "swarmcracker-netns-test",
		MemoryMaxBytes:   1024 << 20,
		PidsMax:          64,
		LogDir:           filepath.Join(f.scratch, "logs"),
		ConsoleSocketDir: filepath.Join(f.scratch, "console"),
		UIDs:             newTestUIDAllocator(t),
		// A cold boot of this image reaches its agent in a few seconds; the hook adds
		// the metadata read and the interface work on top of it.
		ReadyTimeout:   90 * time.Second,
		PrepareTimeout: 45 * time.Second,
		Registry:       store,
	}
	return f
}

// bootSandbox attaches id's network and boots a VM inside it, through the same
// launcher the node uses, with the identity the allocation describes. It is the
// sandbox this package had before an egress allowlist existed: it reaches nothing.
func (f *netnsFixture) bootSandbox(t *testing.T, id string) *sandbox {
	t.Helper()

	return f.bootSandboxWith(t, id, nil)
}

// bootSandboxWith is bootSandbox with an egress allowlist: the same launch, with the
// destinations this sandbox may reach installed before its policy is applied.
func (f *netnsFixture) bootSandboxWith(t *testing.T, id string, allow []string) *sandbox {
	t.Helper()
	ctx := context.Background()

	allocated, err := f.policy.Attach(ctx, id, allow)
	require.NoError(t, err, "attaching the sandbox network")
	t.Cleanup(func() { _ = f.policy.Detach(context.Background(), id) })

	// A sandbox boots its own copy of the image: the guest writes to its own disk.
	disk := filepath.Join(f.scratch, id+".ext4")
	require.NoError(t, copySparse(f.rootfs, disk))

	identity := sandboxIdentity(t, allocated)
	ifaceID, hostTap, guestMAC := allocated.Interface()

	launched, err := f.launcher.Start(ctx, snapshot.LaunchRequest{
		InstanceID: id,
		Mode:       snapshot.ModeCold,
		Kernel:     f.kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		NetNS:      allocated.NetNS,
		VsockPath:  jailedAgentMuxer,

		// The interface the VMM attaches, and the identity the guest adopts once it
		// runs: address, gateway and resolver from the same allocation.
		Net:      []snapshot.NetworkInterface{{IfaceID: ifaceID, HostTap: hostTap, GuestMAC: guestMAC}},
		Identity: &identity,

		// What the node records about this instance, so the teardown that a stop runs
		// (pkg/snapshot's releaseRecord → pkg/vminstance) has the namespace and the
		// device to free.
		Record: vminstance.Instance{
			Kind:    vminstance.KindSandbox,
			NetNS:   allocated.NetNS,
			TAP:     allocated.Tap,
			GuestIP: allocated.GuestIP.String(),
			MAC:     allocated.MAC,
		},
	})
	require.NoError(t, err, "booting sandbox %q inside %s", id, allocated.Name)
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	// The launch is a guest that answered, so the identity was applied inside it:
	// the hook runs before Start returns, and the report is the guest's own answer.
	require.NotNil(t, launched.Prepared, "the launch carried an identity, so the guest reported applying it")

	return &sandbox{
		id:       id,
		net:      allocated,
		launched: launched,
		guest:    dialGuest(t, launched),
	}
}

// sandboxIdentity is the whole identity a sandbox's guest adopts: the network half
// from the allocation, and the per-clone parts the node mints (a host name, a machine
// id, fresh entropy and the clock), which is what RFC D10's identity channel carries.
func sandboxIdentity(t *testing.T, allocated netpolicy.Network) agent.Identity {
	t.Helper()

	identity := allocated.Identity()
	identity.Hostname = "sandbox-" + allocated.ID
	identity.MachineID = randomHex(t, 16)
	identity.Entropy = randomHex(t, 32)
	identity.ClockUnixNano = time.Now().UnixNano()
	return identity
}

// randomHex mints n random bytes as hex, the way the node mints a machine id and a
// clone's entropy.
func randomHex(t *testing.T, n int) string {
	t.Helper()

	raw := make([]byte, n)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return hex.EncodeToString(raw)
}

// dialGuest opens the agent session this test asks the guest its questions over: the
// same path the node uses (the instance's vsock muxer behind its jail), with no token
// because this image requires none.
func dialGuest(t *testing.T, launched snapshot.Launched) *agent.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := agent.Dial(ctx, launched.VsockPath, agent.DefaultPort)
	require.NoError(t, err, "dialling the guest agent")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// run runs one shell command in the guest and returns its exit code and its combined
// output, which is what the evidence in this file is made of.
func (s *sandbox) run(t *testing.T, command string) (int, string) {
	t.Helper()

	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := s.guest.ExecStatus(ctx, []string{"/bin/sh", "-c", command},
		agent.ExecOptions{Stdout: &out, Stderr: &errOut})
	require.NoError(t, err, "running %q in %s", command, s.id)
	return result.ExitCode, out.String() + errOut.String()
}

// mustRun runs a command the guest must complete, and returns its output.
func (s *sandbox) mustRun(t *testing.T, command string) string {
	t.Helper()

	code, out := s.run(t, command)
	require.Zero(t, code, "%s: %q exited %d:\n%s", s.id, command, code, out)
	return out
}

// evidence prints a command and its output exactly as the guest produced them, so the
// run's log is the proof rather than a summary of it.
func (s *sandbox) evidence(t *testing.T, label, command string) string {
	t.Helper()

	code, out := s.run(t, command)
	t.Logf("[%s] %s\n  $ %s\n  exit %d\n%s", s.id, label, command, code, indent(out))
	return out
}

// hostEvidence runs one host command and prints it the same way.
func hostEvidence(t *testing.T, label, name string, args ...string) string {
	t.Helper()

	out, err := exec.Command(name, args...).CombinedOutput()
	t.Logf("[host] %s\n  $ %s %s\n%s", label, name, strings.Join(args, " "), indent(string(out)))
	if err != nil {
		t.Logf("  (exit: %v)", err)
	}
	return string(out)
}

// indent lays a command's output out under its command line.
func indent(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "  | " + line
	}
	return strings.Join(lines, "\n")
}

// TestIntegration_SandboxNetworksAreIsolated is the slice's core: two sandboxes, two
// namespaces, two /30s, one fixed tap0 each, no path between them and no path out —
// and, at the end, nothing left on the node.
func TestIntegration_SandboxNetworksAreIsolated(t *testing.T) {
	requireSandboxHost(t)

	f := newNetnsFixture(t)
	before := hostState(t)

	a := f.bootSandbox(t, netnsSandboxA)
	b := f.bootSandbox(t, netnsSandboxB)

	// 1. Two sandboxes, two networks. The node's own answer, not the allocation's.
	t.Run("each sandbox has its own namespace, its own /30 and its own tap0", func(t *testing.T) {
		require.NotEqual(t, a.net.NetNS, b.net.NetNS)
		require.NotEqual(t, a.net.CIDR, b.net.CIDR)
		assert.Equal(t, netpolicy.TapName, a.net.Tap, "the device name is fixed: the checkpoint freezes it")
		assert.Equal(t, a.net.Tap, b.net.Tap, "and two sandboxes may both use it, in their own namespaces")

		namespaces := hostEvidence(t, "the node's namespaces", "ip", "netns", "list")
		for i, ns := range []netpolicy.Network{a.net, b.net} {
			other := a.net
			if i == 0 {
				other = b.net
			}
			assert.Contains(t, namespaces, ns.Name, "the namespace exists on the node")
			links := hostEvidence(t, ns.Name+" devices", "ip", "netns", "exec", ns.Name, "ip", "-br", "addr", "show")
			assert.Contains(t, links, netpolicy.TapName, "the sandbox owns a tap0 inside its namespace")
			assert.Contains(t, links, ns.Gateway.String()+"/30", "and the gateway half of its own /30")
			assert.NotContains(t, links, other.Gateway.String(),
				"neither namespace carries the other's address")
		}

		// The name is inside the namespace, not on the node: the node has no tap0.
		nodeDevices := hostEvidence(t, "the node's own devices", "ip", "-o", "link", "show")
		assert.NotContains(t, nodeDevices, ": tap0:", "a sandbox's device is not a device of the node")
		for _, ns := range []netpolicy.Network{a.net, b.net} {
			assert.NotContains(t, nodeDevices, ns.Gateway.String(), "nor is its address the node's")
		}

		t.Logf("sandbox %s: namespace %s, %s, gateway %s, guest %s, mac %s",
			a.id, a.net.Name, a.net.CIDR, a.net.Gateway, a.net.GuestIP, a.net.MAC)
		t.Logf("sandbox %s: namespace %s, %s, gateway %s, guest %s, mac %s",
			b.id, b.net.Name, b.net.CIDR, b.net.Gateway, b.net.GuestIP, b.net.MAC)
	})

	// 2. The guest wears the identity the node allocated: read in the guest, with the
	// guest's own tools, and compared with the allocation.
	t.Run("the guest carries the identity allocated for its namespace", func(t *testing.T) {
		for _, s := range []*sandbox{a, b} {
			link := s.evidence(t, "the guest's interface", "ip -o link show "+netpolicy.GuestInterface)
			addresses := s.evidence(t, "the guest's addresses", "ip -o addr show "+netpolicy.GuestInterface)
			routes := s.evidence(t, "the guest's routes", "ip route show")
			resolver := s.evidence(t, "the guest's resolver", "cat /etc/resolv.conf")

			assert.Contains(t, strings.ToLower(link), strings.ToLower(s.net.MAC),
				"the guest wears the MAC the allocation gave it")
			assert.Contains(t, addresses, s.net.GuestIP.String()+"/30",
				"and the guest address of its own /30")
			assert.NotContains(t, addresses, a.net.GuestIP.String()+" ",
				"and no other sandbox's address")
			assert.Contains(t, routes, "default via "+s.net.Gateway.String(),
				"its default route points at its own namespace's gateway")
			assert.Contains(t, resolver, "nameserver "+s.net.Gateway.String(),
				"and its resolver is that same address")

			// The launcher's own report, from inside the guest, says the same thing.
			prepared := s.launched.Prepared
			require.NotNil(t, prepared)
			assert.Equal(t, s.net.MAC, prepared.MAC)
			assert.Contains(t, prepared.Addresses, s.net.GuestIP.String()+"/30")
			assert.Equal(t, []string{s.net.Gateway.String()}, prepared.DNS)
			t.Logf("[%s] the guest's own report: mac=%s addresses=%v routes=%v dns=%v",
				s.id, prepared.MAC, prepared.Addresses, prepared.Routes, prepared.DNS)
		}
	})

	// 3. Neither sandbox can reach the other. A listener is up in B for the TCP half,
	// so the failure is a path that does not exist rather than a service that is not
	// there — and B proves its own listener works, on its own address, which is the
	// control that separates "nothing is listening" from "nothing can arrive".
	t.Run("neither sandbox can reach the other", func(t *testing.T) {
		listenPort := 18080
		b.mustRun(t, fmt.Sprintf(
			"(/bin/nc -l -p %d >/tmp/listener.out 2>&1 &); sleep 1; echo listener-started", listenPort))

		control := b.evidence(t, "the listener answers inside its own guest",
			fmt.Sprintf("printf 'ping\\n' | /bin/nc -w 3 %s %d && echo connected-locally", b.net.GuestIP, listenPort))
		assert.Contains(t, control, "connected-locally",
			"the same address and port answer from the guest that owns them")

		control = a.evidence(t, "A's own address answers A",
			fmt.Sprintf("ping -c 2 -w 3 %s", a.net.GuestIP))
		assert.Contains(t, control, "0% packet loss", "A's own network stack works")

		for _, tc := range []struct {
			name string
			from *sandbox
			to   *sandbox
		}{
			{"A cannot reach B", a, b},
			{"B cannot reach A", b, a},
		} {
			pingOut := tc.from.evidence(t, tc.name+" (ping)",
				fmt.Sprintf("ping -c 2 -w 3 %s", tc.to.net.GuestIP))
			assert.Contains(t, pingOut, "100% packet loss",
				"%s: a ping between two sandboxes must lose every packet", tc.name)

			tcpOut := tc.from.evidence(t, tc.name+" (TCP)",
				fmt.Sprintf("printf 'ping\\n' | /bin/nc -w 3 %s %d; echo \"exit=$?\"", tc.to.net.GuestIP, listenPort))
			assert.Contains(t, tcpOut, "exit=", "the attempt has an outcome to read")
			assert.NotContains(t, tcpOut, "connected-locally", "%s: nothing may answer", tc.name)
			assert.NotContains(t, tcpOut, "\nexit=0", "%s: the connection must not be established", tc.name)
		}
	})

	// 4. With no egress, there is no egress: not to the sandbox's own gateway, not to
	// the node, not to the internet, and no name resolves anywhere.
	t.Run("no egress at all", func(t *testing.T) {
		for _, s := range []*sandbox{a, b} {
			for _, target := range []struct {
				what string
				addr string
			}{
				{"its own namespace's gateway", s.net.Gateway.String()},
				{"the node's bridge address", "192.168.127.1"},
				{"the node's LAN gateway", "192.168.18.1"},
				{"the internet", "1.1.1.1"},
			} {
				out := s.evidence(t, "cannot reach "+target.what, fmt.Sprintf("ping -c 2 -w 3 %s", target.addr))
				assert.Contains(t, out, "100% packet loss",
					"%s must not reach %s (%s)", s.id, target.what, target.addr)
			}

			// DNS: the resolver in the guest's own resolver configuration, and a public
			// one, both of which the guest cannot reach. An answer would print the name
			// it resolved beside an address.
			for _, query := range []string{"nslookup example.com", "nslookup example.com 1.1.1.1"} {
				code, out := s.run(t, query)
				t.Logf("[%s] no name resolves\n  $ %s\n  exit %d\n%s", s.id, query, code, indent(out))
				assert.NotZero(t, code, "%s: %q must not resolve:\n%s", s.id, query, out)
				assert.NotContains(t, out, "Name:", "%s resolved a name through %q:\n%s", s.id, query, out)
			}
		}
	})

	// 5. The policy is what the node says it is, in both places that matter: inside the
	// sandbox, where it denies, and on the node, where it must not appear at all.
	t.Run("the ruleset is inside the sandbox and denies everything", func(t *testing.T) {
		nft := sandboxNft(t)
		for _, s := range []*sandbox{a, b} {
			ruleset := hostEvidence(t, s.net.Name+" ruleset", "ip", "netns", "exec", s.net.Name, nft, "list", "ruleset")
			assert.Contains(t, ruleset, "table inet "+netpolicy.PolicyTable(),
				"%s carries the ruleset that filters it", s.id)
			assert.Equal(t, 3, strings.Count(ruleset, "policy drop"), "%s: every chain denies", s.id)
		}
		nodeRuleset := hostEvidence(t, "the node's own ruleset", nft, "list", "ruleset")
		require.NotEmpty(t, nodeRuleset, "the node's ruleset must be readable, or this check says nothing")
		assert.NotContains(t, nodeRuleset, "table inet "+netpolicy.PolicyTable(),
			"a sandbox's policy is not the node's, and never lands in the node's ruleset")
	})

	// 6. Teardown through the paths that own it: the launcher's stop frees the
	// instance's own resources and releases the record's — the namespace, which takes
	// the device, its addresses and the ruleset with it — and the manager gives the
	// /30 back.
	t.Run("a teardown leaves nothing behind", func(t *testing.T) {
		for _, s := range []*sandbox{a, b} {
			require.NoError(t, s.launched.Stop(context.Background()), "stopping %s", s.id)
			require.NoError(t, f.policy.Detach(context.Background(), s.id), "detaching %s", s.id)

			_, found, err := f.store.Peek(s.id)
			require.NoError(t, err)
			assert.False(t, found, "%s: the record is deleted once its resources are free", s.id)
			_, exists := os.Stat(s.net.NetNS)
			assert.ErrorIs(t, exists, os.ErrNotExist, "%s: the namespace path is gone", s.id)
		}

		after := hostState(t)
		assert.Equal(t, before, after, "the node is as it was before the sandboxes")

		namespaces := hostEvidence(t, "the node's namespaces after the teardown", "ip", "netns", "list")
		for _, ns := range []netpolicy.Network{a.net, b.net} {
			assert.NotContains(t, namespaces, ns.Name)
		}
		leftover := hostEvidence(t, "any sandbox device left on the node", "sh", "-c", "ip -o link show | grep -c :tap0: || true")
		assert.Equal(t, "0", strings.TrimSpace(leftover), "no sandbox's tap0 survives on the node")

		// The released address is handed out again, which is the other half of "nothing
		// was left behind": the pool does not believe the namespace is still there.
		c := f.bootSandbox(t, netnsSandboxC)
		assert.Equal(t, a.net.CIDR, c.net.CIDR, "the released /30 went back to the pool")

		// And the whole thing is idempotent: a second teardown of a gone sandbox is
		// success, which is what a retried stop relies on.
		require.NoError(t, c.launched.Stop(context.Background()))
		require.NoError(t, f.policy.Detach(context.Background(), c.id))
		require.NoError(t, f.policy.Detach(context.Background(), c.id))
		require.NoError(t, f.policy.Detach(context.Background(), a.id))
	})
}

// TestIntegration_ServiceBridgePathStillWorks is the other half of the slice: the
// shared-L2 bridge the service VMs use keeps working while sandboxes run, and a
// sandbox is isolated from it as well.
func TestIntegration_ServiceBridgePathStillWorks(t *testing.T) {
	requireSandboxHost(t)

	f := newNetnsFixture(t)
	before := hostState(t)

	sandboxed := f.bootSandbox(t, netnsSandboxA)

	// The service path's own devices, built with the primitives that path is made of:
	// a bridge, and a TAP on it named the way the translator names one.
	bridge := netnsBridgeName
	tap := "tap-m3svc-0"
	require.NoError(t, runHostCommand("ip", "link", "add", bridge, "type", "bridge"))
	t.Cleanup(func() { _ = runHostCommand("ip", "link", "delete", bridge) })
	require.NoError(t, runHostCommand("ip", "addr", "add", netnsBridgeIP+"/24", "dev", bridge))
	require.NoError(t, runHostCommand("ip", "link", "set", bridge, "up"))

	device, err := network.CreateTAPDevice(tap, bridge)
	require.NoError(t, err, "creating the service TAP through pkg/network")
	t.Cleanup(func() { _ = network.RemoveTAPDevice(tap) })
	assert.Equal(t, tap, device.Name)

	// A VM on that bridge, launched the way every service VM is: no namespace, its
	// interface on the node's own device.
	disk := filepath.Join(f.scratch, netnsBridgeVM+".ext4")
	require.NoError(t, copySparse(f.rootfs, disk))

	allocated := netpolicy.Network{
		ID:      netnsBridgeVM,
		Gateway: netip.MustParseAddr(netnsBridgeIP),
		GuestIP: netip.MustParseAddr(netnsBridgeGuest),
		CIDR:    netip.MustParsePrefix(netnsBridgeSubnet),
		MAC:     "02:00:00:00:be:ef",
	}
	identity := sandboxIdentity(t, allocated)

	launched, err := f.launcher.Start(context.Background(), snapshot.LaunchRequest{
		InstanceID: netnsBridgeVM,
		Mode:       snapshot.ModeCold,
		Kernel:     f.kernel,
		Rootfs:     disk,
		VCPUs:      2,
		MemoryMB:   512,
		// No NetNS: the VMM runs in the node's own namespace, which is what a service
		// VM does. The launcher records an empty namespace for it.
		VsockPath: jailedAgentMuxer,
		Net:       []snapshot.NetworkInterface{{IfaceID: netpolicy.GuestInterface, HostTap: tap, GuestMAC: allocated.MAC}},
		Identity:  &identity,
		Record: vminstance.Instance{
			Kind:    vminstance.KindTask,
			TAP:     tap,
			GuestIP: allocated.GuestIP.String(),
			MAC:     allocated.MAC,
		},
	})
	require.NoError(t, err, "booting the bridge VM")
	t.Cleanup(func() { _ = launched.Stop(context.Background()) })

	record, found, err := f.store.Get(netnsBridgeVM)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, record.NetNS, "a service VM on the shared bridge is in no namespace of its own")

	guest := &sandbox{id: netnsBridgeVM, net: allocated, launched: launched, guest: dialGuest(t, launched)}

	t.Run("the bridge VM is reachable from the node, and the sandbox is not", func(t *testing.T) {
		reachable := hostEvidence(t, "the node reaches the bridge VM", "ping", "-c", "2", "-w", "3", netnsBridgeGuest)
		assert.Contains(t, reachable, "0% packet loss",
			"the shared bridge still carries a service VM's traffic")

		fromSandbox := sandboxed.evidence(t, "the sandbox cannot reach the bridge VM",
			fmt.Sprintf("ping -c 2 -w 3 %s", netnsBridgeGuest))
		assert.Contains(t, fromSandbox, "100% packet loss",
			"a sandbox is isolated from the service network too")

		toSandbox := hostEvidence(t, "the node cannot reach the sandbox's guest",
			"ping", "-c", "2", "-w", "3", sandboxed.net.GuestIP.String())
		assert.Contains(t, toSandbox, "100% packet loss",
			"and nothing on the node has a path into a sandbox")
	})

	t.Run("the sandbox does not disturb the node's own view", func(t *testing.T) {
		guest.evidence(t, "the bridge VM's identity", "ip -o addr show "+netpolicy.GuestInterface)
		links := hostEvidence(t, "the node's devices while a sandbox runs", "ip", "-o", "link", "show")
		assert.Contains(t, links, ": "+tap+":", "the service TAP is the node's own device")
		assert.NotContains(t, links, ": tap0:", "and the sandbox's is not")
	})

	// The teardown of both halves, and the node back as it was. The bridge VM's record
	// names no namespace, so its device is the one the teardown removes from the node.
	require.NoError(t, launched.Stop(context.Background()))
	require.NoError(t, sandboxed.launched.Stop(context.Background()))
	require.NoError(t, f.policy.Detach(context.Background(), sandboxed.id))
	require.NoError(t, network.RemoveTAPDevice(tap))
	require.NoError(t, runHostCommand("ip", "link", "delete", bridge))

	assert.Equal(t, before, hostState(t), "the node is as it was before the test")
}

// hostState is the node's own view of the three things a sandbox must not disturb:
// its devices, its addresses and its namespaces. It is compared before and after, so
// a leftover device or address fails the test rather than being cleaned up quietly.
//
// What is compared is the identity of each entry — the device names, the address on
// each device, the namespace names — and not the kernel's counters or a DHCP lease's
// remaining lifetime, which change while a test runs and say nothing about what is
// left behind.
func hostState(t *testing.T) string {
	t.Helper()

	links := hostCommand(t, "ip", "-o", "link", "show")
	addresses := hostCommand(t, "ip", "-o", "-4", "addr", "show")
	namespaces := hostCommand(t, "ip", "netns", "list")

	var out []string
	for _, fields := range recordFields(links) {
		// "3: wlp3s0: <BROADCAST,...>" — the name is the second field, and a veth's
		// carries its peer ("veth0@if2"), which is part of its identity.
		if len(fields) > 1 {
			out = append(out, "link "+strings.TrimSuffix(fields[1], ":"))
		}
	}
	for _, fields := range recordFields(addresses) {
		for i, field := range fields {
			if field == "inet" && i+1 < len(fields) && len(fields) > 1 {
				out = append(out, "addr "+strings.TrimSuffix(fields[1], ":")+" "+fields[i+1])
			}
		}
	}
	for _, fields := range recordFields(namespaces) {
		out = append(out, "netns "+fields[0])
	}

	sort.Strings(out)
	return strings.Join(out, "\n")
}

// recordFields splits an iproute2 listing into one field slice per record, undoing the
// backslash a long oneline record is wrapped with.
func recordFields(listing string) [][]string {
	var out [][]string
	for _, line := range strings.Split(strings.ReplaceAll(listing, "\\\n", " "), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, fields)
		}
	}
	return out
}

// hostCommand runs one node command and returns its output, failing the test when it
// cannot.
func hostCommand(t *testing.T, name string, args ...string) string {
	t.Helper()

	out, err := exec.Command(name, args...).CombinedOutput()
	require.NoError(t, err, "%s %s: %s", name, strings.Join(args, " "), out)
	return string(out)
}

// runHostCommand runs one iproute2 command on the node itself.
func runHostCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sandboxNft is the nft binary, resolved the way pkg/sandbox/netpolicy resolves it and
// for the same reason: nftables ships in /usr/sbin, which is not on every PATH, and a
// check that ran a bare "nft" would read "no such file" as "no rules" and pass while
// saying nothing.
func sandboxNft(t *testing.T) string {
	t.Helper()

	for _, candidate := range []string{"nft", "/usr/sbin/nft", "/sbin/nft"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("no nft binary: a sandbox's ruleset cannot be read")
	return ""
}
