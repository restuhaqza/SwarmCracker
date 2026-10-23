package testhelpers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ShortTempDir returns a temporary directory with a short absolute path. A Unix
// socket address must fit the platform's sun_path limit, which a test-named
// t.TempDir() can exceed on macOS.
func ShortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "apitest")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// FreeTCPAddr returns a loopback address whose port was free a moment ago.
func FreeTCPAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	return addr
}

// UnixHTTPClient dials the Unix socket at path for a fixed host name.
func UnixHTTPClient(path string) *http.Client {
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

// TLSHTTPClient trusts exactly the certificate at certFile.
func TLSHTTPClient(t *testing.T, certFile string) *http.Client {
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

// WaitForGet sends a GET with the given Accept media type, retrying transport
// failures until the listener has finished binding. Any HTTP response is final,
// including 401.
func WaitForGet(t *testing.T, c *http.Client, url, accept string, headers map[string]string) *http.Response {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", accept)
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
