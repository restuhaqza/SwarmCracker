package apihttp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

var tokenIDPattern = regexp.MustCompile(`^tok_[0-9a-f]{8}$`)

// newTestStore returns an empty store whose file lives in a not-yet-existing
// subdirectory, so tests can assert what OpenTokenStore does and does not
// create.
func newTestStore(t *testing.T) (*TokenStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "api-tokens.json")
	store, err := OpenTokenStore(path)
	require.NoError(t, err)
	return store, path
}

func requireErrorCode(t *testing.T, err error) output.Code {
	t.Helper()
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	return typed.Code
}

func TestOpenTokenStoreMissingFileIsEmptyAndDoesNotCreateIt(t *testing.T) {
	store, path := newTestStore(t)

	assert.Empty(t, store.List())

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "OpenTokenStore must not create the file")
}

func TestCreateAndAuthenticate(t *testing.T) {
	store, _ := newTestStore(t)

	secret, tok, err := store.Create("  ci-reader  ", RoleReader)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(secret, "sc_"), "secret %q lacks the sc_ prefix", secret)
	assert.NotContains(t, secret, "=", "secret must be unpadded base64url")
	assert.Regexp(t, tokenIDPattern, tok.ID)
	assert.Equal(t, "ci-reader", tok.Name, "name must be trimmed")
	assert.Equal(t, RoleReader, tok.Role)
	assert.Empty(t, tok.Hash, "the returned token must not carry the hash")

	_, err = time.Parse(time.RFC3339, tok.CreatedAt)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(tok.CreatedAt, "Z"), "CreatedAt must be UTC: %q", tok.CreatedAt)

	got, ok := store.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, tok, got)
	assert.Empty(t, got.Hash)
}

func TestCreateSecretIs32RandomBytes(t *testing.T) {
	store, _ := newTestStore(t)

	secret, _, err := store.Create("reader", RoleReader)
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(secret, "sc_"))
	require.NoError(t, err)
	assert.Len(t, raw, 32)
}

func TestCreateGeneratesUniqueIDsAndSecrets(t *testing.T) {
	store, _ := newTestStore(t)

	ids := make(map[string]bool)
	secrets := make(map[string]bool)
	for i := 0; i < 16; i++ {
		secret, tok, err := store.Create("same-name", RoleAdmin)
		require.NoError(t, err)
		ids[tok.ID] = true
		secrets[secret] = true
	}
	assert.Len(t, ids, 16)
	assert.Len(t, secrets, 16)
}

func TestAuthenticateRejectsWrongSecret(t *testing.T) {
	store, _ := newTestStore(t)
	firstSecret, first, err := store.Create("reader", RoleReader)
	require.NoError(t, err)
	secondSecret, second, err := store.Create("other", RoleAdmin)
	require.NoError(t, err)

	cases := map[string]string{
		"empty":                  "",
		"prefix only":            "sc_",
		"unrelated":              "sc_" + strings.Repeat("A", 43),
		"hash instead of secret": hashSecret(firstSecret),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := store.Authenticate(candidate)
			assert.False(t, ok)
			assert.Zero(t, got)
		})
	}

	// Every secret authenticates as exactly its own token.
	if got, ok := store.Authenticate(firstSecret); assert.True(t, ok) {
		assert.Equal(t, first.ID, got.ID)
	}
	if got, ok := store.Authenticate(secondSecret); assert.True(t, ok) {
		assert.Equal(t, second.ID, got.ID)
	}
}

func TestCreateValidatesInput(t *testing.T) {
	cases := []struct {
		name      string
		tokenName string
		role      string
	}{
		{"empty name", "", RoleReader},
		{"blank name", "   ", RoleAdmin},
		{"unknown role", "reader", "root"},
		{"empty role", "reader", ""},
		{"case sensitive role", "reader", "Reader"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := newTestStore(t)

			secret, tok, err := store.Create(tc.tokenName, tc.role)
			require.Error(t, err)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
			assert.Empty(t, secret)
			assert.Zero(t, tok)
			assert.Empty(t, store.List())

			_, statErr := os.Stat(path)
			assert.True(t, os.IsNotExist(statErr), "a rejected create must not write the file")
		})
	}
}

func TestCreateAllowsDuplicateNames(t *testing.T) {
	store, _ := newTestStore(t)

	_, first, err := store.Create("same", RoleReader)
	require.NoError(t, err)
	_, second, err := store.Create("same", RoleAdmin)
	require.NoError(t, err)

	assert.NotEqual(t, first.ID, second.ID)
	assert.Len(t, store.List(), 2)
}

func TestListReturnsCopiesWithoutHashes(t *testing.T) {
	store, _ := newTestStore(t)
	_, tok, err := store.Create("x", RoleReader)
	require.NoError(t, err)

	list := store.List()
	require.Len(t, list, 1)
	assert.Equal(t, tok, list[0])
	assert.Empty(t, list[0].Hash)

	data, err := json.Marshal(list)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "hash")

	list[0].Name = "mutated"
	assert.Equal(t, "x", store.List()[0].Name, "List must not expose internal state")
}

func TestRevoke(t *testing.T) {
	store, path := newTestStore(t)
	revokedSecret, revoked, err := store.Create("revoke-me", RoleReader)
	require.NoError(t, err)
	keptSecret, kept, err := store.Create("keep-me", RoleAdmin)
	require.NoError(t, err)

	require.NoError(t, store.Revoke(revoked.ID))

	_, ok := store.Authenticate(revokedSecret)
	assert.False(t, ok)
	if got, ok := store.Authenticate(keptSecret); assert.True(t, ok) {
		assert.Equal(t, kept.ID, got.ID)
	}

	reloaded, err := OpenTokenStore(path)
	require.NoError(t, err)
	_, ok = reloaded.Authenticate(revokedSecret)
	assert.False(t, ok, "revocation must be persisted")
	_, ok = reloaded.Authenticate(keptSecret)
	assert.True(t, ok)

	list := reloaded.List()
	require.Len(t, list, 1)
	assert.Equal(t, kept.ID, list[0].ID)
}

func TestRevokeUnknownID(t *testing.T) {
	store, _ := newTestStore(t)
	_, _, err := store.Create("x", RoleReader)
	require.NoError(t, err)

	for _, id := range []string{"tok_deadbeef", ""} {
		err := store.Revoke(id)
		require.Error(t, err)
		assert.Equal(t, output.CodeNotFound, requireErrorCode(t, err))
	}
	assert.Len(t, store.List(), 1)
}

func TestReloadFromDisk(t *testing.T) {
	store, path := newTestStore(t)
	secret, tok, err := store.Create("reload", RoleAdmin)
	require.NoError(t, err)

	reloaded, err := OpenTokenStore(path)
	require.NoError(t, err)
	assert.Equal(t, []Token{tok}, reloaded.List())

	got, ok := reloaded.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, tok, got)
}

// TestReloadSeesExternalCreateAndRevoke covers the daemon's view of a token file
// the CLI mutates: the store was opened before the file existed and must pick up
// both a new token and its revocation without being reopened.
func TestReloadSeesExternalCreateAndRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenTokenStore(path)
	require.NoError(t, err)
	_, ok := store.Authenticate("sc_not-a-token")
	require.False(t, ok)

	// A second store stands in for the CLI running in another process.
	writer, err := OpenTokenStore(path)
	require.NoError(t, err)
	secret, tok, err := writer.Create("minted-elsewhere", RoleReader)
	require.NoError(t, err)

	got, ok := store.Authenticate(secret)
	require.True(t, ok, "authentication must see a token created after the store was opened")
	assert.Equal(t, tok, got)

	require.NoError(t, writer.Revoke(tok.ID))
	_, ok = store.Authenticate(secret)
	require.False(t, ok, "authentication must see a token revoked after the store was opened")

	require.NoError(t, store.Reload())
	assert.Empty(t, store.List())
}

func TestReloadKeepsTokensWhenFileIsUnchanged(t *testing.T) {
	store, path := newTestStore(t)
	secret, _, err := store.Create("stable", RoleReader)
	require.NoError(t, err)

	// A second OpenTokenStore stands in for another process that only read the
	// file; reloading must not lose the in-memory set.
	_, err = OpenTokenStore(path)
	require.NoError(t, err)
	require.NoError(t, store.Reload())

	_, ok := store.Authenticate(secret)
	assert.True(t, ok)
	assert.Len(t, store.List(), 1)
}

func TestFileAndDirectoryModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "tokens.json")

	store, err := OpenTokenStore(path)
	require.NoError(t, err)
	_, _, err = store.Create("x", RoleReader)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

func TestFileSchema(t *testing.T) {
	store, path := newTestStore(t)
	secret, tok, err := store.Create("x", RoleReader)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), secret, "the plaintext secret must never be stored")

	var raw struct {
		SchemaVersion int              `json:"schemaVersion"`
		Tokens        []map[string]any `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, 1, raw.SchemaVersion)
	require.Len(t, raw.Tokens, 1)
	assert.Equal(t, tok.ID, raw.Tokens[0]["id"])
	assert.Equal(t, tok.Name, raw.Tokens[0]["name"])
	assert.Equal(t, RoleReader, raw.Tokens[0]["role"])
	assert.Equal(t, tok.CreatedAt, raw.Tokens[0]["createdAt"])

	sum := sha256.Sum256([]byte(secret))
	assert.Equal(t, hex.EncodeToString(sum[:]), raw.Tokens[0]["hash"])
}

func TestOpenTokenStoreRejectsMalformedFiles(t *testing.T) {
	cases := map[string]string{
		"invalid json":    `{"schemaVersion":1,"tokens":`,
		"empty file":      "",
		"wrong version":   `{"schemaVersion":2,"tokens":[]}`,
		"missing version": `{"tokens":[]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

			store, err := OpenTokenStore(path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
		})
	}
}

func TestCreateLeavesStateUnchangedWhenWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "state")
	store, err := OpenTokenStore(filepath.Join(dir, "tokens.json"))
	require.NoError(t, err)
	_, _, err = store.Create("first", RoleReader)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	secret, tok, err := store.Create("second", RoleAdmin)
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, requireErrorCode(t, err))
	assert.Empty(t, secret)
	assert.Zero(t, tok)

	list := store.List()
	require.Len(t, list, 1)
	assert.Equal(t, "first", list[0].Name)
	assertNoTempFiles(t, dir)
}

func TestRevokeRestoresStateWhenWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "state")
	store, err := OpenTokenStore(filepath.Join(dir, "tokens.json"))
	require.NoError(t, err)
	secret, tok, err := store.Create("first", RoleReader)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = store.Revoke(tok.ID)
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, requireErrorCode(t, err))
	_, ok := store.Authenticate(secret)
	assert.True(t, ok, "a failed revoke must not drop the token")
	assertNoTempFiles(t, dir)
}

// assertNoTempFiles fails when a failed write left a temporary file behind.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tokens-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, matches, "failed writes must clean up their temporary files")
}
