package apihttp

// The two boundaries a sandbox-bound credential has: it may not read the node's inventory of
// sandboxes, and it may not outlive the sandbox it was minted for.
//
// They are the remaining half of the epic's acceptance rule — "a sandbox token cannot exec
// elsewhere, list sandboxes, or mint a token" — and they are tested here because both are
// properties of the routes: the listing is refused on the identity, and the expiry is bounded
// where the token is minted.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/pkg/sandbox"
)

// TestSandboxScopedTokenCannotListSandboxes is the listing half of acceptance #9: a token
// minted for one sandbox reads that sandbox and is refused the node's inventory — not an
// empty list, which would still answer "nothing else is here", and not a filtered one, which
// would answer it one request at a time.
func TestSandboxScopedTokenCannotListSandboxes(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
		"b": {ID: "b", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	// An admin mints a credential for A, which carries read and exec and names A.
	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/tokens", `{}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var minted v1.SandboxToken
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &minted))

	// The listing is refused, with the reason and the sandbox the credential is bound to.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodGet, "/v1/sandboxes", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Equal(t, "sandbox_list_not_permitted", doc.Error.Details["reason"])
	assert.Equal(t, []any{"a"}, doc.Error.Details["sandboxIds"])

	// Its own sandbox is still readable: the refusal is about the node's inventory, not
	// about the sandbox the credential was minted for.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodGet, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// An admin with no sandbox selector lists as before — the rule narrows a bound
	// credential, it does not close the listing.
	rec = sandboxRequest(t, srv, admin, http.MethodGet, "/v1/sandboxes", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestSandboxTokenExpiryIsBoundedByTheSandbox pins the second boundary: the token's life is
// the sandbox's remaining idle life, an expiry past it is refused rather than shortened, and
// a sandbox with no TTL bounds nothing.
func TestSandboxTokenExpiryIsBoundedByTheSandbox(t *testing.T) {
	now := time.Now().UTC()
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		// Half of a one-minute TTL still ahead of it: the token may live 30 seconds.
		"timed": {ID: "timed", State: sandbox.StateReady, IdleTTLSeconds: 60, LastActive: now.Add(-30 * time.Second)},
		// No TTL: this sandbox never idles out, so nothing bounds its token.
		"forever": {ID: "forever", State: sandbox.StateReady},
		// Already past its idle TTL: the reaper's next pass takes it back.
		"due": {ID: "due", State: sandbox.StateReady, IdleTTLSeconds: 60, LastActive: now.Add(-2 * time.Minute)},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	mint := func(t *testing.T, id, body string) (v1.SandboxToken, *v1.ErrorDocument) {
		t.Helper()
		rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/"+id+"/tokens", body)
		if rec.Code != http.StatusCreated {
			doc := decodeErrorBody(t, rec)
			return v1.SandboxToken{}, &doc
		}
		var token v1.SandboxToken
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &token))
		return token, nil
	}

	// A token that asked for no expiry gets the sandbox's own deadline, a little under 30
	// seconds away. The bound is what is asserted, not the exact second.
	token, failure := mint(t, "timed", `{}`)
	require.Nil(t, failure, "minting for a live sandbox must succeed")
	expiry, err := time.Parse(time.RFC3339, token.ExpiresAt)
	require.NoError(t, err, "the document states the bounded expiry: %q", token.ExpiresAt)
	assert.False(t, expiry.After(now.Add(30*time.Second)), "the token cannot outlive the sandbox: %s", token.ExpiresAt)
	assert.True(t, expiry.After(now), "and it is still usable now: %s", token.ExpiresAt)

	// Asking for a later expiry is refused, and the refusal names the sandbox's deadline so
	// the caller can pick one that fits instead of guessing.
	later := now.Add(2 * time.Hour).Format(time.RFC3339)
	_, failure = mint(t, "timed", `{"expiresAt":"`+later+`"}`)
	require.NotNil(t, failure, "a token that would outlive its sandbox is refused")
	assert.Equal(t, string(output.CodeInvalid), failure.Error.Code)
	assert.Equal(t, "token_outlives_sandbox", failure.Error.Details["reason"])
	assert.NotEmpty(t, failure.Error.Details["sandboxExpiresAt"])

	// An instant the API cannot read is refused with the store's own rule, because a token
	// minted through this route and one minted through the CLI must agree on the format.
	_, failure = mint(t, "timed", `{"expiresAt":"tomorrow"}`)
	require.NotNil(t, failure)
	assert.Equal(t, string(output.CodeInvalid), failure.Error.Code)

	// An earlier expiry is the caller's to choose, and is stored as asked.
	earlier := now.Add(5 * time.Second).Format(time.RFC3339)
	token, failure = mint(t, "timed", `{"expiresAt":"`+earlier+`"}`)
	require.Nil(t, failure)
	assert.Equal(t, earlier, token.ExpiresAt)

	// A sandbox with no TTL bounds nothing: the token may be minted with no expiry at all.
	token, failure = mint(t, "forever", `{}`)
	require.Nil(t, failure)
	assert.Empty(t, token.ExpiresAt, "a sandbox that never idles out bounds no token")

	// A sandbox already past its deadline yields a token that expires now rather than one
	// stamped in the past, which the store would refuse.
	token, failure = mint(t, "due", `{}`)
	require.Nil(t, failure)
	due, err := time.Parse(time.RFC3339, token.ExpiresAt)
	require.NoError(t, err)
	assert.False(t, due.Before(now.Add(-time.Second)), "an expiry the store accepts cannot be in the past: %s", token.ExpiresAt)
	assert.False(t, due.After(now.Add(2*time.Second)), "a sandbox already due gives no real life: %s", token.ExpiresAt)
}

// TestSandboxSelectorNarrowsTheSingleDocumentRead is the other door to the same rule: a
// credential bound to one sandbox reads that sandbox's document and is refused another's by
// id, not only in the listing. Four sides are pinned, because the rule is a narrowing and not
// a new gate: the bound token works on its own sandbox, fails on another with the reason and
// the sandbox it asked about, an operator with no selector reads anywhere, and a runner
// narrowed by tasks (no sandbox selector) behaves exactly as it did before.
func TestSandboxSelectorNarrowsTheSingleDocumentRead(t *testing.T) {
	boxes := &fakeSandboxes{records: map[string]sandbox.Record{
		"a": {ID: "a", State: sandbox.StateReady},
		"b": {ID: "b", State: sandbox.StateReady},
	}}
	srv, store := sandboxServer(t, boxes)
	admin, _, err := store.Create("admin", RoleAdmin)
	require.NoError(t, err)

	rec := sandboxRequest(t, srv, admin, http.MethodPost, "/v1/sandboxes/a/tokens", `{}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var minted v1.SandboxToken
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &minted))

	// Its own sandbox: readable.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodGet, "/v1/sandboxes/a", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Another sandbox: refused, with the reason and the sandbox asked about.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodGet, "/v1/sandboxes/b", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	doc := decodeErrorBody(t, rec)
	assert.Equal(t, string(output.CodeForbidden), doc.Error.Code)
	assert.Equal(t, "sandbox_selector_mismatch", doc.Error.Details["reason"])
	assert.Equal(t, "b", doc.Error.Details["sandboxId"])

	// An ID that does not exist gets the same refusal: a bound credential learns nothing
	// about what is on the node from the difference between the two.
	rec = sandboxRequest(t, srv, minted.Secret, http.MethodGet, "/v1/sandboxes/nope", "")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	// An operator with no sandbox selector reads anywhere, as before.
	operator, _, err := store.Create("operator", RoleOperator)
	require.NoError(t, err)
	for _, id := range []string{"a", "b"} {
		rec = sandboxRequest(t, srv, operator, http.MethodGet, "/v1/sandboxes/"+id, "")
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", id, rec.Body.String())
	}

	// A runner narrowed by tasks only carries no sandbox selector, and this rule leaves it
	// exactly where it was: the question of whether it should read a sandbox at all is not
	// answered here.
	runner := createRunnerToken(t, store, []string{string(ScopeSandboxesRead)}, nil)
	for _, id := range []string{"a", "b"} {
		rec = sandboxRequest(t, srv, runner, http.MethodGet, "/v1/sandboxes/"+id, "")
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", id, rec.Body.String())
	}
}
