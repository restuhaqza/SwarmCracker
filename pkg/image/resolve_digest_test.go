package image

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveImageDigest_ReportsWhatTheTagPointsAt proves the recorded base
// image digest really comes from the registry and costs no layer download: the
// answer is the Docker-Content-Digest the registry sends for a HEAD.
func TestResolveImageDigest_ReportsWhatTheTagPointsAt(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a1b2c3d4", 8)

	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.URL.Path != "/v2/team/app/manifests/1.0" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		w.Header().Set("Docker-Content-Digest", digest)
		// A registry answers a manifest HEAD with the media type, the digest
		// and the manifest's length, and go-containerregistry refuses a HEAD
		// that omits any of them.
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	got, err := ResolveImageDigest(context.Background(), host+"/team/app:1.0", nil)
	require.NoError(t, err)
	assert.Equal(t, digest, got)
	assert.Equal(t, http.MethodHead, gotMethod, "resolving a digest must not fetch the image")
	assert.Equal(t, "/v2/team/app/manifests/1.0", gotPath)
}

func TestResolveImageDigest_RefusesWhatItCannotResolve(t *testing.T) {
	t.Run("an empty reference", func(t *testing.T) {
		_, err := ResolveImageDigest(context.Background(), "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})

	t.Run("a reference the registry does not have", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		host := strings.TrimPrefix(srv.URL, "http://")
		_, err := ResolveImageDigest(context.Background(), host+"/team/missing:1.0", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to resolve the digest")
	})
}
