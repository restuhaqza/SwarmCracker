package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// envRegistryAuthHelper marks the re-executed helper process used by
// TestEncodeRegistryAuthReadsDockerConfig. The default keychain caches the
// process home directory, so HOME/DOCKER_CONFIG can only be controlled reliably
// before that cache is populated — i.e. in a fresh process.
const (
	envRegistryAuthHelper    = "SWARMCRACKER_REGISTRYAUTH_HELPER"
	registryAuthHelperPrefix = "REGISTRYAUTH_HELPER_RESULT:"
)

// anonymousKeychain always reports "no credentials".
type anonymousKeychain struct{}

func (anonymousKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.Anonymous, nil
}

// errKeychain always fails to resolve.
type errKeychain struct{ err error }

func (e errKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return nil, e.err
}

func TestRegistryHostForRef(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		wantHost string
		wantErr  bool
	}{
		{name: "bare name", ref: "nginx", wantHost: "index.docker.io"},
		{name: "bare name with tag", ref: "nginx:1.27", wantHost: "index.docker.io"},
		{name: "implicit docker hub library", ref: "library/nginx:latest", wantHost: "index.docker.io"},
		{name: "explicit docker hub", ref: "docker.io/library/nginx:latest", wantHost: "index.docker.io"},
		{name: "custom registry", ref: "registry.example.com/team/app:1.0", wantHost: "registry.example.com"},
		{name: "custom registry with port", ref: "registry.example.com:5000/team/app", wantHost: "registry.example.com:5000"},
		{name: "digest reference", ref: "registry.example.com/team/app@sha256:" + strings.Repeat("a", 64), wantHost: "registry.example.com"},
		{name: "invalid empty ref", ref: "", wantErr: true},
		{name: "invalid uppercase repo", ref: "registry.example.com/Team/App", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, err := RegistryHostForRef(tt.ref)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.ref)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, host)
		})
	}
}

func TestEncodeRegistryAuthFor_NoCredentials(t *testing.T) {
	encoded, err := EncodeRegistryAuthFor("registry.example.com/team/app:1.0", anonymousKeychain{})

	require.Error(t, err)
	assert.Empty(t, encoded)
	assert.True(t, errors.Is(err, ErrNoRegistryCredentials), "error must wrap ErrNoRegistryCredentials, got %v", err)
	assert.Contains(t, err.Error(), "registry.example.com", "error must name the registry host")
	assert.Contains(t, err.Error(), "docker login", "error must say what to do")
}

func TestEncodeRegistryAuthFor_KeychainError(t *testing.T) {
	wantErr := errors.New("credential helper exploded")
	encoded, err := EncodeRegistryAuthFor("registry.example.com/team/app:1.0", errKeychain{err: wantErr})

	require.Error(t, err)
	assert.Empty(t, encoded)
	assert.True(t, errors.Is(err, wantErr), "error must wrap the keychain error, got %v", err)
	assert.Contains(t, err.Error(), "registry.example.com", "error must name the registry host")
	assert.NotContains(t, err.Error(), "no registry credentials found")
}

func TestEncodeRegistryAuthFor_InvalidRef(t *testing.T) {
	encoded, err := EncodeRegistryAuthFor("", anonymousKeychain{})

	require.Error(t, err)
	assert.Empty(t, encoded)
	assert.False(t, errors.Is(err, ErrNoRegistryCredentials))
}

func TestEncodeRegistryAuthFor_RoundTrip(t *testing.T) {
	ra := RegistryAuthFromConfig(authn.AuthConfig{Username: "alice", Password: "s3cr3t"})

	encoded, err := EncodeRegistryAuthFor("registry.example.com/team/app:1.0", ra.Keychain)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	// URL-safe base64, no padding surprises.
	assert.NotContains(t, encoded, "+")
	assert.NotContains(t, encoded, "/")

	decoded, err := DecodeRegistryAuth(encoded)
	require.NoError(t, err)
	assert.Equal(t, "alice", decoded.Username)
	assert.Equal(t, "s3cr3t", decoded.Password)

	// The decoded config, once wrapped, must authenticate as priority-1 keychain.
	wrapped := RegistryAuthFromConfig(decoded)
	auth, err := wrapped.Keychain.Resolve(mustRepoResource(t, "registry.example.com/team/app:1.0"))
	require.NoError(t, err)
	got, err := auth.Authorization()
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, "s3cr3t", got.Password)
}

func TestEncodeRegistryAuthFor_IdentityTokenOnly(t *testing.T) {
	ra := RegistryAuthFromConfig(authn.AuthConfig{IdentityToken: "id-token"})
	encoded, err := EncodeRegistryAuthFor("registry.example.com/team/app:1.0", ra.Keychain)
	require.NoError(t, err)

	decoded, err := DecodeRegistryAuth(encoded)
	require.NoError(t, err)
	assert.Equal(t, "id-token", decoded.IdentityToken)
}

func TestDecodeRegistryAuth(t *testing.T) {
	const payload = `{"username":"alice","password":"s3cr3t"}`

	tests := []struct {
		name    string
		encoded string
		wantErr bool
	}{
		{name: "url encoding", encoded: base64.URLEncoding.EncodeToString([]byte(payload))},
		{name: "standard encoding", encoded: base64.StdEncoding.EncodeToString([]byte(payload))},
		{name: "raw url encoding", encoded: base64.RawURLEncoding.EncodeToString([]byte(payload))},
		{name: "raw standard encoding", encoded: base64.RawStdEncoding.EncodeToString([]byte(payload))},
		{name: "malformed base64", encoded: "!!!not-base64!!!", wantErr: true},
		{name: "malformed json", encoded: base64.URLEncoding.EncodeToString([]byte(`{"username":`)), wantErr: true},
		{name: "empty payload", encoded: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := DecodeRegistryAuth(tt.encoded)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "X-Registry-Auth payload")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "alice", cfg.Username)
			assert.Equal(t, "s3cr3t", cfg.Password)
		})
	}
}

func TestRegistryAuthFromConfig(t *testing.T) {
	cfg := authn.AuthConfig{Username: "alice", Password: "s3cr3t"}

	ra := RegistryAuthFromConfig(cfg)
	require.NotNil(t, ra)
	require.NotNil(t, ra.Keychain, "config-derived RegistryAuth must set the priority-1 keychain")

	auth, err := ra.Keychain.Resolve(mustRepoResource(t, "registry.example.com/team/app:1.0"))
	require.NoError(t, err)
	got, err := auth.Authorization()
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, "s3cr3t", got.Password)

	// buildRemoteOptions must take the keychain branch (context + keychain only),
	// not fall back to basic auth or the default keychain.
	opts := buildRemoteOptions(context.Background(), ra)
	assert.Len(t, opts, 2)
}

func TestRegistryAuthFromConfig_EmptyConfigStillResolves(t *testing.T) {
	ra := RegistryAuthFromConfig(authn.AuthConfig{})

	auth, err := ra.Keychain.Resolve(mustRepoResource(t, "registry.example.com/team/app:1.0"))
	require.NoError(t, err)
	got, err := auth.Authorization()
	require.NoError(t, err)
	assert.Equal(t, authn.AuthConfig{}, *got)
}

// TestEncodeRegistryAuthReadsDockerConfig proves the standard Docker config
// path: a config.json containing an "auths" entry under DOCKER_CONFIG is read
// and its credentials are encoded.
func TestEncodeRegistryAuthReadsDockerConfig(t *testing.T) {
	if os.Getenv(envRegistryAuthHelper) == "1" {
		encoded, err := EncodeRegistryAuth("registry.example.com/team/app:1.0")
		result := map[string]string{"encoded": encoded}
		if err != nil {
			result["error"] = err.Error()
		}
		payload, mErr := json.Marshal(result)
		if mErr != nil {
			os.Exit(3)
		}
		fmt.Println(registryAuthHelperPrefix + string(payload))
		return
	}

	dir := t.TempDir()
	dockerAuth := base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
	configJSON := fmt.Sprintf(`{"auths":{"registry.example.com":{"auth":%q}}}`, dockerAuth)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte(configJSON), 0o600))

	cmd := exec.Command(os.Args[0], "-test.run=^TestEncodeRegistryAuthReadsDockerConfig$")
	cmd.Env = append(os.Environ(),
		envRegistryAuthHelper+"=1",
		"HOME="+dir,
		"DOCKER_CONFIG="+dir,
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "helper process failed:\n%s", out)

	var result map[string]string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, registryAuthHelperPrefix) {
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, registryAuthHelperPrefix)), &result))
			break
		}
	}
	require.NotNil(t, result, "helper produced no result:\n%s", out)
	require.Empty(t, result["error"])

	decoded, err := DecodeRegistryAuth(result["encoded"])
	require.NoError(t, err)
	assert.Equal(t, "alice", decoded.Username)
	assert.Equal(t, "s3cr3t", decoded.Password)
}

func mustRepoResource(t *testing.T, ref string) name.Repository {
	t.Helper()
	parsed, err := name.ParseReference(ref)
	require.NoError(t, err)
	return parsed.Context()
}
