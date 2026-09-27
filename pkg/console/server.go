// Package console provides an interactive serial-console channel for
// Firecracker microVMs.
//
// Firecracker bridges the guest's ttyS0 to its own stdin/stdout. This package
// turns that pair of pipes into a Unix socket per VM, so a client (see
// `swarmcracker vm attach`) can exchange raw bytes with the guest console.
//
// Firecracker only registers its stdin as a serial input source when that fd
// is a terminal or a FIFO/pipe (see the upstream `serial.rs`), which is why a
// plain os.Pipe is sufficient and no pseudo-terminal is required: the guest
// still sees a real tty (ttyS0), so line editing and job control work inside
// the VM.
package console

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/rs/zerolog"
)

// socketSuffix is appended to a task ID to form its console socket name.
const socketSuffix = ".console.sock"

// defaultScrollbackBytes is how much recent guest output is replayed to a
// client that attaches after the VM has already produced output.
const defaultScrollbackBytes = 16 << 10

// Config configures a per-VM console Server.
type Config struct {
	// SocketDir is the directory that holds the VM's Firecracker sockets.
	SocketDir string
	// TaskID is the VM's task ID; the console socket is
	// <SocketDir>/<TaskID>.console.sock.
	TaskID string
	// Mirror optionally receives a copy of everything the guest writes. The
	// daemon wires this to its logger so journald keeps seeing console output.
	Mirror io.Writer
	// ScrollbackBytes caps the replayed history. Zero uses the default;
	// negative disables scrollback.
	ScrollbackBytes int
	// Logger receives debug-level diagnostics. A nil logger disables logging.
	Logger *zerolog.Logger
}

// Server bridges one VM's serial pipes to a Unix socket.
type Server struct {
	taskID     string
	socketPath string
	listener   net.Listener

	stdinR  *os.File // read end handed to Firecracker as stdin
	stdinW  *os.File // host writes guest input here
	stdoutR *os.File // host reads guest output here
	stdoutW *os.File // write end handed to Firecracker as stdout

	hub    *Hub
	mirror io.Writer
	logger zerolog.Logger

	done      chan struct{}
	closeOnce sync.Once

	connMu sync.Mutex
	conns  map[net.Conn]struct{}

	wg sync.WaitGroup
}

// New creates a console server for a VM and starts listening on its socket.
// The caller must wire Stdin()/Stdout() into the Firecracker process, call
// Start once that process is running, and call Close when the VM is removed.
func New(cfg Config) (*Server, error) {
	if cfg.SocketDir == "" {
		return nil, fmt.Errorf("console: socket directory is required")
	}
	if cfg.TaskID == "" {
		return nil, fmt.Errorf("console: task ID is required")
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("console: failed to create input pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("console: failed to create output pipe: %w", err)
	}

	if err := os.MkdirAll(cfg.SocketDir, 0o755); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("console: failed to create socket dir: %w", err)
	}

	socketPath := SocketPath(cfg.SocketDir, cfg.TaskID)
	// Remove a stale socket from a previous crash so Listen does not fail
	// with "address already in use".
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("console: failed to remove stale socket: %w", err)
	}

	// Use ListenConfig so the listen call is context-aware (noctx).
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("console: failed to listen on %s: %w", socketPath, err)
	}
	// The console is a root-equivalent shell into the VM; restrict access to
	// the owner (the daemon runs as root).
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("console: failed to set socket permissions: %w", err)
	}

	scrollback := cfg.ScrollbackBytes
	if scrollback == 0 {
		scrollback = defaultScrollbackBytes
	}
	if scrollback < 0 {
		scrollback = 0
	}

	mirror := cfg.Mirror
	if mirror == nil {
		mirror = io.Discard
	}

	logger := zerolog.Nop()
	if cfg.Logger != nil {
		logger = *cfg.Logger
	}

	s := &Server{
		taskID:     cfg.TaskID,
		socketPath: socketPath,
		listener:   listener,
		stdinR:     stdinR,
		stdinW:     stdinW,
		stdoutR:    stdoutR,
		stdoutW:    stdoutW,
		hub:        newHub(scrollback),
		mirror:     mirror,
		logger:     logger,
		done:       make(chan struct{}),
		conns:      make(map[net.Conn]struct{}),
	}

	s.logger.Debug().
		Str("task_id", cfg.TaskID).
		Str("socket", socketPath).
		Msg("VM console listening")

	return s, nil
}

// SocketPath returns the VM console socket path.
func (s *Server) SocketPath() string { return s.socketPath }

// TaskID returns the VM's task ID.
func (s *Server) TaskID() string { return s.taskID }

// Stdin returns the read end of the pipe that must be given to the Firecracker
// process as its standard input.
func (s *Server) Stdin() *os.File { return s.stdinR }

// Stdout returns the write end of the pipe that must be given to the
// Firecracker process as its standard output.
func (s *Server) Stdout() *os.File { return s.stdoutW }

// Start begins bridging after the Firecracker process has been started. It
// closes the parent's copies of the child's pipe ends so that EOF is observed
// when the VM exits, then runs the reader and accept loops.
func (s *Server) Start() {
	// Firecracker now owns these fds; drop our copies.
	_ = s.stdinR.Close()
	_ = s.stdoutW.Close()

	s.wg.Add(2)
	go s.readLoop()
	go s.acceptLoop()
}

// Wait blocks until all bridge goroutines have returned. It must only be called
// after Close (Close itself never blocks, so it can be called from a goroutine).
func (s *Server) Wait() { s.wg.Wait() }

// Close tears the console down: it stops accepting clients, disconnects any
// attached client, closes the pipes and removes the socket. It is safe to call
// multiple times and from any goroutine.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)

		if err := s.listener.Close(); err != nil && !isClosedErr(err) {
			s.logger.Debug().Err(err).Msg("VM console listener close failed")
		}
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			s.logger.Debug().Err(err).Msg("Failed to remove VM console socket")
		}

		// Closing the pipes makes readLoop return and detaches the guest's
		// serial input (upstream Firecracker treats EOF as "input detached").
		_ = s.stdinW.Close()
		_ = s.stdoutR.Close()
		// Drop any remaining client copies of the child ends.
		_ = s.stdinR.Close()
		_ = s.stdoutW.Close()

		s.connMu.Lock()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.connMu.Unlock()

		s.hub.Close()

		s.logger.Debug().Str("task_id", s.taskID).Msg("VM console closed")
	})
}

func (s *Server) readLoop() {
	defer s.wg.Done()

	buf := make([]byte, 32<<10)
	for {
		n, err := s.stdoutR.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if _, werr := s.mirror.Write(chunk); werr != nil {
				s.logger.Debug().Err(werr).Msg("VM console mirror write failed")
			}
			s.hub.Broadcast(chunk)
		}
		if err != nil {
			s.logger.Debug().Str("task_id", s.taskID).Err(err).Msg("VM serial output closed")
			s.Close()
			return
		}
	}
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				// Expected during shutdown.
			default:
				s.logger.Debug().Err(err).Msg("VM console accept failed")
			}
			return
		}

		s.connMu.Lock()
		s.conns[conn] = struct{}{}
		s.connMu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(conn)
			s.connMu.Lock()
			delete(s.conns, conn)
			s.connMu.Unlock()
		}()
	}
}

// serve pipes raw bytes between one attached client and the guest console.
func (s *Server) serve(conn net.Conn) {
	defer conn.Close()

	ch, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	clientGone := make(chan struct{})
	go func() {
		// Guest input. A client disconnect surfaces as EOF/error here.
		_, _ = io.Copy(s.stdinW, conn)
		close(clientGone)
	}()

	for {
		select {
		case <-s.done:
			return
		case <-clientGone:
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}
}

func isClosedErr(err error) bool {
	return err == net.ErrClosed
}
