//go:build integration

package integration

// A sandbox's egress on a real KVM host: the second slice of M3 (RFC D5, blocker B5).
//
// What this file proves, and only this: with an empty allowlist a sandbox reaches
// nothing at all — the same behaviour it had before an allowlist existed; with one
// destination allowed, the guest reaches exactly that destination's address and port
// over TCP, with a payload that arrives and an answer that comes back, and nothing
// else — not another port on the same server, not ICMP to the same address, not
// another address, and not the shared bridge the service VMs are on; a name in the
// allowlist resolves through the sandbox's own view while a name outside it does not,
// and the guest has no other resolver; the rules that say all of that live inside the
// sandbox's namespace and their counters show which path carried traffic and which
// only ever dropped; and a teardown leaves no namespace, no device, no rule, no
// endpoint and no record behind.
//
// Prerequisites are the first slice's (Linux, root, /dev/kvm, firecracker, jailer,
// iproute2, nftables, a bootable guest image that starts the guest agent), plus one
// thing: the destination a sandbox is allowed to reach is a server this test runs on
// the node's own address, so the node has to have a routable IPv4 address of its own.
// The DNS half additionally needs the node to resolve a public name; it skips with
// that reason when it cannot.
//
//	SWARMCRACKER_TEST_KERNEL=/var/tmp/m3-netns/assets/vmlinux \
//	SWARMCRACKER_TEST_ROOTFS=/var/tmp/m3-netns/assets/guest-rootfs.ext4 \
//	go test -tags=integration -run TestIntegration_SandboxEgress -v ./test/integration/

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
)

// The sandboxes this file names: one with no allowlist, one with a destination, and
// one whose allowlist carries a name.
const (
	egressSandboxDenied = "m3-edny"
	egressSandboxOne    = "m3-eone"
	egressSandboxNamed  = "m3-ename"

	// egressDNSName is the public name the DNS half resolves. It is a name this
	// test does not control, which is the point: the view's answer is the node's
	// own resolution of a name that exists in the world.
	egressDNSName = "example.com"
	// egressBlockedName is a name the allowlist does not carry. It also exists in
	// the world, so "it does not resolve" is the sandbox's view saying so and not
	// a name that never existed.
	egressBlockedName = "example.org"
)

// TestIntegration_SandboxEgressAllowlist is the slice's core: one allowlisted
// destination is reachable over TCP with a payload, and nothing else is — while a
// sandbox with no allowlist reaches nothing.
func TestIntegration_SandboxEgressAllowlist(t *testing.T) {
	requireSandboxHost(t)

	f := newNetnsFixture(t)
	before := hostState(t)
	namespacesBefore := networkNamespaceCount(t)

	// The destination: a server on the node's own address, on a port the OS picks.
	// A second server on the same address and another port is the control — it
	// answers anyone else, so a sandbox that cannot reach it is being refused by
	// its own policy rather than by a service that is not there.
	address := nodeAddress(t)
	reachable := newDestinationServer(t, address)
	unreachable := newDestinationServer(t, address)
	allowedPort := strconv.Itoa(int(reachable.port))
	otherPort := strconv.Itoa(int(unreachable.port))
	t.Logf("the sandbox is allowed %s:%s; the node's own server also answers on %s:%s",
		address, allowedPort, address, otherPort)

	denied := f.bootSandboxWith(t, egressSandboxDenied, nil)
	granted := f.bootSandboxWith(t, egressSandboxOne, []string{address.String() + ":" + allowedPort})

	// 1. An empty allowlist is what a sandbox had before this existed: no path to
	// anything, including the destination this test did allow the other sandbox.
	t.Run("an empty allowlist reaches nothing", func(t *testing.T) {
		out := denied.evidence(t, "the destination the other sandbox may reach",
			client(address.String(), allowedPort, "hello\\n", 3)+"; echo \"exit=$?\"")
		assert.NotContains(t, out, "ack", "a sandbox with no allowlist reaches no destination")
		assert.NotContains(t, out, "exit=0", "and the connection is not established")

		// The control: the server is up and answering for anyone who can reach it.
		assert.Contains(t, nodeReaches(t, address.String(), allowedPort), "ack",
			"the destination is a working server, so the sandbox's failure is its policy")

		ruleset := rulesetOf(t, denied.net.Name)
		assert.NotContains(t, ruleset, "dport 53", "a sandbox with no allowlist has no resolver either")
		assert.Zero(t, countedAccepts(chainOf(t, ruleset, "input")),
			"nothing but the namespace's own loopback is accepted")

		// The attempt is counted, so a sandbox that was silenced and one that was
		// never asked look different. Which chain counts it depends on the
		// destination: an address the namespace does not own has no route out of
		// it, so the kernel gives up before the input chain and the unreachable
		// attempts show up on the way out.
		assert.Positive(t, countedDrops(t, ruleset),
			"the attempt is counted as a drop, so a silent sandbox is distinguishable from an unused one")
	})

	// 2. The destination the allowlist names is reachable, over TCP, with a payload
	// that arrives and an answer that comes back.
	t.Run("the allowed destination is reachable", func(t *testing.T) {
		out := granted.mustRun(t, client(address.String(), allowedPort, "hello from the sandbox\\n", 5))
		t.Logf("[%s] the allowed destination answered\n%s", granted.id, indent(out))
		assert.Contains(t, out, fmt.Sprintf("%s:%d ack hello from the sandbox", address, reachable.port),
			"the payload arrived and the destination's own answer came back")
	})

	// 3. Nothing else is. Each of these has a working service behind it, or a
	// protocol the allowlist does not name, so each failure is the policy's.
	t.Run("nothing else is reachable", func(t *testing.T) {
		assert.Contains(t, nodeReaches(t, address.String(), otherPort), "ack",
			"the port the sandbox may not use is a working server as well")

		for _, tc := range []struct {
			what    string
			command string
		}{
			{"the same address on a port the allowlist does not name",
				client(address.String(), otherPort, "hello\\n", 3) + "; echo \"exit=$?\""},
			{"the service network's bridge",
				client("192.168.127.1", "4242", "hello\\n", 3) + "; echo \"exit=$?\""},
			{"the internet",
				client("1.1.1.1", "80", "hello\\n", 3) + "; echo \"exit=$?\""},
		} {
			out := granted.evidence(t, "cannot reach "+tc.what, tc.command)
			assert.NotContains(t, out, "ack", "%s must not answer", tc.what)
			assert.NotContains(t, out, "exit=0", "%s must not be established", tc.what)
		}

		// The protocol is part of the allowance: TCP to that address and port, and
		// not the address itself.
		ping := granted.evidence(t, "the address is not reachable as such",
			fmt.Sprintf("ping -c 2 -w 3 %s", address))
		assert.Contains(t, ping, "100% packet loss", "ICMP to an allowed address is not allowed")
	})

	// 4. The counters are the node's own answer about what happened: the allowed
	// pair carried packets in both directions, and everything else was dropped.
	t.Run("the counters show what passed and what was dropped", func(t *testing.T) {
		ruleset := rulesetOf(t, granted.net.Name)
		input := chainOf(t, ruleset, "input")
		output := chainOf(t, ruleset, "output")

		passed := packetsFor(t, input, fmt.Sprintf("ip daddr %s tcp dport %d", address, reachable.port))
		answered := packetsFor(t, output, fmt.Sprintf("ip saddr %s tcp sport %d", address, reachable.port))
		assert.Positive(t, passed, "the allowed connection went through the rule that allows it")
		assert.Positive(t, answered, "and the destination's answer came back through its own rule")

		assert.Positive(t, packetsFor(t, input, "drop"),
			"the attempts that were refused are counted as drops")
		assert.NotContains(t, input, fmt.Sprintf("dport %d", unreachable.port),
			"the port the allowlist does not name has no rule at all")
		assert.NotContains(t, input, "icmp", "and neither does ICMP")
		t.Logf("counters: allowed in=%d out=%d, dropped=%d", passed, answered,
			packetsFor(t, input, "drop"))
	})

	// 5. The rules live inside the sandbox's namespace and nowhere else — which is
	// what makes them unable to outlive it, leak into another sandbox or filter the
	// node's own service traffic.
	t.Run("the rules are inside the sandbox", func(t *testing.T) {
		for _, s := range []*sandbox{denied, granted} {
			ruleset := hostEvidence(t, s.net.Name+" ruleset",
				"ip", "netns", "exec", s.net.Name, sandboxNft(t), "list", "ruleset")
			assert.Contains(t, ruleset, "table inet "+netpolicy.PolicyTable(), "%s carries its own table", s.id)
			assert.Equal(t, 3, strings.Count(ruleset, "policy drop"), "%s: every chain denies", s.id)
		}
		nodeRuleset := hostEvidence(t, "the node's own ruleset", sandboxNft(t), "list", "ruleset")
		require.NotEmpty(t, nodeRuleset, "the node's ruleset must be readable, or this check says nothing")
		assert.NotContains(t, nodeRuleset, "table inet "+netpolicy.PolicyTable(),
			"a sandbox's egress rules never land on the node")
	})

	// 6. The teardown: the endpoints are closed, the namespaces go, the addresses go
	// back, and the node is as it was.
	t.Run("a teardown leaves nothing behind", func(t *testing.T) {
		for _, s := range []*sandbox{denied, granted} {
			require.NoError(t, s.launched.Stop(t.Context()), "stopping %s", s.id)
			require.NoError(t, f.policy.Detach(t.Context(), s.id), "detaching %s", s.id)

			_, found, err := f.store.Peek(s.id)
			require.NoError(t, err)
			assert.False(t, found, "%s: the record is deleted once its resources are free", s.id)
		}

		namespaces := hostEvidence(t, "the node's namespaces after the teardown", "ip", "netns", "list")
		for _, s := range []*sandbox{denied, granted} {
			assert.NotContains(t, namespaces, s.net.Name)
		}
		leftover := hostEvidence(t, "any sandbox device left on the node", "sh", "-c",
			"ip -o link show | grep -c :tap0: || true")
		assert.Equal(t, "0", strings.TrimSpace(leftover), "no sandbox's tap0 survives on the node")
		assert.Equal(t, before, hostState(t), "the node is as it was before the sandboxes")
		assert.Equal(t, namespacesBefore, networkNamespaceCount(t),
			"a namespace whose endpoints were closed is a namespace the node no longer holds")
	})
}

// TestIntegration_SandboxEgressNameAndDNSView is the name half: a name in the
// allowlist resolves through the sandbox's own view and the address it resolves to is
// reachable, while a name outside the allowlist does not resolve and the guest has no
// other resolver to ask.
func TestIntegration_SandboxEgressNameAndDNSView(t *testing.T) {
	requireSandboxHost(t)

	// The node resolves the name for the attach (the view's answers are the
	// attach's resolution), so a node that cannot resolve it has nothing to prove
	// here. It is a prerequisite like the guest image, and it is named as one.
	if _, err := net.DefaultResolver.LookupNetIP(t.Context(), "ip4", egressDNSName); err != nil {
		t.Skipf("this node cannot resolve %s, so the DNS half cannot be proved here: %v", egressDNSName, err)
	}

	f := newNetnsFixture(t)
	before := hostState(t)

	// The name on its own port: 80, with the HTTP the world answers on it.
	entry := net.JoinHostPort(egressDNSName, "80")
	s := f.bootSandboxWith(t, egressSandboxNamed, []string{entry})

	require.Len(t, s.net.Egress, 1, "the allowlist installed one destination")
	destination := s.net.Egress[0]
	require.NotEmpty(t, destination.Addresses, "a name is installed with the addresses the node resolved")

	t.Run("the allowlisted name resolves through the sandbox's own view", func(t *testing.T) {
		resolver := s.evidence(t, "the guest's resolver", "cat /etc/resolv.conf")
		assert.Contains(t, resolver, "nameserver "+s.net.Gateway.String(),
			"the guest's only resolver is its own gateway")
		assert.NotContains(t, resolver, "1.1.1.1", "and it is not the node's resolver")

		out := s.mustRun(t, "nslookup "+egressDNSName)
		t.Logf("[%s] the name resolved\n%s", s.id, indent(out))
		for _, addr := range destination.Addresses {
			assert.Contains(t, out, addr.String(), "the view answers the addresses the attach resolved")
		}

		// The resolved address is reachable on the port the allowlist names, with a
		// real payload and a real answer: the proxy is what carries it.
		request := client(destination.Addresses[0].String(), "80",
			fmt.Sprintf("GET / HTTP/1.0\\r\\nHost: %s\\r\\n\\r\\n", egressDNSName), 10)
		http := s.mustRun(t, request)
		assert.Contains(t, http, "HTTP/1.", "the destination answered through the proxy")
		t.Logf("[%s] the destination's answer\n%s", s.id, indent(firstLine(http)))
	})

	t.Run("a name outside the allowlist does not resolve", func(t *testing.T) {
		for _, name := range []string{egressBlockedName, egressDNSName + ".evil.test"} {
			code, out := s.run(t, "nslookup "+name)
			t.Logf("[%s] %s\n  exit %d\n%s", s.id, name, code, indent(out))
			assert.NotZero(t, code, "%s must not resolve", name)
			assert.Contains(t, strings.ToUpper(out), "NXDOMAIN",
				"%s is a name this sandbox's view says does not exist: %s", name, out)
		}
	})

	t.Run("the guest has no resolver but its own view", func(t *testing.T) {
		// A public resolver is not reachable from the sandbox, so asking one is
		// not a way around the view.
		code, out := s.run(t, "nslookup "+egressDNSName+" 8.8.8.8")
		t.Logf("[%s] asking a public resolver\n  exit %d\n%s", s.id, code, indent(out))
		assert.NotZero(t, code, "the sandbox cannot reach a resolver other than its view")
		assert.NotContains(t, strings.ToUpper(out), "NXDOMAIN",
			"and the failure is the resolver it could not reach, not an answer from it")
		assert.NotContains(t, out, destination.Addresses[0].String(),
			"no public resolver answered the sandbox")
	})

	t.Run("the view's own traffic is counted", func(t *testing.T) {
		ruleset := rulesetOf(t, s.net.Name)
		assert.Positive(t, packetsFor(t, chainOf(t, ruleset, "input"),
			"ip daddr "+s.net.Gateway.String()+" udp dport 53"),
			"the queries the view answered went through the rule that allows them")
		assert.Positive(t, packetsFor(t, chainOf(t, ruleset, "input"),
			fmt.Sprintf("ip daddr %s tcp dport 80", destination.Addresses[0])),
			"and so did the connection to the resolved address")
		// A refused name is not a dropped packet: the query reached the view and
		// the view answered that the name does not exist, which is what the name's
		// NXDOMAIN above is. What the counters say here is that only the allowlist's
		// own pairs carried anything at all.
		assert.NotContains(t, chainOf(t, ruleset, "input"), "dport 53 ip daddr",
			"there is no rule that opens a resolver other than the sandbox's own")
	})

	require.NoError(t, s.launched.Stop(t.Context()))
	require.NoError(t, f.policy.Detach(t.Context(), s.id))
	assert.Equal(t, before, hostState(t), "the node is as it was before the sandbox")
}

// destinationServer is a server the sandbox is allowed to reach: it lives on the
// node's own address, so the node's own stack answers it, and it answers every
// connection with the port that carried it, so a test can tell one destination from
// another.
type destinationServer struct {
	address netip.Addr
	port    uint16
}

// newDestinationServer starts one server on the address the node owns, on a port the
// kernel picks.
func newDestinationServer(t *testing.T, address netip.Addr) destinationServer {
	t.Helper()

	listener, err := net.Listen("tcp4", net.JoinHostPort(address.String(), "0"))
	require.NoError(t, err, "listening on the node's own address")
	t.Cleanup(func() { _ = listener.Close() })

	server := destinationServer{
		address: address,
		port:    uint16(listener.Addr().(*net.TCPAddr).Port),
	}
	go server.serve(listener)
	return server
}

// serve answers one line per connection with an acknowledgement that names the
// address and port it came from, and closes: a client that reads until it is closed
// sees exactly what the destination said.
func (s destinationServer) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			line, _ := bufio.NewReader(conn).ReadString('\n')
			_, _ = fmt.Fprintf(conn, "%s:%d ack %s", s.address, s.port, strings.TrimSpace(line))
		}()
	}
}

// client is a shell command that sends a payload to a destination and waits for the
// answer.
//
// The sleep is not decoration. busybox's nc exits as soon as its standard input ends,
// so a client that writes a request and closes its input never reads the answer: the
// connection would be made, the destination would answer, and the command would print
// nothing — which looks exactly like a proxy that carried nothing. Keeping the input
// open for as long as the client is willing to wait is what makes the answer visible.
func client(address, port, payload string, waitSeconds int) string {
	return fmt.Sprintf("(printf '%s'; sleep %d) | /bin/nc -w %d %s %s",
		payload, waitSeconds, waitSeconds, address, port)
}

// nodeAddress is an address of the node itself: the destination the sandboxes in this
// file are allowed to reach. It is discovered from the node's own route to the
// internet and verified to be one an allowlist may name — IPv4, not loopback, not the
// sandbox pool.
func nodeAddress(t *testing.T) netip.Addr {
	t.Helper()

	out := hostCommand(t, "ip", "-o", "-4", "route", "get", "1.1.1.1")
	fields := strings.Fields(out)
	address := netip.Addr{}
	for i, field := range fields {
		if field == "src" && i+1 < len(fields) {
			parsed, err := netip.ParseAddr(fields[i+1])
			require.NoError(t, err, "the node's own route names a source address: %s", out)
			address = parsed
		}
	}
	require.True(t, address.IsValid(), "the node has no IPv4 address of its own: %s", out)
	require.False(t, address.IsLoopback(), "a destination may not be a loopback address")
	pool := netip.MustParsePrefix("10.63.0.0/16")
	require.False(t, pool.Contains(address), "a destination may not be inside the sandbox pool")
	return address
}

// nodeReaches asks the destination from the node itself, which is the control that
// separates "nothing can reach it" from "this sandbox cannot".
func nodeReaches(t *testing.T, host, port string) string {
	t.Helper()

	out, err := exec.Command("sh", "-c", fmt.Sprintf("printf 'hello\\n' | timeout 5 nc %s %s", host, port)).CombinedOutput()
	t.Logf("[host] the destination answers the node\n  $ printf 'hello\\n' | nc %s %s\n%s", host, port, indent(string(out)))
	if err != nil {
		t.Logf("  (exit: %v)", err)
	}
	return string(out)
}

// rulesetOf reads one sandbox's ruleset from inside its namespace, with the counters
// nft keeps. The counters are how this file tells "the path was used" from "the path
// exists".
func rulesetOf(t *testing.T, name string) string {
	t.Helper()

	return hostEvidence(t, name+" ruleset", "ip", "netns", "exec", name, sandboxNft(t), "list", "ruleset")
}

// chainOf is one chain's text out of a ruleset listing, so a count is about the chain
// it belongs to: every chain ends in a `counter drop` and a reader that searched the
// whole listing would add them together.
func chainOf(t *testing.T, ruleset, chain string) string {
	t.Helper()

	start := strings.Index(ruleset, "chain "+chain+" {")
	require.GreaterOrEqual(t, start, 0, "the ruleset has no %s chain:\n%s", chain, ruleset)
	rest := ruleset[start:]
	if end := strings.Index(rest, "\n\tchain "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// packetsFor is the packet count of the counted rule in a listing that contains every
// fragment, and it fails when there is no such rule: a rule whose counter cannot be
// read is a rule this file cannot hold to account.
func packetsFor(t *testing.T, listing string, fragments ...string) int {
	t.Helper()

	for _, line := range strings.Split(listing, "\n") {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(line, fragment) {
				matched = false
				break
			}
		}
		if matched && strings.Contains(line, "counter packets") {
			return packetsIn(t, line)
		}
	}
	t.Fatalf("the ruleset has no counted rule matching %v:\n%s", fragments, listing)
	return 0
}

// packetsIn is the packet count one counted rule line carries, as nft renders it
// ("counter packets 6 bytes 343 accept").
func packetsIn(t *testing.T, line string) int {
	t.Helper()

	fields := strings.Fields(line)
	for i, field := range fields {
		if field == "packets" && i+1 < len(fields) {
			count, err := strconv.Atoi(fields[i+1])
			require.NoError(t, err, "reading the counter of %q", line)
			return count
		}
	}
	t.Fatalf("no packet count on the rule %q", line)
	return 0
}

// countedAccepts is how many counted accepts a chain holds: everything the chain lets
// through that is not the namespace's own loopback.
func countedAccepts(chain string) int {
	count := 0
	for _, line := range strings.Split(chain, "\n") {
		if strings.Contains(line, "counter packets") && strings.HasSuffix(strings.TrimSpace(line), "accept") &&
			!strings.Contains(line, `iifname "lo"`) && !strings.Contains(line, `oifname "lo"`) {
			count++
		}
	}
	return count
}

// countedDrops is every counted drop in a sandbox's table, across its chains: the rule
// that proves a sandbox was asked something and refused it is whichever chain the
// packet reached, and a destination with no route out of the namespace is refused
// before the input chain ever sees it.
func countedDrops(t *testing.T, ruleset string) int {
	t.Helper()

	total := 0
	for _, chain := range []string{"input", "forward", "output"} {
		for _, line := range strings.Split(chainOf(t, ruleset, chain), "\n") {
			if !strings.Contains(line, "counter packets") || !strings.HasSuffix(strings.TrimSpace(line), "drop") {
				continue
			}
			total += packetsIn(t, line)
		}
	}
	return total
}

// networkNamespaceCount is how many network namespaces the node currently holds, as
// the kernel is willing to show them: a teardown that left one alive — a listener
// still holding it, say — shows up here as a count that did not come back down.
func networkNamespaceCount(t *testing.T) int {
	t.Helper()

	out := hostCommand(t, "sh", "-c", "lsns -t net -o NS -n | sort -u | wc -l")
	count, err := strconv.Atoi(strings.TrimSpace(out))
	require.NoError(t, err, "reading the namespace count: %s", out)
	return count
}

// firstLine is the first line of some output, for a log that stays readable.
func firstLine(out string) string {
	if at := strings.Index(out, "\n"); at >= 0 {
		return out[:at]
	}
	return out
}
