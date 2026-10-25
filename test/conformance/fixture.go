// Package conformance executes one declarative fixture set against two
// environments: the real apihttp handler with an in-memory backend on loopback
// TLS (the mock run, part of every PR), and a live manager (the lab run, on a
// schedule and by hand).
//
// The fixtures are plain JSON and this package is the only executor, so the
// suite is a language-neutral description of the v1 HTTP contract: the Go SDK,
// the Python SDK and the Node SDK can all be pointed at the same files later.
package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion is the only fixture schema this package understands. A file
// that declares another version is a fatal error, never a best-effort parse.
const SchemaVersion = 1

// RunToken marks the text every mutating create name and every Idempotency-Key
// must carry. The runner replaces it with a per-run suffix (UTC timestamp plus
// random characters), so two runs — sequential or parallel, in CI or by hand —
// never create the same named object and never replay another run's stored
// Idempotency-Key response.
const RunToken = "{{run}}"

// reservedVars are the variable names the runner supplies itself. A capture may
// not shadow them: $etag and $next are bound from responses, and $task comes from
// the environment.
var reservedVars = map[string]bool{
	"etag": true,
	"next": true,
	"task": true,
}

// knownRoles is the closed set of token roles a fixture may name. The runner
// owns which secret to use; the fixture only says which authority it needs.
var knownRoles = map[string]bool{
	"reader": true,
	"runner": true,
	"admin":  true,
}

// knownMethods is the closed set of methods the v1 surface serves.
var knownMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
	http.MethodPut:    true,
}

// Fixture is one file: a named group of cases sharing a file-level scope and
// sequential state (the ETag a previous case returned).
type Fixture struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name"`
	Cases         []Case `json:"cases"`

	// Source is the file the fixture was loaded from. It is not part of the
	// JSON document; the loader fills it in so a failure can name the file.
	Source string `json:"-"`
}

// Case is one request and its expectations. A case is the unit a runner skips,
// reports and names as a subtest.
type Case struct {
	Name string `json:"name"`
	// Mutating marks a case whose request changes cluster state. Lab runs skip
	// these unless CONFORMANCE_ALLOW_MUTATION=1.
	Mutating bool `json:"mutating,omitempty"`
	// MockOnly marks a case that asserts data only the in-process fixture set
	// guarantees. Lab runs skip these: they must never assume cluster contents.
	MockOnly bool `json:"mockOnly,omitempty"`
	// RequiresCapabilities names the capabilities (as advertised by
	// GET /v1/version) the target must have for this case to run. A run that
	// declares its capability set skips the case when one is missing; the mock
	// leg counts that skip as an explicit gate, not as a silent loss.
	RequiresCapabilities []string `json:"requiresCapabilities,omitempty"`
	// AbsentCapabilities names capabilities the target must NOT have. It is how
	// a case asserts the documented disabled behaviour (a 501) without breaking
	// a manager that was started with the capability enabled.
	AbsentCapabilities []string `json:"absentCapabilities,omitempty"`
	Request            Request  `json:"request"`
	Expect             Expect   `json:"expect"`
	// Follow follows one next cursor when Expect.NextPresent is true.
	Follow *Follow `json:"follow,omitempty"`
	// Repeat sends the same request more than once. It is how a fixture proves
	// an Idempotency-Key: with SameBody, the second response must be the first.
	Repeat *Repeat `json:"repeat,omitempty"`
	// Stream reads an SSE or NDJSON response instead of a JSON document.
	Stream *Stream `json:"stream,omitempty"`
	// Capture stores values from the response document as variables for later
	// cases in the same file: alias -> dot path. It is how a create's generated
	// id reaches the DELETE that cleans it up, without pinning server ids.
	Capture map[string]string `json:"capture,omitempty"`
}

// Repeat turns one case into several identical requests.
type Repeat struct {
	// Times is how many times the request is sent in total (at least two).
	Times int `json:"times,omitempty"`
	// SameBody asserts every repeat returned the primary response's body and
	// status, which is how a replay proves it did not run a second write.
	SameBody bool `json:"sameBody,omitempty"`
}

// Request is one HTTP request. The runner supplies the bearer token for Role.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Role   string            `json:"role"`
	// Headers are sent verbatim after the runner's defaults, so a fixture can
	// override Accept or add a precondition.
	Headers map[string]string `json:"headers,omitempty"`
	// Body is a JSON value. null or an absent body sends no request body.
	Body json.RawMessage `json:"body,omitempty"`
	// ContentType overrides the default v1 media type a body is sent with. A
	// PATCH uses application/merge-patch+json.
	ContentType string `json:"contentType,omitempty"`
	// Accept overrides the default Accept header.
	Accept string `json:"accept,omitempty"`
}

// Expect is the assertion set for one response. Exactly one of Kind and
// ErrorCode is set, except for a documentless status (204/304) and for a
// streamed response, which carries frames instead of a document.
type Expect struct {
	Status      int    `json:"status"`
	Kind        string `json:"kind,omitempty"`
	ErrorCode   string `json:"errorCode,omitempty"`
	Items       *int   `json:"items,omitempty"`
	NextPresent *bool  `json:"nextPresent,omitempty"`
	// RequestIDInBody asserts the presence (true) or absence (false) of the
	// request ID echo at RequestIDPath in a success document. An error document
	// always echoes the request ID in error.requestId and does not use this flag.
	RequestIDInBody *bool `json:"requestIdInBody,omitempty"`
	// RequestIDPath is the dot path the echo is asserted at. It defaults to the
	// top-level "requestId" and is only meaningful with requestIdInBody: true.
	// The health partial is the one success document that carries it nested
	// (partial.0.error.requestId).
	RequestIDPath string `json:"requestIdPath,omitempty"`
	// Headers are exact-match assertions on the response.
	Headers map[string]string `json:"headers,omitempty"`
	// JSONPaths are dot paths with numeric array indexes, compared as strings.
	// A "*" segment matches any array element, so "items.*.action" asserts that
	// at least one item carries the action.
	JSONPaths map[string]string `json:"jsonPaths,omitempty"`
	// PresentKeys are dot paths that must exist. It is the complement of
	// AbsentKeys and the only way to assert a value the fixture cannot name, such
	// as a server-generated path or a size.
	PresentKeys []string `json:"presentKeys,omitempty"`
	// MinValues are dot paths whose value must be a JSON integer at least the
	// given minimum. It is how a case asserts sizeBytes is a real total without
	// pinning a byte count the node chooses.
	MinValues map[string]int64 `json:"minValues,omitempty"`
	// AbsentKeys are dot paths that must not exist in the body.
	AbsentKeys []string `json:"absentKeys,omitempty"`
	// AbsentKeysAnywhere are key names that must not appear at any depth. Use it
	// for a payload the DTO can never name: a dotted path would silently pass
	// once its parent object is absent.
	AbsentKeysAnywhere []string `json:"absentKeysAnywhere,omitempty"`
}

// Follow is the one extra request that walks a next cursor.
type Follow struct {
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Expect Expect            `json:"expect"`
}

// Stream is a streamed response's assertion. A case names exactly one of three
// shapes: an exact frame count (Frames, optionally with LastType), a read to a
// named terminal frame (UntilType with MinFrames and MaxFrames), or a raw byte
// count (Bytes, for attach, the one route that serves console bytes).
//
// Frames is the number of frames a deterministic stream carries: reading stops as
// soon as that many have arrived. LastType names the frame that must end it (exit
// for exec); when set, reading also stops there, so a live stream that keeps
// emitting is not read to the deadline.
//
// UntilType is the shape for a stream whose length the server chooses: a real
// console log carries as many lines as the guest printed, so a pinned count stops
// before eof and fails a correct server. The case reads until that frame arrives
// instead, with MinFrames and MaxFrames bounding the read.
type Stream struct {
	Frames   int    `json:"frames,omitempty"`
	LastType string `json:"lastType,omitempty"`
	// UntilType names the frame that ends a bounded read. The frame itself is not
	// counted as payload: it is the end marker, not a line.
	UntilType string `json:"untilType,omitempty"`
	// MinFrames is the fewest payload frames that must precede the terminal frame.
	// It is what keeps "read until eof" from passing on a stream that carried no
	// line at all. Required with UntilType.
	MinFrames int `json:"minFrames,omitempty"`
	// MaxFrames bounds a bounded read, terminal frame included: reading stops
	// after that many frames even when the terminal frame never arrives, so a
	// server that omits it fails with the observed frames instead of hanging to
	// the deadline. Required with UntilType, and greater than MinFrames.
	MaxFrames int `json:"maxFrames,omitempty"`
	// Bytes is how many raw bytes a stream case reads before it stops. It is the
	// assertion for application/octet-stream, which carries no frames.
	Bytes int `json:"bytes,omitempty"`
}

// bounded reports whether the case reads to a named terminal frame instead of a
// pinned frame count.
func (s Stream) bounded() bool { return s.UntilType != "" }

// validate checks that a stream names exactly one read shape and that the bounded
// shape is fully specified: an unbounded read could hang, and a read with no floor
// would pass on a stream that carried nothing.
func (s Stream) validate() error {
	shapes := 0
	if s.Bytes > 0 {
		shapes++
	}
	if s.Frames > 0 {
		shapes++
	}
	if s.bounded() {
		shapes++
	}
	switch {
	case shapes == 0:
		return errors.New("stream needs a positive frames count, a positive bytes count or an untilType")
	case shapes > 1:
		return errors.New("stream names exactly one of bytes (a raw stream), frames/lastType (an exact framed stream) or untilType (a bounded framed stream)")
	}

	if s.Bytes > 0 {
		if s.LastType != "" || s.MinFrames > 0 || s.MaxFrames > 0 {
			return errors.New("stream: lastType, minFrames and maxFrames describe a framed stream, not a raw one")
		}
		return nil
	}
	if s.Frames > 0 {
		if s.MinFrames > 0 || s.MaxFrames > 0 {
			return errors.New("stream: minFrames and maxFrames require untilType")
		}
		return nil
	}

	if s.LastType != "" {
		return errors.New("stream: lastType and untilType assert the same terminal frame; name one")
	}
	if s.MinFrames < 1 {
		return errors.New("stream: untilType needs a positive minFrames (payload frames before the terminal frame)")
	}
	if s.MaxFrames < 1 {
		return errors.New("stream: untilType needs a positive maxFrames cap, so a missing terminal frame fails instead of hanging")
	}
	if s.MaxFrames <= s.MinFrames {
		return fmt.Errorf("stream: maxFrames (%d) must exceed minFrames (%d)", s.MaxFrames, s.MinFrames)
	}
	return nil
}

// Load reads every *.json file in dir, sorted by name, and validates it.
func Load(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("conformance: read fixture directory %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	if len(names) == 0 {
		return nil, fmt.Errorf("conformance: no *.json fixtures in %s", dir)
	}

	fixtures := make([]Fixture, 0, len(names))
	for _, name := range names {
		fixture, err := LoadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		fixtures = append(fixtures, fixture)
	}
	if err := assertUniqueCreateNames(fixtures); err != nil {
		return nil, fmt.Errorf("conformance: %s: %w", dir, err)
	}
	return fixtures, nil
}

// assertUniqueCreateNames is the loader's cross-file rule: no two mutating cases
// may create the same named object, and (checked per case) every such name must
// carry the run token. Two files with the same literal name would collide in one
// run — the second create would be a 409 — and the case that asserted a 201 would
// fail for a reason that has nothing to do with the contract.
func assertUniqueCreateNames(fixtures []Fixture) error {
	seen := make(map[string]string)
	for _, fixture := range fixtures {
		for _, c := range fixture.Cases {
			if !c.Mutating {
				continue
			}
			name := c.createdName()
			if name == "" {
				continue
			}
			where := fmt.Sprintf("%s case %q", filepath.Base(fixture.Source), c.Name)
			if first, duplicate := seen[name]; duplicate {
				return fmt.Errorf("mutating name %q is created twice (%s and %s); a single run cannot create it once",
					name, first, where)
			}
			seen[name] = where
		}
	}
	return nil
}

// LoadFile reads and validates one fixture file. The decoder is strict: an
// unknown key is a typo in the contract, not something to ignore.
func LoadFile(path string) (Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Fixture{}, fmt.Errorf("conformance: read %s: %w", path, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var fixture Fixture
	if err := decoder.Decode(&fixture); err != nil {
		return Fixture{}, fmt.Errorf("conformance: parse %s: %w", path, err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Fixture{}, fmt.Errorf("conformance: %s must contain exactly one JSON document", path)
	}

	if fixture.SchemaVersion != SchemaVersion {
		return Fixture{}, fmt.Errorf(
			"conformance: %s: unsupported schemaVersion %d (this runner understands %d)",
			path, fixture.SchemaVersion, SchemaVersion)
	}

	fixture.Source = path
	if err := fixture.Validate(); err != nil {
		return Fixture{}, fmt.Errorf("conformance: %s: %w", path, err)
	}
	return fixture, nil
}

// Validate checks the invariants the runner relies on. It is exported so a new
// fixture can be linted without executing it.
func (f Fixture) Validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return errors.New("fixture name must not be empty")
	}
	if len(f.Cases) == 0 {
		return fmt.Errorf("fixture %q has no cases", f.Name)
	}

	seen := make(map[string]bool, len(f.Cases))
	for i, c := range f.Cases {
		if err := c.validate(); err != nil {
			return fmt.Errorf("case %d (%q): %w", i, c.Name, err)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate case name %q", c.Name)
		}
		seen[c.Name] = true
	}
	return nil
}

// forbiddenHeaders are the header names a fixture may not set. The runner owns
// the credential (Authorization) and the transport owns message framing
// (Connection, Transfer-Encoding, Upgrade); a fixture that sets either would test
// the HTTP stack, not the API.
var forbiddenHeaders = map[string]bool{
	"authorization":       true,
	"cookie":              true,
	"proxy-authorization": true,
	"connection":          true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// validate checks one case. The stream and follow blocks relax the
// "exactly one expectation kind" rule for reasons the error text names.
func (c Case) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("case name must not be empty")
	}
	if !knownRoles[c.Request.Role] {
		return fmt.Errorf("unknown role %q (want reader, runner or admin)", c.Request.Role)
	}
	if !knownMethods[c.Request.Method] {
		return fmt.Errorf("unknown request method %q", c.Request.Method)
	}
	if !strings.HasPrefix(c.Request.Path, "/v1/") {
		return fmt.Errorf("request path %q must be an absolute path under /v1/", c.Request.Path)
	}
	for name := range c.Request.Headers {
		if forbiddenHeaders[strings.ToLower(strings.TrimSpace(name))] {
			return fmt.Errorf("request header %q may not be set by a fixture", name)
		}
	}
	for _, key := range c.Expect.AbsentKeysAnywhere {
		if strings.TrimSpace(key) == "" {
			return errors.New("absentKeysAnywhere entries must be non-empty key names")
		}
	}
	for _, key := range c.Expect.PresentKeys {
		if strings.TrimSpace(key) == "" {
			return errors.New("presentKeys entries must be non-empty dot paths")
		}
	}
	for path := range c.Expect.MinValues {
		if strings.TrimSpace(path) == "" {
			return errors.New("minValues keys must be non-empty dot paths")
		}
	}
	absent := make(map[string]bool, len(c.Expect.AbsentKeys))
	for _, path := range c.Expect.AbsentKeys {
		absent[path] = true
	}
	for _, path := range c.Expect.PresentKeys {
		if absent[path] {
			return fmt.Errorf("path %q is both presentKeys and absentKeys", path)
		}
	}

	if err := c.validateCapabilities(); err != nil {
		return err
	}
	if err := c.validateRunNames(); err != nil {
		return err
	}
	if err := c.validateCapture(); err != nil {
		return err
	}

	strict := c.Stream == nil
	if err := validateExpect(c.Expect, strict, "expect"); err != nil {
		return err
	}

	if c.Stream != nil {
		if c.Expect.RequestIDInBody != nil {
			return errors.New("requestIdInBody describes a JSON document, not a stream")
		}
		if err := c.Stream.validate(); err != nil {
			return err
		}
	}
	if c.Repeat != nil {
		if c.Stream != nil {
			return errors.New("repeat applies to a JSON document, not a stream")
		}
		if c.Repeat.Times < 2 {
			return errors.New("repeat needs times of at least 2")
		}
	}

	if c.Follow != nil {
		if !strings.HasPrefix(c.Follow.Path, "/v1/") {
			return fmt.Errorf("follow path %q must be an absolute path under /v1/", c.Follow.Path)
		}
		// A follow only exists to walk a cursor the primary response must have
		// returned. Without nextPresent: true the runner would either skip the
		// follow silently or follow a cursor the case never asserted.
		if c.Expect.NextPresent == nil || !*c.Expect.NextPresent {
			return errors.New("follow needs expect.nextPresent: true")
		}
		if err := validateExpect(c.Follow.Expect, false, "follow expect"); err != nil {
			return err
		}
	}
	return nil
}

// validateCapabilities checks the two capability gates: a name must be usable
// and a case may not require and forbid the same capability.
func (c Case) validateCapabilities() error {
	required := make(map[string]bool, len(c.RequiresCapabilities))
	for _, name := range c.RequiresCapabilities {
		if strings.TrimSpace(name) == "" {
			return errors.New("requiresCapabilities entries must be non-empty names")
		}
		required[name] = true
	}
	for _, name := range c.AbsentCapabilities {
		if strings.TrimSpace(name) == "" {
			return errors.New("absentCapabilities entries must be non-empty names")
		}
		if required[name] {
			return fmt.Errorf("capability %q is both required and absent", name)
		}
	}
	return nil
}

// validateRunNames enforces the per-run rule: a mutating create and a mutating
// Idempotency-Key must carry the run token, so a repeated or parallel run cannot
// reuse the name or replay the key. A literal name is a collision waiting for the
// second run.
func (c Case) validateRunNames() error {
	if !c.Mutating {
		return nil
	}
	if name := c.createdName(); name != "" && !strings.Contains(name, RunToken) {
		return fmt.Errorf("mutating create name %q must contain %s so runs cannot collide", name, RunToken)
	}
	if key := c.Request.Headers["Idempotency-Key"]; key != "" && !strings.Contains(key, RunToken) {
		return fmt.Errorf("mutating Idempotency-Key %q must contain %s so a later run cannot replay this one", key, RunToken)
	}
	return nil
}

// createdName returns the name a create body declares, or "" when the case does
// not create a named object. It is what the loader's uniqueness rule compares.
func (c Case) createdName() string {
	if c.Request.Method != http.MethodPost || len(c.Request.Body) == 0 {
		return ""
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.Request.Body, &body); err != nil {
		return ""
	}
	return strings.TrimSpace(body.Name)
}

// validateCapture checks the capture aliases: they become variables, so they must
// be usable names and must not shadow one the runner owns.
func (c Case) validateCapture() error {
	for alias, path := range c.Capture {
		if !isVariableName(alias) {
			return fmt.Errorf("capture alias %q is not a variable name", alias)
		}
		if reservedVars[alias] {
			return fmt.Errorf("capture alias %q shadows a variable the runner owns", alias)
		}
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("capture %q needs a non-empty dot path", alias)
		}
	}
	return nil
}

// validateExpect checks one expectation block. strict requires exactly one of
// kind and errorCode; a follow block and a stream may omit both.
func validateExpect(e Expect, strict bool, where string) error {
	if e.Status < 100 || e.Status > 599 {
		return fmt.Errorf("%s status must be a 3-digit HTTP status", where)
	}
	for _, key := range e.AbsentKeysAnywhere {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%s: absentKeysAnywhere entries must be non-empty key names", where)
		}
	}
	hasKind := e.Kind != ""
	hasCode := e.ErrorCode != ""
	if hasCode && e.RequestIDInBody != nil {
		// The error path asserts error.requestId itself; a success-only flag on an
		// error case would be accepted and then silently never checked.
		return fmt.Errorf("%s: requestIdInBody applies to a success document, not an error document", where)
	}
	if e.RequestIDPath != "" && (e.RequestIDInBody == nil || !*e.RequestIDInBody) {
		return fmt.Errorf("%s: requestIdPath is only meaningful with requestIdInBody: true", where)
	}

	documentless := e.Status == http.StatusNoContent || e.Status == http.StatusNotModified
	switch {
	case documentless && (hasKind || hasCode):
		return fmt.Errorf("%s: a %d carries no document, so kind and errorCode must be empty", where, e.Status)
	case documentless:
		return nil
	case hasKind && hasCode:
		return fmt.Errorf("%s: exactly one of kind or errorCode must be set", where)
	case strict && !hasKind && !hasCode:
		return fmt.Errorf("%s: exactly one of kind or errorCode must be set", where)
	}
	return nil
}
