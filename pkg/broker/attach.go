package broker

// This file owns the node-side attach handler: POST /v1/tasks/{taskID}/attach on
// the internal broker.
//
// A manager forwards one HTTP/1.1 request and the handler makes it a full duplex
// pipe to the task's interactive console socket:
//
//	request body (chunked)  -> guest console stdin
//	response body (chunked) -> guest console output
//
// Both directions are live at the same time, which is why the handler enables
// full duplex before it touches the body. Without it net/http would consume the
// rest of the request before writing the first response byte, and an interactive
// session (a shell prompt that arrives before the user types) could never start.
//
// The response is raw bytes — application/octet-stream — the documented exception
// to the v1 JSON envelope, like exec and files. A task that is not running has no
// console socket, so attach answers not_found rather than dialing a dead path.

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// ConsoleDialer connects to a task's console Unix socket. The daemon wires
// net.Dial("unix", path); tests substitute their own to observe the relay.
type ConsoleDialer func(path string) (net.Conn, error)

// DialConsole is the production ConsoleDialer: a plain Unix socket dial. The
// console carries no protocol, just bytes.
func DialConsole(path string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.Dial("unix", path)
}

// NewAttachHandler builds the attach operation. A nil dialer selects
// DialConsole, so the daemon wires the handler without repeating the default.
func NewAttachHandler(dial ConsoleDialer) Handler {
	if dial == nil {
		dial = DialConsole
	}

	return func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request) {
		serveAttach(ctx, dial, task, w, r)
	}
}

// serveAttach validates the target, dials the console and relays both
// directions until the session ends.
func serveAttach(ctx context.Context, dial ConsoleDialer, task Task, w http.ResponseWriter, r *http.Request) {
	// A stopped task keeps its console log (readable through the logs operation)
	// but its console socket is gone, so there is nothing to attach to.
	if !task.consoleAvailable() {
		writeTypedError(w, http.StatusNotFound, noConsoleError(task.ID))
		return
	}
	// The console server creates the socket after the VM starts. A task that
	// resolves as running can therefore still have no console yet; that is a
	// not-found answer, not an unavailable node.
	//nolint:gosec // G703: the path is <socketDir>/<validated task ID>.console.sock.
	if _, err := os.Stat(task.ConsolePath); err != nil {
		writeTypedError(w, http.StatusNotFound, noConsoleError(task.ID))
		return
	}

	// Full duplex must be enabled before anything reads the body: the caller is
	// still streaming stdin while the console starts answering. A writer that
	// cannot do it (an in-memory test recorder, HTTP/2) reports ErrNotSupported
	// and the relay still works one direction at a time, which is all a
	// non-interactive script needs.
	_ = http.NewResponseController(w).EnableFullDuplex()

	// Dial before the 200 header: a dial failure is still a typed v1 error
	// document, which the manager rewrites into the public envelope.
	conn, err := dial(task.ConsolePath)
	if err != nil {
		writeTypedError(w, http.StatusServiceUnavailable,
			output.NewErrorf(codeUnavailable, "task %q console is unavailable", task.ID).
				WithDetails(map[string]any{"reason": "console_unavailable"}))
		return
	}
	defer func() { _ = conn.Close() }()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	flusher := flusherFor(w)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Publish the 200 immediately so a client can read the console before it has
	// finished sending stdin.
	flush()

	// Closing the connection releases a console read that would otherwise wait
	// forever after the caller is gone.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	// Caller stdin -> guest console. When stdin reaches EOF the console is
	// half-closed rather than closed: the guest keeps its output flowing, which
	// is what a piped command needs (echo something in, read the prompt out).
	go func() {
		//nolint:errcheck // a failed copy ends the session; the response side reports it.
		_, _ = io.Copy(conn, r.Body)
		halfCloseWrite(conn)
	}()

	// Guest console -> caller stdout, flushed per chunk so an interactive prompt
	// is not held in a buffer. It returns when the console closes, the connection
	// fails or the request context is cancelled.
	//nolint:errcheck // the only remaining sink is the response body itself.
	_, _ = io.Copy(flushWriter{w: w, flush: flush}, conn)

	// The console side ended, so there is nothing left to write. Closing the
	// connection ends the stdin copy as well; its goroutine exits when the
	// server closes the request body after this handler returns.
	_ = conn.Close()
}

// flushWriter flushes the response after every write, so each console chunk
// reaches the caller instead of waiting in the server's buffer.
type flushWriter struct {
	w     io.Writer
	flush func()
}

// Write forwards one chunk and flushes it.
func (w flushWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.flush()
	return n, err
}

// closeWriter is the half-close a Unix connection supports. TCP has the same
// method; the interface keeps the relay from importing a concrete type.
type closeWriter interface {
	CloseWrite() error
}

// halfCloseWrite shuts down the write direction of conn, so the console sees EOF
// on stdin while its output stays readable. A connection without half-close
// support is left alone: closing it entirely would also end the output.
func halfCloseWrite(conn net.Conn) {
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

// noConsoleError is the typed 404 for a task with no attachable console. It
// names no path: the socket location stays node-local.
func noConsoleError(taskID string) *output.Error {
	return output.NewErrorf(output.CodeNotFound, "task %q has no console", taskID).
		WithDetails(map[string]any{"reason": "console_unavailable"})
}
