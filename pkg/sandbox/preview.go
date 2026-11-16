package sandbox

// The preview proxy: one guest port, reached from the host on an address of the
// sandbox's own.
//
// # Why a proxy and not a forward
//
// A sandbox's guest is on a TAP inside the sandbox's own network namespace
// (pkg/sandbox/netpolicy), and that namespace has no interface on the node: the guest
// is reachable from inside its namespace and nowhere else. So a port a caller exposes
// is not published by a NAT rule or a host forward — there is nothing on the node for
// one to point at. The node runs a listener of its own, on the host's loopback, and
// relays each accepted connection to the guest from *inside* the namespace
// (netpolicy.DialInNamespace), which is the one place the guest's address means
// anything. The relay is therefore a socket of the sandbox on both sides, and it dies
// with the sandbox: rm closes the listener before it frees the namespace the relay
// dials into.
//
// # The address
//
// The listener binds a loopback address derived from the sandbox's ID
// (previewAddress), with the guest's own port. Two properties fall out of that, and
// both are what a caller asks for: the address is *stable* — the same sandbox always
// gets the same one, with no allocator state to lose — and it is *distinct* — two
// sandboxes exposing the same guest port listen on two different addresses, so neither
// can answer for the other. Keeping the guest's port means a service that prints its
// own URL prints something that is still true from the host.
//
// # What authorizes a request
//
// Exposing a port puts a guest's service on the host's loopback, which is the machine
// the node and every other tenant's tooling runs on, so the listener is not open: it
// requires a preview token, minted when the port is exposed and printed once. The check
// is at the HTTP layer, because a preview is a link a person opens: the request head is
// read (bounded), the token is taken from `?token=` or an `Authorization: Bearer`
// header, and the connection is relayed only if it matches the digest the record keeps.
// A request without it is answered 401. Nothing is rewritten: the request reaches the
// guest byte for byte, so the guest sees exactly what the client sent.
//
// An exposure also expires. Once it has, the listener stays and answers 410 Gone to
// every request until the port is un-exposed or the sandbox is removed — the listener
// is the thing that can say "this was here and is gone", and a caller that arrives
// late learns that rather than a connection refused that says nothing.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/restuhaqza/swarmcracker/pkg/sandbox/netpolicy"
)

const (
	// previewHeadCap bounds the HTTP request head the proxy reads before it decides:
	// a head larger than this is refused rather than buffered, so a client cannot grow
	// the node's memory with one connection.
	previewHeadCap = 32 << 10

	// previewHeadTimeout bounds how long a client may take to send that head. A
	// connection that says nothing is closed rather than held.
	previewHeadTimeout = 10 * time.Second

	// previewTokenBytes is how much entropy a preview token carries. It is a bearer
	// credential for a guest service, so it is a full 32 bytes, hex encoded.
	previewTokenBytes = 32
)

// ErrExposed means the sandbox already exposes that guest port. An exposure is one
// listener per port, so a second one is a conflict rather than a replacement.
var ErrExposed = errors.New("sandbox: that port is already exposed")

// ErrNotExposed means the sandbox has no exposure for that guest port.
var ErrNotExposed = errors.New("sandbox: that port is not exposed")

// Exposure is one exposed guest port as its caller learns it: the record's own view of
// the exposure, plus the token — which exists in this value and in the proxy's memory,
// and nowhere on disk.
type Exposure struct {
	ExposedPort
	// Token is the secret a request must present. It is returned once, when the port is
	// exposed; a later read of the sandbox shows the address and the expiry only.
	Token string
	// URL is the address with the token, ready to open: what "the sandbox expose output
	// gives an authenticated URL" means.
	URL string
}

// previewAddress is the loopback address one sandbox's exposures listen on, derived
// from its ID.
//
// It is derived rather than allocated for the same reason a sandbox's MAC is
// (netpolicy.macFor): the same sandbox gets the same address with no state to keep, and
// two sandboxes get different ones. Only the host can reach it, which is what makes a
// preview a preview: the listener is not on the node's network, so exposing a port does
// not put a guest service on the wire. The second octet is forced odd so the address is
// never in 127.0.0.0/16 — 127.0.0.1 is the node's own loopback, and a preview must not
// be able to take it.
func previewAddress(id string) string {
	sum := sha256.Sum256([]byte("swarmcracker/sandbox/preview/" + id))
	return net.IPv4(127, sum[0]|0x01, sum[1], sum[2]).String()
}

// previewToken mints a preview token and the digest the record keeps.
func previewToken() (token, digest string, err error) {
	raw := make([]byte, previewTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("sandbox: mint a preview token: %w", err)
	}
	token = hex.EncodeToString(raw)
	return token, digestToken(token), nil
}

// digestToken is what a record keeps of a preview token: the token's SHA-256, hex
// encoded. A node that keeps the digest can check a token and cannot produce one.
func digestToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// preview is one exposed guest port as the node serves it: a listener on the sandbox's
// own loopback address, and the relay behind it.
type preview struct {
	sandboxID string
	guestPort uint16
	// host is the "address:port" the listener binds, and guest is the "ip:port" inside
	// the sandbox the relay dials.
	host  string
	guest string
	netns string

	// tokenHash is the digest of the token that authorizes a request, and expires is
	// when the exposure stops serving (zero never).
	tokenHash string
	expires   time.Time

	// dial is how the relay reaches the guest. Nil is netpolicy.DialInNamespace, which
	// is the real one: it enters the sandbox's namespace and dials there. It is a seam
	// for the same reason the manager's other pieces are — a unit test has no namespace
	// to enter, and "the token was checked and the request arrived unchanged" has to be
	// provable without one.
	dial func(ctx context.Context, nsPath, address string) (net.Conn, error)

	// now is when the exposure is judged: the manager's own clock, so the expiry the
	// record was stamped with and the expiry the listener enforces are one reading.
	now func() time.Time

	listener net.Listener
	// ctx is cancelled when the preview is closed, so a relay that is still dialing
	// into the namespace gives up instead of racing the namespace's removal.
	ctx    context.Context
	cancel context.CancelFunc

	stopping chan struct{}
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	once  sync.Once
}

// startPreview binds the listener and starts serving. It is bound first and the policy
// is opened by the caller, so a port is never accepted before the sandbox's own table
// describes it.
//
// The preview's context is its own and not a request's: its lifetime is the sandbox's,
// and it ends when close is called.
func startPreview(rec Record, exposed ExposedPort, dial func(ctx context.Context, nsPath, address string) (net.Conn, error), now func() time.Time) (*preview, error) {
	if dial == nil {
		dial = netpolicy.DialInNamespace
	}
	if now == nil {
		now = time.Now
	}
	host := exposed.Host()
	previewCtx, cancel := context.WithCancel(context.Background())
	// ListenConfig rather than net.Listen so the listener is bound to the preview's own
	// context: closing the preview cancels it, and a bind that is still in flight gives
	// up instead of leaving a listener nothing owns.
	var lc net.ListenConfig
	listener, err := lc.Listen(previewCtx, "tcp4", host)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sandbox %s: listen for the preview of guest port %d on %s: %w",
			rec.ID, exposed.GuestPort, host, err)
	}

	p := &preview{
		sandboxID: rec.ID,
		guestPort: exposed.GuestPort,
		host:      host,
		guest:     net.JoinHostPort(rec.GuestIP, strconv.Itoa(int(exposed.GuestPort))),
		netns:     rec.NetNS,
		tokenHash: exposed.TokenHash,
		expires:   exposed.ExpiresAt,
		dial:      dial,
		now:       now,
		listener:  listener,
		ctx:       previewCtx,
		cancel:    cancel,
		stopping:  make(chan struct{}),
		conns:     make(map[net.Conn]struct{}),
	}

	p.wg.Add(1)
	go p.accept()
	return p, nil
}

// accept serves the listener until the preview is closed.
func (p *preview) accept() {
	defer p.wg.Done()
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		if !p.track(conn) {
			_ = conn.Close()
			continue
		}
		p.wg.Add(1)
		go p.serve(conn)
	}
}

// serve decides one connection and, when the request is authorized, relays it to the
// guest.
//
// The order is the answer a caller gets: the head is read under a deadline, an expired
// exposure says 410 before anything else is considered, a request without the token says
// 401, and only then is the guest dialed. A guest that cannot be reached is a 502 — the
// proxy is up and the thing behind it is not, which is a different answer from "no such
// preview" and is what a caller needs to tell those apart.
func (p *preview) serve(client net.Conn) {
	defer p.wg.Done()
	defer p.untrack(client)

	head, err := readRequestHead(client)
	if err != nil {
		// Nothing was read that could be answered: a client that sent no complete
		// request head is not speaking this protocol.
		_ = writePreviewStatus(client, http.StatusBadRequest, "Bad Request", "the preview proxy expects an HTTP request")
		_ = client.Close()
		return
	}

	request, err := parseRequestHead(head)
	if err != nil {
		// A complete head the proxy cannot read as an HTTP request is not a request
		// without a token: it is not this protocol.
		_ = writePreviewStatus(client, http.StatusBadRequest, "Bad Request", "the preview proxy expects an HTTP request")
		_ = client.Close()
		return
	}

	if p.expired() {
		_ = writePreviewStatus(client, http.StatusGone, "Gone", "this preview has expired")
		_ = client.Close()
		return
	}

	if !p.authorized(request) {
		_ = writePreviewStatus(client, http.StatusUnauthorized, "Unauthorized", "this preview requires a valid token")
		_ = client.Close()
		return
	}

	upstream, err := p.dial(p.ctx, p.netns, p.guest)
	if err != nil {
		log.Debug().Err(err).Str("sandbox_id", p.sandboxID).Uint16("guest_port", p.guestPort).
			Msg("A preview could not reach its guest")
		_ = writePreviewStatus(client, http.StatusBadGateway, "Bad Gateway", "the sandbox's guest did not accept the connection")
		_ = client.Close()
		return
	}
	if !p.track(upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer p.untrack(upstream)

	// The request goes on byte for byte, the head included: the token was checked, not
	// consumed, so the guest sees exactly what the client sent.
	if _, err := upstream.Write(head); err != nil {
		// The guest took the connection and then went away before the request was all
		// there: the answer is the same as for a guest that never accepted it.
		_ = writePreviewStatus(client, http.StatusBadGateway, "Bad Gateway", "the sandbox's guest did not take the request")
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	relay(client, upstream)
}

// relay copies one connection to another in both directions until both are finished.
// Each direction is half-closed rather than closed, so a protocol that ends with a
// shutdown (an HTTP client that sends its request and waits) gets its answer.
func relay(client, upstream net.Conn) {
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyHalf(upstream, client)
	go copyHalf(client, upstream)
	<-done
	<-done
	_ = upstream.Close()
	_ = client.Close()
}

// expired reports whether the exposure has stopped serving.
func (p *preview) expired() bool {
	return !p.expires.IsZero() && !p.now().Before(p.expires)
}

// authorized reports whether a request carries this exposure's token, in the query
// string or as a bearer credential.
//
// The comparison is over digests and in constant time: a preview token is checked on a
// path an unauthenticated caller can reach, so neither the length of a wrong guess nor
// the position of the first byte that differs may be observable.
func (p *preview) authorized(request *http.Request) bool {
	if p.tokenHash == "" {
		// An exposure with no digest is one this node cannot check — a record from a
		// build that did not keep one. It serves nobody rather than everybody.
		return false
	}
	presented := requestToken(request)
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(digestToken(presented)), []byte(p.tokenHash)) == 1
}

// requestToken is the token a request presents, as a query parameter or a bearer
// credential, or "" when it presents none.
func requestToken(request *http.Request) string {
	if token := request.URL.Query().Get("token"); token != "" {
		return token
	}
	if bearer, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(bearer)
	}
	return ""
}

// parseRequestHead reads one request head as a request, without consuming a body: the head
// is complete by construction (readRequestHead returns it that way), so what is parsed is
// the request line and the headers, and the body — if the client sent one — is still on the
// connection, which is what lets the request be relayed byte for byte.
func parseRequestHead(head []byte) (*http.Request, error) {
	return http.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
}

// readRequestHead reads one HTTP request head — its request line and headers, up to and
// including the empty line — and returns it exactly as it arrived, so a caller can check
// it and then forward it unchanged.
//
// It is bounded twice: by previewHeadCap on the bytes and by previewHeadTimeout on the
// time. A client that sends neither a complete head nor anything at all is closed rather
// than held. The deadline is cleared before returning, because the caller is about to
// use the same connection for a stream that may legitimately be idle.
func readRequestHead(conn net.Conn) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(previewHeadTimeout)); err != nil {
		return nil, err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	for {
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if head, ok := splitRequestHead(buf); ok {
				return head, nil
			}
			if len(buf) > previewHeadCap {
				return nil, errors.New("the request head is larger than a preview request may be")
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

// splitRequestHead returns the request head in buf, once it is complete, and reports
// whether it is. Both line endings are accepted: HTTP is CRLF, and a client that sends
// LF alone is still sending a request the guest may well answer.
func splitRequestHead(buf []byte) ([]byte, bool) {
	if i := bytes.Index(buf, []byte("\r\n\r\n")); i >= 0 {
		return buf[:i+4], true
	}
	if i := bytes.Index(buf, []byte("\n\n")); i >= 0 {
		return buf[:i+2], true
	}
	return nil, false
}

// writePreviewStatus answers a connection the proxy did not relay, in the smallest
// shape HTTP allows: a status, a reason, and a line of explanation.
func writePreviewStatus(conn net.Conn, status int, reason, message string) error {
	body := message + "\n"
	_, err := fmt.Fprintf(conn,
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, reason, len(body), body)
	return err
}

// track registers a live connection so close can end it. It reports false once the
// preview is stopping: a connection accepted in that window belongs to a sandbox whose
// preview is going away.
func (p *preview) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.stopping:
		return false
	default:
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *preview) untrack(conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, conn)
}

// close stops the listener and every connection it is serving, and returns once the
// relays are done.
//
// Closing the connections is what makes this a teardown rather than a leak: a relay's
// sockets belong to the sandbox's namespace, and a namespace that still has a socket in
// it outlives its name — the namespace can be removed only after this returns, which is
// why rm closes the previews before it detaches the network.
func (p *preview) close() {
	p.once.Do(func() {
		close(p.stopping)
		p.cancel()
		_ = p.listener.Close()

		p.mu.Lock()
		conns := make([]net.Conn, 0, len(p.conns))
		for conn := range p.conns {
			conns = append(conns, conn)
		}
		p.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	p.wg.Wait()
}

// Expose serves one guest port of a ready sandbox on the host, through the preview
// proxy, and returns the exposure with the token that opens it.
//
// The steps are the ones that leave nothing half-done: the listener is bound, then the
// sandbox's own table is opened for the port (so a connection that arrives between the
// two is refused by the policy rather than relayed), then the record is written. A
// failure at either of the first two puts back what it took.
//
// A sandbox must be ready: a failed or half-built sandbox has no guest to relay to, and
// exposing one would be a listener that answers 502 forever. ttl is how long the
// exposure is served for: zero means the node's own default, and a negative value means
// it never expires.
func (m *Manager) Expose(ctx context.Context, id string, guestPort uint16, ttl time.Duration) (Exposure, error) {
	if guestPort == 0 {
		return Exposure{}, fmt.Errorf("%w: port 0 is not a port", ErrInvalid)
	}
	rec, err := m.Get(id)
	if err != nil {
		return Exposure{}, err
	}
	if rec.State != StateReady {
		return Exposure{}, fmt.Errorf("%w: %s is %s, and only a ready sandbox has a guest to reach", ErrNotReady, rec.ID, rec.State)
	}
	if rec.NetNS == "" || rec.GuestIP == "" {
		return Exposure{}, fmt.Errorf("%w: %s has no network to relay into", ErrInvalid, rec.ID)
	}
	if _, found := rec.exposedPort(guestPort); found {
		return Exposure{}, fmt.Errorf("%w: %s already exposes guest port %d", ErrExposed, rec.ID, guestPort)
	}

	ttl = m.previewTTL(ttl)
	now := m.now().UTC()
	exposed := ExposedPort{
		GuestPort: guestPort,
		Address:   previewAddress(rec.ID),
		Created:   now,
	}
	if ttl > 0 {
		exposed.ExpiresAt = now.Add(ttl)
	}
	token, digest, err := previewToken()
	if err != nil {
		return Exposure{}, err
	}
	exposed.TokenHash = digest

	// The listener exists before the policy that describes it, and the policy before the
	// record that advertises it, so at no point does a caller read an exposure that does
	// not serve and no connection is accepted that the table does not describe.
	before := rec.exposedPorts()
	proxy, err := startPreview(rec, exposed, m.cfg.DialGuest, m.now)
	if err != nil {
		return Exposure{}, err
	}
	ports := append(append([]uint16(nil), before...), guestPort)
	if err := m.cfg.Network.SetInbound(ctx, rec.NetNS, rec.GuestIP, ports); err != nil {
		proxy.close()
		return Exposure{}, err
	}

	rec.Ports = append(rec.Ports, exposed)
	rec.Updated = now
	if err := m.cfg.Store.Put(rec); err != nil {
		_ = m.cfg.Network.SetInbound(ctx, rec.NetNS, rec.GuestIP, before)
		proxy.close()
		return Exposure{}, err
	}

	m.recordPreview(rec.ID, proxy)

	log.Info().Str("sandbox_id", rec.ID).Uint16("guest_port", guestPort).
		Str("host", exposed.Host()).Time("expires_at", exposed.ExpiresAt).
		Msg("Sandbox port exposed through the preview proxy")

	return Exposure{ExposedPort: exposed, Token: token, URL: exposed.URL() + "?token=" + token}, nil
}

// Unexpose stops serving one exposed guest port: the listener is closed, the sandbox's
// table stops accepting the port, and the record stops naming it. It is idempotent —
// un-exposing a port that is not exposed is a refusal a caller can act on, and one whose
// listener is already gone is still taken out of the record.
func (m *Manager) Unexpose(ctx context.Context, id string, guestPort uint16) error {
	rec, err := m.Get(id)
	if err != nil {
		return err
	}
	if _, found := rec.exposedPort(guestPort); !found {
		return fmt.Errorf("%w: %s does not expose guest port %d", ErrNotExposed, rec.ID, guestPort)
	}

	// The listener goes first: nothing new is accepted while the table and the record are
	// being brought into line with it.
	m.closePreview(id, guestPort)

	ports := make([]uint16, 0, len(rec.Ports))
	kept := make([]ExposedPort, 0, len(rec.Ports))
	for _, p := range rec.Ports {
		if p.GuestPort == guestPort {
			continue
		}
		ports = append(ports, p.GuestPort)
		kept = append(kept, p)
	}
	if err := m.cfg.Network.SetInbound(ctx, rec.NetNS, rec.GuestIP, ports); err != nil {
		// The table could not be narrowed, so the port is put back where it was: the
		// record still names this exposure, and a record that names one the node does not
		// serve would be the lie this whole ordering exists to avoid.
		m.restorePreview(context.WithoutCancel(ctx), rec, guestPort)
		return err
	}

	rec.Ports = kept
	rec.Updated = m.now().UTC()
	if err := m.cfg.Store.Put(rec); err != nil {
		return err
	}

	log.Info().Str("sandbox_id", rec.ID).Uint16("guest_port", guestPort).
		Msg("Sandbox port is no longer exposed")
	return nil
}

// RestorePreviews re-opens the listeners a node's records say are exposed, at startup.
//
// A preview listener is memory of the process that serves it: a restarted daemon has
// none, while its records still name exposures whose clients may still hold their URLs.
// Re-opening them from the record is what keeps the record true — the token is checked
// against a digest, so a client that kept its URL keeps working, and one that did not
// cannot recover it, which is the same promise an exposure made when it was created.
//
// A sandbox that is not ready, or that the node cannot attach, is skipped: its
// exposures are then the reconciliation pass's to clean up, not this call's to invent.
// A port whose address cannot be bound (another process took it) is reported and left in
// the record, because deleting a caller's exposure to hide a bind failure would be
// worse than saying so.
func (m *Manager) RestorePreviews(ctx context.Context) error {
	records, _, err := m.cfg.Store.List()
	if err != nil {
		return err
	}

	var problems []error
	for _, rec := range records {
		if rec.State != StateReady || len(rec.Ports) == 0 {
			continue
		}
		ports := make([]uint16, 0, len(rec.Ports))
		for _, exposed := range rec.Ports {
			proxy, err := startPreview(rec, exposed, m.cfg.DialGuest, m.now)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			m.recordPreview(rec.ID, proxy)
			ports = append(ports, exposed.GuestPort)
		}
		if err := m.cfg.Network.SetInbound(ctx, rec.NetNS, rec.GuestIP, ports); err != nil {
			problems = append(problems, fmt.Errorf("sandbox %s: re-open the exposed ports: %w", rec.ID, err))
			continue
		}
		log.Info().Str("sandbox_id", rec.ID).Int("ports", len(ports)).
			Msg("Sandbox previews are serving again")
	}
	return errors.Join(problems...)
}

// previewTTL is how long an exposure is served for: what the caller asked for, and the
// node's default where it asked for nothing. Zero means never, which is what the node's
// default of zero means too.
func (m *Manager) previewTTL(ttl time.Duration) time.Duration {
	if ttl < 0 {
		return 0
	}
	if ttl == 0 {
		return m.cfg.PreviewTTL
	}
	return ttl
}

// recordPreview registers a running listener.
func (m *Manager) recordPreview(id string, proxy *preview) {
	m.previewMu.Lock()
	defer m.previewMu.Unlock()
	if m.previews == nil {
		m.previews = make(map[string]map[uint16]*preview)
	}
	if m.previews[id] == nil {
		m.previews[id] = make(map[uint16]*preview)
	}
	if old := m.previews[id][proxy.guestPort]; old != nil {
		old.close()
	}
	m.previews[id][proxy.guestPort] = proxy
}

// restorePreview puts one record's exposure back into service. It is what a failed
// un-expose runs, so the listener and the record cannot disagree about what this node
// serves: an error is returned and the caller reports it, and the exposure is where it was.
func (m *Manager) restorePreview(ctx context.Context, rec Record, guestPort uint16) {
	exposed, found := rec.exposedPort(guestPort)
	if !found {
		return
	}
	proxy, err := startPreview(rec, exposed, m.cfg.DialGuest, m.now)
	if err != nil {
		log.Warn().Err(err).Str("sandbox_id", rec.ID).Uint16("guest_port", guestPort).
			Msg("An exposure could not be put back after a failed un-expose")
		return
	}
	m.recordPreview(rec.ID, proxy)
	if err := m.cfg.Network.SetInbound(ctx, rec.NetNS, rec.GuestIP, rec.exposedPorts()); err != nil {
		log.Warn().Err(err).Str("sandbox_id", rec.ID).Uint16("guest_port", guestPort).
			Msg("An exposure's port could not be opened again after a failed un-expose")
	}
}

// closePreview stops one exposed port's listener and forgets it.
func (m *Manager) closePreview(id string, guestPort uint16) {
	m.previewMu.Lock()
	proxy := m.previews[id][guestPort]
	if proxy != nil {
		delete(m.previews[id], guestPort)
	}
	m.previewMu.Unlock()
	if proxy != nil {
		proxy.close()
	}
}

// closePreviews stops every listener a sandbox has and reports how many it stopped. It
// is what a teardown runs before it frees the namespace those listeners relay into.
func (m *Manager) closePreviews(id string) int {
	m.previewMu.Lock()
	proxies := m.previews[id]
	delete(m.previews, id)
	m.previewMu.Unlock()

	for _, proxy := range proxies {
		proxy.close()
	}
	return len(proxies)
}
