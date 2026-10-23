package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// tasksPrefix is the only route a node broker serves:
	// /v1/tasks/{taskID}/{op}. It is an internal contract shared with the
	// manager proxy, not part of the public API.
	tasksPrefix = "/v1/tasks/"

	// readHeaderTimeout bounds how long a manager may take to send headers.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout bounds the graceful drain when the daemon stops. It is
	// short because a broker handler may be streaming; the daemon then closes
	// the connections rather than hang its own shutdown.
	shutdownTimeout = 5 * time.Second
)

// Broker error codes. They are aliases of the contract taxonomy so the server
// and later handlers cannot drift from the public error codes.
const (
	codeNotFound       = output.CodeNotFound
	codeNotImplemented = output.CodeNotImplemented
	codeUnavailable    = output.CodeUnavailable
	codeUnreachable    = output.CodeUnreachable
)

// Handler is one operation (exec, logs, files, health) for one task. It
// receives the resolved task and the original request, including method,
// query, headers and body.
type Handler func(ctx context.Context, task Task, w http.ResponseWriter, r *http.Request)

// ServerConfig configures a node broker.
type ServerConfig struct {
	Addr      string
	TLSConfig *tls.Config // must carry the cluster server certificate and ClientCAs
	Tasks     TaskSource
}

// Server is the node side of the broker: an mTLS HTTP server with a registry
// of per-operation handlers.
type Server struct {
	addr      string
	tlsConfig *tls.Config
	tasks     TaskSource

	mu       sync.RWMutex
	handlers map[string]Handler

	addrMu    sync.RWMutex
	boundAddr string
}

// NewServer validates the configuration. A server without TLS is refused:
// the broker never speaks plain HTTP.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.TLSConfig == nil {
		return nil, errors.New("broker: a TLS config carrying the cluster certificate is required")
	}
	if cfg.Tasks == nil {
		return nil, errors.New("broker: a task source is required")
	}

	return &Server{
		addr:      cfg.Addr,
		tlsConfig: cfg.TLSConfig,
		tasks:     cfg.Tasks,
		handlers:  make(map[string]Handler),
	}, nil
}

// Handle registers an operation name. Duplicate or empty names are errors, so
// two phases cannot silently overwrite each other's handler.
func (s *Server) Handle(op string, h Handler) error {
	if op == "" {
		return errors.New("broker: operation name must not be empty")
	}
	if h == nil {
		return fmt.Errorf("broker: handler for operation %q must not be nil", op)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.handlers[op]; exists {
		return fmt.Errorf("broker: operation %q is already registered", op)
	}
	s.handlers[op] = h
	return nil
}

// Handler returns the mux used by tests and Serve.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.route)
}

// Listen binds a TCP listener and records the actual address; it does not wrap
// TLS (Serve does that).
func (s *Server) Listen() (net.Listener, error) {
	// A bind has no request lifetime to inherit: ListenConfig is used only so
	// the call carries a context, as the linter requires.
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("broker: listen on %s: %w", s.addr, err)
	}

	s.addrMu.Lock()
	s.boundAddr = lis.Addr().String()
	s.addrMu.Unlock()

	return lis, nil
}

// Serve serves TLS until ctx is cancelled, then shuts down gracefully. It uses
// the TLSConfig from NewServer, so every connection is mutually authenticated.
func (s *Server) Serve(ctx context.Context, lis net.Listener) error {
	if lis == nil {
		return errors.New("broker: Serve needs a listener")
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	tlsListener := tls.NewListener(lis, s.tlsConfig)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(tlsListener) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("broker: shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("broker: serve: %w", err)
	}
}

// Addr returns the address bound by Listen, or the configured address before
// Listen.
func (s *Server) Addr() string {
	s.addrMu.RLock()
	defer s.addrMu.RUnlock()
	if s.boundAddr != "" {
		return s.boundAddr
	}
	return s.addr
}

// route resolves the task, then dispatches to the registered operation.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	taskID, op, ok := splitTaskPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown broker path %q", r.URL.Path)
		return
	}

	task, found, err := s.tasks.Lookup(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "resolve task %q: %v", taskID, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, "task %q not found on this node", taskID)
		return
	}

	h, ok := s.handlerFor(op)
	if !ok {
		writeError(w, http.StatusNotImplemented, codeNotImplemented, "operation %q is not implemented on this node", op)
		return
	}

	h(r.Context(), task, w, r)
}

// handlerFor returns the registered operation, if any.
func (s *Server) handlerFor(op string) (Handler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.handlers[op]
	return h, ok
}

// splitTaskPath parses /v1/tasks/{taskID}/{op}. Anything else - a wrong
// prefix, a missing segment, an empty segment, extra segments - is malformed
// and reported as not found.
func splitTaskPath(path string) (taskID, op string, ok bool) {
	rest, found := strings.CutPrefix(path, tasksPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// writeError writes one v1 error document, shared by the node server and the
// manager proxy so both emit the same envelope on the internal wire.
// output.Error.Document wraps the payload in v1.NewErrorDocument with the
// code's retry semantics.
func writeError(w http.ResponseWriter, status int, code output.Code, format string, args ...any) {
	writeTypedError(w, status, output.NewErrorf(code, format, args...))
}

// writeTypedError is writeError for an error that already carries details.
func writeTypedError(w http.ResponseWriter, status int, typed *output.Error) {
	body, err := encodeDocument(typed.Document())
	if err != nil {
		body = []byte(`{"schemaVersion":1,"kind":"Error","error":{"code":"internal","message":"encode error","retryable":false}}`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: body is a JSON document served as application/json.
	_, _ = w.Write(body)
}

// encodeDocument serializes one document with a trailing newline and without
// HTML escaping, matching pkg/apihttp and pkg/output.
func encodeDocument(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
