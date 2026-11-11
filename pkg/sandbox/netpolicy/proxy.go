package netpolicy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The endpoints a sandbox with an allowlist reaches, and the two namespaces they
// live in.
//
// # Two namespaces in one process
//
// Every socket here is created inside the sandbox's namespace, because that is
// where the guest's packets arrive: a listener bound to a destination's address is
// reached by the guest because the address is local *to that namespace*, and the
// namespace's own table is what accepts the connection. The outgoing half is the
// opposite: the dial to the real destination is made from the node's namespace, so
// it takes the node's own address, route and egress policy instead of asking the
// node to NAT something into the sandbox.
//
// Namespaces are per thread, so a socket is created in the right place by entering
// it on the calling thread and leaving again before anything else runs. Only the
// listener creation is pinned; the accept loops, the DNS answers and the relays run
// unpinned, and therefore dial from the namespace this process lives in.
//
// # Why there is no proxy process
//
// A separate process per sandbox would have to hand its accepted connections to a
// process that can dial, or hold a supervisor to kill it when the sandbox stops:
// an orphaned proxy is a process holding a namespace, a device and a listener for a
// sandbox that no longer exists. Here the endpoints are sockets this process owns,
// so the sandbox's own teardown closes them — the manager closes them *before* it
// removes the namespace — and a daemon that dies takes them with it.

const (
	// dialTimeout bounds how long one connection may take to reach the real
	// destination. A sandbox's client is waiting on it, and a destination that
	// cannot be reached should fail the connection rather than hang the guest.
	dialTimeout = 15 * time.Second

	// threadNetNS is the namespace of the thread that reads it. A namespace is a
	// per-thread attribute, so the process's own path (/proc/self/ns/net) is not
	// necessarily the one this goroutine runs in.
	threadNetNS = "/proc/thread-self/ns/net"

	// viewPort is where a sandbox's resolver answers, on its own gateway.
	viewPort = 53

	// dnsStreamTimeout bounds one DNS-over-TCP connection: a client that opens a
	// stream and says nothing must not hold it forever.
	dnsStreamTimeout = 30 * time.Second

	// maxDNSSize is the largest message the TCP framing can carry: its length
	// prefix is two bytes, which is also why a truncated answer is what the protocol
	// uses for anything larger.
	maxDNSSize = 65535
)

// egress is the set of endpoints one sandbox has. It is an interface because the
// manager's unit tests attach sandboxes without a namespace to enter: a fake
// records the lifecycle, and the real one is proved on a KVM host.
type egress interface {
	// close stops every endpoint. A closed egress accepts nothing, and the
	// sockets it held inside the namespace are gone, which is what lets the
	// namespace itself be removed.
	close()
}

// socketFactory creates the listening sockets an endpoint serves. Every socket it
// makes is created inside the namespace at nsPath — that is where the guest's packets
// arrive, and a socket's namespace is bound when it is created — and handed back to a
// process that has long left it.
//
// It is a value with two functions rather than a method on the proxy because creating
// a socket inside a namespace needs a kernel and a privilege, and *which* sockets an
// allowlist turns into, in what order, is exactly what a unit test can hold to
// account: the production factory enters the namespace (namespaceSockets), and a test
// drives the same code over sockets of its own.
type socketFactory struct {
	stream   func(nsPath, address string) (net.Listener, error)
	datagram func(nsPath, address string) (*net.UDPConn, error)
}

// namespaceSockets is the production factory.
var namespaceSockets = socketFactory{stream: listenIn, datagram: listenUDPIn}

// proxy is one sandbox's endpoints: a TCP relay per allowed destination address and
// port, and — when the allowlist names at least one host — the DNS view on the
// sandbox's gateway.
type proxy struct {
	sockets socketFactory

	relays  []net.Listener
	viewUDP *net.UDPConn
	viewTCP net.Listener
	view    *dnsView

	dialer net.Dialer

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	wg      sync.WaitGroup
	stopped chan struct{}
	once    sync.Once
}

// proxyFor starts the endpoints a plan describes, inside the sandbox's namespace.
//
// The order within the call is the order that leaves nothing half-open: every
// listener is created, and the caller installs the policy last, so a sandbox is
// never reported as allocated while it is still open — and never with listeners it
// cannot describe to its own table. A failure closes what this call started.
func proxyFor(sockets socketFactory, n Network, plan []Destination) (egress, error) {
	p := &proxy{
		sockets: sockets,
		dialer:  net.Dialer{Timeout: dialTimeout},
		conns:   make(map[net.Conn]struct{}),
		stopped: make(chan struct{}),
	}

	if err := p.start(n, plan); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// start creates every listener the plan needs.
//
// One listener per accepted (address, port) — the pairs the ruleset accepts, from the
// same function the rules come from — so the endpoints and the table cannot describe
// different sandboxes, and an allowlist that names the same destination twice by two
// forms (a name and one of its addresses) binds it once.
func (p *proxy) start(n Network, plan []Destination) error {
	for _, rule := range destinationRules(plan) {
		address := net.JoinHostPort(rule.address.String(), strconv.Itoa(int(rule.port)))
		listener, err := p.sockets.stream(n.NetNS, address)
		if err != nil {
			return fmt.Errorf("netpolicy: listen for %s inside %s: %w", address, n.Name, err)
		}
		p.relays = append(p.relays, listener)
		p.wg.Add(1)
		go p.acceptRelays(listener, address)
	}

	if !hasNamedDestination(plan) {
		// An allowlist of literals has no name for a view to answer, so the
		// sandbox gets no resolver at all: there is nothing this sandbox's view
		// could truthfully answer, and answering nothing is `egress: none` for
		// DNS, which is what the ruleset then enforces too.
		return nil
	}

	p.view = newDNSView(plan)
	gateway := net.JoinHostPort(n.Gateway.String(), strconv.Itoa(viewPort))

	udp, err := p.sockets.datagram(n.NetNS, gateway)
	if err != nil {
		return fmt.Errorf("netpolicy: listen for the DNS view on %s inside %s: %w", gateway, n.Name, err)
	}
	p.viewUDP = udp
	p.wg.Add(1)
	go p.acceptDNSQueries()

	tcp, err := p.sockets.stream(n.NetNS, gateway)
	if err != nil {
		return fmt.Errorf("netpolicy: listen for the DNS view over TCP on %s inside %s: %w", gateway, n.Name, err)
	}
	p.viewTCP = tcp
	p.wg.Add(1)
	go p.acceptDNSStreams()
	return nil
}

// hasNamedDestination reports whether the plan has a name for the DNS view.
func hasNamedDestination(plan []Destination) bool {
	for _, d := range plan {
		if d.named() {
			return true
		}
	}
	return false
}

// acceptRelays serves one destination: every connection the guest makes to that
// address and port is relayed to the same address and port from the node.
func (p *proxy) acceptRelays(listener net.Listener, destination string) {
	defer p.wg.Done()
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go p.relayTo(client, destination)
	}
}

// relayTo connects one guest connection to the real destination and copies both
// ways until both are finished.
//
// The dial happens here and not on the thread that created the listener: this
// goroutine is not pinned to a namespace, so its socket is created in the namespace
// this process lives in — the node's.
func (p *proxy) relayTo(client net.Conn, destination string) {
	defer p.wg.Done()
	if !p.track(client) {
		_ = client.Close()
		return
	}
	defer p.untrack(client)

	upstream, err := p.dialer.Dial("tcp", destination)
	if err != nil {
		_ = client.Close()
		return
	}
	if !p.track(upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer p.untrack(upstream)

	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Half-close rather than close: the other direction is still talking,
		// and a protocol that ends with a shutdown (HTTP, a client that sends
		// its request and waits) needs its answer to arrive.
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyHalf(upstream, client)
	go copyHalf(client, upstream)
	<-done
	<-done

	_ = client.Close()
	_ = upstream.Close()
}

// track registers a live connection so close can end it. It reports false once the
// proxy is stopping: a connection accepted in that window belongs to a sandbox
// whose egress is gone.
func (p *proxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stopped:
		return false
	default:
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *proxy) untrack(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, conn)
}

// acceptDNSQueries answers the guest's resolver on the gateway over UDP, which is
// the transport a libc resolver uses first. The answer itself is a map lookup and
// is made on this goroutine: a query cannot depend on another query, and serving
// them one at a time keeps the read loop the only thing touching the socket.
func (p *proxy) acceptDNSQueries() {
	defer p.wg.Done()
	buf := make([]byte, 1500)
	for {
		n, from, err := p.viewUDP.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if reply, ok := p.view.answer(buf[:n]); ok {
			_, _ = p.viewUDP.WriteToUDP(reply, from)
		}
	}
}

// acceptDNSStreams answers the same view over TCP, which a resolver falls back to
// when an answer is truncated and which some clients use directly.
func (p *proxy) acceptDNSStreams() {
	defer p.wg.Done()
	for {
		conn, err := p.viewTCP.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go p.serveDNSStream(conn)
	}
}

// serveDNSStream reads length-prefixed queries from one TCP connection and writes
// length-prefixed answers, for as long as the client asks.
func (p *proxy) serveDNSStream(conn net.Conn) {
	defer p.wg.Done()
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(dnsStreamTimeout))
	for {
		var length [2]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return
		}
		size := int(length[0])<<8 | int(length[1])
		if size == 0 {
			return
		}
		query := make([]byte, size)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		reply, ok := p.view.answer(query)
		if !ok {
			return
		}
		if len(reply) > maxDNSSize {
			return
		}
		frame := make([]byte, 2+len(reply))
		//nolint:gosec // G115: the length was bounded by maxDNSSize just above, which is what two bytes carry.
		binary.BigEndian.PutUint16(frame, uint16(len(reply)))
		copy(frame[2:], reply)
		if _, err := conn.Write(frame); err != nil {
			return
		}
	}
}

// close stops every endpoint and every connection it was carrying. It is the one
// step that makes the namespace removable: a socket inside a namespace is a
// reference to it, so a listener left open would keep a namespace (and the tap0 in
// it) alive after its name was removed.
func (p *proxy) close() {
	p.once.Do(func() {
		close(p.stopped)
		for _, listener := range p.relays {
			_ = listener.Close()
		}
		if p.viewUDP != nil {
			_ = p.viewUDP.Close()
		}
		if p.viewTCP != nil {
			_ = p.viewTCP.Close()
		}

		p.mu.Lock()
		for conn := range p.conns {
			_ = conn.Close()
		}
		p.mu.Unlock()

		p.wg.Wait()
	})
}

// listenIn creates one listening TCP socket inside the namespace at nsPath, and hands
// it back to a process that is no longer in that namespace.
//
// The socket's namespace is bound when it is created, so the listener keeps accepting
// in the sandbox's namespace after this thread has left it, and the connections it
// accepts are sockets of that namespace too: their replies are routed back through
// tap0 to the guest by the namespace's own route.
//
// It goes through ListenConfig so the listen is a context-aware call, like every other
// listener this project opens; a bind has no request lifetime to inherit, so the
// context is empty and says so.
func listenIn(nsPath, address string) (net.Listener, error) {
	var listener net.Listener
	listenConfig := net.ListenConfig{}
	err := inNamespace(nsPath, func() error {
		created, err := listenConfig.Listen(context.Background(), "tcp4", address)
		if err != nil {
			return err
		}
		listener = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return listener, nil
}

// listenUDPIn is listenIn for a datagram socket, which net exposes as its own type.
func listenUDPIn(nsPath, address string) (*net.UDPConn, error) {
	var conn *net.UDPConn
	err := inNamespace(nsPath, func() error {
		// The address is a literal, so this parses rather than resolves: a name
		// here would be a lookup performed inside the sandbox's namespace, which
		// has no resolver.
		addr, err := net.ResolveUDPAddr("udp4", address)
		if err != nil {
			return err
		}
		created, err := net.ListenUDP("udp4", addr)
		if err != nil {
			return err
		}
		conn = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// inNamespace runs fn on a thread that has entered the namespace at path, and leaves
// it again before returning.
//
// The section is deliberately tiny: the thread is locked to this goroutine, and
// nothing between entering and leaving may block, allocate a socket of its own or hand
// the thread to another goroutine, because the namespace is a property of the thread
// and every socket created on it would land in the sandbox.
func inNamespace(path string, fn func() error) error {
	runtime.LockOSThread()
	// A thread that could not be brought back must not be handed to another
	// goroutine: it would create the next socket in the sandbox. Keeping it locked
	// makes the runtime discard it when this goroutine returns.
	release := true
	defer func() {
		if release {
			runtime.UnlockOSThread()
		}
	}()

	here, err := os.Open(threadNetNS)
	if err != nil {
		return fmt.Errorf("read the namespace of this thread: %w", err)
	}
	defer func() { _ = here.Close() }()

	there, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open the namespace %s: %w", path, err)
	}
	defer func() { _ = there.Close() }()

	if err := unix.Setns(int(there.Fd()), unix.CLONE_NEWNET); err != nil {
		return fmt.Errorf("enter the namespace %s: %w", path, err)
	}
	ran := fn()
	if err := unix.Setns(int(here.Fd()), unix.CLONE_NEWNET); err != nil {
		release = false
		return errors.Join(ran, fmt.Errorf("leave the namespace %s: %w", path, err))
	}
	return ran
}

// egressAddresses is every address a plan needs the namespace to own, once, in the
// order they are installed.
func egressAddresses(plan []Destination) []netip.Addr {
	seen := make(map[netip.Addr]bool)
	var addresses []netip.Addr
	for _, d := range plan {
		for _, addr := range d.Addresses {
			if seen[addr] {
				continue
			}
			seen[addr] = true
			addresses = append(addresses, addr)
		}
	}
	return addresses
}
