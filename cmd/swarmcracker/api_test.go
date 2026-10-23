package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/apihttp"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// resetAPIFlags clears the remote/token flags and restores them afterwards, so
// a test that sets them cannot leak into the next one through the package vars.
// -o is included because the root parser writes it during Execute.
func resetAPIFlags(t *testing.T) {
	t.Helper()

	host, ca, tokenFile, token, serverName := apiHost, apiCA, apiTokenFile, apiToken, apiTLSServerName
	format := outputFormat
	apiHost, apiCA, apiTokenFile, apiToken, apiTLSServerName = "", "", "", "", ""
	outputFormat = ""
	t.Cleanup(func() {
		apiHost, apiCA, apiTokenFile, apiToken, apiTLSServerName = host, ca, tokenFile, token, serverName
		outputFormat = format
	})
}

// newAPITestServer starts a TLS server and writes its certificate to a PEM CA
// file, returning the endpoint and CA path the CLI flags expect.
func newAPITestServer(t *testing.T, handler http.HandlerFunc) (endpoint, caFile string) {
	t.Helper()

	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NotNil(t, certPEM)

	caFile = filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caFile, certPEM, 0o600))

	return srv.URL, caFile
}

func writeAPITestDocument(w http.ResponseWriter, status int, doc any) {
	w.Header().Set("Content-Type", "application/vnd.swarmcracker.v1+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

// runAPICommand executes the `api` command group with args and returns stdout.
func runAPICommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := newAPICommand()
	cmd.SetArgs(args)

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	return out.String(), err
}

func requireCode(t *testing.T, err error, want output.Code) *output.Error {
	t.Helper()

	var typed *output.Error
	require.Truef(t, errors.As(err, &typed), "want *output.Error, got %T: %v", err, err)
	assert.Equal(t, want, typed.Code)
	return typed
}

func TestRootFlagsPopulateContractConfig(t *testing.T) {
	resetAPIFlags(t)

	root := newRootCommand()
	require.NoError(t, root.PersistentFlags().Parse([]string{
		"-H", "https://mgr:4244",
		"--ca", "/etc/swarmcracker/api/ca.pem",
		"--token-file", "/etc/swarmcracker/api/token",
		"--token", "sc_explicit",
		"--tls-server-name", "mgr.internal",
	}))

	cfg := contractClientConfig()
	assert.Equal(t, "https://mgr:4244", cfg.Endpoint)
	assert.Equal(t, "/etc/swarmcracker/api/ca.pem", cfg.CAFile)
	assert.Equal(t, "/etc/swarmcracker/api/token", cfg.TokenFile)
	assert.Equal(t, "sc_explicit", cfg.Token)
	assert.Equal(t, "mgr.internal", cfg.ServerName)
}

func TestContractConfigWithoutHostStaysLocal(t *testing.T) {
	resetAPIFlags(t)
	t.Setenv("SWARM_SOCKET", "/run/custom/swarm.sock")

	// --token-file alone is the token-store path for `api token`; it must not
	// turn a contract read into a remote read.
	apiTokenFile = "/etc/swarmcracker/api/tokens.json"

	cfg := contractClientConfig()
	assert.Empty(t, cfg.Endpoint)
	assert.Empty(t, cfg.CAFile)
	assert.Empty(t, cfg.TokenFile)
	assert.Empty(t, cfg.Token)
	assert.Empty(t, cfg.ServerName)
	assert.Equal(t, "/run/custom/swarm.sock", cfg.SocketPath)
}

func TestContractClientRequiresCAWhenHostSet(t *testing.T) {
	resetAPIFlags(t)
	apiHost = "https://mgr:4244"

	_, err := newContractClient()
	require.Error(t, err)
	requireCode(t, err, output.CodeUsage)
}

func TestLegacyServiceListJSONWithHostIsUsage(t *testing.T) {
	resetAPIFlags(t)
	apiHost = "https://mgr:4244"

	err := listServices("json", "", false)
	require.Error(t, err)
	typed := requireCode(t, err, output.CodeUsage)
	assert.Contains(t, typed.Message, "-o json")
}

func TestLegacyLocalDialWithHostIsUsage(t *testing.T) {
	resetAPIFlags(t)
	apiHost = "https://mgr:4244"

	_, _, err := getSwarmClientForService()
	require.Error(t, err)
	requireCode(t, err, output.CodeUsage)
}

// The global --token is the API bearer token, but `cluster join` already owns a
// local --token. The local flag must keep winning on that command.
func TestJoinTokenFlagStaysLocal(t *testing.T) {
	resetAPIFlags(t)

	join, _, err := newRootCommand().Find([]string{"cluster", "join"})
	require.NoError(t, err)
	require.NoError(t, join.ParseFlags([]string{"--token", "SWMTKN-1-join"}))

	flag := join.Flags().Lookup("token")
	require.NotNil(t, flag)
	assert.Equal(t, "Join token from manager (required)", flag.Usage)
	assert.Equal(t, "SWMTKN-1-join", flag.Value.String())
	assert.Empty(t, apiToken, "the join token must not land in the API bearer token")
}

func TestRemoteServiceListTable(t *testing.T) {
	resetAPIFlags(t)

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("sc_testtoken\n"), 0o600))

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/services", r.URL.Path)
		assert.Equal(t, "Bearer sc_testtoken", r.Header.Get("Authorization"))
		writeAPITestDocument(w, http.StatusOK, v1.NewServiceList([]v1.Service{
			{
				ID:   "svc-0123456789abcdef",
				Name: "web",
				Spec: v1.ServiceSpec{
					Mode:     "replicated",
					Replicas: ptrInt64(3),
					Task: v1.TaskSpec{
						Image: "nginx:alpine",
						Ports: []v1.Port{{Target: 80, Published: 8080, Protocol: "tcp"}},
					},
				},
			},
			{
				ID:   "svc-89abcdef01234567",
				Name: "db",
				Spec: v1.ServiceSpec{Mode: "global", Task: v1.TaskSpec{Image: "postgres:16"}},
			},
		}))
	})

	apiHost, apiCA, apiTokenFile = endpoint, caFile, tokenFile

	c, err := newContractClient()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var buf bytes.Buffer
	require.NoError(t, listServicesTableWithClient(context.Background(), c, &buf, "", false))

	want := strings.Join([]string{
		"ID                   NAME                 REPLICAS   PORTS                    IMAGE",
		strings.Repeat("-", 100),
		"svc-01234567         web                  3          8080:80/tcp              nginx:alpine",
		"svc-89abcdef         db                   global     -                        postgres:16",
		"",
		"Total: 2 service(s)",
		"",
	}, "\n")
	assert.Equal(t, want, buf.String())
}

// A remote `service ls --filter id=<prefix>` must match the local semantics:
// the contract filter is a prefix match, not a substring match.
func TestRemoteServiceListTableFilterByIDPrefix(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusOK, v1.NewServiceList([]v1.Service{
			{ID: "svc-0123456789abcdef", Name: "web", Spec: v1.ServiceSpec{Mode: "global"}},
			{ID: "svc-89abcdef01234567", Name: "db", Spec: v1.ServiceSpec{Mode: "global"}},
		}))
	})
	apiHost, apiCA = endpoint, caFile

	c, err := newContractClient()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var buf bytes.Buffer
	require.NoError(t, listServicesTableWithClient(context.Background(), c, &buf, "id=svc-0123", false))

	out := buf.String()
	assert.Contains(t, out, "svc-01234567")
	assert.NotContains(t, out, "svc-89abcdef")
	assert.Contains(t, out, "Total: 1 service(s)")
}

func TestFilterV1ServicesMatchesLocalSemantics(t *testing.T) {
	services := []v1.Service{
		{ID: "svc-0123456789abcdef", Name: "web"},
		{ID: "svc-89abcdef01234567", Name: "db"},
	}

	assert.Len(t, filterV1Services(services, "name=web"), 1)
	assert.Empty(t, filterV1Services(services, "name=we"), "name= is exact")
	assert.Len(t, filterV1Services(services, "id=svc-0123"), 1)
	assert.Empty(t, filterV1Services(services, "id=0123"), "id= is a prefix, not a substring")
	assert.Len(t, filterV1Services(services, "web"), 1, "a bare filter stays a substring match")
}

func TestServiceListTableRoutesToHost(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusOK, v1.NewServiceList([]v1.Service{
			{ID: "svc-0123456789abcdef", Name: "web", Spec: v1.ServiceSpec{Mode: "global"}},
		}))
	})
	apiHost, apiCA = endpoint, caFile

	var listErr error
	out := captureStdout(t, func() { listErr = listServices("table", "", false) })
	require.NoError(t, listErr)
	assert.Contains(t, out, "svc-01234567")
	assert.Contains(t, out, "web")
	assert.Contains(t, out, "Total: 1 service(s)")
}

// --host on a command that only knows the local control socket must be a usage
// error, not a silent read of the local cluster.
func TestLocalOnlyCommandsWithHostAreUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"cluster token", []string{"-H", "https://mgr:4244", "cluster", "token"}},
		{"vm list", []string{"-H", "https://mgr:4244", "vm", "ls"}},
		{"service create", []string{"-H", "https://mgr:4244", "service", "create", "--name", "web", "--image", "nginx"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resetAPIFlags(t)
			// Point the local socket at nothing: a guard failure must not be
			// masked by an unrelated dial error.
			t.Setenv("SWARM_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))

			root := newRootCommand()
			root.SetArgs(tc.args)
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			typed := requireCode(t, err, output.CodeUsage)
			assert.Contains(t, typed.Message, "--host")
		})
	}
}

// The guard must not block the commands that do understand --host.
func TestRemoteCapableCommandWithHostStillWorks(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusOK, v1.NewServiceList([]v1.Service{
			{ID: "svc-0123456789abcdef", Name: "web", Spec: v1.ServiceSpec{Mode: "global"}},
		}))
	})

	root := newRootCommand()
	root.SetArgs([]string{"-H", endpoint, "--ca", caFile, "service", "ls"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	var execErr error
	out := captureStdout(t, func() { execErr = root.Execute() })
	require.NoError(t, execErr)
	assert.Contains(t, out, "svc-01234567")
	assert.Contains(t, out, "Total: 1 service(s)")
}

func TestAPITokenListRejectsJSONOutput(t *testing.T) {
	resetAPIFlags(t)

	apiTokenFile = filepath.Join(t.TempDir(), "tokens.json")
	outputFormat = "json"

	_, err := runAPICommand(t, "token", "list")
	typed := requireCode(t, err, output.CodeUsage)
	assert.Contains(t, typed.Message, "table output")
}

// `api token` edits the local store, so --host must not block it.
func TestAPITokenCreateWithHostStillWorks(t *testing.T) {
	resetAPIFlags(t)

	store := filepath.Join(t.TempDir(), "tokens.json")

	root := newRootCommand()
	root.SetArgs([]string{"-H", "https://mgr:4244", "--token-file", store, "api", "token", "create", "--name", "ci"})
	root.SetErr(io.Discard)

	var execErr error
	out := captureStdout(t, func() { execErr = root.Execute() })
	require.NoError(t, execErr)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(out), "sc_"), out)

	reopened, err := apihttp.OpenTokenStore(store)
	require.NoError(t, err)
	require.Len(t, reopened.List(), 1)
}

func TestVersionWithHostStillWorks(t *testing.T) {
	resetAPIFlags(t)

	root := newRootCommand()
	root.SetArgs([]string{"-H", "https://mgr:4244", "version"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	var execErr error
	out := captureStdout(t, func() { execErr = root.Execute() })
	require.NoError(t, execErr)
	assert.Contains(t, out, Version)
}

func TestFormatV1ServiceReplicas(t *testing.T) {
	assert.Equal(t, "3", formatV1ServiceReplicas(v1.Service{
		Spec: v1.ServiceSpec{Mode: "replicated", Replicas: ptrInt64(3)},
	}))
	assert.Equal(t, "global", formatV1ServiceReplicas(v1.Service{
		Spec: v1.ServiceSpec{Mode: "global"},
	}))
	// A replicated service whose count the server omitted must not be reported
	// as global.
	assert.Equal(t, "0", formatV1ServiceReplicas(v1.Service{
		Spec: v1.ServiceSpec{Mode: "replicated"},
	}))
}

func TestRemoteServiceListTableEmpty(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusOK, v1.NewServiceList(nil))
	})
	apiHost, apiCA = endpoint, caFile

	c, err := newContractClient()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var buf bytes.Buffer
	require.NoError(t, listServicesTableWithClient(context.Background(), c, &buf, "", false))
	assert.Equal(t, "No services found\n", buf.String())
}

func TestRemoteServiceListTableKeepsTypedErrorCode(t *testing.T) {
	resetAPIFlags(t)

	endpoint, caFile := newAPITestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeAPITestDocument(w, http.StatusUnauthorized, v1.NewErrorDocument(v1.Error{
			Code:    string(output.CodeUnauthenticated),
			Message: "bad token",
		}))
	})
	apiHost, apiCA = endpoint, caFile

	c, err := newContractClient()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	err = listServicesTableWithClient(context.Background(), c, &bytes.Buffer{}, "", false)
	require.Error(t, err)
	requireCode(t, err, output.CodeUnauthenticated)
}

func TestAPITokenCreatePrintsSecretAndAuthenticates(t *testing.T) {
	resetAPIFlags(t)

	storePath := filepath.Join(t.TempDir(), "tokens.json")
	apiTokenFile = storePath

	out, err := runAPICommand(t, "token", "create", "--name", "ci", "--role", "reader")
	require.NoError(t, err)

	secret := strings.TrimSuffix(out, "\n")
	require.Equal(t, secret+"\n", out, "create must print only the secret and one newline")
	assert.True(t, strings.HasPrefix(secret, "sc_"), out)

	info, err := os.Stat(storePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// The printed secret is the only copy of the plaintext: the file holds its
	// hash, and the hash must not appear on stdout.
	raw, err := os.ReadFile(storePath)
	require.NoError(t, err)
	hash := hashFromTokenFile(t, raw)
	assert.NotContains(t, out, hash)

	store, err := apihttp.OpenTokenStore(storePath)
	require.NoError(t, err)

	tok, ok := store.Authenticate(secret)
	require.True(t, ok, "the printed secret must authenticate")
	assert.Equal(t, "ci", tok.Name)
	assert.Equal(t, apihttp.RoleReader, tok.Role)
	assert.NotEmpty(t, tok.ID)
}

func TestAPITokenListOmitsHashes(t *testing.T) {
	resetAPIFlags(t)

	storePath := filepath.Join(t.TempDir(), "tokens.json")
	apiTokenFile = storePath

	store, err := apihttp.OpenTokenStore(storePath)
	require.NoError(t, err)
	secret, created, err := store.Create("ci", apihttp.RoleReader)
	require.NoError(t, err)
	_, admin, err := store.Create("ops", apihttp.RoleAdmin)
	require.NoError(t, err)

	out, err := runAPICommand(t, "token", "list")
	require.NoError(t, err)

	assert.Contains(t, out, "ID")
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "ROLE")
	assert.Contains(t, out, "CREATED")
	assert.Contains(t, out, created.ID)
	assert.Contains(t, out, admin.ID)
	assert.Contains(t, out, "ci")
	assert.Contains(t, out, "ops")
	assert.Contains(t, out, apihttp.RoleAdmin)

	raw, err := os.ReadFile(storePath)
	require.NoError(t, err)
	for _, hash := range hashesFromTokenFile(t, raw) {
		assert.NotContains(t, out, hash)
	}
	assert.NotContains(t, out, secret)
}

func TestAPITokenRevoke(t *testing.T) {
	resetAPIFlags(t)

	storePath := filepath.Join(t.TempDir(), "tokens.json")
	apiTokenFile = storePath

	store, err := apihttp.OpenTokenStore(storePath)
	require.NoError(t, err)
	secret, tok, err := store.Create("ci", apihttp.RoleReader)
	require.NoError(t, err)
	_, keep, err := store.Create("ops", apihttp.RoleReader)
	require.NoError(t, err)

	out, err := runAPICommand(t, "token", "revoke", tok.ID)
	require.NoError(t, err)
	assert.Equal(t, "Revoked "+tok.ID+"\n", out)

	reopened, err := apihttp.OpenTokenStore(storePath)
	require.NoError(t, err)
	_, ok := reopened.Authenticate(secret)
	assert.False(t, ok, "a revoked secret must not authenticate")
	assert.Len(t, reopened.List(), 1)
	assert.Equal(t, keep.ID, reopened.List()[0].ID)
}

func TestAPITokenRevokeUnknownIDIsNotFound(t *testing.T) {
	resetAPIFlags(t)

	storePath := filepath.Join(t.TempDir(), "tokens.json")
	apiTokenFile = storePath

	_, err := runAPICommand(t, "token", "revoke", "tok_deadbeef")
	require.Error(t, err)
	requireCode(t, err, output.CodeNotFound)
}

func TestAPITokenCreateRejectsInvalidInput(t *testing.T) {
	resetAPIFlags(t)

	apiTokenFile = filepath.Join(t.TempDir(), "tokens.json")

	tests := []struct {
		name string
		args []string
	}{
		{"empty name", []string{"token", "create", "--name", "  ", "--role", "reader"}},
		{"bad role", []string{"token", "create", "--name", "ci", "--role", "writer"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runAPICommand(t, tc.args...)
			require.Error(t, err)
			requireCode(t, err, output.CodeInvalid)
		})
	}
}

func TestAPITokenUsesStateDirWhenNoTokenFile(t *testing.T) {
	resetAPIFlags(t)

	stateDir := t.TempDir()
	t.Setenv("SWARM_STATE_DIR", stateDir)

	_, err := runAPICommand(t, "token", "create", "--name", "ci")
	require.NoError(t, err)

	store, err := apihttp.OpenTokenStore(filepath.Join(stateDir, "api", "tokens.json"))
	require.NoError(t, err)
	require.Len(t, store.List(), 1)
	assert.Equal(t, "ci", store.List()[0].Name)
}

func TestAPITokenFileOverridesStateDir(t *testing.T) {
	resetAPIFlags(t)

	stateDir := t.TempDir()
	t.Setenv("SWARM_STATE_DIR", stateDir)

	override := filepath.Join(t.TempDir(), "custom-tokens.json")
	apiTokenFile = override

	_, err := runAPICommand(t, "token", "create", "--name", "ci")
	require.NoError(t, err)

	store, err := apihttp.OpenTokenStore(override)
	require.NoError(t, err)
	require.Len(t, store.List(), 1)

	_, err = os.Stat(filepath.Join(stateDir, "api", "tokens.json"))
	assert.True(t, errors.Is(err, os.ErrNotExist), "the state-dir store must stay untouched")
}

// hashFromTokenFile reads the single hash out of a token file for assertions.
func hashFromTokenFile(t *testing.T, raw []byte) string {
	t.Helper()
	hashes := hashesFromTokenFile(t, raw)
	require.Len(t, hashes, 1)
	return hashes[0]
}

func hashesFromTokenFile(t *testing.T, raw []byte) []string {
	t.Helper()

	var doc struct {
		Tokens []struct {
			Hash string `json:"hash"`
		} `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	hashes := make([]string, 0, len(doc.Tokens))
	for _, tok := range doc.Tokens {
		require.NotEmpty(t, tok.Hash, "the store must persist a hash")
		hashes = append(hashes, tok.Hash)
	}
	return hashes
}
