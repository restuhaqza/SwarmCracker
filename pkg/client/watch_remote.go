package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// watchEvents opens the remote manager's NDJSON event stream.
func (r *remoteClient) watchEvents(ctx context.Context, opts v1.EventOptions) (<-chan v1.Event, <-chan error, error) {
	req, err := r.newEventStreamRequest(ctx, opts)
	if err != nil {
		return nil, nil, err
	}

	resp, err := r.stream.Do(req)
	if err != nil {
		return nil, nil, transportError("GET /v1/events", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return nil, nil, decodeErrorDocument(http.MethodGet, "/v1/events", resp.StatusCode, resp.Status, body)
	}

	events := make(chan v1.Event, watchBuffer)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer resp.Body.Close()
		decodeEventStream(ctx, resp.Body, events, errs)
	}()
	return events, errs, nil
}

// newEventStreamRequest builds the authenticated NDJSON request.
func (r *remoteClient) newEventStreamRequest(ctx context.Context, opts v1.EventOptions) (*http.Request, error) {
	u := r.baseURL.JoinPath("v1", "events")
	u.RawQuery = eventStreamQuery(opts).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, output.NewErrorf(output.CodeInternal, "build events request: %v", err)
	}
	req.Header.Set("Accept", "application/x-ndjson")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	return req, nil
}

// eventStreamQuery renders the v1 event filters as query parameters.
func eventStreamQuery(opts v1.EventOptions) url.Values {
	query := url.Values{}
	if len(opts.Types) > 0 {
		query.Set("types", strings.Join(opts.Types, ","))
	}
	if opts.Since != "" {
		query.Set("since", opts.Since)
	}
	if opts.IncludeObject {
		query.Set("include", "object")
	}
	return query
}

// decodeEventStream consumes NDJSON frames until EOF, an error document, or
// cancellation.
func decodeEventStream(ctx context.Context, body io.Reader, events chan<- v1.Event, errs chan<- error) {
	decoder := json.NewDecoder(body)
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if err != io.EOF && ctx.Err() == nil {
				errs <- transportError("read /v1/events", err)
			}
			return
		}
		var probe struct {
			Type string `json:"type"`
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		switch {
		case probe.Type == "ready" || probe.Type == "ping":
			continue
		case probe.Kind == v1.KindError:
			if err := sendEventError(raw, errs); err != nil {
				return
			}
			return
		}

		var event v1.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			continue
		}
		select {
		case events <- event:
		case <-ctx.Done():
			return
		}
	}
}

// sendEventError decodes one error frame and queues it for the caller.
func sendEventError(raw json.RawMessage, errs chan<- error) error {
	var doc v1.ErrorDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	e := output.NewError(output.Code(doc.Error.Code), doc.Error.Message)
	e.Retryable = doc.Error.Retryable
	e.Details = doc.Error.Details
	errs <- e
	return nil
}
