package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	// mediaTypeV1 is the versioned media type every request asks for.
	mediaTypeV1 = "application/vnd.swarmcracker.v1+json"
	// remoteDefaultTimeout bounds one HTTP request end to end.
	remoteDefaultTimeout = 30 * time.Second
	// maxListPages stops a server that never returns a null next cursor.
	maxListPages = 1000
	// maxResponseBytes bounds one decoded body so a misbehaving server cannot
	// exhaust client memory. A list page is orders of magnitude smaller.
	maxResponseBytes = 8 << 20
)

// remoteClient reads the public HTTPS API. It carries no SwarmKit state: the
// server has already resolved IDs, names and node names into documents.
type remoteClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
	// stream is the no-timeout client for SSE/NDJSON. http.Client.Timeout
	// bounds the whole response body, which would cut a watch stream.
	stream *http.Client
}

// newRemote builds the remote backend. Every failure here is a configuration
// mistake, so it is reported as a usage error.
func newRemote(cfg Config) (*Client, error) {
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

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = remoteDefaultTimeout
	}

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
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	return &Client{remote: &remoteClient{
		baseURL: base,
		token:   token,
		http:    &http.Client{Timeout: timeout, Transport: transport},
		stream:  &http.Client{Transport: transport},
	}}, nil
}

// close releases idle keep-alive connections. The client stays usable: a later
// request simply dials again.
func (r *remoteClient) close() {
	if r == nil || r.http == nil {
		return
	}
	r.http.CloseIdleConnections()
}

// parseEndpoint accepts only an https URL with a host. Plain HTTP over TCP is
// rejected rather than downgraded: the bearer token must never travel in the
// clear.
func parseEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, output.NewErrorf(output.CodeUsage, "invalid endpoint %q: %v", endpoint, err)
	}
	if u.Scheme == "http" {
		return nil, output.NewErrorf(output.CodeUsage, "endpoint %q is plaintext HTTP: remote TCP must use TLS (https)", endpoint)
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, output.NewErrorf(output.CodeUsage, "invalid endpoint %q: want an https://host[:port] URL", endpoint)
	}
	return u, nil
}

// loadCA builds the trust store for the endpoint. There is deliberately no
// unverified fallback.
func loadCA(path, endpoint string) (*x509.CertPool, error) {
	if path == "" {
		return nil, output.NewErrorf(output.CodeUsage, "endpoint %q requires CAFile: remote TLS must be verified", endpoint)
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, output.NewErrorf(output.CodeUsage, "read CA file %q: %v", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, output.NewErrorf(output.CodeUsage, "parse CA file %q: no certificates found", path)
	}
	return pool, nil
}

// loadToken resolves the bearer token. An explicit Token wins over TokenFile.
func loadToken(cfg Config) (string, error) {
	if cfg.Token != "" {
		return cfg.Token, nil
	}
	if cfg.TokenFile == "" {
		return "", nil
	}
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return "", output.NewErrorf(output.CodeUsage, "read token file %q: %v", cfg.TokenFile, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// validateSegments rejects path segments that URL joining would silently
// collapse. An empty or dot-segment ID would otherwise turn a single-resource
// read into a list, parent or version read that still decodes cleanly.
func validateSegments(segments []string) error {
	for _, segment := range segments {
		switch segment {
		case "", ".", "..":
			return output.NewErrorf(output.CodeUsage, "invalid resource id %q: must not be empty or a dot segment", segment)
		}
	}
	return nil
}

// transportError maps a failed request or body read. A cancelled or expired
// context is the caller's deadline, not an unreachable server.
func transportError(op string, err error) *output.Error {
	code := output.CodeUnreachable
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = output.CodeTimeout
	}
	return output.NewErrorf(code, "%s: %v", op, err)
}

// remoteRequest is one call to the manager's API. Building it here keeps the
// method, content type and preconditions in one place for reads and writes.
type remoteRequest struct {
	method      string
	segments    []string
	query       url.Values
	body        []byte
	contentType string
	headers     map[string]string
}

// remoteResponse is the status, headers and body of one successful call.
type remoteResponse struct {
	status int
	header http.Header
	body   []byte
}

// do performs one request and returns the body of a 2xx response. Every failure
// leaves as *output.Error so callers keep the stable codes and exit statuses.
func (r *remoteClient) do(ctx context.Context, call remoteRequest) (*remoteResponse, error) {
	if err := validateSegments(call.segments); err != nil {
		return nil, err
	}
	// JoinPath joins raw path elements, so escape each one first: a slash in an
	// ID must stay inside that segment instead of creating a new one.
	escaped := make([]string, len(call.segments))
	for i, segment := range call.segments {
		escaped[i] = url.PathEscape(segment)
	}
	u := r.baseURL.JoinPath(escaped...)
	if call.query != nil {
		u.RawQuery = call.query.Encode()
	}
	path := u.EscapedPath()

	var body io.Reader
	if call.body != nil {
		body = bytes.NewReader(call.body)
	}

	req, err := http.NewRequestWithContext(ctx, call.method, u.String(), body)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal, "build request for %s: %v", path, err)
	}
	req.Header.Set("Accept", mediaTypeV1)
	if call.contentType != "" {
		req.Header.Set("Content-Type", call.contentType)
	}
	for name, value := range call.headers {
		req.Header.Set(name, value)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, transportError(call.method+" "+path, err)
	}
	defer resp.Body.Close()

	// Read one byte past the bound so an oversized body is detected rather than
	// silently truncated into a decode failure.
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, transportError("read response for "+path, err)
	}
	if len(responseBody) > maxResponseBytes {
		return nil, output.NewErrorf(output.CodeInternal,
			"%s %s: response exceeds %d bytes", call.method, path, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeErrorDocument(call.method, path, resp.StatusCode, resp.Status, responseBody)
	}

	return &remoteResponse{status: resp.StatusCode, header: resp.Header, body: responseBody}, nil
}

// get performs one GET and returns its body.
func (r *remoteClient) get(ctx context.Context, segments []string, query url.Values) ([]byte, error) {
	resp, err := r.do(ctx, remoteRequest{
		method:   http.MethodGet,
		segments: segments,
		query:    query,
	})
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

// decodeErrorDocument turns a failure body into *output.Error. A typed server
// document is preserved field for field; anything else falls back to the
// closest code for the HTTP status.
func decodeErrorDocument(method, path string, statusCode int, status string, body []byte) error {
	var doc v1.ErrorDocument
	if err := json.Unmarshal(body, &doc); err == nil && doc.Kind == v1.KindError && doc.Error.Code != "" {
		e := output.NewError(output.Code(doc.Error.Code), doc.Error.Message)
		if e.Message == "" {
			e.Message = status
		}
		e.RequestID = doc.Error.RequestID
		e.Retryable = doc.Error.Retryable
		e.Details = doc.Error.Details
		return e
	}
	return output.NewErrorf(codeForStatus(statusCode), "%s %s: server returned %s", method, path, status)
}

// codeForStatus maps a bare HTTP status onto the contract's codes for servers
// that do not answer with an ErrorDocument.
func codeForStatus(status int) output.Code {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
		return output.CodeInvalid
	case http.StatusUnauthorized:
		return output.CodeUnauthenticated
	case http.StatusForbidden:
		return output.CodeForbidden
	case http.StatusNotFound:
		return output.CodeNotFound
	case http.StatusConflict, http.StatusPreconditionFailed:
		return output.CodeConflict
	case http.StatusGone:
		return output.CodeCursorExpired
	case http.StatusTooManyRequests:
		return output.CodeRateLimited
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return output.CodeNotImplemented
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return output.CodeTimeout
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return output.CodeUnavailable
	default:
		return output.CodeInternal
	}
}

// decodeJSON decodes one document. A malformed body is a server bug, not a
// transport failure, so it is internal.
func decodeJSON(path string, body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return output.NewErrorf(output.CodeInternal, "decode %s response: %v", path, err)
	}
	return nil
}

// getDocument fetches and decodes a single-resource document.
func (r *remoteClient) getDocument(ctx context.Context, segments []string, out any) error {
	body, err := r.get(ctx, segments, nil)
	if err != nil {
		return err
	}
	return decodeJSON(strings.Join(segments, "/"), body, out)
}

// paginate follows next cursors until one is absent, accumulating every page's
// items. extract reads the items and next cursor out of the concrete document
// type, so a list method only names its document. A cursor that repeats would
// otherwise loop until maxListPages; it is reported as a server bug instead.
func paginate[T any, D any](r *remoteClient, ctx context.Context, segments []string, extract func(*D) ([]T, *string)) ([]T, error) {
	path := strings.Join(segments, "/")
	items := []T{}
	cursor := ""
	seen := make(map[string]struct{})

	for page := 0; ; page++ {
		if page >= maxListPages {
			return nil, output.NewErrorf(output.CodeInternal, "list %s did not terminate after %d pages", path, maxListPages)
		}

		query := url.Values{}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := r.get(ctx, segments, query)
		if err != nil {
			return nil, err
		}

		var doc D
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		pageItems, next := extract(&doc)
		items = append(items, pageItems...)

		if next == nil || *next == "" {
			return items, nil
		}
		if _, repeated := seen[*next]; repeated {
			return nil, output.NewErrorf(output.CodeInternal, "list %s repeated cursor %q", path, *next)
		}
		seen[*next] = struct{}{}
		cursor = *next
	}
}

// listServices returns every page of services.
func (r *remoteClient) listServices(ctx context.Context) ([]v1.Service, error) {
	return paginate(r, ctx, []string{"v1", "services"}, func(doc *v1.ServiceList) ([]v1.Service, *string) {
		return doc.Items, doc.Next
	})
}

// listTasks returns every page of tasks.
func (r *remoteClient) listTasks(ctx context.Context) ([]v1.Task, error) {
	return paginate(r, ctx, []string{"v1", "tasks"}, func(doc *v1.TaskList) ([]v1.Task, *string) {
		return doc.Items, doc.Next
	})
}

// listTasksHealth returns every page of tasks with ?health=true, accumulating
// the items, warnings and partials of each page. A 207 page is a normal
// TaskList document, not an error, so `do` already returns it; this method only
// records the partial flag. `next` stays nil because the pages are consumed
// here, exactly like the unenriched list.
//
// Partials are folded by node: the manager emits one per page per failed node,
// so a node that owns tasks on two pages arrives twice. The aggregated document
// names it once.
func (r *remoteClient) listTasksHealth(ctx context.Context) (v1.TaskList, bool, error) {
	doc := v1.NewTaskList(nil)
	partial := false
	cursor := ""
	seen := make(map[string]struct{})
	partialIndex := make(map[string]int)

	for page := 0; ; page++ {
		if page >= maxListPages {
			return v1.TaskList{}, false, output.NewErrorf(output.CodeInternal,
				"list v1/tasks did not terminate after %d pages", maxListPages)
		}

		query := url.Values{}
		query.Set("health", "true")
		if cursor != "" {
			query.Set("cursor", cursor)
		}

		resp, err := r.do(ctx, remoteRequest{
			method:   http.MethodGet,
			segments: []string{"v1", "tasks"},
			query:    query,
		})
		if err != nil {
			return v1.TaskList{}, false, err
		}

		var pageDoc v1.TaskList
		if err := decodeJSON("v1/tasks", resp.body, &pageDoc); err != nil {
			return v1.TaskList{}, false, err
		}
		if resp.status == http.StatusMultiStatus {
			partial = true
		}

		doc.Items = append(doc.Items, pageDoc.Items...)
		for _, entry := range pageDoc.Partial {
			mergePartial(&doc.Partial, partialIndex, entry)
		}
		doc.Warnings = append(doc.Warnings, pageDoc.Warnings...)

		if pageDoc.Next == nil || *pageDoc.Next == "" {
			return doc, partial, nil
		}
		if _, repeated := seen[*pageDoc.Next]; repeated {
			return v1.TaskList{}, false, output.NewErrorf(output.CodeInternal,
				"list v1/tasks repeated cursor %q", *pageDoc.Next)
		}
		seen[*pageDoc.Next] = struct{}{}
		cursor = *pageDoc.Next
	}
}

// mergePartial appends one page's partial, or folds it into the entry already
// recorded for that node: the manager emits one partial per page per failed
// node, so a node hosting tasks on several pages would otherwise repeat. The
// first entry wins; a later page may carry the node name the first one lacked,
// which is backfilled so the single entry stays usable — the same rule
// pkg/apihttp/health.go applies inside one page. A partial without a node ID
// names nothing, so it cannot be folded and is always kept.
func mergePartial(dst *[]v1.Partial, index map[string]int, entry v1.Partial) {
	if entry.NodeID != "" {
		if at, dup := index[entry.NodeID]; dup {
			if (*dst)[at].NodeName == "" {
				(*dst)[at].NodeName = entry.NodeName
			}
			return
		}
		index[entry.NodeID] = len(*dst)
	}
	*dst = append(*dst, entry)
}

// listServiceTasks returns every page of one service's tasks.
func (r *remoteClient) listServiceTasks(ctx context.Context, serviceID string) ([]v1.Task, error) {
	return paginate(r, ctx, []string{"v1", "services", serviceID, "tasks"}, func(doc *v1.TaskList) ([]v1.Task, *string) {
		return doc.Items, doc.Next
	})
}

// listNodes returns every page of nodes.
func (r *remoteClient) listNodes(ctx context.Context) ([]v1.Node, error) {
	return paginate(r, ctx, []string{"v1", "nodes"}, func(doc *v1.NodeList) ([]v1.Node, *string) {
		return doc.Items, doc.Next
	})
}

// listNetworks returns every page of user-defined networks. The server, not
// the client, hides built-in networks.
func (r *remoteClient) listNetworks(ctx context.Context) ([]v1.Network, error) {
	return paginate(r, ctx, []string{"v1", "networks"}, func(doc *v1.NetworkList) ([]v1.Network, *string) {
		return doc.Items, doc.Next
	})
}

// listConfigs returns every page of config metadata.
func (r *remoteClient) listConfigs(ctx context.Context) ([]v1.Config, error) {
	return paginate(r, ctx, []string{"v1", "configs"}, func(doc *v1.ConfigList) ([]v1.Config, *string) {
		return doc.Items, doc.Next
	})
}

// listSecrets returns every page of secret metadata.
func (r *remoteClient) listSecrets(ctx context.Context) ([]v1.Secret, error) {
	return paginate(r, ctx, []string{"v1", "secrets"}, func(doc *v1.SecretList) ([]v1.Secret, *string) {
		return doc.Items, doc.Next
	})
}

// getService returns one service document as served.
func (r *remoteClient) getService(ctx context.Context, id string) (v1.Service, error) {
	return r.getServiceDocument(ctx, id)
}

// getTask returns one task document as served. The server resolves nodeName,
// so no second lookup happens here.
func (r *remoteClient) getTask(ctx context.Context, id string) (v1.Task, error) {
	var doc v1.TaskDocument
	if err := r.getDocument(ctx, []string{"v1", "tasks", id}, &doc); err != nil {
		return v1.Task{}, err
	}
	return doc.Task, nil
}

// getNode returns one node document as served.
func (r *remoteClient) getNode(ctx context.Context, id string) (v1.Node, error) {
	var doc v1.NodeDocument
	if err := r.getDocument(ctx, []string{"v1", "nodes", id}, &doc); err != nil {
		return v1.Node{}, err
	}
	return doc.Node, nil
}

// getNetwork returns one network document as served.
func (r *remoteClient) getNetwork(ctx context.Context, id string) (v1.Network, error) {
	var doc v1.NetworkDocument
	if err := r.getDocument(ctx, []string{"v1", "networks", id}, &doc); err != nil {
		return v1.Network{}, err
	}
	return doc.Network, nil
}

// getConfig returns one config document as served.
func (r *remoteClient) getConfig(ctx context.Context, id string) (v1.Config, error) {
	var doc v1.ConfigDocument
	if err := r.getDocument(ctx, []string{"v1", "configs", id}, &doc); err != nil {
		return v1.Config{}, err
	}
	return doc.Config, nil
}

// getSecret returns one secret document as served.
func (r *remoteClient) getSecret(ctx context.Context, id string) (v1.Secret, error) {
	var doc v1.SecretDocument
	if err := r.getDocument(ctx, []string{"v1", "secrets", id}, &doc); err != nil {
		return v1.Secret{}, err
	}
	return doc.Secret, nil
}

// clusterID reads the cluster identity from the version document. An empty
// clusterId is NotFound, matching the local backend's empty cluster list.
func (r *remoteClient) clusterID(ctx context.Context) (string, error) {
	var doc v1.VersionInfo
	if err := r.getDocument(ctx, []string{"v1", "version"}, &doc); err != nil {
		return "", err
	}
	if doc.ClusterID == "" {
		return "", output.NewError(output.CodeNotFound, "cluster not found")
	}
	return doc.ClusterID, nil
}

// createService sends POST /v1/services. The server resolves and validates the
// body, so this path must not duplicate the local mapping.
func (r *remoteClient) createService(ctx context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	if err := checkCreateOnly(opts); err != nil {
		return v1.Service{}, err
	}

	body, err := json.Marshal(write)
	if err != nil {
		return v1.Service{}, output.NewErrorf(output.CodeInternal, "encode service: %v", err)
	}

	headers := make(map[string]string)
	if opts.IfNoneMatch != "" {
		headers["If-None-Match"] = opts.IfNoneMatch
	}

	resp, err := r.do(ctx, remoteRequest{
		method:      http.MethodPost,
		segments:    []string{"v1", "services"},
		query:       writeQuery(opts),
		body:        body,
		contentType: mediaTypeV1,
		headers:     headers,
	})
	if err != nil {
		return v1.Service{}, err
	}

	var doc v1.ServiceDocument
	if err := decodeJSON("v1/services", resp.body, &doc); err != nil {
		return v1.Service{}, err
	}
	return doc.Service, nil
}

// updateService sends PATCH /v1/services/{ref} with the raw merge patch: the
// server owns parsing, validation and the SwarmKit overlay.
func (r *remoteClient) updateService(ctx context.Context, ref string, patch []byte, opts v1.WriteOptions) (v1.Service, error) {
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Service{}, err
	}

	resp, err := r.do(ctx, remoteRequest{
		method:      http.MethodPatch,
		segments:    []string{"v1", "services", ref},
		query:       writeQuery(opts),
		body:        patch,
		contentType: v1.MergePatchMediaType,
		headers:     ifMatchHeader(opts.IfMatch),
	})
	if err != nil {
		return v1.Service{}, err
	}

	var doc v1.ServiceDocument
	if err := decodeJSON("v1/services/"+ref, resp.body, &doc); err != nil {
		return v1.Service{}, err
	}
	return doc.Service, nil
}

// deleteService GETs the service first so it can return the document as it was
// before the removal, then sends DELETE /v1/services/{ref}.
//
// The GET is not a precondition check: If-Match is forwarded and evaluated by
// the server, which is the only side that can see the current revision. A
// missing service still goes on to DELETE when If-Match or ignoreMissing was
// given, so the server decides between 412, 404 and 204.
func (r *remoteClient) deleteService(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		// The wire contract answers 400 for this; refusing here means no request
		// leaves the process at all.
		return v1.Service{}, dryRunUnsupportedOnDelete()
	}

	previous, getErr := r.getServiceDocument(ctx, ref)
	if getErr != nil && !isNotFound(getErr) {
		return v1.Service{}, getErr
	}
	if getErr != nil && opts.IfMatch == "" && !opts.IgnoreMissing {
		return v1.Service{}, getErr
	}

	query := url.Values{}
	if opts.IgnoreMissing {
		query.Set("ignoreMissing", "true")
	}

	if _, err := r.do(ctx, remoteRequest{
		method:   http.MethodDelete,
		segments: []string{"v1", "services", ref},
		query:    query,
		headers:  ifMatchHeader(opts.IfMatch),
	}); err != nil {
		return v1.Service{}, err
	}

	return previous, nil
}

// rollbackService sends POST /v1/services/{ref}/rollback with no body.
func (r *remoteClient) rollbackService(ctx context.Context, ref string, opts v1.WriteOptions) (v1.Service, error) {
	if opts.DryRun {
		return v1.Service{}, dryRunUnsupportedOnRollback()
	}
	if err := requireIfMatch(opts.IfMatch); err != nil {
		return v1.Service{}, err
	}

	resp, err := r.do(ctx, remoteRequest{
		method:   http.MethodPost,
		segments: []string{"v1", "services", ref, "rollback"},
		headers:  ifMatchHeader(opts.IfMatch),
	})
	if err != nil {
		return v1.Service{}, err
	}

	var doc v1.ServiceDocument
	if err := decodeJSON("v1/services/"+ref+"/rollback", resp.body, &doc); err != nil {
		return v1.Service{}, err
	}
	return doc.Service, nil
}

// getServiceDocument fetches one service document.
func (r *remoteClient) getServiceDocument(ctx context.Context, ref string) (v1.Service, error) {
	var doc v1.ServiceDocument
	if err := r.getDocument(ctx, []string{"v1", "services", ref}, &doc); err != nil {
		return v1.Service{}, err
	}
	return doc.Service, nil
}

// ifMatchHeader renders the optional If-Match header.
func ifMatchHeader(ifMatch string) map[string]string {
	if strings.TrimSpace(ifMatch) == "" {
		return nil
	}
	return map[string]string{"If-Match": ifMatch}
}
