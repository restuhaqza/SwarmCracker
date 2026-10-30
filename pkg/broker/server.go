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

// operationSpec is the method set of one broker operation. It is static: the
// node contract defines the methods, and a handler cannot widen them. Knowing
// them lets the server answer 405 for a wrong method even when the operation
// itself is disabled, and reserve 501 for the method the operation really
// implements.
type operationSpec struct {
	methods []string
}

// allow renders the spec's methods for the Allow header, in declaration order.
func (s operationSpec) allow() string { return strings.Join(s.methods, ", ") }

// allows reports whether the operation implements method.
func (s operationSpec) allows(method string) bool {
	for _, have := range s.methods {
		if have == method {
			return true
		}
	}
	return false
}

// operationSpecs is the registry of known operations and the methods each one
// implements. An operation missing here is unknown: an unregistered one is 501
// for every method, because there is no method set to enforce.
//
// A name may carry one namespace segment (migration/export): the route spells it
// as two path segments, and splitTaskPath only joins them when the joined name is
// registered here, so an extra segment can never smuggle a different operation.
var operationSpecs = map[string]operationSpec{
	"exec":      {methods: []string{http.MethodPost}},
	"attach":    {methods: []string{http.MethodPost}},
	"logs":      {methods: []string{http.MethodGet}},
	"files":     {methods: []string{http.MethodGet, http.MethodPut}},
	"health":    {methods: []string{http.MethodGet}},
	"metrics":   {methods: []string{http.MethodGet}},
	"snapshots": {methods: []string{http.MethodGet, http.MethodPost, http.MethodDelete}},
	// restored stops the process a snapshot restore started for one task. It is
	// a DELETE because it removes that VM, and it takes no body and no query.
	"restored": {methods: []string{http.MethodDelete}},
	// The migration operations move one artifact: export produces it on the
	// source, file streams one of its files, stage receives one on the target.
	MigrationExportOperation: {methods: []string{http.MethodPost}},
	MigrationFileOperation:   {methods: []string{http.MethodGet}},
	MigrationStageOperation:  {methods: []string{http.MethodPut}},
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

	// A wrong method is a request the operation cannot implement, not a missing
	// capability: it is 405 with Allow whether or not a handler is registered.
	// Only the operation's real method can reach the 501 below.
	if spec, known := operationSpecs[op]; known && !spec.allows(r.Method) {
		w.Header().Set("Allow", spec.allow())
		writeTypedError(w, http.StatusMethodNotAllowed, output.NewErrorf(output.CodeInvalid,
			"method %s is not allowed on operation %q", r.Method, op).
			WithDetails(map[string]any{"reason": "method_not_allowed"}))
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

// splitTaskPath parses /v1/tasks/{taskID}/{op}, where {op} is one operation name
// and therefore one or, for a namespaced operation such as migration/export, two
// path segments. Anything else - a wrong prefix, a missing segment, an empty
// segment, extra segments - is malformed and reported as not found.
//
// The namespaced form is resolved against operationSpecs rather than by splitting
// on the first segment: "/v1/tasks/t/exec/extra" stays a malformed path, because
// no operation is named "exec/extra", and a future two-word operation is a
// registry entry plus a handler rather than another special case here.
func splitTaskPath(path string) (taskID, op string, ok bool) {
	rest, found := strings.CutPrefix(path, tasksPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")

	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], true
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		op = parts[1] + "/" + parts[2]
		if _, known := operationSpecs[op]; !known {
			return "", "", false
		}
		return parts[0], op, true
	default:
		return "", "", false
	}
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

// serveTaskDocument answers one per-task read with a 200 and the v1 media type.
// noun names what was read ("health", "metrics") so the one failure message
// stays stable per operation while the encoding and headers live in one place.
func serveTaskDocument(w http.ResponseWriter, taskID, noun string, doc any) {
	body, err := encodeDocument(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, output.CodeInternal,
			"task %q %s could not be encoded", taskID, noun)
		return
	}

	w.Header().Set("Content-Type", publicMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: the body is a JSON document served as the v1 media type.
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
