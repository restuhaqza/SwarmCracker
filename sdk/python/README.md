# SwarmCracker Python SDK

A thin, standard-library-only client for the SwarmCracker API v1, plus the
models generated from [`api/openapi.yaml`](../../api/openapi.yaml).

- **Python 3.11+**, no third-party dependency (only `urllib`, `json`, `ssl`,
  `base64` and `typing`).
- **No business logic.** The client is a transport wrapper: no caching, no
  retries, no request parallelism, no hidden state.
- **Not thread safe.** It is safe to reuse one client for sequential requests.

## Install

There is no published package yet. Point `PYTHONPATH` at `sdk/python`, or vendor
the `swarmcracker` directory into your project:

```bash
PYTHONPATH=sdk/python python3 -c 'import swarmcracker; print(swarmcracker.__version__)'
```

## Quick start

```python
from swarmcracker import ApiError, Client

with Client("https://mgr.example.com:4244", token_file="/etc/swarmcracker/api-token") as client:
    print(client.version()["serverVersion"])
    print([service["name"] for service in client.services.list()["items"]])
```

`host` must be an `https://` URL. Plain `http://` is accepted only for
`localhost`, `127.0.0.1` and `[::1]`, which is what the tests use. Pass at most
one of `token` and `token_file` (the file is read once and trimmed; `~` is not
expanded); with neither, no `Authorization` header is sent (that is the trusted
Unix socket's model, and the socket is not reachable over TCP). `ca` selects a CA
bundle; without it the system trust store is used.

## Surface

```python
client.version() -> VersionInfo

client.services.list(*, label=None, limit=None, cursor=None) -> ServiceList
client.services.get(ref) -> ServiceDocument
client.services.create(body, *, idempotency_key=None) -> ServiceDocument
client.services.patch(ref, patch, *, if_match, wait=None) -> ServiceDocument
client.services.delete(ref, *, if_match=None) -> None

client.tasks.list(*, label=None, limit=None, cursor=None, health=None) -> TaskList
client.tasks.get(ref, *, health=None) -> TaskDocument
client.tasks.exec(ref, command, *, env=None, workdir=None) -> ExecStream
client.tasks.logs(ref, *, follow=False, tail=None, since=None) -> LogStream

client.events.watch(*, types=None, since=None) -> EventStream
```

`label` accepts one selector or a sequence, which is sent as repeated `label`
parameters. `health=True` is sent as `health=true`.

The returned values are the generated `TypedDict` models from
`swarmcracker.types`, so they are plain dictionaries at runtime and type
checkers see their fields.

## Streaming

`exec` and `logs` are NDJSON streams; `events` is server-sent events. Every
stream is an iterator and a context manager.

```python
with client.tasks.exec("5f3a1b2c9d", ["sh", "-c", "echo hi >&2"], env={"A": "1"}) as run:
    for frame in run:
        if frame.type in ("stdout", "stderr"):
            print(frame.type, frame.data)   # bytes, already base64-decoded
    print("exit", run.exit_code)             # raises ApiError if no exit frame arrived
```

Each frame keeps the wire keys (`frame["type"]`, `frame["data"]`, or
`frame.type` / `frame.data` for convenience). `data` is always `bytes` and is
`b""` on frames without a payload. An `error` frame raises `ApiError` with that
frame's code; a stream that ends without an exit frame makes `run.exit_code`
raise `ApiError` with code `unreachable`. A frame that breaks the contract (no
`type`, an unknown exec frame type, or `data` that is not a base64 string)
raises `ApiError` with code `internal`.

```python
for frame in client.tasks.logs("5f3a1b2c9d", follow=True, tail=100):
    if frame.type == "log":
        print(frame.time, frame.data.decode(errors="replace"))

for event in client.events.watch(types=["service", "task"], since="42"):
    print(event["type"], event["resource"])
```

`events.watch` sends `Last-Event-ID: <since>` when `since` is given, skips the
`ready` frame, stops at `event: end` and raises `ApiError` for an `event: error`
frame. Breaking out of any stream closes the response.

`timeout` is the socket timeout of each read, not a deadline for the whole
stream. The events endpoint sends a heartbeat comment every 15 s, so a long-lived
watch needs a `timeout` above that gap.

## Errors

Every failure is an `ApiError`:

```python
try:
    client.services.get("missing")
except ApiError as error:
    print(error.code, error.status, error.request_id, error.retryable, error.details)
```

| Field | Meaning |
|-------|---------|
| `code` | The v1 error code (`invalid`, `not_found`, `forbidden`, …), `"unreachable"` for a transport failure, `"internal"` for an undecodable response |
| `message` | The server's message |
| `status` | The HTTP status; `0` for a transport failure |
| `request_id` | The `X-Request-Id` (also repeated by the error document) |
| `retryable` | Whether the server says a retry can succeed |
| `details` | Typed failure context, or `None` |

The bearer token is never included in an error or a message.

### Retries

The client does not retry automatically — a deliberate divergence from contract
§13.5. Retry idempotent calls yourself (`GET`, and a `POST` carrying an
`Idempotency-Key`) on `429` or `5xx`, and honour `Retry-After` when the response
sets it. Never retry a stream or a `POST` without an idempotency key.

## Wire details

Every request carries `Accept: application/vnd.swarmcracker.v1+json` (or the
stream media type) and `X-SwarmCracker-Api-Version: 1`. Writes carry
`Content-Type: application/vnd.swarmcracker.v1+json`, except
`services.patch`, whose body is an RFC 7386 merge patch and therefore uses
`application/merge-patch+json` — the only media type the PATCH route accepts.
`If-Match` is a keyword-only argument on `patch` and on `delete`; `patch`
requires it and `delete` sends it only when you pass one (the API rejects a stale
value, not an absent one). `idempotency_key` is optional on `create`.

Redirects are never followed: a `3xx` surfaces as `ApiError` with code
`internal`, so the bearer token cannot be replayed to another origin.

## Generated models

`sdk/python/swarmcracker/types.py` and `sdk/node/src/types.d.ts` are generated by
`tools/sdkgen` from `api/openapi.yaml` and committed. Do not edit them by hand:

```bash
make sdk-gen       # rewrite both files from the spec
make sdk-check     # fail if either file is stale; never writes
make sdk-test      # run the Python and Node suites
make sdk-test-python  # run only this SDK's suite
```

Because the models are generated with `from __future__ import annotations`,
`TypedDict.__required_keys__` is not computed correctly at runtime (type checkers
read the annotations and are correct); treat the models as static types.

## Not in this round

Snapshot and files routes, retries, pagination helpers and request timeouts per
call. `tasks.list(label=...)` is forwarded as specified but the current manager
does not filter the task list by label. The Node client lives in
[`sdk/node`](../node/README.md) and has the same surface.
