package conformance

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

	if _, err := resolveVars("$next", map[string]string{}); !errors.Is(err, errUnsetVar) {
		t.Fatalf("want errUnsetVar for a missing variable, got %v", err)
	}
}

// TestAbsentKeysAnywhereScansEveryDepth is the check the dot paths could not
// make: a payload field nested anywhere must be found, and an absent one must
// stay absent.
func TestAbsentKeysAnywhereScansEveryDepth(t *testing.T) {
	document := decodeBody(t, `{
		"kind": "ConfigList",
		"items": [{"name": "settings", "spec": {"data": "listen 8080"}}],
		"warnings": [{"details": {"data": "nested"}}]
	}`)

	if !containsKey(document, "data") {
		t.Fatal("data is nested under items.0.spec; the scan must find it")
	}
	if containsKey(document, "registryAuth") {
		t.Fatal("registryAuth is not in the body")
	}
	if containsKey(document, "kind") != true {
		t.Fatal("the scan must find a top-level key too")
	}
}

func TestLookupPathWildcard(t *testing.T) {
	body := decodeBody(t, `{
		"kind": "AuditList",
		"items": [
			{"action": "service.update"},
			{"action": "service.create"},
			{"action": "config.create"}
		]
	}`)

	got, ok := lookupPath(body, "items.*.action")
	if !ok || got != "service.update" {
		t.Fatalf("items.*.action = %q, %v", got, ok)
	}
	if _, ok := lookupPath(body, "items.*.missing"); ok {
		t.Fatal("a wildcard must fail when no element carries the path")
	}
	if _, ok := lookupPath(body, "items.*.action.deeper"); ok {
		t.Fatal("a wildcard must not invent a value")
	}
}

// blockingAfter yields data and then never returns. It models a follow stream
// that stays open until the client closes it, which is what the old reader
// waited out.
func blockingAfter(data string) io.Reader {
	return io.MultiReader(strings.NewReader(data), neverReader{block: make(chan struct{})})
}

type neverReader struct{ block chan struct{} }

func (r neverReader) Read([]byte) (int, error) {
	<-r.block
	return 0, io.EOF
}

func TestReadStreamStopsAtTheTargetFrameCount(t *testing.T) {
	body := "event: ready\ndata: {\"type\":\"ready\"}\n\n"
	for i := 0; i < 5; i++ {
		body += "id: 1\nevent: service.created\ndata: {\"type\":\"service.created\",\"cursor\":\"1\"}\n\n"
	}

	done := make(chan streamRead, 1)
	go func() { done <- readStream(blockingAfter(body), 2, "") }()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read: %v", got.err)
		}
		if got.frames != 2 {
			t.Fatalf("frames = %d, want 2", got.frames)
		}
		if got.controls != 1 {
			t.Fatalf("control frames = %d, want the ready frame", got.controls)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readStream waited for a live stream to end instead of stopping at the target")
	}
}

func TestReadStreamStopsAtTheTerminalFrame(t *testing.T) {
	body := `{"type":"stdout","data":"aGVsbG8="}` + "\n" + `{"type":"exit","exitCode":0}` + "\n"

	done := make(chan streamRead, 1)
	go func() { done <- readStream(blockingAfter(body), 5, "exit") }()

	select {
	case got := <-done:
		if got.lastType != "exit" {
			t.Fatalf("last type = %q, want exit", got.lastType)
		}
		if got.frames != 2 {
			t.Fatalf("frames = %d, want the two frames before the terminal one", got.frames)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readStream did not stop at the terminal frame")
	}
}

// TestAssertStreamAcceptsAQuietLabCluster pins the lab tolerance: a live event
// stream on a cluster that changed nothing sends the ready frame and no data,
// and that must not fail the run. The mock leg is where frame content is proven.
func TestAssertStreamAcceptsAQuietLabCluster(t *testing.T) {
	c := Case{Name: "events", Stream: &Stream{Frames: 2}}
	res := &result{
		status:   http.StatusOK,
		header:   http.Header{"Content-Type": []string{eventStreamMediaType}},
		stream:   true,
		controls: 1,
	}

	assertStream(t, c, res, true)
}

// TestAssertStreamAcceptsExtraLabFrames proves the lab count is a floor: an exec
// command or a log tail may emit more frames than the fixture names, as long as
// the terminal frame arrives.
func TestAssertStreamAcceptsExtraLabFrames(t *testing.T) {
	c := Case{Name: "exec", Stream: &Stream{Frames: 2, LastType: "exit"}}
	res := &result{
		status:   http.StatusOK,
		header:   http.Header{"Content-Type": []string{ndjsonMediaType}},
		stream:   true,
		frames:   4,
		lastType: "exit",
	}

	assertStream(t, c, res, true)
}

// TestRunReportsCasesAndSkips is what lets the mock leg fail on a silent skip:
// the summary counts what ran, what did not, and why.
func TestRunReportsCasesAndSkips(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		_, _ = w.Write([]byte(`{"kind":"VersionInfo"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "skips.json", `{
		"schemaVersion": 1,
		"name": "skips",
		"cases": [
			{
				"name": "runs",
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "mock only",
				"mockOnly": true,
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			}
		]
	}`)

	summary := Run(t, Env{
		BaseURL: server.URL,
		Client:  server.Client(),
		Tokens:  map[string]string{"reader": "test-token"},
		Lab:     true,
	}, dir)

	if summary.Cases != 2 || summary.Skipped != 1 {
		t.Fatalf("summary = %+v, want 2 cases and 1 skip", summary)
	}
}

// TestLabRunSkipsACaseWithoutItsVariable proves a task-scoped stream does not
// fail a lab run that has no task id: it skips, exactly like a missing role.
func TestLabRunSkipsACaseWithoutItsVariable(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "streams.json", `{
		"schemaVersion": 1,
		"name": "streams",
		"cases": [{
			"name": "exec",
			"request": {"method": "POST", "path": "/v1/tasks/$task/exec", "role": "reader",
				"body": {"command": ["echo", "hi"]}},
			"expect": {"status": 200},
			"stream": {"frames": 2, "lastType": "exit"}
		}]
	}`)

	summary := Run(t, Env{
		BaseURL: server.URL,
		Client:  server.Client(),
		Tokens:  map[string]string{"reader": "test-token"},
		Lab:     true,
	}, dir)

	if requests != 0 {
		t.Fatalf("a case without its variable must not reach the server; got %d requests", requests)
	}
	if summary.Skipped != 1 {
		t.Fatalf("summary = %+v, want one skip", summary)
	}
}

// TestRunRepeatsARequestAndComparesDocuments is the Idempotency-Key contract: the
// same key and body must replay the first document, byte for byte.
func TestRunRepeatsARequestAndComparesDocuments(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Service","name":"stable","revision":"1"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "write.json", `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [{
			"name": "idempotent create",
			"mutating": true,
			"request": {"method": "POST", "path": "/v1/services", "role": "admin",
				"headers": {"Idempotency-Key": "case-1"},
				"body": {"name": "stable"}},
			"expect": {"status": 201, "kind": "Service", "jsonPaths": {"name": "stable"}},
			"repeat": {"times": 2, "sameBody": true}
		}]
	}`)

	Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"admin": "test-token"},
		AllowMutation: true,
	}, dir)

	if requests != 2 {
		t.Fatalf("requests = %d, want the case to run twice", requests)
	}
}

// TestRepeatMismatch proves sameBody is an assertion, not a hint: a second
// create that produced a new revision must be reported as a mismatch.
func TestRepeatMismatch(t *testing.T) {
	first := &result{status: http.StatusCreated, body: []byte(`{"kind":"Service","revision":"1"}`)}

	if got := repeatMismatch(first, &result{status: http.StatusCreated, body: first.body}); got != "" {
		t.Fatalf("an identical replay reported %q", got)
	}
	if got := repeatMismatch(first, &result{status: http.StatusOK, body: first.body}); got == "" {
		t.Fatal("a different status must be a mismatch")
	}
	if got := repeatMismatch(first, &result{status: http.StatusCreated, body: []byte(`{"kind":"Service","revision":"2"}`)}); got == "" {
		t.Fatal("a different document must be a mismatch")
	}
}

// TestTruncateBodyKeepsValidUTF8 proves the truncated report is never invalid
// text: chopping a multi-byte rune would print a replacement character in a
// failure message, which is exactly where a reader needs the body intact.
func TestTruncateBodyKeepsValidUTF8(t *testing.T) {
	body := []byte(strings.Repeat("é", failureBodyLimit))

	got := truncateBody(body)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateBody returned invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("want a truncation marker, got %q", got[len(got)-32:])
	}
	if len(got) > failureBodyLimit+len("...(truncated)") {
		t.Fatalf("truncated body is %d bytes, want at most %d",
			len(got), failureBodyLimit+len("...(truncated)"))
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
