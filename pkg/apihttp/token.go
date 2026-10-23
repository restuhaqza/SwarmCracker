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
	// schemaVersion is the token file schema this build writes. Version 2 adds
	// scopes, task selectors, expiry and last-used metadata.
	schemaVersion = 2
	// schemaVersionPhase1 is the read-only token schema: role only, scopes
	// expanded from the role on load.
	schemaVersionPhase1 = 1

	secretPrefix  = "sc_"
	tokenIDPrefix = "tok_"

	secretBytes  = 32
	tokenIDBytes = 4

	tokenFileMode = 0o600
	tokenDirMode  = 0o700
)

// Token is a stored API token. Hash is kept in memory for authentication only;
// it is never serialized or printed.
//
// Scopes is the effective authorization set: the role expansion, narrowed by
// TokenOptions.Scopes at mint time. TaskSelectors restricts which tasks a runner
// may touch. ExpiresAt is RFC3339 UTC; empty means never.
type Token struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Role          string         `json:"role"`
	CreatedAt     string         `json:"createdAt"`
	Scopes        []string       `json:"scopes,omitempty"`
	TaskSelectors *TaskSelectors `json:"taskSelectors,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	LastUsedAt    string         `json:"lastUsedAt,omitempty"`
	Hash          string         `json:"-"` // never serialized or printed
}

// diskToken is the on-disk representation and the only place the hash lives.
type diskToken struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Role          string         `json:"role"`
	Hash          string         `json:"hash"`
	CreatedAt     string         `json:"createdAt"`
	Scopes        []string       `json:"scopes,omitempty"`
	TaskSelectors *TaskSelectors `json:"taskSelectors,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	LastUsedAt    string         `json:"lastUsedAt,omitempty"`
}

// tokenFile is the schema of the token file.
type tokenFile struct {
	SchemaVersion int         `json:"schemaVersion"`
	Tokens        []diskToken `json:"tokens"`
}

// TokenStore is a token set backed by a single JSON file. It is safe for
// concurrent use; every mutation is persisted before it returns.
//
// The file is shared with other processes: the CLI mints and revokes tokens
// while the daemon serves requests. Authentication therefore re-reads the file
// when it changed on disk, so a token created after start-up is accepted without
// a restart. Create and Revoke persist the whole in-memory set; a long-lived
// store that shares the file with another writer should Reload before mutating.
type TokenStore struct {
	mu     sync.RWMutex
	path   string
	tokens []Token
	// loaded is the identity of the file the in-memory set came from; nil means
	// the file did not exist at the last load. It is what makes a reload cheap:
	// an unchanged file skips the read and the JSON parse.
	loaded os.FileInfo
}

// OpenTokenStore loads path, or returns an empty store when the file does not
// exist. It does not create the file until the first mutation.
func OpenTokenStore(path string) (*TokenStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, output.NewError(output.CodeInvalid, "token store path must not be empty")
	}

	s := &TokenStore{path: path}
	// s is not shared yet, so no lock is needed here.
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload re-reads the token file when it changed since the last load, so tokens
// another process created or revoked take effect. Authentication calls it on
// every request; call it directly when a caller needs the refresh up front.
func (s *TokenStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reloadLocked()
}

// reloadLocked replaces the in-memory set with the file's contents when the file
// changed. The caller must hold s.mu for writing, or own s exclusively.
func (s *TokenStore) reloadLocked() error {
	// Stat before reading: if the file is replaced between the two calls the
	// cached identity stays older than the content, so the next reload repeats
	// the work instead of missing the change.
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A missing file is an empty set: revoking by deleting the file takes
			// effect like any other change.
			s.tokens = nil
			s.loaded = nil
			return nil
		}
		return output.NewErrorf(output.CodeInternal, "read token store %s: %v", s.path, err)
	}
	if s.loaded != nil && unchangedFile(s.loaded, info) {
		return nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return output.NewErrorf(output.CodeInternal, "read token store %s: %v", s.path, err)
	}
	tokens, err := parseTokenFile(data, s.path)
	if err != nil {
		return err
	}

	s.tokens = tokens
	s.loaded = info
	return nil
}

// unchangedFile reports whether info still describes the cached file. Every
// write is an atomic rename, so a replaced file has a new identity; size and
// modification time cover an in-place rewrite.
func unchangedFile(cached, info os.FileInfo) bool {
	return os.SameFile(cached, info) &&
		cached.Size() == info.Size() &&
		cached.ModTime().Equal(info.ModTime())
}

// parseTokenFile decodes one token file and validates its schema. Both the
// phase-1 schema and the current one are accepted; a phase-1 record carries no
// scopes, so its role is expanded here.
//
// The role is the upper bound for a record's scopes whatever the file says: an
// explicit scope set is re-validated with the same rules as the mint path, and a
// record carrying an unknown or out-of-role scope fails the load. Otherwise a
// restored or hand-edited token file could grant authority its role never
// carries — a reader with tokens:admin, say — and List/audit would describe a
// role that does not match the token's real power.
//
// An unknown role fails the load too: guessing a scope set for a role nobody
// defined would authorize something the operator never granted.
func parseTokenFile(data []byte, path string) ([]Token, error) {
	var file tokenFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, output.NewErrorf(output.CodeInvalid, "parse token store %s: %v", path, err)
	}
	switch file.SchemaVersion {
	case schemaVersionPhase1, schemaVersion:
	default:
		return nil, output.NewErrorf(output.CodeInvalid,
			"token store %s: unsupported schemaVersion %d (want %d or %d)",
			path, file.SchemaVersion, schemaVersionPhase1, schemaVersion)
	}

	tokens := make([]Token, 0, len(file.Tokens))
	for _, dt := range file.Tokens {
		tok := Token{
			ID:         dt.ID,
			Name:       dt.Name,
			Role:       dt.Role,
			CreatedAt:  dt.CreatedAt,
			ExpiresAt:  dt.ExpiresAt,
			LastUsedAt: dt.LastUsedAt,
			Hash:       dt.Hash,
		}

		roleScopes, err := RoleScopes(tok.Role)
		if err != nil {
			return nil, output.NewErrorf(output.CodeInvalid,
				"token store %s: token %q: %v", path, tok.ID, err)
		}

		scopes := roleScopes
		if len(dt.Scopes) > 0 {
			if scopes, err = resolveScopes(tok.Role, roleScopes, dt.Scopes); err != nil {
				return nil, output.NewErrorf(output.CodeInvalid,
					"token store %s: token %q: %v", path, tok.ID, err)
			}
		}
		tok.Scopes = scopeStrings(scopes)

		// A loaded selector goes through the same normalization as a minted one,
		// so an entry that was hand-edited to be blank cannot match by accident.
		selectors, err := normalizeSelectors(dt.TaskSelectors)
		if err != nil {
			return nil, output.NewErrorf(output.CodeInvalid,
				"token store %s: token %q: %v", path, tok.ID, err)
		}
		tok.TaskSelectors = selectors

		tokens = append(tokens, tok)
	}
	return tokens, nil
}

// scopeStrings renders a scope set for storage.
func scopeStrings(scopes []Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		out = append(out, string(scope))
	}
	return out
}

// cloneToken returns a copy that shares no slice or map with the stored record,
// so a caller of List or Authenticate cannot mutate the in-memory set (and have
// the next persist write the mutation to disk).
func cloneToken(tok Token) Token {
	out := tok
	out.Scopes = append([]string(nil), tok.Scopes...)
	if tok.TaskSelectors != nil {
		selectors := *tok.TaskSelectors
		selectors.ServiceIDs = append([]string(nil), tok.TaskSelectors.ServiceIDs...)
		selectors.TaskIDs = append([]string(nil), tok.TaskSelectors.TaskIDs...)
		if tok.TaskSelectors.Labels != nil {
			selectors.Labels = make(map[string]string, len(tok.TaskSelectors.Labels))
			for key, value := range tok.TaskSelectors.Labels {
				selectors.Labels[key] = value
			}
		}
		out.TaskSelectors = &selectors
	}
	return out
}

// TokenOptions narrows what a role grants. The zero value means "the full role
// set, no selector, never expires".
type TokenOptions struct {
	// Scopes narrows the role: every requested scope must be part of it. Nil or
	// empty keeps the whole role set.
	Scopes []string
	// TaskSelectors restricts which tasks a runner may reach. A runner token
	// without one is rejected.
	TaskSelectors *TaskSelectors
	// ExpiresAt is an RFC3339 instant; empty means never expires.
	ExpiresAt string
}

// Create validates name/role, generates the secret and persists the token. It is
// CreateWithOptions with no narrowing, kept for existing callers.
func (s *TokenStore) Create(name, role string) (string, Token, error) {
	return s.CreateWithOptions(name, role, TokenOptions{})
}

// CreateWithOptions validates name, role and options, generates the secret and
// persists the token. The returned secret is the only time the plaintext is
// available.
func (s *TokenStore) CreateWithOptions(name, role string, opts TokenOptions) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Token{}, output.NewError(output.CodeInvalid, "token name must not be empty")
	}

	roleScopes, err := RoleScopes(role)
	if err != nil {
		return "", Token{}, err
	}
	scopes, err := resolveScopes(role, roleScopes, opts.Scopes)
	if err != nil {
		return "", Token{}, err
	}
	selectors, err := normalizeSelectors(opts.TaskSelectors)
	if err != nil {
		return "", Token{}, err
	}
	if role == RoleRunner && (selectors == nil || selectors.Empty()) {
		return "", Token{}, output.NewError(output.CodeInvalid,
			"runner tokens require at least one task selector (--service-id, --task-id or --selector-label)")
	}

	expiresAt := strings.TrimSpace(opts.ExpiresAt)
	if expiresAt != "" {
		at, parseErr := time.Parse(time.RFC3339, expiresAt)
		if parseErr != nil {
			return "", Token{}, output.NewErrorf(output.CodeInvalid,
				"expires-at must be an RFC3339 timestamp: %v", parseErr)
		}
		if !at.After(time.Now()) {
			return "", Token{}, output.NewError(output.CodeInvalid, "expires-at must be in the future")
		}
		expiresAt = at.UTC().Format(time.RFC3339)
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
		ID:            id,
		Name:          name,
		Role:          role,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Scopes:        scopeStrings(scopes),
		TaskSelectors: selectors,
		ExpiresAt:     expiresAt,
		Hash:          hashSecret(secret),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens = append(s.tokens, tok)
	if err := s.persistLocked(); err != nil {
		s.tokens = s.tokens[:len(s.tokens)-1]
		return "", Token{}, err
	}

	public := cloneToken(tok)
	public.Hash = ""
	return secret, public, nil
}

// resolveScopes narrows allowed by requested. Every requested scope must be part
// of the role: a token can never hold more than its role grants.
func resolveScopes(role string, allowed []Scope, requested []string) ([]Scope, error) {
	if len(requested) == 0 {
		return append([]Scope(nil), allowed...), nil
	}

	granted := make(map[Scope]bool, len(allowed))
	for _, scope := range allowed {
		granted[scope] = true
	}

	out := make([]Scope, 0, len(requested))
	seen := make(map[Scope]bool, len(requested))
	for _, raw := range requested {
		scope := Scope(strings.TrimSpace(raw))
		if !KnownScope(string(scope)) {
			return nil, output.NewErrorf(output.CodeInvalid, "unknown scope %q", raw)
		}
		if !granted[scope] {
			return nil, output.NewErrorf(output.CodeInvalid,
				"scope %q is not granted by role %q", raw, role)
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	return out, nil
}

// normalizeSelectors validates and copies a selector set. An empty or all-empty
// input becomes nil, which means "no restriction"; every stored entry is trimmed
// and non-empty, so a selector never matches by accident.
func normalizeSelectors(in *TaskSelectors) (*TaskSelectors, error) {
	if in == nil {
		return nil, nil
	}

	out := &TaskSelectors{}
	for _, id := range in.ServiceIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, output.NewError(output.CodeInvalid, "task selector service id must not be empty")
		}
		out.ServiceIDs = append(out.ServiceIDs, id)
	}
	for _, id := range in.TaskIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, output.NewError(output.CodeInvalid, "task selector task id must not be empty")
		}
		out.TaskIDs = append(out.TaskIDs, id)
	}
	if len(in.Labels) > 0 {
		out.Labels = make(map[string]string, len(in.Labels))
		for key, value := range in.Labels {
			key = strings.TrimSpace(key)
			if key == "" {
				return nil, output.NewError(output.CodeInvalid, "task selector label key must not be empty")
			}
			out.Labels[key] = value
		}
	}

	if out.Empty() {
		return nil, nil
	}
	return out, nil
}

// Authenticate hashes secret and constant-time compares it against the stored
// hashes. An empty secret never authenticates.
//
// The store is refreshed from disk first, so a token minted by another process
// (the CLI) works immediately. A refresh failure keeps the last good set: an
// unreadable token file must never authenticate an unknown secret, and it must
// not lock a reader out of the tokens it already knows either.
//
// An expired token never authenticates. Its record stays in the file so
// `api token list` still shows it; only the secret stops working.
//
// LastUsedAt is updated in memory and never written here: persisting it on every
// request would turn every read into a disk write. The next Create or Revoke
// writes the value current at that moment, and a reload from disk may drop it.
func (s *TokenStore) Authenticate(secret string) (Token, bool) {
	if secret == "" {
		return Token{}, false
	}
	want := hashSecret(secret)

	s.mu.Lock()
	_ = s.reloadLocked()
	defer s.mu.Unlock()

	now := time.Now()
	for i := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(want), []byte(s.tokens[i].Hash)) != 1 {
			continue
		}
		if tokenExpired(s.tokens[i].ExpiresAt, now) {
			return Token{}, false
		}

		s.tokens[i].LastUsedAt = now.UTC().Format(time.RFC3339)
		public := cloneToken(s.tokens[i])
		public.Hash = ""
		return public, true
	}
	return Token{}, false
}

// tokenExpired reports whether expiresAt is at or before now. An unparseable
// value counts as expired: a token whose expiry cannot be evaluated must never
// authenticate.
func tokenExpired(expiresAt string, now time.Time) bool {
	if expiresAt == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return true
	}
	return !at.After(now)
}

// List returns tokens without hashes, in creation order. It reports the set this
// process holds; call Reload to see changes another process made.
func (s *TokenStore) List() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Token, 0, len(s.tokens))
	for _, tok := range s.tokens {
		public := cloneToken(tok)
		public.Hash = ""
		out = append(out, public)
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
			ID:            tok.ID,
			Name:          tok.Name,
			Role:          tok.Role,
			Hash:          tok.Hash,
			CreatedAt:     tok.CreatedAt,
			Scopes:        tok.Scopes,
			TaskSelectors: tok.TaskSelectors,
			ExpiresAt:     tok.ExpiresAt,
			LastUsedAt:    tok.LastUsedAt,
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

	// Record the file this set was just written to, so the next authentication
	// does not re-read it. A failed stat simply forces that re-read.
	if info, err := os.Stat(s.path); err == nil {
		s.loaded = info
	} else {
		s.loaded = nil
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

	tmpPath, err := writeTempFile(dir, ".tokens-*.tmp", data, tokenFileMode)
	if err != nil {
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
