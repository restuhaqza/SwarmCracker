package apihttp

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
	"github.com/stretchr/testify/require"
)

// serveRun is one background Serve call: cancel ends it, done closes after it
// returns and result holds its error.
type serveRun struct {
	cancel context.CancelFunc
	result chan error
	done   chan struct{}
}

// startServe runs srv.Serve in the background. The test must end with a cancel,
// which the cleanup does as well so a failing assertion cannot leak a listener.
func startServe(t *testing.T, srv *Server, unixPath, tcpAddr string) *serveRun {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	run := &serveRun{
		cancel: cancel,
		result: make(chan error, 1),
		done:   make(chan struct{}),
	}

	go func() {
		run.result <- srv.Serve(ctx, unixPath, tcpAddr)
		close(run.done)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after the context was canceled")
		}
	})
	return run
}

// stop cancels the run and returns Serve's result once it has finished.
func (r *serveRun) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		return <-r.result
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the context was canceled")
		return nil
	}
}

// waitForGet sends a GET, retrying transport failures until the listener has
// finished binding. Any HTTP response is final, including 401.
func waitForGet(t *testing.T, c *http.Client, url string, headers map[string]string) *http.Response {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", MediaType)
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

// unixHTTPClient dials a Unix socket for a fixed host name.
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

// freeTCPAddr returns a loopback address whose port was free a moment ago.
func freeTCPAddr(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// shortTempDir returns a temporary directory with a short absolute path. A Unix
// socket address must fit the platform's sun_path limit, which a test-named
// t.TempDir() can exceed on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "apihttp")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// testCertDir generates the bootstrap TLS pair and returns its directory.
func testCertDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "api")
	_, _, err := EnsureCertificate(dir, "localhost", nil)
	require.NoError(t, err)
	return dir
}

// newServeServer builds a server with no token store: the Unix socket needs
// none, and the TCP tests install their own.
func newServeServer(t *testing.T, store *TokenStore, certDir string) *Server {
	t.Helper()

	srv, err := New(Config{
		Client:        &fakeClient{clusterID: "cluster-1"},
		Tokens:        store,
		CertDir:       certDir,
		ServerVersion: "0.11.0",
		GRPCVersion:   "1",
		ClusterID:     "cluster-1",
	})
	require.NoError(t, err)
	return srv
}

// storeWithToken returns a persisted store and one reader secret.
func storeWithToken(t *testing.T) (*TokenStore, string) {
	t.Helper()

	store, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	require.NoError(t, err)
	secret, _, err := store.Create("ci", RoleReader)
	require.NoError(t, err)
	return store, secret
}

func TestServe_UnixSocketServesVersionWithoutToken(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "run", "api.sock")
	srv := newServeServer(t, nil, "")
	run := startServe(t, srv, sock, "")

	resp := waitForGet(t, unixHTTPClient(sock), "http://unix/v1/version", nil)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, MediaType, resp.Header.Get("Content-Type"))
	require.Equal(t, APIVersion, resp.Header.Get("X-SwarmCracker-Api-Version"))

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"v1"}, doc.APIVersions)
	require.Equal(t, "cluster-1", doc.ClusterID)

	// The socket's file mode is the credential, so it must not be group- or
	// world-accessible.
	info, err := os.Stat(sock)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(unixSocketMode), info.Mode().Perm())

	require.NoError(t, run.stop(t))

	_, err = os.Stat(sock)
	require.True(t, errors.Is(err, fs.ErrNotExist), "socket file should be removed on shutdown")
}

func TestServe_TCPRequiresBearerToken(t *testing.T) {
	certDir := testCertDir(t)
	store, secret := storeWithToken(t)
	srv := newServeServer(t, store, certDir)

	addr := freeTCPAddr(t)
	run := startServe(t, srv, "", addr)

	client := tlsHTTPClient(t, filepath.Join(certDir, tlsCertFileName))
	url := "https://" + addr + "/v1/version"

	resp := waitForGet(t, client, url, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))

	resp = waitForGet(t, client, url, map[string]string{"Authorization": "Bearer " + secret})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"v1"}, doc.APIVersions)

	require.NoError(t, run.stop(t))
}

func TestServe_CancelStopsBothListeners(t *testing.T) {
	certDir := testCertDir(t)
	store, secret := storeWithToken(t)
	srv := newServeServer(t, store, certDir)

	sock := filepath.Join(shortTempDir(t), "api.sock")
	addr := freeTCPAddr(t)
	run := startServe(t, srv, sock, addr)

	unixResp := waitForGet(t, unixHTTPClient(sock), "http://unix/v1/version", nil)
	require.Equal(t, http.StatusOK, unixResp.StatusCode)
	unixResp.Body.Close()

	tcpClient := tlsHTTPClient(t, filepath.Join(certDir, tlsCertFileName))
	tcpResp := waitForGet(t, tcpClient, "https://"+addr+"/v1/version",
		map[string]string{"Authorization": "Bearer " + secret})
	require.Equal(t, http.StatusOK, tcpResp.StatusCode)
	tcpResp.Body.Close()

	require.NoError(t, run.stop(t))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix/v1/version", nil)
	require.NoError(t, err)
	_, err = unixHTTPClient(sock).Do(req)
	require.Error(t, err, "the Unix socket must be closed after shutdown")

	tcpReq, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+addr+"/v1/version", nil)
	require.NoError(t, err)
	_, err = tcpClient.Do(tcpReq)
	require.Error(t, err, "the TCP listener must be closed after shutdown")
}

func TestServe_TCPBindErrorNamesAddress(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer held.Close()
	addr := held.Addr().String()

	srv := newServeServer(t, nil, testCertDir(t))
	err = srv.Serve(context.Background(), "", addr)
	require.Error(t, err)
	require.Contains(t, err.Error(), addr)
}

func TestServe_UnixBindErrorNamesSocket(t *testing.T) {
	blocker := filepath.Join(shortTempDir(t), "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	sock := filepath.Join(blocker, "api.sock")

	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), sock, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), sock)
}

func TestServe_ReplacesStaleSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "api.sock")

	// Bind once and leave the file behind, which is what a crashed daemon does.
	stale, err := net.Listen("unix", sock)
	require.NoError(t, err)
	unixListener, ok := stale.(*net.UnixListener)
	require.True(t, ok)
	unixListener.SetUnlinkOnClose(false)
	require.NoError(t, unixListener.Close())
	_, err = os.Lstat(sock)
	require.NoError(t, err, "the stale socket file should still exist")

	srv := newServeServer(t, nil, "")
	run := startServe(t, srv, sock, "")

	resp := waitForGet(t, unixHTTPClient(sock), "http://unix/v1/version", nil)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, run.stop(t))
}

func TestServe_RefusesToReplaceRegularFile(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "api.sock")
	require.NoError(t, os.WriteFile(sock, []byte("keep me"), 0o600))

	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), sock, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), sock)

	data, err := os.ReadFile(sock)
	require.NoError(t, err)
	require.Equal(t, "keep me", string(data), "a non-socket file must not be deleted")
}

func TestServe_TCPNeedsCertificate(t *testing.T) {
	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), "", freeTCPAddr(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "CertDir")

	srv = newServeServer(t, nil, t.TempDir())
	err = srv.Serve(context.Background(), "", freeTCPAddr(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), tlsCertFileName)
}

func TestServe_NeedsAtLeastOneAddress(t *testing.T) {
	srv := newServeServer(t, nil, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := srv.Serve(ctx, "", "")
	require.Error(t, err)
}
