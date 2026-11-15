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
	"fmt"
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

	// octetStreamMediaType (attach.go) and tarMediaType are the two shapes a sandbox
	// file takes, in both directions: one file's bytes, and a tar archive of a tree.
	// The node chooses the operation of a write from exactly this header.
	tarMediaType = "application/x-tar"
	// sandboxFileKindHeader is where a read says which of the two it answered with,
	// so a caller that stores the body knows what it stored without inferring it from
	// a media type.
	sandboxFileKindHeader = "X-Swarmcracker-File-Kind"
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

// ListCheckpoints returns every checkpoint stored on the node, whichever sandbox it
// came from: a checkpoint outlives its sandbox, so the node-scoped listing is the one
// that can still find it afterwards.
func (c *SandboxClient) ListCheckpoints(ctx context.Context) ([]v1.Checkpoint, error) {
	var doc v1.CheckpointList
	if err := c.do(ctx, http.MethodGet, []string{"v1", "checkpoints"}, nil, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

// ListSandboxCheckpoints returns the checkpoints taken from one sandbox.
func (c *SandboxClient) ListSandboxCheckpoints(ctx context.Context, id string) ([]v1.Checkpoint, error) {
	if err := validateSegments([]string{id}); err != nil {
		return nil, err
	}
	var doc v1.CheckpointList
	if err := c.do(ctx, http.MethodGet, []string{"v1", "sandboxes", id, "checkpoints"}, nil, &doc); err != nil {
		return nil, err
	}
	return doc.Items, nil
}

// GetCheckpoint returns one checkpoint by name.
func (c *SandboxClient) GetCheckpoint(ctx context.Context, name string) (v1.Checkpoint, error) {
	if err := validateSegments([]string{name}); err != nil {
		return v1.Checkpoint{}, err
	}
	var doc v1.CheckpointDocument
	if err := c.do(ctx, http.MethodGet, []string{"v1", "checkpoints", name}, nil, &doc); err != nil {
		return v1.Checkpoint{}, err
	}
	return doc.Checkpoint, nil
}

// CreateCheckpoint takes a checkpoint of one sandbox under a name the caller chooses.
// The name is required because a checkpoint is something the caller forks later, and a
// minted one would only be reachable through a listing.
func (c *SandboxClient) CreateCheckpoint(ctx context.Context, id, name string) (v1.Checkpoint, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.Checkpoint{}, err
	}
	body, err := json.Marshal(v1.CheckpointCreate{Name: name})
	if err != nil {
		return v1.Checkpoint{}, output.NewErrorf(output.CodeInternal, "encode checkpoint request: %v", err)
	}
	var doc v1.CheckpointDocument
	if err := c.do(ctx, http.MethodPost, []string{"v1", "sandboxes", id, "checkpoints"}, body, &doc); err != nil {
		return v1.Checkpoint{}, err
	}
	return doc.Checkpoint, nil
}

// DeleteCheckpoint forgets one checkpoint and reports what that actually freed, and what
// the remaining checkpoints still reference.
func (c *SandboxClient) DeleteCheckpoint(ctx context.Context, name string) (v1.CheckpointRemoval, error) {
	if err := validateSegments([]string{name}); err != nil {
		return v1.CheckpointRemoval{}, err
	}
	var doc v1.CheckpointRemoval
	if err := c.do(ctx, http.MethodDelete, []string{"v1", "checkpoints", name}, nil, &doc); err != nil {
		return v1.CheckpointRemoval{}, err
	}
	return doc, nil
}

// ForkSandbox clones what a sandbox is running now. The node takes a fresh checkpoint to
// do it and answers with the clones and the checkpoint it left behind.
func (c *SandboxClient) ForkSandbox(ctx context.Context, id string, req v1.SandboxFork) (v1.SandboxList, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.SandboxList{}, err
	}
	return c.fork(ctx, []string{"v1", "sandboxes", id, "fork"}, req)
}

// ForkCheckpoint clones a stored checkpoint by name.
func (c *SandboxClient) ForkCheckpoint(ctx context.Context, name string, req v1.SandboxFork) (v1.SandboxList, error) {
	if err := validateSegments([]string{name}); err != nil {
		return v1.SandboxList{}, err
	}
	return c.fork(ctx, []string{"v1", "checkpoints", name, "fork"}, req)
}

// RestoreCheckpoint is one clone of a checkpoint, on the sandbox-scoped route.
func (c *SandboxClient) RestoreCheckpoint(ctx context.Context, id, name string, req v1.SandboxFork) (v1.SandboxList, error) {
	if err := validateSegments([]string{id, name}); err != nil {
		return v1.SandboxList{}, err
	}
	return c.fork(ctx, []string{"v1", "sandboxes", id, "checkpoints", name, "restore"}, req)
}

// fork posts a fork body and returns the document, which carries the clones and, on the
// routes that mint one, the checkpoint they came from.
func (c *SandboxClient) fork(ctx context.Context, segments []string, req v1.SandboxFork) (v1.SandboxList, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return v1.SandboxList{}, output.NewErrorf(output.CodeInternal, "encode fork request: %v", err)
	}
	var doc v1.SandboxList
	if err := c.do(ctx, http.MethodPost, segments, body, &doc); err != nil {
		return v1.SandboxList{}, err
	}
	return doc, nil
}

// SandboxFileKind is the shape a guest path was served in, as the node reported it: the
// value of the response's X-Swarmcracker-File-Kind header, which is also what its media
// type says.
type SandboxFileKind string

const (
	// SandboxFileKindFile is a regular file: the body is that file's bytes.
	SandboxFileKindFile SandboxFileKind = "file"
	// SandboxFileKindDir is a directory: the body is a tar archive of it, whose names are
	// relative to the guest's file root.
	SandboxFileKindDir SandboxFileKind = "dir"
)

// SandboxFileQuery is how one file request names its path, and the mode a write gives a
// file. It is a struct rather than two parameters so a caller cannot pass them in the
// wrong order.
type SandboxFileQuery struct {
	// Path is the guest path: relative to the guest's workspace root, which the guest
	// agent confines every file operation to.
	Path string
	// Mode is the permissions of a written file, and is ignored for a read and for an
	// archive. Zero means the node's default (0644).
	Mode uint32
}

// GetSandboxFile opens one guest path in a sandbox for reading, and reports the shape the
// node served it in: a regular file's bytes, or a tar archive of a directory. The caller
// closes the reader.
//
// The shape is known before any bytes are read, which is what lets a caller choose where
// they go: a file's bytes and a tree's archive want different destinations, and a stream has
// only one direction. It is a streaming call, so a tree of any size costs a chunk of memory
// rather than its own size, and the caller's context bounds it rather than the generic
// request timeout.
func (c *SandboxClient) GetSandboxFile(ctx context.Context, id string, query SandboxFileQuery) (SandboxFileKind, io.ReadCloser, error) {
	if err := validateSegments([]string{id}); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(query.Path) == "" {
		return "", nil, output.NewError(output.CodeInvalid,
			"a guest path is required: name the file or directory to copy")
	}

	req, err := c.request(ctx, http.MethodGet, []string{"v1", "sandboxes", id, "files"}, nil)
	if err != nil {
		return "", nil, err
	}
	req.URL.RawQuery = url.Values{"path": {query.Path}}.Encode()
	// A file read is not a document: the bytes are the answer, in whichever of the two
	// shapes the guest reported.
	req.Header.Set("Accept", octetStreamMediaType+", "+tarMediaType)

	resp, err := c.stream.Do(req)
	if err != nil {
		return "", nil, c.connectError("GET /v1/sandboxes/"+id+"/files", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return "", nil, decodeErrorDocument(http.MethodGet,
			"/v1/sandboxes/"+id+"/files", resp.StatusCode, resp.Status, errorBody)
	}

	return SandboxFileKind(resp.Header.Get(sandboxFileKindHeader)), resp.Body, nil
}

// PutSandboxFile writes one regular file into a sandbox from r, and returns what the guest
// wrote. The mode is the guest file's permissions; zero means the node's default.
func (c *SandboxClient) PutSandboxFile(ctx context.Context, id string, query SandboxFileQuery, r io.Reader) (v1.SandboxFileWrite, error) {
	return c.putSandboxFile(ctx, id, query, r, octetStreamMediaType)
}

// PutSandboxTree streams a tar archive into a sandbox, and returns what the guest wrote.
//
// The archive's names are relative to the guest's file root — which is what GetSandboxFile
// produces for a directory — and every entry must be the named directory or something under
// it. A template whose agent predates the operation answers 501 with details.capability
// "untar"; a single file still arrives through PutSandboxFile.
func (c *SandboxClient) PutSandboxTree(ctx context.Context, id string, query SandboxFileQuery, r io.Reader) (v1.SandboxFileWrite, error) {
	return c.putSandboxFile(ctx, id, query, r, tarMediaType)
}

// putSandboxFile is the write both shapes take: the media type chooses which operation the
// node runs, so it is the one thing that differs.
func (c *SandboxClient) putSandboxFile(ctx context.Context, id string, query SandboxFileQuery, r io.Reader, contentType string) (v1.SandboxFileWrite, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.SandboxFileWrite{}, err
	}
	if strings.TrimSpace(query.Path) == "" {
		return v1.SandboxFileWrite{}, output.NewError(output.CodeInvalid,
			"a guest path is required: name the file or directory to write")
	}

	req, err := c.request(ctx, http.MethodPut, []string{"v1", "sandboxes", id, "files"}, nil)
	if err != nil {
		return v1.SandboxFileWrite{}, err
	}
	values := url.Values{"path": {query.Path}}
	if query.Mode != 0 {
		values.Set("mode", fmt.Sprintf("%04o", query.Mode))
	}
	req.URL.RawQuery = values.Encode()
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", mediaTypeV1)
	if r != nil {
		req.Body = io.NopCloser(r)
		// The length is not known here: a caller streams a workspace through a pipe
		// as often as it hands over a file, and the node streams the body either way.
		req.ContentLength = -1
	}

	resp, err := c.stream.Do(req)
	if err != nil {
		return v1.SandboxFileWrite{}, c.connectError("PUT /v1/sandboxes/"+id+"/files", err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return v1.SandboxFileWrite{}, c.connectError("PUT /v1/sandboxes/"+id+"/files", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return v1.SandboxFileWrite{}, decodeErrorDocument(http.MethodPut,
			"/v1/sandboxes/"+id+"/files", resp.StatusCode, resp.Status, payload)
	}

	var doc v1.SandboxFileWrite
	if err := json.Unmarshal(payload, &doc); err != nil {
		return v1.SandboxFileWrite{}, output.NewErrorf(output.CodeInternal,
			"PUT /v1/sandboxes/%s/files: decode response: %v", id, err)
	}
	return doc, nil
}

// SandboxStatus reads a sandbox and what its guest says about itself: its identity, kernel,
// resources and toolchain, and its own view of its network. Both come from the guest agent's
// own operations rather than from parsing a command's output, so a template needs no shell
// for it.
func (c *SandboxClient) SandboxStatus(ctx context.Context, id string) (v1.SandboxStatus, error) {
	if err := validateSegments([]string{id}); err != nil {
		return v1.SandboxStatus{}, err
	}
	var doc v1.SandboxStatus
	if err := c.do(ctx, http.MethodGet, []string{"v1", "sandboxes", id, "status"}, nil, &doc); err != nil {
		return v1.SandboxStatus{}, err
	}
	return doc, nil
}

// SandboxLogs streams a sandbox's serial console into w, as the node recorded it.
//
// The console is the VMM's record of the guest's serial port, not the guest agent's: it
// starts before the agent is up and survives a boot that failed, so a sandbox that is not
// ready still has one.
func (c *SandboxClient) SandboxLogs(ctx context.Context, id string, w io.Writer) error {
	if err := validateSegments([]string{id}); err != nil {
		return err
	}

	req, err := c.request(ctx, http.MethodGet, []string{"v1", "sandboxes", id, "logs"}, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/plain")

	resp, err := c.stream.Do(req)
	if err != nil {
		return c.connectError("GET /v1/sandboxes/"+id+"/logs", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errorBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return decodeErrorDocument(http.MethodGet,
			"/v1/sandboxes/"+id+"/logs", resp.StatusCode, resp.Status, errorBody)
	}
	if w == nil {
		return nil
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return output.NewErrorf(output.CodeUnavailable,
			"read the body of /v1/sandboxes/%s/logs: %v", id, err)
	}
	return nil
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
