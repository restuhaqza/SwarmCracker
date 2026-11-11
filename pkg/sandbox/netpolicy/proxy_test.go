package netpolicy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"
)

// These are the parts of an endpoint that do not need a namespace: the relay, the
// DNS stream's framing and the lifecycle. Entering a namespace needs a kernel and a
// privilege, so the sockets that live inside one are proved on a KVM host
// (test/integration); what is proved here is that the bytes a relay carries are the
// same bytes on both sides, and that a closed proxy leaves nothing running.

// testProxy is a proxy with nothing listening: its relay can be driven with a
// connection a test owns.
func testProxy() *proxy {
	return &proxy{
		dialer:  net.Dialer{Timeout: 5 * time.Second},
		conns:   make(map[net.Conn]struct{}),
		stopped: make(chan struct{}),
	}
}

// echoServer is a destination that reads its whole request before it answers, which
// is what a protocol that ends with a shutdown does (HTTP/1.0, a client that sends
// its request and waits). It answers only when it has seen EOF, so a relay that
// closes both directions on the first EOF — instead of half-closing — never delivers
// this answer.
func echoServer(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				request, err := io.ReadAll(conn)
				if err != nil {
					return
				}
				_, _ = conn.Write(append([]byte("echo:"), request...))
			}()
		}
	}()
	return listener
}

// waitFor fails the test rather than hanging it when a relay never finishes.
func waitFor(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never finished", what)
	}
}

// TestTheRelayCarriesAPayloadBothWays is the whole contract of an egress relay: what
// the sandbox sends arrives at the destination, what the destination answers arrives
// back, and a client that half-closes after its request still gets its answer.
func TestTheRelayCarriesAPayloadBothWays(t *testing.T) {
	upstream := echoServer(t)
	p := testProxy()

	// The sandbox's side of the connection, as a real connection: the relay
	// half-closes towards the sandbox too, and a pipe's end cannot be half-closed.
	sandbox := listenerPair(t)
	client := sandbox.client
	guest := sandbox.guest

	relayed := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer close(relayed)
		p.relayTo(client, upstream.Addr().String())
	}()

	_, err := guest.Write([]byte("hello from the sandbox"))
	require.NoError(t, err)
	require.NoError(t, guest.CloseWrite(), "the sandbox half-closes after its request")

	answer, err := io.ReadAll(guest)
	require.NoError(t, err)
	assert.Equal(t, "echo:hello from the sandbox", string(answer),
		"the destination's answer arrives, so the relay half-closed instead of closing")

	waitFor(t, relayed, "the relay")
}

// connectionPair is one connection and the two ends of it: what the relay carries, and what
// the sandbox holds.
type connectionPair struct {
	client *net.TCPConn
	guest  *net.TCPConn
}

// listenerPair returns one established connection with both of its ends, so a test
// can be both the relay's client and the sandbox at the same time.
func listenerPair(t *testing.T) connectionPair {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	dialed, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	accepted, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = dialed.Close()
		_ = accepted.Close()
	})
	return connectionPair{client: dialed.(*net.TCPConn), guest: accepted.(*net.TCPConn)}
}

// TestARelayToAnUnreachableDestinationEndsTheConnection: a destination that cannot
// be dialled ends the sandbox's connection instead of holding it open, and the relay
// does not wait for a destination that never answered.
func TestARelayToAnUnreachableDestinationEndsTheConnection(t *testing.T) {
	// A port nothing listens on: the dial is refused, which is the answer this
	// test needs without waiting for a timeout.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	destination := closed.Addr().String()
	require.NoError(t, closed.Close())

	p := testProxy()
	client, sandbox := net.Pipe()
	relayed := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer close(relayed)
		p.relayTo(client, destination)
	}()

	waitFor(t, relayed, "the relay to an unreachable destination")

	_ = sandbox.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = sandbox.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "the sandbox's connection is closed, not left hanging")
	_ = sandbox.Close()
}

// TestClosingTheProxyEndsItsConnectionsAndRefusesNewOnes: the close is what lets the
// namespace go away, so it must end every connection it was carrying and leave no
// goroutine behind, and a connection offered after it belongs to nobody.
func TestClosingTheProxyEndsItsConnectionsAndRefusesNewOnes(t *testing.T) {
	p := testProxy()
	client, sandbox := net.Pipe()
	require.True(t, p.track(client))

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		p.close()
	}()
	waitFor(t, closed, "the close")

	assert.False(t, p.track(client), "a connection offered after the close is not taken")

	_ = sandbox.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := sandbox.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF, "the tracked connection was closed")
	_ = sandbox.Close()

	// A second close is a no-op, which is what makes a retried teardown safe.
	p.close()
}

// TestTheDNSStreamAnswersALengthPrefixedQuery: a resolver that falls back to TCP
// gets the same answer as one that asks over UDP, framed the way the protocol says.
func TestTheDNSStreamAnswersALengthPrefixedQuery(t *testing.T) {
	p := testProxy()
	p.view = newDNSView(viewPlan())

	client, server := net.Pipe()
	answered := make(chan struct{})
	p.wg.Add(1)
	go func() {
		defer close(answered)
		p.serveDNSStream(server)
	}()

	query := query(t, "api.example.com.", dnsmessage.TypeA)
	frame := append([]byte{byte(len(query) >> 8), byte(len(query))}, query...)
	_, err := client.Write(frame)
	require.NoError(t, err)

	var length [2]byte
	_, err = io.ReadFull(client, length[:])
	require.NoError(t, err)
	reply := make([]byte, int(length[0])<<8|int(length[1]))
	_, err = io.ReadFull(client, reply)
	require.NoError(t, err)

	var unpacked dnsmessage.Message
	require.NoError(t, unpacked.Unpack(reply))
	require.Len(t, unpacked.Answers, 2)
	resource, ok := unpacked.Answers[0].Body.(*dnsmessage.AResource)
	require.True(t, ok)
	assert.Equal(t, "198.51.100.7", netip.AddrFrom4(resource.A).String())

	_ = client.Close()
	waitFor(t, answered, "the DNS stream")
}

// TestTheListenersAreWhatServeOneSandbox: the accept loops are the whole of what a
// sandbox's endpoints do, and they can be driven here because the only thing that
// needs a namespace is where the socket is created, not how it is served.
func TestTheListenersAreWhatServeOneSandbox(t *testing.T) {
	upstream := echoServer(t)
	p := testProxy()

	// A relay: the loop accepts what the sandbox connects to and copies it to the
	// destination, both ways.
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p.relays = append(p.relays, relay)
	p.wg.Add(1)
	go p.acceptRelays(relay, upstream.Addr().String())

	sandbox, err := net.Dial("tcp", relay.Addr().String())
	require.NoError(t, err)
	guest := sandbox.(*net.TCPConn)
	_, err = guest.Write([]byte("through the loop"))
	require.NoError(t, err)
	require.NoError(t, guest.CloseWrite())

	answer, err := io.ReadAll(guest)
	require.NoError(t, err)
	assert.Equal(t, "echo:through the loop", string(answer))
	_ = guest.Close()

	// The DNS view over UDP: one query in, one answer out, on the connection the
	// proxy's own loop reads.
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	p.viewUDP = udp
	p.view = newDNSView(viewPlan())
	p.wg.Add(1)
	go p.acceptDNSQueries()

	client, err := net.DialUDP("udp4", nil, udp.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	_, err = client.Write(query(t, "api.example.com.", dnsmessage.TypeA))
	require.NoError(t, err)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))

	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	require.NoError(t, err)
	var reply dnsmessage.Message
	require.NoError(t, reply.Unpack(buf[:n]))
	assert.Len(t, reply.Answers, 2, "the view's answer comes back through the loop")
	_ = client.Close()

	// The DNS view over TCP, through its own accept loop.
	stream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p.viewTCP = stream
	p.wg.Add(1)
	go p.acceptDNSStreams()

	streamClient, err := net.Dial("tcp", stream.Addr().String())
	require.NoError(t, err)
	packed := query(t, "api.example.com.", dnsmessage.TypeA)
	_, err = streamClient.Write(append([]byte{byte(len(packed) >> 8), byte(len(packed))}, packed...))
	require.NoError(t, err)

	var length [2]byte
	_, err = io.ReadFull(streamClient, length[:])
	require.NoError(t, err)
	framed := make([]byte, int(length[0])<<8|int(length[1]))
	_, err = io.ReadFull(streamClient, framed)
	require.NoError(t, err)
	require.NoError(t, reply.Unpack(framed))
	assert.Len(t, reply.Answers, 2)
	_ = streamClient.Close()

	// The close is what a teardown runs: every listener and the read loop behind
	// it stops, and nothing of this proxy is left running.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		p.close()
	}()
	waitFor(t, closed, "the close")
}

// TestTheResolverIsTheNodesOwn: the production Resolve is the node's resolver, and it
// fails when the node cannot answer — an allowlist name that cannot be resolved is a
// refusal at attach, not a destination that silently never works.
func TestTheResolverIsTheNodesOwn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := nodeResolver(ctx, "api.example.com")
	require.Error(t, err)
}

// TestInNamespaceDoesNotRunAnythingForAMissingNamespace: entering a namespace that
// is not there fails before the work, so an endpoint is never created in the wrong
// place.
func TestInNamespaceDoesNotRunAnythingForAMissingNamespace(t *testing.T) {
	err := inNamespace("/var/run/netns/sc-netpolicy-none", func() error {
		t.Fatal("the work ran without a namespace to run it in")
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open the namespace")
}

// TestListenInReportsWhatItCouldNotListenOn: a listener that cannot be created is an
// error that names the socket, and an attach that fails here starts nothing.
func TestListenInReportsWhatItCouldNotListenOn(t *testing.T) {
	_, err := listenIn("/var/run/netns/sc-netpolicy-none", "127.0.0.1:80")
	require.Error(t, err)
}

// localSockets creates its listeners in the namespace this test runs in, and records
// the addresses it was asked for: the same code that creates a sandbox's sockets,
// driven where a unit test can hold it to account.
func localSockets(t *testing.T) (socketFactory, *[]string) {
	t.Helper()
	var asked []string
	return socketFactory{
		stream: func(_ string, address string) (net.Listener, error) {
			asked = append(asked, "tcp "+address)
			return net.Listen("tcp4", "127.0.0.1:0")
		},
		datagram: func(_ string, address string) (*net.UDPConn, error) {
			asked = append(asked, "udp "+address)
			return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		},
	}, &asked
}

// TestAnAllowlistBecomesOneRelayPerAddressAndPort: what an allowlist turns into is one
// listener per accepted (address, port) — the same pairs the ruleset accepts — so the
// endpoints and the policy cannot describe different sandboxes.
func TestAnAllowlistBecomesOneRelayPerAddressAndPort(t *testing.T) {
	sockets, asked := localSockets(t)
	n := Network{ID: "alpha", Name: "sbx-alpha", NetNS: "/var/run/netns/sbx-alpha", Gateway: netip.MustParseAddr("10.63.0.1")}

	endpoints, err := proxyFor(sockets, n, []Destination{
		{Host: "203.0.113.10", Port: 443, Addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")}},
		{Host: "api.example.com", Port: 8443, Addresses: []netip.Addr{
			netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("198.51.100.8"),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(endpoints.close)

	assert.Equal(t, []string{
		"tcp 198.51.100.7:8443",
		"tcp 198.51.100.8:8443",
		"tcp 203.0.113.10:443",
		"udp 10.63.0.1:53",
		"tcp 10.63.0.1:53",
	}, *asked, "one relay per accepted pair, in the order the rulesets name them, then the view on the sandbox's own gateway")
}

// TestOneDestinationNamedTwoWaysIsOneListener: an allowlist that names a destination
// as a host and as one of the addresses that host resolves to is one destination, and
// binding it twice would fail the attach on an address already in use.
func TestOneDestinationNamedTwoWaysIsOneListener(t *testing.T) {
	sockets, asked := localSockets(t)
	n := Network{ID: "alpha", Name: "sbx-alpha", NetNS: "/var/run/netns/sbx-alpha", Gateway: netip.MustParseAddr("10.63.0.1")}
	address := netip.MustParseAddr("198.51.100.7")

	endpoints, err := proxyFor(sockets, n, []Destination{
		{Host: "api.example.com", Port: 8443, Addresses: []netip.Addr{address}},
		{Host: "198.51.100.7", Port: 8443, Addresses: []netip.Addr{address}},
	})
	require.NoError(t, err)
	t.Cleanup(endpoints.close)

	assert.Equal(t, []string{"tcp 198.51.100.7:8443", "udp 10.63.0.1:53", "tcp 10.63.0.1:53"}, *asked)
}

// TestAnAllowlistOfLiteralsGetsNoView: with no name in the allowlist there is nothing
// for a resolver to answer, so the sandbox gets no resolver — which is also what the
// ruleset says, since it opens no port 53 for such a sandbox.
func TestAnAllowlistOfLiteralsGetsNoView(t *testing.T) {
	sockets, asked := localSockets(t)
	n := Network{ID: "alpha", Name: "sbx-alpha", NetNS: "/var/run/netns/sbx-alpha", Gateway: netip.MustParseAddr("10.63.0.1")}

	endpoints, err := proxyFor(sockets, n, []Destination{
		{Host: "203.0.113.10", Port: 443, Addresses: []netip.Addr{netip.MustParseAddr("203.0.113.10")}},
	})
	require.NoError(t, err)
	t.Cleanup(endpoints.close)

	assert.Equal(t, []string{"tcp 203.0.113.10:443"}, *asked)
}

// TestAFailedEndpointLeavesNothingRunning: a socket that cannot be created ends the
// attach, names what it could not create, and closes the sockets this call had already
// started — a sandbox must not keep half of its egress.
func TestAFailedEndpointLeavesNothingRunning(t *testing.T) {
	var created []net.Listener
	sockets := socketFactory{
		stream: func(_ string, address string) (net.Listener, error) {
			if strings.HasPrefix(address, "10.63.0.1:") {
				return nil, errors.New("cannot assign requested address")
			}
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			created = append(created, listener)
			return listener, err
		},
		datagram: func(_ string, _ string) (*net.UDPConn, error) {
			return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		},
	}
	n := Network{ID: "alpha", Name: "sbx-alpha", NetNS: "/var/run/netns/sbx-alpha", Gateway: netip.MustParseAddr("10.63.0.1")}

	_, err := proxyFor(sockets, n, []Destination{{
		Host:      "api.example.com",
		Port:      8443,
		Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.7")},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.63.0.1:53", "the error names the socket that could not be created")
	assert.Contains(t, err.Error(), "sbx-alpha", "and the sandbox it was for")

	require.NotEmpty(t, created)
	for _, listener := range created {
		_, err := listener.Accept()
		assert.Error(t, err, "the relay the call had already started is closed")
	}
}
