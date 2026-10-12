package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// errEndConn is an internal signal that a request ended the framed session and
// ServeConn must stop serving. It is not reported to the caller as an error.
var errEndConn = errors.New("agent: request ended the connection")

// ServeConn serves the guest-agent protocol over rw. Requests are handled one
// at a time, in order, until the peer closes the connection, a protocol error
// occurs, or a request ends the session. A clean disconnect returns nil.
//
// A TTY exec ends the session: the guest attaches the client's raw stdin to the
// PTY, which shares the byte stream with framed requests, so the framed
// protocol cannot continue afterwards. ServeConn returns after sending the exit
// frame, leaving the caller to close rw.
//
// The caller is responsible for the connection lifetime; ServeConn does not
// close rw.
func ServeConn(rw io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &server{rw: rw, ctx: ctx, cancel: cancel}
	for {
		var req Request
		if _, err := ReadFrame(rw, &req); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		if err := s.dispatch(&req); err != nil {
			if errors.Is(err, errEndConn) {
				return nil
			}
			return err
		}
	}
}

// server holds the connection state for one client. mu serialises frame
// writes so concurrent output streams (stdout/stderr) never interleave.
type server struct {
	rw     io.ReadWriteCloser
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
}

// write sends a single frame, serialised against other writers.
func (s *server) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return WriteFrame(s.rw, v)
}

// writeError reports a request failure to the client as an error frame. The
// connection stays usable, so a nil return is intentional.
func (s *server) writeError(format string, args ...any) error {
	return s.write(Message{Type: "error", Error: fmt.Sprintf(format, args...)})
}

func (s *server) dispatch(req *Request) error {
	switch req.Op {
	case "hello":
		return s.handleHello(req)
	case "exec":
		if err := s.handleExec(req); err != nil {
			return err
		}
		if req.TTY {
			// A TTY session shares the raw byte stream with framed requests,
			// so the framed protocol cannot continue. handleExec has already
			// sent the exit frame; end the connection.
			return errEndConn
		}
		return nil
	case "put":
		return s.handlePut(req)
	case "get":
		return s.handleGet(req)
	default:
		return s.writeError("unknown op %q", req.Op)
	}
}

func (s *server) handleHello(req *Request) error {
	if req.Version != Version {
		return s.writeError("agent version mismatch: client %s, guest %s", req.Version, Version)
	}
	return s.write(Message{Type: "hello", Version: Version})
}

// handlePut receives a stream of file frames and writes them to req.Path.
func (s *server) handlePut(req *Request) error {
	if req.Path == "" {
		return s.writeError("put: empty path")
	}
	if dir := filepath.Dir(req.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return s.writeError("put: create parent directory for %s: %v", req.Path, err)
		}
	}

	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(req.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return s.writeError("put: open %s: %v", req.Path, err)
	}

	for {
		var msg Message
		if _, err := ReadFrame(s.rw, &msg); err != nil {
			_ = f.Close()
			return fmt.Errorf("put: read data frame: %w", err)
		}
		if msg.Type != "file" {
			_ = f.Close()
			return s.writeError("put: unexpected frame type %q", msg.Type)
		}
		if len(msg.Data) == 0 {
			break
		}
		if _, err := f.Write(msg.Data); err != nil {
			_ = f.Close()
			return s.writeError("put: write %s: %v", req.Path, err)
		}
	}
	if err := f.Close(); err != nil {
		return s.writeError("put: close %s: %v", req.Path, err)
	}
	return s.write(Message{Type: "ok"})
}

// handleGet streams req.Path to the client as file frames, terminated by an
// empty file frame.
func (s *server) handleGet(req *Request) error {
	if req.Path == "" {
		return s.writeError("get: empty path")
	}
	f, err := os.Open(req.Path)
	if err != nil {
		return s.writeError("get: open %s: %v", req.Path, err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, fileChunkSize)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if werr := s.write(Message{Type: "file", Data: buf[:n]}); werr != nil {
				return werr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return s.writeError("get: read %s: %v", req.Path, err)
		}
	}
	return s.write(Message{Type: "file"})
}
