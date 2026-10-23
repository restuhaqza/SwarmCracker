// Package apihttp hosts the manager's programmatic HTTPS API.
//
// This file owns the manager-local bearer token store: a JSON file that holds
// only SHA-256 hashes of the secrets handed out at creation time. Tokens are
// created and revoked by the CLI and verified by the HTTP API, so both paths
// share this package.
package apihttp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// RoleReader may read the v1 API.
	RoleReader = "reader"
	// RoleAdmin may read the v1 API and manage tokens.
	RoleAdmin = "admin"

	schemaVersion = 1

	secretPrefix  = "sc_"
	tokenIDPrefix = "tok_"

	secretBytes  = 32
	tokenIDBytes = 4

	tokenFileMode = 0o600
	tokenDirMode  = 0o700
)

// Token is a stored API token. Hash is kept in memory for authentication only;
// it is never serialized or printed.
type Token struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Hash      string `json:"-"` // never serialized or printed
}

// diskToken is the on-disk representation and the only place the hash lives.
type diskToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Hash      string `json:"hash"`
	CreatedAt string `json:"createdAt"`
}

// tokenFile is the schema of the token file.
type tokenFile struct {
	SchemaVersion int         `json:"schemaVersion"`
	Tokens        []diskToken `json:"tokens"`
}

// TokenStore is a token set backed by a single JSON file. It is safe for
// concurrent use; every mutation is persisted before it returns.
type TokenStore struct {
	mu     sync.RWMutex
	path   string
	tokens []Token
}

// OpenTokenStore loads path, or returns an empty store when the file does not
// exist. It does not create the file until the first mutation.
func OpenTokenStore(path string) (*TokenStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, output.NewError(output.CodeInvalid, "token store path must not be empty")
	}

	s := &TokenStore{path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, output.NewErrorf(output.CodeInternal, "read token store %s: %v", path, err)
	}

	var file tokenFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, output.NewErrorf(output.CodeInvalid, "parse token store %s: %v", path, err)
	}
	if file.SchemaVersion != schemaVersion {
		return nil, output.NewErrorf(output.CodeInvalid,
			"token store %s: unsupported schemaVersion %d (want %d)", path, file.SchemaVersion, schemaVersion)
	}

	tokens := make([]Token, 0, len(file.Tokens))
	for _, dt := range file.Tokens {
		tokens = append(tokens, Token{
			ID:        dt.ID,
			Name:      dt.Name,
			Role:      dt.Role,
			CreatedAt: dt.CreatedAt,
			Hash:      dt.Hash,
		})
	}
	s.tokens = tokens
	return s, nil
}

// Create validates name/role, generates the secret and persists the token.
// The returned secret is the only time the plaintext is available.
func (s *TokenStore) Create(name, role string) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Token{}, output.NewError(output.CodeInvalid, "token name must not be empty")
	}
	if role != RoleReader && role != RoleAdmin {
		return "", Token{}, output.NewErrorf(output.CodeInvalid,
			"unsupported token role %q (want %q or %q)", role, RoleReader, RoleAdmin)
	}

	id, err := newTokenID()
	if err != nil {
		return "", Token{}, output.NewErrorf(output.CodeInternal, "generate token ID: %v", err)
	}
	secret, err := newSecret()
	if err != nil {
		return "", Token{}, output.NewErrorf(output.CodeInternal, "generate token secret: %v", err)
	}

	tok := Token{
		ID:        id,
		Name:      name,
		Role:      role,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Hash:      hashSecret(secret),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens = append(s.tokens, tok)
	if err := s.persistLocked(); err != nil {
		s.tokens = s.tokens[:len(s.tokens)-1]
		return "", Token{}, err
	}

	public := tok
	public.Hash = ""
	return secret, public, nil
}

// Authenticate hashes secret and constant-time compares it against the stored
// hashes. An empty secret never authenticates.
func (s *TokenStore) Authenticate(secret string) (Token, bool) {
	if secret == "" {
		return Token{}, false
	}
	want := hashSecret(secret)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, tok := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(want), []byte(tok.Hash)) == 1 {
			public := tok
			public.Hash = ""
			return public, true
		}
	}
	return Token{}, false
}

// List returns tokens without hashes, in creation order.
func (s *TokenStore) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Token, 0, len(s.tokens))
	for _, tok := range s.tokens {
		tok.Hash = ""
		out = append(out, tok)
	}
	return out
}

// Revoke removes a token by ID and persists the change. An unknown ID is
// not_found, and a failed write leaves the store unchanged.
func (s *TokenStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, tok := range s.tokens {
		if tok.ID != id {
			continue
		}

		kept := make([]Token, 0, len(s.tokens)-1)
		kept = append(kept, s.tokens[:i]...)
		kept = append(kept, s.tokens[i+1:]...)

		s.tokens = kept
		if err := s.persistLocked(); err != nil {
			restored := make([]Token, 0, len(kept)+1)
			restored = append(restored, kept[:i]...)
			restored = append(restored, tok)
			restored = append(restored, kept[i:]...)
			s.tokens = restored
			return err
		}
		return nil
	}

	return output.NewErrorf(output.CodeNotFound, "token %q not found", id)
}

// persistLocked writes the store to disk. The caller must hold s.mu.
func (s *TokenStore) persistLocked() error {
	file := tokenFile{
		SchemaVersion: schemaVersion,
		Tokens:        make([]diskToken, 0, len(s.tokens)),
	}
	for _, tok := range s.tokens {
		file.Tokens = append(file.Tokens, diskToken{
			ID:        tok.ID,
			Name:      tok.Name,
			Role:      tok.Role,
			Hash:      tok.Hash,
			CreatedAt: tok.CreatedAt,
		})
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "encode token store: %v", err)
	}
	data = append(data, '\n')

	if err := writeFileAtomic(s.path, data); err != nil {
		return output.NewErrorf(output.CodeInternal, "write token store %s: %v", s.path, err)
	}
	return nil
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory, fsyncs it and renames it into place.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, tokenDirMode); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}

	if err := tmp.Chmod(tokenFileMode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// newTokenID returns "tok_" plus 8 random hex characters.
func newTokenID() (string, error) {
	buf := make([]byte, tokenIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return tokenIDPrefix + hex.EncodeToString(buf), nil
}

// newSecret returns "sc_" plus unpadded base64url of 32 random bytes.
func newSecret() (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashSecret returns the lowercase hex SHA-256 of the full secret.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
