package apihttp

// This file owns the manager API's bootstrap TLS certificate: a self-signed
// ECDSA P-256 server certificate persisted next to the token store. It exists so
// the HTTPS listener starts without operator-provided files. The same
// certificate acts as its own trust root (IsCA), so a client can pin tls.crt as
// its CA; replacing it with a cluster-CA-signed leaf is a follow-up.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// tlsMu serializes EnsureCertificate. A pair spans two files, so two concurrent
// regenerations could otherwise interleave their renames and leave a
// certificate with the wrong key. Generation happens at most once per manager
// start, so a single package-wide lock is cheap enough.
var tlsMu sync.Mutex

const (
	tlsCertFileName = "tls.crt"
	tlsKeyFileName  = "tls.key"

	tlsDirMode  = 0o700
	tlsKeyMode  = 0o600
	tlsCertMode = 0o644

	tlsCommonName = "swarmcracker-api"

	// tlsValidity is how long a freshly generated certificate is valid.
	tlsValidity = 365 * 24 * time.Hour
	// tlsRenewBefore is the remaining lifetime below which a pair is replaced.
	tlsRenewBefore = 30 * 24 * time.Hour
	// tlsNotBeforeSkew backdates NotBefore to tolerate small clock skew.
	tlsNotBeforeSkew = 5 * time.Minute

	tlsSerialBits = 128
)

// EnsureCertificate returns the paths to a persisted server certificate and
// key, generating them when absent or within 30 days of expiry.
//
// The files are <dir>/tls.crt (0644) and <dir>/tls.key (0600); dir is created
// with mode 0700 when missing. A valid existing pair is reused untouched.
func EnsureCertificate(dir, hostname string, extraHosts []string) (certFile, keyFile string, err error) {
	if strings.TrimSpace(dir) == "" {
		return "", "", output.NewError(output.CodeInvalid, "certificate directory must not be empty")
	}

	tlsMu.Lock()
	defer tlsMu.Unlock()

	certFile = filepath.Join(dir, tlsCertFileName)
	keyFile = filepath.Join(dir, tlsKeyFileName)

	if certificateIsValid(certFile, keyFile) {
		return certFile, keyFile, nil
	}

	if err := os.MkdirAll(dir, tlsDirMode); err != nil {
		return "", "", output.NewErrorf(output.CodeInternal, "create certificate directory %s: %v", dir, err)
	}

	now := time.Now()
	certPEM, keyPEM, err := generateCertificate(hostname, extraHosts, now.Add(-tlsNotBeforeSkew), now.Add(tlsValidity))
	if err != nil {
		return "", "", output.NewErrorf(output.CodeInternal, "generate certificate: %v", err)
	}

	if err := writeCertificatePair(certFile, keyFile, certPEM, keyPEM); err != nil {
		return "", "", output.NewErrorf(output.CodeInternal, "write certificate pair in %s: %v", dir, err)
	}

	return certFile, keyFile, nil
}

// certificateIsValid reports whether certFile/keyFile form a loadable pair whose
// leaf still outlives tlsRenewBefore.
func certificateIsValid(certFile, keyFile string) bool {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return false
	}
	if len(pair.Certificate) == 0 {
		return false
	}

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	return time.Now().Add(tlsRenewBefore).Before(leaf.NotAfter)
}

// generateCertificate returns a self-signed PEM certificate and its ECDSA
// P-256 private key. The caller supplies the validity window so tests can build
// deliberately expiring pairs.
func generateCertificate(hostname string, extraHosts []string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}

	dnsNames, ipAddresses := certificateHosts(hostname, extraHosts)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: tlsCommonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// randomSerial returns a random positive 128-bit serial number.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), tlsSerialBits)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	return serial.Add(serial, big.NewInt(1)), nil
}

// certificateHosts builds the SAN lists: localhost, the optional hostname, the
// loopback addresses and every extra host, each kept once in input order. Extra
// hosts that parse as IP addresses become IP SANs, the rest DNS SANs.
func certificateHosts(hostname string, extraHosts []string) (dnsNames []string, ipAddresses []net.IP) {
	dnsSeen := make(map[string]bool)
	ipSeen := make(map[string]bool)

	addDNS := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || dnsSeen[name] {
			return
		}
		dnsSeen[name] = true
		dnsNames = append(dnsNames, name)
	}
	addIP := func(ip net.IP) {
		if ip == nil || ipSeen[ip.String()] {
			return
		}
		ipSeen[ip.String()] = true
		ipAddresses = append(ipAddresses, ip)
	}

	addDNS("localhost")
	addDNS(hostname)
	addIP(net.ParseIP("127.0.0.1"))
	addIP(net.ParseIP("::1"))

	for _, host := range extraHosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			addIP(ip)
			continue
		}
		addDNS(host)
	}

	return dnsNames, ipAddresses
}

// writeCertificatePair writes both files via temporary files in dir and renames
// them into place. On failure it removes any temporary files and a key it has
// already renamed, so the next call sees a missing file and regenerates.
func writeCertificatePair(certFile, keyFile string, certPEM, keyPEM []byte) error {
	keyTmp, err := writeTempFile(keyFile, keyPEM, tlsKeyMode)
	if err != nil {
		return err
	}
	certTmp, err := writeTempFile(certFile, certPEM, tlsCertMode)
	if err != nil {
		_ = os.Remove(keyTmp)
		return err
	}

	if err := os.Rename(keyTmp, keyFile); err != nil {
		_ = os.Remove(keyTmp)
		_ = os.Remove(certTmp)
		return err
	}
	if err := os.Rename(certTmp, certFile); err != nil {
		_ = os.Remove(certTmp)
		// Drop the new key so the pair is not left half-updated; a missing key
		// makes the next call regenerate both files.
		_ = os.Remove(keyFile)
		return err
	}

	return nil
}

// writeTempFile writes data to a temporary file beside path with mode and
// returns the temporary path. The caller renames or removes it.
func writeTempFile(path string, data []byte, mode os.FileMode) (string, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()

	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}

	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}
