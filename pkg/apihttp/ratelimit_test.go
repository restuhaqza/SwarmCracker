package apihttp

import (
	"net/http"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimiterBucket(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(1, 2)
	limiter.now = func() time.Time { return now }

	allowed, _ := limiter.Allow("token:a")
	assert.True(t, allowed)
	allowed, _ = limiter.Allow("token:a")
	assert.True(t, allowed)

	allowed, retry := limiter.Allow("token:a")
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, retry, 900*time.Millisecond)

	// Another token has its own bucket.
	allowed, _ = limiter.Allow("token:b")
	assert.True(t, allowed)

	now = now.Add(time.Second)
	allowed, _ = limiter.Allow("token:a")
	assert.True(t, allowed, "one token refills after one second")
}

func TestRateLimitMiddleware(t *testing.T) {
	fc := &fakeClient{}
	api := newWriteTestAPI(t, fc)
	// Rebuild with a one-request bucket for the operator token only.
	tokenStore, err := OpenTokenStore(t.TempDir() + "/tokens.json")
	require.NoError(t, err)
	operator, _, err := tokenStore.Create("deploy", RoleOperator)
	require.NoError(t, err)
	reader, _, err := tokenStore.Create("dashboard", RoleReader)
	require.NoError(t, err)
	srv, err := New(Config{
		Client:    fc,
		Tokens:    tokenStore,
		RateLimit: NewRateLimiter(0.001, 1),
	})
	require.NoError(t, err)
	api.testAPI.handler = srv.Handler()
	api.testAPI.token = reader
	api.operator = operator
	api.reader = reader

	first := api.do(t, http.MethodGet, "/v1/version", withToken(api.operator))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := api.do(t, http.MethodGet, "/v1/version", withToken(api.operator))
	require.Equal(t, http.StatusTooManyRequests, second.Code, second.Body.String())
	assert.NotEmpty(t, second.Header().Get("Retry-After"))
	doc := decodeError(t, second)
	assert.Equal(t, string(output.CodeRateLimited), doc.Error.Code)
	assert.True(t, doc.Error.Retryable)
	assert.Contains(t, doc.Error.Details, "retryAfterMs")

	// A different token is unaffected.
	other := api.do(t, http.MethodGet, "/v1/version", withToken(api.reader))
	assert.Equal(t, http.StatusOK, other.Code, other.Body.String())
}
