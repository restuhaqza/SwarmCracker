# API conformance suite

One declarative fixture set, executed against two very different environments:

| Run | Target | When |
|-----|--------|------|
| **mock** | the real `apihttp` handler, an in-memory backend and a real token store on loopback TLS | every pull request (`make conformance`) |
| **lab** | a real manager over HTTPS | manually and on a weekly schedule (`make conformance-lab`) |

The fixtures are plain JSON, so the same files can later drive the Go, Python and
Node SDKs. Nothing here asserts an implementation detail: every case describes
the public v1 HTTP contract.

```bash
make conformance                          # mock leg, no cluster or root needed
go test ./test/conformance -count=1       # both legs; the lab one skips

CONFORMANCE_HOST=https://manager:8443 \
CONFORMANCE_CA=/path/to/ca.pem \
CONFORMANCE_READER_TOKEN=... \
make conformance-lab                      # lab leg
```

## Fixture format (`schemaVersion: 1`)

Every `fixtures/*.json` file is loaded and validated at startup. A malformed
file, an unknown schema version or an unknown JSON key is fatal: a suite that
silently runs fewer cases is worse than one that fails.

```json
{
  "schemaVersion": 1,
  "name": "read",
  "cases": [
    {
      "name": "list services page one",
      "mutating": false,
      "mockOnly": false,
      "request": {
        "method": "GET",
        "path": "/v1/services",
        "query": {"limit": "1"},
        "role": "reader",
        "headers": {"If-None-Match": "\"stale\""},
        "body": null,
        "contentType": "application/vnd.swarmcracker.v1+json",
        "accept": "application/vnd.swarmcracker.v1+json"
      },
      "expect": {
        "status": 200,
        "kind": "ServiceList",
        "items": 1,
        "nextPresent": true,
        "requestIdInBody": false,
        "errorCode": "",
        "headers": {"X-SwarmCracker-Api-Version": "1"},
        "jsonPaths": {"items.0.name": "web"},
        "absentKeys": ["items.0.spec.task.registryAuth"],
        "absentKeysAnywhere": ["registryAuth"]
      },
      "follow": {
        "path": "/v1/services",
        "query": {"cursor": "$next"},
        "expect": {"status": 200, "items": 1, "nextPresent": false}
      },
      "stream": {"frames": 2, "lastType": "exit"}
    }
  ]
}
```

The case above shows every block in one place. In practice a case is a document
expectation plus at most one of `follow` or `repeat`, or a `stream` case with
frames.

### Rules

- `role` is `reader`, `runner` or `admin`. The runner supplies the token; a
  fixture never contains one. A role with no token configured skips only the
  cases that need it (with a log line, never a failure).
- `expect.kind` checks the document `kind`; `expect.errorCode` checks
  `error.code`. Exactly one must be set, except for a documentless status
  (`204`/`304`) and for a `stream` case, which asserts frames instead.
- `jsonPaths`, `absentKeys` and `absentKeysAnywhere` use dot paths with numeric
  array indexes (`items.0.name`). Values are compared as strings; numbers keep
  their exact JSON text.
  - A `*` segment matches any array element, so `items.*.action` means "at least
    one item has this action", without pinning its position in the page.
  - `absentKeys` is a path; it passes when the parent object is absent.
    `absentKeysAnywhere` is a key name scanned at every depth, which is what a
    payload field needs: the DTO never emits `data` on a secret, so a dotted path
    would pass for the wrong reason. Use it for secrets, config values and
    `registryAuth`.
- Every response, success or error, must carry
  `X-SwarmCracker-Api-Version: 1` and a non-empty `X-Request-Id`. A JSON
  response must carry the v1 media type. An error document must echo the request
  ID in `error.requestId`.
- `expect.headers` are exact-match assertions.
- `request.body` is a JSON value. `null` or an absent body sends no body. A body
  is sent as the v1 media type unless `request.contentType` overrides it (a
  PATCH uses `application/merge-patch+json`).
- `request.accept` defaults to the v1 media type.
- A fixture may not set `Authorization`, `Cookie`, `Proxy-Authorization`,
  `Connection`, `Transfer-Encoding` or `Upgrade`: the runner owns the credential
  and the transport owns framing. The loader rejects them.
- `$name` references: in a header or a query value only a whole-value reference
  is substituted (so a literal `"stale"` is never mistaken for a variable); in a
  **path** every reference is substituted, because a task id is one segment
  (`/v1/tasks/$task/exec`).
  - `$next` — the `next` cursor of the response being followed (in `follow`).
  - `$etag` — the `ETag` of the previous case **in the same file**. It is how a
    create is followed by a patch and a delete without hard-coding a revision.
    Cases in one file run in order.
  - `$task` — a task id the environment supplies (`task-a` in the mock,
    `CONFORMANCE_TASK_ID` in the lab). Without it, task-scoped stream cases skip.
- `follow` walks exactly one next cursor. It requires `expect.nextPresent: true`:
  a follow without a cursor is a fixture bug, and the loader rejects it.
- `repeat: {times: N, sameBody: true}` sends the same request `N` times and
  asserts every response has the primary one's status and body. With an
  `Idempotency-Key` that is the proof of a replay: a second create would return a
  new resource with a new revision. `times` must be at least 2.
- `stream` reads an SSE or NDJSON response and returns as soon as it has seen
  `stream.frames` data frames (or the terminal `lastType`, or the 2 s deadline),
  then closes the body. It counts data frames, ignoring the `ready` and `ping`
  control frames. `lastType` names the frame that must end the stream: `exec`
  ends with `exit`, a non-following log stream with `eof`.
- Non-stream requests have a 10 s timeout, so a dead manager fails pointedly
  instead of hanging until the test binary's own timeout.

### Skip policy

- `mutating: true` cases are skipped in the lab run unless
  `CONFORMANCE_ALLOW_MUTATION=1`. They always run in the mock run, which throws
  its backend away.
- `mockOnly: true` cases assert data only the mock guarantees (names, pagination,
  a 207 health partial, an audit entry for a write the mock just ran). The lab
  run skips them. **Lab fixtures must never assume cluster contents**: a read
  that needs an existing object asserts `200` with `kind` and only `absentKeys`;
  name-specific `jsonPaths` belong in a `mockOnly` case.
- A task-scoped stream case (`$task`) skips in the lab when
  `CONFORMANCE_TASK_ID` is not set: the cluster, not the runner, lacks the value.
- **The mock run may skip nothing.** `TestConformanceMock` fails if any case was
  skipped and asserts the exact number of cases in `expectedMockCases`, so a case
  that silently stops running (a lost file, a filter, a widened skip rule) is a
  failure rather than a green run.

## The mock leg

`mock_test.go` wires the real handler, a real `TokenStore` (reader, runner with a
task selector, admin), a real `AuditStore`, an `IdempotencyStore`, high rate
limits, the broker capability and a stateful in-memory backend:

- two services (`web` → `svc-web-1`, `api` → `svc-api-2`) so pagination has a
  second page and `svc-web` is a genuinely unique prefix while `svc-` is
  ambiguous;
- two tasks, one on a reachable node and one on an unreachable one, so
  `?health=true` is a `207` with one partial;
- one node, one network, one config with a value and one secret;
- revisions as decimal strings; writes bump them and honour `If-Match`;
- reference resolution mirrors `pkg/client`: exact ID, exact name, then a unique
  prefix of at least four characters, with `ambiguous` naming its candidates;
- `WatchEvents` emits two events then closes;
- `Proxy` serves a finite NDJSON exec stream and a finite log stream.

The backend is mutex-guarded, so the suite is clean under `-race`.

## The lab leg

`lab_test.go` reads:

| Variable | Meaning |
|----------|---------|
| `CONFORMANCE_HOST` | base URL of the manager's TLS listener (required) |
| `CONFORMANCE_CA` | path to a PEM CA to trust, in addition to the system roots (optional) |
| `CONFORMANCE_READER_TOKEN` | reader token (required) |
| `CONFORMANCE_RUNNER_TOKEN` | runner token; runner cases skip without it (optional) |
| `CONFORMANCE_ADMIN_TOKEN` | admin token; admin cases skip without it (optional) |
| `CONFORMANCE_TASK_ID` | a task id for the exec/logs cases; they skip without it (optional) |
| `CONFORMANCE_ALLOW_MUTATION` | `1` runs the mutating cases; default off |

The stream cases run in both legs. A live manager cannot be pinned to mock data,
so in lab mode the terminal frame is what is asserted (`exit` for exec, `eof` for
logs), extra data frames are allowed, and an event stream that opened and sent
only the `ready` control frame passes: a quiet cluster has no changes to report,
and the mock leg proves the frame content.

`make conformance-lab` skips cleanly (exit 0) when `CONFORMANCE_HOST` or
`CONFORMANCE_READER_TOKEN` is missing. Tokens are never printed; a failure names
the request path, status and a body truncated to 2 KiB, never the
`Authorization` header.

The scheduled workflow is `.github/workflows/conformance-lab.yml`. It writes the
`CONFORMANCE_CA` secret to a temporary file, refuses to run without
`CONFORMANCE_HOST`, and deletes the file afterwards. Set the repository variable
`CONFORMANCE_TASK_ID` to a task that exists on the cluster to run the exec and
logs cases; without it they skip. Operators who prefer to seed a cluster can use
the `mockOnly` case names as a checklist, but the lab run itself does not require
seeded data.

## Adding a case

1. Pick one file: `read`, `errors`, `write` or `streams`.
2. Add the case with the smallest assertion set that still proves the contract.
3. Ask: does this pass against an empty cluster? If not, it needs
   `"mockOnly": true` or must be self-contained (create the object it uses).
4. Bump `expectedMockCases` in `mock_test.go`; the mock run fails on a count
   mismatch by design.
5. `make conformance` and `go test ./test/conformance -count=1`.
