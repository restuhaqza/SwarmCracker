package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/swarmkit/v2/node"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
	"github.com/restuhaqza/swarmcracker/test/testhelpers"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// runAPIFlags parses args through the daemon's real flag set and returns the
// resolved API runtime.
func runAPIFlags(t *testing.T, args ...string) apiRuntime {
	t.Helper()

	var got apiRuntime
	app := cli.NewApp()
	app.Flags = newAppFlags()
	app.Action = func(c *cli.Context) error {
		got = apiRuntimeFrom(c)
		return nil
	}
	require.NoError(t, app.Run(append([]string{"swarmd-firecracker"}, args...)))
	return got
}

func TestAPIRuntimeFrom_Defaults(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	cfg := runAPIFlags(t, "--state-dir", stateDir)

	require.True(t, cfg.enabled)
	require.Equal(t, "127.0.0.1:4244", cfg.tcpAddr)
	require.Equal(t, filepath.Join(stateDir, "api.sock"), cfg.socket)
	require.Equal(t, filepath.Join(stateDir, "api", "tokens.json"), cfg.tokenFile)
	require.Equal(t, filepath.Join(stateDir, "api"), cfg.certDir)
	require.Equal(t, filepath.Join(stateDir, "api", "audit.jsonl"), cfg.auditFile)
	require.Equal(t, 720*time.Hour, cfg.auditRetention)
}

func TestAPIRuntimeFrom_DefaultStateDirPaths(t *testing.T) {
	cfg := runAPIFlags(t)

	require.Equal(t, filepath.Join(defaultStateDir, "api.sock"), cfg.socket)
	require.Equal(t, filepath.Join(defaultStateDir, "api", "tokens.json"), cfg.tokenFile)
	require.Equal(t, filepath.Join(defaultStateDir, "api"), cfg.certDir)
	require.Equal(t, filepath.Join(defaultStateDir, "api", "audit.jsonl"), cfg.auditFile)
	require.Equal(t, 30*24*time.Hour, cfg.auditRetention)
}

func TestAPIRuntimeFrom_Overrides(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	cfg := runAPIFlags(t,
		"--state-dir", stateDir,
		"--api-listen", "0.0.0.0:4444",
		"--api-socket", "/tmp/api-test.sock",
		"--api-token-file", "/tmp/api-test-tokens.json",
		"--api-cert-dir", "/tmp/api-test-certs",
		"--api-audit-file", "/tmp/api-test-audit.jsonl",
		"--api-audit-retention", "48h",
		"--api-disabled",
	)

	require.False(t, cfg.enabled)
	require.Equal(t, "0.0.0.0:4444", cfg.tcpAddr)
	require.Equal(t, "/tmp/api-test.sock", cfg.socket)
	require.Equal(t, "/tmp/api-test-tokens.json", cfg.tokenFile)
	require.Equal(t, "/tmp/api-test-certs", cfg.certDir)
	require.Equal(t, "/tmp/api-test-audit.jsonl", cfg.auditFile)
	require.Equal(t, 48*time.Hour, cfg.auditRetention)
}

func TestAPICertHosts(t *testing.T) {
	cases := []struct {
		name    string
		tcpAddr string
		want    []string
	}{
		{name: "empty disables TCP", tcpAddr: "", want: nil},
		{name: "loopback with port", tcpAddr: "127.0.0.1:4244", want: []string{"127.0.0.1"}},
		{name: "wildcard IPv4", tcpAddr: "0.0.0.0:4244", want: nil},
		{name: "wildcard IPv6", tcpAddr: "[::]:4244", want: nil},
		{name: "bare port", tcpAddr: ":4244", want: nil},
		{name: "host name", tcpAddr: "api.example.com:4244", want: []string{"api.example.com"}},
		{name: "no port", tcpAddr: "localhost", want: []string{"localhost"}},
		{name: "bracketed IPv6", tcpAddr: "[fd00::1]:4244", want: []string{"fd00::1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, apiCertHosts(tc.tcpAddr))
		})
	}
}

func TestStartAPI_DisabledIsNoOp(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	cfg := apiRuntime{
		enabled:        false,
		tcpAddr:        "127.0.0.1:4244",
		socket:         filepath.Join(stateDir, "api.sock"),
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention: 720 * time.Hour,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.NoError(t, err)
	require.NotNil(t, stop)

	stop()
	stop() // idempotent

	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	require.Empty(t, entries, "a disabled API must not create any file")
}

func TestStartAPI_RequiresNodeCertificates(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	cfg := apiRuntime{
		enabled:        true,
		socket:         filepath.Join(stateDir, "api.sock"),
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention: 720 * time.Hour,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.Error(t, err)
	require.Nil(t, stop)
	require.Contains(t, err.Error(), "swarm-node.crt")
	require.NoFileExists(t, cfg.socket)
}

// TestStartAPI_BusyTCPPortFails is the reason startAPI binds before returning:
// the caller must learn about a taken port at startup, not at shutdown.
func TestStartAPI_BusyTCPPortFails(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()
	busyAddr := held.Addr().String()

	cfg := apiRuntime{
		enabled:        true,
		tcpAddr:        busyAddr,
		socket:         filepath.Join(stateDir, "api.sock"),
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention: 720 * time.Hour,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.Error(t, err, "a busy TCP port must be reported by startAPI")
	require.Nil(t, stop)
	require.Contains(t, err.Error(), busyAddr, "the error must name the address")
	require.NoFileExists(t, cfg.socket, "a failed bind must not leave a Unix socket behind")
}

func TestStartAPI_ServesVersionAndStops(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	sock := filepath.Join(stateDir, "api.sock")
	cfg := apiRuntime{
		enabled:        true,
		tcpAddr:        testhelpers.FreeTCPAddr(t),
		socket:         sock,
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention: 720 * time.Hour,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.NoError(t, err)
	require.NotNil(t, stop)

	resp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", apihttp.MediaType, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, Version, doc.ServerVersion)
	require.Equal(t, apiversion.Current, doc.GRPCProtocolVersion)
	require.Equal(t, []string{"read", "write", "events", "exec", "logs", "files", "health"}, doc.Capabilities,
		"the node certificates are present, so the broker manager and health are wired")
	// The stub control socket is unreachable, so there is no cluster ID yet;
	// the version probe must answer anyway.
	require.Empty(t, doc.ClusterID)

	// The TLS listener is up and answers 401 until an operator mints a token.
	certFile := filepath.Join(cfg.certDir, "tls.crt")
	require.FileExists(t, certFile)
	require.FileExists(t, filepath.Join(cfg.certDir, "tls.key"))

	tcpResp := testhelpers.WaitForGet(t, testhelpers.TLSHTTPClient(t, certFile),
		"https://"+cfg.tcpAddr+"/v1/version", apihttp.MediaType, nil)
	require.Equal(t, http.StatusUnauthorized, tcpResp.StatusCode)
	tcpResp.Body.Close()

	// The trusted Unix listener reaches the audit route and reports an empty,
	// non-null list: no mutation has been recorded yet.
	unixAudit := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock),
		"http://unix/v1/audit", apihttp.MediaType, nil)
	require.Equal(t, http.StatusOK, unixAudit.StatusCode)
	var auditDoc struct {
		Kind  string          `json:"kind"`
		Items json.RawMessage `json:"items"`
	}
	require.NoError(t, json.NewDecoder(unixAudit.Body).Decode(&auditDoc))
	unixAudit.Body.Close()
	require.Equal(t, v1.KindAuditList, auditDoc.Kind)
	require.Equal(t, "[]", string(auditDoc.Items))

	// A reader token authenticates but does not hold audit:read.
	require.NoFileExists(t, cfg.tokenFile, "the token store is only written on the first mutation")

	store, err := apihttp.OpenTokenStore(cfg.tokenFile)
	require.NoError(t, err)
	readerSecret, _, err := store.Create("phase-one-reader", apihttp.RoleReader)
	require.NoError(t, err)

	tlsClient := testhelpers.TLSHTTPClient(t, certFile)
	readerResp := testhelpers.WaitForGet(t, tlsClient,
		"https://"+cfg.tcpAddr+"/v1/audit", apihttp.MediaType,
		map[string]string{"Authorization": "Bearer " + readerSecret})
	require.Equal(t, http.StatusForbidden, readerResp.StatusCode)
	var errDoc v1.ErrorDocument
	require.NoError(t, json.NewDecoder(readerResp.Body).Decode(&errDoc))
	readerResp.Body.Close()
	require.Equal(t, "audit:read", errDoc.Error.Details["requiredScope"])

	// The same token reads a route its role grants.
	readerVersion := testhelpers.WaitForGet(t, tlsClient,
		"https://"+cfg.tcpAddr+"/v1/version", apihttp.MediaType,
		map[string]string{"Authorization": "Bearer " + readerSecret})
	require.Equal(t, http.StatusOK, readerVersion.StatusCode)
	readerVersion.Body.Close()

	adminSecret, _, err := store.Create("phase-two-admin", apihttp.RoleAdmin)
	require.NoError(t, err)

	adminResp := testhelpers.WaitForGet(t, tlsClient,
		"https://"+cfg.tcpAddr+"/v1/audit", apihttp.MediaType,
		map[string]string{"Authorization": "Bearer " + adminSecret})
	require.Equal(t, http.StatusOK, adminResp.StatusCode)
	adminResp.Body.Close()

	// No mutation ran, so nothing has been audited yet and the log file does not
	// exist: opening the store must not create it.
	require.NoFileExists(t, cfg.auditFile)

	stop()
	stop() // idempotent

	_, err = os.Stat(sock)
	require.True(t, errors.Is(err, fs.ErrNotExist), "the socket should be gone after stop")
}

// TestStartAPI_BadAuditFileFails pins the startup contract: an unusable audit
// path is a startup error, not a surprise on the first write.
func TestStartAPI_BadAuditFileFails(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	// A directory at the audit path cannot be opened as a JSONL file.
	auditPath := filepath.Join(stateDir, "api", "audit.jsonl")
	require.NoError(t, os.MkdirAll(auditPath, 0o700))

	cfg := apiRuntime{
		enabled:        true,
		tcpAddr:        testhelpers.FreeTCPAddr(t),
		socket:         filepath.Join(stateDir, "api.sock"),
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      auditPath,
		auditRetention: 720 * time.Hour,
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.Error(t, err)
	require.Nil(t, stop)
	require.Contains(t, err.Error(), auditPath)
	require.NoFileExists(t, cfg.socket, "a failed audit open must not leave a Unix socket behind")
}

// TestStartAPIForNode_WorkerNeverStartsTheAPI pins the manager-only rule: the
// daemon passes every node through startAPIForNode, and a worker must end up with
// no listener and no API files.
func TestStartAPIForNode_WorkerNeverStartsTheAPI(t *testing.T) {
	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	cfg := apiRuntime{
		enabled:        true,
		tcpAddr:        testhelpers.FreeTCPAddr(t),
		socket:         filepath.Join(stateDir, "api.sock"),
		tokenFile:      filepath.Join(stateDir, "api", "tokens.json"),
		certDir:        filepath.Join(stateDir, "api"),
		auditFile:      filepath.Join(stateDir, "api", "audit.jsonl"),
		auditRetention: 720 * time.Hour,
	}
	config := &node.Config{
		StateDir:         stateDir,
		Hostname:         "worker-1",
		JoinAddr:         "10.0.0.1:4242",
		ListenControlAPI: filepath.Join(stateDir, "swarm.sock"),
	}

	stop, err := startAPIForNode(context.Background(), cfg, config)
	require.NoError(t, err)
	require.NotNil(t, stop)
	stop()
	stop() // idempotent

	require.NoFileExists(t, cfg.socket, "a worker must not bind the API socket")
	require.NoDirExists(t, cfg.certDir, "a worker must not create API material")
}

// seedNodeCertificates writes the cluster TLS material client.New loads. The
// bootstrap API certificate stands in for the node certificate: the daemon's
// control client dials lazily, so the files only need to load, not to
// authenticate a running manager.
func seedNodeCertificates(t *testing.T, stateDir string) {
	t.Helper()

	certDir := filepath.Join(stateDir, "certificates")
	require.NoError(t, os.MkdirAll(certDir, 0o700))

	certFile, keyFile, err := apihttp.EnsureCertificate(t.TempDir(), "localhost", nil)
	require.NoError(t, err)
	certPEM, err := os.ReadFile(certFile)
	require.NoError(t, err)
	keyPEM, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(certDir, "swarm-node.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "swarm-node.key"), keyPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certDir, "swarm-root-ca.crt"), certPEM, 0o644))
}

// fakeBrokerSources satisfies the broker's task and node readers without a
// SwarmKit cluster.
type fakeBrokerSources struct{}

func (fakeBrokerSources) GetTask(context.Context, string) (v1.Task, error) {
	return v1.Task{}, errors.New("not used")
}

func (fakeBrokerSources) GetNode(context.Context, string) (v1.Node, error) {
	return v1.Node{}, errors.New("not used")
}

// TestNewBrokerManager_RequiresClusterTLS pins that the manager-side broker is
// built from the node's cluster identity and is optional when the material is
// unavailable (the API still serves, exec answers 501).
func TestNewBrokerManager_RequiresClusterTLS(t *testing.T) {
	_, err := newBrokerManager(t.TempDir(), fakeBrokerSources{}, fakeBrokerSources{}, 0)
	require.Error(t, err, "without certificate material there is no broker manager")

	stateDir := testhelpers.ShortTempDir(t)
	seedNodeCertificates(t, stateDir)

	mgr, err := newBrokerManager(stateDir, fakeBrokerSources{}, fakeBrokerSources{}, 0)
	require.NoError(t, err)
	require.NotNil(t, mgr)
}
