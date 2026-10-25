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

## Python

`sdk/python` contains the `swarmcracker` package: Python 3.11+, standard library
only, one `Client` with `services`, `tasks` and `events` attributes. A manager
with a wired node broker also advertises `exec`, which is what the optional task
section uses.

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
import sys

from swarmcracker import ApiError, Client


def main() -> int:
    host = os.environ.get("SWARMCRACKER_HOST")
    if not host:
        print("error=usage", file=sys.stderr)
        print("SWARMCRACKER_HOST is not set", file=sys.stderr)
        return 1

    try:
        with Client(
            host,
            ca=os.environ.get("SWARMCRACKER_CA") or None,
            token_file=os.environ.get("SWARMCRACKER_TOKEN_FILE") or None,
        ) as client:
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
        print(f"error={error.code}", file=sys.stderr)
        return 1
    except ValueError:
        # A host the client refuses (plain http to a non-loopback address, both
        # a token and a token_file, ...) is a usage error, not a server failure.
        print("error=usage", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
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

  const client = new Client({
    host,
    ca: process.env.SWARMCRACKER_CA || undefined,
    tokenFile: process.env.SWARMCRACKER_TOKEN_FILE || undefined,
  });

  try {
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
    client.close();
  }

  return 0;
}

process.exitCode = await main();
```

## Run them in CI

`make examples` runs `test/examples/run.sh`, which:

1. builds `test/examples/mockserver`, a loopback-only fixture that speaks the v1
   envelope, headers and NDJSON exec frames and performs no authentication;
2. runs both programs with `SWARMCRACKER_HOST` pointed at it, then again with
   `SWARMCRACKER_TASK=t1`, and compares the exact output lines.

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

- **File transfer** — `GET`/`PUT /v1/tasks/{id}/files` use raw
  `application/octet-stream`, not the JSON envelope, so neither SDK wraps it.
- **Snapshots** — the snapshot routes are experimental and have no SDK surface;
  use the CLI (`swarmcracker vm snapshot`).
- **Browser clients** — the Node client needs a CA from the filesystem and a
  trusted token store, so it is a server-side client. A browser build would need
  a different trust story; the repository does not ship one.
