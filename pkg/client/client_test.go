package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigFromEnv(t *testing.T) {
	t.Setenv("SWARM_STATE_DIR", "/tmp/swarm-state")
	t.Setenv("SWARM_SOCKET", "/tmp/swarm.sock")

	cfg := DefaultConfig()
	assert.Equal(t, "/tmp/swarm.sock", cfg.SocketPath)
	assert.Equal(t, "/tmp/swarm-state", cfg.StateDir)
	assert.Equal(t, filepath.Join("/tmp/swarm-state", "certificates"), cfg.CertDir)
	assert.Equal(t, 10*time.Second, cfg.Timeout)
}

func TestConfigWithDefaults(t *testing.T) {
	t.Setenv("SWARM_STATE_DIR", "")
	t.Setenv("SWARM_SOCKET", "")

	cfg := Config{StateDir: "/tmp/custom"}.withDefaults()
	assert.Equal(t, "/var/run/swarmkit/swarm.sock", cfg.SocketPath)
	assert.Equal(t, "/tmp/custom", cfg.StateDir)
	assert.Equal(t, "/tmp/custom/certificates", cfg.CertDir)
	assert.Equal(t, 10*time.Second, cfg.Timeout)

	cfg = Config{CertDir: "/tmp/certs", Timeout: time.Second}.withDefaults()
	assert.Equal(t, "/tmp/certs", cfg.CertDir)
	assert.Equal(t, time.Second, cfg.Timeout)
}

func TestNewAndClose(t *testing.T) {
	dir := t.TempDir()
	writeTestCredentials(t, dir, true)

	c, err := New(Config{
		CertDir:    dir,
		SocketPath: filepath.Join(dir, "swarm.sock"),
		Timeout:    time.Second,
	})
	require.NoError(t, err)
	require.NotNil(t, c)
	require.NotNil(t, c.conn)
	require.NoError(t, c.Close())

	var nilClient *Client
	require.NoError(t, nilClient.Close())
}

func TestNewCredentialErrors(t *testing.T) {
	t.Run("missing certificate pair", func(t *testing.T) {
		_, err := New(Config{CertDir: t.TempDir()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load node certificate")
	})

	t.Run("missing cluster CA", func(t *testing.T) {
		dir := t.TempDir()
		writeTestCredentials(t, dir, false)

		_, err := New(Config{CertDir: dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read cluster CA")
	})

	t.Run("invalid cluster CA", func(t *testing.T) {
		dir := t.TempDir()
		writeTestCredentials(t, dir, true)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "swarm-root-ca.crt"), []byte("not a certificate"), 0o644))

		_, err := New(Config{CertDir: dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse cluster CA")
	})
}

func writeTestCredentials(t *testing.T, dir string, includeCA bool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "swarmcracker-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NotNil(t, certPEM)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NotNil(t, keyPEM)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "swarm-node.crt"), certPEM, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "swarm-node.key"), keyPEM, 0o600))
	if includeCA {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "swarm-root-ca.crt"), certPEM, 0o644))
	}
}
