package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/broker"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// runBrokerFlags parses args through the daemon's real flag set and returns the
// resolved broker runtime.
func runBrokerFlags(t *testing.T, args ...string) brokerRuntime {
	t.Helper()

	var got brokerRuntime
	app := cli.NewApp()
	app.Flags = newAppFlags()
	app.Action = func(c *cli.Context) error {
		got = brokerRuntimeFrom(c)
		return nil
	}
	require.NoError(t, app.Run(append([]string{"swarmd-firecracker"}, args...)))
	return got
}

func TestBrokerRuntimeFrom_Defaults(t *testing.T) {
	cfg := runBrokerFlags(t)

	require.Equal(t, "0.0.0.0:4245", cfg.listenAddr)
	require.Equal(t, "/var/run/firecracker", cfg.socketDir)
}

func TestBrokerRuntimeFrom_EmptyListenDisables(t *testing.T) {
	cfg := runBrokerFlags(t, "--broker-listen", "")

	require.Empty(t, cfg.listenAddr, "an empty --broker-listen disables the broker")
}

func TestBrokerRuntimeFrom_Overrides(t *testing.T) {
	cfg := runBrokerFlags(t,
		"--broker-listen", "127.0.0.1:5252",
		"--socket-dir", "/srv/firecracker",
	)

	require.Equal(t, "127.0.0.1:5252", cfg.listenAddr)
	require.Equal(t, "/srv/firecracker", cfg.socketDir)
}

// seedBrokerCertificates writes the cluster node certificate, key and root CA
// into <stateDir>/certificates the way the SwarmKit node does, and returns the
// pair a test client must present.
//
// The production cluster certificate is a single identity used for both server
// and client authentication, so the leaf carries both EKUs.
func seedBrokerCertificates(t *testing.T, stateDir string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	now := time.Now()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "swarmcracker-broker-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "swarmcracker-broker-test-node"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	require.NoError(t, err)

	certDir := filepath.Join(stateDir, "certificates")
	require.NoError(t, os.MkdirAll(certDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-node.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-node.key"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(certDir, "swarm-root-ca.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		0o644,
	))

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return tls.Certificate{
		Certificate: [][]byte{leafDER},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}, pool
}

// brokerTestClient dials with the cluster certificate and verifies the node
// chain against the cluster CA.
func brokerTestClient(cert tls.Certificate, pool *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
				MinVersion:   tls.VersionTLS12,
			},
		},
	}
}

func TestStartBroker_DisabledIsNoOp(t *testing.T) {
	srv, stop, err := startBroker(context.Background(), brokerRuntime{}, t.TempDir())
	require.NoError(t, err)
	require.Nil(t, srv)
	require.NotNil(t, stop)

	stop()
	stop() // idempotent
}

func TestStartBroker_RequiresCertificates(t *testing.T) {
	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: t.TempDir()}

	srv, stop, err := startBroker(context.Background(), cfg, t.TempDir())
	require.Error(t, err)
	require.Nil(t, srv)
	require.Nil(t, stop)
}

func TestStartBroker_ServesMTLSAndStops(t *testing.T) {
	stateDir := t.TempDir()
	clientCert, pool := seedBrokerCertificates(t, stateDir)
	socketDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "task-1.sock"), nil, 0o600))

	cfg := brokerRuntime{listenAddr: "127.0.0.1:0", socketDir: socketDir}
	srv, stop, err := startBroker(context.Background(), cfg, stateDir)
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, stop)

	require.NoError(t, srv.Handle("health", func(_ context.Context, task broker.Task, w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "task="+task.ID)
	}))

	client := brokerTestClient(clientCert, pool)
	resp, err := client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/health")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "task=task-1", string(body))

	stop()
	stop() // idempotent

	client.CloseIdleConnections()
	_, err = client.Get("https://" + srv.Addr() + "/v1/tasks/task-1/health")
	require.Error(t, err, "the stop function must shut the broker down")
}
