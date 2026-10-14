package ingress

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProvider struct{ table Table }

func (f fakeProvider) Table() Table { return f.table }

func TestTableServer_ServesTable(t *testing.T) {
	want := TableFromRoutes(sampleRoutes())
	srv := NewTableServer(fakeProvider{want}, nil, nil, ":0")
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
	srv := NewTableServer(fakeProvider{}, nil, nil, ":0")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/ingress", "application/json", nil)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// reportRequest builds a report request carrying a client certificate, which is
// where the server takes the reporting node's identity from.
func reportRequest(body string, withCert bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/ingress/report", strings.NewReader(body))
	if withCert {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
			Subject: pkix.Name{CommonName: "node-1"},
		}}}
	}
	return req
}

// TestTableServer_StoresNodeReport covers the report endpoint: the report is
// stored under the certificate's identity, and addresses are normalized.
func TestTableServer_StoresNodeReport(t *testing.T) {
	reports := NewReportStore(time.Minute)
	srv := NewTableServer(fakeProvider{}, reports, nil, ":0")

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, reportRequest(`{"node_id":"spoofed","tasks":[{"task_id":"t1","ips":["192.168.127.5/24"]}]}`, true))

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, map[string][]string{"t1": {"192.168.127.5"}}, reports.Addresses(),
		"the CIDR suffix is stripped and the body cannot claim another node")
}

// TestTableServer_ReportRequiresCertificate asserts a report without a client
// certificate is refused, so a node cannot report on another node's behalf.
func TestTableServer_ReportRequiresCertificate(t *testing.T) {
	srv := NewTableServer(fakeProvider{}, NewReportStore(time.Minute), nil, ":0")

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, reportRequest(`{"tasks":[]}`, false))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestTableServer_ReportRejectsBadPayload asserts a malformed report is rejected
// rather than stored as an empty one.
func TestTableServer_ReportRejectsBadPayload(t *testing.T) {
	reports := NewReportStore(time.Minute)
	srv := NewTableServer(fakeProvider{}, reports, nil, ":0")

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, reportRequest("not json", true))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, reports.Addresses())
}

// TestTableServer_ReportingDisabled asserts the endpoint does not exist when the
// server was created without a store.
func TestTableServer_ReportingDisabled(t *testing.T) {
	srv := NewTableServer(fakeProvider{}, nil, nil, ":0")

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, reportRequest(`{"tasks":[]}`, true))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
