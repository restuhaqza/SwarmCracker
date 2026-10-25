package conformance

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
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
	envTaskID        = "CONFORMANCE_TASK_ID"
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
	if err := validateLabHost(host); err != nil {
		t.Fatal(err)
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

	// A task-scoped stream (exec, logs, attach) needs a task id that exists on
	// this cluster. It is not a token and not a secret: the operator names one.
	vars := map[string]string{}
	if task := strings.TrimSpace(os.Getenv(envTaskID)); task != "" {
		vars["task"] = task
	} else {
		t.Logf("%s is not set; task-scoped stream and snapshot cases will skip", envTaskID)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    labRoots(t),
			},
		},
	}

	// The capability set decides which gated cases run, and the build string is
	// the evidence a lab report has to name. The probe is a real request, so a
	// manager that cannot answer it fails the run instead of guessing.
	info := fetchVersion(t, client, host, reader)
	t.Logf("lab manager: %s", describeCapabilities(info))

	Run(t, Env{
		BaseURL:       host,
		Client:        client,
		Tokens:        tokens,
		Vars:          vars,
		Capabilities:  info.Capabilities,
		Lab:           true,
		AllowMutation: os.Getenv(envAllowMutation) == "1",
	}, "fixtures")
}

// validateLabHost enforces the lab transport rule: HTTPS only. The run carries
// bearer tokens and, with mutation allowed, creates real objects, so a cleartext
// listener is never an acceptable fallback — a typo must fail loudly instead of
// sending a token over plain HTTP.
func validateLabHost(host string) error {
	scheme, rest, _ := strings.Cut(host, "://")
	switch {
	case rest == "" || strings.TrimSpace(rest) == "":
		return fmt.Errorf("%s must be an absolute https:// URL", envHost)
	case !strings.EqualFold(scheme, "https"):
		return fmt.Errorf("%s must start with https:// (got %q): the lab run carries bearer tokens and refuses a cleartext target",
			envHost, scheme+"://")
	}
	return nil
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

// TestValidateLabHost pins the transport rule: the lab run carries bearer tokens,
// so a cleartext URL is refused outright instead of silently downgraded.
func TestValidateLabHost(t *testing.T) {
	tests := []struct {
		host    string
		wantErr bool
	}{
		{host: "https://127.0.0.1:4244"},
		{host: "HTTPS://manager.example:8443"},
		{host: "http://127.0.0.1:4244", wantErr: true},
		{host: "http://manager.example:8443", wantErr: true},
		{host: "127.0.0.1:4244", wantErr: true},
		{host: "https://", wantErr: true},
		{host: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			err := validateLabHost(tc.host)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateLabHost(%q) = %v, want an error: %v", tc.host, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "https://") {
				t.Fatalf("the error must name the https:// rule, got %v", err)
			}
		})
	}
}
