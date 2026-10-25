package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The wire constants the runner asserts. They are duplicated from pkg/apihttp
// on purpose: the fixtures describe the public contract, not a Go package, so an
// accidental rename inside the server must fail this suite.
const (
	// MediaType is the only body media type API v1 serves or accepts.
	MediaType = "application/vnd.swarmcracker.v1+json"
	// APIVersion is the value of X-SwarmCracker-Api-Version on every response.
	APIVersion = "1"

	apiVersionHeader = "X-SwarmCracker-Api-Version"
	requestIDHeader  = "X-Request-Id"

	eventStreamMediaType = "text/event-stream"
	ndjsonMediaType      = "application/x-ndjson"

	readyFrame = "ready"
	pingFrame  = "ping"

	// streamDeadline bounds reading one SSE/NDJSON response. A follow stream
	// stays open until the caller disconnects, so the runner stops reading
	// instead of waiting for the server to end it.
	streamDeadline = 2 * time.Second
	// failureBodyLimit is how much of a failing response body is reported.
	failureBodyLimit = 2 << 10
)

// Env is one target environment: the loopback TLS listener over the real
// handler, or a live manager.
type Env struct {
	BaseURL string
	Client  *http.Client
	// Tokens maps a fixture role (reader, runner, admin) to its bearer secret.
	// A missing role skips only the cases that need it.
	Tokens map[string]string
	// Lab marks a run against a real manager: mock-only cases are skipped and a
	// mutation needs AllowMutation.
	Lab bool
	// AllowMutation permits cases marked mutating. It is set from
	// CONFORMANCE_ALLOW_MUTATION in the lab test and is always true in the mock.
	AllowMutation bool
}

// Run loads every *.json fixture in dir and executes all cases as subtests
// named "<file>/<case>". A malformed or unknown-version fixture is fatal: a
// suite that silently runs fewer cases is worse than one that fails.
func Run(t *testing.T, env Env, dir string) {
	t.Helper()

	fixtures, err := Load(dir)
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}

	for _, fixture := range fixtures {
		t.Run(fixtureSlug(fixture.Source), func(t *testing.T) {
			// vars is the file's sequential scope: a later case can use the
			// ETag an earlier one returned ($etag). Subtests run in order, so a
			// plain map is enough and needs no lock.
			vars := make(map[string]string)
			for _, c := range fixture.Cases {
				t.Run(c.Name, func(t *testing.T) {
					runCase(t, env, c, vars)
				})
			}
		})
	}
}

// fixtureSlug is the subtest name a fixture file contributes.
func fixtureSlug(source string) string {
	return strings.TrimSuffix(filepath.Base(source), ".json")
}

// runCase executes one case: skip policy, token lookup, request, assertions and
// the optional cursor follow.
func runCase(t *testing.T, env Env, c Case, vars map[string]string) {
	t.Helper()

	switch {
	case env.Lab && c.MockOnly:
		t.Skip("mock-only case: it asserts fixture data a live cluster need not contain")
	case c.Mutating && !env.AllowMutation:
		t.Skip("mutating case: set CONFORMANCE_ALLOW_MUTATION=1 to run it")
	}

	token := strings.TrimSpace(env.Tokens[c.Request.Role])
	if token == "" {
		t.Skipf("no %s token configured for this environment; skipping", c.Request.Role)
	}

	client := env.Client
	if client == nil {
		client = http.DefaultClient
	}

	ctx := t.Context()
	if c.Stream != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, streamDeadline)
		defer cancel()
	}

	req, err := newRequest(ctx, env, c.Request, token, vars)
	if err != nil {
		t.Fatalf("%s: build request: %v", c.Name, err)
	}

	res := send(t, client, req, c.Stream != nil)
	assertCase(t, c, res)

	if c.Follow != nil {
		runFollow(t, env, client, c, token, vars, res)
	}
	if res.etag != "" {
		vars["etag"] = res.etag
	}
}

// newRequest builds one authenticated request. Only the fixture's own path,
// query and headers can reach the message a failure prints; the token never
// leaves the Authorization header.
func newRequest(ctx context.Context, env Env, spec Request, token string, vars map[string]string) (*http.Request, error) {
	path, err := resolveVars(spec.Path, vars)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(strings.TrimRight(env.BaseURL, "/") + path)
	if err != nil {
		return nil, fmt.Errorf("invalid base URL and path: %w", err)
	}
	if len(spec.Query) > 0 {
		query := u.Query()
		for key, value := range spec.Query {
			resolved, resolveErr := resolveVars(value, vars)
			if resolveErr != nil {
				return nil, fmt.Errorf("query %s: %w", key, resolveErr)
			}
			query.Set(key, resolved)
		}
		u.RawQuery = query.Encode()
	}

	body, err := requestBody(spec.Body)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, u.String(), reader)
	if err != nil {
		return nil, err
	}

	accept := spec.Accept
	if accept == "" {
		accept = MediaType
	}
	req.Header.Set("Accept", accept)

	if body != nil || spec.ContentType != "" {
		contentType := spec.ContentType
		if contentType == "" {
			contentType = MediaType
		}
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	for name, value := range spec.Headers {
		resolved, resolveErr := resolveVars(value, vars)
		if resolveErr != nil {
			return nil, fmt.Errorf("header %s: %w", name, resolveErr)
		}
		req.Header.Set(name, resolved)
	}
	return req, nil
}

// requestBody renders a fixture body. null and an absent body both mean "send
// no body", which is what a GET or a DELETE needs.
func requestBody(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	return trimmed, nil
}

// resolveVars substitutes one $name reference. Only a whole-value reference is
// substituted, so a literal header such as "stale" can never be mistaken for a
// variable.
func resolveVars(value string, vars map[string]string) (string, error) {
	if !strings.HasPrefix(value, "$") {
		return value, nil
	}
	name := strings.TrimPrefix(value, "$")
	if !isVariableName(name) {
		return value, nil
	}
	resolved, ok := vars[name]
	if !ok {
		return "", fmt.Errorf("$%s is not set; it must come from an earlier response", name)
	}
	return resolved, nil
}

// isVariableName reports whether name is a bare identifier. Anything else after
// the dollar sign is literal text, so a shell-style "${etag}" written by
// accident stays exactly as it was written.
func isVariableName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// result is one response as the assertions see it.
type result struct {
	status int
	header http.Header
	body   []byte
	etag   string

	method string
	url    string

	stream       bool
	streamFrames int
	lastType     string
	streamErr    error
}

// send performs the request and reads either the whole body or the stream. A
// stream that outlives the deadline is not an error here: the frame assertions
// report what was actually observed.
func send(t *testing.T, client *http.Client, req *http.Request, streaming bool) *result {
	t.Helper()

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	res := &result{
		status: resp.StatusCode,
		header: resp.Header,
		etag:   strings.TrimSpace(resp.Header.Get("ETag")),
		method: req.Method,
		url:    req.URL.String(),
	}

	if streaming {
		res.stream = true
		res.streamFrames, res.lastType, res.streamErr = readStream(resp.Body)
		return res
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL.String(), err)
	}
	res.body = body
	return res
}

// readStream counts SSE data lines and NDJSON lines, ignoring the ready and
// ping control frames. It stops at EOF or at the request deadline.
func readStream(body io.Reader) (frames int, lastType string, err error) {
	reader := bufio.NewReader(body)
	for {
		line, readErr := reader.ReadString('\n')
		if payload, ok := streamPayload(strings.TrimRight(line, "\r\n")); ok {
			typ := frameType(payload)
			if typ != "" {
				lastType = typ
			}
			if typ != readyFrame && typ != pingFrame {
				frames++
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return frames, lastType, nil
			}
			return frames, lastType, readErr
		}
	}
}

// streamPayload extracts one frame payload. SSE frames arrive as "data: ..."
// lines; NDJSON frames are bare JSON lines. Event names, ids and comments are
// framing, not data.
func streamPayload(line string) (string, bool) {
	line = strings.TrimSpace(line)
	switch {
	case line == "" || strings.HasPrefix(line, ":"):
		return "", false
	case strings.HasPrefix(line, "data:"):
		return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
	case strings.HasPrefix(line, "{"):
		return line, true
	default:
		return "", false
	}
}

// frameType reads the type discriminator of one frame.
func frameType(payload string) string {
	var frame struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return ""
	}
	return frame.Type
}

// assertCase checks status, headers, document or stream, in that order: the
// first failing fact is the one a reader needs.
func assertCase(t *testing.T, c Case, res *result) {
	t.Helper()

	if got := res.header.Get(apiVersionHeader); got != APIVersion {
		res.failf(t, c.Name, "%s must be %q, got %q", apiVersionHeader, APIVersion, got)
	}
	if strings.TrimSpace(res.header.Get(requestIDHeader)) == "" {
		res.failf(t, c.Name, "%s must be present on every response", requestIDHeader)
	}

	if res.status != c.Expect.Status {
		res.failf(t, c.Name, "want status %d", c.Expect.Status)
	}
	for name, want := range c.Expect.Headers {
		if got := res.header.Get(name); got != want {
			res.failf(t, c.Name, "header %s: want %q, got %q", name, want, got)
		}
	}

	if c.Stream != nil {
		assertStream(t, c, res)
		return
	}

	if res.status == http.StatusNoContent || res.status == http.StatusNotModified {
		if len(bytes.TrimSpace(res.body)) != 0 {
			res.failf(t, c.Name, "a %d must carry no body", res.status)
		}
		return
	}

	assertJSONContentType(t, c.Name, res)
	assertDocument(t, c.Name, c.Expect, res)
}

// assertJSONContentType requires the v1 media type on every JSON response.
func assertJSONContentType(t *testing.T, label string, res *result) {
	t.Helper()
	if got := res.header.Get("Content-Type"); got != MediaType {
		res.failf(t, label, "want Content-Type %q, got %q", MediaType, got)
	}
}

// assertStream checks the stream media type, frame count and last frame type.
func assertStream(t *testing.T, c Case, res *result) {
	t.Helper()

	switch got := res.header.Get("Content-Type"); got {
	case eventStreamMediaType, ndjsonMediaType:
	default:
		res.failf(t, c.Name, "want an SSE or NDJSON content type, got %q", got)
	}

	if want := c.expectedFrames(); res.streamFrames != want {
		res.failf(t, c.Name, "want %d data frames, got %d (last type %q)", want, res.streamFrames, res.lastType)
	}
	if want := c.Stream.LastType; want != "" && res.lastType != want {
		res.failf(t, c.Name, "want the last frame type %q, got %q", want, res.lastType)
	}
}

// assertDocument checks the JSON envelope. An expectation with an errorCode
// describes an error document; everything else describes a success document.
func assertDocument(t *testing.T, label string, expect Expect, res *result) {
	t.Helper()

	document, err := decodeDocument(res.body)
	if err != nil {
		res.failf(t, label, "body is not JSON: %v", err)
	}

	if expect.ErrorCode != "" {
		assertErrorDocument(t, label, expect, res, document)
		return
	}

	kind, ok := lookupPath(document, "kind")
	if expect.Kind != "" && (!ok || kind != expect.Kind) {
		res.failf(t, label, "want kind %q, got %q", expect.Kind, kind)
	}
	if expect.RequestIDInBody != nil {
		if _, present := lookupPath(document, "requestId"); present != *expect.RequestIDInBody {
			res.failf(t, label, "requestId in body: want present=%v", *expect.RequestIDInBody)
		}
	}
	assertDocumentFacts(t, label, expect, res, document)
}

// assertErrorDocument checks the error envelope and the request ID echo.
func assertErrorDocument(t *testing.T, label string, expect Expect, res *result, document any) {
	t.Helper()

	if kind, _ := lookupPath(document, "kind"); kind != "Error" {
		res.failf(t, label, "want an Error document, got kind %q", kind)
	}
	code, ok := lookupPath(document, "error.code")
	if !ok || code != expect.ErrorCode {
		res.failf(t, label, "want error.code %q, got %q", expect.ErrorCode, code)
	}

	requestID := res.header.Get(requestIDHeader)
	echoed, ok := lookupPath(document, "error.requestId")
	if !ok || echoed != requestID || echoed == "" {
		res.failf(t, label, "error.requestId must echo %s (%q), got %q", requestIDHeader, requestID, echoed)
	}

	assertDocumentFacts(t, label, expect, res, document)
}

// assertDocumentFacts applies the shared item, cursor, path and absence
// assertions to a decoded document.
func assertDocumentFacts(t *testing.T, label string, expect Expect, res *result, document any) {
	t.Helper()

	if expect.Items != nil {
		items, ok := lookupItems(document)
		if !ok || len(items) != *expect.Items {
			res.failf(t, label, "want %d items, got %d", *expect.Items, len(items))
		}
	}
	if expect.NextPresent != nil {
		next, present := lookupPath(document, "next")
		hasNext := present && next != "null" && next != ""
		if hasNext != *expect.NextPresent {
			res.failf(t, label, "next present: want %v, got %v", *expect.NextPresent, hasNext)
		}
	}
	for path, want := range expect.JSONPaths {
		got, ok := lookupPath(document, path)
		if !ok {
			res.failf(t, label, "%s is missing; want %q", path, want)
		}
		if got != want {
			res.failf(t, label, "%s: want %q, got %q", path, want, got)
		}
	}
	for _, path := range expect.AbsentKeys {
		if _, present := lookupPath(document, path); present {
			res.failf(t, label, "%s must not be present", path)
		}
	}
}

// runFollow walks one next cursor. It reuses the primary case's role, token and
// headers so the second request is the same caller continuing the same read.
func runFollow(t *testing.T, env Env, client *http.Client, c Case, token string, vars map[string]string, first *result) {
	t.Helper()

	document, err := decodeDocument(first.body)
	if err != nil {
		first.failf(t, c.Name, "follow needs a JSON body: %v", err)
	}
	next, ok := lookupPath(document, "next")
	if !ok || next == "" || next == "null" {
		first.failf(t, c.Name, "follow requires a next cursor, but the first page had none")
	}

	followVars := make(map[string]string, len(vars)+1)
	for name, value := range vars {
		followVars[name] = value
	}
	followVars["next"] = next

	spec := Request{
		Method:  http.MethodGet,
		Path:    c.Follow.Path,
		Query:   c.Follow.Query,
		Role:    c.Request.Role,
		Headers: c.Request.Headers,
	}
	req, err := newRequest(t.Context(), env, spec, token, followVars)
	if err != nil {
		first.failf(t, c.Name, "build follow request: %v", err)
	}

	followCase := c
	followCase.Name = c.Name + " (follow)"
	followCase.Expect = c.Follow.Expect
	followCase.Stream = nil
	followCase.Follow = nil

	assertCase(t, followCase, send(t, client, req, false))
}

// decodeDocument decodes one response with numbers preserved, so an integer
// compared through jsonPaths keeps the exact text the server wrote.
func decodeDocument(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}

// lookupItems returns the top-level items array.
func lookupItems(document any) ([]any, bool) {
	object, ok := document.(map[string]any)
	if !ok {
		return nil, false
	}
	items, ok := object["items"].([]any)
	return items, ok
}

// lookupPath resolves one dot path with numeric array indexes and renders the
// value as the string a fixture writes.
func lookupPath(document any, path string) (string, bool) {
	current := document
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return "", false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return "", false
			}
			current = node[index]
		default:
			return "", false
		}
	}
	return renderValue(current)
}

// renderValue renders one scalar for comparison. A container is present but has
// no scalar rendering, which keeps absentKeys honest for objects too.
func renderValue(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "null", true
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", true
	}
}

// failf reports one failure with the case, the request, the status and a
// truncated body. The Authorization header is deliberately never printed.
func (r *result) failf(t *testing.T, label, format string, args ...any) {
	t.Helper()

	message := fmt.Sprintf(format, args...)
	if r.streamErr != nil {
		message += fmt.Sprintf("\nstream error: %v", r.streamErr)
	}
	if r.stream {
		message += fmt.Sprintf("\nstream: %d frames, last type %q", r.streamFrames, r.lastType)
	}
	t.Fatalf("%s: %s\nrequest: %s %s\nstatus: %d\nbody: %s",
		label, message, r.method, r.url, r.status, truncateBody(r.body))
}

// truncateBody bounds a reported body so one huge document cannot drown the log.
func truncateBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "<empty>"
	}
	if len(text) <= failureBodyLimit {
		return text
	}
	return text[:failureBodyLimit] + "...(truncated)"
}
