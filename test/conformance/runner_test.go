package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, raw string) any {
	t.Helper()
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return value
}

func TestLookupPath(t *testing.T) {
	body := decodeBody(t, `{
		"kind": "ServiceList",
		"items": [{"name": "web", "replicas": 3, "labels": {"team": "payments"}}],
		"next": null
	}`)

	tests := []struct {
		path    string
		want    string
		present bool
	}{
		{path: "kind", want: "ServiceList", present: true},
		{path: "items.0.name", want: "web", present: true},
		{path: "items.0.replicas", want: "3", present: true},
		{path: "items.0.labels.team", want: "payments", present: true},
		{path: "items.1.name", present: false},
		{path: "items.0.missing", present: false},
		{path: "nope", present: false},
		{path: "next", want: "null", present: true},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got, ok := lookupPath(body, tc.path)
			if ok != tc.present {
				t.Fatalf("present = %v, want %v", ok, tc.present)
			}
			if ok && got != tc.want {
				t.Fatalf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveVars(t *testing.T) {
	vars := map[string]string{"next": "abc", "etag": `"2"`}

	for _, literal := range []string{`"stale"`, "prefix-$etag", "${etag}"} {
		got, err := resolveVars(literal, vars)
		if err != nil {
			t.Fatalf("resolve %q: %v", literal, err)
		}
		if got != literal {
			t.Fatalf("only a whole-value reference is a variable: %q became %q", literal, got)
		}
	}

	got, err := resolveVars("$etag", vars)
	if err != nil || got != `"2"` {
		t.Fatalf("$etag = %q, %v", got, err)
	}

	if _, err := resolveVars("$next", map[string]string{}); err == nil {
		t.Fatal("want an error for a missing variable")
	}
}

// TestRunExecutesFixtures is the runner's own contract test: a real in-process
// HTTP server, one fixture file, and every assertion path the mock suite uses
// (status, kind, paths, absence, pagination and a follow request).
func TestRunExecutesFixtures(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		_, _ = w.Write([]byte(`{"kind":"VersionInfo","apiVersions":["v1"],"capabilities":["read"]}`))
	})
	mux.HandleFunc("GET /v1/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		if r.URL.Query().Get("cursor") == "page-2" {
			_, _ = w.Write([]byte(`{"kind":"ServiceList","items":[{"name":"api"}],"next":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"ServiceList","items":[{"name":"web"}],"next":"page-2"}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "read.json", `{
		"schemaVersion": 1,
		"name": "read",
		"cases": [
			{
				"name": "version",
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo",
					"requestIdInBody": false,
					"jsonPaths": {"kind": "VersionInfo", "capabilities.0": "read"}}
			},
			{
				"name": "page one",
				"request": {"method": "GET", "path": "/v1/services", "role": "reader"},
				"expect": {"status": 200, "kind": "ServiceList", "items": 1,
					"nextPresent": true, "jsonPaths": {"items.0.name": "web"},
					"absentKeys": ["items.0.spec"]},
				"follow": {"path": "/v1/services", "query": {"cursor": "$next"},
					"expect": {"status": 200, "items": 1, "jsonPaths": {"items.0.name": "api"}}}
			}
		]
	}`)

	Run(t, Env{
		BaseURL: server.URL,
		Client:  server.Client(),
		Tokens:  map[string]string{"reader": "test-token"},
	}, dir)
}

func TestRunRejectsAnEmptyFixtureDirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty fixture directory must fail loudly, not run zero cases")
	}
}

// TestLabSkipPolicy proves the three lab skip rules before any request is sent:
// mock-only cases, mutating cases without CONFORMANCE_ALLOW_MUTATION, and a role
// whose token is missing. The server answers 500, so a request that is not
// skipped fails its assertion.
func TestLabSkipPolicy(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "skips.json", `{
		"schemaVersion": 1,
		"name": "skips",
		"cases": [
			{
				"name": "mock only",
				"mockOnly": true,
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "mutating",
				"mutating": true,
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "no admin token",
				"request": {"method": "GET", "path": "/v1/version", "role": "admin"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			}
		]
	}`)

	Run(t, Env{
		BaseURL: server.URL,
		Client:  server.Client(),
		Tokens:  map[string]string{"reader": "test-token"},
		Lab:     true,
	}, dir)

	if requests != 0 {
		t.Fatalf("a skipped case must not reach the server; got %d requests", requests)
	}
}
