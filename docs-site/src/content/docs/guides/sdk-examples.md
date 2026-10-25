---
title: "SDK examples"
description: "Runnable Python and Node programs against the SwarmCracker API v1, and how CI runs them."
---

Two runnable programs that read a manager over HTTP API v1: one Python, one Node.
They are the executable form of the [API reference](/reference/api/) — if the
wire format changes, these break before the prose does.

Run them against this repository's checkout (the SDKs are not published to a
package index):

```bash
make examples
```

That command needs `go`, `python3` and `node`. It builds a loopback fixture, runs
both programs against it and diffs their output; no cluster, root, Docker or
network is involved. See [Run them in CI](#run-them-in-ci).

The snippets below are the example files with the installed-package import. In
the repository the Python file inserts `sdk/python` on `sys.path` and the Node
file imports `sdk/node/src/index.mjs` by relative path; nothing else differs.

Go callers do not need either package: the CLI and the tests use `pkg/client`,
the Go SDK, which is part of this module. See [Go](#go).

## Python

`sdk/python` contains the `swarmcracker` package: Python 3.11+, standard library
only, one `Client` with `services`, `tasks` and `events` attributes. A manager
with a wired node broker also advertises `exec`, which is what the optional task
section uses. Retries are opt-in (`retries=0` by default) and confined to
idempotent requests; see [Files, snapshots and retries](#files-snapshots-and-retries).

```python
#!/usr/bin/env python3
"""Print the v1 read path of a SwarmCracker manager with the shipped Python SDK.

Environment:

    SWARMCRACKER_HOST         the manager URL (required)
    SWARMCRACKER_CA           path to the CA bundle (optional)
    SWARMCRACKER_TOKEN_FILE   path to a file holding the bearer token (optional)
    SWARMCRACKER_TASK         a task reference; when set, the example also runs
                              `true` in it and prints the exit status and stdout

The output is stable `key=value` lines on stdout, one fact per line, so CI can
diff it. A failed request prints `error=<code>` on stderr and exits 1. The token,
the CA contents and the request headers are never printed.
"""

import os
from sys import stderr

from swarmcracker import ApiError, Client


def main() -> int:
    host = os.environ.get("SWARMCRACKER_HOST")
    if not host:
        print("error=usage", file=stderr)
        print("SWARMCRACKER_HOST is not set", file=stderr)
        return 1

    # The constructor validates the host and reads the CA and token files, so a
    # refused host or an unreadable file is a usage error, not a request failure.
    # It has to be caught here: outside this block it would escape as a traceback.
    try:
        client = Client(
            host,
            ca=os.environ.get("SWARMCRACKER_CA") or None,
            token_file=os.environ.get("SWARMCRACKER_TOKEN_FILE") or None,
        )
    except (ValueError, OSError):
        print("error=usage", file=stderr)
        return 1

    try:
        with client:
            version = client.version()
            print(f"version={version['serverVersion']}")
            print("capabilities=" + ",".join(sorted(version.get("capabilities") or [])))

            services = client.services.list(limit=2)
            print(f"services={len(services['items'])}")
            for service in services["items"]:
                print(f"service={service['name']}")

            task = os.environ.get("SWARMCRACKER_TASK")
            if task:
                stdout = b""
                with client.tasks.exec(task, ["true"]) as stream:
                    for frame in stream:
                        if frame["type"] == "stdout":
                            stdout += frame["data"]
                    exit_code = stream.exit_code
                # A non-zero guest status is still a successful API call: print
                # it and keep going, so the example does not fail on a command.
                print(f"exec.exit={exit_code}")
                print(f"exec.stdout={stdout.decode('utf-8', 'replace').strip()}")
    except ApiError as error:
        print(f"error={error.code}", file=stderr)
        return 1

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
```

## Node

`sdk/node` is an ESM package with no dependencies (Node 18+). It wraps
`node:https` rather than `fetch` so a custom CA is usable without a dispatcher
package. The API is promise-based; the exec, log and event streams are async
iterables.

```javascript
#!/usr/bin/env node
// Print the v1 read path of a SwarmCracker manager with the shipped Node SDK.
//
// Environment:
//
//   SWARMCRACKER_HOST         the manager URL (required)
//   SWARMCRACKER_CA           path to the CA bundle (optional)
//   SWARMCRACKER_TOKEN_FILE   path to a file holding the bearer token (optional)
//   SWARMCRACKER_TASK         a task reference; when set, the example also runs
//                             `true` in it and prints the exit status and stdout
//
// The output is stable `key=value` lines on stdout, one fact per line, so CI can
// diff it. A failed request prints `error=<code>` on stderr and exits 1. The
// token, the CA contents and the request headers are never printed.

import { ApiError, Client } from 'swarmcracker';

async function main() {
  const host = process.env.SWARMCRACKER_HOST;
  if (!host) {
    console.error('error=usage');
    console.error('SWARMCRACKER_HOST is not set');
    return 1;
  }

  let client;
  try {
    // The constructor validates the host and reads the CA and token files, so a
    // refused host or an unreadable file must be caught here too: outside this
    // block it would escape as an unhandled rejection and a stack trace.
    client = new Client({
      host,
      ca: process.env.SWARMCRACKER_CA || undefined,
      tokenFile: process.env.SWARMCRACKER_TOKEN_FILE || undefined,
    });

    const version = await client.version();
    console.log(`version=${version.serverVersion}`);
    console.log(`capabilities=${[...(version.capabilities ?? [])].sort().join(',')}`);

    const services = await client.services.list({ limit: 2 });
    console.log(`services=${services.items.length}`);
    for (const service of services.items) {
      console.log(`service=${service.name}`);
    }

    const task = process.env.SWARMCRACKER_TASK;
    if (task) {
      const chunks = [];
      const stream = client.tasks.exec(task, ['true']);
      for await (const frame of stream) {
        if (frame.type === 'stdout') {
          chunks.push(Buffer.from(frame.data));
        }
      }
      // A non-zero guest status is still a successful API call: print it and
      // keep going, so the example does not fail on a command.
      console.log(`exec.exit=${stream.exitCode}`);
      console.log(`exec.stdout=${Buffer.concat(chunks).toString('utf8').trim()}`);
    }
  } catch (error) {
    if (error instanceof ApiError) {
      console.error(`error=${error.code}`);
      return 1;
    }
    if (error instanceof TypeError) {
      // A host the client refuses (plain http to a non-loopback address, both
      // a token and a tokenFile, ...) is a usage error, not a server failure.
      console.error('error=usage');
      return 1;
    }
    throw error;
  } finally {
    client?.close();
  }

  return 0;
}

process.exitCode = await main();
```

## Files, snapshots and retries

Both clients wrap the raw file routes and the experimental snapshot routes; the
command-line examples above do not exercise them, so these snippets are runnable
but not diffed. `files_get`/`filesGet` returns the file's bytes (not an envelope),
`files_put`/`filesPut` sends the whole body, and `mode` is an octal string. The
snapshot helpers use the flat `/v1/snapshots` routes for list/restore/delete, so
the task travels as a query parameter and is required; create and stop-restored
are task-scoped. Snapshots need `--snapshots-enable` on the node: until then every
route answers `501 not_implemented` with `details.capability: "snapshots"`.

`retries` is the number of *extra* attempts for an idempotent request (`GET`,
`DELETE`, and the files `PUT`, whose whole body is resent) that fails with `429`
or `5xx`. The wait is the response's `Retry-After` when it is usable, otherwise
exponential backoff, capped by `retry_max_wait` (Python) / `retryMaxWaitMs`
(Node). `0` — the default — never retries, so nothing changes unless you ask for
it.

```py
# Python: files and snapshots need a task that is running.
client = Client(host, ca=ca, token_file=token_file, retries=3)

client.tasks.files_put(task, "/tmp/hello", b"hello\n", mode="0644")
print(client.tasks.files_get(task, "/tmp/hello").decode())   # hello

snapshot = client.tasks.snapshots_create(task, vcpus=2, memory_mb=512)
for item in client.tasks.snapshots_list(task)["items"]:
    print(item["id"], item["sizeBytes"])   # sizeBytes = memory + state + disk copy
client.tasks.snapshots_stop_restored(task)
client.tasks.snapshots_delete(task, snapshot["id"])
```

```js
// Node: the same calls, promise-based; retries are off unless set.
const client = new Client({ host, ca, tokenFile, retries: 3 });

await client.tasks.filesPut(task, '/tmp/hello', Buffer.from('hello\n'), { mode: '0644' });
console.log((await client.tasks.filesGet(task, '/tmp/hello')).toString()); // hello

const snapshot = await client.tasks.snapshotsCreate(task, { vcpus: 2, memoryMB: 512 });
for (const item of (await client.tasks.snapshotsList({ task })).items) {
  console.log(item.id, item.sizeBytes); // sizeBytes = memory + state + disk copy
}
await client.tasks.snapshotsStopRestored(task);
await client.tasks.snapshotsDelete(task, snapshot.id);
```

On a `files_get`/`filesGet` body that ends before its declared length both clients
raise a typed transport error (`unreachable`) instead of returning a short file.

## Go

There is no separate Go package to install: `pkg/client` is the Go SDK and lives
in this module, so anything that builds against the repository can use it:

```go
import "github.com/restuhaqza/swarmcracker/pkg/client"

c, err := client.New(client.Config{
	Endpoint:  "https://manager:4244", // empty selects the local Unix socket
	CAFile:    "/path/to/ca.crt",      // required when Endpoint is set
	TokenFile: "/path/to/token",
})
if err != nil {
	return err
}
defer c.Close()
```

`Config` also takes `Token` (which wins over `TokenFile`), `ServerName`,
`Timeout`, `StateDir`/`CertDir` and `SocketPath`; `client.DefaultConfig()`
fills the local defaults from the environment the CLI uses. The methods are
`ListServices`, `GetService`, `CreateService`, `UpdateService`, `DeleteService`,
`RollbackService`, `ListTasks`, `ListTasksHealth`, `GetTask`,
`ListServiceTasks`, `ExecTask`, `AttachTask`, `WatchEvents`,
`ListNodes`/`GetNode`/`UpdateNode`, `ListNetworks`/`GetNetwork`/
`CreateNetwork`/`DeleteNetwork`, `ListConfigs`/`GetConfig`/`CreateConfig`/
`UpdateConfig`/`DeleteConfig`, `ListSecrets`/`GetSecret`/`CreateSecret`/
`UpdateSecret`/`DeleteSecret` and `ClusterID` — every one takes a
`context.Context` and returns the generated `pkg/api/v1` types. Snapshot
create/restore stay in the CLI (`cmd/swarmcracker`), not in `pkg/client`; the
Go client does expose the restored-VM stop as `StopRestoredTask`.

## Run them in CI

`make examples` runs `test/examples/run.sh`, which:

1. builds `test/examples/mockserver`, a loopback-only fixture that speaks the v1
   envelope, headers and NDJSON exec frames and performs no authentication;
2. runs both programs with `SWARMCRACKER_HOST` pointed at it, then again with
   `SWARMCRACKER_TASK=t1`, and compares the exact output lines;
3. reruns the exec path against a fixture started with `-exit-code 3`: a failed
   guest command still prints `exec.exit=3` and the program still exits 0;
4. checks the error contract: an unreadable `SWARMCRACKER_CA` and a non-loopback
   `http://` host each print exactly one `error=usage` line on stderr and exit
   1, with no traceback;
5. diffs the two snippets on this page against the example files, so the page
   cannot drift from the code.

The `examples` job in `.github/workflows/ci.yml` runs that script and then builds
`Dockerfile.cli`, so a broken snippet or a broken client-only image fails the PR.
The fixture tests (`go test ./test/examples/mockserver`) cover the parts the
examples do not exercise, such as the second page of the cursor.

To point the programs at a real manager instead, set `SWARMCRACKER_HOST` to its
HTTPS URL, `SWARMCRACKER_CA` to the CA bundle the manager printed, and
`SWARMCRACKER_TOKEN_FILE` to a file holding a reader or runner token. None of the
three is printed by either program, and the container image in `Dockerfile.cli`
takes the same two files as read-only mounts.

## What the examples do not cover

- **Attach** — `POST /v1/tasks/{id}/attach` is one full-duplex HTTP/1.1 stream
  (stdin in, console out), which neither SDK models: a promise/async-iterator
  client is the wrong shape for a request body that stays open. Use the CLI
  (`swarmcracker -H … vm attach <task>`) or a raw HTTP/1.1 client; see the
  [API reference](/reference/api/#task-exec-attach-logs-files-and-health).
- **Browser clients** — the Node client needs a CA from the filesystem and a
  trusted token store, so it is a server-side client. A browser build would need
  a different trust story; the repository does not ship one.
