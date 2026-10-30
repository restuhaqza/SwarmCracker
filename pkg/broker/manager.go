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
	"reflect"
	"regexp"
	"sort"
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

	// maxRelayedDocumentBytes bounds a successful snapshot document or page. A
	// page can legitimately carry long rootfs paths, so it uses the same 8 MiB
	// response budget pkg/client applies to one decoded response rather than the
	// much smaller error-body limit.
	maxRelayedDocumentBytes = 8 << 20

	// defaultHealthTimeout bounds one health fetch or metrics read when
	// ManagerConfig names no deadline. It is short on purpose: both are
	// best-effort, per-task enrichment and a list page fans out to several nodes.
	defaultHealthTimeout = 2 * time.Second
)

// operationPattern is the shape of an operation segment. Anything else (an
// uppercase or path-like value) is rejected before it can reach a URL. One
// namespace separator is allowed because the migration operations are named
// migration/export, migration/file and migration/stage; the node spells the same
// names as two path segments, and the escaped form the manager sends decodes back
// to them.
var operationPattern = regexp.MustCompile(`^[a-z]+(/[a-z]+)?$`)

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
	// HealthTimeout bounds one small per-task read on one node: a health fetch or
	// a metrics read. Zero means defaultHealthTimeout. It is per request so a node
	// that stops answering cannot hold a list fan-out open.
	HealthTimeout time.Duration
	// SnapshotTimeout bounds one snapshot operation (list, create, restore,
	// stop-restored or delete). Zero means DefaultSnapshotTimeout. Create and
	// restore copy a whole VM memory image and create also copies the task disk,
	// so the deadline is generous; it exists so a node that stops answering
	// cannot hold a public request open.
	SnapshotTimeout time.Duration
	// MigrationTimeout bounds one migration transfer: an export, a file read, a
	// stage or a discard. Zero means DefaultMigrationTimeout. It is separate from
	// SnapshotTimeout because a transfer is bounded by the link rather than by
	// the disk: an artifact may carry 64 GiB (migration.MaxFileSize), which the
	// snapshot deadline would cut off on anything but a fast network. The deadline
	// still exists so a node that stops answering mid-stream cannot pin a
	// migration open forever.
	MigrationTimeout time.Duration
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
	tasks            TaskReader
	nodes            NodeReader
	port             int
	client           *http.Client
	transport        http.RoundTripper
	healthTimeout    time.Duration
	snapshotTimeout  time.Duration
	migrationTimeout time.Duration
	errorWriter      func(w http.ResponseWriter, r *http.Request, err error)
	requestIDFrom    func(ctx context.Context) string
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
		snapshotTimeout = DefaultSnapshotTimeout
	}

	migrationTimeout := cfg.MigrationTimeout
	if migrationTimeout <= 0 {
		migrationTimeout = DefaultMigrationTimeout
	}

	return &Manager{
		tasks:            cfg.Tasks,
		nodes:            cfg.Nodes,
		port:             port,
		client:           cfg.Client,
		transport:        transport,
		healthTimeout:    healthTimeout,
		snapshotTimeout:  snapshotTimeout,
		migrationTimeout: migrationTimeout,
		errorWriter:      cfg.ErrorWriter,
		requestIDFrom:    cfg.RequestIDFrom,
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

	route, err := m.routeForNode(ctx, task.NodeID, task.ID, op)
	if err != nil {
		return Route{}, err
	}
	route.Task = task
	return route, nil
}

// routeForNode maps a node ID and a placeholder task ID to the node broker URL.
// It is the node-scoped form of routeFor, for the operations that must address a
// node which has no task of its own: a migration moves an artifact onto the
// target before SwarmKit has created the task that will restore it, so the
// manager cannot resolve that node from a task.
//
// The URL has the one shape every node operation has - /v1/tasks/{id}/{op} - and
// the placeholder is only that segment. The migration operations other than export
// are artifact-scoped and never read it; export, which does read a task, always
// passes the real task.
func (m *Manager) routeForNode(ctx context.Context, nodeID, taskID, op string) (Route, error) {
	if !operationPattern.MatchString(op) {
		return Route{}, output.NewErrorf(output.CodeInvalid, "unsupported broker operation %q", op)
	}
	if taskID == "" {
		return Route{}, output.NewError(output.CodeInvalid, "task ID must not be empty")
	}
	if nodeID == "" {
		return Route{}, output.NewError(output.CodeInvalid, "node ID must not be empty")
	}

	node, err := m.nodes.GetNode(ctx, nodeID)
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
	// one segment instead of changing the route shape. The operation is escaped
	// per segment, so a namespaced name keeps its separator: the node reads
	// migration/export as two path segments, and an escaped slash would reach it
	// as one.
	target := &url.URL{
		Scheme:  brokerScheme,
		Host:    net.JoinHostPort(host, strconv.Itoa(m.port)),
		Path:    tasksPrefix + taskID + "/" + op,
		RawPath: tasksPrefix + url.PathEscape(taskID) + "/" + escapeOperation(op),
	}

	return Route{Node: node, URL: target}, nil
}

// escapeOperation escapes an operation name segment by segment, keeping the one
// namespace separator operationPattern allows.
func escapeOperation(op string) string {
	segments := strings.Split(op, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
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
		return nil, transportError(ctx, route.Node.ID, "health", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
		// Decode into a value, not a *v1.TaskHealth: a "null" body succeeds with a
		// nil document, and a pointer predicate would then dereference nil.
		health, err := decodeNodeDocument(resp.Body, route.Node.ID, "health",
			func(health v1.TaskHealth) bool { return health.Status != "" })
		if err != nil {
			return nil, err
		}
		return &health, nil
	default:
		return nil, decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "health")
	}
}

// decodeNodeDocument reads one task document a node returned, shared by every
// per-task read. A node that breaks the wire contract is an internal failure
// rather than a half-filled document; complete reports whether the decoded value
// can stand in for a reading (zero is a valid sample, so "non-empty" is not a
// safe rule for every document).
//
// Callers decode into a value type, never a pointer: a JSON "null" decodes
// successfully into a nil pointer, and a panic here would escape through the
// reader goroutine pkg/apihttp starts without a recover. isNilDocument keeps that
// mistake from turning into a daemon crash even if a later caller forgets.
func decodeNodeDocument[T any](body io.Reader, nodeID, noun string, complete func(T) bool) (T, error) {
	var doc T
	decoder := json.NewDecoder(io.LimitReader(body, maxRelayedErrorBytes))
	if err := decoder.Decode(&doc); err != nil || isNilDocument(doc) || !complete(doc) {
		var zero T
		return zero, output.NewErrorf(output.CodeInternal,
			"node %q returned an unreadable %s document", nodeID, noun).
			WithDetails(map[string]any{"nodeId": nodeID})
	}
	return doc, nil
}

// isNilDocument reports whether a decoded document is nil. An interface, map,
// slice, func, channel or pointer decodes a JSON "null" as nil without an error,
// so a completeness predicate for such a type would dereference nil.
func isNilDocument[T any](doc T) bool {
	value := reflect.ValueOf(doc)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// decodeNodeError relays a node error document. Its code, message and retry hint
// are the contract, so they are preserved verbatim; a body that is not a v1
// error document becomes a stable unavailable failure that names no address.
// noun names the operation ("health", "metrics") for that fallback wording.
func decodeNodeError(body io.Reader, status int, nodeID, noun string) error {
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
	return output.NewErrorf(codeUnavailable, "node %q %s unavailable (status %d)",
		nodeID, noun, status)
}

// transportError maps a failed dial or a deadline to a retryable failure, shared
// by every per-task read. The underlying error can name an internal address, so
// it never reaches the message. A node that cannot be reached is unavailable:
// "unreachable" is client-synthesized and never appears in a server document.
// noun names the operation ("health", "metrics").
func transportError(ctx context.Context, nodeID, noun string, err error) *output.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return output.NewErrorf(output.CodeTimeout, "node %q %s read timed out", nodeID, noun).
			WithDetails(map[string]any{"nodeId": nodeID})
	}
	return output.NewErrorf(codeUnavailable, "node %q broker is unavailable", nodeID).
		WithDetails(map[string]any{"nodeId": nodeID})
}

// Metrics fetches one task's last in-guest CPU/memory sample from the node that
// hosts it. The node answers from the reading its own poller cached, so the
// manager neither scrapes Prometheus nor dials the guest.
//
// A node 404 is returned as its typed not_found error rather than as an absent
// value: "no_sample" and "task_not_running" are different answers to an
// autoscaler ("skip this replica" versus "this replica is gone"), so the code
// and details travel to the caller to branch on.
func (m *Manager) Metrics(ctx context.Context, task v1.Task) (v1.TaskMetrics, error) {
	route, err := m.routeFor(ctx, task, MetricsOperation)
	if err != nil {
		return v1.TaskMetrics{}, err
	}

	// The health deadline applies: a metrics read is the same shape of work — one
	// small request to one node — and a caller fans it out the same way.
	callCtx, cancel := context.WithTimeout(ctx, m.healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, route.URL.String(), nil)
	if err != nil {
		return v1.TaskMetrics{}, output.NewErrorf(output.CodeInternal,
			"build node %q metrics request", route.Node.ID)
	}
	if m.requestIDFrom != nil {
		if id := m.requestIDFrom(ctx); id != "" {
			req.Header.Set(requestIDHeader, id)
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return v1.TaskMetrics{}, transportError(ctx, route.Node.ID, "metrics", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return v1.TaskMetrics{}, decodeNodeError(resp.Body, resp.StatusCode, route.Node.ID, "metrics")
	}
	// Zero is a valid reading for an idle guest, so an empty document cannot
	// stand in for one.
	return decodeNodeDocument(resp.Body, route.Node.ID, "metrics",
		func(sample v1.TaskMetrics) bool { return sample.TaskID != "" })
}

// --- snapshots ---
//
// The four snapshot methods are manager-built requests: the caller's token never
// travels to a node, so there is nothing to strip. They share one route rule
// (routeFor), one deadline and one wire decoder, and they relay a node's typed
// error unchanged so the public handler can answer the same code.

// CreateSnapshot snapshots a running task's VM on its own node. The task's
// service and node IDs travel as internal query parameters so the store can group
// snapshots for retention; req becomes the node's JSON body.
func (m *Manager) CreateSnapshot(ctx context.Context, task v1.Task, req v1.SnapshotCreateRequest) (v1.Snapshot, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return v1.Snapshot{}, output.NewError(output.CodeInternal,
			"the snapshot create request could not be encoded")
	}

	query := make(url.Values, 2)
	if task.ServiceID != "" {
		query.Set("service", task.ServiceID)
	}
	if task.NodeID != "" {
		query.Set("node", task.NodeID)
	}
	return m.snapshotDocument(ctx, task, http.MethodPost, query, body)
}

// RestoreSnapshot restores one of a task's snapshots on the node that holds it.
func (m *Manager) RestoreSnapshot(ctx context.Context, task v1.Task, snapshotID string) (v1.Snapshot, error) {
	if !v1.ValidSnapshotID(snapshotID) {
		return v1.Snapshot{}, errInvalidSnapshotID()
	}
	return m.snapshotDocument(ctx, task, http.MethodPost, url.Values{"restore": {snapshotID}}, nil)
}

// ListTaskSnapshots lists the snapshots the task's node holds for it, ordered by
// (createdAt, id) ascending. The manager sorts rather than trusting the node so
// pagination is stable regardless of the node's own order.
func (m *Manager) ListTaskSnapshots(ctx context.Context, task v1.Task) ([]v1.Snapshot, error) {
	resp, route, err := m.callSnapshot(ctx, task, snapshotOperation, http.MethodGet, nil, nil)
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
	sortSnapshots(doc.Items)
	return doc.Items, nil
}

// StopRestored stops the restored VM a previous restore started for the task on
// its node. It is a DELETE with no body and no query; a task the node does not
// track answers 404 not_found with details.reason "no_restored_vm", which is
// relayed as the typed not-found error.
func (m *Manager) StopRestored(ctx context.Context, task v1.Task) error {
	resp, route, err := m.callSnapshot(ctx, task, restoredOperation, http.MethodDelete, nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a restored VM stop", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	}
	return decodeSnapshotNodeError(resp.Body, resp.StatusCode, route.Node.ID)
}

// DeleteSnapshot removes one of a task's snapshots.
func (m *Manager) DeleteSnapshot(ctx context.Context, task v1.Task, snapshotID string) error {
	if !v1.ValidSnapshotID(snapshotID) {
		return errInvalidSnapshotID()
	}

	resp, route, err := m.callSnapshot(ctx, task, snapshotOperation, http.MethodDelete, url.Values{"id": {snapshotID}}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// A 2xx that is not 204 did not delete: only the 204 contract is a
		// success here.
		return output.NewErrorf(output.CodeInternal,
			"node %q returned status %d for a snapshot delete", route.Node.ID, resp.StatusCode).
			WithDetails(map[string]any{"nodeId": route.Node.ID})
	}
	return decodeSnapshotNodeError(resp.Body, resp.StatusCode, route.Node.ID)
}

// snapshotDocument performs a create or restore and decodes the 200/201 document.
func (m *Manager) snapshotDocument(
	ctx context.Context, task v1.Task, method string, query url.Values, body []byte,
) (v1.Snapshot, error) {
	resp, route, err := m.callSnapshot(ctx, task, snapshotOperation, method, query, body)
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
// one request under the snapshot deadline. op names the broker operation —
// snapshotOperation for the four snapshot methods, restoredOperation for a
// stop-restored. A non-nil body is sent as the node's JSON document. The
// returned body owns the deadline: closing it releases the context, so a caller
// must always close it.
func (m *Manager) callSnapshot(
	ctx context.Context, task v1.Task, op, method string, query url.Values, body []byte,
) (*http.Response, Route, error) {
	route, err := m.routeFor(ctx, task, op)
	if err != nil {
		return nil, Route{}, err
	}

	var reader io.Reader
	length := int64(-1)
	contentType := ""
	if body != nil {
		reader = bytes.NewReader(body)
		length = int64(len(body))
		contentType = publicMediaType
	}

	resp, err := m.doNodeRequest(ctx, route, method, query, reader, length, contentType, m.snapshotTimeout, "snapshot")
	if err != nil {
		return nil, route, err
	}
	return resp, route, nil
}

// doNodeRequest performs one manager-built request to one node under timeout: a
// whole-VM operation in either direction is the same shape of work, whether it
// moves a snapshot's metadata or streams a memory image, so the one transport
// error mapping is shared. The deadline is the caller's because a snapshot
// operation and a migration transfer are bounded by different things - a disk
// copy and a network link - and the same generous number would be wrong for one
// of them.
//
// body may be nil. length is the body's byte count; a negative value leaves it to
// the transport and is what a request without a body uses. contentType is set only
// when one is given. The returned body owns the deadline: closing it releases the
// context, so a caller must always close it. noun names the operation in the two
// failure messages ("snapshot", "migration export").
func (m *Manager) doNodeRequest(
	ctx context.Context, route Route, method string, query url.Values,
	body io.Reader, length int64, contentType string, timeout time.Duration, noun string,
) (*http.Response, error) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)

	target := *route.URL
	if len(query) > 0 {
		target.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(callCtx, method, target.String(), body)
	if err != nil {
		cancel()
		return nil, output.NewErrorf(output.CodeInternal,
			"build node %q %s request", route.Node.ID, noun)
	}
	if length >= 0 {
		req.ContentLength = length
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if m.requestIDFrom != nil {
		if id := m.requestIDFrom(ctx); id != "" {
			req.Header.Set(requestIDHeader, id)
		}
	}

	resp, err := m.client.Do(req)
	if err != nil {
		cancel()
		return nil, snapshotTransportError(ctx, route.Node.ID, err)
	}
	resp.Body = &snapshotResponseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
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
	return json.NewDecoder(io.LimitReader(body, maxRelayedDocumentBytes)).Decode(out)
}

// sortSnapshots orders a page by (createdAt, id) ascending. Timestamps are
// RFC3339Nano strings; parsing them is required because a fractional second is
// lexicographically smaller than the bare second ("…00.5Z" < "…00Z") even though
// it is later in time. A value that does not parse falls back to a string compare.
func sortSnapshots(items []v1.Snapshot) {
	sort.SliceStable(items, func(i, j int) bool {
		return snapshotBefore(items[i], items[j])
	})
}

// snapshotBefore reports whether a sorts before b by (createdAt, id).
func snapshotBefore(a, b v1.Snapshot) bool {
	if at, aErr := time.Parse(time.RFC3339Nano, a.CreatedAt); aErr == nil {
		if bt, bErr := time.Parse(time.RFC3339Nano, b.CreatedAt); bErr == nil {
			if !at.Equal(bt) {
				return at.Before(bt)
			}
		}
	}
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	return a.ID < b.ID
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
