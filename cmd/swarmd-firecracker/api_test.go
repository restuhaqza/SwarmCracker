package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/apiversion"
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
	stateDir := shortTempDir(t)
	cfg := runAPIFlags(t, "--state-dir", stateDir)

	require.True(t, cfg.enabled)
	require.Equal(t, "127.0.0.1:4244", cfg.tcpAddr)
	require.Equal(t, filepath.Join(stateDir, "api.sock"), cfg.socket)
	require.Equal(t, filepath.Join(stateDir, "api", "tokens.json"), cfg.tokenFile)
	require.Equal(t, filepath.Join(stateDir, "api"), cfg.certDir)
}

func TestAPIRuntimeFrom_DefaultStateDirPaths(t *testing.T) {
	cfg := runAPIFlags(t)

	require.Equal(t, filepath.Join(defaultStateDir, "api.sock"), cfg.socket)
	require.Equal(t, filepath.Join(defaultStateDir, "api", "tokens.json"), cfg.tokenFile)
	require.Equal(t, filepath.Join(defaultStateDir, "api"), cfg.certDir)
}

func TestAPIRuntimeFrom_Overrides(t *testing.T) {
	stateDir := shortTempDir(t)
	cfg := runAPIFlags(t,
		"--state-dir", stateDir,
		"--api-listen", "0.0.0.0:4444",
		"--api-socket", "/tmp/api-test.sock",
		"--api-token-file", "/tmp/api-test-tokens.json",
		"--api-cert-dir", "/tmp/api-test-certs",
		"--api-disabled",
	)

	require.False(t, cfg.enabled)
	require.Equal(t, "0.0.0.0:4444", cfg.tcpAddr)
	require.Equal(t, "/tmp/api-test.sock", cfg.socket)
	require.Equal(t, "/tmp/api-test-tokens.json", cfg.tokenFile)
	require.Equal(t, "/tmp/api-test-certs", cfg.certDir)
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
	stateDir := shortTempDir(t)
	cfg := apiRuntime{
		enabled:   false,
		tcpAddr:   "127.0.0.1:4244",
		socket:    filepath.Join(stateDir, "api.sock"),
		tokenFile: filepath.Join(stateDir, "api", "tokens.json"),
		certDir:   filepath.Join(stateDir, "api"),
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
	stateDir := shortTempDir(t)
	cfg := apiRuntime{
		enabled:   true,
		socket:    filepath.Join(stateDir, "api.sock"),
		tokenFile: filepath.Join(stateDir, "api", "tokens.json"),
		certDir:   filepath.Join(stateDir, "api"),
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.Error(t, err)
	require.Nil(t, stop)
	require.Contains(t, err.Error(), "swarm-node.crt")
	require.NoFileExists(t, cfg.socket)
}

func TestStartAPI_ServesVersionAndStops(t *testing.T) {
	stateDir := shortTempDir(t)
	seedNodeCertificates(t, stateDir)

	sock := filepath.Join(stateDir, "api.sock")
	cfg := apiRuntime{
		enabled:   true,
		tcpAddr:   freeTCPAddr(t),
		socket:    sock,
		tokenFile: filepath.Join(stateDir, "api", "tokens.json"),
		certDir:   filepath.Join(stateDir, "api"),
	}

	stop, err := startAPI(context.Background(), cfg, stateDir, "node-1", filepath.Join(stateDir, "swarm.sock"))
	require.NoError(t, err)
	require.NotNil(t, stop)

	resp := waitForGet(t, unixHTTPClient(sock), "http://unix/v1/version", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, Version, doc.ServerVersion)
	require.Equal(t, apiversion.Current, doc.GRPCProtocolVersion)
	require.Equal(t, []string{"read"}, doc.Capabilities)
	// The stub control socket is unreachable, so there is no cluster ID yet;
	// the version probe must answer anyway.
	require.Empty(t, doc.ClusterID)

	// The TLS listener is up and answers 401 until an operator mints a token.
	certFile := filepath.Join(cfg.certDir, "tls.crt")
	require.FileExists(t, certFile)
	require.FileExists(t, filepath.Join(cfg.certDir, "tls.key"))

	tcpResp := waitForGet(t, tlsHTTPClient(t, certFile), "https://"+cfg.tcpAddr+"/v1/version", nil)
	require.Equal(t, http.StatusUnauthorized, tcpResp.StatusCode)
	tcpResp.Body.Close()

	stop()
	stop() // idempotent

	_, err = os.Stat(sock)
	require.True(t, errors.Is(err, fs.ErrNotExist), "the socket should be gone after stop")
	require.NoFileExists(t, cfg.tokenFile, "the token store is only written on the first mutation")
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

// shortTempDir returns a temporary directory with a short absolute path: the
// daemon's Unix socket address must fit the platform's sun_path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "swarmd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// freeTCPAddr returns a loopback address whose port was free a moment ago.
func freeTCPAddr(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// unixHTTPClient dials the daemon's trusted Unix socket.
func unixHTTPClient(path string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		},
	}
}

// tlsHTTPClient trusts exactly the API certificate at certFile.
func tlsHTTPClient(t *testing.T, certFile string) *http.Client {
	t.Helper()

	pemBytes, err := os.ReadFile(certFile)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pemBytes), "certificate file must hold a PEM certificate")

	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}
}

// waitForGet retries a GET until the listener has finished binding. Any HTTP
// response is final, including 401.
func waitForGet(t *testing.T, c *http.Client, url string, headers map[string]string) *http.Response {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		require.NoError(t, err)
		for name, value := range headers {
			req.Header.Set(name, value)
		}

		resp, err := c.Do(req)
		if err == nil {
			return resp
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("GET %s did not reach the server: %v", url, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
