package conformance

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	go func() { done <- readStream(blockingAfter(body), Stream{Frames: 2}) }()

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
	go func() { done <- readStream(blockingAfter(body), Stream{Frames: 5, LastType: "exit"}) }()

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

// logStreamBody builds an NDJSON console log: count log frames, then the eof
// frame when eof is true. A real console log has as many lines as the guest
// printed, which is what the fixture must not pin.
func logStreamBody(count int, eof bool) string {
	var body strings.Builder
	for i := 0; i < count; i++ {
		body.WriteString(`{"type":"log","stream":"stdout","data":"YQ=="}`)
		body.WriteString("\n")
	}
	if eof {
		body.WriteString(`{"type":"eof"}`)
		body.WriteString("\n")
	}
	return body.String()
}

// TestReadStreamReadsAVariableLogToItsTerminalFrame is the first real lab run's
// finding: the task's console carried two log frames plus eof, so the fixture's
// pinned `frames: 2` stopped before the terminal frame and failed a correct
// server. untilType reads every line and stops at eof, not at a count.
func TestReadStreamReadsAVariableLogToItsTerminalFrame(t *testing.T) {
	spec := Stream{UntilType: "eof", MinFrames: 1, MaxFrames: 20}

	done := make(chan streamRead, 1)
	go func() { done <- readStream(blockingAfter(logStreamBody(5, true)), spec) }()

	select {
	case got := <-done:
		if got.lastType != "eof" {
			t.Fatalf("last type = %q, want eof after every log line", got.lastType)
		}
		if got.payload != 5 {
			t.Fatalf("payload frames = %d, want the five lines before eof", got.payload)
		}
		passed := &result{stream: true, lastType: got.lastType, payload: got.payload}
		if err := spec.boundedProblem(passed); err != nil {
			t.Fatalf("a variable-length log must satisfy the bounded stream: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a bounded read waited for a live stream to end instead of stopping at eof")
	}
}

// TestBoundedStreamFailsWithoutItsTerminalFrame pins the other half of the shape:
// a stream that keeps emitting and never sends eof stops at the cap and fails,
// instead of passing on whatever frames happened to arrive.
func TestBoundedStreamFailsWithoutItsTerminalFrame(t *testing.T) {
	spec := Stream{UntilType: "eof", MinFrames: 1, MaxFrames: 6}

	done := make(chan streamRead, 1)
	go func() { done <- readStream(blockingAfter(logStreamBody(20, false)), spec) }()

	var got streamRead
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a bounded read must stop at its frame cap instead of waiting for an eof that never comes")
	}
	if got.frames != spec.MaxFrames {
		t.Fatalf("frames = %d, want the cap %d", got.frames, spec.MaxFrames)
	}

	err := spec.boundedProblem(&result{stream: true, lastType: got.lastType, payload: got.payload})
	if err == nil {
		t.Fatal("a stream without its terminal frame must fail")
	}
	if !strings.Contains(err.Error(), `"eof"`) {
		t.Fatalf("the failure must name the missing terminal frame, got %v", err)
	}
}

// TestBoundedStreamNeedsAPayloadFrame keeps minFrames honest: an eof on a stream
// that carried no line is not a log tail.
func TestBoundedStreamNeedsAPayloadFrame(t *testing.T) {
	spec := Stream{UntilType: "eof", MinFrames: 1, MaxFrames: 5}
	got := readStream(strings.NewReader(logStreamBody(0, true)), spec)

	if got.lastType != "eof" {
		t.Fatalf("last type = %q, want eof", got.lastType)
	}
	err := spec.boundedProblem(&result{stream: true, lastType: got.lastType, payload: got.payload})
	if err == nil {
		t.Fatal("eof alone must not satisfy minFrames")
	}
	if !strings.Contains(err.Error(), "at least 1 payload frames") {
		t.Fatalf("the failure must name the missing payload frames, got %v", err)
	}
}

// TestPinnedFrameCountStopsBeforeEof is why the logs fixture moved to untilType:
// read with the old `frames: 2` spec, a two-line console log stops on the second
// line, so lastType is "log" and the `lastType: "eof"` assertion cannot hold. The
// mock's one-line log hid this; the first real task did not.
func TestPinnedFrameCountStopsBeforeEof(t *testing.T) {
	read := readStream(strings.NewReader(logStreamBody(2, true)), Stream{Frames: 2, LastType: "eof"})

	if read.lastType != "log" {
		t.Fatalf("last type = %q, want the second log line (the old fixture's failure)", read.lastType)
	}
	if read.frames != 2 {
		t.Fatalf("frames = %d, want the pinned 2", read.frames)
	}
}

// TestAssertStreamAcceptsAVariableLogLength pins the fixture fix at the
// assertion level, in both legs: two log frames then eof pass with untilType,
// where the old `frames: 2` + `lastType: eof` fixture failed on that server.
func TestAssertStreamAcceptsAVariableLogLength(t *testing.T) {
	c := Case{Name: "logs", Stream: &Stream{UntilType: "eof", MinFrames: 1, MaxFrames: 20}}

	for _, lab := range []bool{false, true} {
		res := &result{
			status:   http.StatusOK,
			header:   http.Header{"Content-Type": []string{ndjsonMediaType}},
			stream:   true,
			frames:   3,
			payload:  2,
			lastType: "eof",
		}
		assertStream(t, c, res, lab)
	}
}

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Service","name":"` + body.Name + `","revision":"1"}`))
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
				"headers": {"Idempotency-Key": "case-1-{{run}}"},
				"body": {"name": "stable-{{run}}"}},
			"expect": {"status": 201, "kind": "Service", "jsonPaths": {"name": "stable-{{run}}"}},
			"repeat": {"times": 2, "sameBody": true}
		}]
	}`)

	Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"admin": "test-token"},
		AllowMutation: true,
		RunSuffix:     "20260101T000000Z-abcdef01",
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

// TestTruncateBodyQuotesABinaryBody pins the report for a raw response: a file
// body is not text, so it is rendered as a quoted string instead of turning the
// failure log into a binary blob.
func TestTruncateBodyQuotesABinaryBody(t *testing.T) {
	body := []byte("conformance file\n\x00\x01\xff")
	got := truncateBody(body)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateBody returned invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, `"`) || !strings.Contains(got, `\xff`) {
		t.Fatalf("want a quoted rendering with the last byte escaped, got %q", got)
	}

	huge := []byte(strings.Repeat("\xff", failureBodyLimit))
	got = truncateBody(huge)
	if !utf8.ValidString(got) {
		t.Fatalf("a huge binary body must still be reported as text: %q", got)
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("want a truncation marker, got %q", got[len(got)-32:])
	}
	if len(got) > failureBodyLimit+len("...(truncated)")+2 {
		t.Fatalf("quoted body is %d bytes, want at most %d", len(got), failureBodyLimit+len("...(truncated)")+2)
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

// TestRunSendsAndAssertsRawBodies runs the file round-trip in a fixture: the PUT
// body is the decoded bytes with the octet-stream default content type, and the
// GET assertion compares the response to the same bytes exactly.
func TestRunSendsAndAssertsRawBodies(t *testing.T) {
	payload := []byte("conformance file\n\x00\x01\xff")
	encoded := base64.StdEncoding.EncodeToString(payload)

	var gotContentType string
	var gotBody []byte

	mux := http.NewServeMux()
	writeHeaders := func(w http.ResponseWriter) {
		w.Header().Set(apiVersionHeader, APIVersion)
		w.Header().Set(requestIDHeader, "req_test")
	}
	mux.HandleFunc("PUT /v1/tasks/task-a/files", func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		writeHeaders(w)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/tasks/task-a/files", func(w http.ResponseWriter, _ *http.Request) {
		writeHeaders(w)
		w.Header().Set("Content-Type", octetStreamMediaType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "write.json", fmt.Sprintf(`{
		"schemaVersion": 1,
		"name": "write",
		"cases": [
			{
				"name": "upload",
				"mutating": true,
				"request": {"method": "PUT", "path": "/v1/tasks/task-a/files", "role": "admin",
					"bodyBase64": "%[1]s"},
				"expect": {"status": 204}
			},
			{
				"name": "download",
				"request": {"method": "GET", "path": "/v1/tasks/task-a/files", "role": "admin",
					"accept": "application/octet-stream"},
				"expect": {"status": 200, "bodyBase64": "%[1]s", "bodyLength": %[2]d}
			}
		]
	}`, encoded, len(payload)))

	summary := Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"admin": "test-token"},
		AllowMutation: true,
	}, dir)

	if summary.Skipped != 0 {
		t.Fatalf("summary = %+v, want no skips", summary)
	}
	if gotContentType != octetStreamMediaType {
		t.Fatalf("a raw body must default to %q, got %q", octetStreamMediaType, gotContentType)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("the server received %q, want the exact upload bytes %q", gotBody, payload)
	}
}

// TestRawBodyProblemFailsOnATruncatedOrDifferentBody pins the raw assertion as a
// value: exact bytes pass, and a short or altered body fails with the reason a
// reader needs instead of a silent pass.
func TestRawBodyProblemFailsOnATruncatedOrDifferentBody(t *testing.T) {
	want := []byte("hello world")
	encoded := base64.StdEncoding.EncodeToString(want)

	tests := []struct {
		name   string
		expect Expect
		body   []byte
		want   string
	}{
		{name: "exact bytes", expect: Expect{BodyBase64: encoded}, body: want},
		{name: "exact with a length", expect: Expect{BodyBase64: encoded, BodyLength: intPtr(len(want))}, body: want},
		{name: "truncated", expect: Expect{BodyBase64: encoded}, body: want[:5], want: "truncated"},
		{name: "truncated and a declared length", expect: Expect{BodyBase64: encoded, BodyLength: intPtr(len(want))},
			body: want[:5], want: "want 11 body bytes, got 5"},
		{name: "different", expect: Expect{BodyBase64: encoded}, body: []byte("hello worlD"), want: "first difference at byte 10"},
		{name: "longer", expect: Expect{BodyBase64: encoded}, body: append(append([]byte(nil), want...), '!'),
			want: "first difference at byte 11"},
		{name: "a length only", expect: Expect{BodyLength: intPtr(3)}, body: want, want: "want 3 body bytes, got 11"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := &result{body: tc.body}
			got := rawBodyProblem(tc.expect, res)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("rawBodyProblem = %q, want no problem", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("rawBodyProblem = %q, want it to name %q", got, tc.want)
			}
		})
	}
}

func TestRunRejectsAnEmptyFixtureDirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty fixture directory must fail loudly, not run zero cases")
	}
}

// TestRunGatesOnTheAdvertisedCapabilities is the gate contract in one table: a
// required capability that is missing skips, one that is present runs, and a case
// that asserts a capability must be absent skips when the target has it.
func TestRunGatesOnTheAdvertisedCapabilities(t *testing.T) {
	var served []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = append(served, r.URL.Path)
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"kind":"VersionInfo"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "gates.json", `{
		"schemaVersion": 1,
		"name": "gates",
		"cases": [
			{
				"name": "needs snapshots",
				"requiresCapabilities": ["snapshots"],
				"request": {"method": "GET", "path": "/v1/needs", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "needs read",
				"requiresCapabilities": ["read"],
				"request": {"method": "GET", "path": "/v1/has", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "needs attach",
				"requiresCapabilities": ["attach"],
				"request": {"method": "GET", "path": "/v1/attach", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "asserts snapshots disabled",
				"absentCapabilities": ["snapshots"],
				"request": {"method": "GET", "path": "/v1/disabled", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			}
		]
	}`)

	summary := Run(t, Env{
		BaseURL:      server.URL,
		Client:       server.Client(),
		Tokens:       map[string]string{"reader": "test-token"},
		Capabilities: []string{"read", "snapshots"},
	}, dir)

	if summary.Cases != 4 || summary.Skipped != 2 || summary.Gated != 2 {
		t.Fatalf("summary = %+v, want 4 cases and 2 gated skips", summary)
	}
	if len(served) != 2 || served[0] != "/v1/needs" || served[1] != "/v1/has" {
		t.Fatalf("requests = %v, want only the two advertised cases to reach the server", served)
	}
}

// TestRunAppliesTheRunSuffix is the collision guard: the token reaches the path,
// the body, the header and the expectation compared against the response, so two
// runs cannot create the same object or replay the same key.
func TestRunAppliesTheRunSuffix(t *testing.T) {
	var name, key, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("Idempotency-Key")

		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		name = body.Name

		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Service","name":"` + body.Name + `"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "write.json", `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [{
			"name": "create",
			"mutating": true,
			"request": {
				"method": "POST", "path": "/v1/services/conformance-ws-{{run}}", "role": "admin",
				"headers": {"Idempotency-Key": "conformance-idem-{{run}}"},
				"body": {"name": "conformance-ws-{{run}}"}
			},
			"expect": {"status": 201, "kind": "Service", "jsonPaths": {"name": "conformance-ws-{{run}}"}}
		}]
	}`)

	summary := Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"admin": "test-token"},
		AllowMutation: true,
		RunSuffix:     "20260101T000000Z-abcdef01",
	}, dir)

	if summary.Cases != 1 || summary.Skipped != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if want := "conformance-ws-20260101T000000Z-abcdef01"; name != want {
		t.Fatalf("body name = %q, want %q", name, want)
	}
	if want := "conformance-idem-20260101T000000Z-abcdef01"; key != want {
		t.Fatalf("Idempotency-Key = %q, want %q", key, want)
	}
	if want := "/v1/services/conformance-ws-20260101T000000Z-abcdef01"; path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

// TestRunCapturesAResponseValue proves a capture is a real variable: the id a
// create returned names the request that cleans it up, with no pinned server id.
func TestRunCapturesAResponseValue(t *testing.T) {
	var cleaned string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/services", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"kind":"Service","id":"svc-42","name":"x"}`))
	})
	mux.HandleFunc("DELETE /v1/services/{id}", func(w http.ResponseWriter, r *http.Request) {
		cleaned = r.PathValue("id")
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "write.json", `{
		"schemaVersion": 1,
		"name": "write",
		"cases": [
			{
				"name": "create",
				"mutating": true,
				"request": {"method": "POST", "path": "/v1/services", "role": "admin",
					"body": {"name": "conformance-ws-{{run}}"}},
				"expect": {"status": 201, "kind": "Service"},
				"capture": {"service": "id"}
			},
			{
				"name": "delete",
				"mutating": true,
				"request": {"method": "DELETE", "path": "/v1/services/$service", "role": "admin"},
				"expect": {"status": 204}
			}
		]
	}`)

	Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"admin": "test-token"},
		AllowMutation: true,
	}, dir)

	if cleaned != "svc-42" {
		t.Fatalf("the delete used %q, want the captured id", cleaned)
	}
}

// TestRequestIDEchoMismatch pins both branches of requestIdInBody: a true case
// needs the echo at its path and it must be this request's id; a false case needs
// the path absent.
func TestRequestIDEchoMismatch(t *testing.T) {
	document := decodeBody(t, `{
		"kind": "TaskList",
		"partial": [{"error": {"code": "unavailable", "requestId": "req_test"}}]
	}`)

	tests := []struct {
		name    string
		expect  Expect
		header  string
		wantErr bool
	}{
		{name: "top-level true is absent", expect: Expect{RequestIDInBody: boolPtr(true)}, header: "req_test", wantErr: true},
		{name: "nested true", expect: Expect{RequestIDInBody: boolPtr(true), RequestIDPath: "partial.0.error.requestId"}, header: "req_test"},
		{name: "nested true, another request", expect: Expect{RequestIDInBody: boolPtr(true), RequestIDPath: "partial.0.error.requestId"}, header: "req_other", wantErr: true},
		{name: "nested false", expect: Expect{RequestIDInBody: boolPtr(false), RequestIDPath: "partial.0.error.requestId"}, header: "req_test", wantErr: true},
		{name: "top-level false", expect: Expect{RequestIDInBody: boolPtr(false)}, header: "req_test"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := requestIDEchoMismatch(document, tc.expect, tc.header)
			if (got != "") != tc.wantErr {
				t.Fatalf("mismatch = %q, want an error: %v", got, tc.wantErr)
			}
		})
	}
}

// TestMinimumMismatch pins minValues: an integer at or above the floor passes, and
// a missing, non-numeric or smaller value is a mismatch with a readable reason.
func TestMinimumMismatch(t *testing.T) {
	document := decodeBody(t, `{"sizeBytes": 4096, "ratio": 1.5, "label": "big"}`)

	tests := []struct {
		path    string
		want    int64
		wantErr bool
	}{
		{path: "sizeBytes", want: 1},
		{path: "sizeBytes", want: 4096},
		{path: "sizeBytes", want: 4097, wantErr: true},
		{path: "missing", want: 1, wantErr: true},
		{path: "ratio", want: 1, wantErr: true},
		{path: "label", want: 1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s>=%d", tc.path, tc.want), func(t *testing.T) {
			got := minimumMismatch(document, tc.path, tc.want)
			if (got != "") != tc.wantErr {
				t.Fatalf("mismatch = %q, want an error: %v", got, tc.wantErr)
			}
		})
	}
}

// TestAssertStreamAcceptsASilentLabConsole is the raw-stream counterpart of the
// quiet-cluster rule: a live guest that prints nothing before the deadline must
// not fail a run whose 200 and media type already prove the relay reached it.
func TestAssertStreamAcceptsASilentLabConsole(t *testing.T) {
	c := Case{Name: "attach", Stream: &Stream{Bytes: 14}}
	res := &result{
		status:   http.StatusOK,
		header:   http.Header{"Content-Type": []string{octetStreamMediaType}},
		stream:   true,
		raw:      true,
		rawBytes: 0,
	}

	assertStream(t, c, res, true)
}

// runSuffixPattern is the shape newRunSuffix promises: a UTC timestamp, a dash and
// eight hex characters.
var runSuffixPattern = regexp.MustCompile(`^\d{8}T\d{6}Z-[0-9a-f]{8}$`)

// TestNewRunSuffixIsUnique proves the suffix has the shape the fixtures rely on
// and that no two runs in one process share it.
func TestNewRunSuffixIsUnique(t *testing.T) {
	seen := make(map[string]bool, 512)
	for i := 0; i < 512; i++ {
		suffix, err := newRunSuffix()
		if err != nil {
			t.Fatalf("newRunSuffix: %v", err)
		}
		if !runSuffixPattern.MatchString(suffix) {
			t.Fatalf("suffix %q does not look like a timestamp plus a nonce", suffix)
		}
		if seen[suffix] {
			t.Fatalf("suffix %q repeated; two runs could collide", suffix)
		}
		seen[suffix] = true
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
