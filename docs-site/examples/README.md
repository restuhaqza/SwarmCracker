# Runnable SDK examples

Two programs, one per shipped client, that read a manager over HTTP API v1:

| File | Client |
|------|--------|
| `python/example.py` | `sdk/python` (package `swarmcracker`) |
| `node/example.mjs` | `sdk/node` (ESM, no dependencies) |

They are the source of the snippets in
[`guides/sdk-examples.md`](../src/content/docs/guides/sdk-examples.md), and the
snippets are byte-identical except for the import: the files here import the
checkout (`sys.path` / a relative path), while the page shows the
installed-package form (`from swarmcracker import ...` / `from 'swarmcracker'`).
Keep them in step when changing either.

## Run them

From the repository root:

```bash
make examples
```

`test/examples/run.sh` builds `test/examples/mockserver`, a loopback-only fixture
with a fixed dataset and no authentication, runs both programs against it, and
compares the exact output lines — first on the read path, then with
`SWARMCRACKER_TASK=t1` on the exec path. Two more passes pin the contracts a
reader relies on: the fixture restarted with `-exit-code 3` proves a failed guest
command still prints `exec.exit=3` and the program still exits 0, and an
unreadable `SWARMCRACKER_CA` plus a non-loopback `http://` host prove the
constructor's failures print one `error=usage` line and no traceback. It needs
`go`, `python3` and `node`; no cluster, root, Docker or network.

To run one program by hand against anything that speaks v1:

```bash
export SWARMCRACKER_HOST=https://mgr:4244        # required, https or loopback http
export SWARMCRACKER_CA=/path/to/tls.crt          # optional CA bundle
export SWARMCRACKER_TOKEN_FILE=/path/to/token    # optional bearer token
export SWARMCRACKER_TASK=task-id                 # optional: also run `true`

PYTHONPATH=sdk/python python3 docs-site/examples/python/example.py
node docs-site/examples/node/example.mjs
```

Output is stable `key=value` lines on stdout: `version`, `capabilities`
(sorted), `services`, one `service` per item, and with a task `exec.exit` and
`exec.stdout`. A typed failure prints `error=<code>` on stderr and exits 1. The
token, the CA contents and the request headers are never printed.
