package broker

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// shortTempDir returns a temporary directory with a short path. A Unix socket
// path is capped at ~104 bytes on macOS, and t.TempDir embeds the test name,
// which a descriptive name pushes over that limit.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "sc-attach")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// listenConsole starts a Unix listener at path, which is where pkg/console puts
// a running VM's interactive console. It is closed when the test ends.
func listenConsole(t *testing.T, path string) net.Listener {
	t.Helper()

	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })
	return lis
}

// attachTaskSource resolves one running task with a console socket.
func attachTaskSource(t *testing.T, dir, taskID string) *fakeTaskSource {
	t.Helper()
	return &fakeTaskSource{
		task: Task{
			ID:          taskID,
			SocketPath:  filepath.Join(dir, taskID+".sock"),
			Running:     true,
			ConsolePath: filepath.Join(dir, taskID+".console.sock"),
		},
		found: true,
	}
}

// TestAttachEchoesThroughTheConsole pins the relay: every byte of the request
// body reaches the console, and every console byte reaches the response body.
// The fake console echoes and half-closes on stdin EOF, like pkg/console does.
func TestAttachEchoesThroughTheConsole(t *testing.T) {
	dir := shortTempDir(t)
	src := attachTaskSource(t, dir, "task-1")
	lis := listenConsole(t, src.task.ConsolePath)

	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Echo: guest output is whatever the caller typed.
		_, _ = io.Copy(conn, conn)
		halfCloseWrite(conn)
	}()

	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle("attach", NewAttachHandler(DialConsole)))

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/attach",
		strings.NewReader("hello console"))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "hello console", rec.Body.String())
}

// TestAttachWithoutConsoleIsNotFound covers the three shapes that have no
// console to attach to. None of them may dial: a stale or absent path must not
// reach the console server.
func TestAttachWithoutConsoleIsNotFound(t *testing.T) {
	dir := shortTempDir(t)

	cases := []struct {
		name string
		task Task
	}{
		{
			name: "task is not running",
			task: Task{
				ID:          "task-1",
				SocketPath:  filepath.Join(dir, "task-1.sock"),
				ConsolePath: filepath.Join(dir, "task-1.console.sock"),
			},
		},
		{
			name: "log-only task has no console path",
			task: Task{ID: "task-1", LogPath: filepath.Join(dir, "task-1.log")},
		},
		{
			name: "console socket was never created",
			task: Task{
				ID:          "task-1",
				Running:     true,
				ConsolePath: filepath.Join(dir, "task-1.console.sock"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialed := make(chan string, 1)
			dial := func(path string) (net.Conn, error) {
				dialed <- path
				return nil, os.ErrNotExist
			}

			srv := newRouteServer(t, &fakeTaskSource{task: tc.task, found: true})
			require.NoError(t, srv.Handle("attach", NewAttachHandler(dial)))

			req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/attach", strings.NewReader("x"))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			require.Equal(t, http.StatusNotFound, rec.Code)
			doc := decodeErrorDocument(t, rec.Body)
			require.Equal(t, string(output.CodeNotFound), doc.Error.Code)
			require.Contains(t, doc.Error.Message, `task "task-1" has no console`)
			require.Equal(t, "console_unavailable", doc.Error.Details["reason"])
			require.False(t, doc.Error.Retryable)
			require.Empty(t, dialed, "a task with no console must not be dialed")
		})
	}
}

// TestAttachDialFailureIsRetryableUnavailable pins the difference between "no
// console" and "the console is not answering": a dial failure is a 503 the
// caller may retry.
func TestAttachDialFailureIsRetryableUnavailable(t *testing.T) {
	dir := shortTempDir(t)
	src := attachTaskSource(t, dir, "task-1")
	// A regular file passes the existence check but is not a socket, so the
	// production dialer fails exactly as it would for a console that stopped
	// listening between the stat and the dial.
	require.NoError(t, os.WriteFile(src.task.ConsolePath, nil, 0o600))

	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle("attach", NewAttachHandler(DialConsole)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/v1/tasks/task-1/attach", strings.NewReader("x")))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	doc := decodeErrorDocument(t, rec.Body)
	require.Equal(t, string(codeUnavailable), doc.Error.Code)
	require.True(t, doc.Error.Retryable)
	require.Equal(t, "console_unavailable", doc.Error.Details["reason"])
}

// TestAttachIsPostOnly pins the operation's method set: attach streams a body,
// so a GET is a 405 with Allow, not a 501 for a missing handler.
func TestAttachIsPostOnly(t *testing.T) {
	dir := shortTempDir(t)
	srv := newRouteServer(t, attachTaskSource(t, dir, "task-1"))
	require.NoError(t, srv.Handle("attach", NewAttachHandler(DialConsole)))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/attach", nil))

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
	require.Equal(t, "method_not_allowed", decodeErrorDocument(t, rec.Body).Error.Details["reason"])
}

// TestAttachConsoleCloseEndsStdinCopy pins the exit of the stdin copy goroutine.
//
// The session is console-driven: when the console closes, the handler closes the
// connection and returns even though the caller may still be typing, so a
// caller that never ends its stdin cannot hold the response open. The copy
// goroutine then has to end when the server closes the request body after the
// handler returns. The fake console connection reports the half-close that copy
// performs on its way out, so the assertion is the goroutine's own signal
// rather than a sleep or a goroutine count.
func TestAttachConsoleCloseEndsStdinCopy(t *testing.T) {
	dir := shortTempDir(t)
	src := attachTaskSource(t, dir, "task-1")
	lis := listenConsole(t, src.task.ConsolePath)

	halfClosed := make(chan struct{})
	var halfOnce sync.Once
	dial := func(path string) (net.Conn, error) {
		conn, err := DialConsole(path)
		if err != nil {
			return nil, err
		}
		return &observedConsoleConn{
			Conn:         conn,
			onCloseWrite: func() { halfOnce.Do(func() { close(halfClosed) }) },
		}, nil
	}

	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// The guest says goodbye and ends its output side while the caller is
		// still typing.
		_, _ = conn.Write([]byte("bye"))
		halfCloseWrite(conn)
	}()

	srv := newRouteServer(t, src)
	require.NoError(t, srv.Handle("attach", NewAttachHandler(dial)))

	body := newBlockingBody(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/task-1/attach", body)
	rec := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		srv.Handler().ServeHTTP(rec, req)
	}()

	// The relay is live: the stdin copy is parked inside the caller's body.
	select {
	case <-body.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the stdin copy never started reading the request body")
	}

	// The handler must not wait for stdin: the console ended the session.
	select {
	case <-handlerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler waited for the caller's stdin instead of ending with the console")
	}

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "bye", rec.Body.String(), "the console output is still delivered")

	// Still parked: returning must not be confused with the copy having exited.
	select {
	case <-halfClosed:
		t.Fatal("the stdin copy exited while the request body was still open")
	default:
	}

	// net/http closes the request body once a handler returns; doing it here
	// mirrors that and must end the copy goroutine.
	require.NoError(t, body.Close())

	select {
	case <-halfClosed:
	case <-time.After(10 * time.Second):
		t.Fatal("the stdin copy outlived the closed request body")
	}
}

// observedConsoleConn is a console connection that reports the half-close the
// stdin copy performs when it exits.
type observedConsoleConn struct {
	net.Conn
	onCloseWrite func()
	once         sync.Once
}

// CloseWrite reports the copy's exit and half-closes the console as usual.
func (c *observedConsoleConn) CloseWrite() error {
	c.once.Do(c.onCloseWrite)
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

// blockingBody is a request body that delivers nothing until it is closed: the
// caller is still typing for the whole session. reading closes on the first
// Read, so a test can prove the relay actually reached the body.
type blockingBody struct {
	reading chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingBody(t *testing.T) *blockingBody {
	t.Helper()
	return &blockingBody{reading: make(chan struct{}), closed: make(chan struct{})}
}

// Read blocks until the body is closed, then reports EOF.
func (b *blockingBody) Read([]byte) (int, error) {
	select {
	case <-b.reading:
	default:
		close(b.reading)
	}
	<-b.closed
	return 0, io.EOF
}

// Close releases a blocked Read.
func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestManagerProxyRelaysAttachFullDuplex is the streaming proof for the whole
// manager path. It fails if either hop buffers:
//
//   - a byte written to the client request must reach the console while the
//     client is still sending its body, and
//   - a console byte must reach the client while the console is still open.
//
// A buffering proxy (or a handler that reads the body before writing the
// response) deadlocks here until the guards expire.
func TestManagerProxyRelaysAttachFullDuplex(t *testing.T) {
	pki := newTestPKI(t)
	dir := shortTempDir(t)

	src := attachTaskSource(t, dir, "task-1")
	lis := listenConsole(t, src.task.ConsolePath)

	consoleGotStdin := make(chan string, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read the first four bytes of the caller's stdin. Nothing has closed
		// the body yet, so this only succeeds when the relay is live.
		stdin := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, stdin); readErr != nil {
			return
		}
		consoleGotStdin <- string(stdin)

		// Answer immediately, before the caller finishes sending stdin.
		if _, writeErr := conn.Write([]byte("pong")); writeErr != nil {
			return
		}

		// Drain stdin until the relay half-closes it, then close the console
		// side so the response ends.
		_, _ = io.Copy(io.Discard, conn)
		halfCloseWrite(conn)
	}()

	_, baseURL := startTestBroker(t, pki, src, map[string]Handler{
		"attach": NewAttachHandler(DialConsole),
	})

	host, port := brokerTarget(t, baseURL)
	mgr := newTestManager(t,
		&stubTaskReader{task: v1.Task{ID: "task-1", NodeID: "node-1"}},
		&stubNodeReader{node: v1.Node{ID: "node-1", Address: host}},
		port,
		pki.httpClient(),
	)

	// The public handler is the same shape as apihttp's: enable full duplex on
	// the response, then relay.
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).EnableFullDuplex()
		if err := mgr.Proxy(r.Context(), w, r, "task-ref", "attach"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer proxySrv.Close()

	stdinR, stdinW := io.Pipe()

	req, err := http.NewRequest(http.MethodPost, proxySrv.URL+"/v1/tasks/task-ref/attach", stdinR)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")

	type result struct {
		resp *http.Response
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(req)
		resCh <- result{resp: resp, err: doErr}
	}()
	t.Cleanup(func() { _ = stdinW.Close() })

	// Send stdin without ending the body. The console must see it now.
	_, err = stdinW.Write([]byte("ping"))
	require.NoError(t, err)

	select {
	case got := <-consoleGotStdin:
		require.Equal(t, "ping", got, "the console must receive stdin while the body is open")
	case <-time.After(10 * time.Second):
		t.Fatal("the request body was buffered: the console never saw stdin")
	}

	var res result
	select {
	case res = <-resCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the response never started: the relay waited for the whole body")
	}
	require.NoError(t, res.err)
	require.NotNil(t, res.resp)
	defer func() { _ = res.resp.Body.Close() }()
	require.Equal(t, http.StatusOK, res.resp.StatusCode)

	// Read the console's answer while it is still open.
	consoleOut := make(chan string, 1)
	go func() {
		buf := make([]byte, 4)
		if _, readErr := io.ReadFull(res.resp.Body, buf); readErr != nil {
			consoleOut <- ""
			return
		}
		consoleOut <- string(buf)
	}()

	select {
	case got := <-consoleOut:
		require.Equal(t, "pong", got, "a console byte must reach the caller while the console is open")
	case <-time.After(10 * time.Second):
		t.Fatal("the console byte was buffered: the response waited for the console to close")
	}

	// Ending stdin half-closes the console input; the console then closes and
	// the response ends.
	require.NoError(t, stdinW.Close())
	rest, err := io.ReadAll(res.resp.Body)
	require.NoError(t, err)
	require.Empty(t, rest, "nothing follows the console's last byte")
}
