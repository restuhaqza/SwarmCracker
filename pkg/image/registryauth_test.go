package image

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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
	tests := []struct {
		name string
		cfg  authn.AuthConfig
	}{
		{
			name: "basic credentials",
			cfg:  authn.AuthConfig{Username: "alice", Password: "s3cr3t"},
		},
		{
			// "alice:pw?" is standard base64 "YWxpY2U6cHc/" but URL-safe base64
			// "YWxpY2U6cHc_", so this case also fails if the "auth" field ever
			// switches to the URL alphabet — which docker cannot decode, since it
			// reads that field with base64.StdEncoding.
			name: "credentials whose auth field needs the standard alphabet",
			cfg:  authn.AuthConfig{Username: "alice", Password: "pw?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ra := RegistryAuthFromConfig(tt.cfg)

			encoded, err := EncodeRegistryAuthFor("registry.example.com/team/app:1.0", ra.Keychain)
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			// URL-safe base64, no padding surprises.
			assert.NotContains(t, encoded, "+")
			assert.NotContains(t, encoded, "/")

			// Pin docker's wire format rather than only re-reading our own output
			// with our own decoder: docker parses X-Registry-Auth as a flat JSON
			// object with these exact key spellings, and its "auth" field is
			// standard base64 of "username:password".
			var payload map[string]string
			require.NoError(t, json.Unmarshal(decodeXRegistryAuthPayload(t, encoded), &payload),
				"X-Registry-Auth payload must be base64 of a flat JSON object")
			assert.Equal(t, tt.cfg.Username, payload["username"])
			assert.Equal(t, tt.cfg.Password, payload["password"])
			assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(tt.cfg.Username+":"+tt.cfg.Password)), payload["auth"],
				`docker's "auth" field is standard base64 of "username:password"`)

			decoded, err := DecodeRegistryAuth(encoded)
			require.NoError(t, err)
			assert.Equal(t, tt.cfg.Username, decoded.Username)
			assert.Equal(t, tt.cfg.Password, decoded.Password)

			// The decoded config, once wrapped, must authenticate as priority-1 keychain.
			wrapped := RegistryAuthFromConfig(decoded)
			auth, err := wrapped.Keychain.Resolve(mustRepoResource(t, "registry.example.com/team/app:1.0"))
			require.NoError(t, err)
			got, err := auth.Authorization()
			require.NoError(t, err)
			assert.Equal(t, tt.cfg.Username, got.Username)
			assert.Equal(t, tt.cfg.Password, got.Password)
		})
	}
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

	// Which buildRemoteOptions branch runs is decided by exactly these fields. A
	// non-nil Keychain selects the priority-1 branch; the empty Username,
	// Password and Token rule out every lower-priority branch. (Counting the
	// returned []remote.Option would be vacuous: every branch returns context
	// plus exactly one more.)
	assert.NotNil(t, ra.Keychain)
	assert.Empty(t, ra.Username, "basic auth must not be set")
	assert.Empty(t, ra.Password, "basic auth must not be set")
	assert.Empty(t, ra.Token, "bearer auth must not be set")
}

// TestRegistryAuthFromConfig_KeychainAuthenticatesRequest drives the options
// buildRemoteOptions derives from a config-backed RegistryAuth against a local
// registry that requires basic auth, proving the priority-1 keychain does not
// merely get selected but actually authenticates requests.
func TestRegistryAuthFromConfig_KeychainAuthenticatesRequest(t *testing.T) {
	const (
		username = "alice"
		password = "s3cr3t"
	)

	var (
		mu      sync.Mutex
		gotAuth []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok {
			mu.Lock()
			gotAuth = append(gotAuth, user+":"+pass)
			mu.Unlock()
		}
		if r.URL.Path == "/v2/" {
			// Challenging the ping with Basic (rather than answering 200) makes
			// go-containerregistry use its basic-auth transport, which replays
			// the authenticator's credentials on every subsequent request.
			w.Header().Set("WWW-Authenticate", `Basic realm="swarmcracker-test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !ok || user != username || pass != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	ref, err := name.ParseReference(host + "/team/app:1.0")
	require.NoError(t, err)

	ra := RegistryAuthFromConfig(authn.AuthConfig{Username: username, Password: password})
	desc, err := remote.Get(ref, buildRemoteOptions(context.Background(), ra)...)
	require.NoError(t, err, "the config-backed keychain must authenticate against a registry that requires basic auth")
	require.NotNil(t, desc)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{username + ":" + password}, gotAuth,
		"the priority-1 keychain must put the resolved credentials on the wire")
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

// decodeXRegistryAuthPayload base64-decodes an X-Registry-Auth payload without
// going through DecodeRegistryAuth, so wire-format assertions inspect the JSON
// document independently of the package's own decoder. Both standard and
// URL-safe base64 are accepted because clients emit either for this header.
func decodeXRegistryAuthPayload(t *testing.T, encoded string) []byte {
	t.Helper()
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(encoded); err == nil {
			return raw
		}
	}
	t.Fatalf("X-Registry-Auth payload %q is not valid base64", encoded)
	return nil
}

func mustRepoResource(t *testing.T, ref string) name.Repository {
	t.Helper()
	parsed, err := name.ParseReference(ref)
	require.NoError(t, err)
	return parsed.Context()
}
