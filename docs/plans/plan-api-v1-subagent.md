# Plan: API v1 remaining phases (subagent-driven)

Spec: `docs/plans/api-contract-v1-proposal.md`,
`docs/plans/api-v1-capability-map.md`.
Mode: subagent-driven development. One implementer per task, then a fresh
reviewer. Controller owns this plan and the ledger.

## Global constraints

These bind every task and are copied into every reviewer dispatch.

- Go 1.26; keep the current module path `github.com/restuhaqza/swarmcracker`.
- `pkg/api/v1` is the wire contract. Fields are camelCase; absent means absent;
  list `items` is never null; enums are lowercase/open.
- Every HTTP response uses `application/vnd.swarmcracker.v1+json`, carries
  `X-SwarmCracker-Api-Version: 1`, and errors are `v1.ErrorDocument`.
- `api.ControlClient` may appear only under `pkg/client` (including
  `pkg/client/legacy`). A guard test enforces this.
- Legacy `--format json` output is frozen; do not change its bytes.
- Remote TCP TLS must verify the server. `InsecureSkipVerify` is allowed only on
  the local Unix-socket gRPC path.
- Secret/config values never appear in a read document. Registry credentials
  never appear in a response.
- List ordering is `createdAt` ascending, then `id` ascending. `limit` default
  100, hard max 1000; over max is `invalid` (HTTP 400).
- Error/exit mapping comes from `pkg/output`; do not invent a second taxonomy.
- Tests must assert behavior, not just exercise code. Run the covering package
  tests before committing. One task = one commit (or a short commit series).
- Do not touch `.impeccable/`, `docs/plans/rfc-programmatic-api.md`, or the
  stashed pre-work.
- Work only on branch `feat/api-v1-subagent`. Do not push or merge.

## Interfaces fixed up front

```go
// pkg/apihttp
type Client interface {
    ListServices(context.Context) ([]v1.Service, error)
    GetService(context.Context, string) (v1.Service, error)
    ListServiceTasks(context.Context, string) ([]v1.Task, error)
    ListTasks(context.Context) ([]v1.Task, error)
    GetTask(context.Context, string) (v1.Task, error)
    ListNodes(context.Context) ([]v1.Node, error)
    GetNode(context.Context, string) (v1.Node, error)
    ListNetworks(context.Context) ([]v1.Network, error)
    GetNetwork(context.Context, string) (v1.Network, error)
    ListConfigs(context.Context) ([]v1.Config, error)
    GetConfig(context.Context, string) (v1.Config, error)
    ListSecrets(context.Context) ([]v1.Secret, error)
    GetSecret(context.Context, string) (v1.Secret, error)
    ClusterID(context.Context) (string, error)
}

type Config struct {
    Client       Client
    Tokens       *TokenStore
    ServerVersion string
    GRPCVersion  string
    ClusterID    string
    Capabilities []string
    TrustUnix    bool
}

func New(cfg Config) (*Server, error)
func (s *Server) Handler() http.Handler
func (s *Server) Serve(ctx context.Context, unixPath, tcpAddr string) error

// TokenStore
type Token struct {
    ID, Name, Role, Hash, CreatedAt string
}
func OpenTokenStore(path string) (*TokenStore, error)
func (s *TokenStore) Create(name, role string) (secret string, tok Token, err error)
func (s *TokenStore) Authenticate(secret string) (Token, bool)
func (s *TokenStore) List() []Token
func (s *TokenStore) Revoke(id string) error

// TLS bootstrap
func EnsureCertificate(dir, hostname string, extraHosts []string) (certFile, keyFile string, err error)

// pkg/client additions
type Config struct {
    // existing fields...
    Endpoint, CAFile, TokenFile, Token, ServerName string
}
```

## Phase 1 tasks

### Task 1 — version document
- Goal: add `v1.VersionInfo`, `/v1/version` schema/path, tests.
- Files: `pkg/api/v1/version.go` (new), `pkg/api/v1/version_test.go` (new),
  `pkg/api/v1/openapi_test.go`, `api/openapi.yaml`.
- Interfaces: `v1.NewVersionInfo(apiVersions []string, serverVersion, grpcVersion, clusterID string, capabilities []string) VersionInfo`.
- Steps: write failing marshal/OpenAPI test; add DTO; add path/schema.
- Verify: `go test ./pkg/api/v1`.
- Depends: none.

### Task 2 — inspect documents and client getters
- Goal: add single-resource documents for network/config/secret and
  `GetTask/GetNode/GetNetwork/GetConfig/GetSecret` on `pkg/client`, with mapping
  tests.
- Files: `pkg/api/v1/network.go`, `config.go`, `secret.go`, their tests,
  `pkg/client/client.go`, `pkg/client/remote.go` (new or stub), mapping tests.
- Interfaces: getters returning `v1.*`; remote backend is Task 3.
- Steps: TDD mapping from fake SwarmKit responses; keep list methods unchanged.
- Verify: `go test ./pkg/api/v1 ./pkg/client`.
- Depends: none.

### Task 3 — remote HTTP transport
- Goal: `client.New` with `Config.Endpoint` uses HTTPS, bearer token, CA
  verification, typed error decoding, cursor following.
- Files: `pkg/client/client.go`, `pkg/client/remote.go` (new),
  `pkg/client/remote_test.go` (new).
- Interfaces: existing `Client` methods transparently route remote; `Close` safe.
- Steps: httptest server; assert headers, 401 mapping, pagination, TLS CA
  requirement; then implement.
- Verify: `go test ./pkg/client`.
- Depends: Task 2.

### Task 4 — token store
- Goal: manager-local token file with hash-only storage and role validation.
- Files: `pkg/apihttp/token.go` (new), `pkg/apihttp/token_test.go` (new).
- Interfaces as fixed above. File mode 0600; atomic write; `reader` and `admin`
  roles; reject empty name/unknown role.
- Verify: `go test ./pkg/apihttp`.
- Depends: none.

### Task 5 — TLS bootstrap certificate
- Goal: persist a self-signed server cert/key with SANs for hostname,
  localhost, 127.0.0.1, ::1 and extra hosts.
- Files: `pkg/apihttp/tls.go` (new), `pkg/apihttp/tls_test.go` (new).
- Interfaces: `EnsureCertificate`; idempotent reload; 0600 key.
- Verify: `go test ./pkg/apihttp`.
- Depends: none.

### Task 6 — read handlers, auth, pagination, ETag
- Goal: serve `/v1/version` and read routes for services, tasks, nodes,
  networks, configs, secrets; bearer auth on TCP; trusted Unix socket; limit,
  cursor, label filter, `If-None-Match`, typed errors.
- Files: `pkg/apihttp/server.go`, `handlers.go`, `pagination.go` (new),
  `server_test.go`, `pagination_test.go` (new).
- Interfaces: `apihttp.Client`, `Config`, `New`, `Handler`.
- Steps: handler tests with fake client first; then implement; include empty
  list `[]`, 401/403, limit=5000 -> 400, 304.
- Verify: `go test ./pkg/apihttp`.
- Depends: Tasks 1, 2, 4.

### Task 7 — daemon wiring and listeners
- Goal: managers start the API on a Unix socket and `--api-listen` (default
  `127.0.0.1:4244`), using the local `pkg/client` and token store; shut down
  with the node.
- Files: `cmd/swarmd-firecracker/api.go` (new), `run.go`, `node.go`,
  `flags.go`, `api_test.go` (new).
- Interfaces: `startAPI(ctx, cfg) (stop func(), err error)`; flags
  `--api-listen`, `--api-socket`, `--api-token-file`, `--api-cert-dir`,
  `--api-disabled`.
- Verify: `GOOS=linux go test ./cmd/swarmd-firecracker`.
- Depends: Tasks 3, 5, 6.

### Task 8 — CLI remote flags and token commands
- Goal: global `-H/--host`, `--ca`, `--token-file`, `--token`,
  `--tls-server-name`; all contract reads use them; `api token
  create/list/revoke`.
- Files: `cmd/swarmcracker/api.go` (new), `main.go`, `cmd_service.go`,
  `cmd_task.go`, `cmd_node.go`, `cmd_config.go`, `cmd_secret.go`,
  `cmd_network_admin.go`, `cmd_api_test.go` (new).
- Interfaces: `newContractClient() (*client.Client, error)`; token commands use
  `apihttp.OpenTokenStore`.
- Verify: `GOOS=linux go test ./cmd/swarmcracker`.
- Depends: Tasks 3, 4, 6.

### Task 9 — phase 1 OpenAPI and docs
- Goal: document every phase 1 route, auth, headers, error responses and the
  `api token` workflow.
- Files: `api/openapi.yaml`, `docs-site/src/content/docs/reference/api.md` (or
  the existing API reference), `pkg/api/v1/openapi_test.go`.
- Verify: `go test ./pkg/api/v1` and `cd docs-site && npm run verify`.
- Depends: Tasks 1, 6, 8.

### Task 10 — phase 1 acceptance
- Goal: an in-process acceptance test that starts the real handler with a fake
  client, asserts remote `-o json` bytes, auth failures, pagination and ETag;
  plus a documented `set -e` script.
- Files: `test/api/phase1_test.go` (new), `test/api/phase1.sh` (new),
  `Makefile` (`api-check` runs it).
- Verify: `make api-check`.
- Depends: Tasks 6, 8.

## Phase 2 tasks

### Task 11 — scopes, roles, selectors, audit
- Goal: token scopes (`services:read`, `services:write`, `configs:read`,
  `configs:read-values`, `tasks:exec`, …), role expansion, task selectors,
  audit records.
- Files: `pkg/apihttp/auth.go`, `audit.go`, tests; token store migration.
- Verify: `go test ./pkg/apihttp`.
- Depends: Task 4.

### Task 12 — service writes
- Goal: POST/PATCH/DELETE services backed by SwarmKit RPCs; overlay preserves
  unmodeled fields; `If-Match` 428/412.
- Files: `pkg/client/write.go`, `pkg/apihttp/services_write.go`, tests.
- Verify: `go test ./pkg/client ./pkg/apihttp`.
- Depends: Tasks 3, 11.

### Task 13 — config/secret/network/node writes
- Goal: config/secret create/labels/delete; network create/delete; node
  availability/labels; immutable fields rejected.
- Files: `pkg/client/write.go`, `pkg/apihttp/named_write.go`,
  `network_write.go`, `node_write.go`, tests.
- Verify: same as Task 12.
- Depends: Task 12.

### Task 14 — idempotency, wait, dry run
- Goal: `Idempotency-Key`, `?wait=true`, `?dryRun=true` with the documented
  document fields.
- Files: `pkg/apihttp/idempotency.go`, `wait.go`, tests.
- Verify: `go test ./pkg/apihttp`.
- Depends: Task 12.

### Task 15 — events
- Goal: `/v1/events` SSE + NDJSON, cursor, `cursor_expired` 410.
- Files: `pkg/apihttp/events.go`, `pkg/client/watch.go`, tests.
- Verify: `go test ./pkg/apihttp ./pkg/client`.
- Depends: Tasks 3, 14.

### Task 16 — rate limits and phase 2 acceptance
- Goal: per-token token bucket, `Retry-After`, acceptance script.
- Files: `pkg/apihttp/ratelimit.go`, `test/api/phase2_test.go`,
  `test/api/phase2.sh`.
- Verify: `make api-check`.
- Depends: Tasks 11–15.

## Phase 3 tasks (XL, split)

### Task 17 — broker channel
- Goal: manager↔node mTLS broker registry and task-keyed routing.
- Files: `pkg/broker/`, `cmd/swarmd-firecracker/broker.go`, tests.
- Verify: `go test ./pkg/broker/...`.
- Depends: Task 11.

### Task 18 — exec
- Goal: `/v1/tasks/{id}/exec` with TTY rules, NDJSON frames, exit/signal
  mapping, `--exit-code=api`.
- Files: `pkg/broker/exec.go`, `pkg/apihttp/exec.go`, CLI wiring, tests.
- Verify: `go test ./pkg/broker/... ./pkg/apihttp`.
- Depends: Task 17.

### Task 19 — logs and files
- Goal: log follow and regular-file read/write through the broker.
- Files: `pkg/broker/logs.go`, `files.go`, `pkg/apihttp/logs.go`, `files.go`.
- Verify: `go test ./pkg/broker/... ./pkg/apihttp`.
- Depends: Task 18.

### Task 20 — health capability
- Goal: `/v1/tasks?health=true`, broker fan-out, `207 partial`.
- Files: `pkg/broker/health.go`, `pkg/apihttp/health.go`, tests.
- Verify: `go test ./pkg/broker/... ./pkg/apihttp`.
- Depends: Task 19.

## Phase 4 tasks

### Task 21 — snapshots
- Goal: experimental snapshot routes behind a capability flag.
- Files: `pkg/apihttp/snapshots.go`, `pkg/client/snapshot.go`, tests.
- Verify: `go test ./pkg/apihttp ./pkg/client`.
- Depends: Task 20.

### Task 22 — generated SDKs
- Goal: generate Go/Python/Node clients from OpenAPI; thin wrappers.
- Files: `sdk/`, `Makefile`, CI.
- Verify: `make sdk-check`.
- Depends: Task 21.

### Task 23 — conformance suite
- Goal: run the same fixtures against an in-process mock and the lab.
- Files: `test/conformance/`, `.github/workflows/`.
- Verify: `make conformance`.
- Depends: Task 22.

### Task 24 — docs examples and CI image
- Goal: executable Python/Node examples, Dockerfile for CI runners.
- Files: `docs-site/`, `Dockerfile.cli`, CI.
- Verify: docs verify + example run.
- Depends: Task 23.

## Execution note

Tasks 1–10 are the immediate phase 1 batch. Tasks 11–24 follow only after the
phase 1 acceptance is green. Every task gets a brief in
`.sdd/api-v1/tasks/`, a report in `.sdd/api-v1/reports/`, and a review package
in `.sdd/api-v1/reviews/`.
