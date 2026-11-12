// This file owns the sandbox control plane's client: the v1 sandbox routes
// (GET/POST /v1/sandboxes, GET/DELETE /v1/sandboxes/{id}, POST
// /v1/sandboxes/{id}/exec, POST /v1/sandboxes/{id}/tokens) over the node's Unix
// socket or over --host with a bearer token.
//
// It is its own client rather than a set of methods on Client because the two reach
// different servers: Client dials a cluster (the SwarmKit control socket or a
// manager's HTTPS API), while the sandbox control plane is a node's own daemon
// (sandboxd). A caller that only runs sandboxes must not need cluster certificates to
// do it — and a caller that has them still talks to the sandbox daemon, not to
// SwarmKit.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// SandboxSocketEnv names the sandbox daemon's control socket, so a deployment
	// that moved it does not have to pass a flag to every command.
	SandboxSocketEnv = "SWARM_SANDBOX_SOCKET"
	// DefaultSandboxSocket is where sandboxd listens when nothing says otherwise.
	DefaultSandboxSocket = "/var/run/swarmcracker/sandboxd.sock"
	// sandboxHost is the URL host on the Unix-socket transport. It is never
	// resolved: the transport dials the socket for every request.
	sandboxHost = "sandboxd"

	// sandboxClientTimeout bounds one sandbox request that answers with a document.
	// It is much longer than the manager API's default because a create is a boot: a
	// template is resolved, a disk is copied, a guest boots and its agent answers,
	// and the client must outlast all of it rather than report a timeout for a
	// sandbox that is coming up. The exec stream is not covered by it — a command may
	// take as long as its own context allows.
	sandboxClientTimeout = 10 * time.Minute
)

// SandboxSocketPath resolves the sandbox daemon's control socket: the explicit path,
// then the environment, then the default. One rule, so the daemon and the CLI cannot
// disagree about where it listens.
func SandboxSocketPath(explicit string) string {
	if path := strings.TrimSpace(explicit); path != "" {
		return path
	}
	if path := strings.TrimSpace(os.Getenv(SandboxSocketEnv)); path != "" {
		return path
	}
	return DefaultSandboxSocket
}

// sandboxRequestTimeout is how long one document-shaped sandbox call may take.
//
// A caller that named a timeout keeps it, and one that named none gets
// sandboxClientTimeout — never the manager API's read-sized default, which
// config.DefaultConfig carries and which a create would always exceed.
func sandboxRequestTimeout(configured time.Duration) time.Duration {
	if configured <= 0 || configured == DefaultConfig().Timeout {
		return sandboxClientTimeout
	}
	return configured
}

// SandboxClient speaks the sandbox control plane's API. It is safe for concurrent
// use.
type SandboxClient struct {
	base  *url.URL
	token string
	// socket is the control socket this client dials, or "" for a remote endpoint.
	// It is kept so a connection failure can name it: "is sandboxd running?" is the
	// question a caller has, and the path is the answer.
	socket string
	http   *http.Client
	stream *http.Client
}

// NewSandboxClient dials a node's sandbox control plane: the Unix socket the daemon
// listens on, or, when cfg.Endpoint is set, the manager's HTTPS API with cfg's CA and
// token.
//
// The two transports differ in exactly one thing: whether the credential is the
// socket's file mode or a bearer token. Nothing else about the client changes, so a
// command that works locally works remotely.
func NewSandboxClient(cfg Config) (*SandboxClient, error) {
	if cfg.Endpoint != "" {
		base, err := parseEndpoint(cfg.Endpoint)
		if err != nil {
			return nil, err
		}
		pool, err := loadCA(cfg.CAFile, cfg.Endpoint)
		if err != nil {
			return nil, err
		}
		token, err := loadToken(cfg)
		if err != nil {
			return nil, err
		}
		timeout := sandboxRequestTimeout(cfg.Timeout)

		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: cfg.ServerName,
				MinVersion: tls.VersionTLS12,
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}

		return &SandboxClient{
			base:   base,
			token:  token,
			http:   &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: refuseRedirect},
			stream: &http.Client{Transport: transport, CheckRedirect: refuseRedirect},
		}, nil
	}

	socket := SandboxSocketPath(cfg.SandboxSocket)
	base, err := url.Parse("http://" + sandboxHost)
	if err != nil {
		return nil, output.NewErrorf(output.CodeUsage, "sandbox socket %q: %v", socket, err)
	}
	transport := &http.Transport{
		// The socket's file mode is the credential: the Unix listener serves the
		// trusted local identity, which is an administrator. A dial failure is
		// reported as unreachable rather than as a 401, because there is no token to
		// be wrong.
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:       8,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		MaxConnsPerHost:    4,
	}

	timeout := sandboxRequestTimeout(cfg.Timeout)
	return &SandboxClient{
		base:   base,
		socket: socket,
		http:   &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: refuseRedirect},
		stream: &http.Client{Transport: transport, CheckRedirect: refuseRedirect},
	}, nil
}

// Socket reports the control socket this client dials, or "" for a remote endpoint.
// It is what a caller prints when the daemon is not there: "connect to <socket>" is
// the one piece of information that makes the failure actionable.
func (c *SandboxClient) Socket() string {
	if c == nil {
		return ""
	}
	return c.socket
}

// Close releases idle connections. The client stays usable.
func (c *SandboxClient) Close() {
	if c == nil {
		return
	}
	if c.http != nil {
		c.http.CloseIdleConnections()
	}
	if c.stream != nil {
		c.stream.CloseIdleConnections()
	}
}

// ListSandboxes returns every sandbox the node runs.
func (c *SandboxClient) ListSandboxes(ctx context.Context) ([]v1.Sandbox, error) {
	var doc v1.SandboxList
	if err := c.do(ctx, http.MethodGet, []string{"v1", "sandboxes"}, nil, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

// GetSandbox returns one sandbox.
func (c *SandboxClient) GetSandbox(ctx context.Context, id string) (v1.Sandbox, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.Sandbox{}, err
	}
	var doc v1.SandboxDocument
	if err := c.do(ctx, http.MethodGet, []string{"v1", "sandboxes", id}, nil, &doc); err != nil {
		return v1.Sandbox{}, err
	}
	return doc.Sandbox, nil
}

// CreateSandbox creates one sandbox and returns it once its guest agent has answered.
// The request's template is resolved by the node and its pin checked there, so a
// template this node cannot boot is refused with template_pin_mismatch.
func (c *SandboxClient) CreateSandbox(ctx context.Context, req v1.SandboxCreate) (v1.Sandbox, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return v1.Sandbox{}, output.NewErrorf(output.CodeInternal, "encode sandbox create request: %v", err)
	}
	var doc v1.SandboxDocument
	if err := c.do(ctx, http.MethodPost, []string{"v1", "sandboxes"}, body, &doc); err != nil {
		return v1.Sandbox{}, err
	}
	return doc.Sandbox, nil
}

// DeleteSandbox removes one sandbox and returns what removing it freed.
func (c *SandboxClient) DeleteSandbox(ctx context.Context, id string) (v1.SandboxRemoval, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.SandboxRemoval{}, err
	}
	var doc v1.SandboxRemoval
	if err := c.do(ctx, http.MethodDelete, []string{"v1", "sandboxes", id}, nil, &doc); err != nil {
		return v1.SandboxRemoval{}, err
	}
	return doc, nil
}

// CreateSandboxToken mints a token scoped to one sandbox. The secret is in the
// response and nowhere else.
func (c *SandboxClient) CreateSandboxToken(ctx context.Context, id string, req v1.SandboxTokenRequest) (v1.SandboxToken, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.SandboxToken{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return v1.SandboxToken{}, output.NewErrorf(output.CodeInternal, "encode sandbox token request: %v", err)
	}
	var doc v1.SandboxToken
	if err := c.do(ctx, http.MethodPost, []string{"v1", "sandboxes", id, "tokens"}, body, &doc); err != nil {
		return v1.SandboxToken{}, err
	}
	return doc, nil
}

// ExecSandbox runs one command in a sandbox and streams its output. It is the same
// exec contract, frame for frame, that ExecTask speaks to a task: a non-zero guest
// exit is a successful call, and only a stream without an exit frame is a failure.
func (c *SandboxClient) ExecSandbox(ctx context.Context, id string, req v1.ExecRequest, stdout, stderr io.Writer) (ExecResult, error) {
	if err := validateSegments([]string{id}); err != nil {
		return ExecResult{}, err
	}

	body, err := json.Marshal(req)
	if err != nil {
		return ExecResult{}, output.NewErrorf(output.CodeInternal, "encode exec request: %v", err)
	}

	req2, err := c.request(ctx, http.MethodPost, []string{"v1", "sandboxes", id, "exec"}, body)
	if err != nil {
		return ExecResult{}, err
	}
	// The stream client has no whole-response timeout: a command may outlive the
	// generic request timeout, and its own context bounds it.
	resp, err := c.stream.Do(req2)
	if err != nil {
		return ExecResult{}, c.connectError("POST /v1/sandboxes/"+id+"/exec", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return ExecResult{}, decodeErrorDocument(http.MethodPost,
			"/v1/sandboxes/"+id+"/exec", resp.StatusCode, resp.Status, errorBody)
	}
	return decodeExecStream(resp.Body, stdout, stderr)
}

// do runs one request that answers with a JSON document.
func (c *SandboxClient) do(ctx context.Context, method string, segments []string, body []byte, out any) error {
	req, err := c.request(ctx, method, segments, body)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return c.connectError(method+" /"+strings.Join(segments, "/"), err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return c.connectError(method+" /"+strings.Join(segments, "/"), err)
	}
	path := "/" + strings.Join(segments, "/")

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeErrorDocument(method, path, resp.StatusCode, resp.Status, payload)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return output.NewErrorf(output.CodeInternal, "%s %s: decode response: %v", method, path, err)
	}
	return nil
}

// request builds one HTTP request against the sandbox API, with the v1 media type and
// the bearer token when this client has one.
func (c *SandboxClient) request(ctx context.Context, method string, segments []string, body []byte) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, transportError(method+" /"+strings.Join(segments, "/"), err)
	}
	if err := validateSegments(segments); err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	u := c.base.JoinPath(segments...)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal, "build %s %s: %v", method, u.Path, err)
	}
	req.Header.Set("Accept", mediaTypeV1)
	if body != nil {
		req.Header.Set("Content-Type", mediaTypeV1)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// errSandboxSocketUnreachable is the failure a caller sees when the sandbox daemon is
// not running. It is not a wrapper: it names the socket, which is the one piece of
// information that makes the failure actionable.
func sandboxUnreachable(socket string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return output.NewErrorf(output.CodeTimeout, "connect to the sandbox daemon at %s: %v", socket, err)
	}
	return output.NewErrorf(output.CodeUnreachable,
		"connect to the sandbox daemon at %s: %v (is sandboxd running?)", socket, err)
}

// connectError maps a failed request on a client with no response. A local client
// names its socket, because "sandboxd is not running" is the failure a caller meets
// first and the one a bare "connection refused" does not explain.
func (c *SandboxClient) connectError(op string, err error) error {
	if c.socket != "" {
		return sandboxUnreachable(c.socket, err)
	}
	return transportError(op, err)
}
