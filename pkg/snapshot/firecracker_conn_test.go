package snapshot

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openConns tracks how many connections a fake Firecracker API server holds
// open. Firecracker refuses new API connections once its per-process limit is
// reached, so a client that leaves connections behind breaks every later call.
type openConns struct {
	mu    sync.Mutex
	open  int
	peak  int
	total int
}

func (c *openConns) connState(_ net.Conn, state http.ConnState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch state {
	case http.StateNew:
		c.open++
		c.total++
		if c.open > c.peak {
			c.peak = c.open
		}
	case http.StateClosed, http.StateHijacked:
		c.open--
	}
}

func (c *openConns) stats() (open, peak, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open, c.peak, c.total
}

// startFakeFirecrackerAPI serves handler over a Unix socket, which is the
// transport the snapshot package uses against a real Firecracker.
func startFakeFirecrackerAPI(t *testing.T, handler http.Handler) (string, *openConns) {
	t.Helper()

	socketPath := testSocketPath(t, "fc.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	conns := &openConns{}
	srv := &http.Server{Handler: handler, ConnState: conns.connState}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return socketPath, conns
}

// waitForNoOpenConns blocks until the fake API has no connection left open. It
// fails the test instead of returning a bool so a leak names itself.
func waitForNoOpenConns(t *testing.T, conns *openConns, where string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if open, _, _ := conns.stats(); open == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	open, peak, total := conns.stats()
	require.Failf(t, "connections leaked", "%s: %d connection(s) still open (peak %d, total %d); Firecracker answers \"Too many open connections\" once this repeats",
		where, open, peak, total)
}

// TestFirecrackerClientDisablesKeepAlives pins the transport setting the fix
// depends on: a pooled connection would outlive the client that opened it,
// because every call builds its own client and never reuses the pool.
func TestFirecrackerClientDisablesKeepAlives(t *testing.T) {
	client := newUnixHTTPClient("/tmp/unused.sock", time.Second)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok, "unix client must carry an *http.Transport")
	assert.True(t, transport.DisableKeepAlives,
		"keep-alives would leave one connection open per Firecracker call")
}

// TestFirecrackerCallsReleaseTheirConnection runs the pause/snapshot/resume
// cycle shape many times and requires the API to be connection-free after each
// call. Before the fix the pool kept every client's connection until it timed
// out, and the second create answered 503 "Too many open connections".
func TestFirecrackerCallsReleaseTheirConnection(t *testing.T) {
	var calls atomic.Int32
	socketPath, conns := startFakeFirecrackerAPI(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))

	ctx := context.Background()
	const cycles = 20

	for i := 0; i < cycles; i++ {
		require.NoError(t, patchFirecrackerAPI(ctx, socketPath, "/vm", map[string]string{"state": "Paused"}))
		waitForNoOpenConns(t, conns, "after pause")

		require.NoError(t, putFirecrackerAPI(ctx, socketPath, "/snapshot/create", map[string]string{"snapshot_type": "Full"}))
		waitForNoOpenConns(t, conns, "after snapshot create")

		require.NoError(t, patchFirecrackerAPI(ctx, socketPath, "/vm", map[string]string{"state": "Resumed"}))
		waitForNoOpenConns(t, conns, "after resume")
	}

	_, peak, total := conns.stats()
	assert.Equal(t, int(calls.Load()), total, "every call must dial its own connection")
	assert.Equal(t, 1, peak, "sequential calls must never overlap connections")
}

// countingBody records that a response body was closed, and how many times.
type countingBody struct {
	io.ReadCloser
	closed *atomic.Int32
	once   sync.Once
}

func (b *countingBody) Close() error {
	b.once.Do(func() { b.closed.Add(1) })
	return b.ReadCloser.Close()
}

// countingTransport wraps the real round tripper so the test can see the body
// each call path received.
type countingTransport struct {
	base   http.RoundTripper
	opened *atomic.Int32
	closed *atomic.Int32
}

func (c countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.base.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		c.opened.Add(1)
		resp.Body = &countingBody{ReadCloser: resp.Body, closed: c.closed}
	}
	return resp, err
}

// TestFirecrackerCallsCloseBodiesOnEveryPath covers the success path and the
// error path: a failed call that skipped Close would hold its connection open
// exactly like the successful ones did.
func TestFirecrackerCallsCloseBodiesOnEveryPath(t *testing.T) {
	socketPath, _ := startFakeFirecrackerAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "Too many open connections")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	var opened, closed atomic.Int32
	base := newUnixHTTPClient(socketPath, time.Second)
	previous := firecrackerClientFactory
	firecrackerClientFactory = func(string, time.Duration) *http.Client {
		return &http.Client{
			Timeout:   base.Timeout,
			Transport: countingTransport{base: base.Transport, opened: &opened, closed: &closed},
		}
	}
	t.Cleanup(func() { firecrackerClientFactory = previous })

	ctx := context.Background()

	require.NoError(t, patchFirecrackerAPI(ctx, socketPath, "/vm", map[string]string{"state": "Resumed"}))

	err := patchFirecrackerAPI(ctx, socketPath, "/fail", map[string]string{"state": "Resumed"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Too many open connections",
		"the API error must still carry Firecracker's message")

	assert.Equal(t, int32(2), opened.Load(), "both calls must have received a body")
	assert.Equal(t, int32(2), closed.Load(), "the failing call must close its body too")
}
