package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// defaultPort is the fixed broker port. Nodes advertise no separate broker
	// address: the manager reuses the node address it already knows.
	defaultPort = 4245
	// brokerScheme is the only scheme the broker uses. There is no plain-HTTP
	// fallback.
	brokerScheme = "https"

	// publicMediaType and publicAPIVersion are the public envelope's type and
	// version. They are duplicated here rather than imported so the broker does
	// not depend on pkg/apihttp (whose Broker interface points back at Manager).
	publicMediaType  = "application/vnd.swarmcracker.v1+json"
	publicAPIVersion = "1"
	apiVersionHeader = "X-SwarmCracker-Api-Version"
	requestIDHeader  = "X-Request-Id"

	// maxRelayedErrorBytes bounds an error body the manager decodes. A broker
	// error document is far smaller; the cap stops a hostile node from making
	// the manager buffer an unbounded body.
	maxRelayedErrorBytes = 1 << 20

	// defaultHealthTimeout bounds one health fetch when ManagerConfig names no
	// deadline. It is short on purpose: health is best-effort enrichment and a
	// list page fans out to several nodes.
	defaultHealthTimeout = 2 * time.Second
)

// operationPattern is the shape of an operation segment. Anything else (an
// uppercase or path-like value) is rejected before it can reach a URL.
var operationPattern = regexp.MustCompile(`^[a-z]+$`)

// TaskReader reads one task. It is a narrow view of the manager's task store,
// deliberately independent of SwarmKit control types.
type TaskReader interface {
	GetTask(context.Context, string) (v1.Task, error)
}

// NodeReader reads one node.
type NodeReader interface {
	GetNode(context.Context, string) (v1.Node, error)
}

// ManagerConfig configures the manager side of the broker.
type ManagerConfig struct {
	Tasks  TaskReader
	Nodes  NodeReader
	Port   int          // default 4245
	Client *http.Client // required; transport must carry the cluster mTLS config
	// HealthTimeout bounds one health fetch. Zero means defaultHealthTimeout.
	// It is per request so a node that stops answering cannot hold a list
	// fan-out open.
	HealthTimeout time.Duration
	// SnapshotTimeout bounds one snapshot operation (list, create, restore or
	// delete). Zero means defaultSnapshotTimeout. Create and restore copy a whole
	// VM memory image, so the deadline is generous; it exists so a node that
	// stops answering cannot hold a public request open.
	SnapshotTimeout time.Duration
	// ErrorWriter renders a transport failure as the caller's error envelope.
	// The public handler passes apihttp.WriteError so a public caller gets the
	// v1 media type and its request ID; nil keeps the internal v1 default.
	ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)
	// RequestIDFrom reads the caller's request ID from a context. The public
	// handler passes its own reader (apihttp.RequestIDFromContext), so a node
	// can name the request that caused a fetch without this package importing
	// pkg/apihttp. Nil omits the header.
	RequestIDFrom func(ctx context.Context) string
}

// Manager resolves a task to its node and proxies a caller's request there.
type Manager struct {
	tasks           TaskReader
	nodes           NodeReader
	port            int
	client          *http.Client
	transport       http.RoundTripper
	healthTimeout   time.Duration
	snapshotTimeout time.Duration
	errorWriter     func(w http.ResponseWriter, r *http.Request, err error)
	requestIDFrom   func(ctx context.Context) string
}

// Route is one resolved task-to-node destination inside the cluster.
type Route struct {
	Task v1.Task
	Node v1.Node
	URL  *url.URL
}

// NewManager validates the configuration. The HTTP client is required because
// its transport is the only place the cluster mTLS configuration lives; the
// manager does not build a second client of its own.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Tasks == nil {
		return nil, errors.New("broker: a task reader is required")
	}
	if cfg.Nodes == nil {
		return nil, errors.New("broker: a node reader is required")
	}
	if cfg.Client == nil {
		return nil, errors.New("broker: an HTTP client carrying the cluster mTLS transport is required")
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("broker: invalid port %d", port)
	}

	transport := cfg.Client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	healthTimeout := cfg.HealthTimeout
	if healthTimeout <= 0 {
		healthTimeout = defaultHealthTimeout
	}

	snapshotTimeout := cfg.SnapshotTimeout
	if snapshotTimeout <= 0 {
		snapshotTimeout = defaultSnapshotTimeout
	}

	return &Manager{
		tasks:           cfg.Tasks,
		nodes:           cfg.Nodes,
		port:            port,
		client:          cfg.Client,
		transport:       transport,
		healthTimeout:   healthTimeout,
		snapshotTimeout: snapshotTimeout,
		errorWriter:     cfg.ErrorWriter,
		requestIDFrom:   cfg.RequestIDFrom,
	}, nil
}

// Resolve maps a task ref/name to the task's node and the node broker URL:
// https://<node host>:<port>/v1/tasks/<resolved task ID>/<op>.
//
// The caller's reference is resolved through the task store, so the node URL
// always carries a concrete task ID rather than user input. A task source or
// node source failure is returned unchanged; the public handler maps it.
func (m *Manager) Resolve(ctx context.Context, taskRef, op string) (Route, error) {
	task, err := m.tasks.GetTask(ctx, taskRef)
	if err != nil {
		return Route{}, err
	}
	return m.routeFor(ctx, task, op)
}

// routeFor maps an already-resolved task to its node and the node broker URL.
// It is the single node-address rule: the broker port is always the configured
// one and an advertised address carrying its own port is reduced to its host.
func (m *Manager) routeFor(ctx context.Context, task v1.Task, op string) (Route, error) {
	if !operationPattern.MatchString(op) {
		return Route{}, output.NewErrorf(output.CodeInvalid, "unsupported broker operation %q", op)
	}
	if task.ID == "" {
		return Route{}, output.NewError(output.CodeInvalid, "task ID must not be empty")
	}
	if task.NodeID == "" {
		return Route{}, output.NewErrorf(output.CodeUnavailable, "task %q is not assigned to a node", task.ID)
	}

	node, err := m.nodes.GetNode(ctx, task.NodeID)
	if err != nil {
		return Route{}, err
	}

	host := nodeHost(node.Address)
	if host == "" && node.Manager != nil {
		host = nodeHost(node.Manager.Address)
	}
	if host == "" {
		return Route{}, output.NewErrorf(output.CodeUnavailable, "node %q has no address to reach its broker", node.ID)
	}

	// Path and RawPath are set together: Path is the decoded form, RawPath the
	// escaped hint, so a task ID that carries a delimiter still round-trips as
	// one segment instead of changing the route shape.
	target := &url.URL{
		Scheme:  brokerScheme,
		Host:    net.JoinHostPort(host, strconv.Itoa(m.port)),
		Path:    tasksPrefix + task.ID + "/" + op,
		RawPath: tasksPrefix + url.PathEscape(task.ID) + "/" + url.PathEscape(op),
	}

	return Route{Task: task, Node: node, URL: target}, nil
}

// Proxy resolves and reverse-proxies the caller's request to the node broker.
// It strips Authorization, Proxy-Authorization and Cookie before forwarding, so
// the node never sees a public user token. A resolve error is returned to the
// caller; transport errors are rendered as a 503 v1 ErrorDocument with code
// "unavailable": the contract reserves "unreachable" for the client, which
// synthesizes it from its own failed dial.
func (m *Manager) Proxy(ctx context.Context, w http.ResponseWriter, r *http.Request, taskRef, op string) error {
	route, err := m.Resolve(ctx, taskRef, op)
	if err != nil {
		return err
	}

	target := *route.URL
	target.RawQuery = r.URL.RawQuery

	proxy := &httputil.ReverseProxy{
		// Flush immediately after every write: later operations stream NDJSON,
		// and a buffered copy would hold a log tail until the process exits.
		FlushInterval: -1,
		Transport:     m.transport,
		// Rewrite (not the deprecated Director) also leaves the raw query
		// untouched, so the node sees exactly what the caller sent.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = target.Path
			pr.Out.URL.RawPath = target.RawPath
			pr.Out.URL.RawQuery = target.RawQuery
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("Cookie")
			pr.SetXForwarded()
		},
		// A node error is an internal document; rewrite it as the public one so
		// the caller never sees application/json or a missing requestId. A
		// successful stream is not an error document and is left byte-for-byte
		// intact.
		ModifyResponse: rewriteRelayedError,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, _ error) {
			// The underlying transport error can name an internal address, so
			// the document stays stable and the configured writer decides what
			// the caller is told. A node down is unavailable, not unreachable:
			// the server never sends the client-synthesized code.
			m.writeProxyError(rw, req, output.NewErrorf(codeUnavailable,
				"node %q broker is unavailable", route.Node.ID).
				WithDetails(map[string]any{"nodeId": route.Node.ID}))
		},
	}

	proxy.ServeHTTP(w, r.WithContext(ctx))
	return nil
}

// writeProxyError renders a transport failure with the configured public error
// writer, or with the internal v1 document when none is set.
func (m *Manager) writeProxyError(w http.ResponseWriter, r *http.Request, err *output.Error) {
	if m.errorWriter != nil {
		m.errorWriter(w, r, err)
		return
	}
	writeTypedError(w, http.StatusServiceUnavailable, err)
}

// rewriteRelayedError converts an internal node error document into the public
// envelope: the v1 media type, the API version header and the caller's request
// ID. Every 4xx/5xx body is inspected, not a fixed status list, so a node's typed
// 400 (a directory instead of a file), 413 (an oversized upload) or 415 reaches
// the caller in the public envelope too. Anything that is not a v1 Error
// document — an NDJSON stream, an HTML error from a proxy, an empty body — is
// passed through unchanged.
func rewriteRelayedError(resp *http.Response) error {
	if resp.StatusCode < http.StatusBadRequest {
		return nil
	}
	if resp.Body == nil {
		return nil
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRelayedErrorBytes+1))
	_ = resp.Body.Close()
	// Restore a readable body before returning: ReverseProxy owns the copy of
	// whatever is left here.
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil || len(body) > maxRelayedErrorBytes {
		return nil //nolint:nilerr // the original body is forwarded unchanged
	}

	var doc v1.ErrorDocument
	if err := json.Unmarshal(body, &doc); err != nil || doc.Kind != v1.KindError {
		return nil //nolint:nilerr // not a v1 error document: pass it through
	}
	if doc.Error.RequestID == "" && resp.Request != nil {
		doc.Error.RequestID = resp.Request.Header.Get(requestIDHeader)
	}

	rewritten, err := encodeDocument(doc)
	if err != nil {
		return nil //nolint:nilerr // leave the original body in place
	}

	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	resp.Header.Set("Content-Type", publicMediaType)
	resp.Header.Set("X-Content-Type-Options", "nosniff")
	resp.Header.Set(apiVersionHeader, publicAPIVersion)
	return nil
}

// nodeHost extracts the host from a node address that may be a bare IP, an
// IPv6 literal, or host:port. The broker port is always the configured one.
func nodeHost(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	// A bracketed IPv6 literal without a port: SplitHostPort rejects it, and
	// the brackets must be gone before net.JoinHostPort adds them back.
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(address, "["), "]")
	}
	return address
}

// Health fetches one task's live health from the node that hosts it. The node
// reads the sidecar the executor publishes, so the manager never dials a
// loopback endpoint and never needs a second credential.
//
// (nil, nil) means "no health published": a node 204 (nothing probed yet) and a
// node 404 (the task has no VM on that node) are both absent, not failures, so a
// list fan-out must not report them as partial.
func (m *Manager) Health(ctx context.Context, task v1.Task) (*v1.TaskHealth, error) {
	route, err := m.routeFor(ctx, task, healthOperation)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, m.healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, route.URL.String(), nil)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal,
			"build node %q health request", route.Node.ID)
	}
	if m.requestIDFrom != nil {
		if id := m.requestIDFrom(ctx); id != "" {
			req.Header.Set(requestIDHeader, id)
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, healthTransportError(ctx, route.Node.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
		return decodeNodeHealth(resp.Body, route.Node.ID)
	default:
		return nil, decodeNodeHealthError(resp.Body, resp.StatusCode, route.Node.ID)
	}
}

// decodeNodeHealth reads one health document. A node that breaks the wire
// contract is an internal failure rather than a half-filled TaskHealth.
func decodeNodeHealth(body io.Reader, nodeID string) (*v1.TaskHealth, error) {
	var health v1.TaskHealth
	decoder := json.NewDecoder(io.LimitReader(body, maxRelayedErrorBytes))
	if err := decoder.Decode(&health); err != nil || health.Status == "" {
		return nil, output.NewErrorf(output.CodeInternal,
			"node %q returned an unreadable health document", nodeID)
	}
	return &health, nil
}

// decodeNodeHealthError relays a node error document. Its code, message and
// retry hint are the contract, so they are preserved; a body that is not a v1
// error document becomes a stable unavailable failure that names no address.
func decodeNodeHealthError(body io.Reader, status int, nodeID string) error {
	data, err := io.ReadAll(io.LimitReader(body, maxRelayedErrorBytes))
	if err == nil {
		var doc v1.ErrorDocument
		if unmarshalErr := json.Unmarshal(data, &doc); unmarshalErr == nil &&
			doc.Kind == v1.KindError && doc.Error.Code != "" {
			typed := output.NewError(output.Code(doc.Error.Code), doc.Error.Message)
			typed.Retryable = doc.Error.Retryable
			typed.Details = doc.Error.Details
			return typed
		}
	}
	return output.NewErrorf(codeUnavailable, "node %q health is unavailable (status %d)",
		nodeID, status)
}

// healthTransportError maps a failed dial or a deadline to a retryable failure.
// The underlying error can name an internal address, so it never reaches the
// message. A node that cannot be reached is unavailable: "unreachable" is
// client-synthesized and never appears in a server document.
func healthTransportError(ctx context.Context, nodeID string, err error) *output.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return output.NewErrorf(output.CodeTimeout, "node %q health check timed out", nodeID).
			WithDetails(map[string]any{"nodeId": nodeID})
	}
	return output.NewErrorf(codeUnavailable, "node %q broker is unavailable", nodeID).
		WithDetails(map[string]any{"nodeId": nodeID})
}

// --- snapshots ---
//
// The four snapshot methods are manager-built requests: the caller's token never
// travels to a node, so there is nothing to strip. They share one route rule
// (routeFor), one deadline and one wire decoder, and they relay a node's typed
// error unchanged so the public handler can answer the same code.

// CreateSnapshot snapshots a running task's VM on its own node.
func (m *Manager) CreateSnapshot(ctx context.Context, task v1.Task) (v1.Snapshot, error) {
	return m.snapshotDocument(ctx, task, http.MethodPost, nil)
}

// RestoreSnapshot restores one of a task's snapshots on the node that holds it.
func (m *Manager) RestoreSnapshot(ctx context.Context, task v1.Task, snapshotID string) (v1.Snapshot, error) {
	if !v1.ValidSnapshotID(snapshotID) {
		return v1.Snapshot{}, errInvalidSnapshotID()
	}
	return m.snapshotDocument(ctx, task, http.MethodPost, url.Values{"restore": {snapshotID}})
}

// ListTaskSnapshots lists the snapshots the task's node holds for it.
func (m *Manager) ListTaskSnapshots(ctx context.Context, task v1.Task) ([]v1.Snapshot, error) {
	resp, route, err := m.callSnapshot(ctx, task, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeSnapshotNodeError(resp.Body, resp.StatusCode, route.Node.ID)
	}

	var doc v1.SnapshotList
	if err := decodeSnapshotWire(resp.Body, &doc); err != nil || doc.Kind != v1.KindSnapshotList {
		return nil, snapshotUnreadable(route.Node.ID)
	}
	if doc.Items == nil {
		return []v1.Snapshot{}, nil
	}
	return doc.Items, nil
}

// DeleteSnapshot removes one of a task's snapshots.
func (m *Manager) DeleteSnapshot(ctx context.Context, task v1.Task, snapshotID string) error {
	if !v1.ValidSnapshotID(snapshotID) {
		return errInvalidSnapshotID()
	}

	resp, route, err := m.callSnapshot(ctx, task, http.MethodDelete, url.Values{"id": {snapshotID}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return decodeSnapshotNodeError(resp.Body, resp.StatusCode, route.Node.ID)
}

// snapshotDocument performs a create or restore and decodes the 200/201 document.
func (m *Manager) snapshotDocument(
	ctx context.Context, task v1.Task, method string, query url.Values,
) (v1.Snapshot, error) {
	resp, route, err := m.callSnapshot(ctx, task, method, query)
	if err != nil {
		return v1.Snapshot{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return v1.Snapshot{}, decodeSnapshotNodeError(resp.Body, resp.StatusCode, route.Node.ID)
	}

	var doc v1.SnapshotDocument
	if err := decodeSnapshotWire(resp.Body, &doc); err != nil || doc.ID == "" {
		return v1.Snapshot{}, snapshotUnreadable(route.Node.ID)
	}
	return doc.Snapshot, nil
}

// callSnapshot resolves the task's node, builds the operation URL and performs
// one request under the snapshot deadline. The returned body owns the deadline:
// closing it releases the context, so a caller must always close it.
func (m *Manager) callSnapshot(
	ctx context.Context, task v1.Task, method string, query url.Values,
) (*http.Response, Route, error) {
	route, err := m.routeFor(ctx, task, snapshotOperation)
	if err != nil {
		return nil, Route{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, m.snapshotTimeout)

	target := *route.URL
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(callCtx, method, target.String(), nil)
	if err != nil {
		cancel()
		return nil, route, output.NewErrorf(output.CodeInternal,
			"build node %q snapshot request", route.Node.ID)
	}
	if m.requestIDFrom != nil {
		if id := m.requestIDFrom(ctx); id != "" {
			req.Header.Set(requestIDHeader, id)
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		cancel()
		return nil, route, snapshotTransportError(ctx, route.Node.ID, err)
	}
	resp.Body = &snapshotResponseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, route, nil
}

// snapshotResponseBody releases the per-request deadline when the body closes.
type snapshotResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// Close closes the body and then releases the request deadline.
func (b *snapshotResponseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// snapshotTransportError maps a failed dial or a deadline to a stable failure.
// A node down is unavailable, never unreachable, and the message names no
// internal address.
func snapshotTransportError(ctx context.Context, nodeID string, err error) *output.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return output.NewErrorf(output.CodeTimeout, "node %q snapshot request timed out", nodeID).
			WithDetails(map[string]any{"nodeId": nodeID})
	}
	return output.NewErrorf(codeUnavailable, "node %q broker is unavailable", nodeID).
		WithDetails(map[string]any{"nodeId": nodeID})
}

// decodeSnapshotWire decodes one bounded snapshot document from the node wire.
func decodeSnapshotWire(body io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(body, maxRelayedErrorBytes)).Decode(out)
}

// decodeSnapshotNodeError relays a node error document: its code, message,
// retry hint and details are the contract. A body that is not a v1 error document
// becomes a stable unavailable failure that names no address.
func decodeSnapshotNodeError(body io.Reader, status int, nodeID string) error {
	data, err := io.ReadAll(io.LimitReader(body, maxRelayedErrorBytes))
	if err == nil {
		var doc v1.ErrorDocument
		if unmarshalErr := json.Unmarshal(data, &doc); unmarshalErr == nil &&
			doc.Kind == v1.KindError && doc.Error.Code != "" {
			typed := output.NewError(output.Code(doc.Error.Code), doc.Error.Message)
			typed.Retryable = doc.Error.Retryable
			typed.Details = doc.Error.Details
			return typed
		}
	}
	return output.NewErrorf(codeUnavailable, "snapshot request failed on node %q (status %d)",
		nodeID, status)
}

// snapshotUnreadable is the typed internal failure for a node that breaks the
// snapshot wire contract, so a caller never receives a half-filled document.
func snapshotUnreadable(nodeID string) *output.Error {
	return output.NewErrorf(output.CodeInternal,
		"node %q returned an unreadable snapshot document", nodeID).
		WithDetails(map[string]any{"nodeId": nodeID})
}

// errInvalidSnapshotID is the typed 400 for an id that could address a directory
// outside the snapshot root.
func errInvalidSnapshotID() *output.Error {
	return output.NewError(output.CodeInvalid, "snapshot ID is malformed").
		WithDetails(map[string]any{"parameter": "id"})
}
