package conformance

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	// octetStreamMediaType is the raw console relay's media type: attach is the
	// one route that does not answer a JSON document or a framed stream.
	octetStreamMediaType = "application/octet-stream"

	readyFrame = "ready"
	pingFrame  = "ping"

	// streamDeadline bounds reading one SSE/NDJSON response. A follow stream
	// stays open until the caller disconnects, so the runner stops reading
	// instead of waiting for the server to end it.
	streamDeadline = 2 * time.Second
	// requestTimeout bounds one non-stream request, so a dead manager fails
	// pointedly instead of hanging until the test binary's own timeout.
	requestTimeout = 10 * time.Second
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
	// Vars seeds the per-file variables a fixture can reference. It is how a
	// task-scoped stream gets its id without hard-coding cluster data: the mock
	// sets task, the lab run reads CONFORMANCE_TASK_ID.
	Vars map[string]string
	// Capabilities is the capability set the target advertises in
	// GET /v1/version. A gated case compares against it. Nil means "not gated":
	// every case runs, which is what a runner unit test wants.
	Capabilities []string
	// RunSuffix replaces every RunToken in a request path, query, header or body.
	// Run fills it with a per-run value; an Env built by hand may leave it empty,
	// and then the token is sent literally.
	RunSuffix string
	// Lab marks a run against a real manager: mock-only cases are skipped and a
	// mutation needs AllowMutation.
	Lab bool
	// AllowMutation permits cases marked mutating. It is set from
	// CONFORMANCE_ALLOW_MUTATION in the lab test and is always true in the mock.
	AllowMutation bool
}

// Summary reports what one Run did. A suite that silently ran fewer cases than
// it defines is a regression, so the mock leg asserts these counts.
type Summary struct {
	// Cases is how many fixture cases were loaded and attempted.
	Cases int
	// Skipped is how many of them were skipped, for any reason.
	Skipped int
	// Gated is how many of them were skipped because the target does not
	// advertise a capability the case requires, or advertises one the case
	// asserts must be absent. It is a declared skip: the mock leg allows it and
	// fails on any other.
	Gated int
	// Skips names every skipped case and why. The counts above say how many
	// cases did not run; this says which ones and what would let them run, which
	// is what a lab run has to report instead of quietly passing on fewer cases.
	Skips []Skip
}

// The reasons a case is skipped. A lab run is strict about them: a skip it can
// avoid is a failure, because a green run that executed half the fixtures proves
// nothing about the contract.
const (
	// SkipMockOnly is a case that asserts fixture data only the mock guarantees.
	// No setting turns it on against a live manager: it is the one skip a lab
	// run is allowed to report and pass.
	SkipMockOnly = "mock-only"
	// SkipCapability is a case the target's /v1/version gates out because it
	// lacks a capability the case requires.
	SkipCapability = "capability"
	// SkipAbsentCapability is a case the target's capability set turns off by
	// having the capability: it asserts the documented disabled behaviour (a
	// 501), so it is the opposite-mode twin of SkipCapability. No setting makes a
	// snapshots-enabled manager answer it, which is why a lab run may report it
	// and still pass — like a mock-only skip.
	SkipAbsentCapability = "absent-capability"
	// SkipMutation is a mutating case the run did not allow.
	SkipMutation = "mutation"
	// SkipToken is a case whose role has no token in this environment.
	SkipToken = "token"
	// SkipVariable is a case that needs a variable the environment did not
	// supply, such as the task id a stream or snapshot case runs against.
	SkipVariable = "variable"
)

// Skip is one case that did not run. Case is the subtest path, so a failure can
// name the exact fixture case.
type Skip struct {
	Case string
	Kind string
	// Detail names what is missing: the capability, the role or the variable.
	Detail string
}

// recordSkip counts one skipped case and keeps it, so the policy that reads the
// summary can name it afterwards. t.Skip unwinds the subtest, so this must be
// called before it.
func (s *Summary) recordSkip(t *testing.T, kind, detail string) {
	t.Helper()

	s.Skipped++
	s.Skips = append(s.Skips, Skip{Case: t.Name(), Kind: kind, Detail: detail})
}

// Run loads every *.json fixture in dir and executes all cases as subtests
// named "<file>/<case>". A malformed or unknown-version fixture is fatal: a
// suite that silently runs fewer cases is worse than one that fails.
func Run(t *testing.T, env Env, dir string) Summary {
	t.Helper()

	fixtures, err := Load(dir)
	if err != nil {
		t.Fatalf("conformance: %v", err)
	}

	if env.RunSuffix == "" {
		suffix, suffixErr := newRunSuffix()
		if suffixErr != nil {
			t.Fatalf("conformance: build a per-run suffix: %v", suffixErr)
		}
		env.RunSuffix = suffix
	}
	// The suffix is not a secret, and naming it in the log is what makes a
	// fixture failure reproducible: the created object names carry it.
	t.Logf("run suffix %q", env.RunSuffix)

	summary := Summary{}
	for _, fixture := range fixtures {
		t.Run(fixtureSlug(fixture.Source), func(t *testing.T) {
			// vars is the file's sequential scope: the environment seeds it, and
			// a later case can use the ETag an earlier one returned ($etag).
			// Subtests run in order, so a plain map is enough and needs no lock.
			vars := seedVars(env)
			for _, c := range fixture.Cases {
				t.Run(c.Name, func(t *testing.T) {
					summary.Cases++
					runCase(t, env, c, vars, &summary)
				})
			}
		})
	}
	return summary
}

// seedVars copies the environment's variables into a fixture's scope, so a file
// cannot leak an ETag into the next one.
func seedVars(env Env) map[string]string {
	vars := make(map[string]string, len(env.Vars)+1)
	for name, value := range env.Vars {
		if strings.TrimSpace(value) != "" {
			vars[name] = value
		}
	}
	return vars
}

// newRunSuffix builds the per-run marker a mutating fixture's names and keys
// carry: a UTC timestamp plus random characters. The timestamp makes a failure
// easy to place in a log; the randomness is what keeps two runs that start in the
// same second apart, which a timestamp alone cannot promise.
func newRunSuffix() (string, error) {
	var nonce [4]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(nonce[:]), nil
}

// applyRunSuffix replaces every run token in one request component. A path, a
// body and a header all carry names, so all three go through it.
func applyRunSuffix(value, suffix string) string {
	if suffix == "" {
		return value
	}
	return strings.ReplaceAll(value, RunToken, suffix)
}

// resolveRunTokens rewrites the values a response is compared against, so a
// fixture writes a name once and both the request and the expectation carry the
// per-run suffix. It copies the maps it touches: a fixture's expectations are
// shared by every leg that loaded them.
func resolveRunTokens(c Case, suffix string) Case {
	if suffix == "" {
		return c
	}

	c.Expect.JSONPaths = suffixedValues(c.Expect.JSONPaths, suffix)
	c.Expect.Headers = suffixedValues(c.Expect.Headers, suffix)
	if c.Follow != nil {
		c.Follow.Expect.JSONPaths = suffixedValues(c.Follow.Expect.JSONPaths, suffix)
		c.Follow.Expect.Headers = suffixedValues(c.Follow.Expect.Headers, suffix)
	}
	return c
}

// suffixedValues returns a copy of values with the run token replaced in every
// value, or nil when there is nothing to rewrite.
func suffixedValues(values map[string]string, suffix string) map[string]string {
	if len(values) == 0 {
		return values
	}
	resolved := make(map[string]string, len(values))
	for key, value := range values {
		resolved[key] = applyRunSuffix(value, suffix)
	}
	return resolved
}

// gatedSkipReason reports why a capability gate skips this case, plus the
// capability list that gated it and the skip kind, or empty strings when the case
// may run. The kind separates the two directions: a missing required capability
// is a precondition the operator can supply, while an advertised absent
// capability means the case is asserted in its opposite mode and does not apply.
// An environment that declared no capability set is not gated: the runner trusts
// the fixture, which is what the runner's own tests rely on.
func (e Env) gatedSkipReason(c Case) (reason, detail, kind string) {
	if e.Capabilities == nil {
		return "", "", ""
	}

	advertised := make(map[string]bool, len(e.Capabilities))
	for _, name := range e.Capabilities {
		advertised[strings.TrimSpace(name)] = true
	}

	missing := make([]string, 0, len(c.RequiresCapabilities))
	for _, name := range c.RequiresCapabilities {
		if !advertised[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("this environment does not advertise %s; run it against a manager that enables the capability",
			strings.Join(missing, ", ")), strings.Join(missing, " and "), SkipCapability
	}

	present := make([]string, 0, len(c.AbsentCapabilities))
	for _, name := range c.AbsentCapabilities {
		if advertised[name] {
			present = append(present, name)
		}
	}
	if len(present) > 0 {
		return fmt.Sprintf("this environment advertises %s, so the disabled-capability case does not apply",
			strings.Join(present, ", ")), strings.Join(present, " and "), SkipAbsentCapability
	}
	return "", "", ""
}

// capture stores the response values a later case in the same file references,
// so a server-generated id reaches the request that cleans it up. A missing path
// fails the case: a capture that silently stored nothing would make the next
// request name a literal "$alias".
func capture(t *testing.T, c Case, res *result, vars map[string]string) {
	t.Helper()
	if len(c.Capture) == 0 || res.stream {
		return
	}

	document, err := decodeDocument(res.body)
	if err != nil {
		res.failf(t, c.Name, "capture needs a JSON body: %v", err)
	}
	for alias, path := range c.Capture {
		value, present := lookupPath(document, path)
		if !present || value == "" || value == "null" {
			res.failf(t, c.Name, "capture %q: %s is missing", alias, path)
		}
		vars[alias] = value
	}
}

// fixtureSlug is the subtest name a fixture file contributes.
func fixtureSlug(source string) string {
	return strings.TrimSuffix(filepath.Base(source), ".json")
}

// runCase executes one case: skip policy, token lookup, request, assertions, the
// optional repeat and the optional cursor follow. Every skip is counted before
// t.Skip unwinds the subtest.
func runCase(t *testing.T, env Env, c Case, vars map[string]string, summary *Summary) {
	t.Helper()

	c = resolveRunTokens(c, env.RunSuffix)

	gate, detail, kind := env.gatedSkipReason(c)
	switch {
	case env.Lab && c.MockOnly:
		summary.recordSkip(t, SkipMockOnly, "")
		t.Skip("mock-only case: it asserts fixture data a live cluster need not contain")
	case gate != "":
		// A capability gate is a declared skip: the target was asked and said no.
		// The lab leg reports it (and, strict by default, fails on a required
		// one) and the mock leg allows it and fails on any other.
		summary.recordSkip(t, kind, detail)
		summary.Gated++
		t.Skip(gate)
	case c.Mutating && !env.AllowMutation:
		summary.recordSkip(t, SkipMutation, "")
		t.Skip("mutating case: set CONFORMANCE_ALLOW_MUTATION=1 to run it")
	}

	token := strings.TrimSpace(env.Tokens[c.Request.Role])
	if token == "" {
		summary.recordSkip(t, SkipToken, c.Request.Role)
		t.Skipf("no %s token configured for this environment; skipping", c.Request.Role)
	}

	client := env.Client
	if client == nil {
		client = http.DefaultClient
	}

	timeout := requestTimeout
	if c.Stream != nil {
		timeout = streamDeadline
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	req, err := newRequest(ctx, env, c.Request, token, vars)
	if err != nil {
		if env.Lab && errors.Is(err, errUnsetVar) {
			summary.recordSkip(t, SkipVariable, missingVariable(err))
			t.Skipf("%v; the environment did not supply it", err)
		}
		t.Fatalf("%s: build request: %v", c.Name, err)
	}

	res := send(t, client, req, c.Stream)
	assertCase(t, c, res, env.Lab)

	if c.Repeat != nil {
		runRepeat(t, env, client, c, token, vars, res)
	}

	// capture runs before the ETag is recorded: a case may store the id it just
	// created and let a later DELETE name it.
	capture(t, c, res, vars)

	// A follow is the brief's contract: only a case that asserted nextPresent
	// walks the cursor. Validation already requires the flag, so this gate is a
	// second line of defence.
	if c.Follow != nil && c.Expect.NextPresent != nil && *c.Expect.NextPresent {
		runFollow(t, env, client, c, token, vars, res)
	}

	// A repeat's last response carries the same ETag as the first; keeping the
	// first is what a later case expects anyway.
	if res.etag != "" {
		vars["etag"] = res.etag
	}
}

// runRepeat sends the same request again and, when the fixture says so, proves
// the response document is byte-identical to the first one. With an
// Idempotency-Key that is the whole assertion: a second create would return a
// new resource with a new revision.
func runRepeat(t *testing.T, env Env, client *http.Client, c Case, token string, vars map[string]string, first *result) {
	t.Helper()

	for attempt := 1; attempt < c.Repeat.Times; attempt++ {
		ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
		req, err := newRequest(ctx, env, c.Request, token, vars)
		if err != nil {
			cancel()
			first.failf(t, c.Name, "build repeat %d: %v", attempt+1, err)
		}
		again := send(t, client, req, c.Stream)
		cancel()

		if !c.Repeat.SameBody {
			continue
		}
		if mismatch := repeatMismatch(first, again); mismatch != "" {
			first.failf(t, c.Name, "repeat %d returned %s", attempt+1, mismatch)
		}
	}
}

// repeatMismatch reports how a repeated response differs from the first one, or
// "" when it is the same status and document. It is the assertion an
// Idempotency-Key replay has to satisfy: the middleware stores the first body
// and status, so a second create that produced a new revision is caught here.
func repeatMismatch(first, again *result) string {
	if again.status != first.status {
		return fmt.Sprintf("status %d, want the first response's %d", again.status, first.status)
	}
	if !bytes.Equal(again.body, first.body) {
		return fmt.Sprintf("a different document:\nfirst: %s\nagain: %s",
			truncateBody(first.body), truncateBody(again.body))
	}
	return ""
}

// errUnsetVar marks a $name reference the environment did not provide. The lab
// run turns it into a skip, like a missing role token: the cluster, not the
// runner, is what lacks the value.
var errUnsetVar = errors.New("variable not set")

// unsetVarError names the $variable that was not supplied, so a skip can say
// which one is missing instead of only that something was.
type unsetVarError struct {
	name string
	// where is the request component the reference appeared in, for the message.
	where string
}

func (e *unsetVarError) Error() string {
	if e.where == "" {
		return fmt.Sprintf("%v: $%s must come from an earlier response or the environment", errUnsetVar, e.name)
	}
	return fmt.Sprintf("%v: $%s in the %s must come from an earlier response or the environment", errUnsetVar, e.name, e.where)
}

func (e *unsetVarError) Unwrap() error { return errUnsetVar }

// missingVariable names the variable an error reports as unset, or "" when the
// error is about something else.
func missingVariable(err error) string {
	var unset *unsetVarError
	if errors.As(err, &unset) {
		return unset.name
	}
	return ""
}

// newRequest builds one authenticated request. Only the fixture's own path,
// query and headers can reach the message a failure prints; the token never
// leaves the Authorization header.
func newRequest(ctx context.Context, env Env, spec Request, token string, vars map[string]string) (*http.Request, error) {
	path, err := resolvePath(spec.Path, vars)
	if err != nil {
		return nil, err
	}
	path = applyRunSuffix(path, env.RunSuffix)

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
			query.Set(key, applyRunSuffix(resolved, env.RunSuffix))
		}
		u.RawQuery = query.Encode()
	}

	body, err := requestBody(spec)
	if err != nil {
		return nil, err
	}
	// The run token is a JSON name marker. Raw bytes are sent exactly as the
	// fixture encoded them, so rewriting them would make "exact bytes" mean
	// something else.
	if body != nil && spec.BodyBase64 == "" && env.RunSuffix != "" {
		body = bytes.ReplaceAll(body, []byte(RunToken), []byte(env.RunSuffix))
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
		switch {
		case contentType != "":
		case spec.BodyBase64 != "":
			// A raw body defaults to bytes: the file upload refuses anything but
			// application/octet-stream.
			contentType = octetStreamMediaType
		default:
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
		req.Header.Set(name, applyRunSuffix(resolved, env.RunSuffix))
	}
	return req, nil
}

// requestBody renders a fixture body. bodyBase64 is decoded to the exact bytes
// the route receives; otherwise the JSON text of body is sent. null and an
// absent body both mean "send no body", which is what a GET or a DELETE needs.
func requestBody(spec Request) ([]byte, error) {
	if spec.BodyBase64 != "" {
		body, err := base64.StdEncoding.DecodeString(spec.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("request.bodyBase64: %w", err)
		}
		return body, nil
	}

	trimmed := bytes.TrimSpace(spec.Body)
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
		return "", &unsetVarError{name: name}
	}
	return resolved, nil
}

// resolvePath substitutes every $name reference inside a request path. A path is
// a template ("/v1/tasks/$task/exec") because a task id is one segment, not the
// whole value.
func resolvePath(path string, vars map[string]string) (string, error) {
	var out strings.Builder
	for {
		at := strings.IndexByte(path, '$')
		if at < 0 {
			out.WriteString(path)
			return out.String(), nil
		}
		out.WriteString(path[:at])

		rest := path[at+1:]
		end := 0
		for end < len(rest) && isVariableByte(rest[end]) {
			end++
		}
		if end == 0 {
			out.WriteByte('$')
			path = rest
			continue
		}

		name := rest[:end]
		resolved, ok := vars[name]
		if !ok {
			return "", &unsetVarError{name: name, where: "request path"}
		}
		out.WriteString(resolved)
		path = rest[end:]
	}
}

// isVariableByte reports whether b may appear in a variable name. Names are
// ASCII identifiers, so a path segment such as "$5.00" is literal text.
func isVariableByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
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

	stream    bool
	raw       bool
	frames    int
	payload   int
	controls  int
	rawBytes  int
	lastType  string
	streamErr error
}

// streamRead is what one streaming response yielded before the reader stopped.
type streamRead struct {
	frames   int
	payload  int
	controls int
	rawBytes int
	lastType string
	err      error
}

// send performs the request and reads either the whole body or the stream. A
// stream that outlives the deadline is not an error here: the frame assertions
// report what was actually observed.
func send(t *testing.T, client *http.Client, req *http.Request, stream *Stream) *result {
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

	if stream != nil {
		res.stream = true
		if stream.Bytes > 0 {
			read := readRawStream(resp.Body, stream.Bytes)
			res.raw = true
			res.rawBytes = read.rawBytes
			res.streamErr = read.err
			return res
		}
		read := readStream(resp.Body, *stream)
		res.frames = read.frames
		res.payload = read.payload
		res.controls = read.controls
		res.lastType = read.lastType
		res.streamErr = read.err
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
// ping control frames. It returns as soon as it has seen the number of frames the
// case asks for, a frame of the terminal type, the bounded read's frame cap, EOF
// or the request deadline: a live follow stream never ends on its own, so the
// runner stops reading rather than waiting for the server to close it.
//
// A bounded read (untilType) also counts the payload frames before the terminal
// one, which is the floor a log case asserts: the terminal frame itself is the end
// marker, not a line.
func readStream(body io.Reader, spec Stream) streamRead {
	var read streamRead

	terminal := spec.LastType
	if spec.bounded() {
		terminal = spec.UntilType
	}

	reader := bufio.NewReader(body)
	for {
		line, readErr := reader.ReadString('\n')
		if payload, ok := streamPayload(strings.TrimRight(line, "\r\n")); ok {
			typ := frameType(payload)
			if typ != "" {
				read.lastType = typ
			}
			switch {
			case typ == readyFrame || typ == pingFrame:
				read.controls++
			case typ != "" && typ == terminal:
				read.frames++
			default:
				read.frames++
				read.payload++
			}
		}

		if spec.bounded() {
			if read.lastType == spec.UntilType || read.frames >= spec.MaxFrames {
				return read
			}
		} else if read.frames >= spec.Frames || (spec.LastType != "" && read.lastType == spec.LastType) {
			return read
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				read.err = readErr
			}
			return read
		}
	}
}

// readRawStream reads a raw console stream: exactly want bytes, or however many
// arrived before the request deadline. It is attach's counterpart to readStream:
// application/octet-stream carries no frames to count, and a live console may stay
// open and silent, so stopping at the count is what ends the read.
func readRawStream(body io.Reader, want int) streamRead {
	var read streamRead

	buf := make([]byte, want)
	n, err := io.ReadFull(body, buf)
	read.rawBytes = n
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		read.err = err
	}
	return read
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
func assertCase(t *testing.T, c Case, res *result, lab bool) {
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
		assertStream(t, c, res, lab)
		return
	}

	// A raw response carries opaque bytes: its media type and length are the
	// fixture's to assert (expect.headers, expect.bodyBase64, expect.bodyLength),
	// and there is no JSON envelope to check.
	if c.Expect.BodyBase64 != "" || c.Expect.BodyLength != nil {
		assertRawBody(t, c, res)
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

// assertRawBody checks an opaque response body (a file download): the bytes must
// match the fixture exactly, and a short body fails as truncated instead of as a
// different value.
func assertRawBody(t *testing.T, c Case, res *result) {
	t.Helper()
	if problem := rawBodyProblem(c.Expect, res); problem != "" {
		res.failf(t, c.Name, "%s", problem)
	}
}

// rawBodyProblem is the raw-body assertion as a value, so a test can pin both
// outcomes — the exact bytes and a truncated body — without failing the binary.
//
// A declared bodyLength is checked first: it is the failure a relay that stops
// copying produces, and reporting it as a length difference names the bug. A
// body that stops short of the expected bytes is reported as truncation for the
// same reason.
func rawBodyProblem(expect Expect, res *result) string {
	if expect.BodyLength != nil && len(res.body) != *expect.BodyLength {
		return fmt.Sprintf("want %d body bytes, got %d", *expect.BodyLength, len(res.body))
	}
	if expect.BodyBase64 == "" {
		return ""
	}

	want, err := base64.StdEncoding.DecodeString(expect.BodyBase64)
	if err != nil {
		// The loader rejects this; a hand-built fixture can still reach here.
		return fmt.Sprintf("bodyBase64 is not valid base64: %v", err)
	}
	if bytes.Equal(res.body, want) {
		return ""
	}
	if bytes.HasPrefix(want, res.body) {
		return fmt.Sprintf("the body is truncated: got %d of %d bytes", len(res.body), len(want))
	}
	return fmt.Sprintf("want %d exact bytes, got %d; first difference at byte %d",
		len(want), len(res.body), firstDifference(res.body, want))
}

// firstDifference returns the index of the first byte two bodies disagree on, or
// the length of the shorter one when one is a prefix of the other.
func firstDifference(got, want []byte) int {
	limit := len(got)
	if len(want) < limit {
		limit = len(want)
	}
	for i := 0; i < limit; i++ {
		if got[i] != want[i] {
			return i
		}
	}
	return limit
}

// assertJSONContentType requires the v1 media type on every JSON response.
func assertJSONContentType(t *testing.T, label string, res *result) {
	t.Helper()
	if got := res.header.Get("Content-Type"); got != MediaType {
		res.failf(t, label, "want Content-Type %q, got %q", MediaType, got)
	}
}

// assertStream checks the stream media type and then the shape's assertion: an
// exact frame count, a bounded read to a named terminal frame, or a raw byte
// count.
//
// The mock is deterministic, so it asserts the exact count. A live manager is
// not: an exec command or a log tail may carry more frames than the fixture
// names, and a quiet cluster emits no event at all, only the ready control
// frame. In lab mode the terminal frame is the assertion, extra frames are
// allowed, and control-only is accepted for an event stream that opened
// correctly.
func assertStream(t *testing.T, c Case, res *result, lab bool) {
	t.Helper()

	if c.Stream.Bytes > 0 {
		assertRawStream(t, c, res, lab)
		return
	}

	switch got := res.header.Get("Content-Type"); got {
	case eventStreamMediaType, ndjsonMediaType:
	default:
		res.failf(t, c.Name, "want an SSE or NDJSON content type, got %q", got)
	}

	if c.Stream.bounded() {
		assertBoundedStream(t, c, res)
		return
	}

	if want := c.Stream.LastType; want != "" && res.lastType != want {
		res.failf(t, c.Name, "want a final %q frame, got %q after %d data frames", want, res.lastType, res.frames)
	}

	if !lab {
		if want := c.Stream.Frames; res.frames != want {
			res.failf(t, c.Name, "want %d data frames, got %d (last type %q)", want, res.frames, res.lastType)
		}
		return
	}

	if c.Stream.LastType != "" {
		// The terminal frame matched above, which is the whole assertion.
		return
	}
	if res.frames >= c.Stream.Frames {
		return
	}
	if res.frames == 0 && res.controls > 0 {
		t.Logf("the live stream carried only control frames (a quiet cluster); the mock leg proves frame content")
		return
	}

	res.failf(t, c.Name, "want at least %d data frames, got %d (last type %q)", c.Stream.Frames, res.frames, res.lastType)
}

// assertBoundedStream checks a stream read to a named terminal frame. It behaves
// the same in both legs: "read until eof, and prove the tail carried a line" is
// true of the mock and of a real console, and the frame count is the server's to
// choose either way.
func assertBoundedStream(t *testing.T, c Case, res *result) {
	t.Helper()
	if err := c.Stream.boundedProblem(res); err != nil {
		res.failf(t, c.Name, "%v", err)
	}
}

// boundedProblem is a bounded stream's assertion as a value, so a test can pin
// both outcomes — the terminal frame arrived with enough payload, and the
// terminal frame never arrived — without failing the test binary.
func (s Stream) boundedProblem(res *result) error {
	if res.lastType != s.UntilType {
		return fmt.Errorf("want a final %q frame, got %q after %d payload frames (cap %d frames)",
			s.UntilType, res.lastType, res.payload, s.MaxFrames)
	}
	if res.payload < s.MinFrames {
		return fmt.Errorf("want at least %d payload frames before %q, got %d",
			s.MinFrames, s.UntilType, res.payload)
	}
	return nil
}

// assertRawStream checks a raw console relay (attach): the response is
// application/octet-stream and carries console bytes.
//
// The mock writes a known console line, so it asserts the exact byte count. A
// live guest may print nothing before the caller hangs up — a getty that is idle,
// or a console whose output the caller is not entitled to see — so the lab leg
// accepts a silent console: the 200 and the media type already prove the session
// reached the node, and the mock proves the bytes.
func assertRawStream(t *testing.T, c Case, res *result, lab bool) {
	t.Helper()

	if got := res.header.Get("Content-Type"); got != octetStreamMediaType {
		res.failf(t, c.Name, "want Content-Type %q, got %q", octetStreamMediaType, got)
	}

	if !lab {
		if res.rawBytes != c.Stream.Bytes {
			res.failf(t, c.Name, "want %d console bytes, got %d", c.Stream.Bytes, res.rawBytes)
		}
		return
	}
	if res.rawBytes == 0 {
		t.Logf("the live console sent no bytes before the deadline; the mock leg proves the relay copies console output")
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
		if mismatch := requestIDEchoMismatch(document, expect, res.header.Get(requestIDHeader)); mismatch != "" {
			res.failf(t, label, "%s", mismatch)
		}
	}
	assertDocumentFacts(t, label, expect, res, document)
}

// requestIDEchoMismatch reports how the document disagrees with
// requestIdInBody/requestIdPath, or "" when it agrees. A true expectation is
// checked twice: the path exists, and it carries this request's ID — a document
// that echoed someone else's request would be worse than one that echoed none.
func requestIDEchoMismatch(document any, expect Expect, requestID string) string {
	path := expect.RequestIDPath
	if path == "" {
		path = "requestId"
	}

	value, present := lookupPath(document, path)
	if present != *expect.RequestIDInBody {
		return fmt.Sprintf("%s in body: want present=%v", path, *expect.RequestIDInBody)
	}
	if !*expect.RequestIDInBody {
		return ""
	}
	if strings.TrimSpace(requestID) == "" {
		return fmt.Sprintf("%s must echo %s, but the response carried none", path, requestIDHeader)
	}
	if value != requestID {
		return fmt.Sprintf("%s must echo %s %q, got %q", path, requestIDHeader, requestID, value)
	}
	return ""
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
		values := lookupValues(document, path)
		if len(values) == 0 {
			res.failf(t, label, "%s is missing; want %q", path, want)
		}
		if !slices.Contains(values, want) {
			res.failf(t, label, "%s: want %q, got %q", path, want, values)
		}
	}
	for _, path := range expect.PresentKeys {
		if _, present := lookupPath(document, path); !present {
			res.failf(t, label, "%s must be present", path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(expect.MinValues)) {
		if mismatch := minimumMismatch(document, path, expect.MinValues[path]); mismatch != "" {
			res.failf(t, label, "%s", mismatch)
		}
	}
	for _, path := range expect.AbsentKeys {
		if _, present := lookupPath(document, path); present {
			res.failf(t, label, "%s must not be present", path)
		}
	}
	for _, key := range expect.AbsentKeysAnywhere {
		if containsKey(document, key) {
			res.failf(t, label, "a %q key must not appear at any depth", key)
		}
	}
}

// minimumMismatch reports why the value at path is not an integer at least want,
// or "" when it is. The comparison is on the exact JSON text, so a float is
// rejected instead of silently rounded.
func minimumMismatch(document any, path string, want int64) string {
	value, present := lookupPath(document, path)
	if !present {
		return fmt.Sprintf("%s is missing; want an integer of at least %d", path, want)
	}

	got, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Sprintf("%s: want an integer of at least %d, got %s", path, want, value)
	}
	if got < want {
		return fmt.Sprintf("%s: want at least %d, got %d", path, want, got)
	}
	return ""
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
	followCase.Repeat = nil

	followCtx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	req = req.WithContext(followCtx)

	assertCase(t, followCase, send(t, client, req, nil), env.Lab)
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
// value as the string a fixture writes. It returns the first match, which is
// what the envelope assertions want.
func lookupPath(document any, path string) (string, bool) {
	values := lookupValues(document, path)
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

// lookupValues resolves a dot path to every value it matches. A "*" segment
// matches every array element, so "items.*.action" answers "which actions does
// this page carry?"; a jsonPaths assertion with a wildcard therefore means "at
// least one item has this value", which is how a fixture asserts an entry exists
// without pinning its position.
func lookupValues(document any, path string) []string {
	values := make([]string, 0, 1)
	collectValues(document, strings.Split(path, "."), &values)
	return values
}

func collectValues(current any, segments []string, values *[]string) {
	if len(segments) == 0 {
		if rendered, ok := renderValue(current); ok {
			*values = append(*values, rendered)
		}
		return
	}

	segment, rest := segments[0], segments[1:]
	switch node := current.(type) {
	case map[string]any:
		next, ok := node[segment]
		if !ok {
			return
		}
		collectValues(next, rest, values)
	case []any:
		if segment == "*" {
			for _, item := range node {
				collectValues(item, rest, values)
			}
			return
		}
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(node) {
			return
		}
		collectValues(node[index], rest, values)
	}
}

// containsKey reports whether key appears as an object key at any depth. It is
// the scan an absentKeys dot path cannot make: a path whose parent is missing
// passes without ever looking at the payload.
func containsKey(document any, key string) bool {
	switch node := document.(type) {
	case map[string]any:
		if _, ok := node[key]; ok {
			return true
		}
		for _, value := range node {
			if containsKey(value, key) {
				return true
			}
		}
	case []any:
		for _, value := range node {
			if containsKey(value, key) {
				return true
			}
		}
	}
	return false
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
		if r.raw {
			message += fmt.Sprintf("\nstream: %d console bytes", r.rawBytes)
		} else {
			message += fmt.Sprintf("\nstream: %d frames (%d payload), %d control frames, last type %q",
				r.frames, r.payload, r.controls, r.lastType)
		}
	}
	t.Fatalf("%s: %s\nrequest: %s %s\nstatus: %d\nbody: %s",
		label, message, r.method, r.url, r.status, truncateBody(r.body))
}

// truncateBody bounds a reported body so one huge document cannot drown the log.
// The cut lands on a UTF-8 boundary, so the report is never invalid text. A body
// that is not text at all — a file download, a console relay — is rendered as a
// quoted Go string instead: the bytes stay exact and a failure log stays readable.
func truncateBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "<empty>"
	}
	if !utf8.ValidString(text) {
		return truncateQuoted(text)
	}
	if len(text) <= failureBodyLimit {
		return text
	}

	cut := failureBodyLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "...(truncated)"
}

// truncateQuoted renders a non-text body as a quoted string. It cuts to a quarter
// of the limit first, because one byte can become four characters (\xff), so a
// short body stays short and a huge one is cut before it is escaped.
func truncateQuoted(text string) string {
	limit := failureBodyLimit / 4
	if len(text) <= limit {
		return strconv.Quote(text)
	}
	return strconv.Quote(text[:limit]) + "...(truncated)"
}
