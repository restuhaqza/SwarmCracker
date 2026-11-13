// This file owns the /v1/events stream: query parsing, SSE and NDJSON framing,
// heartbeat, label filtering and the cursor_expired error.
package apihttp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	eventFormatSSE    = "sse"
	eventFormatNDJSON = "ndjson"

	defaultEventHeartbeat = 15 * time.Second
)

// parseEventQuery validates every query parameter and returns the format.
func (s *Server) parseEventQuery(r *http.Request) (v1.EventOptions, string, error) {
	query := r.URL.Query()
	opts := v1.EventOptions{IncludeConfigValues: canReadConfigValues(r)}

	if raw := strings.TrimSpace(query.Get("types")); raw != "" {
		for _, kind := range strings.Split(raw, ",") {
			kind = strings.TrimSpace(kind)
			if !eventKindAllowed(kind) {
				return v1.EventOptions{}, "", invalidField("types", "unsupported event type "+kind)
			}
			opts.Types = append(opts.Types, kind)
		}
	}

	since := strings.TrimSpace(query.Get("since"))
	if since == "" {
		since = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if since != "" {
		if _, err := strconv.ParseUint(since, 10, 64); err != nil {
			return v1.EventOptions{}, "", invalidField("since", "must be a watch cursor")
		}
		opts.Since = since
	}

	if raw := strings.TrimSpace(query.Get("include")); raw != "" {
		if raw != "object" {
			return v1.EventOptions{}, "", invalidField("include", `must be "object"`)
		}
		opts.IncludeObject = true
	}

	format := eventFormatSSE
	accept := strings.ToLower(r.Header.Get(acceptHeader))
	if strings.Contains(accept, "application/x-ndjson") {
		format = eventFormatNDJSON
	}
	return opts, format, nil
}

func eventKindAllowed(kind string) bool {
	switch kind {
	case "service", "task", "node", "network", "config", "secret", sandboxEventKind:
		return true
	default:
		return false
	}
}

// sandboxEventKind is the `types` value that selects the node's own sandbox
// events. It is not one of the cluster watch's kinds: the resource is a sandbox,
// which is not a SwarmKit object, so the events come from the bus in
// sandbox_events.go rather than from pkg/client.
const sandboxEventKind = "sandbox"

// clusterEventKinds returns the requested kinds without the node-local ones, and
// reports whether the node's own sandbox events were asked for. The list that
// reaches the cluster client must contain only kinds it watches: it refuses an
// unknown one rather than ignoring it.
func clusterEventKinds(types []string) ([]string, bool) {
	if len(types) == 0 {
		// No filter means every kind, which includes the local ones.
		return nil, true
	}
	out := make([]string, 0, len(types))
	wantSandbox := false
	for _, kind := range types {
		if kind == sandboxEventKind {
			wantSandbox = true
			continue
		}
		out = append(out, kind)
	}
	return out, wantSandbox
}

// watchEvents serves GET /v1/events.
//
// It is the merge of two sources over one stream: the cluster's watch, which
// pkg/client maps from SwarmKit, and this node's own sandbox changes
// (sandbox_events.go). The framing, the cursor, the type and label filters and the
// heartbeat are the same for both, which is the point of a single stream.
func (s *Server) watchEvents(w http.ResponseWriter, r *http.Request) {
	opts, format, err := s.parseEventQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	selector, err := parseLabelSelectors(r.URL.Query()["label"])
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	sources, err := s.openEventSources(r, opts)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer sources.close()

	s.writeEventStream(w, r, opts, format, selector, sources)
}

// eventSources is the two channels one /v1/events call reads, and the cleanup that
// ends its subscription to the node's own bus.
type eventSources struct {
	// cluster is the cluster watch's events. It is nil when the caller asked for
	// nothing but sandbox events, in which case no watch is opened at all.
	cluster <-chan v1.Event
	// errs carries the cluster watch's failures. Nil with cluster.
	errs <-chan error
	// local is this node's sandbox events, or nil when the caller did not ask for
	// them: a nil channel never fires in a select, which is how "not subscribed" is
	// expressed.
	local <-chan v1.Event
	// cancel ends the bus subscription, if one was made.
	cancel func()
}

// close ends the subscription. It runs when the handler returns, which is when the
// request context has ended: the loop exits on that, and this is the other half of it.
func (s *eventSources) close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// openEventSources opens what the caller asked to watch.
//
// The cluster client is given only the kinds it watches — it refuses an unknown one
// rather than ignoring it — and a caller who asked for nothing but sandbox events
// gets no cluster watch at all, because an empty kind list means "every kind" to
// that client and would have to be filtered back out.
func (s *Server) openEventSources(r *http.Request, opts v1.EventOptions) (*eventSources, error) {
	clusterKinds, wantSandbox := clusterEventKinds(opts.Types)
	sources := &eventSources{}

	if len(opts.Types) == 0 || len(clusterKinds) > 0 {
		clusterOpts := opts
		clusterOpts.Types = clusterKinds
		events, errs, err := s.cfg.Client.WatchEvents(r.Context(), clusterOpts)
		if err != nil {
			return nil, err
		}
		sources.cluster, sources.errs = events, errs
	}

	if wantSandbox && s.cfg.Sandboxes != nil {
		ch, cancel := s.cfg.SandboxEvents.Subscribe()
		sources.local, sources.cancel = ch, cancel
	}
	return sources, nil
}

// writeEventStream writes the stream's headers, its ready frame, and then one frame
// per event until the request ends or a source closes.
func (s *Server) writeEventStream(w http.ResponseWriter, r *http.Request, opts v1.EventOptions, format string, selector []labelSelector, sources *eventSources) {
	heartbeat := s.cfg.EventHeartbeat
	if heartbeat <= 0 {
		heartbeat = defaultEventHeartbeat
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	if format == eventFormatNDJSON {
		w.Header().Set("Content-Type", "application/x-ndjson")
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flush := flushFunc(w)
	s.handleReadyFrame(w, format)
	flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-sources.cluster:
			if !ok || !s.writeEvent(w, r, opts, format, selector, flush, event) {
				return
			}
		case event, ok := <-sources.local:
			if !ok || !s.writeEvent(w, r, opts, format, selector, flush, event) {
				return
			}
		case err, ok := <-sources.errs:
			if !ok {
				return
			}
			if err != nil {
				s.writeStreamError(w, r, format, err)
				flush()
			}
			return
		case <-ticker.C:
			s.writeHeartbeat(w, format)
			flush()
		}
	}
}

// writeEvent writes one event and reports whether the stream may go on.
//
// The copy is deliberate: a sandbox event is handed to every subscriber of the bus by
// value, and stripping the object for a caller who did not ask for it must not reach
// the others.
func (s *Server) writeEvent(w http.ResponseWriter, r *http.Request, opts v1.EventOptions, format string, selector []labelSelector, flush func(), raw v1.Event) bool {
	event := raw
	if !opts.IncludeObject {
		event.Object = nil
	}
	if !matchesLabels(event.Labels, selector) {
		return true
	}

	if format == eventFormatNDJSON {
		s.writeNDJSON(w, event)
		flush()
		return true
	}

	body, err := encodeJSON(event)
	if err != nil {
		s.writeStreamError(w, r, format, output.NewErrorf(output.CodeInternal, "encode event: %v", err))
		flush()
		return false
	}
	s.writeSSE(w, event.Cursor, event.Type, body)
	flush()
	return true
}

// handleReadyFrame writes the stream's first frame, which tells the caller the stream
// is live. A ready frame has no cursor: it is written before any event.
func (s *Server) handleReadyFrame(w http.ResponseWriter, format string) {
	if format == eventFormatNDJSON {
		s.writeNDJSON(w, v1.ReadyFrame{Type: "ready"})
		return
	}
	s.writeSSE(w, "", "ready", []byte(`{"type":"ready"}`))
}

// writeHeartbeat writes one keep-alive frame.
func (s *Server) writeHeartbeat(w http.ResponseWriter, format string) {
	if format == eventFormatNDJSON {
		s.writeNDJSON(w, v1.ReadyFrame{Type: "ping"})
		return
	}
	_, _ = fmt.Fprint(w, ": ping\n\n")
}

// flushFunc returns the writer's flush, or a no-op for a writer that cannot flush
// (a response recorder under test, for instance).
func flushFunc(w http.ResponseWriter) func() {
	flusher, _ := w.(http.Flusher)
	return func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// writeSSE writes one framed event. The id is the resume cursor; a ready frame
// has no cursor because the manager has not read the store version yet.
func (s *Server) writeSSE(w http.ResponseWriter, cursor, event string, data []byte) {
	if cursor != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", cursor)
	}
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.TrimSpace(string(data)))
}

// writeNDJSON writes one compact JSON line.
func (s *Server) writeNDJSON(w http.ResponseWriter, value any) {
	body, err := encodeJSON(value)
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}

// writeStreamError reports a failure after the stream headers are sent. HTTP
// status cannot change, so the typed document travels as an event/line.
func (s *Server) writeStreamError(w http.ResponseWriter, r *http.Request, format string, err error) {
	typed := typedError(err)
	payload := v1.Error{
		Code:      string(typed.Code),
		Message:   typed.Message,
		RequestID: requestIDFrom(r),
		Retryable: typed.Retryable,
		Details:   typed.Details,
	}
	doc := v1.NewErrorDocument(payload)
	if format == eventFormatNDJSON {
		s.writeNDJSON(w, doc)
		return
	}
	body, encodeErr := encodeJSON(doc)
	if encodeErr != nil {
		return
	}
	s.writeSSE(w, "", "error", body)
}
