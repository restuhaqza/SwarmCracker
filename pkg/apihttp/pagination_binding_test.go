package apihttp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCursorIsBoundToItsRoute pins item 2 end to end: a cursor is a position in
// one query, not a global offset. A cursor issued for /v1/tasks cannot page
// /v1/services, and the rejection names the reason.
func TestCursorIsBoundToItsRoute(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		tasks: []v1.Task{
			{ID: "task-1", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "task-2", CreatedAt: "2026-01-02T00:00:00Z"},
		},
		services: []v1.Service{
			{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "svc-2", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	})

	rec := api.do(t, http.MethodGet, "/v1/tasks?limit=1", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var tasks v1.TaskList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tasks))
	require.NotNil(t, tasks.Next)

	rec = api.do(t, http.MethodGet,
		"/v1/services?limit=1&cursor="+url.QueryEscape(*tasks.Next), withToken(api.token))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	doc := decodeError(t, rec)
	assert.Equal(t, string(output.CodeInvalid), doc.Error.Code)
	assert.Equal(t, "cursor_mismatch", doc.Error.Details["reason"])

	// A legacy cursor carries no fingerprint and belongs to no query.
	legacy := base64.RawURLEncoding.EncodeToString([]byte("2026-01-01T00:00:00Z\x00task-1"))
	rec = api.do(t, http.MethodGet,
		"/v1/tasks?limit=1&cursor="+url.QueryEscape(legacy), withToken(api.token))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "cursor_mismatch", decodeError(t, rec).Error.Details["reason"])
}

// TestCursorIsBoundToTheEffectiveFilter pins the filter half of the binding: the
// same route with a different label selection is a different result set, so the
// cursor does not carry over.
func TestCursorIsBoundToTheEffectiveFilter(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		services: []v1.Service{
			{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z", Labels: map[string]string{"team": "web"}},
			{ID: "svc-2", CreatedAt: "2026-01-02T00:00:00Z", Labels: map[string]string{"team": "web"}},
			{ID: "svc-3", CreatedAt: "2026-01-03T00:00:00Z", Labels: map[string]string{"team": "db"}},
		},
	})

	rec := api.do(t, http.MethodGet, "/v1/services?limit=1&label=team%3Dweb", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page v1.ServiceList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.NotNil(t, page.Next)

	rec = api.do(t, http.MethodGet,
		"/v1/services?limit=1&label=team%3Ddb&cursor="+url.QueryEscape(*page.Next), withToken(api.token))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "cursor_mismatch", decodeError(t, rec).Error.Details["reason"])

	// The same selection still pages.
	rec = api.do(t, http.MethodGet,
		"/v1/services?limit=1&label=team%3Dweb&cursor="+url.QueryEscape(*page.Next), withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestCursorLabelOrderDoesNotMatter pins the canonicalisation: the selectors are
// ANDed, so repeating them in another order is the same query and the cursor
// still pages.
func TestCursorLabelOrderDoesNotMatter(t *testing.T) {
	api := newTestAPI(t, &fakeClient{
		services: []v1.Service{
			{ID: "svc-1", CreatedAt: "2026-01-01T00:00:00Z", Labels: map[string]string{"team": "web", "tier": "front"}},
			{ID: "svc-2", CreatedAt: "2026-01-02T00:00:00Z", Labels: map[string]string{"team": "web", "tier": "front"}},
		},
	})

	rec := api.do(t, http.MethodGet,
		"/v1/services?limit=1&label=team%3Dweb&label=tier%3Dfront", withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page v1.ServiceList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.NotNil(t, page.Next)

	rec = api.do(t, http.MethodGet,
		"/v1/services?limit=1&label=tier%3Dfront&label=team%3Dweb&cursor="+url.QueryEscape(*page.Next),
		withToken(api.token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
