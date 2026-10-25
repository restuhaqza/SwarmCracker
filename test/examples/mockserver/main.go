// Command mockserver is the loopback fixture the documented SDK examples run
// against (docs-site/examples, driven by test/examples/run.sh).
//
// It is not a second SwarmCracker server and must never be deployed: it serves a
// fixed dataset, performs no authentication and refuses to bind anything but a
// loopback address. It exists so `make examples` can prove the shipped Python and
// Node clients against the v1 wire format without a cluster, root or network.
//
// It implements exactly the three routes the examples use:
//
//	GET  /v1/version
//	GET  /v1/services   (two services; honours limit and cursor with a real next)
//	POST /v1/tasks/{id}/exec  (NDJSON: one stdout frame, then the exit status)
//
// Every response carries the v1 media type, X-SwarmCracker-Api-Version and
// X-Request-Id, and an unknown path or method answers a v1 ErrorDocument.
//
// -exit-code N sets the guest status the exec stream reports (default 0), so the
// runner can prove that the examples print a failed command and still exit 0.
// Each services request is logged to stderr so the runner can assert the page
// size the examples asked for.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	apiVersion      = "1"
	v1MediaType     = "application/vnd.swarmcracker.v1+json"
	ndjsonMediaType = "application/x-ndjson"
	serverVersion   = "0.11.0-example"

	apiVersionHeader = "X-SwarmCracker-Api-Version"
	requestIDHeader  = "X-Request-Id"

	// execStdout is what a `true` run prints in this fixture. It keeps its
	// newline so the examples have to trim it, as they do for a real command.
	execStdout = "example\n"
)

// service is one item of the fixture's ServiceList.
type service struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Revision string        `json:"revision"`
	Spec     serviceSpec   `json:"spec"`
	Status   serviceStatus `json:"status"`
}

type serviceSpec struct {
	Mode     string   `json:"mode"`
	Replicas int      `json:"replicas"`
	Task     taskSpec `json:"task"`
}

type taskSpec struct {
	Image string `json:"image"`
}

type serviceStatus struct {
	DesiredTasks      int  `json:"desiredTasks"`
	RunningTasks      int  `json:"runningTasks"`
	TasksInTransition int  `json:"tasksInTransition"`
	Converged         bool `json:"converged"`
}

// services is the fixture dataset. The order is significant: the examples print
// it as-is, and run.sh asserts web before api.
var services = []service{
	{
		ID:       "svc-web",
		Name:     "web",
		Revision: "1",
		Spec:     serviceSpec{Mode: "replicated", Replicas: 2, Task: taskSpec{Image: "nginx:1.27-alpine"}},
		Status:   serviceStatus{DesiredTasks: 2, RunningTasks: 2, Converged: true},
	},
	{
		ID:       "svc-api",
		Name:     "api",
		Revision: "1",
		Spec:     serviceSpec{Mode: "replicated", Replicas: 1, Task: taskSpec{Image: "caddy:2.8-alpine"}},
		Status:   serviceStatus{DesiredTasks: 1, RunningTasks: 1, Converged: true},
	},
}

// handler serves the fixture and counts requests for X-Request-Id.
type handler struct {
	requests atomic.Uint64
	// exitCode is the guest status the exec stream reports.
	exitCode int
}

func newHandler(exitCode int) http.Handler {
	return &handler{exitCode: exitCode}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// v1 headers are set for every response, including errors.
	w.Header().Set(apiVersionHeader, apiVersion)
	w.Header().Set(requestIDHeader, fmt.Sprintf("req-%d", h.requests.Add(1)))

	switch {
	case r.URL.Path == "/v1/version":
		if r.Method != http.MethodGet {
			h.methodNotAllowed(w, http.MethodGet)
			return
		}
		h.version(w)
	case r.URL.Path == "/v1/services":
		if r.Method != http.MethodGet {
			h.methodNotAllowed(w, http.MethodGet)
			return
		}
		h.listServices(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/tasks/") && strings.HasSuffix(r.URL.Path, "/exec"):
		if r.Method != http.MethodPost {
			h.methodNotAllowed(w, http.MethodPost)
			return
		}
		h.exec(w, r)
	default:
		h.fail(w, http.StatusNotFound, "not_found", "no such route: "+r.URL.Path, nil)
	}
}

func (h *handler) version(w http.ResponseWriter) {
	writeDocument(w, http.StatusOK, map[string]any{
		"schemaVersion":       1,
		"kind":                "VersionInfo",
		"apiVersions":         []string{"1"},
		"serverVersion":       serverVersion,
		"grpcProtocolVersion": "1",
		// Deliberately unsorted: the examples sort capabilities before printing.
		"capabilities": []string{"write", "events", "read", "exec"},
	})
}

func (h *handler) listServices(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// run.sh asserts the examples ask for a page of two.
	fmt.Fprintf(os.Stderr, "mockserver: %s /v1/services limit=%s cursor=%s\n",
		r.Method, query.Get("limit"), query.Get("cursor"))

	limit := len(services)
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			h.fail(w, http.StatusBadRequest, "invalid", "limit must be a positive integer", map[string]any{"fields": []string{"limit"}})
			return
		}
		limit = parsed
	}

	start := 0
	if raw := query.Get("cursor"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed >= len(services) {
			h.fail(w, http.StatusBadRequest, "invalid", "cursor is not a position this fixture issued", map[string]any{"fields": []string{"cursor"}})
			return
		}
		start = parsed
	}

	end := start + limit
	if end > len(services) {
		end = len(services)
	}
	// Items is allocated so the document always carries [] rather than null.
	items := make([]service, end-start)
	copy(items, services[start:end])

	var next *string
	if end < len(services) {
		cursor := strconv.Itoa(end)
		next = &cursor
	}

	writeDocument(w, http.StatusOK, map[string]any{
		"schemaVersion": 1,
		"kind":          "ServiceList",
		"items":         items,
		"next":          next,
	})
}

// execRequest is the part of ExecRequest this fixture reads.
type execRequest struct {
	Command []string `json:"command"`
}

// execFrame is one NDJSON frame of the exec stream. []byte is base64 on the wire.
type execFrame struct {
	Type     string `json:"type"`
	Data     []byte `json:"data,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

func (h *handler) exec(w http.ResponseWriter, r *http.Request) {
	reference := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/exec")
	if reference == "" || strings.Contains(reference, "/") {
		h.fail(w, http.StatusNotFound, "not_found", "no such task", nil)
		return
	}

	var request execRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		h.fail(w, http.StatusBadRequest, "invalid", "the body is not an ExecRequest", nil)
		return
	}
	if len(request.Command) == 0 {
		h.fail(w, http.StatusBadRequest, "invalid", "command must not be empty", map[string]any{"fields": []string{"command"}})
		return
	}

	w.Header().Set("Content-Type", ndjsonMediaType)
	w.WriteHeader(http.StatusOK)

	encoder := json.NewEncoder(w)
	exitCode := h.exitCode
	frames := []execFrame{
		{Type: "stdout", Data: []byte(execStdout)},
		{Type: "exit", ExitCode: &exitCode},
	}
	for _, frame := range frames {
		if err := encoder.Encode(frame); err != nil {
			// The client has gone; there is nothing to report to it.
			return
		}
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *handler) methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	h.fail(w, http.StatusMethodNotAllowed, "invalid", "method not allowed on this route", map[string]any{"reason": "method_not_allowed"})
}

// fail writes a v1 ErrorDocument. requestId is the header value so a client can
// correlate the two, exactly as the real API does.
func (h *handler) fail(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	errorBody := map[string]any{
		"code":      code,
		"message":   message,
		"requestId": w.Header().Get(requestIDHeader),
		"retryable": false,
	}
	if details != nil {
		errorBody["details"] = details
	}
	writeDocument(w, status, map[string]any{
		"schemaVersion": 1,
		"kind":          "Error",
		"error":         errorBody,
	})
}

// writeDocument writes document as the v1 media type.
func writeDocument(w http.ResponseWriter, status int, document any) {
	body, err := json.Marshal(document)
	if err != nil {
		// Nothing in this fixture can fail to marshal; fail closed rather than
		// serving half a body.
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", v1MediaType)
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// checkLoopback refuses anything but a loopback bind address. The fixture has no
// authentication, so the address is the security boundary.
func checkLoopback(host string) error {
	switch host {
	case "localhost":
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("listen host %q is not a loopback address", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("listen host %q is not loopback; this fixture has no authentication", host)
	}
	return nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback address to serve on")
	exitCode := flag.Int("exit-code", 0, "guest exit status the exec stream reports")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("mockserver: bad -listen %q: %v", *listen, err)
	}
	if err := checkLoopback(host); err != nil {
		log.Fatalf("mockserver: %v", err)
	}

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("mockserver: %v", err)
	}

	// run.sh waits for this exact line, so print it unbuffered and complete.
	fmt.Printf("addr=%s\n", listener.Addr().String())
	_ = os.Stdout.Sync()

	server := &http.Server{
		Handler:           newHandler(*exitCode),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("mockserver: %v", err)
	}
}
