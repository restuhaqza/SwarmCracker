package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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
)

// remoteClient reads the public HTTPS API. It carries no SwarmKit state: the
// server has already resolved IDs, names and node names into documents.
type remoteClient struct {
	baseURL *url.URL
	token   string
	http    *http.Client
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
	}}, nil
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

// get performs one GET and returns the body of a 2xx response. Every failure
// leaves as *output.Error so callers keep the stable codes and exit statuses.
func (r *remoteClient) get(ctx context.Context, segments []string, query url.Values) ([]byte, error) {
	// JoinPath joins raw path elements, so escape each one first: a slash in an
	// ID must stay inside that segment instead of creating a new one.
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		escaped[i] = url.PathEscape(segment)
	}
	u := r.baseURL.JoinPath(escaped...)
	u.RawQuery = query.Encode()
	path := u.EscapedPath()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal, "build request for %s: %v", path, err)
	}
	req.Header.Set("Accept", mediaTypeV1)
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return nil, output.NewErrorf(output.CodeUnreachable, "GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, output.NewErrorf(output.CodeUnreachable, "read response for %s: %v", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, decodeErrorDocument(path, resp.StatusCode, resp.Status, body)
	}
	return body, nil
}

// decodeErrorDocument turns a failure body into *output.Error. A typed server
// document is preserved field for field; anything else falls back to the
// closest code for the HTTP status.
func decodeErrorDocument(path string, statusCode int, status string, body []byte) error {
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
	return output.NewErrorf(codeForStatus(statusCode), "GET %s: server returned %s", path, status)
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

// paginate follows next cursors until null. Each page is decoded by decode,
// which returns the page's next cursor.
func (r *remoteClient) paginate(ctx context.Context, segments []string, decode func([]byte) (*string, error)) error {
	path := strings.Join(segments, "/")
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxListPages {
			return output.NewErrorf(output.CodeInternal, "list %s did not terminate after %d pages", path, maxListPages)
		}

		query := url.Values{}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		body, err := r.get(ctx, segments, query)
		if err != nil {
			return err
		}
		next, err := decode(body)
		if err != nil {
			return err
		}
		if next == nil || *next == "" {
			return nil
		}
		cursor = *next
	}
}

// listServices returns every page of services.
func (r *remoteClient) listServices(ctx context.Context) ([]v1.Service, error) {
	segments := []string{"v1", "services"}
	path := strings.Join(segments, "/")
	items := []v1.Service{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.ServiceList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listTasks returns every page of tasks.
func (r *remoteClient) listTasks(ctx context.Context) ([]v1.Task, error) {
	segments := []string{"v1", "tasks"}
	path := strings.Join(segments, "/")
	items := []v1.Task{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.TaskList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listServiceTasks returns every page of one service's tasks.
func (r *remoteClient) listServiceTasks(ctx context.Context, serviceID string) ([]v1.Task, error) {
	segments := []string{"v1", "services", serviceID, "tasks"}
	path := strings.Join(segments, "/")
	items := []v1.Task{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.TaskList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listNodes returns every page of nodes.
func (r *remoteClient) listNodes(ctx context.Context) ([]v1.Node, error) {
	segments := []string{"v1", "nodes"}
	path := strings.Join(segments, "/")
	items := []v1.Node{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.NodeList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listNetworks returns every page of user-defined networks. The server, not
// the client, hides built-in networks.
func (r *remoteClient) listNetworks(ctx context.Context) ([]v1.Network, error) {
	segments := []string{"v1", "networks"}
	path := strings.Join(segments, "/")
	items := []v1.Network{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.NetworkList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listConfigs returns every page of config metadata.
func (r *remoteClient) listConfigs(ctx context.Context) ([]v1.Config, error) {
	segments := []string{"v1", "configs"}
	path := strings.Join(segments, "/")
	items := []v1.Config{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.ConfigList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// listSecrets returns every page of secret metadata.
func (r *remoteClient) listSecrets(ctx context.Context) ([]v1.Secret, error) {
	segments := []string{"v1", "secrets"}
	path := strings.Join(segments, "/")
	items := []v1.Secret{}
	err := r.paginate(ctx, segments, func(body []byte) (*string, error) {
		var doc v1.SecretList
		if err := decodeJSON(path, body, &doc); err != nil {
			return nil, err
		}
		items = append(items, doc.Items...)
		return doc.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// getService returns one service document as served.
func (r *remoteClient) getService(ctx context.Context, id string) (v1.Service, error) {
	var doc v1.ServiceDocument
	if err := r.getDocument(ctx, []string{"v1", "services", id}, &doc); err != nil {
		return v1.Service{}, err
	}
	return doc.Service, nil
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

// clusterID reads the cluster identity from the version document.
func (r *remoteClient) clusterID(ctx context.Context) (string, error) {
	var doc v1.VersionInfo
	if err := r.getDocument(ctx, []string{"v1", "version"}, &doc); err != nil {
		return "", err
	}
	return doc.ClusterID, nil
}
