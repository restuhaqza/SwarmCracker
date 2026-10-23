package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

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
}

// Manager resolves a task to its node and proxies a caller's request there.
type Manager struct {
	tasks     TaskReader
	nodes     NodeReader
	port      int
	transport http.RoundTripper
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

	return &Manager{
		tasks:     cfg.Tasks,
		nodes:     cfg.Nodes,
		port:      port,
		transport: transport,
	}, nil
}

// Resolve maps a task ref/name to the task's node and the node broker URL:
// https://<node host>:<port>/v1/tasks/<resolved task ID>/<op>.
//
// The caller's reference is resolved through the task store, so the node URL
// always carries a concrete task ID rather than user input. A task source or
// node source failure is returned unchanged; the public handler maps it.
func (m *Manager) Resolve(ctx context.Context, taskRef, op string) (Route, error) {
	if !operationPattern.MatchString(op) {
		return Route{}, output.NewErrorf(output.CodeInvalid, "unsupported broker operation %q", op)
	}

	task, err := m.tasks.GetTask(ctx, taskRef)
	if err != nil {
		return Route{}, err
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
// "unreachable".
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
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, _ error) {
			// The underlying transport error can name an internal address, so
			// the document stays stable and a later public handler decides what
			// the caller is told.
			writeError(rw, http.StatusServiceUnavailable, codeUnreachable,
				"node %q broker is unreachable", route.Node.ID)
		},
	}

	proxy.ServeHTTP(w, r.WithContext(ctx))
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
