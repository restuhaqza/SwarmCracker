package image

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// ErrNoRegistryCredentials reports that no credential could be found for the
// registry serving an image. Callers wrap it with the registry host so the CLI
// can print an actionable message.
var ErrNoRegistryCredentials = errors.New("no registry credentials found")

// EncodeRegistryAuth resolves credentials for ref from the default docker
// config (or credential helper) and returns the base64url X-Registry-Auth
// payload for ContainerSpec.PullOptions.RegistryAuth.
func EncodeRegistryAuth(ref string) (string, error) {
	return EncodeRegistryAuthFor(ref, authn.DefaultKeychain)
}

// EncodeRegistryAuthFor is EncodeRegistryAuth with an explicit keychain; a nil
// keychain means authn.DefaultKeychain. Test seam.
func EncodeRegistryAuthFor(ref string, kc authn.Keychain) (string, error) {
	if kc == nil {
		kc = authn.DefaultKeychain
	}

	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", ref, err)
	}
	host := parsed.Context().RegistryStr()

	// Resolve against the repository resource so credential helpers see both
	// the registry and the repository, matching docker's own lookup.
	authenticator, err := kc.Resolve(parsed.Context())
	if err != nil {
		return "", fmt.Errorf("resolving credentials for %s: %w", host, err)
	}

	cfg, err := authenticator.Authorization()
	if err != nil {
		return "", fmt.Errorf("resolving credentials for %s: %w", host, err)
	}
	if cfg == nil || (cfg.Username == "" && cfg.Password == "" && cfg.IdentityToken == "" && cfg.RegistryToken == "") {
		return "", fmt.Errorf("%w for %s: run `docker login %s`", ErrNoRegistryCredentials, host, host)
	}

	payload, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("encoding registry credentials for %s: %w", host, err)
	}

	return base64.URLEncoding.EncodeToString(payload), nil
}

// RegistryHostForRef reports the registry host that serves ref
// ("index.docker.io" for a bare name).
func RegistryHostForRef(ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", ref, err)
	}
	return parsed.Context().RegistryStr(), nil
}

// DecodeRegistryAuth parses an X-Registry-Auth payload.
func DecodeRegistryAuth(encoded string) (authn.AuthConfig, error) {
	decoded, err := decodeBase64(encoded)
	if err != nil {
		return authn.AuthConfig{}, fmt.Errorf("decoding X-Registry-Auth payload: %w", err)
	}

	var cfg authn.AuthConfig
	if err := json.Unmarshal(decoded, &cfg); err != nil {
		return authn.AuthConfig{}, fmt.Errorf("parsing X-Registry-Auth payload: %w", err)
	}
	return cfg, nil
}

// decodeBase64 accepts both URL-safe and standard base64, with or without
// padding, because clients emit either form for X-Registry-Auth.
func decodeBase64(encoded string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.URLEncoding,
		base64.StdEncoding,
		base64.RawURLEncoding,
		base64.RawStdEncoding,
	}

	var err error
	for _, enc := range encodings {
		var decoded []byte
		if decoded, err = enc.DecodeString(encoded); err == nil {
			return decoded, nil
		}
	}
	return nil, err
}

// RegistryAuthFromConfig wraps a decoded config so buildRemoteOptions uses it as
// its priority-1 keychain, keeping go-containerregistry's own field precedence.
func RegistryAuthFromConfig(cfg authn.AuthConfig) *RegistryAuth {
	return &RegistryAuth{Keychain: staticKeychain{cfg: cfg}}
}

// staticKeychain is an authn.Keychain that always returns cfg, regardless of
// the resource it is asked about.
type staticKeychain struct {
	cfg authn.AuthConfig
}

// Resolve implements authn.Keychain.
func (s staticKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return authn.FromConfig(s.cfg), nil
}
