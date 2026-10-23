package apihttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireCode asserts err is the typed failure with code.
func requireCode(t *testing.T, err error, code output.Code) *output.Error {
	t.Helper()
	var typed *output.Error
	require.Truef(t, errors.As(err, &typed), "want *output.Error, got %T: %v", err, err)
	assert.Equal(t, code, typed.Code, typed.Message)
	return typed
}

// requireField asserts err is invalid and names path in details.fields.
func requireField(t *testing.T, err error, path string) {
	t.Helper()
	typed := requireCode(t, err, output.CodeInvalid)
	fields, ok := typed.Details["fields"].(map[string]any)
	require.Truef(t, ok, "details.fields missing: %#v", typed.Details)
	assert.Contains(t, fields, path)
}

// idempotencyBody is one well-formed service create body.
func idempotencyBody(name string) []byte {
	return []byte(`{"name":"` + name + `","spec":{"task":{"image":"nginx:alpine"}}}`)
}

func TestIdempotencyKeyValidation(t *testing.T) {
	t.Run("trimmed", func(t *testing.T) {
		key, err := parseIdempotencyKey("  abc-123 \n")
		require.NoError(t, err)
		assert.Equal(t, "abc-123", key)
	})

	t.Run("empty means no idempotency", func(t *testing.T) {
		key, err := parseIdempotencyKey("   ")
		require.NoError(t, err)
		assert.Empty(t, key)
	})

	t.Run("too long", func(t *testing.T) {
		key, err := parseIdempotencyKey(string(bytes.Repeat([]byte{'a'}, maxIdempotencyKeyBytes+1)))
		assert.Empty(t, key)
		requireField(t, err, idempotencyKeyHeader)
	})

	t.Run("control character", func(t *testing.T) {
		key, err := parseIdempotencyKey("abc\x01def")
		assert.Empty(t, key)
		requireField(t, err, idempotencyKeyHeader)
	})

	t.Run("255 bytes is accepted", func(t *testing.T) {
		key, err := parseIdempotencyKey(string(bytes.Repeat([]byte{'a'}, maxIdempotencyKeyBytes)))
		require.NoError(t, err)
		assert.Len(t, key, maxIdempotencyKeyBytes)
	})
}

func TestIdempotencyStoreReplay(t *testing.T) {
	store := NewIdempotencyStore(0)

	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay, "a first request reserves the key")

	header := http.Header{}
	header.Set("Content-Type", MediaType)
	header.Set("ETag", `"7"`)
	store.Complete("key-1", "scope-a", http.StatusCreated, header, []byte(`{"ok":true}`+"\n"))

	replay, err = store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	require.NotNil(t, replay, "a completed key replays")
	assert.Equal(t, http.StatusCreated, replay.Status)
	assert.Equal(t, `"7"`, replay.Header.Get("ETag"))
	assert.Equal(t, MediaType, replay.Header.Get("Content-Type"))
	assert.Equal(t, `{"ok":true}`+"\n", string(replay.Body))

	// A replay is a copy: mutating it must not corrupt the store.
	replay.Body[0] = 'X'
	again, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, `{"ok":true}`+"\n", string(again.Body))
}

func TestIdempotencyStoreDifferentBodyIsReuse(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)

	replay, err := store.Begin("key-1", "scope-a", "hash-b")
	assert.Nil(t, replay)
	typed := requireCode(t, err, output.CodeConflict)
	assert.Equal(t, "idempotency_key_reuse", typed.Details["reason"])
	assert.False(t, typed.Retryable)
}

func TestIdempotencyStoreInFlight(t *testing.T) {
	store := NewIdempotencyStore(0)

	first, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	require.Nil(t, first)

	second, err := store.Begin("key-1", "scope-a", "hash-a")
	assert.Nil(t, second)
	typed := requireCode(t, err, output.CodeConflict)
	assert.Equal(t, "in_progress", typed.Details["reason"])
	assert.Equal(t, idempotencyRetryAfterMs, typed.Details["retryAfterMs"])
	assert.True(t, typed.Retryable, "an in-progress duplicate may retry once the first finishes")
}

func TestIdempotencyStoreDifferentScopeNeverCollides(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "token:one|POST|/v1/services", "hash-a")
	require.NoError(t, err)

	// Same key, same body, different token: a new reservation, not a replay.
	replay, err := store.Begin("key-1", "token:two|POST|/v1/services", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay)

	// Same key, same token, different path: also new.
	replay, err = store.Begin("key-1", "token:one|POST|/v1/configs", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay)
}

func TestIdempotencyStoreExpiry(t *testing.T) {
	store := NewIdempotencyStore(DefaultIdempotencyTTL)
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	store.Complete("key-1", "scope-a", http.StatusOK, http.Header{}, []byte("body"))

	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	require.NotNil(t, replay, "inside the TTL the response replays")

	now = now.Add(DefaultIdempotencyTTL + time.Second)
	replay, err = store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay, "an expired key is swept and reserved again")
	assert.Len(t, store.entries, 1, "the expired entry was replaced by a new reservation")
}

func TestIdempotencyStoreServerErrorIsNotStored(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	store.Complete("key-1", "scope-a", http.StatusInternalServerError, http.Header{}, []byte("boom"))

	// The reservation is gone, so the same key can run again.
	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay)
}

func TestIdempotencyStoreHeaderFiltering(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)

	header := http.Header{}
	header.Set("Content-Type", MediaType)
	header.Set("ETag", `"3"`)
	header.Set("Allow", "GET, POST")
	header.Set("Location", "/v1/services/svc-1")
	header.Set("Retry-After", "1")
	header.Set("X-Request-Id", "req_should_not_be_stored")
	header.Set("Idempotency-Replayed", "true")
	header.Set("X-Custom", "nope")
	store.Complete("key-1", "scope-a", http.StatusCreated, header, []byte("{}"))

	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	require.NotNil(t, replay)

	for _, name := range contractHeaders {
		assert.NotEmptyf(t, replay.Header.Get(name), "%s must survive", name)
	}
	assert.Empty(t, replay.Header.Get("X-Request-Id"), "the replay carries the current request id")
	assert.Empty(t, replay.Header.Get("Idempotency-Replayed"))
	assert.Empty(t, replay.Header.Get("X-Custom"), "only contract headers are stored")
}

func TestIdempotencyStoreAbort(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	store.Abort("key-1", "scope-a")

	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay, "an aborted reservation can be taken again")
}

// blockingCreateClient holds the first create inside the handler so a second
// request can arrive while the key is in flight. It is deliberately not the
// shared fakeClient: the assertion is about the middleware, not the fake.
type blockingCreateClient struct {
	*fakeClient
	entered chan struct{}
	release chan struct{}
}

func (b *blockingCreateClient) CreateService(ctx context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	b.fakeClient.createCalls++
	close(b.entered)
	<-b.release
	return b.fakeClient.createdService, nil
}

func TestIdempotencyMiddlewareReplay(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{
		ID: "svc-1", Name: "web", Revision: "7",
		Spec:   v1.ServiceSpec{Mode: "replicated", Task: v1.TaskSpec{Image: "nginx:alpine"}},
		Status: v1.ServiceStatus{DesiredTasks: 1, Converged: true},
	}}
	api := newWriteTestAPI(t, fc)

	body := idempotencyBody("web")
	first := api.doWrite(t, http.MethodPost, "/v1/services", body,
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.Empty(t, first.Header().Get(idempotencyReplayedHeader), "the first response is not a replay")
	assert.Equal(t, 1, fc.createCalls)

	second := api.doWrite(t, http.MethodPost, "/v1/services", body,
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "  key-1  "))
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())

	assert.Equal(t, "true", second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, first.Body.String(), second.Body.String(), "a replay is byte-identical")
	assert.Equal(t, first.Header().Get("ETag"), second.Header().Get("ETag"))
	assert.Equal(t, APIVersion, second.Header().Get(apiVersionHeader))

	assert.NotEmpty(t, first.Header().Get(requestIDHeader))
	assert.NotEmpty(t, second.Header().Get(requestIDHeader))
	assert.NotEqual(t, first.Header().Get(requestIDHeader), second.Header().Get(requestIDHeader),
		"the replay describes the current request, not the original")
	assert.NotContains(t, second.Body.String(), "req_")

	assert.Equal(t, 1, fc.createCalls, "a replay does not run the handler")
	assert.Len(t, api.records(t), 1, "a replay does not append a second audit record")
}

func TestIdempotencyMiddlewareDifferentBody(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("api"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())

	doc := decodeError(t, second)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
	assert.Equal(t, "idempotency_key_reuse", doc.Error.Details["reason"])
	assert.Equal(t, 1, fc.createCalls, "the second body never reached the handler")
	assert.Len(t, api.records(t), 1)
}

func TestIdempotencyMiddlewareConcurrentInFlight(t *testing.T) {
	blocking := &blockingCreateClient{
		fakeClient: &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"}},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	api := newWriteTestAPI(t, blocking)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
			withToken(api.operator), withContentType(MediaType),
			withHeader(idempotencyKeyHeader, "key-1"))
	}()

	<-blocking.entered

	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())

	doc := decodeError(t, second)
	assert.Equal(t, string(output.CodeConflict), doc.Error.Code)
	assert.Equal(t, "in_progress", doc.Error.Details["reason"])
	assert.Equal(t, float64(idempotencyRetryAfterMs), doc.Error.Details["retryAfterMs"])
	assert.True(t, doc.Error.Retryable)

	close(blocking.release)
	first := <-done
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.Empty(t, first.Header().Get(idempotencyReplayedHeader))

	assert.Equal(t, 1, blocking.createCalls, "only the first request reached the handler")
}

func TestIdempotencyMiddlewareServerErrorRetries(t *testing.T) {
	// failOnceClient fails the first create and succeeds afterwards, so the same
	// key must run the handler twice: a 5xx is never stored.
	fc := &flakyCreateClient{fakeClient: &fakeClient{
		createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"},
	}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusInternalServerError, first.Code, first.Body.String())

	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	assert.Empty(t, second.Header().Get(idempotencyReplayedHeader), "the retry is a real execution")
	assert.Equal(t, 2, fc.createCalls)
}

// flakyCreateClient fails the first create and succeeds afterwards.
type flakyCreateClient struct {
	*fakeClient
}

func (f *flakyCreateClient) CreateService(ctx context.Context, write v1.ServiceWrite, opts v1.WriteOptions) (v1.Service, error) {
	f.createCalls++
	if f.createCalls == 1 {
		return v1.Service{}, output.NewError(output.CodeInternal, "manager exploded")
	}
	return f.fakeClient.createdService, nil
}

func TestIdempotencyMiddlewareDryRunIsNotStored(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{Name: "web"}}
	api := newWriteTestAPI(t, fc)

	dry := api.doWrite(t, http.MethodPost, "/v1/services?dryRun=true", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, dry.Code, dry.Body.String())
	assert.Contains(t, dry.Body.String(), `"dryRun":true`)

	real := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, real.Code, real.Body.String())
	assert.Empty(t, real.Header().Get(idempotencyReplayedHeader),
		"a dry run writes nothing, so it cannot be replayed")
	assert.NotContains(t, real.Body.String(), `"dryRun"`)
}

func TestIdempotencyMiddlewareWrongContentTypeIsNotStored(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType("application/json"),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusUnsupportedMediaType, first.Code, first.Body.String())

	// A 415 was never stored, so a correct retry with the same key runs.
	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	assert.Empty(t, second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, 1, fc.createCalls)
}

func TestIdempotencyMiddlewareRollbackIsStored(t *testing.T) {
	fc := &fakeClient{rolledBackService: v1.Service{ID: "svc-1", Name: "web", Revision: "19"}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
		withToken(api.operator), withHeader("If-Match", `"18"`),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
		withToken(api.operator), withHeader("If-Match", `"18"`),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	assert.Equal(t, "true", second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.Equal(t, 1, fc.rollbackCalls)
	assert.Len(t, api.records(t), 1, "a replayed rollback is not audited twice")
}

func TestIdempotencyMiddlewareKeyIsPerToken(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, first.Code)

	// A different token with the same key is a different scope; it runs.
	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.token), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	assert.Empty(t, second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, 2, fc.createCalls)
}

func TestIdempotencyMiddlewareAuditCount(t *testing.T) {
	api := newWriteTestAPI(t, &fakeClient{
		createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"},
	})

	// Dry run: no record.
	dry := api.doWrite(t, http.MethodPost, "/v1/services?dryRun=true", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType))
	require.Equal(t, http.StatusCreated, dry.Code, dry.Body.String())
	assert.Empty(t, api.records(t))

	// Real create: one record.
	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-2"))
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Len(t, api.records(t), 1)

	// Replay: still one record.
	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-2"))
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())
	assert.Len(t, api.records(t), 1)
}

func TestIdempotencyMiddlewareInvalidKey(t *testing.T) {
	fc := &fakeClient{}
	api := newWriteTestAPI(t, fc)

	rec := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "bad\x01key"))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	fields, ok := doc.Error.Details["fields"].(map[string]any)
	require.True(t, ok, "details.fields missing: %#v", doc.Error.Details)
	assert.Contains(t, fields, idempotencyKeyHeader)
	assert.Zero(t, fc.createCalls)
}

// TestIdempotencyMiddlewarePanicAborts proves a handler panic releases the
// reservation: the next request with the same key is not told "in progress"
// forever. net/http recovers the panic at the listener, so the test does too.
func TestIdempotencyMiddlewarePanicAborts(t *testing.T) {
	store := NewIdempotencyStore(DefaultIdempotencyTTL)
	s := &Server{cfg: Config{Idempotency: store}}

	panicking := func(http.ResponseWriter, *http.Request) { panic("boom") }
	handler := s.idempotent(panicking)

	req := httptest.NewRequest(http.MethodPost, "/v1/services", bytes.NewReader(idempotencyBody("web")))
	req.Header.Set("Content-Type", MediaType)
	req.Header.Set(idempotencyKeyHeader, "key-1")

	func() {
		defer func() {
			require.NotNil(t, recover(), "the middleware must re-panic")
		}()
		handler(httptest.NewRecorder(), req)
	}()

	replay, err := store.Begin("key-1", idempotencyScope(req), bodyHashOf(t, idempotencyBody("web")))
	require.NoError(t, err)
	assert.Nil(t, replay, "the reservation was aborted")
}

// bodyHashOf mirrors the middleware's hash so the panic test can address the
// same entry.
func bodyHashOf(t *testing.T, body []byte) string {
	t.Helper()
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// TestIdempotencyStoreIsManagerLocal is documentation as a test: the store has
// no file, no shared state and no cluster dependency, so a second manager starts
// empty.
func TestIdempotencyStoreIsManagerLocal(t *testing.T) {
	a := NewIdempotencyStore(DefaultIdempotencyTTL)
	b := NewIdempotencyStore(DefaultIdempotencyTTL)

	_, err := a.Begin("key-1", "scope", "hash")
	require.NoError(t, err)
	a.Complete("key-1", "scope", http.StatusOK, http.Header{"Content-Type": {MediaType}}, []byte("{}"))

	replay, err := b.Begin("key-1", "scope", "hash")
	require.NoError(t, err)
	assert.Nil(t, replay, "keys live in one manager's memory only")
}

// The two media-type constants must stay distinct so the eligibility rule has
// something to compare, and the wire names are part of the contract.
func TestIdempotencyContractConstants(t *testing.T) {
	assert.NotEqual(t, MediaType, v1.MergePatchMediaType)
	assert.Equal(t, "Idempotency-Key", idempotencyKeyHeader)
	assert.Equal(t, "Idempotency-Replayed", idempotencyReplayedHeader)
	assert.Equal(t, 24*time.Hour, DefaultIdempotencyTTL)
}

// TestIdempotencyMiddlewareBadModifierDoesNotSpendKey pins the request-shape
// distinction: a malformed modifier is not a result and must not be replayed.
func TestIdempotencyMiddlewareBadModifierDoesNotSpendKey(t *testing.T) {
	fc := &fakeClient{createdService: v1.Service{ID: "svc-1", Name: "web", Revision: "7"}}
	api := newWriteTestAPI(t, fc)

	bad := api.doWrite(t, http.MethodPost, "/v1/services?dryRun=1", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusBadRequest, bad.Code, bad.Body.String())

	good := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusCreated, good.Code, good.Body.String())
	assert.Empty(t, good.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, 1, fc.createCalls)
}

// TestIdempotencyReplayedErrorUsesCurrentRequestID proves a replayed 4xx body is
// re-rendered, not echoed with the first request's requestId.
func TestIdempotencyReplayedErrorUsesCurrentRequestID(t *testing.T) {
	fc := &fakeClient{writeErr: output.NewError(output.CodeConflict, "service already exists")}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusConflict, first.Code, first.Body.String())

	fc.writeErr = nil
	second := api.doWrite(t, http.MethodPost, "/v1/services", idempotencyBody("web"),
		withToken(api.operator), withContentType(MediaType),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assert.Equal(t, "true", second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, 1, fc.createCalls, "the replay did not run the handler again")

	doc := decodeError(t, second)
	assert.Equal(t, second.Header().Get(requestIDHeader), doc.Error.RequestID,
		"the replayed error must carry the current request id")
	assert.NotEqual(t, first.Header().Get(requestIDHeader), second.Header().Get(requestIDHeader))
}

// TestIdempotencyMiddlewareRollbackWithContentType proves a client-supplied
// Content-Type does not disable idempotency on the bodyless rollback POST.
func TestIdempotencyMiddlewareRollbackWithContentType(t *testing.T) {
	fc := &fakeClient{rolledBackService: v1.Service{ID: "svc-1", Name: "web", Revision: "19"}}
	api := newWriteTestAPI(t, fc)

	first := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
		withToken(api.operator), withContentType(MediaType),
		withHeader("If-Match", `"18"`),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := api.doWrite(t, http.MethodPost, "/v1/services/web/rollback", nil,
		withToken(api.operator), withContentType(MediaType),
		withHeader("If-Match", `"18"`),
		withHeader(idempotencyKeyHeader, "key-1"))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Equal(t, "true", second.Header().Get(idempotencyReplayedHeader))
	assert.Equal(t, 1, fc.rollbackCalls)
}

// TestIdempotencyStorePreconditionRequiredIsNotStored pins the 428 rule: a
// missing If-Match is a correctable request shape, so the retry must run.
func TestIdempotencyStorePreconditionRequiredIsNotStored(t *testing.T) {
	store := NewIdempotencyStore(0)

	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	store.Complete("key-1", "scope-a", http.StatusPreconditionRequired,
		http.Header{"Content-Type": []string{MediaType}}, []byte(`{"kind":"Error"}`+"\n"))

	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay, "a 428 must not spend the key")
}

func TestIdempotencyStorePreconditionFailedIsNotStored(t *testing.T) {
	store := NewIdempotencyStore(0)
	_, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	store.Complete("key-1", "scope-a", http.StatusPreconditionFailed,
		http.Header{"Content-Type": []string{MediaType}}, []byte(`{"kind":"Error"}`+"\n"))
	replay, err := store.Begin("key-1", "scope-a", "hash-a")
	require.NoError(t, err)
	assert.Nil(t, replay, "a 412 must not spend the key")
}

func TestIdempotencyStoreIsBounded(t *testing.T) {
	store := NewIdempotencyStore(0)
	for i := 0; i < maxIdempotencyEntries+10; i++ {
		_, err := store.Begin(string(rune('a'+i%26))+string(rune(i)), "scope", "hash")
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, len(store.entries), maxIdempotencyEntries)
}
