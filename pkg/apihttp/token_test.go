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
	assert.Empty(t, got.Hash)
	assert.Equal(t, tok.ID, got.ID)
	assert.Equal(t, tok.Name, got.Name)
	assert.Equal(t, tok.Role, got.Role)
	assert.Equal(t, tok.CreatedAt, got.CreatedAt)
	assert.Equal(t, tok.Scopes, got.Scopes)
	assert.NotEmpty(t, got.LastUsedAt, "authentication records the last use")
	_, err = time.Parse(time.RFC3339, got.LastUsedAt)
	require.NoError(t, err)
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
	assert.Equal(t, tok.ID, got.ID)
	assert.Equal(t, tok.Scopes, got.Scopes)
	assert.NotEmpty(t, got.LastUsedAt)
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
	assert.Equal(t, tok.ID, got.ID)
	assert.Equal(t, tok.Scopes, got.Scopes)

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
	assert.Equal(t, 2, raw.SchemaVersion)
	require.Len(t, raw.Tokens, 1)
	assert.Equal(t, tok.ID, raw.Tokens[0]["id"])
	assert.Equal(t, tok.Name, raw.Tokens[0]["name"])
	assert.Equal(t, RoleReader, raw.Tokens[0]["role"])
	assert.Equal(t, tok.CreatedAt, raw.Tokens[0]["createdAt"])

	roleScopes, err := RoleScopes(RoleReader)
	require.NoError(t, err)
	assert.ElementsMatch(t, scopeStrings(roleScopes), raw.Tokens[0]["scopes"])

	sum := sha256.Sum256([]byte(secret))
	assert.Equal(t, hex.EncodeToString(sum[:]), raw.Tokens[0]["hash"])
}

func TestOpenTokenStoreRejectsMalformedFiles(t *testing.T) {
	cases := map[string]string{
		"invalid json":    `{"schemaVersion":1,"tokens":`,
		"empty file":      "",
		"wrong version":   `{"schemaVersion":3,"tokens":[]}`,
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

// TestCreateUsesTheFullRoleScopeSet pins the default: a role creates a token
// that holds exactly what the role grants.
func TestCreateUsesTheFullRoleScopeSet(t *testing.T) {
	for _, role := range []string{RoleReader, RoleRunner, RoleOperator, RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			store, _ := newTestStore(t)

			opts := TokenOptions{TaskSelectors: &TaskSelectors{ServiceIDs: []string{"svc-1"}}}
			_, tok, err := store.CreateWithOptions("ci", role, opts)
			require.NoError(t, err)

			want, err := RoleScopes(role)
			require.NoError(t, err)
			assert.ElementsMatch(t, scopeStrings(want), tok.Scopes)
		})
	}
}

func TestCreateWithOptionsNarrowsScopes(t *testing.T) {
	store, path := newTestStore(t)

	secret, tok, err := store.CreateWithOptions("ci", RoleOperator, TokenOptions{
		Scopes: []string{"services:read", "services:write", "services:read"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"services:read", "services:write"}, tok.Scopes,
		"requested scopes are deduplicated and keep their requested order")
	assert.NotContains(t, tok.Scopes, string(ScopeTasksExec),
		"a narrowed token must not keep the rest of the role")

	got, ok := store.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, tok.Scopes, got.Scopes)

	// The narrowing survives a reload.
	reopened, err := OpenTokenStore(path)
	require.NoError(t, err)
	list := reopened.List()
	require.Len(t, list, 1)
	assert.Equal(t, tok.Scopes, list[0].Scopes)
}

func TestCreateWithOptionsRejectsScopesOutsideTheRole(t *testing.T) {
	cases := []struct {
		name   string
		role   string
		scopes []string
	}{
		{"reader cannot write", RoleReader, []string{"services:write"}},
		{"reader cannot read values", RoleReader, []string{"configs:read-values"}},
		{"runner cannot write", RoleRunner, []string{"services:write"}},
		{"operator cannot audit", RoleOperator, []string{"audit:read"}},
		{"operator cannot administer tokens", RoleOperator, []string{"tokens:admin"}},
		{"unknown scope", RoleAdmin, []string{"services:delete"}},
		{"empty scope", RoleAdmin, []string{"  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := newTestStore(t)

			secret, tok, err := store.CreateWithOptions("ci", tc.role, TokenOptions{Scopes: tc.scopes})
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

func TestCreateRunnerRequiresSelector(t *testing.T) {
	cases := map[string]*TaskSelectors{
		"nil selector":   nil,
		"empty selector": {},
		"blank ids":      {ServiceIDs: []string{"   "}},
	}
	for name, selectors := range cases {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestStore(t)

			_, _, err := store.CreateWithOptions("ci", RoleRunner, TokenOptions{TaskSelectors: selectors})
			require.Error(t, err)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
			assert.Contains(t, err.Error(), "selector")
		})
	}

	t.Run("non-runner may omit selectors", func(t *testing.T) {
		for _, role := range []string{RoleOperator, RoleAdmin} {
			store, _ := newTestStore(t)
			_, tok, err := store.CreateWithOptions("ops", role, TokenOptions{})
			require.NoError(t, err)
			assert.Nil(t, tok.TaskSelectors, "no selector means all tasks")
		}
	})
}

func TestCreateWithOptionsNormalizesSelectors(t *testing.T) {
	store, path := newTestStore(t)

	_, tok, err := store.CreateWithOptions("ci", RoleRunner, TokenOptions{
		TaskSelectors: &TaskSelectors{
			ServiceIDs: []string{"  svc-1  "},
			TaskIDs:    []string{"task-1"},
			Labels:     map[string]string{" team ": "payments"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, tok.TaskSelectors)
	assert.Equal(t, []string{"svc-1"}, tok.TaskSelectors.ServiceIDs)
	assert.Equal(t, []string{"task-1"}, tok.TaskSelectors.TaskIDs)
	assert.Equal(t, map[string]string{"team": "payments"}, tok.TaskSelectors.Labels)

	// The selector is part of the on-disk record.
	reopened, err := OpenTokenStore(path)
	require.NoError(t, err)
	list := reopened.List()
	require.Len(t, list, 1)
	assert.Equal(t, tok.TaskSelectors, list[0].TaskSelectors)
}

func TestCreateWithOptionsRejectsEmptySelectorEntries(t *testing.T) {
	cases := map[string]*TaskSelectors{
		"empty service id": {ServiceIDs: []string{""}},
		"empty task id":    {TaskIDs: []string{"  "}},
		"empty label key":  {Labels: map[string]string{"": "payments"}},
	}
	for name, selectors := range cases {
		t.Run(name, func(t *testing.T) {
			store, _ := newTestStore(t)
			_, _, err := store.CreateWithOptions("ci", RoleOperator, TokenOptions{TaskSelectors: selectors})
			require.Error(t, err)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
		})
	}
}

func TestCreateWithOptionsExpiry(t *testing.T) {
	store, path := newTestStore(t)

	future := time.Now().Add(time.Hour).UTC()

	t.Run("stored canonically in UTC", func(t *testing.T) {
		offset := future.In(time.FixedZone("plus2", 2*3600)).Format(time.RFC3339)
		_, tok, err := store.CreateWithOptions("expiring", RoleReader, TokenOptions{ExpiresAt: offset})
		require.NoError(t, err)
		assert.Equal(t, future.Format(time.RFC3339), tok.ExpiresAt)
		assert.True(t, strings.HasSuffix(tok.ExpiresAt, "Z"), tok.ExpiresAt)
	})

	t.Run("past is rejected", func(t *testing.T) {
		_, _, err := store.CreateWithOptions("expired", RoleReader, TokenOptions{
			ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		})
		require.Error(t, err)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	})

	t.Run("malformed is rejected", func(t *testing.T) {
		_, _, err := store.CreateWithOptions("bad", RoleReader, TokenOptions{ExpiresAt: "tomorrow"})
		require.Error(t, err)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	})

	reopened, err := OpenTokenStore(path)
	require.NoError(t, err)
	list := reopened.List()
	require.Len(t, list, 1)
	assert.Equal(t, future.Format(time.RFC3339), list[0].ExpiresAt)
}

// TestAuthenticateRejectsExpiredToken writes the record directly: Create refuses
// an expiry in the past, and the load path must still fail closed when time
// catches up with a token minted earlier.
func TestAuthenticateRejectsExpiredToken(t *testing.T) {
	const secret = "sc_expiredtoken"

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, path, tokenFile{
		SchemaVersion: schemaVersion,
		Tokens: []diskToken{{
			ID:        "tok_00000001",
			Name:      "expired",
			Role:      RoleReader,
			Hash:      hashSecret(secret),
			CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		}},
	})

	store, err := OpenTokenStore(path)
	require.NoError(t, err)
	require.Len(t, store.List(), 1, "an expired token stays listed")

	_, ok := store.Authenticate(secret)
	assert.False(t, ok, "an expired token must never authenticate")
	assert.Empty(t, store.List()[0].LastUsedAt, "a rejected token is not used")
}

func TestAuthenticateFailsClosedOnUnparseableExpiry(t *testing.T) {
	const secret = "sc_badexpiry"

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, path, tokenFile{
		SchemaVersion: schemaVersion,
		Tokens: []diskToken{{
			ID: "tok_00000002", Name: "bad", Role: RoleReader,
			Hash: hashSecret(secret), CreatedAt: "2026-01-01T00:00:00Z",
			ExpiresAt: "not-a-timestamp",
		}},
	})

	store, err := OpenTokenStore(path)
	require.NoError(t, err)
	_, ok := store.Authenticate(secret)
	assert.False(t, ok)
}

// TestAuthenticateDoesNotWriteTheTokenFile pins the no-write rule: a read must
// not turn into a token-file mutation.
func TestAuthenticateDoesNotWriteTheTokenFile(t *testing.T) {
	store, path := newTestStore(t)
	secret, _, err := store.Create("ci", RoleReader)
	require.NoError(t, err)

	before, err := os.ReadFile(path)
	require.NoError(t, err)
	beforeInfo, err := os.Stat(path)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, ok := store.Authenticate(secret)
		require.True(t, ok)
	}

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	afterInfo, err := os.Stat(path)
	require.NoError(t, err)

	assert.Equal(t, before, after, "authentication must not rewrite the token file")
	assert.True(t, beforeInfo.ModTime().Equal(afterInfo.ModTime()))

	assert.NotEmpty(t, store.List()[0].LastUsedAt, "last used is still tracked in memory")

	// The metadata is persisted by the next mutation, not by the read.
	_, _, err = store.Create("second", RoleReader)
	require.NoError(t, err)
	persisted, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(persisted), `"lastUsedAt"`)
}

// TestPhase1TokenFileMigratesToV2 loads a phase-1 record (role only, no scopes),
// authenticates it, and asserts the next mutation writes schemaVersion 2 with
// the expanded scope set.
func TestPhase1TokenFileMigratesToV2(t *testing.T) {
	const secret = "sc_phaseone"

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, path, tokenFile{
		SchemaVersion: schemaVersionPhase1,
		Tokens: []diskToken{{
			ID: "tok_00000003", Name: "phase-one", Role: RoleAdmin,
			Hash: hashSecret(secret), CreatedAt: "2026-01-01T00:00:00Z",
		}},
	})

	store, err := OpenTokenStore(path)
	require.NoError(t, err)

	list := store.List()
	require.Len(t, list, 1)
	assert.Equal(t, RoleAdmin, list[0].Role)
	assert.NotEmpty(t, list[0].Scopes, "a phase-1 record has its role expanded on load")
	assert.Nil(t, list[0].TaskSelectors)
	assert.Empty(t, list[0].ExpiresAt)

	got, ok := store.Authenticate(secret)
	require.True(t, ok, "a phase-1 token must keep working")
	assert.Contains(t, got.Scopes, string(ScopeAuditRead))

	// Any mutation upgrades the file.
	_, _, err = store.CreateWithOptions("runner", RoleRunner, TokenOptions{
		TaskSelectors: &TaskSelectors{ServiceIDs: []string{"svc-1"}},
	})
	require.NoError(t, err)

	var raw struct {
		SchemaVersion int              `json:"schemaVersion"`
		Tokens        []map[string]any `json:"tokens"`
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Equal(t, 2, raw.SchemaVersion)
	require.Len(t, raw.Tokens, 2)
	assert.Equal(t, "tok_00000003", raw.Tokens[0]["id"])
	assert.NotEmpty(t, raw.Tokens[0]["scopes"], "the migrated record now carries its scopes")

	// The rewritten file still loads.
	reopened, err := OpenTokenStore(path)
	require.NoError(t, err)
	assert.Len(t, reopened.List(), 2)
}

// TestPhase1UnknownRoleFailsLoad keeps a hand-edited role from authorizing
// anything: an unknown role has no scope set to expand.
func TestPhase1UnknownRoleFailsLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, path, tokenFile{
		SchemaVersion: schemaVersionPhase1,
		Tokens: []diskToken{{
			ID: "tok_00000004", Name: "auditor", Role: "auditor",
			Hash: hashSecret("sc_whatever"), CreatedAt: "2026-01-01T00:00:00Z",
		}},
	})

	store, err := OpenTokenStore(path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	assert.Contains(t, err.Error(), "auditor")
}

// TestListReportsNewMetadata pins what an operator sees: scopes, selector and
// expiry, but never the hash.
func TestListReportsNewMetadata(t *testing.T) {
	store, _ := newTestStore(t)

	expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	_, _, err := store.CreateWithOptions("ci", RoleRunner, TokenOptions{
		Scopes:        []string{"tasks:read", "tasks:exec"},
		TaskSelectors: &TaskSelectors{Labels: map[string]string{"team": "payments"}},
		ExpiresAt:     expires,
	})
	require.NoError(t, err)

	list := store.List()
	require.Len(t, list, 1)
	assert.Equal(t, []string{"tasks:read", "tasks:exec"}, list[0].Scopes)
	assert.Equal(t, map[string]string{"team": "payments"}, list[0].TaskSelectors.Labels)
	assert.Equal(t, expires, list[0].ExpiresAt)
	assert.Empty(t, list[0].Hash)

	data, err := json.Marshal(list)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "hash")
}

// writeTokenFile writes one token file for the load-path tests.
func writeTokenFile(t *testing.T, path string, file tokenFile) {
	t.Helper()

	data, err := json.MarshalIndent(file, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
}

// TestReturnedTokensDoNotShareState pins that a caller cannot edit the store
// through a token it was handed: List, Authenticate and Create all return a copy.
func TestReturnedTokensDoNotShareState(t *testing.T) {
	store, path := newTestStore(t)

	secret, created, err := store.CreateWithOptions("ci", RoleRunner, TokenOptions{
		TaskSelectors: &TaskSelectors{
			ServiceIDs: []string{"svc-1"},
			Labels:     map[string]string{"team": "payments"},
		},
	})
	require.NoError(t, err)

	mutate := func(tok *Token) {
		tok.Scopes[0] = "mutated:read"
		tok.TaskSelectors.ServiceIDs[0] = "svc-2"
		tok.TaskSelectors.Labels["team"] = "search"
	}

	mutate(&created)

	list := store.List()
	require.Len(t, list, 1)
	mutate(&list[0])

	got, ok := store.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, []string{"svc-1"}, got.TaskSelectors.ServiceIDs)
	assert.Equal(t, "payments", got.TaskSelectors.Labels["team"])
	assert.NotEqual(t, "mutated:read", got.Scopes[0])

	// A persist writes the store's copy, not a caller's mutation.
	_, _, err = store.Create("second", RoleReader)
	require.NoError(t, err)

	reopened, err := OpenTokenStore(path)
	require.NoError(t, err)
	first := reopened.List()[0]
	assert.Equal(t, []string{"svc-1"}, first.TaskSelectors.ServiceIDs)
	assert.Equal(t, "payments", first.TaskSelectors.Labels["team"])
	assert.NotEqual(t, "mutated:read", first.Scopes[0])
}

// TestLoadRejectsOutOfRoleScopes pins the load-side half of the role guarantee:
// a scope set read from disk is validated exactly like a minted one, so a
// restored or hand-edited file cannot grant authority the role does not carry.
func TestLoadRejectsOutOfRoleScopes(t *testing.T) {
	cases := []struct {
		name   string
		role   string
		scopes []string
	}{
		{"reader cannot administer tokens", RoleReader, []string{"tokens:admin"}},
		{"reader cannot write secrets", RoleReader, []string{"secrets:write"}},
		{"runner cannot write services", RoleRunner, []string{"services:write"}},
		{"operator cannot audit", RoleOperator, []string{"audit:read"}},
		{"unknown scope", RoleAdmin, []string{"services:delete"}},
		{"one bad scope among good ones", RoleAdmin, []string{"services:read", "nope:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			writeTokenFile(t, path, tokenFile{
				SchemaVersion: schemaVersion,
				Tokens: []diskToken{{
					ID: "tok_00000005", Name: "edited", Role: tc.role,
					Hash: hashSecret("sc_edited"), CreatedAt: "2026-01-01T00:00:00Z",
					Scopes: tc.scopes,
				}},
			})

			store, err := OpenTokenStore(path)
			require.Error(t, err)
			assert.Nil(t, store)
			assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
			assert.Contains(t, err.Error(), "tok_00000005")
		})
	}
}

// TestLoadKeepsAValidNarrowedScopeSet makes sure the validation does not widen a
// token that legitimately holds less than its role.
func TestLoadKeepsAValidNarrowedScopeSet(t *testing.T) {
	const secret = "sc_narrowedload"

	path := filepath.Join(t.TempDir(), "tokens.json")
	writeTokenFile(t, path, tokenFile{
		SchemaVersion: schemaVersion,
		Tokens: []diskToken{{
			ID: "tok_00000006", Name: "narrow", Role: RoleOperator,
			Hash: hashSecret(secret), CreatedAt: "2026-01-01T00:00:00Z",
			Scopes: []string{"services:write", "services:read", "services:read"},
		}},
	})

	store, err := OpenTokenStore(path)
	require.NoError(t, err)

	got, ok := store.Authenticate(secret)
	require.True(t, ok)
	assert.Equal(t, []string{"services:write", "services:read"}, got.Scopes,
		"a valid set is kept, deduplicated, and never widened to the whole role")
}

// TestLoadValidatesSelectors applies the mint-time normalization to a selector
// read from disk: blank entries fail closed, real entries are trimmed.
func TestLoadValidatesSelectors(t *testing.T) {
	t.Run("blank entry fails the load", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tokens.json")
		writeTokenFile(t, path, tokenFile{
			SchemaVersion: schemaVersion,
			Tokens: []diskToken{{
				ID: "tok_00000007", Name: "edited", Role: RoleRunner,
				Hash: hashSecret("sc_edited"), CreatedAt: "2026-01-01T00:00:00Z",
				TaskSelectors: &TaskSelectors{ServiceIDs: []string{"   "}},
			}},
		})

		store, err := OpenTokenStore(path)
		require.Error(t, err)
		assert.Nil(t, store)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	})

	t.Run("entries are trimmed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tokens.json")
		writeTokenFile(t, path, tokenFile{
			SchemaVersion: schemaVersion,
			Tokens: []diskToken{{
				ID: "tok_00000008", Name: "trimmed", Role: RoleRunner,
				Hash: hashSecret("sc_trimmed"), CreatedAt: "2026-01-01T00:00:00Z",
				TaskSelectors: &TaskSelectors{
					ServiceIDs: []string{"  svc-1 "},
					Labels:     map[string]string{" team ": "payments"},
				},
			}},
		})

		store, err := OpenTokenStore(path)
		require.NoError(t, err)

		list := store.List()
		require.Len(t, list, 1)
		require.NotNil(t, list[0].TaskSelectors)
		assert.Equal(t, []string{"svc-1"}, list[0].TaskSelectors.ServiceIDs)
		assert.Equal(t, map[string]string{"team": "payments"}, list[0].TaskSelectors.Labels)
	})
}
