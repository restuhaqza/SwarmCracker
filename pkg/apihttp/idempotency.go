// This file owns the Idempotency-Key store and the POST middleware that replays
// a completed response instead of running the handler twice.
//
// The store is manager-local and in memory (contract §7.2 and §12.2): v1 serves
// one manager, and HA is a phase-2 problem that has to replicate tokens, audit
// records and these keys together. Nothing here is shared between processes.
//
// Scope is the caller's identity plus the method and path, so two tokens using
// the same key never collide, and the same token retrying a different route
// starts a new reservation. The body hash is what separates "the same request
// again" from "a different request that reused the key".
package apihttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// DefaultIdempotencyTTL is how long a completed response can be replayed.
	DefaultIdempotencyTTL = 24 * time.Hour

	idempotencyKeyHeader      = "Idempotency-Key"
	idempotencyReplayedHeader = "Idempotency-Replayed"

	// maxIdempotencyKeyBytes bounds one key. The contract's minimum is a UUID
	// (36 bytes); 255 is the conventional ceiling for an opaque token.
	maxIdempotencyKeyBytes = 255

	// idempotencyRetryAfterMs is the hint carried by an in_progress conflict. It
	// is long enough for the first request's write to land and short enough that
	// a client retry loop does not sleep through the window.
	idempotencyRetryAfterMs = 250
)

// contractHeaders are the only response headers a replay carries. A request ID
// is per request and must be the current one, and Idempotency-Replayed describes
// the replay itself, so neither is ever stored.
var contractHeaders = []string{
	"Content-Type",
	"ETag",
	"Allow",
	"Location",
	"Retry-After",
}

// Replay is one stored response: exactly the status, contract headers and body
// the first request produced.
type Replay struct {
	Status int
	Header http.Header
	Body   []byte
}

// idempotencyEntry is one reservation. completed is false while the handler is
// still running, which is what turns a concurrent duplicate into in_progress
// rather than a second execution.
type idempotencyEntry struct {
	bodyHash  string
	completed bool
	expiresAt time.Time
	replay    Replay
}

// IdempotencyStore is the manager-local key store. It is safe for concurrent
// use and never touched outside this package.
type IdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]*idempotencyEntry
	ttl     time.Duration
	// now is injectable so the expiry test does not sleep.
	now func() time.Time
}

// NewIdempotencyStore returns an empty store. A non-positive ttl means
// DefaultIdempotencyTTL.
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}
	return &IdempotencyStore{
		entries: make(map[string]*idempotencyEntry),
		ttl:     ttl,
		now:     time.Now,
	}
}

// Begin reserves the key or returns a completed replay.
//
// The three outcomes are deliberate:
//   - no entry: reserve it and return (nil, nil);
//   - a completed entry with the same body hash: return a copy to replay;
//   - an in-flight or different-body entry: a typed conflict.
//
// Expired entries are swept on every Begin, so an idle store releases its keys
// when it is next used rather than needing a background goroutine.
func (s *IdempotencyStore) Begin(key, scope, bodyHash string) (*Replay, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.sweepLocked(now)

	entry, ok := s.entries[scopeKey(key, scope)]
	if ok {
		if entry.bodyHash != bodyHash {
			return nil, idempotencyKeyReuseError()
		}
		if !entry.completed {
			return nil, idempotencyInProgressError()
		}
		replay := cloneReplay(entry.replay)
		return &replay, nil
	}

	s.entries[scopeKey(key, scope)] = &idempotencyEntry{
		bodyHash:  bodyHash,
		expiresAt: now.Add(s.ttl),
	}
	return nil, nil
}

// Complete records the response for a reserved key.
//
// A 5xx deletes the reservation instead: the server failed, so the caller must
// be able to retry the same key rather than receive the failure forever. A key
// that is no longer present (expired mid-flight) is not resurrected.
func (s *IdempotencyStore) Complete(key, scope string, status int, header http.Header, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	composite := scopeKey(key, scope)
	if status >= http.StatusInternalServerError {
		delete(s.entries, composite)
		return
	}

	entry, ok := s.entries[composite]
	if !ok {
		return
	}
	entry.completed = true
	entry.expiresAt = s.now().Add(s.ttl)
	entry.replay = Replay{
		Status: status,
		Header: contractHeaderCopy(header),
		Body:   append([]byte(nil), body...),
	}
}

// Abort removes an in-flight reservation after a panic or an unrecorded path.
func (s *IdempotencyStore) Abort(key, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, scopeKey(key, scope))
}

// sweepLocked drops every expired entry. The caller holds the write lock.
func (s *IdempotencyStore) sweepLocked(now time.Time) {
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
}

// scopeKey combines the caller's scope with the key so a key is only ever
// compared inside the scope that produced it.
func scopeKey(key, scope string) string {
	return scope + "\x00" + key
}

// cloneReplay deep-copies a stored response so a caller cannot mutate the store.
func cloneReplay(replay Replay) Replay {
	out := Replay{Status: replay.Status, Body: append([]byte(nil), replay.Body...)}
	if replay.Header != nil {
		out.Header = replay.Header.Clone()
	}
	return out
}

// contractHeaderCopy keeps only the headers a replay is allowed to repeat.
func contractHeaderCopy(header http.Header) http.Header {
	out := make(http.Header, len(contractHeaders))
	for _, name := range contractHeaders {
		canonical := http.CanonicalHeaderKey(name)
		if values, ok := header[canonical]; ok && len(values) > 0 {
			out[canonical] = append([]string(nil), values...)
		}
	}
	return out
}

// idempotencyKeyReuseError is the 409 for a key that was already used with a
// different body. The key is spent: reusing it cannot mean "do something else".
func idempotencyKeyReuseError() error {
	return output.NewError(output.CodeConflict,
		"this Idempotency-Key was already used with a different request body").
		WithDetails(map[string]any{"reason": "idempotency_key_reuse"})
}

// idempotencyInProgressError is the 409 for a duplicate that arrives while the
// first request is still running. It is retryable: the first request may yet
// complete and answer the original response.
func idempotencyInProgressError() error {
	err := output.NewError(output.CodeConflict,
		"another request with this Idempotency-Key is still in progress")
	err.Retryable = true
	return err.WithDetails(map[string]any{
		"reason":       "in_progress",
		"retryAfterMs": idempotencyRetryAfterMs,
	})
}

// parseIdempotencyKey trims the header and validates what is left. An empty key
// means "no idempotency"; an oversized or control-character key is invalid
// rather than silently ignored, because ignoring it would make a retry unsafe.
func parseIdempotencyKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}
	if len(key) > maxIdempotencyKeyBytes {
		return "", invalidField(idempotencyKeyHeader, "must be at most 255 bytes")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return "", invalidField(idempotencyKeyHeader, "must not contain control characters")
		}
	}
	return key, nil
}

// idempotencyScope is the token + method + path identity of one request.
//
// The token id is the scope: two tokens that happen to choose the same UUID are
// independent callers. A local (Unix-socket) request has no token id, so its
// scope is just "local".
func idempotencyScope(r *http.Request) string {
	identity, _ := IdentityFrom(r.Context())
	actor := identity.Type
	if identity.TokenID != "" {
		actor += ":" + identity.TokenID
	}
	return actor + "|" + r.Method + "|" + r.URL.Path
}

// idempotencyEligible reports whether a POST may be stored.
//
// Only a create body (the v1 media type) is stored; a merge-patch or any other
// media type is a client mistake whose 415 must not become a replay. A rollback
// is a POST with no body and no content type, so the path is the exception that
// keeps "no Content-Type" from being treated as "not a create".
func idempotencyEligible(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/rollback")
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	return err == nil && strings.EqualFold(mediaType, MediaType)
}

// idempotent wraps a POST handler with the replay store.
//
// The body is buffered (under the same 8 MiB cap the handler enforces and then
// restored) so its hash identifies the request. The handler runs into a capture
// writer; a completed 4xx/2xx response is stored, a 5xx is not, and the response
// is copied to the real writer with the current request ID. A panic aborts the
// reservation and re-panics so the process keeps its normal panic behaviour.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store := s.idempotencyStore()
		if store == nil {
			h(w, r)
			return
		}

		key, err := parseIdempotencyKey(r.Header.Get(idempotencyKeyHeader))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if key == "" || !idempotencyEligible(r) {
			h(w, r)
			return
		}
		// A dry run writes nothing, so there is nothing to replay; the handler
		// still validates and reports its own bad dryRun value.
		if dryRun, _ := parseDryRun(r.URL.Query()); dryRun {
			h(w, r)
			return
		}

		body, ok := bufferIdempotentBody(r)
		if !ok {
			// Over the cap or unreadable: the handler answers 413/400, and that
			// answer must not be stored as if the request had been understood.
			h(w, r)
			return
		}

		sum := sha256.Sum256(body)
		scope := idempotencyScope(r)
		replay, err := store.Begin(key, scope, hex.EncodeToString(sum[:]))
		if err != nil {
			s.writeWriteError(w, r, err)
			return
		}
		if replay != nil {
			copyContractHeaders(w.Header(), replay.Header)
			w.Header().Set(idempotencyReplayedHeader, "true")
			w.WriteHeader(replay.Status)
			_, _ = w.Write(replay.Body)
			return
		}

		capture := newResponseCapture()
		panicked := true
		defer func() {
			if panicked {
				store.Abort(key, scope)
			}
		}()

		h(capture, r)
		panicked = false

		store.Complete(key, scope, capture.statusCode(), capture.header, capture.body.Bytes())

		copyContractHeaders(w.Header(), capture.header)
		w.WriteHeader(capture.statusCode())
		_, _ = w.Write(capture.body.Bytes())
	}
}

// idempotencyStore returns the configured store. New always installs one, so a
// nil store only appears on a hand-built Server, where the middleware passes
// through instead of writing to shared state.
func (s *Server) idempotencyStore() *IdempotencyStore {
	return s.cfg.Idempotency
}

// bufferIdempotentBody reads the request body up to the write cap and restores
// it for the handler. ok is false when the body is over the cap or unreadable:
// the handler then produces its own 413/400 and nothing is stored.
func bufferIdempotentBody(r *http.Request) (body []byte, ok bool) {
	if r.Body == nil {
		return nil, true
	}

	original := r.Body
	body, err := io.ReadAll(io.LimitReader(original, maxWriteBodyBytes+1))
	// Restore the stream (what was read plus whatever is left) before the
	// handler runs, whatever happened. The handler's MaxBytesReader will still
	// see an oversized body and answer 413.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), original))

	if err != nil || len(body) > maxWriteBodyBytes {
		return nil, false
	}
	return body, true
}

// responseCapture is the writer a stored handler runs into. It records the
// status, headers and body so Complete can store them and the middleware can
// then copy them to the real writer.
type responseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseCapture() *responseCapture {
	return &responseCapture{header: make(http.Header)}
}

func (c *responseCapture) Header() http.Header { return c.header }

func (c *responseCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *responseCapture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.body.Write(p)
}

// statusCode is the recorded status, defaulting to 200 for a handler that only
// wrote a body.
func (c *responseCapture) statusCode() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}

// copyContractHeaders copies a stored response's headers onto the live writer.
// The per-request headers wrap already set (version, request ID) are untouched.
func copyContractHeaders(dst, src http.Header) {
	for name, values := range src {
		if strings.EqualFold(name, requestIDHeader) {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}
