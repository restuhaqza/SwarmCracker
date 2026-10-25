package broker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testPKI is a throwaway cluster CA plus one leaf certificate. The leaf is
// valid for both server and client authentication, because that is what the
// cluster node certificate does: the same identity serves the ingress mTLS
// endpoint and dials its peers. A test that used two different certificates
// would not exercise the real handshake.
type testPKI struct {
	cert tls.Certificate
	pool *x509.CertPool
}

// newTestPKI builds a CA and a leaf signed by it. The leaf is valid for
// localhost and the loopback addresses, so a client that verifies hostnames
// (rather than pinning the chain) can dial the test server without weakening
// verification.
func newTestPKI(t *testing.T) testPKI {
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

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return testPKI{
		cert: tls.Certificate{
			Certificate: [][]byte{leafDER},
			PrivateKey:  leafKey,
			Leaf:        leaf,
		},
		pool: pool,
	}
}

// serverConfig is the node side of the broker handshake: present the cluster
// certificate and require a peer certificate signed by the cluster CA.
func (p testPKI) serverConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.cert},
		ClientCAs:    p.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

// clientConfig is the manager side: present the cluster certificate and verify
// the node chain against the cluster CA.
func (p testPKI) clientConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.cert},
		RootCAs:      p.pool,
		MinVersion:   tls.VersionTLS12,
	}
}

// httpClient returns a client configured for the cluster mTLS handshake.
func (p testPKI) httpClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: p.clientConfig()},
	}
}

// clientWithoutCertificate verifies the server chain but presents no client
// certificate, which the broker must reject.
func (p testPKI) clientWithoutCertificate() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    p.pool,
			MinVersion: tls.VersionTLS12,
		}},
	}
}
