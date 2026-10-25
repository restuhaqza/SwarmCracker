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
	MockOnly bool    `json:"mockOnly,omitempty"`
	Request  Request `json:"request"`
	Expect   Expect  `json:"expect"`
	// Follow follows one next cursor when Expect.NextPresent is true.
	Follow *Follow `json:"follow,omitempty"`
	// Stream reads an SSE or NDJSON response instead of a JSON document.
	Stream *Stream `json:"stream,omitempty"`
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
	// RequestIDInBody asserts the presence (true) or absence (false) of a
	// top-level requestId in a success document. An error document always echoes
	// the request ID in error.requestId and does not use this flag.
	RequestIDInBody *bool `json:"requestIdInBody,omitempty"`
	// Headers are exact-match assertions on the response.
	Headers map[string]string `json:"headers,omitempty"`
	// JSONPaths are dot paths with numeric array indexes, compared as strings.
	JSONPaths map[string]string `json:"jsonPaths,omitempty"`
	// AbsentKeys are dot paths that must not exist in the body.
	AbsentKeys []string `json:"absentKeys,omitempty"`
	// StreamFrames is an accepted alias for Stream.Frames.
	StreamFrames int `json:"streamFrames,omitempty"`
}

// Follow is the one extra request that walks a next cursor.
type Follow struct {
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Expect Expect            `json:"expect"`
}

// Stream describes a streaming response: how many data frames it must carry
// and, optionally, the type of its last frame.
type Stream struct {
	Frames   int    `json:"frames,omitempty"`
	LastType string `json:"lastType,omitempty"`
}

// expectedFrames is the frame count the case asserts, honouring the
// expect.streamFrames alias.
func (c Case) expectedFrames() int {
	if c.Stream == nil {
		return 0
	}
	if c.Expect.StreamFrames > 0 {
		return c.Expect.StreamFrames
	}
	return c.Stream.Frames
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
	return fixtures, nil
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

	strict := c.Stream == nil
	if err := validateExpect(c.Expect, strict, "expect"); err != nil {
		return err
	}

	if c.Stream != nil {
		if c.expectedFrames() <= 0 {
			return errors.New("stream needs a positive frames count")
		}
	}

	if c.Follow != nil {
		if !strings.HasPrefix(c.Follow.Path, "/v1/") {
			return fmt.Errorf("follow path %q must be an absolute path under /v1/", c.Follow.Path)
		}
		if err := validateExpect(c.Follow.Expect, false, "follow expect"); err != nil {
			return err
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
	hasKind := e.Kind != ""
	hasCode := e.ErrorCode != ""

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
