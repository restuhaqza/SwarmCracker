package conformance

// This file owns the one discovery request both legs make: GET /v1/version.
// Capability gating needs the target's own answer, not a guess, and the lab run
// records the daemon build it tested. The probe uses a real token and the real
// media type, so it is the same contract every other case exercises.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// versionInfo is the subset of the version document the legs use.
type versionInfo struct {
	Kind                string   `json:"kind"`
	SchemaVersion       int      `json:"schemaVersion"`
	APIVersions         []string `json:"apiVersions"`
	ServerVersion       string   `json:"serverVersion"`
	GRPCProtocolVersion string   `json:"grpcProtocolVersion"`
	Capabilities        []string `json:"capabilities"`
}

// fetchVersion reads GET /v1/version with one token. It fails the test rather
// than returning a partial answer: a run that cannot read the capability set
// would silently skip or run every gated case for the wrong reason.
func fetchVersion(t *testing.T, client *http.Client, baseURL, token string) versionInfo {
	t.Helper()

	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/version", nil)
	if err != nil {
		t.Fatalf("build the version probe: %v", err)
	}
	req.Header.Set("Accept", MediaType)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/version: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/version: want 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(apiVersionHeader); got != APIVersion {
		t.Fatalf("GET /v1/version: %s must be %q, got %q", apiVersionHeader, APIVersion, got)
	}

	var info versionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode the version document: %v", err)
	}
	if info.Kind != "VersionInfo" {
		t.Fatalf("GET /v1/version: want kind VersionInfo, got %q", info.Kind)
	}
	if info.Capabilities == nil {
		// An absent list is a version document that cannot gate anything; naming
		// it is better than gating on an empty set.
		t.Fatalf("GET /v1/version: capabilities must be present, got %s", describeCapabilities(info))
	}
	return info
}

// describeCapabilities renders the capability set for a log line. It never
// includes a token: the document has no credential in it.
func describeCapabilities(info versionInfo) string {
	return fmt.Sprintf("server %q, api %v, capabilities %v",
		info.ServerVersion, info.APIVersions, info.Capabilities)
}
