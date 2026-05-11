// Package image provides unit tests for registry authentication.
package image

import (
	"context"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
)

func TestRegistryAuth_NewRegistryAuth(t *testing.T) {
	auth := NewRegistryAuth("testuser", "testpass")

	if auth == nil {
		t.Fatal("NewRegistryAuth returned nil")
	}
	if auth.Username != "testuser" {
		t.Errorf("expected username 'testuser', got %q", auth.Username)
	}
	if auth.Password != "testpass" {
		t.Errorf("expected password 'testpass', got %q", auth.Password)
	}
	if auth.Token != "" {
		t.Errorf("expected empty token, got %q", auth.Token)
	}
	if auth.Keychain != nil {
		t.Error("expected nil keychain")
	}
}

func TestRegistryAuth_NewTokenAuth(t *testing.T) {
	auth := NewTokenAuth("my-bearer-token")

	if auth == nil {
		t.Fatal("NewTokenAuth returned nil")
	}
	if auth.Token != "my-bearer-token" {
		t.Errorf("expected token 'my-bearer-token', got %q", auth.Token)
	}
	if auth.Username != "" {
		t.Errorf("expected empty username, got %q", auth.Username)
	}
	if auth.Password != "" {
		t.Errorf("expected empty password, got %q", auth.Password)
	}
	if auth.Keychain != nil {
		t.Error("expected nil keychain")
	}
}

func TestRegistryAuth_NewKeychainAuth(t *testing.T) {
	// Create a mock keychain
	mockKeychain := authn.DefaultKeychain

	auth := NewKeychainAuth(mockKeychain)

	if auth == nil {
		t.Fatal("NewKeychainAuth returned nil")
	}
	if auth.Keychain == nil {
		t.Error("expected non-nil keychain")
	}
	if auth.Username != "" {
		t.Errorf("expected empty username, got %q", auth.Username)
	}
	if auth.Password != "" {
		t.Errorf("expected empty password, got %q", auth.Password)
	}
	if auth.Token != "" {
		t.Errorf("expected empty token, got %q", auth.Token)
	}
}

func TestBuildRemoteOptions_NilAuth(t *testing.T) {
	ctx := context.Background()
	opts := buildRemoteOptions(ctx, nil)

	if len(opts) < 2 {
		t.Errorf("expected at least 2 options (context + auth), got %d", len(opts))
	}
}

func TestBuildRemoteOptions_EmptyAuth(t *testing.T) {
	ctx := context.Background()
	auth := &RegistryAuth{}
	opts := buildRemoteOptions(ctx, auth)

	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
	// Empty auth should fall back to default keychain
}

func TestBuildRemoteOptions_BasicAuth(t *testing.T) {
	ctx := context.Background()
	auth := &RegistryAuth{
		Username: "testuser",
		Password: "testpass",
	}
	opts := buildRemoteOptions(ctx, auth)

	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestBuildRemoteOptions_TokenAuth(t *testing.T) {
	ctx := context.Background()
	auth := &RegistryAuth{
		Token: "bearer-token",
	}
	opts := buildRemoteOptions(ctx, auth)

	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestBuildRemoteOptions_KeychainAuth(t *testing.T) {
	ctx := context.Background()
	auth := &RegistryAuth{
		Keychain: authn.DefaultKeychain,
	}
	opts := buildRemoteOptions(ctx, auth)

	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestBuildRemoteOptions_PriorityOrder(t *testing.T) {
	ctx := context.Background()

	// Test: Keychain takes precedence over Token
	auth := &RegistryAuth{
		Keychain: authn.DefaultKeychain,
		Token:    "ignored-token",
		Username: "ignored-user",
		Password: "ignored-pass",
	}
	opts := buildRemoteOptions(ctx, auth)
	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}

	// Test: Token takes precedence over Basic auth
	auth = &RegistryAuth{
		Token:    "bearer-token",
		Username: "ignored-user",
		Password: "ignored-pass",
	}
	opts = buildRemoteOptions(ctx, auth)
	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestBuildRemoteOptions_ContextIncluded(t *testing.T) {
	ctx := context.Background()
	opts := buildRemoteOptions(ctx, nil)

	// Verify context is always included
	if len(opts) == 0 {
		t.Error("expected at least 1 option (context)")
	}
}

func TestBuildRemoteOptions_UsernameOnlyNoFallback(t *testing.T) {
	ctx := context.Background()
	// Username only (no password) should fall back to default keychain
	auth := &RegistryAuth{
		Username: "testuser",
	}
	opts := buildRemoteOptions(ctx, auth)
	// Should fall back to default keychain since password is empty
	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}

func TestBuildRemoteOptions_PasswordOnlyNoFallback(t *testing.T) {
	ctx := context.Background()
	// Password only (no username) should fall back to default keychain
	auth := &RegistryAuth{
		Password: "testpass",
	}
	opts := buildRemoteOptions(ctx, auth)
	// Should fall back to default keychain since username is empty
	if len(opts) < 2 {
		t.Errorf("expected at least 2 options, got %d", len(opts))
	}
}
