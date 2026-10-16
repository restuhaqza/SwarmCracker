# Plan — #44: Registry authentication UX (`--with-registry-auth`)

Branch: `feat/registry-auth` · Issue: [#44](https://github.com/restuhaqza/SwarmCracker/issues/44)
Program: `docs/plans/backlog-program.md` (sequential backlog, subagent-driven)

## Current state (measured)

- `RegistryAuth{Username,Password,Token,Keychain}` and `buildRemoteOptions` live in
  `pkg/image/auth.go:15,28`; the precedence is Keychain → Token → Basic →
  `authn.DefaultKeychain`.
- `ImagePreparer.config.RegistryAuth` (`pkg/image/preparer.go:47`, yaml
  `registry_auth`) is the **only** source of pull credentials, read at
  `preparer.go:254` and `:449` (`PullImage(ctx, ref, ip.config.RegistryAuth)`). It is
  preparer-global, never per-task, and nothing populates it from the config file
  (`cmd/swarmcracker/helpers.go:85`, `pkg/swarmkit/executor.go:191`).
- SwarmKit **already carries** the credential:
  `api.ContainerSpec.PullOptions.RegistryAuth`
  (`swarmkit/v2/api/specs.pb.go:826,916`). The repo has **0 references** to
  `PullOptions`, so nothing sets it and the agent never sees it.
- `types.Container` (`pkg/types/task.go:92`) has no auth field; `convertTask`
  (`pkg/swarmkit/executor.go:1396`) copies Image/Command/Args/Env/Hostname/DiskSize
  only. `ImagePreparer.Prepare(ctx, *Task)` is at `pkg/types/task.go:280`.
- CLI: flags `cmd/swarmcracker/cmd_service.go:291-338`, option struct `:240-289`,
  pure builder `buildServiceSpec(o serviceCreateOptions)`
  (`cmd/swarmcracker/cmd_service_spec.go:91`), container literal `:159-168`,
  `createService` `:1207`, `service update` `:1261`.
- Redaction risk: the whole `api.Service` / `api.Task` is marshaled to JSON at
  `cmd/swarmcracker/cmd_service.go:748` (service ls) and `:992-994` (service
  inspect), and `cmd/swarmcracker/cmd_task.go:220` (task inspect). Once
  `PullOptions` is set those would print the credential.
- `authn.DefaultKeychain` already reads `~/.docker/config.json` and credential
  helpers, so "supply credentials via the standard Docker config" needs no new
  code; a `login` command is deferred (the issue says "and/or").

## Design

Manager resolves, worker consumes:

1. `service create --with-registry-auth` → the CLI resolves the local credentials
   for the image's registry and writes the base64 `X-Registry-Auth` payload into
   `ContainerSpec.PullOptions.RegistryAuth`.
2. The agent copies that field into the internal task; the image preparer prefers
   it over the preparer-global config.
3. The credential never reaches logs, `service inspect`, `task inspect`, `service ls`
   JSON, or metrics.

### Interfaces (exact, binding)

`pkg/image` (new `registryauth.go`):

```go
// ErrNoRegistryCredentials reports that no credential could be found for the
// registry serving an image. Callers wrap it with the registry host so the CLI
// can print an actionable message.
var ErrNoRegistryCredentials = errors.New("no registry credentials found")

// EncodeRegistryAuth resolves credentials for ref from the default docker
// config (or credential helper) and returns the base64url X-Registry-Auth
// payload for ContainerSpec.PullOptions.RegistryAuth.
func EncodeRegistryAuth(ref string) (string, error)

// EncodeRegistryAuthFor is EncodeRegistryAuth with an explicit keychain; a nil
// keychain means authn.DefaultKeychain. Test seam.
func EncodeRegistryAuthFor(ref string, kc authn.Keychain) (string, error)

// RegistryHostForRef reports the registry host that serves ref
// ("index.docker.io" for a bare name).
func RegistryHostForRef(ref string) (string, error)

// DecodeRegistryAuth parses an X-Registry-Auth payload.
func DecodeRegistryAuth(encoded string) (authn.AuthConfig, error)

// RegistryAuthFromConfig wraps a decoded config so buildRemoteOptions uses it as
// its priority-1 keychain, keeping go-containerregistry's own field precedence.
func RegistryAuthFromConfig(cfg authn.AuthConfig) *RegistryAuth
```

`pkg/image/preparer.go`:

```go
// authForTask returns the credentials to use for a task: the task's own
// X-Registry-Auth payload when present, otherwise fallback.
func authForTask(taskRegistryAuth string, fallback *RegistryAuth) (*RegistryAuth, error)
```

`pkg/types/task.go`:

```go
type Container struct {
	// ... existing fields ...
	// RegistryAuth is the base64 X-Registry-Auth payload from the service spec's
	// PullOptions. Empty means the preparer's configured credentials are used.
	RegistryAuth string
}
```

CLI: `serviceCreateOptions.registryAuth string` holds the **already-encoded**
payload, so `buildServiceSpec` stays pure; `createService` does the I/O.

## Global constraints

- Go 1.26. `gofmt -l pkg cmd` must print nothing (`make fmt` uses goimports).
- Errors: `fmt.Errorf("...: %w", err)`; sentinel errors where the CLI must branch
  (`errors.Is`).
- Logging: zerolog via `github.com/rs/zerolog/log`. **Never log a password, token,
  or the encoded payload** — at most the registry host and username.
- No new dependency unless unavoidable; `go mod tidy -diff` must stay clean
  (`ci.yml:42`).
- Tests: table-driven + testify; every new behaviour needs a test; tests must be
  host- and privilege-independent (no reliance on leftover devices or on running as
  root) per `docs-site/.../contributing/testing/unit-tests.md`.
- A flag is never accepted-and-ignored: `--with-registry-auth` either works or
  fails with a clear error **before** the mutating RPC (`service update` may
  first read the service to decide whether it is golden).
- Docs live in `docs-site/src/content/docs/`.
- Tests run on the Linux test server (`root@192.168.18.25`, checkout
  `/root/sc-verify`); this workstation is darwin and `cmd/...` does not build here.
  Canonical check:
  `ssh root@192.168.18.25 'set -e; cd /root/sc-verify && git fetch -q origin && git checkout -q <branch> && git reset -q --hard origin/<branch> && gofmt -l pkg cmd; go vet ./...; go test -count=1 <pkgs>'`

## Task T1 — credential resolution and encoding (`pkg/image`)

Goal: resolve credentials for an image reference from the standard docker config
and encode/decode the `X-Registry-Auth` payload, with an actionable error when
none exist.

Files: `pkg/image/registryauth.go` (new), `pkg/image/registryauth_test.go` (new),
`pkg/image/auth.go` (only if a shared helper belongs there).

Interfaces: exactly the `pkg/image` block above.

Steps:
1. `RegistryHostForRef` — parse `ref` with `github.com/google/go-containerregistry/pkg/name`
   (`name.ParseReference`), return `r.Context().RegistryStr()`. Bare names must
   yield `index.docker.io`. Invalid refs return a wrapped error.
2. `EncodeRegistryAuthFor(ref, kc)` — resolve via the keychain for the repository
   resource, call `Authenticator.Authorization()`, and return
   `base64.URLEncoding.EncodeToString(json.Marshal(cfg))`. When the resolved config
   carries no `Username`, `Password`, `IdentityToken` and `RegistryToken`, return
   an error wrapping `ErrNoRegistryCredentials` that names the registry host and
   says what to do (`docker login <host>`). A keychain resolution error is also
   wrapped with the host.
3. `EncodeRegistryAuth(ref)` = `EncodeRegistryAuthFor(ref, authn.DefaultKeychain)`.
4. `DecodeRegistryAuth(encoded)` — base64-decode (accept standard as well as URL
   encoding, since some clients emit both) and `json.Unmarshal` into
   `authn.AuthConfig`; a malformed payload is a wrapped error.
5. `RegistryAuthFromConfig(cfg)` — return `&RegistryAuth{Keychain: staticKeychain{cfg}}`
   where `staticKeychain.Resolve` returns `authn.FromConfig(cfg)`. Keep it unexported.
6. Do not add logging of credential material anywhere.

Verification:
`ssh root@192.168.18.25 '... go test -count=1 ./pkg/image/...'` and locally
`gofmt -l pkg`. Tests must cover: round-trip encode→decode preserves
username/password; a temp `DOCKER_CONFIG` `config.json` with an `auths` entry is
read (proves the standard Docker config path); no credentials for a registry →
`errors.Is(err, ErrNoRegistryCredentials)` and the message names the host;
malformed base64 and malformed JSON → error; bare name → `index.docker.io`;
`RegistryAuthFromConfig` produces a `RegistryAuth` whose keychain authenticates
(the decoded config is actually used by `buildRemoteOptions`).

Depends on: —

## Task T2 — thread the credential to the agent and the preparer

Goal: the per-task credential from the service spec reaches the image pull, and the
preparer prefers it over its global config.

Files: `pkg/types/task.go`, `pkg/swarmkit/executor.go`, `pkg/image/preparer.go`,
`pkg/executor/executor.go` (only if it builds the internal task from the API spec),
plus the matching `_test.go` files.

Interfaces: `Container.RegistryAuth string`; `authForTask` as above; `Prepare`
signature unchanged.

Steps:
1. Add `RegistryAuth string` to `types.Container` with the doc comment above.
2. In `convertTask` (`pkg/swarmkit/executor.go:1396`) copy
   `containerSpec.Container.PullOptions.RegistryAuth` when `PullOptions != nil`.
3. Find how the **other** executor (`pkg/executor/executor.go`) obtains its task:
   if it builds a `types.Task` from a SwarmKit `api.Task`, copy the same field
   there. If it always receives an already-converted task, say so in the report.
4. In `pkg/image/preparer.go`, add `authForTask` and use it in place of
   `ip.config.RegistryAuth` at both call sites (`:254` and `:449`), so a task-level
   payload wins. An undecodable payload must fail the prepare with a wrapped error,
   not fall back silently.
5. Keep `PullImage`/`ExtractImageToDir` signatures as they are.

Verification: `go test -count=1 ./pkg/image/... ./pkg/types/... ./pkg/swarmkit/...`
on the server (note: `pkg/swarmkit` has pre-existing root-only failures unrelated to
this change — compare the failing set with `main` rather than expecting green).
Tests must cover: `convertTask` copies `PullOptions.RegistryAuth` (and tolerates a
nil `PullOptions`); `authForTask` returns the task auth when set, the fallback when
empty, and an error for a malformed payload; the preparer test double
(`test/mocks/mocks.go:150`) still sees the task.

Depends on: T1.

## Task T3 — `service create --with-registry-auth`

Goal: the flag resolves the credential and puts it on the spec; a missing
credential is a clear pre-RPC error.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd/swarmcracker/cmd_service_spec.go`,
their `_test.go` files.

Steps:
1. Add `--with-registry-auth` (bool, default false) to `service create`
   (`cmd/swarmcracker/cmd_service.go:291-338`) and to `serviceCreateOptions`
   (`:240-289`).
2. Add `registryAuth string` to `serviceCreateOptions` and have `buildServiceSpec`
   set `spec.Task.GetContainer().PullOptions = &api.ContainerSpec_PullOptions{RegistryAuth: o.registryAuth}`
   when it is non-empty. It stays pure (no I/O).
3. In `createService` (`:1207`), when the flag is set, call
   `image.EncodeRegistryAuth(o.image)` **before** building/sending the spec and
   before any RPC; on failure return a wrapped, actionable error (name the registry
   host, mention `docker login <host>`), and make it visible to the CLI user.
4. Do not log the payload; log at most the registry host at debug level.

Verification: `go test -count=1 ./cmd/swarmcracker/...` on the server — the package
already has pure-spec tests (`cmd_service_spec_test.go:23,87`) and this package
builds only on Linux. Tests must cover: `buildServiceSpec` sets `PullOptions` when
`registryAuth` is set and leaves it nil otherwise; the create path returns an
`ErrNoRegistryCredentials`-wrapped error and sends no request when resolution fails
(use the existing fake client pattern); the flag is registered on both
`service create` and its help output.

Depends on: T1, T2.

## Task T4 — `service update` set/clear/preserve semantics

Goal: `service update` must not silently drop an existing credential, and must be
able to set or clear it explicitly.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd/swarmcracker/cmd_service_spec.go`,
their `_test.go` files.

Steps:
1. Read how `service update` builds the new spec today (`cmd_service.go:377-520`
   and the apply path around `:1436-1567`) and whether it starts from the current
   service spec.
2. Add `--with-registry-auth` to `service update` and honour
   `anyFlagChanged(cmd, "with-registry-auth")`: flag given and true → resolve and
   set; flag given as false → clear `PullOptions`; flag absent → **preserve** the
   credential already on the service.
3. Extract the decision into a pure, unit-testable helper so the three cases are
   tested directly.

Verification: `go test -count=1 ./cmd/swarmcracker/...`; tests for the three cases
above, including that an update without the flag keeps the current payload.

Depends on: T3.

## Task T5 — redaction in every serialized output

Goal: no credential can reach stdout, JSON, logs or metrics.

Files: `cmd/swarmcracker/cmd_service.go`, `cmd/swarmcracker/cmd_task.go`, their
`_test.go` files (put the helper where its callers are, or in a small shared file in
`cmd/swarmcracker`).

Steps:
1. Add a redaction helper that clears
   `PullOptions.RegistryAuth` on the spec of a service/task **copy** (do not mutate
   the response object used for other fields).
2. Apply it before `json.Marshal`/`MarshalIndent` at
   `cmd_service.go:748` (service ls) and `:992-994` (service inspect), and
   `cmd_task.go:220` (task inspect). Also check `service ps`/`task ls` text output
   cannot carry it (it cannot today, but assert it).
3. Replace the payload with a stable placeholder (e.g. `"***"`) rather than an empty
   string, so an operator can see that credentials are attached.

Verification: `go test -count=1 ./cmd/swarmcracker/...`; tests build a service/task
whose spec carries a recognisable secret and assert it appears in **no** output of
the inspect/ls JSON paths, while the placeholder does. Include a test that
`service inspect` still round-trips the rest of the spec.

Depends on: T3.

## Task T6 — documentation

Goal: the flag, the credential sources and the redaction are documented.

Files: `docs-site/src/content/docs/reference/cli.md`,
`docs-site/src/content/docs/guides/configuration.md`, and
`docs-site/src/content/docs/guides/operations.md` if a pull-failure runbook belongs
there.

Steps:
1. `reference/cli.md`: `--with-registry-auth` in the `service create` and
   `service update` flag tables, and a short section on what it does and what it
   errors with.
2. `guides/configuration.md`: where credentials come from — `~/.docker/config.json`
   and credential helpers on the **manager** (resolved at create/update time), and
   precedence. **Refuted during T6 (2026-10-17):** the `images.registry_auth` config
   field assumed by the original brief does not exist — `pkg/config`'s
   `ImagesConfig` has no such field and `cmd/swarmcracker/helpers.go:85` never sets
   `image.PreparerConfig.RegistryAuth`, so a `registry_auth` key in the config file
   is silently ignored. The shipped docs (configuration.md, operations.md) state
   this and point at the Docker config instead.
3. State explicitly that credentials are redacted in `service inspect`,
   `task inspect` and JSON output, and that the worker does not need a local Docker
   config.
4. Verify with the docs pipeline (`astro check` + link check) as documented in
   `docs-site/README.md`.

Verification: docs check passes; `grep` shows the flag documented in both tables.

Depends on: T3.

## Verification (program-level, run by the controller)

- Unit tests for the touched packages on the test server, as root **and** as an
  unprivileged user (the suite must not depend on privileges — project rule).
- `verify.sh feat/registry-auth main` on the test server: head-only failures empty.
- CI (test + lint + build), including the coverage gate.
- **Lab e2e on the 2-node nested-KVM cluster**: stand up a registry with basic auth,
  push an image, then `service create --with-registry-auth` and prove the worker
  pulls it with **no** local Docker config; plus the negative case (no stored
  credential → clear error, no service created).
- Independent review of the whole branch before merge.

## Acceptance mapping (from #44)

| Criterion | Task |
|---|---|
| `service create --image ... --with-registry-auth` succeeds on a worker using stored credentials | T1–T3 + lab e2e |
| Credentials via `login` and/or standard Docker `config.json` | T1 (config.json/credential helpers); `login` deferred, recorded in the plan |
| Never in logs / `service inspect` / metrics (with a test) | T5 |
| Missing/invalid credential → clear, actionable error | T1 (sentinel) + T3 (pre-RPC error) + lab negative case |
| Unit tests for auth resolution + documented multi-node e2e | T1–T5 + lab e2e |
| Docs updated (`reference/cli.md`, `guides/configuration.md`) | T6 |

## Out of scope (recorded)

- `swarmcracker login` (writing `~/.docker/config.json`). The issue accepts
  "and/or the standard Docker config.json"; reading it is implemented. Revisit if a
  stored-credential flow is wanted.
- Registry credentials in `service inspect --pretty` variants beyond the JSON paths
  listed; they share the same spec and are covered by T5's helper if present.
