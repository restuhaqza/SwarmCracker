package conformance

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strings"
	"testing"
)

// The lab run's environment contract. Tokens are read from the environment and
// never printed; a missing optional token skips only the cases that need it.
const (
	envHost          = "CONFORMANCE_HOST"
	envCA            = "CONFORMANCE_CA"
	envReaderToken   = "CONFORMANCE_READER_TOKEN"
	envRunnerToken   = "CONFORMANCE_RUNNER_TOKEN"
	envAdminToken    = "CONFORMANCE_ADMIN_TOKEN"
	envAllowMutation = "CONFORMANCE_ALLOW_MUTATION"
)

// TestConformanceLab runs the same fixtures against a live manager. It skips
// cleanly when the environment is not configured, which is what makes the
// scheduled workflow safe on a fork.
func TestConformanceLab(t *testing.T) {
	host := strings.TrimSpace(os.Getenv(envHost))
	reader := strings.TrimSpace(os.Getenv(envReaderToken))
	if host == "" || reader == "" {
		t.Skipf("lab conformance needs %s and %s; configure the environment to run it", envHost, envReaderToken)
	}
	if !strings.HasPrefix(host, "https://") && !strings.HasPrefix(host, "http://") {
		t.Fatalf("%s must be an http:// or https:// base URL", envHost)
	}

	tokens := map[string]string{"reader": reader}
	for role, name := range map[string]string{"runner": envRunnerToken, "admin": envAdminToken} {
		token := strings.TrimSpace(os.Getenv(name))
		if token == "" {
			t.Logf("%s is not set; cases that need the %s role will skip", name, role)
			continue
		}
		tokens[role] = token
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    labRoots(t),
			},
		},
	}

	Run(t, Env{
		BaseURL:       host,
		Client:        client,
		Tokens:        tokens,
		Lab:           true,
		AllowMutation: os.Getenv(envAllowMutation) == "1",
	}, "fixtures")
}

// labRoots returns the trust store for the lab listener: the system roots plus
// the operator's CA when CONFORMANCE_CA names one.
func labRoots(t *testing.T) *x509.CertPool {
	t.Helper()

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	path := strings.TrimSpace(os.Getenv(envCA))
	if path == "" {
		t.Logf("%s is not set; trusting the system roots", envCA)
		return pool
	}

	pem, err := os.ReadFile(path)
	if err != nil {
		// The path is not secret; the contents are, and they are never printed.
		t.Fatalf("read %s from %s: %v", envCA, path, err)
	}
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("%s (%s) contains no PEM certificate", envCA, path)
	}
	return pool
}
