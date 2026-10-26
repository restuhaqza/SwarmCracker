package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// redirectTarget starts a plaintext HTTP server that records every hit. Plain
// HTTP is deliberate: a client that followed the redirect would replay the
// bearer token without TLS, which is the exact failure the policy prevents.
func redirectTarget(t *testing.T) (*httptest.Server, *atomic.Int32, chan http.Header) {
	t.Helper()

	var hits atomic.Int32
	headers := make(chan http.Header, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case headers <- r.Header.Clone():
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	return target, &hits, headers
}

// redirectingServer answers every request with a 302 to target and reports the
// Authorization header the first request carried.
func redirectingServer(t *testing.T, target string) (*Client, chan string) {
	t.Helper()

	initialAuth := make(chan string, 4)
	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		initialAuth <- r.Header.Get("Authorization")
		w.Header().Set("Location", target+r.URL.Path)
		w.WriteHeader(http.StatusFound)
	})
	return c, initialAuth
}

// TestRemoteClientsRefuseRedirects pins the policy on all three clients: one
// shared helper, so a redirect can never silently become a followed hop on a
// client that only differs in timeout or protocol negotiation.
func TestRemoteClientsRefuseRedirects(t *testing.T) {
	c := newTestRemote(t, Config{Token: "s3cret"}, func(http.ResponseWriter, *http.Request) {})

	for name, client := range map[string]*http.Client{
		"http":   c.remote.http,
		"stream": c.remote.stream,
		"attach": c.remote.attach,
	} {
		require.NotNilf(t, client.CheckRedirect, "%s must refuse redirects", name)
		assert.ErrorIsf(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse, name)
	}
}

// TestRemoteDocumentGetDoesNotFollowRedirect proves a single-document GET stops
// at the 3xx: the redirect target is never contacted, so the bearer token is
// never replayed there, and the caller sees the status through the normal error
// mapping rather than a document decoded out of a redirect body.
func TestRemoteDocumentGetDoesNotFollowRedirect(t *testing.T) {
	target, hits, targetHeaders := redirectTarget(t)
	c, initialAuth := redirectingServer(t, target.URL)

	_, err := c.ClusterID(context.Background())
	require.Error(t, err)

	// A redirect is not a typed server document, so the status mapping applies.
	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
	assert.Contains(t, typed.Message, "302")

	// The token really was on the first request, so the negative check below is
	// meaningful.
	assert.Equal(t, "Bearer s3cret", <-initialAuth)

	// Redirect handling is synchronous inside Client.Do, so by the time the call
	// returned a followed hop would already have landed.
	assert.Zero(t, hits.Load(), "no request may follow the redirect")
	select {
	case header := <-targetHeaders:
		t.Fatalf("the redirect target was contacted with Authorization %q", header.Get("Authorization"))
	default:
	}
}

// TestRemoteStreamDoesNotFollowRedirect proves the event stream client behaves
// the same way: a 302 is an error, not a stream, and the target stays untouched.
func TestRemoteStreamDoesNotFollowRedirect(t *testing.T) {
	target, hits, targetHeaders := redirectTarget(t)
	c, initialAuth := redirectingServer(t, target.URL)

	events, errs, err := c.WatchEvents(context.Background(), v1.EventOptions{})
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Nil(t, errs)

	typed := codeOf(t, err)
	assert.Equal(t, output.CodeInternal, typed.Code)
	assert.Contains(t, typed.Message, "302")

	assert.Equal(t, "Bearer s3cret", <-initialAuth)

	assert.Zero(t, hits.Load(), "no request may follow the redirect")
	select {
	case header := <-targetHeaders:
		t.Fatalf("the redirect target was contacted with Authorization %q", header.Get("Authorization"))
	default:
	}
}
