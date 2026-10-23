// This file owns GET /v1/tasks/{ref}/logs: the public task log stream.
//
// The handler validates the query, resolves the reference to a concrete task ID
// and hands the request to the broker, which proxies it to the node that runs the
// task. The response is application/x-ndjson — the same documented exception as
// exec — and is never buffered here.
//
// Logs are reader-scoped (tasks:logs): unlike exec and files they are not gated
// by task selectors and they are not audited, because reading a task's console
// output is the same authority a reader already has over the task list.
package apihttp

import (
	"net/http"
	"strconv"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// logsOperation is the broker operation name for a task log.
const logsOperation = "logs"

// taskLogs serves GET /v1/tasks/{ref}/logs.
func (s *Server) taskLogs(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("id")

	if !s.requireBroker(w, r, logsOperation) {
		return
	}

	if status, err := validateLogsQuery(r); err != nil {
		s.writeErrorStatus(w, r, status, err)
		return
	}

	// Resolve the reference server-side: the broker only ever receives a
	// concrete task ID, never user input.
	task, err := s.cfg.Client.GetTask(r.Context(), ref)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Carry the public request ID on the outbound call so a relayed node error
	// can name it.
	r.Header.Set(requestIDHeader, requestIDFrom(r))

	if err := s.cfg.Broker.Proxy(r.Context(), w, r, task.ID, logsOperation); err != nil {
		s.writeError(w, r, err)
	}
}

// validateLogsQuery rejects a query the node would reject. The values are not
// rewritten: tail, since and follow are forwarded exactly as the caller sent
// them once they are known to be well formed, so the public contract and the
// node handler agree on the meaning.
func validateLogsQuery(r *http.Request) (int, error) {
	values := r.URL.Query()

	if values.Has("tail") {
		// A tail above the node's maximum is forwarded: the node clamps it, so
		// asking for more is a request for "all of it", not a bad request.
		tail, err := strconv.Atoi(values.Get("tail"))
		if err != nil || tail < 0 {
			return http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"tail must be a non-negative integer").
				WithDetails(map[string]any{"parameter": "tail"})
		}
	}

	if values.Has("since") {
		if _, err := time.Parse(time.RFC3339Nano, values.Get("since")); err != nil {
			return http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"since must be an RFC3339 timestamp").
				WithDetails(map[string]any{"parameter": "since"})
		}
	}

	if values.Has("follow") {
		if _, err := strconv.ParseBool(values.Get("follow")); err != nil {
			return http.StatusBadRequest, output.NewError(output.CodeInvalid,
				"follow must be true or false").
				WithDetails(map[string]any{"parameter": "follow"})
		}
	}

	return 0, nil
}
