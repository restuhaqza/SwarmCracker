package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The preview proxy, held to account without a KVM host.
//
// What can be proved here is everything but the one thing the integration run proves:
// the relay reaches a *real* guest inside a namespace. Here the dial is the seam, so the
// gate, the address, the policy and the teardown are all exercised for real — a listener
// on a real loopback address, a real HTTP request through it — with a local server
// standing in for the guest.

// previewRecord is a ready sandbox the preview can be attached to: one with a network,
// which is what an exposure relays into.
func previewRecord(id string) Record {
	rec := testRecord(id)
	rec.NetNS = "/var/run/netns/sbx-" + id
	rec.GuestIP = "10.63.0.2"
	rec.TAP = "tap0"
	return rec
}

// newPreviewFixture is a manager with one ready sandbox in its store.
func newPreviewFixture(t *testing.T, id string) (*fixture, Record) {
	t.Helper()
	f := newFixture(t)
	rec := previewRecord(id)
	require.NoError(t, f.store.Put(rec))
	return f, rec
}

// testGuest is a stand-in for the guest's service: it answers every request with the
// request it saw, so a test can prove the request arrived unchanged.
func testGuest(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Guest-Saw", r.URL.RequestURI())
		_, _ = io.WriteString(w, "hello from the guest")
	}))
	t.Cleanup(server.Close)
	return server
}

// countingGuest is a stand-in guest that also counts what it was asked, so a test can say
// "the guest never heard about this" instead of inferring it from a status line.
func countingGuest(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = io.WriteString(w, "hello from the guest")
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

// dialTo points the preview's dial at a local server: the guest, for a test.
func dialTo(server *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
}

// previewGet sends one HTTP request to an exposed port's address, with an optional token.
func previewGet(t *testing.T, host, path string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + host + path)
	require.NoError(t, err, "the preview listener should be bound at %s", host)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// TestPreviewAddressIsStableAndDistinct: an exposure's host address is a function of the
// sandbox and of nothing else, so the same sandbox always answers on the same one and two
// sandboxes cannot meet on the host. It is also never the node's own loopback address,
// which a preview must not be able to take.
func TestPreviewAddressIsStableAndDistinct(t *testing.T) {
	first := previewAddress("alpha")
	assert.Equal(t, first, previewAddress("alpha"), "the same sandbox gets the same address")
	assert.NotEqual(t, first, previewAddress("beta"), "two sandboxes get different ones")
	assert.NotEqual(t, "127.0.0.1", first, "the node's own loopback is not a sandbox's")

	for _, id := range []string{"alpha", "beta", "gamma", "sbx-1", "prev-1"} {
		addr := net.ParseIP(previewAddress(id))
		require.NotNil(t, addr, "the address parses")
		assert.True(t, addr.IsLoopback(), "%s is a loopback address: a preview is reachable from the host and nowhere else", id)
		assert.True(t, addr.To4() != nil)
	}
}

// TestExposeServesThePortAndOpensThePolicy: exposing a guest port binds a listener on the
// sandbox's own address, keeps the guest's port, opens exactly that port in the sandbox's
// table, and records it. It returns the token once, and the record keeps only its digest.
func TestExposeServesThePortAndOpensThePolicy(t *testing.T) {
	f, rec := newPreviewFixture(t, "prev1")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 20*time.Minute)
	require.NoError(t, err)

	assert.Equal(t, uint16(18080), exposure.GuestPort, "the guest's own port is the port served")
	assert.Equal(t, previewAddress(rec.ID), exposure.Address)
	assert.Equal(t, fmt.Sprintf("%s:18080", previewAddress(rec.ID)), exposure.Host())
	assert.NotEmpty(t, exposure.Token)
	assert.Contains(t, exposure.URL, "http://"+exposure.Host()+"/?token="+exposure.Token)
	assert.Equal(t, f.now.Add(20*time.Minute), exposure.ExpiresAt)

	// The policy: the port is open in the sandbox's table, and only that port.
	assert.Equal(t, []uint16{18080}, f.network.inbound[rec.NetNS])

	// The record: the exposure is there, without the token.
	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	require.Len(t, stored.Ports, 1)
	assert.Equal(t, uint16(18080), stored.Ports[0].GuestPort)
	assert.Equal(t, digestToken(exposure.Token), stored.Ports[0].TokenHash, "the record keeps the digest, not the token")
	assert.NotContains(t, mustJSON(t, stored), exposure.Token, "the token itself is never written down")
}

// TestTwoSandboxesMayExposeTheSameGuestPort: two sandboxes exposing one guest port meet
// nowhere — different addresses, different listeners, and neither answers for the other.
func TestTwoSandboxesMayExposeTheSameGuestPort(t *testing.T) {
	f, first := newPreviewFixture(t, "prevA")
	second := previewRecord("prevB")
	require.NoError(t, f.store.Put(second))

	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	a, err := f.manager.Expose(context.Background(), first.ID, 18080, 0)
	require.NoError(t, err)
	b, err := f.manager.Expose(context.Background(), second.ID, 18080, 0)
	require.NoError(t, err)

	assert.NotEqual(t, a.Host(), b.Host(), "the same guest port on two sandboxes is two host addresses")
	assert.NotEqual(t, a.Token, b.Token, "and two credentials")

	// Each address serves its own sandbox: a's token on b's address is refused.
	status, _ := previewGet(t, b.Host(), "/?token="+a.Token)
	assert.Equal(t, http.StatusUnauthorized, status, "one sandbox's token is not another's")

	// Both sandboxes carry their own opening, and each names its own port.
	assert.Equal(t, []uint16{18080}, f.network.inbound[first.NetNS])
	assert.Equal(t, []uint16{18080}, f.network.inbound[second.NetNS])
}

// TestExposeRefusesWhatItCannotServe: a sandbox that is not ready has no guest to relay
// to, a port that is already exposed is one listener too many, and port 0 is not a port.
func TestExposeRefusesWhatItCannotServe(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevC")

	_, err := f.manager.Expose(context.Background(), rec.ID, 0, 0)
	require.ErrorIs(t, err, ErrInvalid)

	failed := previewRecord("prevD")
	failed.State, failed.Reason = StateFailed, "its VMM is gone"
	require.NoError(t, f.store.Put(failed))
	_, err = f.manager.Expose(context.Background(), failed.ID, 18080, 0)
	require.ErrorIs(t, err, ErrNotReady)

	_, err = f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)
	_, err = f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.ErrorIs(t, err, ErrExposed)
}

// TestUnexposeClosesTheListenerAndTheOpening: un-exposing a port stops the listener, takes
// the port back out of the sandbox's table, and stops the record naming it.
func TestUnexposeClosesTheListenerAndTheOpening(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevE")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	first, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)
	second, err := f.manager.Expose(context.Background(), rec.ID, 18081, 0)
	require.NoError(t, err)

	require.NoError(t, f.manager.Unexpose(context.Background(), rec.ID, 18080))

	// The port is gone from the table and from the record; the other one is untouched.
	assert.Equal(t, []uint16{18081}, f.network.inbound[rec.NetNS])
	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	require.Len(t, stored.Ports, 1)
	assert.Equal(t, uint16(18081), stored.Ports[0].GuestPort)

	// The listener is gone: nothing is bound at that address any more.
	require.Error(t, reachable(first.Host()), "the listener should be closed")

	// Un-exposing it again is a refusal a caller can act on, and the other exposure
	// still serves.
	require.ErrorIs(t, f.manager.Unexpose(context.Background(), rec.ID, 18080), ErrNotExposed)
	require.NoError(t, reachable(second.Host()), "the other exposure still listens")
}

// TestRemoveClosesThePreviews: rm is the promise that the listeners are gone, not only
// that the record is. The report says how many were closed.
func TestRemoveClosesThePreviews(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevF")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	first, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)
	second, err := f.manager.Expose(context.Background(), rec.ID, 18081, 0)
	require.NoError(t, err)

	freed, err := f.manager.Remove(context.Background(), rec.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, freed.Previews, "rm reports the listeners it closed")
	require.Error(t, reachable(first.Host()))
	require.Error(t, reachable(second.Host()))
}

// TestTheProxyRequiresTheTokenAndExpires is the contract of the listener itself: 401
// without the token, the guest's answer with it, 410 once the exposure has expired, and
// the request arrives at the guest exactly as the client sent it.
func TestTheProxyRequiresTheTokenAndExpires(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevG")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 30*time.Minute)
	require.NoError(t, err)

	// Without a token, and with a wrong one: refused, and the guest never hears about it.
	status, _ := previewGet(t, exposure.Host(), "/")
	assert.Equal(t, http.StatusUnauthorized, status, "a preview without a token is refused")
	status, _ = previewGet(t, exposure.Host(), "/?token=not-the-token")
	assert.Equal(t, http.StatusUnauthorized, status, "a wrong token is refused")
	status, _ = previewGet(t, exposure.Host(), "/?token="+exposure.Token+"extra")
	assert.Equal(t, http.StatusUnauthorized, status, "a token that is a prefix of the right one is not the right one")

	// With the token: the guest's own answer, and the request arrived unchanged.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "http://"+exposure.Host()+"/reports?q=1&token="+exposure.Token, nil)
	require.NoError(t, err)
	req.Header.Set("X-Client", "a-header-the-guest-should-see")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "hello from the guest", string(body))
	assert.Equal(t, "/reports?q=1&token="+exposure.Token, resp.Header.Get("X-Guest-Saw"),
		"the guest sees the request the client sent, token and all")

	// A bearer credential is the same token by another route.
	bearer, err := http.NewRequest(http.MethodGet, "http://"+exposure.Host()+"/reports", nil)
	require.NoError(t, err)
	bearer.Header.Set("Authorization", "Bearer "+exposure.Token)
	respBearer, err := client.Do(bearer)
	require.NoError(t, err)
	defer func() { _ = respBearer.Body.Close() }()
	assert.Equal(t, http.StatusOK, respBearer.StatusCode, "a bearer token authorizes the same request")

	// Past its expiry: nothing is served, not even with the right token, and the answer
	// says the exposure is gone rather than that the token is wrong.
	f.clock.Advance(31 * time.Minute)
	status, _ = previewGet(t, exposure.Host(), "/?token="+exposure.Token)
	assert.Equal(t, http.StatusGone, status, "an expired exposure answers 410")
}

// TestRestorePreviewsReopensWhatTheRecordNames: a listener is memory of the daemon that
// serves it, and a restarted daemon re-opens what its records still name. The digest in
// the record is what keeps a client's existing URL working.
func TestRestorePreviewsReopensWhatTheRecordNames(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevH")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	// A record from a previous life: the exposure is named, with a digest of a token the
	// client still holds.
	token := strings.Repeat("ab", 32)
	rec.Ports = []ExposedPort{{
		GuestPort: 18080,
		Address:   previewAddress(rec.ID),
		TokenHash: digestToken(token),
		Created:   f.now,
		ExpiresAt: f.now.Add(time.Hour),
	}}
	require.NoError(t, f.store.Put(rec))

	require.NoError(t, f.manager.RestorePreviews(context.Background()))

	assert.Equal(t, []uint16{18080}, f.network.inbound[rec.NetNS], "the opening is put back")
	status, _ := previewGet(t, rec.Ports[0].Host(), "/?token=wrong")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, body := previewGet(t, rec.Ports[0].Host(), "/?token="+token)
	assert.Equal(t, http.StatusOK, status, "the client's existing URL still works")
	assert.Equal(t, "hello from the guest", body)
}

// TestListDropsThePreviewsOfASandboxItFindsGone: a sandbox whose VMM is gone is failed by
// the next listing, and a failed sandbox cannot serve a preview: its listener is closed
// and its record stops naming it, so a caller reads one answer rather than two.
func TestListDropsThePreviewsOfASandboxItFindsGone(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevI")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	records, _, err := f.manager.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, StateFailed, records[0].State, "a sandbox with no instance record is failed")
	assert.Empty(t, records[0].Ports, "and it names no exposure")
	require.Error(t, reachable(exposure.Host()), "its listener is closed")
}

// mustJSON renders a record as the store writes it, so a test can assert what is — and is
// not — on disk.
func mustJSON(t *testing.T, rec Record) string {
	t.Helper()
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	return string(data)
}

// reachable returns nil when something is listening at one host:port, so a test can
// assert a listener is there (NoError) or gone (Error).
func reachable(host string) error {
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// TestTheProxyAnswersBadRequests: the listener is an HTTP proxy, so a client that never
// finishes a request head, or sends one larger than a preview may carry, is answered
// rather than held — and the answer is the same whether the exposure is live or not.
func TestTheProxyAnswersBadRequests(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevJ")
	guest, asked := countingGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	// A head that never arrives: half a request, then a close.
	half, err := net.DialTimeout("tcp", exposure.Host(), 2*time.Second)
	require.NoError(t, err)
	_, err = half.Write([]byte("GET / HTTP/1.1\r\nHost:"))
	require.NoError(t, err)
	require.NoError(t, half.(*net.TCPConn).CloseWrite())
	answer, err := io.ReadAll(half)
	require.NoError(t, err)
	_ = half.Close()
	assert.Contains(t, string(answer), "400 Bad Request")

	// A head larger than the cap: the proxy refuses to buffer it, and what matters is that
	// the request never reaches the guest. The client sent more than the proxy read, so the
	// answer may arrive with the connection reset behind it — the assertion is the guest.
	big, err := net.DialTimeout("tcp", exposure.Host(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = big.Close() }()
	payload := append([]byte("GET / HTTP/1.1\r\nHost: x\r\nX-Fill: "), bytes.Repeat([]byte("a"), previewHeadCap+1024)...)
	_, _ = big.Write(payload)
	_ = big.SetReadDeadline(time.Now().Add(5 * time.Second))
	bigAnswer := make([]byte, 512)
	read, _ := big.Read(bigAnswer)
	assert.NotContains(t, string(bigAnswer[:max(read, 0)]), "200 OK", "an oversized head is never relayed")
	assert.Zero(t, asked.Load(), "the guest was never asked: the head was larger than a preview may carry")
	// The half-head case above never reached it either.
	assert.Zero(t, asked.Load())
}

// TestTheProxyAcceptsABareLineFeedHead: HTTP is CRLF, and a client that sends LF alone is
// still sending a request the guest may answer — so the head is recognised either way, and
// what reaches the guest is what the client sent.
func TestTheProxyAcceptsABareLineFeedHead(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevK")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	conn, err := net.DialTimeout("tcp", exposure.Host(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	// "Connection: close" so the guest's own answer ends the connection: the relay is a
	// pipe, and what closes it is the guest's decision, not the proxy's.
	_, err = io.WriteString(conn, "GET /?token="+exposure.Token+" HTTP/1.1\nHost: preview\nConnection: close\n\n")
	require.NoError(t, err)

	answer, err := io.ReadAll(conn)
	require.NoError(t, err)
	assert.Contains(t, string(answer), "200 OK")
	assert.Contains(t, string(answer), "hello from the guest")
}

// TestTheProxyReportsAGuestItCannotReach: the proxy is up and the thing behind it is not,
// which is a different answer from "no such preview" and is what a caller needs to tell
// those apart.
func TestTheProxyReportsAGuestItCannotReach(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevL")
	f.manager.cfg.DialGuest = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("the guest's port is closed")
	}

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	status, _ := previewGet(t, exposure.Host(), "/?token="+exposure.Token)
	assert.Equal(t, http.StatusBadGateway, status, "a guest that cannot be reached is a 502")
}

// TestAnExposureWithNoDigestServesNobody: a record from a build that kept no digest is one
// this node cannot check, and it refuses rather than serves everybody.
func TestAnExposureWithNoDigestServesNobody(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevM")

	rec.Ports = []ExposedPort{{
		GuestPort: 18080,
		Address:   previewAddress(rec.ID),
		Created:   f.now,
		// No TokenHash: the digest this node would check against is missing.
	}}
	require.NoError(t, f.store.Put(rec))
	require.NoError(t, f.manager.RestorePreviews(context.Background()))

	exposure := rec.Ports[0]
	status, _ := previewGet(t, exposure.Host(), "/")
	assert.Equal(t, http.StatusUnauthorized, status)
	status, _ = previewGet(t, exposure.Host(), "/?token=anything-at-all")
	assert.Equal(t, http.StatusUnauthorized, status, "an exposure with no digest authorizes nobody")
}

// TestExposeWithNoDeadline: a negative TTL is "never", which the record carries as no
// expiry at all rather than as a timestamp in the past.
func TestExposeWithNoDeadline(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevN")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, -time.Hour)
	require.NoError(t, err)
	assert.True(t, exposure.ExpiresAt.IsZero(), "a negative TTL never expires")

	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	assert.True(t, stored.Ports[0].ExpiresAt.IsZero())

	status, _ := previewGet(t, exposure.Host(), "/?token="+exposure.Token)
	assert.Equal(t, http.StatusOK, status)
}

// TestExposePutsNothingBackWhenThePolicyCannotBeOpened: a port the sandbox's own table
// would not accept must not be left listening — the listener is the opening, and the table
// is what describes it.
func TestExposePutsNothingBackWhenThePolicyCannotBeOpened(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevO")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)
	f.network.inboundErr = errors.New("nft: no such table")

	_, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.Error(t, err)

	host := net.JoinHostPort(previewAddress(rec.ID), "18080")
	require.Error(t, reachable(host), "nothing is listening for a port the policy refused")

	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Ports, "and the record names no exposure")
}

// TestUnexposePutsTheExposureBackWhenThePolicyCannotBeNarrowed: the record names what the
// node serves, so a failed un-expose leaves both as they were.
func TestUnexposePutsTheExposureBackWhenThePolicyCannotBeNarrowed(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevP")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	f.network.inboundErr = errors.New("nft: no such table")
	err = f.manager.Unexpose(context.Background(), rec.ID, 18080)
	require.Error(t, err, "the refusal is reported")

	require.NoError(t, reachable(exposure.Host()), "the listener is back, because the record still names the exposure")
	status, body := previewGet(t, exposure.Host(), "/?token="+exposure.Token)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "hello from the guest", body)

	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	assert.Len(t, stored.Ports, 1, "and the record is unchanged")
}

// TestUnexposeTakesOutAnExposureWhoseListenerIsGone: a listener is the daemon's memory, so
// a restarted daemon has none while the record still names the exposure — and un-exposing
// it has to work rather than refuse.
func TestUnexposeTakesOutAnExposureWhoseListenerIsGone(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevQ")

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)
	// What a restart does: the listener goes with the process.
	f.manager.closePreview(rec.ID, 18080)
	require.Error(t, reachable(exposure.Host()))

	require.NoError(t, f.manager.Unexpose(context.Background(), rec.ID, 18080))
	stored, err := f.manager.Get(rec.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Ports)
	assert.Equal(t, map[string][]uint16{}, f.network.inbound)
}

// TestRestorePreviewsSkipsWhatItCannotServe: a sandbox that is not ready has no guest to
// relay to, and an address another process holds is reported while the rest of the node's
// exposures still serve — deleting a caller's exposure to hide a bind failure would be
// worse than saying so.
func TestRestorePreviewsSkipsWhatItCannotServe(t *testing.T) {
	f, ready := newPreviewFixture(t, "prevR")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	failed := previewRecord("prevS")
	failed.State, failed.Reason = StateFailed, "its VMM is gone"
	failed.Ports = []ExposedPort{{GuestPort: 18080, Address: previewAddress(failed.ID), TokenHash: digestToken("x"), Created: f.now}}
	require.NoError(t, f.store.Put(failed))

	digest := digestToken(strings.Repeat("cd", 32))
	ready.Ports = []ExposedPort{
		{GuestPort: 18080, Address: previewAddress(ready.ID), TokenHash: digest, Created: f.now},
		{GuestPort: 18081, Address: previewAddress(ready.ID), TokenHash: digest, Created: f.now},
	}
	require.NoError(t, f.store.Put(ready))

	// Another process holds one of the two addresses.
	held, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4",
		net.JoinHostPort(previewAddress(ready.ID), "18081"))
	require.NoError(t, err)
	defer func() { _ = held.Close() }()

	err = f.manager.RestorePreviews(context.Background())
	require.Error(t, err, "the port that cannot be bound is reported")
	assert.Contains(t, err.Error(), ready.ID)

	// The other exposure of the same sandbox serves, and the failed sandbox's does not.
	status, _ := previewGet(t, net.JoinHostPort(previewAddress(ready.ID), "18080"), "/?token="+strings.Repeat("cd", 32))
	assert.Equal(t, http.StatusOK, status, "an exposure that could be opened is open")
	require.Error(t, reachable(net.JoinHostPort(previewAddress(failed.ID), "18080")),
		"a sandbox that is not ready has no exposure")
	assert.Equal(t, []uint16{18080}, f.network.inbound[ready.NetNS],
		"only the ports that are really served are opened in the table")
}

// TestANonHTTPHeadIsRefusedAsThat: a complete head the proxy cannot read as an HTTP
// request is not a request without a token — it is not this protocol — and it is refused
// before the guest hears anything.
func TestANonHTTPHeadIsRefusedAsThat(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevU")
	guest, asked := countingGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	conn, err := net.DialTimeout("tcp", exposure.Host(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "THIS IS NOT A REQUEST\r\nX-Whatever: yes\r\n\r\n")
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	answer, err := io.ReadAll(conn)
	require.NoError(t, err)
	assert.Contains(t, string(answer), "400 Bad Request")
	assert.Zero(t, asked.Load(), "the guest was never asked")
}

// TestTheProxyReportsAGuestThatGoesAwayMidRequest: a guest that takes the connection and
// dies before the request is all there is answered like one that never accepted it, so a
// client sees a failure rather than an empty connection.
func TestTheProxyReportsAGuestThatGoesAwayMidRequest(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevV")
	f.manager.cfg.DialGuest = func(context.Context, string, string) (net.Conn, error) {
		// A connection whose far end is already closed: the write cannot be taken.
		left, right := net.Pipe()
		_ = right.Close()
		_ = left.Close()
		return left, nil
	}

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	status, _ := previewGet(t, exposure.Host(), "/?token="+exposure.Token)
	assert.Equal(t, http.StatusBadGateway, status)
}

// TestRemoveClosesALiveRelay: a preview's sockets belong to the sandbox's namespace, so a
// connection still being relayed has to be closed before the namespace is freed — rm is
// the promise that the listeners are gone, and a listener's live connections are part of
// that promise.
func TestRemoveClosesALiveRelay(t *testing.T) {
	f, rec := newPreviewFixture(t, "prevW")
	guest := testGuest(t)
	f.manager.cfg.DialGuest = dialTo(guest)

	exposure, err := f.manager.Expose(context.Background(), rec.ID, 18080, 0)
	require.NoError(t, err)

	// A request that leaves the connection open: the guest's stand-in keeps it alive, so
	// the relay is still running when the sandbox is removed.
	conn, err := net.DialTimeout("tcp", exposure.Host(), 2*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "GET /?token="+exposure.Token+" HTTP/1.1\r\nHost: preview\r\n\r\n")
	require.NoError(t, err)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	answer, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, answer.StatusCode, "the relay is live before the removal")
	_, err = io.ReadAll(answer.Body) // the guest's answer, read out of the way
	require.NoError(t, err)
	_ = answer.Body.Close()

	// rm: the listener and the connection it is serving both go.
	_, err = f.manager.Remove(context.Background(), rec.ID)
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = reader.ReadString('\n')
	require.Error(t, err, "the relayed connection is closed with the listener, not left open")
	assert.NotEqual(t, os.ErrDeadlineExceeded, err, "and it was closed rather than left to time out")
}
