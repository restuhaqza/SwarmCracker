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
	case "service", "task", "node", "network", "config", "secret":
		return true
	default:
		return false
	}
}

// watchEvents serves GET /v1/events.
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

	events, errs, err := s.cfg.Client.WatchEvents(r.Context(), opts)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

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

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	if format == eventFormatNDJSON {
		s.writeNDJSON(w, v1.ReadyFrame{Type: "ready"})
	} else {
		s.writeSSE(w, "", "ready", []byte(`{"type":"ready"}`))
	}
	flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if !matchesLabels(event.Labels, selector) {
				continue
			}
			if format == eventFormatNDJSON {
				s.writeNDJSON(w, event)
			} else {
				body, encodeErr := encodeJSON(event)
				if encodeErr != nil {
					s.writeStreamError(w, r, format, output.NewErrorf(output.CodeInternal, "encode event: %v", encodeErr))
					flush()
					return
				}
				s.writeSSE(w, event.Cursor, event.Type, body)
			}
			flush()
		case err, ok := <-errs:
			if !ok {
				return
			}
			if err != nil {
				s.writeStreamError(w, r, format, err)
				flush()
			}
			return
		case <-ticker.C:
			if format == eventFormatNDJSON {
				s.writeNDJSON(w, v1.ReadyFrame{Type: "ping"})
			} else {
				_, _ = fmt.Fprint(w, ": ping\n\n")
			}
			flush()
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
