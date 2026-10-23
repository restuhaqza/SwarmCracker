package apihttp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const testHostname = "manager.example.com"

// loadLeaf loads the certificate and key at certFile/keyFile and returns the
// parsed leaf together with the raw certificate PEM.
func loadLeaf(t *testing.T, certFile, keyFile string) (*x509.Certificate, []byte) {
	t.Helper()

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err)
	require.NotEmpty(t, pair.Certificate)

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)

	data, err := os.ReadFile(certFile)
	require.NoError(t, err)
	return leaf, data
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// assertUnique fails when values contains a duplicate.
func assertUnique(t *testing.T, values []string) {
	t.Helper()
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		require.False(t, seen[value], "duplicate SAN %q in %v", value, values)
		seen[value] = true
	}
}

// writePair writes certPEM/keyPEM at the canonical paths inside dir, creating
// the directory when needed.
func writePair(t *testing.T, dir string, certPEM, keyPEM []byte) (string, string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))

	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o644))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))
	return certFile, keyFile
}

func TestEnsureCertificateCreatesPair(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")

	certFile, keyFile, err := EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, "tls.crt"), certFile)
	assert.Equal(t, filepath.Join(dir, "tls.key"), keyFile)

	_, err = tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err, "the generated pair must load as a TLS key pair")

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "created directory must be 0700")

	certInfo, err := os.Stat(certFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), certInfo.Mode().Perm(), "the public certificate is 0644")

	keyInfo, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm(), "the private key is 0600")
}

func TestEnsureCertificateProperties(t *testing.T) {
	certFile, keyFile, err := EnsureCertificate(t.TempDir(), testHostname, nil)
	require.NoError(t, err)

	leaf, _ := loadLeaf(t, certFile, keyFile)

	assert.Equal(t, "swarmcracker-api", leaf.Subject.CommonName)
	assert.True(t, leaf.IsCA, "the bootstrap certificate is also the trust root")
	assert.True(t, leaf.BasicConstraintsValid)
	assert.NotZero(t, leaf.KeyUsage&x509.KeyUsageDigitalSignature)
	assert.NotZero(t, leaf.KeyUsage&x509.KeyUsageCertSign)
	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth)

	now := time.Now()
	assert.WithinDuration(t, now.Add(-5*time.Minute), leaf.NotBefore, time.Minute)
	assert.WithinDuration(t, now.Add(365*24*time.Hour), leaf.NotAfter, time.Minute)
	assert.Positive(t, leaf.SerialNumber.Sign(), "the serial number must be positive")

	assert.Equal(t, x509.ECDSA, leaf.PublicKeyAlgorithm)
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.Equal(t, elliptic.P256(), pub.Curve)
}

func TestEnsureCertificateSANs(t *testing.T) {
	extras := []string{"api.example.com", "10.1.2.3", "api.example.com", "::1"}

	certFile, keyFile, err := EnsureCertificate(t.TempDir(), testHostname, extras)
	require.NoError(t, err)

	leaf, _ := loadLeaf(t, certFile, keyFile)

	assert.Contains(t, leaf.DNSNames, "localhost")
	assert.Contains(t, leaf.DNSNames, testHostname)
	assert.Contains(t, leaf.DNSNames, "api.example.com")
	assertUnique(t, leaf.DNSNames)

	var ipStrings []string
	for _, ip := range leaf.IPAddresses {
		ipStrings = append(ipStrings, ip.String())
	}
	assert.Contains(t, ipStrings, "127.0.0.1")
	assert.Contains(t, ipStrings, "::1")
	assert.Contains(t, ipStrings, "10.1.2.3")
	assertUnique(t, ipStrings)

	assert.NotContains(t, leaf.DNSNames, "10.1.2.3", "an IP extra host belongs in IP SANs")
}

func TestEnsureCertificateEmptyHostnameAddsOnlyLocalNames(t *testing.T) {
	certFile, keyFile, err := EnsureCertificate(t.TempDir(), "  ", nil)
	require.NoError(t, err)

	leaf, _ := loadLeaf(t, certFile, keyFile)
	assert.Equal(t, []string{"localhost"}, leaf.DNSNames)
}

func TestEnsureCertificateIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	certFile, keyFile, err := EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)

	firstCert := readFile(t, certFile)
	firstKey := readFile(t, keyFile)
	certInfo, err := os.Stat(certFile)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyFile)
	require.NoError(t, err)

	againCertFile, againKeyFile, err := EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)
	assert.Equal(t, certFile, againCertFile)
	assert.Equal(t, keyFile, againKeyFile)

	assert.Equal(t, firstCert, readFile(t, certFile), "a valid pair must not be rewritten")
	assert.Equal(t, firstKey, readFile(t, keyFile), "a valid pair must not be rewritten")

	certInfo2, err := os.Stat(certFile)
	require.NoError(t, err)
	keyInfo2, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.True(t, certInfo.ModTime().Equal(certInfo2.ModTime()), "cert mtime must not change")
	assert.True(t, keyInfo.ModTime().Equal(keyInfo2.ModTime()), "key mtime must not change")
}

func TestEnsureCertificateKeyMode(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}

	dir := t.TempDir()
	_, keyFile, err := EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)

	info, err := os.Stat(keyFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestEnsureCertificateRegeneratesExpiringSoon(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	oldCertPEM, oldKeyPEM, err := generateCertificate(testHostname, nil, now.Add(-time.Minute), now.Add(10*24*time.Hour))
	require.NoError(t, err)
	certFile, keyFile := writePair(t, dir, oldCertPEM, oldKeyPEM)

	certFile, keyFile, err = EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)

	assert.NotEqual(t, oldCertPEM, readFile(t, certFile), "a certificate expiring within 30 days must be replaced")
	assert.NotEqual(t, oldKeyPEM, readFile(t, keyFile), "the key is regenerated with the certificate")

	leaf, _ := loadLeaf(t, certFile, keyFile)
	assert.WithinDuration(t, time.Now().Add(365*24*time.Hour), leaf.NotAfter, time.Minute)
}

func TestEnsureCertificateRenewalBoundary(t *testing.T) {
	cases := map[string]struct {
		remaining  time.Duration
		regenerate bool
	}{
		"31 days left is reused":      {remaining: 31 * 24 * time.Hour},
		"exactly 30 days regenerates": {remaining: 30 * 24 * time.Hour, regenerate: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()
			oldCertPEM, oldKeyPEM, err := generateCertificate(testHostname, nil, now.Add(-time.Minute), now.Add(tc.remaining))
			require.NoError(t, err)
			certFile, keyFile := writePair(t, dir, oldCertPEM, oldKeyPEM)

			certFile, keyFile, err = EnsureCertificate(dir, testHostname, nil)
			require.NoError(t, err)

			if !tc.regenerate {
				assert.Equal(t, oldCertPEM, readFile(t, certFile), "more than 30 days left must be reused")
				assert.Equal(t, oldKeyPEM, readFile(t, keyFile))
				return
			}

			assert.NotEqual(t, oldCertPEM, readFile(t, certFile), "30 days or less left must be replaced")
			assert.NotEqual(t, oldKeyPEM, readFile(t, keyFile))

			leaf, _ := loadLeaf(t, certFile, keyFile)
			assert.WithinDuration(t, time.Now().Add(tlsValidity), leaf.NotAfter, time.Minute)
		})
	}
}

func TestEnsureCertificateRegeneratesNotYetValidPair(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	futureCertPEM, futureKeyPEM, err := generateCertificate(testHostname, nil, now.Add(time.Hour), now.Add(tlsValidity))
	require.NoError(t, err)
	certFile, keyFile := writePair(t, dir, futureCertPEM, futureKeyPEM)

	certFile, keyFile, err = EnsureCertificate(dir, testHostname, nil)
	require.NoError(t, err)

	assert.NotEqual(t, futureCertPEM, readFile(t, certFile), "a not-yet-valid certificate must be replaced")
	assert.NotEqual(t, futureKeyPEM, readFile(t, keyFile))

	leaf, _ := loadLeaf(t, certFile, keyFile)
	assert.False(t, leaf.NotBefore.After(time.Now()), "the replacement must already be valid")
	assertNoTempCertificateFiles(t, dir)
}

func TestEnsureCertificateRegeneratesCorruptPair(t *testing.T) {
	cases := map[string]struct {
		cert, key []byte
		remove    string
	}{
		"corrupt cert": {cert: []byte("not a certificate"), key: nil},
		"corrupt key":  {cert: nil, key: []byte("not a key")},
		"missing key":  {remove: "key"},
		"missing cert": {remove: "cert"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			certFile, keyFile, err := EnsureCertificate(dir, testHostname, nil)
			require.NoError(t, err)
			validCert := readFile(t, certFile)

			switch tc.remove {
			case "key":
				require.NoError(t, os.Remove(keyFile))
			case "cert":
				require.NoError(t, os.Remove(certFile))
			}
			if tc.cert != nil {
				require.NoError(t, os.WriteFile(certFile, tc.cert, 0o644))
			}
			if tc.key != nil {
				require.NoError(t, os.WriteFile(keyFile, tc.key, 0o600))
			}

			certFile, keyFile, err = EnsureCertificate(dir, testHostname, nil)
			require.NoError(t, err)

			_, err = tls.LoadX509KeyPair(certFile, keyFile)
			require.NoError(t, err)
			assert.NotEqual(t, validCert, readFile(t, certFile), "a corrupt pair must be regenerated")
			assertNoTempCertificateFiles(t, dir)
		})
	}
}

func TestEnsureCertificateVerifiesAsOwnRoot(t *testing.T) {
	certFile, keyFile, err := EnsureCertificate(t.TempDir(), testHostname, []string{"10.1.2.3"})
	require.NoError(t, err)

	leaf, certPEM := loadLeaf(t, certFile, keyFile)

	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(certPEM), "the certificate must be valid PEM")

	for _, name := range []string{testHostname, "localhost", "127.0.0.1", "::1", "10.1.2.3"} {
		t.Run(name, func(t *testing.T) {
			_, err := leaf.Verify(x509.VerifyOptions{
				Roots:     roots,
				DNSName:   name,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			require.NoError(t, err)
		})
	}

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:   roots,
		DNSName: "unknown.example.com",
	})
	require.Error(t, err, "a name outside the SANs must not verify")
}

func TestEnsureCertificateRejectsEmptyDir(t *testing.T) {
	for _, dir := range []string{"", "   "} {
		_, _, err := EnsureCertificate(dir, testHostname, nil)
		require.Error(t, err)
		assert.Equal(t, output.CodeInvalid, requireErrorCode(t, err))
	}
}

func TestEnsureCertificateTrimsDir(t *testing.T) {
	dir := t.TempDir()

	certFile, keyFile, err := EnsureCertificate("  "+dir+"  ", testHostname, nil)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, "tls.crt"), certFile)
	assert.Equal(t, filepath.Join(dir, "tls.key"), keyFile)
	_, err = tls.LoadX509KeyPair(certFile, keyFile)
	require.NoError(t, err)
}

func TestEnsureCertificateSanitizesExtraHosts(t *testing.T) {
	certFile, keyFile, err := EnsureCertificate(t.TempDir(), "", []string{" ", "", " db.internal ", "db.internal"})
	require.NoError(t, err)

	leaf, _ := loadLeaf(t, certFile, keyFile)

	assert.Equal(t, []string{"localhost", "db.internal"}, leaf.DNSNames)

	var ipStrings []string
	for _, ip := range leaf.IPAddresses {
		ipStrings = append(ipStrings, ip.String())
	}
	assert.Equal(t, []string{"127.0.0.1", "::1"}, ipStrings)
}

func TestEnsureCertificateKeepsValidPairRegardlessOfHosts(t *testing.T) {
	dir := t.TempDir()

	certFile, keyFile, err := EnsureCertificate(dir, testHostname, []string{"a.example.com"})
	require.NoError(t, err)
	firstCert := readFile(t, certFile)
	firstKey := readFile(t, keyFile)

	_, _, err = EnsureCertificate(dir, "other.example.com", []string{"b.example.com"})
	require.NoError(t, err)

	assert.Equal(t, firstCert, readFile(t, certFile), "a still-valid pair is never rewritten for new hosts")
	assert.Equal(t, firstKey, readFile(t, keyFile))
}

func TestEnsureCertificateReportsWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}

	dir := filepath.Join(t.TempDir(), "tls")
	require.NoError(t, os.MkdirAll(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, _, err := EnsureCertificate(dir, testHostname, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, requireErrorCode(t, err))
	assertNoTempCertificateFiles(t, dir)
}

func TestEnsureCertificateRecoversWhenCertRenameFails(t *testing.T) {
	dir := t.TempDir()

	// A non-empty directory at the certificate path makes the final rename fail
	// after the key has already been renamed into place. Rename reports EISDIR
	// here (the source is a file, not a directory); any error takes the same
	// recovery branch.
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	require.NoError(t, os.MkdirAll(certFile, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(certFile, "occupied"), []byte("x"), 0o600))

	_, _, err := EnsureCertificate(dir, testHostname, nil)
	require.Error(t, err)
	assert.Equal(t, output.CodeInternal, requireErrorCode(t, err))
	assert.Contains(t, err.Error(), "write certificate pair in", "the failure must come from the write, not an earlier step")

	_, statErr := os.Stat(keyFile)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the already-renamed key must be removed")
	assertNoTempCertificateFiles(t, dir)
}

func TestEnsureCertificateConcurrentCalls(t *testing.T) {
	dir := t.TempDir()
	const callers = 8

	type result struct {
		certFile, keyFile string
		err               error
	}
	results := make([]result, callers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].certFile, results[i].keyFile, results[i].err = EnsureCertificate(dir, testHostname, nil)
		}(i)
	}
	wg.Wait()

	for i, res := range results {
		require.NoError(t, res.err, "caller %d", i)
	}

	first := results[0]
	_, err := tls.LoadX509KeyPair(first.certFile, first.keyFile)
	require.NoError(t, err)

	wantKey := readFile(t, first.keyFile)
	for i, res := range results[1:] {
		assert.Equal(t, first.certFile, res.certFile)
		assert.Equal(t, wantKey, readFile(t, res.keyFile), "caller %d saw a different key", i+1)
	}
	assertNoTempCertificateFiles(t, dir)
}

// assertNoTempCertificateFiles fails when a write left a temporary file behind.
func assertNoTempCertificateFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tls.*-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, matches, "writes must clean up their temporary files")
}
