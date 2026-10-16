package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeTaskAuth builds an X-Registry-Auth payload the way the manager's CLI
// does, so authForTask is exercised against the real wire format.
func encodeTaskAuth(t *testing.T, cfg authn.AuthConfig) string {
	t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	return base64.URLEncoding.EncodeToString(raw)
}

func TestAuthForTask(t *testing.T) {
	t.Run("empty task auth returns the fallback unchanged", func(t *testing.T) {
		fallback := &RegistryAuth{Username: "fallback", Password: "pw"}

		got, err := authForTask("", fallback)
		require.NoError(t, err)
		assert.Same(t, fallback, got)
	})

	t.Run("empty task auth and nil fallback stays nil", func(t *testing.T) {
		got, err := authForTask("", nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("task auth wins over the fallback", func(t *testing.T) {
		fallback := &RegistryAuth{Username: "fallback", Password: "pw"}
		cfg := authn.AuthConfig{Username: "bob", Password: "secret"}

		got, err := authForTask(encodeTaskAuth(t, cfg), fallback)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.NotSame(t, fallback, got, "the task payload must take precedence")

		ref, err := name.ParseReference("registry.example.com/team/app:1.0")
		require.NoError(t, err)
		authenticator, err := got.Keychain.Resolve(ref.Context())
		require.NoError(t, err)
		decoded, err := authenticator.Authorization()
		require.NoError(t, err)
		assert.Equal(t, "bob", decoded.Username)
		assert.Equal(t, "secret", decoded.Password)
	})

	t.Run("malformed base64 is an error, not a silent fallback", func(t *testing.T) {
		fallback := &RegistryAuth{Username: "fallback", Password: "pw"}

		got, err := authForTask("!!!not-base64!!!", fallback)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "decoding task registry credentials")
	})

	t.Run("malformed JSON is an error, not a silent fallback", func(t *testing.T) {
		encoded := base64.URLEncoding.EncodeToString([]byte("not json"))

		got, err := authForTask(encoded, nil)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "decoding task registry credentials")
	})
}

// TestPrepareImage_MalformedTaskRegistryAuthFails proves the preparer fails the
// prepare on an undecodable task payload before it reaches the registry, rather
// than silently falling back to its global config.
func TestPrepareImage_MalformedTaskRegistryAuthFails(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.prepareImage(context.Background(), "alpine:latest", "id", t.TempDir()+"/out.ext4", "!!!not-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving registry credentials")
}

// TestExtractWithGGCR_MalformedTaskRegistryAuthFails covers the second call site
// of authForTask: the pull path must reject a malformed payload too.
func TestExtractWithGGCR_MalformedTaskRegistryAuthFails(t *testing.T) {
	ip := NewImagePreparer(&PreparerConfig{RootfsDir: t.TempDir()}).(*ImagePreparer)

	err := ip.extractWithGGCR(context.Background(), "alpine:latest", t.TempDir(), "!!!not-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving registry credentials")
}
