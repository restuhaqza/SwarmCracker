package conformance

import (
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
				"requestIdInBody": false,
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
	if c.Expect.RequestIDInBody == nil || *c.Expect.RequestIDInBody {
		t.Errorf("requestIdInBody = %v", c.Expect.RequestIDInBody)
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
				f.Cases[0].Follow = &Follow{
					Path:   "/v1/services",
					Expect: Expect{Status: 200, Kind: "ServiceList", ErrorCode: "invalid"},
				}
			}),
			want: "exactly one",
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
