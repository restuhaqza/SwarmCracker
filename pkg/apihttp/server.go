// This file owns the HTTP server: the Client contract it reads through, the
// route table, the response envelope helpers and the per-request middleware
// (request ID, content negotiation, bearer auth).
package apihttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// MediaType is the only body media type API v1 serves or accepts.
	MediaType = "application/vnd.swarmcracker.v1+json"
	// APIVersion is the value of X-SwarmCracker-Api-Version on every response.
	APIVersion = "1"

	apiVersionHeader    = "X-SwarmCracker-Api-Version"
	requestIDHeader     = "X-Request-Id"
	authorizationHeader = "Authorization"
	acceptHeader        = "Accept"

	bearerScheme = "Bearer"

	requestIDPrefix = "req_"
	requestIDBytes  = 6 // 12 lowercase hex characters

	etagHeader = "ETag"
)

// Client is the read surface the HTTP layer needs. It is deliberately narrower
// than pkg/client: the server depends on the behaviour, not the transport, so
// tests and future backends can implement it without SwarmKit.
type Client interface {
	ListServices(context.Context) ([]v1.Service, error)
	GetService(context.Context, string) (v1.Service, error)
	ListServiceTasks(context.Context, string) ([]v1.Task, error)
	ListTasks(context.Context) ([]v1.Task, error)
	GetTask(context.Context, string) (v1.Task, error)
	ListNodes(context.Context) ([]v1.Node, error)
	GetNode(context.Context, string) (v1.Node, error)
	ListNetworks(context.Context) ([]v1.Network, error)
	GetNetwork(context.Context, string) (v1.Network, error)
	ListConfigs(context.Context) ([]v1.Config, error)
	GetConfig(context.Context, string) (v1.Config, error)
	ListSecrets(context.Context) ([]v1.Secret, error)
	GetSecret(context.Context, string) (v1.Secret, error)
	ClusterID(context.Context) (string, error)
}

// Config is the server's wiring. Tokens may be nil for a Unix-socket-only
// deployment: Handler then rejects every request, UnixHandler still works.
type Config struct {
	Client        Client
	Tokens        *TokenStore
	ServerVersion string
	GRPCVersion   string
	ClusterID     string
	Capabilities  []string
}

// Server serves the v1 read surface. It is safe for concurrent use once New has
// returned.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New validates cfg and builds the route table.
func New(cfg Config) (*Server, error) {
	if cfg.Client == nil {
		return nil, output.NewError(output.CodeInvalid, "apihttp: Config.Client must not be nil")
	}
	if cfg.Capabilities == nil {
		cfg.Capabilities = []string{"read"}
	}

	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// routes registers every phase-1 read route. Each path also gets a
// method-agnostic handler so a non-GET request returns a typed 405 rather than
// ServeMux's plain-text default.
func (s *Server) routes() {
	s.handle("/v1/version", s.getVersion)

	s.handle("/v1/services", s.listServices)
	s.handle("/v1/services/{id}", s.getService)
	s.handle("/v1/services/{id}/tasks", s.listServiceTasks)

	s.handle("/v1/tasks", s.listTasks)
	s.handle("/v1/tasks/{id}", s.getTask)

	s.handle("/v1/nodes", s.listNodes)
	s.handle("/v1/nodes/{id}", s.getNode)

	s.handle("/v1/networks", s.listNetworks)
	s.handle("/v1/networks/{id}", s.getNetwork)

	s.handle("/v1/configs", s.listConfigs)
	s.handle("/v1/configs/{id}", s.getConfig)

	s.handle("/v1/secrets", s.listSecrets)
	s.handle("/v1/secrets/{id}", s.getSecret)

	// Every path is owned by this package: an unknown one is a typed 404, never
	// ServeMux's plain-text "404 page not found".
	s.mux.HandleFunc("/", s.notFound)
}

// handle registers the GET route and the typed rejections for every other
// method on the same path. The method-specific patterns are more specific, so
// GET wins. HEAD is registered explicitly: without it Go's ServeMux would serve
// HEAD through the GET pattern, and this surface is strictly GET-only.
func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(http.MethodGet+" "+pattern, h)
	s.mux.HandleFunc(http.MethodHead+" "+pattern, s.methodNotAllowed)
	s.mux.HandleFunc(pattern, s.methodNotAllowed)
}

// Handler returns the TCP listener's handler: every request must carry a valid
// reader or admin bearer token.
func (s *Server) Handler() http.Handler { return s.wrap(true) }

// UnixHandler returns the trusted local-socket handler. It performs no token
// check: the socket's file mode is the credential.
func (s *Server) UnixHandler() http.Handler { return s.wrap(false) }

// wrap applies the middleware shared by both listeners. The order is deliberate:
// establish identity and version headers for every response, authenticate before
// doing any work on the authenticated listener, then negotiate the media type.
func (s *Server) wrap(requireAuth bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set(apiVersionHeader, APIVersion)
		w.Header().Set(requestIDHeader, requestID)

		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		r = withCleanPath(r)

		if requireAuth {
			if err := s.authenticate(r); err != nil {
				s.writeError(w, r, err)
				return
			}
		}
		if err := checkAccept(r); err != nil {
			s.writeError(w, r, err)
			return
		}
		if err := checkQuery(r); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

// requestIDKey is the context key for the per-request ID.
type requestIDKey struct{}

// withCleanPath canonicalizes the request path before routing. ServeMux
// answers a non-canonical path (a doubled slash or a "."/".." segment) with its
// own text/html 307, which would bypass the typed v1 envelope. Cleaning here
// keeps every response a v1 document and preserves a single trailing slash so
// the router still applies its own subtree rules.
func withCleanPath(r *http.Request) *http.Request {
	if r.URL == nil || r.URL.Path == "" {
		return r
	}

	cleaned := path.Clean(r.URL.Path)
	if cleaned != "/" && strings.HasSuffix(r.URL.Path, "/") {
		cleaned += "/"
	}
	if cleaned == r.URL.Path {
		return r
	}

	u := *r.URL
	u.Path = cleaned
	u.RawPath = ""
	r.URL = &u
	return r
}

func requestIDFrom(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// newRequestID returns "req_" plus 12 lowercase hex characters.
func newRequestID() string {
	var buf [requestIDBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail on supported platforms; keeping the format
		// stable is better than failing a request that has not started yet.
		return requestIDPrefix + fmt.Sprintf("%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	}
	return requestIDPrefix + hex.EncodeToString(buf[:])
}

// checkAccept rejects an Accept header that names only unsupported versions.
func checkAccept(r *http.Request) error {
	raw := strings.TrimSpace(r.Header.Get(acceptHeader))
	if raw == "" {
		return nil
	}

	for _, part := range strings.Split(raw, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		switch mediaType {
		case "*/*", "application/json", MediaType:
			return nil
		}
	}

	return output.NewErrorf(output.CodeUnsupportedVersion,
		"unsupported Accept header; this server serves %s", MediaType).
		WithDetails(map[string]any{"supported": []string{"v1"}})
}

// checkQuery rejects a query string that does not parse. url.Values silently
// drops malformed parameters, which would otherwise turn a bad cursor or limit
// into "absent" and answer 200.
func checkQuery(r *http.Request) error {
	if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
		return output.NewErrorf(output.CodeInvalid, "malformed query string: %v", err)
	}
	return nil
}

// authenticate validates the bearer token on the TCP listener.
func (s *Server) authenticate(r *http.Request) error {
	header := strings.TrimSpace(r.Header.Get(authorizationHeader))
	if header == "" {
		return output.NewError(output.CodeUnauthenticated, "missing bearer token")
	}

	scheme, secret, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return output.NewError(output.CodeUnauthenticated, "authorization must use the Bearer scheme")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return output.NewError(output.CodeUnauthenticated, "missing bearer token")
	}

	if s.cfg.Tokens == nil {
		return output.NewError(output.CodeUnauthenticated, "no API tokens are configured")
	}

	token, ok := s.cfg.Tokens.Authenticate(secret)
	if !ok {
		return output.NewError(output.CodeUnauthenticated, "invalid bearer token")
	}

	switch token.Role {
	case RoleReader, RoleAdmin:
		return nil
	default:
		// The store rejects unknown roles at creation time; this is defence in
		// depth for a hand-edited token file.
		return output.NewErrorf(output.CodeForbidden, "token role %q may not read the API", token.Role)
	}
}

// writeJSON writes one successful document. The ETag is the quoted SHA-256 of
// the exact body bytes, so a validator can be compared without re-serializing.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	body, err := encodeJSON(value)
	if err != nil {
		s.writeError(w, r, output.NewErrorf(output.CodeInternal, "encode response: %v", err))
		return
	}

	etag := etagFor(body)
	w.Header().Set("Content-Type", MediaType)
	w.Header().Set(etagHeader, etag)

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError writes one typed error document with the status implied by its
// code. Every failure carries the same request ID that the middleware put in
// X-Request-Id.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	typed := typedError(err)
	s.writeErrorStatus(w, r, statusForCode(typed.Code), typed)
}

// writeErrorStatus writes a typed error document with an explicit status, for
// failures whose status the taxonomy cannot infer (405).
func (s *Server) writeErrorStatus(w http.ResponseWriter, r *http.Request, status int, err error) {
	typed := typedError(err)

	// Copy rather than mutate: the same *output.Error value may be shared by
	// many requests (a client's sentinel error, for example).
	payload := v1.Error{
		Code:      string(typed.Code),
		Message:   typed.Message,
		RequestID: requestIDFrom(r),
		Retryable: typed.Retryable,
		Details:   typed.Details,
	}

	body, encodeErr := encodeJSON(v1.NewErrorDocument(payload))
	if encodeErr != nil {
		body = []byte(`{"schemaVersion":1,"kind":"Error","error":{"code":"internal","message":"encode error","retryable":false}}`)
	}

	if typed.Code == output.CodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", bearerScheme)
	}
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// encodeJSON serializes one document with a trailing newline and without HTML
// escaping, matching pkg/output so the API and the CLI agree on text bytes.
func encodeJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// typedError normalizes any failure to the stable taxonomy. A *output.Error is
// already typed; anything else is first mapped through the gRPC classifier.
func typedError(err error) *output.Error {
	var typed *output.Error
	if errors.As(err, &typed) {
		return typed
	}
	if mapped := output.FromGRPC(err); errors.As(mapped, &typed) {
		return typed
	}
	return output.NewError(output.CodeInternal, err.Error())
}

// statusForCode maps the stable error code to its HTTP status.
func statusForCode(code output.Code) int {
	switch code {
	case output.CodeInvalid, output.CodeAmbiguous, output.CodeCursorExpired, output.CodeUsage:
		return http.StatusBadRequest
	case output.CodeNotFound:
		return http.StatusNotFound
	case output.CodeUnauthenticated:
		return http.StatusUnauthorized
	case output.CodeForbidden:
		return http.StatusForbidden
	case output.CodeConflict:
		return http.StatusConflict
	case output.CodeRateLimited:
		return http.StatusTooManyRequests
	case output.CodeUnavailable, output.CodeUnreachable:
		return http.StatusServiceUnavailable
	case output.CodeTimeout:
		return http.StatusGatewayTimeout
	case output.CodeNotImplemented:
		return http.StatusNotImplemented
	case output.CodeUnsupportedVersion:
		return http.StatusNotAcceptable
	default:
		return http.StatusInternalServerError
	}
}

// etagFor returns the quoted SHA-256 of body.
func etagFor(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// etagMatches implements If-None-Match: "*" matches any representation, W/
// prefixes are ignored and a comma-separated list is accepted.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// notFound answers an unknown path with a typed 404.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, output.NewErrorf(output.CodeNotFound,
		"no route for %s %s", r.Method, r.URL.Path).
		WithDetails(map[string]any{"resource": "route"}))
}

// methodNotAllowed answers a known path with a typed 405. The read surface is
// GET-only, so Allow names exactly that method.
func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "GET")
	s.writeErrorStatus(w, r, http.StatusMethodNotAllowed, output.NewErrorf(output.CodeInvalid,
		"method %s is not allowed on %s", r.Method, r.URL.Path))
}
