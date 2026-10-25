package conformance

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	// envAllowSkips is the one opt-out from the lab leg's strict skip policy.
	// Without it, a case that did not run fails the run.
	envAllowSkips = "CONFORMANCE_ALLOW_SKIPS"
)

// tokenEnvForRole names the variable that supplies each role's token, so a skip
// can say which variable to set.
var tokenEnvForRole = map[string]string{
	"reader": envReaderToken,
	"runner": envRunnerToken,
	"admin":  envAdminToken,
}

// envForVariable names the variable that supplies a $name the environment seeds.
// A value that comes from an earlier response instead ($snapshot, $service) has
// no variable: the case that captures it has to run.
var envForVariable = map[string]string{
	"task": envTaskID,
}

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

	summary := Run(t, Env{
		BaseURL:       host,
		Client:        client,
		Tokens:        tokens,
		Vars:          vars,
		Capabilities:  info.Capabilities,
		Lab:           true,
		AllowMutation: os.Getenv(envAllowMutation) == "1",
	}, "fixtures")

	// Strict by default: a case that did not run is a failure, not a note. A lab
	// run that quietly executes half the fixture set proves nothing about the
	// contract, and a green run is the one an operator cites as evidence.
	if err := labSkipFailure(summary, allowSkips()); err != nil {
		t.Fatal(err)
	}
}

// allowSkips reports whether the operator opted out of the strict skip policy.
func allowSkips() bool {
	return strings.TrimSpace(os.Getenv(envAllowSkips)) == "1"
}

// labSkipFailure is the lab leg's skip policy: every case that did not run is a
// failure unless the operator set CONFORMANCE_ALLOW_SKIPS=1. The message names
// each skipped case and what would let it run, so the fix is a variable, not an
// investigation.
//
// A mock-only case is the one exception: no setting can make it run against a
// live manager, and the mock leg is where it is proven.
func labSkipFailure(summary Summary, allowed bool) error {
	if allowed {
		return nil
	}

	lines := make([]string, 0, len(summary.Skips))
	for _, skip := range summary.Skips {
		if skip.Kind == SkipMockOnly {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", skip.Case, enableHint(skip)))
	}
	if len(lines) == 0 {
		return nil
	}

	return fmt.Errorf("%d of %d lab cases should have run but were skipped, so this run proves less than it claims:\n%s\n"+
		"Set %s=1 to accept the skips (the workflow sets it only for a mutation-disabled schedule).",
		len(lines), summary.Cases, strings.Join(lines, "\n"), envAllowSkips)
}

// enableHint says what would let a skipped case run. It names the variable, not
// the symptom: a capability gate needs a manager build, a mutation needs the
// switch, a missing variable needs the variable set.
func enableHint(skip Skip) string {
	switch skip.Kind {
	case SkipMutation:
		return "set " + envAllowMutation + "=1"
	case SkipCapability:
		return fmt.Sprintf("a manager that advertises %s", skip.Detail)
	case SkipToken:
		if name, ok := tokenEnvForRole[skip.Detail]; ok {
			return "set " + name
		}
		return fmt.Sprintf("set a token for the %s role", skip.Detail)
	case SkipVariable:
		if name, ok := envForVariable[skip.Detail]; ok {
			return fmt.Sprintf("set %s (it supplies $%s)", name, skip.Detail)
		}
		return fmt.Sprintf("run the earlier case that captures $%s", skip.Detail)
	case SkipMockOnly:
		return "nothing: the case asserts data only the mock guarantees"
	}
	return "the case did not declare why it skipped"
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

// TestLabSkipsAreStrictByDefault is what makes a lab run evidence: a case that
// did not run fails the run, and CONFORMANCE_ALLOW_SKIPS=1 is the operator's only
// opt-out. The skips come from a real Run, so the recorded reasons are the ones
// the runner produces: a capability gate, a disallowed mutation, an absent
// variable and a mock-only case.
func TestLabSkipsAreStrictByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-SwarmCracker-Api-Version", "1")
		w.Header().Set("X-Request-Id", "req_test")
		w.Header().Set("Content-Type", MediaType)
		_, _ = w.Write([]byte(`{"kind":"VersionInfo"}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	writeFixture(t, dir, "skips.json", `{
		"schemaVersion": 1,
		"name": "skips",
		"cases": [
			{
				"name": "runs",
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "no attach capability",
				"requiresCapabilities": ["attach"],
				"request": {"method": "GET", "path": "/v1/attach", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "mutating",
				"mutating": true,
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "task scoped",
				"request": {"method": "GET", "path": "/v1/tasks/$task/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			},
			{
				"name": "mock only",
				"mockOnly": true,
				"request": {"method": "GET", "path": "/v1/version", "role": "reader"},
				"expect": {"status": 200, "kind": "VersionInfo"}
			}
		]
	}`)

	// The gated run: no attach capability, mutation off, no task id.
	gated := Run(t, Env{
		BaseURL:      server.URL,
		Client:       server.Client(),
		Tokens:       map[string]string{"reader": "test-token"},
		Capabilities: []string{"read"},
		Lab:          true,
	}, dir)

	t.Setenv(envAllowSkips, "")
	err := labSkipFailure(gated, allowSkips())
	if err == nil {
		t.Fatal("a skip under the strict default must fail the lab run")
	}
	for _, want := range []string{
		"/no_attach_capability",
		"/mutating",
		"/task_scoped",
		"a manager that advertises attach",
		envAllowMutation + "=1",
		envTaskID,
		envAllowSkips + "=1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure must name %q, got:\n%s", want, err)
		}
	}
	if strings.Contains(err.Error(), "mock only") {
		t.Fatalf("a mock-only skip is not a failure:\n%s", err)
	}

	// The same run with the documented opt-out is green.
	t.Setenv(envAllowSkips, "1")
	if err := labSkipFailure(gated, allowSkips()); err != nil {
		t.Fatalf("%s=1 must accept the same skips, got %v", envAllowSkips, err)
	}

	// A fully configured run skips nothing but the mock-only case, and that one
	// must not fail the strict policy: otherwise no lab run could ever be green.
	configured := Run(t, Env{
		BaseURL:       server.URL,
		Client:        server.Client(),
		Tokens:        map[string]string{"reader": "test-token"},
		Vars:          map[string]string{"task": "task-a"},
		Capabilities:  []string{"read", "attach"},
		Lab:           true,
		AllowMutation: true,
	}, dir)

	t.Setenv(envAllowSkips, "")
	if err := labSkipFailure(configured, allowSkips()); err != nil {
		t.Fatalf("a configured run must pass the strict policy, got %v", err)
	}
}
