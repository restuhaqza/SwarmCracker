// This file owns the two apply modifiers of a write: `wait=converged` (hold the
// response until the service converges) and `dryRun=true` (validate without
// persisting).
//
// Both are query parameters rather than headers because they change what the
// call means, not how it is transported. The parsing is strict: a typo must be a
// 400, never a silently ignored modifier that turns a dry run into a real write
// or a wait into an immediate answer.
package apihttp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// waitConverged is the only value `wait` accepts.
	waitConverged = "converged"

	// defaultWaitTimeoutMs is the contract's default wait budget (contract §7.3).
	defaultWaitTimeoutMs = 60000
	// maxWaitTimeoutMs is the contract's hard wait ceiling (contract §12.3).
	maxWaitTimeoutMs = 600000
	// waitPollInterval is how often a waiting request re-reads the service.
	waitPollInterval = 100 * time.Millisecond

	waitTimeoutCode = "wait_timeout"
	waitErrorCode   = "wait_error"
)

// parseWait reads `wait` and `timeoutMs`. It returns (false, 0, nil) when the
// request does not ask to wait.
func parseWait(query url.Values) (bool, time.Duration, error) {
	if !query.Has("wait") {
		if query.Has("timeoutMs") {
			return false, 0, invalidField("timeoutMs", "requires wait=converged")
		}
		return false, 0, nil
	}
	if query.Get("wait") != waitConverged {
		return false, 0, invalidField("wait", `must be "converged"`)
	}

	timeout := time.Duration(defaultWaitTimeoutMs) * time.Millisecond
	if query.Has("timeoutMs") {
		raw := query.Get("timeoutMs")
		ms, err := strconv.Atoi(raw)
		if err != nil || ms <= 0 || ms > maxWaitTimeoutMs {
			return false, 0, invalidField("timeoutMs",
				"must be an integer between 1 and 600000 milliseconds")
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	return true, timeout, nil
}

// parseDryRun reads `dryRun`. Absent means false; only the two JSON booleans are
// accepted, so "1" and "TRUE" are not silently treated as a dry run.
func parseDryRun(query url.Values) (bool, error) {
	if !query.Has("dryRun") {
		return false, nil
	}
	switch query.Get("dryRun") {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, invalidField("dryRun", "must be true or false")
	}
}

// rejectWait rejects a wait on a route that cannot wait. Callers pass routes
// other than service create, service patch and rollback.
func rejectWait(query url.Values) error {
	if !query.Has("wait") && !query.Has("timeoutMs") {
		return nil
	}
	return invalidField("wait", "is only supported on services")
}

// rejectDeleteDryRun parses dryRun and refuses a true value on a DELETE. The
// parse still runs first so a bad value is an invalid request, not a capability
// complaint.
func rejectDeleteDryRun(query url.Values) error {
	dryRun, err := parseDryRun(query)
	if err != nil {
		return err
	}
	if dryRun {
		return invalidField("dryRun", "is not supported on delete")
	}
	return nil
}

// serviceWriteQuery parses the modifiers of a service create or patch. A wait
// and a dry run are mutually exclusive: there is nothing to converge when the
// write did not happen.
func serviceWriteQuery(r *http.Request) (v1.WriteOptions, bool, time.Duration, error) {
	query := r.URL.Query()

	dryRun, err := parseDryRun(query)
	if err != nil {
		return v1.WriteOptions{}, false, 0, err
	}
	wait, timeout, err := parseWait(query)
	if err != nil {
		return v1.WriteOptions{}, false, 0, err
	}
	if wait && dryRun {
		return v1.WriteOptions{}, false, 0, invalidField("wait", "cannot be combined with dryRun")
	}
	return v1.WriteOptions{DryRun: dryRun}, wait, timeout, nil
}

// waitForService polls one service until it reports converged or the timeout
// expires.
//
// A timeout is not an error: the mutation already committed, so the last
// document is returned with converged=false and a warning. A read failure during
// the wait is also a warning, carrying the last good document — turning it into
// an error would invite a client to retry a write that already succeeded.
func (s *Server) waitForService(ctx context.Context, id string, timeout time.Duration) (v1.Service, *v1.Warning) {
	start := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()

	var last v1.Service
	for {
		remaining := timeout - time.Since(start)
		if remaining <= 0 {
			return timedOutWait(last, timeout)
		}
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		document, err := s.cfg.Client.GetService(callCtx, id)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return timedOutWait(last, timeout)
			}
			return last, &v1.Warning{
				Code:    waitErrorCode,
				Message: "reading the service while waiting: " + safeWaitError(err),
			}
		}
		last = document
		if document.Status.Converged {
			return document, nil
		}

		select {
		case <-ctx.Done():
			return timedOutWait(last, timeout)
		case <-deadline.C:
			return timedOutWait(last, timeout)
		case <-ticker.C:
		}
	}
}

// safeWaitError keeps a useful message without echoing a raw gRPC status line.
// The manager's internal error chain is not part of the wire contract.
func safeWaitError(err error) string {
	message := err.Error()
	message = strings.TrimPrefix(message, "rpc error: ")
	if index := strings.Index(message, "desc = "); index >= 0 {
		message = message[index+len("desc = "):]
	}
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// timedOutWait is the success-with-a-warning document of a wait timeout.
func timedOutWait(last v1.Service, timeout time.Duration) (v1.Service, *v1.Warning) {
	last.Status.Converged = false
	return last, &v1.Warning{
		Code:    waitTimeoutCode,
		Message: "service did not converge within " + strconv.FormatInt(timeout.Milliseconds(), 10) + "ms",
	}
}

// waitServiceDocument runs the wait after a committed service write and keeps
// the mutation's document when the first read already failed: the caller
// should not lose the write it just made to a transient manager read error.
func (s *Server) waitServiceDocument(ctx context.Context, committed v1.Service, timeout time.Duration) (v1.Service, []v1.Warning) {
	id := committed.ID
	if id == "" {
		return committed, nil
	}

	document, warning := s.waitForService(ctx, id, timeout)
	if warning == nil {
		return document, nil
	}
	if document.ID == "" {
		document = committed
	}
	return document, []v1.Warning{*warning}
}

// notImplemented is the 501 for a capability a route does not have.
func notImplemented(capability, message string) error {
	return output.NewError(output.CodeNotImplemented, message).
		WithDetails(map[string]any{"capability": capability})
}
