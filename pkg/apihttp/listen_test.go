package apihttp

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

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/restuhaqza/swarmcracker/test/testhelpers"
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

// testCertDir generates the bootstrap TLS pair and returns its directory.
func testCertDir(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "api")
	_, _, err := EnsureCertificate(dir, "localhost", nil)
	require.NoError(t, err)
	return dir
}

// newServeServer builds a server with the given token store: the Unix socket
// needs none, and the TCP tests install their own.
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
	sock := filepath.Join(testhelpers.ShortTempDir(t), "run", "api.sock")
	srv := newServeServer(t, nil, "")
	run := startServe(t, srv, sock, "")

	resp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", MediaType, nil)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, MediaType, resp.Header.Get("Content-Type"))
	require.Equal(t, APIVersion, resp.Header.Get("X-SwarmCracker-Api-Version"))

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"v1"}, doc.APIVersions)
	require.Equal(t, "cluster-1", doc.ClusterID)

	// The socket's file mode is the credential, so it must not be group- or
	// world-accessible, and neither must the directory Serve created for it.
	info, err := os.Stat(sock)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(unixSocketMode), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Dir(sock))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(unixDirMode), dirInfo.Mode().Perm())

	require.NoError(t, run.stop(t))

	_, err = os.Stat(sock)
	require.True(t, errors.Is(err, fs.ErrNotExist), "socket file should be removed on shutdown")
}

func TestServe_TCPRequiresBearerToken(t *testing.T) {
	certDir := testCertDir(t)
	store, secret := storeWithToken(t)
	srv := newServeServer(t, store, certDir)

	addr := testhelpers.FreeTCPAddr(t)
	run := startServe(t, srv, "", addr)

	client := testhelpers.TLSHTTPClient(t, filepath.Join(certDir, tlsCertFileName))
	url := "https://" + addr + "/v1/version"

	resp := testhelpers.WaitForGet(t, client, url, MediaType, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"))

	resp = testhelpers.WaitForGet(t, client, url, MediaType, map[string]string{"Authorization": "Bearer " + secret})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var doc v1.VersionInfo
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	require.Equal(t, []string{"v1"}, doc.APIVersions)

	require.NoError(t, run.stop(t))
}

// TestServe_TCPSeesTokenCreatedAfterStart is the daemon's real sequence: the API
// starts with an empty store and an operator mints the first token in another
// process. The running server must accept it without a restart.
func TestServe_TCPSeesTokenCreatedAfterStart(t *testing.T) {
	certDir := testCertDir(t)
	tokenPath := filepath.Join(testhelpers.ShortTempDir(t), "api", "tokens.json")

	store, err := OpenTokenStore(tokenPath)
	require.NoError(t, err)
	srv := newServeServer(t, store, certDir)

	addr := testhelpers.FreeTCPAddr(t)
	run := startServe(t, srv, "", addr)
	defer func() { require.NoError(t, run.stop(t)) }()

	client := testhelpers.TLSHTTPClient(t, filepath.Join(certDir, tlsCertFileName))
	url := "https://" + addr + "/v1/version"

	resp := testhelpers.WaitForGet(t, client, url, MediaType, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// A second store stands in for `swarmcracker api token create` running while
	// the daemon is up.
	writer, err := OpenTokenStore(tokenPath)
	require.NoError(t, err)
	secret, _, err := writer.Create("operator", RoleReader)
	require.NoError(t, err)

	resp = testhelpers.WaitForGet(t, client, url, MediaType, map[string]string{"Authorization": "Bearer " + secret})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "a token minted after start-up must authenticate")
}

func TestServe_CancelStopsBothListeners(t *testing.T) {
	certDir := testCertDir(t)
	store, secret := storeWithToken(t)
	srv := newServeServer(t, store, certDir)

	sock := filepath.Join(testhelpers.ShortTempDir(t), "api.sock")
	addr := testhelpers.FreeTCPAddr(t)
	run := startServe(t, srv, sock, addr)

	unixResp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", MediaType, nil)
	require.Equal(t, http.StatusOK, unixResp.StatusCode)
	unixResp.Body.Close()

	tcpClient := testhelpers.TLSHTTPClient(t, filepath.Join(certDir, tlsCertFileName))
	tcpResp := testhelpers.WaitForGet(t, tcpClient, "https://"+addr+"/v1/version", MediaType,
		map[string]string{"Authorization": "Bearer " + secret})
	require.Equal(t, http.StatusOK, tcpResp.StatusCode)
	tcpResp.Body.Close()

	require.NoError(t, run.stop(t))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://unix/v1/version", nil)
	require.NoError(t, err)
	_, err = testhelpers.UnixHTTPClient(sock).Do(req)
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
	blocker := filepath.Join(testhelpers.ShortTempDir(t), "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	sock := filepath.Join(blocker, "api.sock")

	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), sock, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), sock)
}

func TestServe_ReplacesStaleSocket(t *testing.T) {
	sock := filepath.Join(testhelpers.ShortTempDir(t), "api.sock")

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

	resp := testhelpers.WaitForGet(t, testhelpers.UnixHTTPClient(sock), "http://unix/v1/version", MediaType, nil)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, run.stop(t))
}

func TestServe_RefusesToReplaceRegularFile(t *testing.T) {
	sock := filepath.Join(testhelpers.ShortTempDir(t), "api.sock")
	require.NoError(t, os.WriteFile(sock, []byte("keep me"), 0o600))

	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), sock, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), sock)

	// Refusing an operator path is a configuration mistake, not an internal
	// failure: the caller must see the code it was created with.
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, output.CodeInvalid, typed.Code)

	data, err := os.ReadFile(sock)
	require.NoError(t, err)
	require.Equal(t, "keep me", string(data), "a non-socket file must not be deleted")
}

func TestServe_TCPNeedsCertificate(t *testing.T) {
	srv := newServeServer(t, nil, "")
	err := srv.Serve(context.Background(), "", testhelpers.FreeTCPAddr(t))
	require.Error(t, err)
	require.Contains(t, err.Error(), "CertDir")

	srv = newServeServer(t, nil, t.TempDir())
	err = srv.Serve(context.Background(), "", testhelpers.FreeTCPAddr(t))
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
