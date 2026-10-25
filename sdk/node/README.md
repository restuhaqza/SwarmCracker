# SwarmCracker Node SDK

A thin, dependency-free client for the SwarmCracker API v1, plus the models
generated from [`api/openapi.yaml`](../../api/openapi.yaml).

- **Node 18+**, no dependency at all (only `node:https`, `node:http` and
  `node:fs`).
- **ESM only.** `sdk/node/package.json` sets `"type": "module"`.
- **No business logic.** The client is a transport wrapper: no caching, no
  retries, no request parallelism, no hidden state.
- **Not concurrency safe.** It is safe to reuse one client for sequential
  requests.

## Install

There is no published package yet. Copy `sdk/node` into your project, or import
it by path:

```js
import { Client } from './sdk/node/src/index.mjs';
```

## Quick start

```js
import { ApiError, Client } from './sdk/node/src/index.mjs';

const client = new Client({
  host: 'https://mgr.example.com:4244',
  tokenFile: '/etc/swarmcracker/api-token',
});

try {
  const info = await client.version();
  console.log(info.serverVersion);

  const page = await client.services.list();
  console.log(page.items.map((service) => service.name));
} finally {
  client.close();
}
```

`host` must be an `https://` URL. Plain `http://` is accepted only for
`localhost`, `127.0.0.1` and `[::1]`, which is what the tests use. Pass at most
one of `token` and `tokenFile` (the file is read once and trimmed; `~` is not
expanded); with neither, no `Authorization` header is sent (that is the trusted
Unix socket's model, and the socket is not reachable over TCP). `ca` is either a
PEM string or the path to a PEM file and selects a CA bundle; without it the
system trust store is used. TLS verification is never disabled.

## Surface

```js
client.version() -> Promise<VersionInfo>

client.services.list({ label, limit, cursor }) -> Promise<ServiceList>
client.services.get(ref) -> Promise<ServiceDocument>
client.services.create(body, { idempotencyKey }) -> Promise<ServiceDocument>
client.services.patch(ref, patch, { ifMatch, wait }) -> Promise<ServiceDocument>
client.services.delete(ref, { ifMatch }) -> Promise<null>

client.tasks.list({ label, limit, cursor, health }) -> Promise<TaskList>
client.tasks.get(ref, { health }) -> Promise<TaskDocument>
client.tasks.exec(ref, command, { env, workdir, signal }) -> ExecStream
client.tasks.logs(ref, { follow, tail, since, signal }) -> LogStream

client.events.watch({ types, since, signal }) -> EventStream
```

`label` accepts one selector or an array, which is sent as repeated `label`
parameters. `health: true` is sent as `health=true`. `ifMatch` is required by
`patch` and `delete` and is sent as `If-Match`; `idempotencyKey` is optional on
`create`.

The returned values are the generated interfaces from `types.d.ts`, so an editor
completes their fields.

## Streaming

`exec` and `logs` are NDJSON streams; `events` is server-sent events. Every
stream is an async iterable with a `close()` method.

```js
const run = client.tasks.exec('5f3a1b2c9d', ['sh', '-c', 'echo hi >&2'], { env: { A: '1' } });
for await (const frame of run) {
  if (frame.type === 'stdout' || frame.type === 'stderr') {
    // frame.data is a Uint8Array, already base64-decoded
    process.stdout.write(frame.data);
  }
}
console.log('exit', run.exitCode); // throws ApiError when no exit frame arrived
```

Each frame keeps the wire keys (`frame.type`, `frame.data`, plus `time` and
`stream` on log frames). `data` is always a `Uint8Array` and is empty on frames
without a payload. An `error` frame rejects with that frame's `code`; a stream
that ends without an exit frame makes `run.exitCode` throw `ApiError` with code
`unreachable`. A frame that breaks the contract (no `type`, an unknown exec frame
type, or `data` that is not a base64 string) rejects with `ApiError` code
`internal`.

```js
for await (const frame of client.tasks.logs('5f3a1b2c9d', { follow: true, tail: 100 })) {
  if (frame.type === 'log') {
    console.log(frame.time, Buffer.from(frame.data).toString('utf8'));
  }
}

for await (const event of client.events.watch({ types: ['service', 'task'], since: '42' })) {
  console.log(event.type, event.resource);
}
```

`events.watch` sends `Last-Event-ID: <since>` when `since` is given, skips the
`ready` frame, stops at `event: end` and rejects with `ApiError` for an
`event: error` frame. Breaking out of any stream (or calling `close()`) releases
the connection.

## Timeouts and cancellation

`timeoutMs` (default `30000`) is the deadline of one buffered request, covering
connect and the whole body. For `exec` and `logs` it is only a *connect*
deadline: it is cleared as soon as the response headers arrive, so a stream is
never cut while it keeps producing output. `events.watch` has no total deadline.

Pass `{ signal }` (any `AbortSignal`) to `exec`, `logs` or `events.watch` to stop
a stream; iteration then ends. A buffered request that outlives `timeoutMs`
rejects with `ApiError` code `unreachable`.

## Errors

Every failure is an `ApiError`:

```js
try {
  await client.services.get('missing');
} catch (error) {
  if (error instanceof ApiError) {
    console.log(error.code, error.status, error.requestId, error.retryable, error.details);
  }
}
```

| Field | Meaning |
|-------|---------|
| `code` | The v1 error code (`invalid`, `not_found`, `forbidden`, ...), `"unreachable"` for a transport failure, `"internal"` for an undecodable response |
| `message` | The server's message |
| `status` | The HTTP status; `0` for a transport failure |
| `requestId` | The `X-Request-Id` (also repeated by the error document) |
| `retryable` | Whether the server says a retry can succeed |
| `details` | Typed failure context, or `null` |

A configuration mistake (an unreadable token or CA file) throws `ApiError` with
code `usage` and status `0`; an invalid constructor argument throws `TypeError`.
The bearer token is never included in an error or a message.

## Wire details

Every request carries `Accept: application/vnd.swarmcracker.v1+json` (or the
stream media type) and `X-SwarmCracker-Api-Version: 1`. Writes carry
`Content-Type: application/vnd.swarmcracker.v1+json`, except `services.patch`,
whose body is an RFC 7386 merge patch and therefore uses
`application/merge-patch+json` -- the only media type the PATCH route accepts.

Redirects are never followed: a `3xx` surfaces as `ApiError` with code
`internal`, so the bearer token cannot be replayed to another origin.

## Generated models

`sdk/node/src/types.d.ts` and `sdk/python/swarmcracker/types.py` are generated by
`tools/sdkgen` from `api/openapi.yaml` and committed. Do not edit them by hand:

```bash
make sdk-gen             # rewrite both files from the spec
make sdk-check           # fail if either file is stale; never writes
make sdk-test            # run the Python and Node suites
make sdk-test-node       # run only this SDK's suite
```

`src/client.d.ts` is hand-written and imports the generated names. The stream
declarations assume TypeScript lib `es2018` or later (for `AsyncIterable`).

## Not in this round

The files and snapshot routes are not exposed yet (the Python SDK omits them for
the same reason). Also out of scope: retries, pagination helpers, request
timeouts per call, a `.ts` source build, and a published npm package.
`tasks.list({ label })` is forwarded as specified but the current manager does
not filter the task list by label.
