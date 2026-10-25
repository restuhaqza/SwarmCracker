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

// TestValidateRejectsAnAmbiguousStream pins the two stream shapes: a framed
// stream counts frames, a raw stream reads bytes, and a case picks one.
func TestValidateRejectsAnAmbiguousStream(t *testing.T) {
	stream := func(s Stream) Fixture {
		return Fixture{SchemaVersion: SchemaVersion, Name: "streams", Cases: []Case{{
			Name:    "attach",
			Request: Request{Method: "POST", Path: "/v1/tasks/t/attach", Role: "runner"},
			Expect:  Expect{Status: 200},
			Stream:  &s,
		}}}
	}

	if err := stream(Stream{Bytes: 4}).Validate(); err != nil {
		t.Fatalf("a bytes-only stream must validate: %v", err)
	}
	if err := stream(Stream{Bytes: 4, Frames: 2}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "not both") {
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
}
