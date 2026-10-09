package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct{ table Table }

func (f fakeProvider) Table() Table { return f.table }

func TestTableServer_ServesTable(t *testing.T) {
	want := TableFromRoutes(sampleRoutes())
	srv := NewTableServer(fakeProvider{want}, nil, ":0")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/ingress")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var got Table
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, want.Revision, got.Revision)
	assert.Len(t, got.Routes, 2)
}

func TestTableServer_RejectsNonGET(t *testing.T) {
	srv := NewTableServer(fakeProvider{}, nil, ":0")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/ingress", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
