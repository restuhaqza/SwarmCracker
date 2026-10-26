package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture drops one fixture file into a temp dir. The loader is the unit
// under test, so the files are written as raw JSON rather than through the
// types: a strict decoder must be shown to reject what it is given.
func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

func loadOne(t *testing.T, body string) ([]Fixture, error) {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, "case.json", body)
	return Load(dir)
}

func TestLoadReadsTheDeclarativeShape(t *testing.T) {
	fixtures, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "read",
		"cases": [{
			"name": "list services page one",
			"mutating": false,
			"mockOnly": true,
			"request": {
				"method": "GET",
				"path": "/v1/services",
				"query": {"limit": "1"},
				"role": "reader",
				"headers": {"If-None-Match": "\"stale\""},
				"body": null
			},
			"expect": {
				"status": 200,
				"kind": "ServiceList",
				"items": 1,
				"nextPresent": true,
				"headers": {"X-SwarmCracker-Api-Version": "1"},
				"jsonPaths": {"items.0.name": "web"},
				"absentKeys": ["registryAuth"]
			},
			"follow": {
				"path": "/v1/services",
				"query": {"cursor": "$next"},
				"expect": {"status": 200, "items": 1}
			},
			"stream": {"frames": 2, "lastType": "exit"}
		}]
	}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("want 1 fixture, got %d", len(fixtures))
	}

	fixture := fixtures[0]
	if fixture.SchemaVersion != SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", fixture.SchemaVersion, SchemaVersion)
	}
	if fixture.Name != "read" {
		t.Errorf("name = %q", fixture.Name)
	}
	if !strings.HasSuffix(fixture.Source, "case.json") {
		t.Errorf("source = %q, want the file it came from", fixture.Source)
	}
	if len(fixture.Cases) != 1 {
		t.Fatalf("want 1 case, got %d", len(fixture.Cases))
	}

	c := fixture.Cases[0]
	if c.Name != "list services page one" {
		t.Errorf("case name = %q", c.Name)
	}
	if !c.MockOnly {
		t.Error("mockOnly was not decoded")
	}
	if c.Request.Method != "GET" || c.Request.Path != "/v1/services" {
		t.Errorf("request = %s %s", c.Request.Method, c.Request.Path)
	}
	if c.Request.Query["limit"] != "1" {
		t.Errorf("query = %v", c.Request.Query)
	}
	if c.Request.Role != "reader" {
		t.Errorf("role = %q", c.Request.Role)
	}
	if c.Expect.Status != 200 || c.Expect.Kind != "ServiceList" {
		t.Errorf("expect = %+v", c.Expect)
	}
	if c.Expect.Items == nil || *c.Expect.Items != 1 {
		t.Errorf("items = %v", c.Expect.Items)
	}
	if c.Expect.NextPresent == nil || !*c.Expect.NextPresent {
		t.Errorf("nextPresent = %v", c.Expect.NextPresent)
	}
	// A stream case reads a documentless body, so the document-only flags must be
	// absent; TestLoadReadsTheNewAssertions covers the echo and its path.
	if c.Expect.RequestIDInBody != nil || c.Expect.RequestIDPath != "" {
		t.Errorf("requestIdInBody = %v, requestIdPath = %q", c.Expect.RequestIDInBody, c.Expect.RequestIDPath)
	}
	if c.Expect.JSONPaths["items.0.name"] != "web" {
		t.Errorf("jsonPaths = %v", c.Expect.JSONPaths)
	}
	if c.Follow == nil || c.Follow.Query["cursor"] != "$next" {
		t.Errorf("follow = %+v", c.Follow)
	}
	if c.Stream == nil || c.Stream.Frames != 2 || c.Stream.LastType != "exit" {
		t.Errorf("stream = %+v", c.Stream)
	}
}

func TestLoadRejectsAnUnknownSchemaVersion(t *testing.T) {
	_, err := loadOne(t, `{"schemaVersion": 2, "name": "future", "cases": []}`)
	if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("want a schemaVersion error, got %v", err)
	}
}

func TestLoadRejectsUnknownJSONKeys(t *testing.T) {
	_, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "read",
		"cases": [{"name": "x", "surprise": true,
			"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
			"expect": {"status": 200, "kind": "VersionInfo"}}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("want the unknown key named, got %v", err)
	}
}

// TestLoadRejectsTheRemovedStreamFramesAlias pins the removal of the alias: the
// fixture format keeps one frame count, stream.frames.
func TestLoadRejectsTheRemovedStreamFramesAlias(t *testing.T) {
	_, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "streams",
		"cases": [{
			"name": "events",
			"request": {"method": "GET", "path": "/v1/events", "role": "reader"},
			"expect": {"status": 200, "streamFrames": 2}
		}]
	}`)
	if err == nil || !strings.Contains(err.Error(), "streamFrames") {
		t.Fatalf("expect.streamFrames was removed; want the loader to reject it, got %v", err)
	}
}

func TestLoadRejectsTrailingDocuments(t *testing.T) {
	_, err := loadOne(t, `{"schemaVersion": 1, "name": "a", "cases": []} {"schemaVersion": 1}`)
	if err == nil {
		t.Fatal("want an error for a second JSON document")
	}
}

func TestValidateRejectsBadFixtures(t *testing.T) {
	base := func(mutate func(*Fixture)) Fixture {
		f := Fixture{
			SchemaVersion: SchemaVersion,
			Name:          "read",
			Cases: []Case{{
				Name:    "ok",
				Request: Request{Method: "GET", Path: "/v1/version", Role: "reader"},
				Expect:  Expect{Status: 200, Kind: "VersionInfo"},
			}},
		}
		mutate(&f)
		return f
	}

	tests := []struct {
		name string
		f    Fixture
		want string
	}{
		{
			name: "duplicate case names",
			f: base(func(f *Fixture) {
				f.Cases = append(f.Cases, f.Cases[0])
			}),
			want: "duplicate case name",
		},
		{
			name: "unknown role",
			f: base(func(f *Fixture) {
				f.Cases[0].Request.Role = "operator"
			}),
			want: "unknown role",
		},
		{
			name: "relative path",
			f: base(func(f *Fixture) {
				f.Cases[0].Request.Path = "v1/version"
			}),
			want: "absolute path",
		},
		{
			name: "path outside v1",
			f: base(func(f *Fixture) {
				f.Cases[0].Request.Path = "/healthz"
			}),
			want: "absolute path",
		},
		{
			name: "missing method",
			f: base(func(f *Fixture) {
				f.Cases[0].Request.Method = ""
			}),
			want: "method",
		},
		{
			name: "unknown method",
			f: base(func(f *Fixture) {
				f.Cases[0].Request.Method = "TRACE"
			}),
			want: "method",
		},
		{
			name: "both expectation kinds",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.ErrorCode = "not_found"
			}),
			want: "exactly one",
		},
		{
			name: "no expectation kind",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.Kind = ""
			}),
			want: "exactly one",
		},
		{
			name: "missing status",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.Status = 0
			}),
			want: "status",
		},
		{
			name: "follow without a path",
			f: base(func(f *Fixture) {
				f.Cases[0].Follow = &Follow{Expect: Expect{Status: 200}}
			}),
			want: "follow",
		},
		{
			name: "follow with two expectation kinds",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.NextPresent = boolPtr(true)
				f.Cases[0].Follow = &Follow{
					Path:   "/v1/services",
					Expect: Expect{Status: 200, Kind: "ServiceList", ErrorCode: "invalid"},
				}
			}),
			want: "exactly one",
		},
		{
			name: "follow without nextPresent",
			f: base(func(f *Fixture) {
				f.Cases[0].Follow = &Follow{
					Path:   "/v1/services",
					Expect: Expect{Status: 200, Kind: "ServiceList"},
				}
			}),
			want: "nextPresent",
		},
		{
			name: "follow with nextPresent false",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.NextPresent = boolPtr(false)
				f.Cases[0].Follow = &Follow{
					Path:   "/v1/services",
					Expect: Expect{Status: 200, Kind: "ServiceList"},
				}
			}),
			want: "nextPresent",
		},
		{
			name: "repeat with one run",
			f: base(func(f *Fixture) {
				f.Cases[0].Repeat = &Repeat{Times: 1, SameBody: true}
			}),
			want: "repeat",
		},
		{
			name: "repeat on a stream",
			f: base(func(f *Fixture) {
				f.Cases[0].Stream = &Stream{Frames: 2}
				f.Cases[0].Repeat = &Repeat{Times: 2, SameBody: true}
			}),
			want: "repeat",
		},
		{
			name: "empty absent key anywhere",
			f: base(func(f *Fixture) {
				f.Cases[0].Expect.AbsentKeysAnywhere = []string{" "}
			}),
			want: "absentKeysAnywhere",
		},
		{
			name: "stream without frames",
			f: base(func(f *Fixture) {
				f.Cases[0].Stream = &Stream{}
			}),
			want: "stream",
		},
		{
			name: "empty case name",
			f: base(func(f *Fixture) {
				f.Cases[0].Name = ""
			}),
			want: "name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.f.Validate()
			if err == nil {
				t.Fatalf("want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestLoadReadsRawBodies keeps the raw file round-trip in the declarative
// format: a base64 request body in, a base64 response body plus an exact length
// out, so an SDK can send and check bytes that JSON cannot carry.
func TestLoadReadsRawBodies(t *testing.T) {
	fixtures, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [
			{
				"name": "upload bytes",
				"mutating": true,
				"request": {"method": "PUT", "path": "/v1/tasks/t/files", "role": "admin",
					"bodyBase64": "AAH/"},
				"expect": {"status": 204}
			},
			{
				"name": "download bytes",
				"request": {"method": "GET", "path": "/v1/tasks/t/files", "role": "admin"},
				"expect": {"status": 200, "bodyBase64": "AAH/", "bodyLength": 3}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	upload := fixtures[0].Cases[0]
	if upload.Request.BodyBase64 != "AAH/" {
		t.Errorf("request.bodyBase64 = %q", upload.Request.BodyBase64)
	}
	if len(upload.Request.Body) != 0 {
		t.Errorf("request.body = %s, want no JSON body", upload.Request.Body)
	}

	download := fixtures[0].Cases[1]
	if download.Expect.BodyBase64 != "AAH/" {
		t.Errorf("expect.bodyBase64 = %q", download.Expect.BodyBase64)
	}
	if download.Expect.BodyLength == nil || *download.Expect.BodyLength != 3 {
		t.Errorf("expect.bodyLength = %v", download.Expect.BodyLength)
	}
}

// TestValidateRejectsContradictoryRawBodies is the raw-body rule in one table.
// Every combination in which the fields would be silently half-checked is
// refused, and the loader names the case so the offending fixture is obvious.
func TestValidateRejectsContradictoryRawBodies(t *testing.T) {
	base := func(c Case) Fixture {
		return Fixture{SchemaVersion: SchemaVersion, Name: "write", Cases: []Case{c}}
	}
	ok := Case{
		Name:    "raw file",
		Request: Request{Method: "PUT", Path: "/v1/tasks/t/files", Role: "admin"},
		Expect:  Expect{Status: 204},
	}

	tests := []struct {
		name  string
		setup func(*Case)
		want  string
	}{
		{
			name: "a JSON body and a raw body",
			setup: func(c *Case) {
				c.Request.Body = []byte(`{"name":"x"}`)
				c.Request.BodyBase64 = "eA=="
				c.Expect.Status = 204
			},
			want: "mutually exclusive",
		},
		{
			name: "a body declared null and a raw body",
			setup: func(c *Case) {
				c.Request.Body = []byte("null")
				c.Request.BodyBase64 = "eA=="
				c.Expect.Status = 204
			},
			want: "mutually exclusive",
		},
		{
			name: "invalid base64 in the request",
			setup: func(c *Case) {
				c.Request.BodyBase64 = "not base64!"
				c.Expect.Status = 204
			},
			want: "not valid base64",
		},
		{
			name: "a raw body assertion on a stream case",
			setup: func(c *Case) {
				c.Stream = &Stream{Frames: 2}
				c.Expect = Expect{Status: 200, BodyBase64: "eA=="}
			},
			want: "raw response",
		},
		{
			name: "a raw body assertion on a follow",
			setup: func(c *Case) {
				c.Expect.NextPresent = boolPtr(true)
				c.Follow = &Follow{
					Path:   "/v1/services",
					Expect: Expect{Status: 200, BodyLength: intPtr(3)},
				}
			},
			want: "follow",
		},
		{
			name: "a raw body assertion with a document kind",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 200, Kind: "Snapshot", BodyBase64: "eA=="}
			},
			want: "kind and errorCode must be empty",
		},
		{
			name: "a raw body assertion with an error code",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 409, ErrorCode: "conflict", BodyLength: intPtr(3)}
			},
			want: "kind and errorCode must be empty",
		},
		{
			name: "a raw body assertion with a document assertion",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 200, BodyBase64: "eA==", JSONPaths: map[string]string{"kind": "Snapshot"}}
			},
			want: "only assertions a raw response supports",
		},
		{
			name: "invalid base64 in the expectation",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 200, BodyBase64: "eA"}
			},
			want: "not valid base64",
		},
		{
			name: "a negative body length",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 200, BodyLength: intPtr(-1)}
			},
			want: "must not be negative",
		},
		{
			name: "a raw body assertion on a documentless status",
			setup: func(c *Case) {
				c.Expect = Expect{Status: 204, BodyLength: intPtr(0)}
			},
			want: "carries no body",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.setup(&c)
			err := base(c).Validate()
			if err == nil {
				t.Fatalf("want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			// A validation error has to name the case: a fixture directory is
			// large, and "expect: ..." alone does not say which one is wrong.
			if !strings.Contains(err.Error(), c.Name) {
				t.Fatalf("error %q does not name the case %q", err, c.Name)
			}
		})
	}
}

func TestValidateAcceptsADocumentless204(t *testing.T) {
	f := Fixture{
		SchemaVersion: SchemaVersion,
		Name:          "write",
		Cases: []Case{{
			Name:    "delete",
			Request: Request{Method: "DELETE", Path: "/v1/services/web", Role: "admin"},
			Expect:  Expect{Status: 204},
		}},
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("a 204 carries no document and needs no kind: %v", err)
	}
}

func boolPtr(value bool) *bool { return &value }

func intPtr(value int) *int { return &value }

// TestValidateRejectsForbiddenHeaders keeps the runner the only owner of the
// credential and of the message framing: a fixture may not set either.
func TestValidateRejectsForbiddenHeaders(t *testing.T) {
	headers := []string{
		"Authorization", "authorization", "Cookie", "Proxy-Authorization",
		"Connection", "Transfer-Encoding", "Upgrade",
	}

	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			f := Fixture{
				SchemaVersion: SchemaVersion,
				Name:          "read",
				Cases: []Case{{
					Name: "ok",
					Request: Request{
						Method:  "GET",
						Path:    "/v1/version",
						Role:    "reader",
						Headers: map[string]string{header: "x"},
					},
					Expect: Expect{Status: 200, Kind: "VersionInfo"},
				}},
			}
			err := f.Validate()
			if err == nil || !strings.Contains(err.Error(), header) {
				t.Fatalf("want %q rejected, got %v", header, err)
			}
		})
	}
}

// TestLoadReadsRepeatAndAbsentKeysAnywhere keeps the two new assertions in the
// declarative format rather than in a Go-only helper.
func TestLoadReadsRepeatAndAbsentKeysAnywhere(t *testing.T) {
	fixtures, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [{
			"name": "idempotent create",
			"mutating": true,
			"request": {
				"method": "POST",
				"path": "/v1/services",
				"role": "admin",
				"headers": {"Idempotency-Key": "case-1-{{run}}"},
				"body": {"name": "x-{{run}}", "spec": {"task": {"image": "nginx:alpine"}}}
			},
			"expect": {
				"status": 201,
				"kind": "Service",
				"absentKeysAnywhere": ["registryAuth"]
			},
			"repeat": {"times": 2, "sameBody": true}
		}]
	}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	c := fixtures[0].Cases[0]
	if c.Repeat == nil || c.Repeat.Times != 2 || !c.Repeat.SameBody {
		t.Errorf("repeat = %+v", c.Repeat)
	}
	if len(c.Expect.AbsentKeysAnywhere) != 1 || c.Expect.AbsentKeysAnywhere[0] != "registryAuth" {
		t.Errorf("absentKeysAnywhere = %v", c.Expect.AbsentKeysAnywhere)
	}
}

// TestShippedFixturesLoad keeps the fixture directory itself honest: every
// committed file parses, validates and still covers the four documented groups.
func TestShippedFixturesLoad(t *testing.T) {
	fixtures, err := Load("fixtures")
	if err != nil {
		t.Fatalf("load shipped fixtures: %v", err)
	}

	names := make(map[string]bool, len(fixtures))
	cases := 0
	for _, fixture := range fixtures {
		names[fixture.Name] = true
		cases += len(fixture.Cases)
	}
	for _, want := range []string{"read", "errors", "write", "streams"} {
		if !names[want] {
			t.Errorf("shipped fixtures are missing %q (have %v)", want, names)
		}
	}
	if cases < 20 {
		t.Errorf("shipped fixtures define %d cases; want at least 20", cases)
	}
}

// TestValidateRequiresTheRunToken proves the per-run rule: a mutating create name
// and a mutating Idempotency-Key are what a second run would collide on, so both
// must carry the token the runner replaces.
func TestValidateRequiresTheRunToken(t *testing.T) {
	create := func(name, key string) Fixture {
		headers := map[string]string{}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		return Fixture{
			SchemaVersion: SchemaVersion,
			Name:          "write",
			Cases: []Case{{
				Name:     "create",
				Mutating: true,
				Request: Request{
					Method:  "POST",
					Path:    "/v1/services",
					Role:    "admin",
					Headers: headers,
					Body:    []byte(`{"name":"` + name + `"}`),
				},
				Expect: Expect{Status: 201, Kind: "Service"},
			}},
		}
	}

	if err := create("conformance-ws-"+RunToken, "").Validate(); err != nil {
		t.Fatalf("a name with the run token must validate: %v", err)
	}
	if err := create("conformance-ws", "").Validate(); err == nil ||
		!strings.Contains(err.Error(), RunToken) {
		t.Fatalf("a literal mutating name must be rejected, got %v", err)
	}
	if err := create("conformance-ws-"+RunToken, "conformance-idem").Validate(); err == nil ||
		!strings.Contains(err.Error(), "Idempotency-Key") {
		t.Fatalf("a literal mutating Idempotency-Key must be rejected, got %v", err)
	}
	if err := create("conformance-ws-"+RunToken, "conformance-idem-"+RunToken).Validate(); err != nil {
		t.Fatalf("a suffixed Idempotency-Key must validate: %v", err)
	}
}

// TestLoadRejectsADuplicateCreateName is the cross-file half of the run rule: one
// run cannot create the same name twice, so the loader must find the collision
// before any request is sent.
func TestLoadRejectsADuplicateCreateName(t *testing.T) {
	dir := t.TempDir()
	body := `{
		"schemaVersion": 1,
		"name": "%s",
		"cases": [{
			"name": "create",
			"mutating": true,
			"request": {"method": "POST", "path": "/v1/services", "role": "admin",
				"body": {"name": "conformance-shared-%s"}},
			"expect": {"status": 201, "kind": "Service"}
		}]
	}`
	writeFixture(t, dir, "a.json", fmt.Sprintf(body, "a", RunToken))
	writeFixture(t, dir, "b.json", fmt.Sprintf(body, "b", RunToken))

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "created twice") {
		t.Fatalf("want the duplicate name reported, got %v", err)
	}
}

// TestValidateRejectsBadCapabilityGates keeps the gate deterministic: a case that
// both requires and forbids one capability could never run on any target.
func TestValidateRejectsBadCapabilityGates(t *testing.T) {
	base := func(c *Case) Fixture {
		return Fixture{SchemaVersion: SchemaVersion, Name: "read", Cases: []Case{*c}}
	}
	ok := Case{
		Name:    "gated",
		Request: Request{Method: "GET", Path: "/v1/version", Role: "reader"},
		Expect:  Expect{Status: 200, Kind: "VersionInfo"},
	}

	both := ok
	both.RequiresCapabilities = []string{"snapshots"}
	both.AbsentCapabilities = []string{"snapshots"}
	if err := base(&both).Validate(); err == nil || !strings.Contains(err.Error(), "both required and absent") {
		t.Fatalf("want the contradiction reported, got %v", err)
	}

	blank := ok
	blank.RequiresCapabilities = []string{" "}
	if err := base(&blank).Validate(); err == nil || !strings.Contains(err.Error(), "requiresCapabilities") {
		t.Fatalf("want a blank capability rejected, got %v", err)
	}
}

// TestValidateRejectsBadCaptures keeps a capture from naming a variable the
// runner owns or a name no path can reference.
func TestValidateRejectsBadCaptures(t *testing.T) {
	for _, tc := range []struct{ alias, want string }{
		{alias: "etag", want: "shadows"},
		{alias: "bad-alias", want: "not a variable name"},
		{alias: "snapshot", want: "non-empty dot path"},
	} {
		c := Case{
			Name:    "capture",
			Request: Request{Method: "POST", Path: "/v1/services", Role: "admin"},
			Expect:  Expect{Status: 201, Kind: "Service"},
			Capture: map[string]string{tc.alias: "id"},
		}
		if tc.alias == "snapshot" {
			c.Capture[tc.alias] = " "
		}
		f := Fixture{SchemaVersion: SchemaVersion, Name: "write", Cases: []Case{c}}
		if err := f.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("capture %q: want %q, got %v", tc.alias, tc.want, err)
		}
	}
}

// streamFixture wraps one stream shape in an otherwise valid stream case, so a
// validation test only has to name the shape under test.
func streamFixture(s Stream) Fixture {
	return Fixture{SchemaVersion: SchemaVersion, Name: "streams", Cases: []Case{{
		Name:    "attach",
		Request: Request{Method: "POST", Path: "/v1/tasks/t/attach", Role: "runner"},
		Expect:  Expect{Status: 200},
		Stream:  &s,
	}}}
}

// TestValidateRejectsAnAmbiguousStream pins the two stream shapes: a framed
// stream counts frames, a raw stream reads bytes, and a case picks one.
func TestValidateRejectsAnAmbiguousStream(t *testing.T) {
	stream := streamFixture

	if err := stream(Stream{Bytes: 4}).Validate(); err != nil {
		t.Fatalf("a bytes-only stream must validate: %v", err)
	}
	if err := stream(Stream{Bytes: 4, Frames: 2}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("want the mixed stream rejected, got %v", err)
	}
	if err := stream(Stream{}).Validate(); err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("want a shapeless stream rejected, got %v", err)
	}

	echoing := stream(Stream{Frames: 2})
	echoing.Cases[0].Expect.RequestIDInBody = boolPtr(true)
	if err := echoing.Validate(); err == nil || !strings.Contains(err.Error(), "not a stream") {
		t.Fatalf("want requestIdInBody on a stream rejected, got %v", err)
	}
}

// TestValidateBoundedStreamShape pins the untilType shape: a bounded read needs a
// floor and a cap so it can neither pass on an empty stream nor hang on a server
// that never sends the terminal frame, and it replaces the pinned count instead
// of adding to it.
func TestValidateBoundedStreamShape(t *testing.T) {
	if err := streamFixture(Stream{UntilType: "eof", MinFrames: 1, MaxFrames: 200}).Validate(); err != nil {
		t.Fatalf("a bounded stream must validate: %v", err)
	}

	tests := []struct {
		name   string
		stream Stream
		want   string
	}{
		{name: "no floor", stream: Stream{UntilType: "eof", MaxFrames: 200}, want: "minFrames"},
		{name: "no cap", stream: Stream{UntilType: "eof", MinFrames: 1}, want: "maxFrames"},
		{name: "cap at the floor", stream: Stream{UntilType: "eof", MinFrames: 4, MaxFrames: 4}, want: "must exceed"},
		{name: "count and terminal", stream: Stream{Frames: 2, UntilType: "eof", MinFrames: 1, MaxFrames: 9}, want: "exactly one"},
		{name: "lastType and untilType", stream: Stream{LastType: "eof", UntilType: "eof", MinFrames: 1, MaxFrames: 9}, want: "name one"},
		{name: "bounds on an exact stream", stream: Stream{Frames: 2, MinFrames: 1, MaxFrames: 9}, want: "require untilType"},
		{name: "bounds on a raw stream", stream: Stream{Bytes: 4, MinFrames: 1}, want: "not a raw one"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := streamFixture(tc.stream).Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// TestValidateRejectsAContradictoryExpectation covers the three new expectation
// rules that would otherwise be silently ignored.
func TestValidateRejectsAContradictoryExpectation(t *testing.T) {
	base := func(expect Expect) Fixture {
		return Fixture{SchemaVersion: SchemaVersion, Name: "read", Cases: []Case{{
			Name:    "case",
			Request: Request{Method: "GET", Path: "/v1/version", Role: "reader"},
			Expect:  expect,
		}}}
	}

	errorWithEcho := Expect{Status: 404, ErrorCode: "not_found", RequestIDInBody: boolPtr(false)}
	if err := base(errorWithEcho).Validate(); err == nil || !strings.Contains(err.Error(), "success document") {
		t.Fatalf("want requestIdInBody on an error case rejected, got %v", err)
	}

	pathWithoutFlag := Expect{Status: 200, Kind: "VersionInfo", RequestIDPath: "requestId"}
	if err := base(pathWithoutFlag).Validate(); err == nil || !strings.Contains(err.Error(), "requestIdPath") {
		t.Fatalf("want requestIdPath without the flag rejected, got %v", err)
	}

	both := Expect{Status: 200, Kind: "VersionInfo", PresentKeys: []string{"id"}, AbsentKeys: []string{"id"}}
	if err := base(both).Validate(); err == nil || !strings.Contains(err.Error(), "presentKeys") {
		t.Fatalf("want a path in both lists rejected, got %v", err)
	}
}

// TestLoadReadsTheNewAssertions keeps the capability gates, the capture, the
// presence rules and the raw stream in the declarative format rather than in a
// Go-only helper.
func TestLoadReadsTheNewAssertions(t *testing.T) {
	fixtures, err := loadOne(t, `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [
			{
				"name": "snapshot",
				"mutating": true,
				"requiresCapabilities": ["snapshots"],
				"absentCapabilities": ["attach"],
				"request": {"method": "POST", "path": "/v1/tasks/t/snapshots", "role": "admin"},
				"expect": {"status": 201, "kind": "Snapshot",
					"requestIdInBody": true, "requestIdPath": "partial.0.error.requestId",
					"presentKeys": ["id", "rootfsPath"], "minValues": {"sizeBytes": 1}},
				"capture": {"snapshot": "id"}
			},
			{
				"name": "attach",
				"requiresCapabilities": ["attach"],
				"request": {"method": "POST", "path": "/v1/tasks/t/attach", "role": "runner"},
				"expect": {"status": 200},
				"stream": {"bytes": 14}
			},
			{
				"name": "logs",
				"requiresCapabilities": ["logs"],
				"request": {"method": "GET", "path": "/v1/tasks/t/logs", "role": "reader"},
				"expect": {"status": 200},
				"stream": {"untilType": "eof", "minFrames": 1, "maxFrames": 200}
			}
		]
	}`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	first := fixtures[0].Cases[0]
	if len(first.RequiresCapabilities) != 1 || first.RequiresCapabilities[0] != "snapshots" {
		t.Errorf("requiresCapabilities = %v", first.RequiresCapabilities)
	}
	if len(first.AbsentCapabilities) != 1 || first.AbsentCapabilities[0] != "attach" {
		t.Errorf("absentCapabilities = %v", first.AbsentCapabilities)
	}
	if first.Expect.RequestIDInBody == nil || !*first.Expect.RequestIDInBody {
		t.Errorf("requestIdInBody = %v", first.Expect.RequestIDInBody)
	}
	if first.Expect.RequestIDPath != "partial.0.error.requestId" {
		t.Errorf("requestIdPath = %q", first.Expect.RequestIDPath)
	}
	if len(first.Expect.PresentKeys) != 2 || first.Expect.MinValues["sizeBytes"] != 1 {
		t.Errorf("presentKeys = %v, minValues = %v", first.Expect.PresentKeys, first.Expect.MinValues)
	}
	if first.Capture["snapshot"] != "id" {
		t.Errorf("capture = %v", first.Capture)
	}
	if second := fixtures[0].Cases[1]; second.Stream == nil || second.Stream.Bytes != 14 {
		t.Errorf("stream = %+v", fixtures[0].Cases[1].Stream)
	}
	bounded := fixtures[0].Cases[2].Stream
	if bounded == nil || bounded.UntilType != "eof" || bounded.MinFrames != 1 || bounded.MaxFrames != 200 {
		t.Errorf("bounded stream = %+v", bounded)
	}
}
