package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// consoleSink is a stdout stand-in that reports every Write on a channel, so a
// test waits for console bytes to arrive instead of sleeping.
type consoleSink struct {
	mu     sync.Mutex
	buf    strings.Builder
	notify chan struct{}
}

func newConsoleSink() *consoleSink {
	return &consoleSink{notify: make(chan struct{}, 64)}
}

func (s *consoleSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *consoleSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitFor blocks until the sink has received want. The channel is the
// synchronisation; the deadline only keeps a hung test from hanging the suite.
func (s *consoleSink) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(s.String(), want) {
			return
		}
		select {
		case <-s.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %q; console said %q", want, s.String())
		}
	}
}

// attachHeaders is the request surface every attach request must carry.
type attachHeaders struct {
	method      string
	path        string
	accept      string
	contentType string
	auth        string
}

// newAttachTestServer starts a TLS server whose handler runs under full duplex
// and reports the request headers on seen.
func newAttachTestServer(t *testing.T, seen chan<- attachHeaders, handler http.HandlerFunc) *Client {
	t.Helper()
	return newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		// Without full duplex the handler cannot write the console before it has
		// finished reading stdin, which is exactly the shape attach needs. The
		// handler runs on its own goroutine, so it reports with a status, never
		// t.FailNow.
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if seen != nil {
			seen <- attachHeaders{
				method:      r.Method,
				path:        r.URL.Path,
				accept:      r.Header.Get("Accept"),
				contentType: r.Header.Get("Content-Type"),
				auth:        r.Header.Get("Authorization"),
			}
		}
		handler(w, r)
	})
}

// TestAttachTaskStreamsBothDirections pins the full-duplex contract: the
// console banner reaches stdout while stdin is still open, stdin bytes reach the
// server while the request body is still open, and a console EOF ends the call
// cleanly.
func TestAttachTaskStreamsBothDirections(t *testing.T) {
	seen := make(chan attachHeaders, 1)
	bodies := make(chan string, 1)

	c := newAttachTestServer(t, seen, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		// A real console greets the caller before it reads anything.
		if _, err := io.WriteString(w, "console> "); err != nil {
			return
		}
		flusher := http.NewResponseController(w)
		if err := flusher.Flush(); err != nil {
			return
		}

		// Echo every chunk as it arrives, as the guest's tty would, so the
		// client sees its input while the request body is still open.
		var body strings.Builder
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body.Write(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if ferr := flusher.Flush(); ferr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		bodies <- body.String()
	})

	stdinR, stdinW := io.Pipe()
	out := newConsoleSink()
	done := make(chan error, 1)
	go func() { done <- c.AttachTask(context.Background(), "web-1", stdinR, out) }()

	// Both directions are proven while the request body is still open: the
	// banner arrives before stdin sends anything, and the echoed input arrives
	// before stdin is closed.
	out.waitFor(t, "console> ")

	_, err := stdinW.Write([]byte("hello"))
	require.NoError(t, err)
	out.waitFor(t, "hello")

	require.NoError(t, stdinW.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("AttachTask did not return after the console closed")
	}

	assert.Equal(t, "console> hello", out.String())
	assert.Equal(t, "hello", <-bodies)

	req := <-seen
	assert.Equal(t, http.MethodPost, req.method)
	assert.Equal(t, "/v1/tasks/web-1/attach", req.path)
	assert.Equal(t, octetStreamMediaType, req.accept)
	assert.Equal(t, octetStreamMediaType, req.contentType)
	assert.Equal(t, "Bearer s3cret", req.auth)
}

// TestAttachTaskNon2xxMapsErrorDocument pins the error path: a refused attach
// decodes the normal Error document, not a transport error.
func TestAttachTaskNon2xxMapsErrorDocument(t *testing.T) {
	c := newAttachTestServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", testAccept)
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(v1.NewErrorDocument(v1.Error{
			Code:      string(output.CodeNotFound),
			Message:   `task "web-1" has no console`,
			RequestID: "req_attach",
			Retryable: false,
			Details:   map[string]any{"reason": "console_unavailable"},
		}))
	})

	err := c.AttachTask(context.Background(), "web-1", strings.NewReader(""), io.Discard)
	require.Error(t, err)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeNotFound, typed.Code)
	assert.Equal(t, "req_attach", typed.RequestID)
	assert.Equal(t, "console_unavailable", typed.Details["reason"])
}

// TestAttachTaskDetachEndsCleanlyAndStopsSending pins the detach rule: the
// Ctrl-P Ctrl-Q sequence is swallowed locally, the request is aborted, and the
// call reports success so the CLI exits 0 without stopping the VM.
func TestAttachTaskDetachEndsCleanlyAndStopsSending(t *testing.T) {
	received := make(chan string, 16)
	bodyDone := make(chan error, 1)

	c := newAttachTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}

		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				received <- string(buf[:n])
			}
			if err != nil {
				bodyDone <- err
				return
			}
		}
	})

	stdinR, stdinW := io.Pipe()
	out := newConsoleSink()
	done := make(chan error, 1)
	go func() { done <- c.AttachTask(context.Background(), "web-1", stdinR, out) }()

	_, err := stdinW.Write([]byte("hi"))
	require.NoError(t, err)
	select {
	case got := <-received:
		assert.Equal(t, "hi", got)
	case <-time.After(5 * time.Second):
		t.Fatal("stdin never reached the server")
	}

	_, err = stdinW.Write([]byte("\x10\x11"))
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err, "a detach is a normal end, not a failure")
	case <-time.After(5 * time.Second):
		t.Fatal("AttachTask did not return after the detach sequence")
	}

	// The request body was closed: the server's read loop ended without stdin
	// ever reaching EOF.
	select {
	case <-bodyDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the request body close")
	}

	// The sequence itself is never forwarded, and nothing follows it. The wait
	// is a guard: the positive proof is the closed body above.
	t.Cleanup(func() { _ = stdinW.CloseWithError(io.ErrClosedPipe) })
	go func() { _, _ = stdinW.Write([]byte("late")) }()
	select {
	case extra := <-received:
		t.Fatalf("bytes arrived after the detach: %q", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestAttachTransportIsHTTP11Only pins the protocol guard: the console session
// must never negotiate HTTP/2, which cannot carry it, and it must have no
// whole-request timeout.
func TestAttachTransportIsHTTP11Only(t *testing.T) {
	c := newTestRemote(t, Config{Token: "s3cret"}, func(http.ResponseWriter, *http.Request) {})

	transport, ok := c.remote.attach.Transport.(*http.Transport)
	require.True(t, ok, "attach must use an *http.Transport it can pin to HTTP/1.1")
	assert.False(t, transport.ForceAttemptHTTP2)
	assert.NotNil(t, transport.TLSNextProto, "a non-nil empty map is the documented HTTP/2 opt-out")
	assert.Empty(t, transport.TLSNextProto)
	assert.Zero(t, c.remote.attach.Timeout, "the session is bounded by its context, not a client timeout")
}

// TestAttachTaskRequiresRemoteEndpoint pins the remote-only rule.
func TestAttachTaskRequiresRemoteEndpoint(t *testing.T) {
	c := &Client{}

	err := c.AttachTask(context.Background(), "web-1", strings.NewReader(""), io.Discard)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

// TestAttachTaskRejectsInvalidReference pins the path guard the other remote
// calls share: a dot segment must not turn into another route.
func TestAttachTaskRejectsInvalidReference(t *testing.T) {
	c := newAttachTestServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an invalid reference must not reach the server")
	})

	err := c.AttachTask(context.Background(), "..", strings.NewReader(""), io.Discard)
	require.Error(t, err)
	assert.Equal(t, output.CodeUsage, codeOf(t, err).Code)
}

// TestAttachTaskStdoutWriteError pins the local failure path.
func TestAttachTaskStdoutWriteError(t *testing.T) {
	c := newAttachTestServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("console"))
	})

	err := c.AttachTask(context.Background(), "web-1", strings.NewReader(""),
		errWriter{err: io.ErrClosedPipe})
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, codeOf(t, err).Code)
}

// TestAttachTaskContextCancelIsReported pins that a caller's own cancellation
// (not a detach) is still an error, so a Ctrl-C is not silently a success.
func TestAttachTaskContextCancelIsReported(t *testing.T) {
	blocked := make(chan struct{})
	c := newAttachTestServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		<-r.Context().Done()
		close(blocked)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.AttachTask(ctx, "web-1", strings.NewReader(""), newConsoleSink()) }()

	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, output.CodeTimeout, codeOf(t, err).Code)
	case <-time.After(5 * time.Second):
		t.Fatal("AttachTask ignored the cancelled context")
	}
}
