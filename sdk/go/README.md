# SwarmCracker Go SDK

The Go SDK is [`pkg/client`](../../pkg/client). There is no second transport and
no wrapper module: importing the module's own package keeps one dial, TLS and
SwarmKit-mapping implementation for the CLI and for Go programs.

- **Go 1.26+**, the module's existing packages only (no new dependency).
- **One client for both transports**: the manager's local Unix control socket and
  the remote HTTPS API.
- **No business logic.** The client is a transport wrapper: no caching, no
  retries. It is safe for concurrent use.

## Install

```bash
go get github.com/restuhaqza/swarmcracker/pkg/client
```

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/restuhaqza/swarmcracker/pkg/client"
)

func main() {
	c, err := client.New(client.Config{
		Endpoint:  "https://mgr:4244",
		CAFile:    "/etc/swarmcracker/api-ca.pem",
		TokenFile: "/etc/swarmcracker/api-token",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	services, err := c.ListServices(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	for _, service := range services {
		fmt.Println(service.Name)
	}
}
```

`Endpoint` is the manager's HTTPS base URL. It is required for TCP: plain `http`
is refused, and `CAFile` is required with it because there is no unverified
fallback. `TokenFile` holds a bearer token and is optional; its content is
trimmed, and `Config.Token` wins over it. Leaving both empty sends no
`Authorization` header, which is what the trusted Unix socket expects. Without
`Endpoint` the client dials the local control socket (`Config.SocketPath`,
`Config.StateDir`, `Config.CertDir`).

`Config.Timeout` bounds one request; zero selects the package default (10 s
locally, 30 s for a remote endpoint).

## Surface

Reads:

| Method | Route |
|--------|-------|
| `ListServices(ctx)` | `GET /v1/services` |
| `GetService(ctx, ref)` | `GET /v1/services/{ref}` |
| `ListTasks(ctx)` | `GET /v1/tasks` |
| `ListServiceTasks(ctx, ref)` | `GET /v1/services/{ref}/tasks` |
| `GetTask(ctx, id)` | `GET /v1/tasks/{id}` |
| `ListNodes(ctx)` / `GetNode(ctx, ref)` | `GET /v1/nodes`, `GET /v1/nodes/{ref}` |
| `ListNetworks(ctx)` / `GetNetwork(ctx, ref)` | `GET /v1/networks`, `GET /v1/networks/{ref}` |
| `ListConfigs(ctx)` / `GetConfig(ctx, ref)` | `GET /v1/configs`, `GET /v1/configs/{ref}` |
| `ListSecrets(ctx)` / `GetSecret(ctx, ref)` | `GET /v1/secrets`, `GET /v1/secrets/{ref}` |
| `ClusterID(ctx)` | `GET /v1/version` |

Writes (the third argument is `v1.WriteOptions{IfMatch: revision}`):

| Method | Route |
|--------|-------|
| `CreateService(ctx, v1.ServiceWrite, opts)` | `POST /v1/services` |
| `UpdateService(ctx, ref, patch, opts)` | `PATCH /v1/services/{ref}` |
| `RollbackService(ctx, ref, opts)` | `POST /v1/services/{ref}/rollback` |
| `DeleteService(ctx, ref, opts)` | `DELETE /v1/services/{ref}` |
| `CreateConfig` / `UpdateConfig` / `DeleteConfig` | `/v1/configs` |
| `CreateSecret` / `UpdateSecret` / `DeleteSecret` | `/v1/secrets` |
| `CreateNetwork` / `DeleteNetwork` / `UpdateNode` | `/v1/networks`, `/v1/nodes` |

`UpdateService` takes an RFC 7386 merge-patch document as `[]byte`:

```go
service, err := c.UpdateService(ctx, services[0].ID,
	[]byte(`{"spec":{"replicas":3}}`),
	v1.WriteOptions{IfMatch: services[0].Revision},
)
```

## Exec and events

`ExecTask` streams one non-interactive command and reports its exit status. A
non-zero guest exit is a successful call; only a missing exit frame is a failure.

```go
var stdout, stderr bytes.Buffer
result, err := c.ExecTask(ctx, taskID,
	v1.ExecRequest{Command: []string{"sh", "-c", "echo hi >&2"}},
	&stdout, &stderr,
)
fmt.Println(result.ExitCode, stderr.String())
```

`WatchEvents` streams events until the context is cancelled or the server closes
the stream:

```go
events, errs, err := c.WatchEvents(ctx, v1.EventOptions{Types: []string{"service"}})
if err != nil {
	return err
}
for event := range events {
	fmt.Println(event.Cursor, event.Type)
}
return <-errs
```

Exec over the remote API requires `Endpoint`; without it the call returns a
`usage` error, because the local transport reaches the guest agent another way.

## Errors

Over the remote HTTPS transport every failure is `*output.Error`:

```go
var failure *output.Error
if errors.As(err, &failure) {
	switch failure.Code {
	case output.CodeNotFound:
		// ...
	case output.CodeUnreachable, output.CodeTimeout:
		// retry later
	}
}
```

`failure.Code` is the stable contract; the message text is not. The bearer token
never appears in an error. A read on the local Unix socket can surface the
manager's gRPC status instead, so keep a `status.Code(err)` fallback when the
same code has to work with both transports.

## Models

The returned documents are the DTOs in
[`pkg/api/v1`](../../pkg/api/v1) -- the same documents `swarmcracker ... -o json`
prints and the same shapes `api/openapi.yaml` defines. The generated Python and
TypeScript models are produced from that spec, so all three SDKs agree field for
field.

## Tests

`go test ./sdk/go` compiles the examples and runs the ones with deterministic
output. It makes no network call.

```bash
go test ./sdk/go
gofmt -l sdk/go
```

## Not in this round

Snapshot and file helpers, retries, and an exposed cursor. The remote transport
follows the list `next` cursor itself, so a list call returns every page rather
than one page per call.
