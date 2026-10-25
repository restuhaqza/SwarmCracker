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
	"strconv"
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

// Client is the surface the HTTP layer needs. It is deliberately narrower than
// pkg/client: the server depends on the behaviour, not the transport, so tests
// and future backends can implement it without SwarmKit.
//
// The write methods take v1.WriteOptions rather than HTTP headers so the
// transport stays out of the client: the same interface serves the local
// SwarmKit path and the remote HTTP path.
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

	CreateService(context.Context, v1.ServiceWrite, v1.WriteOptions) (v1.Service, error)
	UpdateService(context.Context, string, []byte, v1.WriteOptions) (v1.Service, error)
	DeleteService(context.Context, string, v1.WriteOptions) (v1.Service, error)
	RollbackService(context.Context, string, v1.WriteOptions) (v1.Service, error)

	CreateConfig(context.Context, v1.ConfigWrite, v1.WriteOptions) (v1.Config, error)
	UpdateConfig(context.Context, string, []byte, v1.WriteOptions) (v1.Config, error)
	DeleteConfig(context.Context, string, v1.WriteOptions) (v1.Config, error)
	CreateSecret(context.Context, v1.SecretWrite, v1.WriteOptions) (v1.Secret, error)
	UpdateSecret(context.Context, string, []byte, v1.WriteOptions) (v1.Secret, error)
	DeleteSecret(context.Context, string, v1.WriteOptions) (v1.Secret, error)
	CreateNetwork(context.Context, v1.NetworkWrite, v1.WriteOptions) (v1.Network, error)
	DeleteNetwork(context.Context, string, v1.WriteOptions) (v1.Network, error)
	UpdateNode(context.Context, string, []byte, v1.WriteOptions) (v1.Node, error)

	// WatchEvents opens the manager's change stream. The events channel is
	// closed at end of stream; errors after the stream started arrive on the
	// error channel. An initial failure (an expired cursor, for example) is the
	// returned error.
	WatchEvents(context.Context, v1.EventOptions) (<-chan v1.Event, <-chan error, error)
}

// Broker is the manager's node-broker transport. The public handler depends on
// the behaviour (resolve, authorize, relay), not on pkg/broker's concrete
// Manager, so tests can substitute one and the packages stay acyclic.
type Broker interface {
	Proxy(ctx context.Context, w http.ResponseWriter, r *http.Request, taskRef, op string) error
	// Health reads one task's live health from the node that hosts it. A nil
	// TaskHealth with a nil error means the task publishes none: that is an
	// absent value, not a failure.
	Health(ctx context.Context, task v1.Task) (*v1.TaskHealth, error)
}

// Config is the server's wiring. Tokens may be nil for a Unix-socket-only
// deployment: Handler then rejects every request, UnixHandler still works.
type Config struct {
	Client Client
	Tokens *TokenStore
	// Broker relays task-scoped operations (exec today) to the node that runs
	// the task. A nil broker leaves those routes answering 501.
	Broker Broker
	// Snapshots is the manager-side transport for the experimental snapshot
	// surface. It is deliberately a second, optional interface so the exec,
	// logs, files and health fakes do not have to grow four methods. A nil
	// transport leaves the snapshot routes answering 501.
	Snapshots SnapshotBroker
	// SnapshotsEnabled gates the experimental snapshot surface. Snapshots are
	// disabled by default: with this false every snapshot route answers 501
	// not_implemented with details.capability "snapshots", before the task is
	// resolved or audited.
	SnapshotsEnabled bool
	// SnapshotTimeout bounds one create or restore request. Zero means 30s.
	SnapshotTimeout time.Duration
	// Audit is the manager-local audit log. It is optional: a nil store makes
	// recordAudit a no-op and GET /v1/audit answer 501.
	Audit *AuditStore
	// Idempotency is the manager-local Idempotency-Key store. It is optional: New
	// installs a default store (24 h TTL) when it is nil.
	Idempotency *IdempotencyStore
	// CertDir holds tls.crt and tls.key for the TLS listener. Serve requires it
	// only when a TCP address is configured; EnsureCertificate writes the pair.
	CertDir       string
	ServerVersion string
	GRPCVersion   string
	ClusterID     string
	// Capabilities is the advertised capability list. Nil means the served
	// surface: read, write and events. New always adds the broker operations
	// (exec, logs, files, health) when Broker is non-nil, so a supplied list can
	// narrow the read/write surface but never deny an operation the server
	// actually serves: a wired broker and GET /v1/version cannot disagree.
	Capabilities []string
	// EventHeartbeat is the SSE/NDJSON keep-alive interval. Zero means 15s;
	// tests set a short interval.
	EventHeartbeat time.Duration
	// HealthPageBudget is the wall-clock budget of one task list's health
	// fan-out. Zero means 5s; tests set a short budget.
	HealthPageBudget time.Duration
	// RateLimit is the per-token bucket. Zero installs the 10 req/s, burst 20
	// default in New.
	RateLimit *RateLimiter
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
		cfg.Capabilities = []string{"read", "write", "events"}
	}
	// A wired broker serves these operations, so they are advertised no matter
	// what the caller supplied. This package owns the advertisement; an embedder
	// that builds a broker cannot forget the capability it enables. Snapshots
	// are experimental and gated separately: they are only promised when the
	// capability, its transport and the shared broker transport are all wired.
	// A supplied list that claims snapshots while the capability is disabled is
	// stripped: the version document must never promise a route that answers 501.
	snapshotsAdvertised := cfg.Broker != nil && cfg.SnapshotsEnabled && cfg.Snapshots != nil
	if !snapshotsAdvertised {
		cfg.Capabilities = withoutCapability(cfg.Capabilities, snapshotCapability)
	}
	cfg.Capabilities = withBrokerCapabilities(cfg.Capabilities, cfg.Broker != nil, snapshotsAdvertised)
	if cfg.EventHeartbeat <= 0 {
		cfg.EventHeartbeat = defaultEventHeartbeat
	}
	if cfg.HealthPageBudget <= 0 {
		cfg.HealthPageBudget = healthPageBudget
	}
	if cfg.RateLimit == nil {
		cfg.RateLimit = NewRateLimiter(DefaultRatePerSecond, DefaultRateBurst)
	}
	// Idempotency is manager-local and in memory; every server gets one so a
	// POST with an Idempotency-Key is never silently unprotected.
	if cfg.Idempotency == nil {
		cfg.Idempotency = NewIdempotencyStore(DefaultIdempotencyTTL)
	}

	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// brokerCapabilities are the operations only a configured Broker can serve.
// They are the single list New unions into the advertisement, so the version
// document cannot promise less than the routes reach.
var brokerCapabilities = []string{"exec", "logs", "files", "health"}

// withBrokerCapabilities returns capabilities plus every operation the wired
// transports serve, preserving the caller's order and appending the missing ones
// in the canonical order. An explicitly narrowed list keeps its narrowing for
// every other capability; it cannot remove an operation the server serves.
//
// Snapshots are listed last and only when both the shared broker transport and
// the snapshot capability are enabled: the routes need a node broker to relay
// through, so advertising them without one would be a promise the server cannot
// keep.
func withBrokerCapabilities(capabilities []string, broker, snapshots bool) []string {
	extra := make([]string, 0, len(brokerCapabilities)+1)
	if broker {
		extra = append(extra, brokerCapabilities...)
	}
	if broker && snapshots {
		extra = append(extra, snapshotCapability)
	}

	effective := make([]string, len(capabilities), len(capabilities)+len(extra))
	copy(effective, capabilities)

	present := make(map[string]struct{}, len(effective))
	for _, capability := range effective {
		present[capability] = struct{}{}
	}
	for _, capability := range extra {
		if _, ok := present[capability]; ok {
			continue
		}
		effective = append(effective, capability)
	}
	return effective
}

// withoutCapability returns capabilities without name, preserving order. It is
// how a gated capability that is disabled is removed from a caller-supplied list:
// the version document must not promise a route that answers 501.
func withoutCapability(capabilities []string, name string) []string {
	if len(capabilities) == 0 {
		return capabilities
	}

	kept := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability != name {
			kept = append(kept, capability)
		}
	}
	return kept
}

// routes registers every route with the method it serves and the scope it
// requires.
//
// Scope map (contract §8.1): the resource a route touches decides the scope, and
// the service-tasks subresource belongs to the task reader, not the service
// reader. A path is registered exactly once, with all of its methods, so the
// catch-all handler that produces a typed 405 cannot collide with another
// registration.
func (s *Server) routes() {
	s.handleRoutes("/v1/version", read(ScopeClusterRead, s.getVersion))

	s.handleRoutes("/v1/services",
		read(ScopeServicesRead, s.listServices),
		write(http.MethodPost, ScopeServicesWrite, s.createService),
	)
	s.handleRoutes("/v1/services/{id}",
		read(ScopeServicesRead, s.getService),
		write(http.MethodPatch, ScopeServicesWrite, s.updateService),
		write(http.MethodDelete, ScopeServicesWrite, s.deleteService),
	)
	s.handleRoutes("/v1/services/{id}/tasks", read(ScopeTasksRead, s.listServiceTasks))
	s.handleRoutes("/v1/services/{id}/rollback",
		write(http.MethodPost, ScopeServicesWrite, s.rollbackService))

	s.handleRoutes("/v1/tasks", read(ScopeTasksRead, s.listTasks))
	s.handleRoutes("/v1/tasks/{id}", read(ScopeTasksRead, s.getTask))
	// Exec is a stream, not a JSON document: it must not pass through the
	// Idempotency-Key middleware, which buffers a response to replay it.
	s.handleRoutes("/v1/tasks/{id}/exec",
		streamPost(ScopeTasksExec, s.execTask))
	// Logs are an NDJSON stream and files are raw bytes. Neither is a JSON
	// document, and a PUT must not be buffered by the Idempotency-Key
	// middleware, so only POST ever takes that path.
	s.handleRoutes("/v1/tasks/{id}/logs", read(ScopeTasksLogs, s.taskLogs))
	s.handleRoutes("/v1/tasks/{id}/files",
		read(ScopeTasksFiles, s.getTaskFile),
		write(http.MethodPut, ScopeTasksFiles, s.putTaskFile),
	)
	// Snapshots are experimental and capability-gated. The task-scoped forms are
	// the primary path; the flat /v1/snapshots forms require ?task= because a
	// snapshot lives on one node.
	s.handleRoutes("/v1/tasks/{id}/snapshots",
		read(ScopeTasksSnapshots, s.listTaskSnapshots),
		write(http.MethodPost, ScopeTasksSnapshots, s.createTaskSnapshot),
	)
	s.handleRoutes("/v1/tasks/{id}/snapshots/{sid}/restore",
		write(http.MethodPost, ScopeTasksSnapshots, s.restoreTaskSnapshot))
	s.handleRoutes("/v1/tasks/{id}/snapshots/{sid}",
		write(http.MethodDelete, ScopeTasksSnapshots, s.deleteTaskSnapshot))
	s.handleRoutes("/v1/snapshots", read(ScopeTasksSnapshots, s.listSnapshots))
	s.handleRoutes("/v1/snapshots/{id}/restore",
		write(http.MethodPost, ScopeTasksSnapshots, s.restoreSnapshot))
	s.handleRoutes("/v1/snapshots/{id}",
		write(http.MethodDelete, ScopeTasksSnapshots, s.deleteSnapshot))

	s.handleRoutes("/v1/nodes",
		read(ScopeNodesRead, s.listNodes),
	)
	s.handleRoutes("/v1/nodes/{id}",
		read(ScopeNodesRead, s.getNode),
		write(http.MethodPatch, ScopeNodesWrite, s.updateNode),
	)

	s.handleRoutes("/v1/networks",
		read(ScopeNetworksRead, s.listNetworks),
		write(http.MethodPost, ScopeNetworksWrite, s.createNetwork),
	)
	s.handleRoutes("/v1/networks/{id}",
		read(ScopeNetworksRead, s.getNetwork),
		write(http.MethodDelete, ScopeNetworksWrite, s.deleteNetwork),
	)

	s.handleRoutes("/v1/configs",
		read(ScopeConfigsRead, s.listConfigs),
		write(http.MethodPost, ScopeConfigsWrite, s.createConfig),
	)
	s.handleRoutes("/v1/configs/{id}",
		read(ScopeConfigsRead, s.getConfig),
		write(http.MethodPatch, ScopeConfigsWrite, s.updateConfig),
		write(http.MethodDelete, ScopeConfigsWrite, s.deleteConfig),
	)

	s.handleRoutes("/v1/secrets",
		read(ScopeSecretsRead, s.listSecrets),
		write(http.MethodPost, ScopeSecretsWrite, s.createSecret),
	)
	s.handleRoutes("/v1/secrets/{id}",
		read(ScopeSecretsRead, s.getSecret),
		write(http.MethodPatch, ScopeSecretsWrite, s.updateSecret),
		write(http.MethodDelete, ScopeSecretsWrite, s.deleteSecret),
	)

	s.handleRoutes("/v1/audit", read(ScopeAuditRead, s.listAudit))
	s.handleRoutes("/v1/events", read(ScopeEventsRead, s.watchEvents))

	// Every path is owned by this package: an unknown one is a typed 404, never
	// ServeMux's plain-text "404 page not found".
	s.mux.HandleFunc("/", s.notFound)
}

// route is one method on one path: the scope it needs and the handler it runs.
type route struct {
	method string
	scope  Scope
	handle http.HandlerFunc
	// streaming marks a route whose response is a stream. It opts out of the
	// Idempotency-Key middleware, which buffers a whole response to replay it.
	streaming bool
}

// read registers a GET route.
func read(scope Scope, h http.HandlerFunc) route {
	return route{method: http.MethodGet, scope: scope, handle: h}
}

// write registers one mutating method. Writes authenticate and negotiate the
// request media type in the same middleware as reads; the handler checks its own
// body media type after the route scope has been enforced.
func write(method string, scope Scope, h http.HandlerFunc) route {
	return route{method: method, scope: scope, handle: h}
}

// streamPost registers a POST whose response is streamed rather than a single
// document. It is the only route that skips the Idempotency-Key middleware.
func streamPost(scope Scope, h http.HandlerFunc) route {
	return route{method: http.MethodPost, scope: scope, handle: h, streaming: true}
}

// handleRoutes registers every method of one path plus the typed method
// rejection for the rest.
//
// The scope check lives inside each method registration, so a token that lacks
// the write scope gets a 403 with details.requiredScope, not a 405: the route
// exists, the caller may not use it. HEAD is registered explicitly (405) because
// Go's ServeMux would otherwise serve it through the GET pattern.
func (s *Server) handleRoutes(pattern string, routes ...route) {
	methods := make([]string, 0, len(routes))
	for _, rt := range routes {
		handler := rt.handle
		if rt.method == http.MethodPost && !rt.streaming {
			// A POST is a create or a rollback: the only routes an
			// Idempotency-Key can protect. Auth and scope run first, so an
			// unauthorized request never reserves a key.
			handler = s.idempotent(handler)
		}
		s.mux.HandleFunc(rt.method+" "+pattern, s.requireScope(rt.scope, handler))
		methods = append(methods, rt.method)
	}

	allow := strings.Join(methods, ", ")
	s.mux.HandleFunc(http.MethodHead+" "+pattern, s.methodNotAllowed(allow))
	s.mux.HandleFunc(pattern, s.methodNotAllowed(allow))
}

// requireScope is the per-route authorization check. It reads the Identity the
// middleware stored and denies by default.
func (s *Server) requireScope(scope Scope, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok || !identity.HasScope(scope) {
			s.writeError(w, r, forbiddenScope(scope))
			return
		}
		h(w, r)
	}
}

// Handler returns the TCP listener's handler: every request must carry a valid
// bearer token. The token's role and scopes then decide which routes it reaches.
func (s *Server) Handler() http.Handler { return s.wrap(true) }

// UnixHandler returns the trusted local-socket handler. It performs no token
// check: the socket's file mode is the credential.
func (s *Server) UnixHandler() http.Handler { return s.wrap(false) }

// wrap applies the middleware shared by both listeners. The order is deliberate:
// establish identity and version headers for every response, authenticate before
// doing any work on the authenticated listener, then negotiate the media type.
//
// The authenticated listener stores the token's Identity; the Unix listener
// stores LocalIdentity, because the socket's file mode is the credential.
func (s *Server) wrap(requireAuth bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := newRequestID()
		w.Header().Set(apiVersionHeader, APIVersion)
		w.Header().Set(requestIDHeader, requestID)

		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		r = withCleanPath(r)

		identity := LocalIdentity()
		if requireAuth {
			var err error
			identity, err = s.authenticate(r)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
		}
		r = r.WithContext(withIdentity(r.Context(), identity))

		if allowed, retryAfter := s.cfg.RateLimit.Allow(rateLimitKey(identity)); !allowed {
			seconds := int((retryAfter + time.Second - 1) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			s.writeError(w, r, rateLimitError(retryAfter, s.cfg.RateLimit))
			return
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
	return RequestIDFromContext(r.Context())
}

// RequestIDFromContext returns the request ID the middleware stored on a
// request's context, or "" when there is none. It is the reader the daemon wires
// into the broker manager, so a node-side fetch can carry the caller's request
// ID without the broker importing this package.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
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
		case "*/*", "application/json", MediaType, "text/event-stream", "application/x-ndjson",
			"application/octet-stream":
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

// authenticate validates the bearer token on the TCP listener and returns the
// identity it establishes. Every 401 response byte is unchanged from phase 1:
// the failure modes and their messages are the contract.
func (s *Server) authenticate(r *http.Request) (Identity, error) {
	header := strings.TrimSpace(r.Header.Get(authorizationHeader))
	if header == "" {
		return Identity{}, output.NewError(output.CodeUnauthenticated, "missing bearer token")
	}

	scheme, secret, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return Identity{}, output.NewError(output.CodeUnauthenticated, "authorization must use the Bearer scheme")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Identity{}, output.NewError(output.CodeUnauthenticated, "missing bearer token")
	}

	if s.cfg.Tokens == nil {
		return Identity{}, output.NewError(output.CodeUnauthenticated, "no API tokens are configured")
	}

	token, ok := s.cfg.Tokens.Authenticate(secret)
	if !ok {
		// A wrong secret and an expired record are the same answer: the caller
		// learns nothing about which tokens exist.
		return Identity{}, output.NewError(output.CodeUnauthenticated, "invalid bearer token")
	}

	return identityForToken(token), nil
}

// identityForToken converts a stored token into the request Identity. The scope
// set is exactly what the store issued; the role is kept for the task-selector
// rule and for diagnostics.
func identityForToken(token Token) Identity {
	scopes := make([]Scope, 0, len(token.Scopes))
	for _, scope := range token.Scopes {
		scopes = append(scopes, Scope(scope))
	}

	return Identity{
		Type:      "token",
		TokenID:   token.ID,
		Name:      token.Name,
		Role:      token.Role,
		Scopes:    scopes,
		Selectors: token.TaskSelectors,
	}
}

// writeJSON writes one successful document with the phase-1 validator: the
// quoted SHA-256 of the exact body bytes. Lists keep this; a single service
// carries its SwarmKit revision instead.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	s.writeJSONWithETag(w, r, status, value, "")
}

// writeJSONWithETag writes one successful document with an explicit validator.
// An empty etag falls back to the body hash, so a document without a revision
// still answers If-None-Match.
func (s *Server) writeJSONWithETag(w http.ResponseWriter, r *http.Request, status int, value any, etag string) {
	body, err := encodeJSON(value)
	if err != nil {
		s.writeError(w, r, output.NewErrorf(output.CodeInternal, "encode response: %v", err))
		return
	}
	if etag == "" {
		etag = etagFor(body)
	}

	w.Header().Set("Content-Type", MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(etagHeader, etag)

	// If-None-Match is a conditional read. On a write, the same header carries a
	// different meaning (create-only on POST), so it must not turn a success
	// into a 304.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(status)
	//nolint:gosec // G705: body is a JSON document served as application/json.
	_, _ = w.Write(body)
}

// WriteError writes one public v1 error document with the status implied by its
// code, using the request ID the middleware stored on the request. It is
// exported for the broker manager's ErrorWriter hook: a transport failure is
// detected inside the proxy, but the public envelope belongs to this package.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	typed := typedError(err)
	writeErrorDocument(w, r, statusForCode(typed.Code), typed)
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
	writeErrorDocument(w, r, status, typedError(err))
}

// writeErrorDocument renders one typed failure. It is the only place the v1
// error envelope is built for the public server.
func writeErrorDocument(w http.ResponseWriter, r *http.Request, status int, typed *output.Error) {
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
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	//nolint:gosec // G705: body is a JSON document served as application/json.
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
	case output.CodeInvalid, output.CodeAmbiguous, output.CodeUsage:
		return http.StatusBadRequest
	case output.CodeCursorExpired:
		return http.StatusGone
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

// methodNotAllowed answers a known path with a typed 405. The allowed methods
// are the ones registered for that path, in registration order.
//
// The body keeps the stable `invalid` code and names the plumbing in
// details.reason (contract §4.2), so an SDK can tell "wrong method" from any
// other invalid request without growing its exception set.
func (s *Server) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		s.writeErrorStatus(w, r, http.StatusMethodNotAllowed,
			output.NewErrorf(output.CodeInvalid,
				"method %s is not allowed on %s", r.Method, r.URL.Path).
				WithDetails(map[string]any{"reason": "method_not_allowed"}))
	}
}
