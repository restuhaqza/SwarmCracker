// Package image provides registry authentication for OCI images.
package image

import (
	"context"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/rs/zerolog/log"
)

// RegistryAuth holds authentication configuration for registry access.
// Fields are checked in priority order: Keychain > Token > Username/Password.
// If no fields are set, falls back to the default keychain (Docker config, etc.).
type RegistryAuth struct {
	// Username for basic authentication
	Username string
	// Password for basic authentication
	Password string
	// Token for bearer/token authentication (takes precedence over Username/Password)
	Token string
	// Keychain for credential lookup (takes precedence over Token)
	Keychain authn.Keychain
}

// buildRemoteOptions builds remote.Options for registry operations based on auth configuration.
// The auth chain is: Keychain → Token → Basic auth → Default keychain fallback
func buildRemoteOptions(ctx context.Context, auth *RegistryAuth) []remote.Option {
	opts := []remote.Option{}

	// Always include context
	opts = append(opts, remote.WithContext(ctx))

	// Handle authentication in priority order
	if auth == nil {
		// No auth provided, use default keychain
		log.Debug().Msg("Using default keychain for authentication")
		opts = append(opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		return opts
	}

	// Priority 1: Custom keychain
	if auth.Keychain != nil {
		log.Debug().Msg("Using custom keychain for authentication")
		opts = append(opts, remote.WithAuthFromKeychain(auth.Keychain))
		return opts
	}

	// Priority 2: Bearer token
	if auth.Token != "" {
		log.Debug().Msg("Using bearer token for authentication")
		opts = append(opts, remote.WithAuth(&authn.Bearer{Token: auth.Token}))
		return opts
	}

	// Priority 3: Basic auth (username/password)
	if auth.Username != "" && auth.Password != "" {
		log.Debug().
			Str("username", auth.Username).
			Msg("Using basic authentication")
		opts = append(opts, remote.WithAuth(&authn.Basic{
			Username: auth.Username,
			Password: auth.Password,
		}))
		return opts
	}

	// Fallback: Default keychain (Docker config, credential helpers, etc.)
	log.Debug().Msg("No auth configured, falling back to default keychain")
	opts = append(opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	return opts
}

// NewRegistryAuth creates a RegistryAuth with basic authentication.
func NewRegistryAuth(username, password string) *RegistryAuth {
	return &RegistryAuth{
		Username: username,
		Password: password,
	}
}

// NewTokenAuth creates a RegistryAuth with bearer token authentication.
func NewTokenAuth(token string) *RegistryAuth {
	return &RegistryAuth{
		Token: token,
	}
}

// NewKeychainAuth creates a RegistryAuth with a custom keychain.
func NewKeychainAuth(keychain authn.Keychain) *RegistryAuth {
	return &RegistryAuth{
		Keychain: keychain,
	}
}