package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// get performs one request against a fixture server and returns the response.
func get(t *testing.T, url string) *http.Response {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func decodeBody[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	var document T
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return document
}

// TestVersion asserts the envelope, the v1 headers and the request id echoed in
// an error document. The examples print serverVersion and the sorted
// capabilities, so both must be present here.
func TestVersion(t *testing.T) {
	server := httptest.NewServer(newHandler(0))
	defer server.Close()

	response := get(t, server.URL+"/v1/version")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != v1MediaType {
		t.Errorf("Content-Type = %q, want %q", got, v1MediaType)
	}
	if got := response.Header.Get(apiVersionHeader); got != apiVersion {
		t.Errorf("%s = %q, want %q", apiVersionHeader, got, apiVersion)
	}
	if got := response.Header.Get(requestIDHeader); got == "" {
		t.Errorf("%s is empty", requestIDHeader)
	}

	version := decodeBody[struct {
		SchemaVersion int      `json:"schemaVersion"`
		Kind          string   `json:"kind"`
		APIVersions   []string `json:"apiVersions"`
		ServerVersion string   `json:"serverVersion"`
		Capabilities  []string `json:"capabilities"`
	}](t, response)

	if version.SchemaVersion != 1 || version.Kind != "VersionInfo" {
		t.Errorf("envelope = %d/%q, want 1/VersionInfo", version.SchemaVersion, version.Kind)
	}
	if version.ServerVersion != serverVersion {
		t.Errorf("serverVersion = %q, want %q", version.ServerVersion, serverVersion)
	}
	if len(version.Capabilities) == 0 {
		t.Error("capabilities is empty")
	}
}

type listDocument struct {
	SchemaVersion int       `json:"schemaVersion"`
	Kind          string    `json:"kind"`
	Items         []service `json:"items"`
	Next          *string   `json:"next"`
}

// TestListServicesPagination asserts the examples' page size and that the next
// cursor is real: following it yields the remaining service.
func TestListServicesPagination(t *testing.T) {
	server := httptest.NewServer(newHandler(0))
	defer server.Close()

	first := decodeBody[listDocument](t, get(t, server.URL+"/v1/services?limit=2"))
	if first.Kind != "ServiceList" || first.SchemaVersion != 1 {
		t.Fatalf("envelope = %d/%q, want 1/ServiceList", first.SchemaVersion, first.Kind)
	}
	if len(first.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(first.Items))
	}
	if first.Items[0].Name != "web" || first.Items[1].Name != "api" {
		t.Errorf("names = %q, %q; want web, api", first.Items[0].Name, first.Items[1].Name)
	}
	if first.Next != nil {
		t.Errorf("next = %q on the last page, want null", *first.Next)
	}

	page := decodeBody[listDocument](t, get(t, server.URL+"/v1/services?limit=1"))
	if len(page.Items) != 1 || page.Items[0].Name != "web" {
		t.Fatalf("first page = %+v, want only web", page.Items)
	}
	if page.Next == nil {
		t.Fatal("next is null on a page that has a successor")
	}

	rest := decodeBody[listDocument](t, get(t, server.URL+"/v1/services?limit=1&cursor="+*page.Next))
	if len(rest.Items) != 1 || rest.Items[0].Name != "api" {
		t.Fatalf("second page = %+v, want only api", rest.Items)
	}
	if rest.Next != nil {
		t.Errorf("next = %q on the last page, want null", *rest.Next)
	}
}

type errorDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Error         struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
	} `json:"error"`
}

// TestErrors asserts that the fixture answers unknown routes, bad queries and
// wrong methods with a decodable v1 ErrorDocument that names the request.
func TestErrors(t *testing.T) {
	server := httptest.NewServer(newHandler(0))
	defer server.Close()

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{"unknown path", http.MethodGet, "/v1/nope", http.StatusNotFound, "not_found"},
		{"bad limit", http.MethodGet, "/v1/services?limit=0", http.StatusBadRequest, "invalid"},
		{"unknown cursor", http.MethodGet, "/v1/services?cursor=99", http.StatusBadRequest, "invalid"},
		{"wrong method", http.MethodPost, "/v1/services", http.StatusMethodNotAllowed, "invalid"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := http.NewRequest(testCase.method, server.URL+testCase.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer func() { _ = response.Body.Close() }()

			if response.StatusCode != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, testCase.wantStatus)
			}
			if got := response.Header.Get("Content-Type"); got != v1MediaType {
				t.Errorf("Content-Type = %q, want %q", got, v1MediaType)
			}
			document := decodeBody[errorDocument](t, response)
			if document.SchemaVersion != 1 || document.Kind != "Error" {
				t.Errorf("envelope = %d/%q, want 1/Error", document.SchemaVersion, document.Kind)
			}
			if document.Error.Code != testCase.wantCode {
				t.Errorf("code = %q, want %q", document.Error.Code, testCase.wantCode)
			}
			if document.Error.RequestID == "" || document.Error.RequestID != response.Header.Get(requestIDHeader) {
				t.Errorf("error.requestId = %q, header = %q", document.Error.RequestID, response.Header.Get(requestIDHeader))
			}
		})
	}
}

// TestExec asserts the NDJSON frame set the examples consume: one stdout frame
// carrying "example\n" and exactly one exit frame with the configured status.
// It also asserts the v1 headers, as the other routes do.
func TestExec(t *testing.T) {
	type frame struct {
		Type     string `json:"type"`
		Data     []byte `json:"data"`
		ExitCode *int   `json:"exitCode"`
	}

	for _, want := range []int{0, 3} {
		t.Run(fmt.Sprintf("exit %d", want), func(t *testing.T) {
			server := httptest.NewServer(newHandler(want))
			defer server.Close()

			response, err := http.Post(server.URL+"/v1/tasks/t1/exec", v1MediaType, strings.NewReader(`{"command":["true"]}`))
			if err != nil {
				t.Fatalf("POST exec: %v", err)
			}
			defer func() { _ = response.Body.Close() }()

			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if got := response.Header.Get("Content-Type"); got != ndjsonMediaType {
				t.Errorf("Content-Type = %q, want %q", got, ndjsonMediaType)
			}
			if got := response.Header.Get(apiVersionHeader); got != apiVersion {
				t.Errorf("%s = %q, want %q", apiVersionHeader, got, apiVersion)
			}
			if got := response.Header.Get(requestIDHeader); got == "" {
				t.Errorf("%s is empty", requestIDHeader)
			}

			var frames []frame
			scanner := bufio.NewScanner(response.Body)
			for scanner.Scan() {
				var decoded frame
				if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
					t.Fatalf("decode frame %q: %v", scanner.Text(), err)
				}
				frames = append(frames, decoded)
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("read stream: %v", err)
			}

			if len(frames) != 2 {
				t.Fatalf("frames = %d, want 2", len(frames))
			}
			if frames[0].Type != "stdout" || !bytes.Equal(frames[0].Data, []byte(execStdout)) {
				t.Errorf("first frame = %q %q, want stdout %q", frames[0].Type, frames[0].Data, execStdout)
			}
			if frames[1].Type != "exit" || frames[1].ExitCode == nil || *frames[1].ExitCode != want {
				t.Errorf("last frame = %+v, want exit %d", frames[1], want)
			}
		})
	}

	server := httptest.NewServer(newHandler(0))
	defer server.Close()

	empty, err := http.Post(server.URL+"/v1/tasks/t1/exec", v1MediaType, strings.NewReader(`{"command":[]}`))
	if err != nil {
		t.Fatalf("POST empty command: %v", err)
	}
	defer func() { _ = empty.Body.Close() }()
	if empty.StatusCode != http.StatusBadRequest {
		t.Errorf("empty command status = %d, want 400", empty.StatusCode)
	}
}

// TestCheckLoopback asserts the fixture's only security control: it will not
// serve on an address other clients can reach.
func TestCheckLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		if err := checkLoopback(host); err != nil {
			t.Errorf("checkLoopback(%q) = %v, want nil", host, err)
		}
	}
	for _, host := range []string{"0.0.0.0", "", "192.0.2.1", "example.com"} {
		if err := checkLoopback(host); err == nil {
			t.Errorf("checkLoopback(%q) = nil, want an error", host)
		}
	}
}
