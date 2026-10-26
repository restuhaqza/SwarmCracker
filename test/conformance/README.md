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

`CONFORMANCE_HOST` must start with `https://`. The lab run carries bearer tokens,
so a cleartext target is refused with an error instead of being upgraded or
downgraded silently.

## Fixture format (`schemaVersion: 1`)

Every `fixtures/*.json` file is loaded and validated at startup. A malformed
file, an unknown schema version or an unknown JSON key is fatal: a suite that
silently runs fewer cases is worse than one that fails.

```json
{
  "schemaVersion": 1,
  "name": "write",
  "cases": [
    {
      "name": "create a service",
      "mutating": true,
      "mockOnly": false,
      "requiresCapabilities": ["snapshots"],
      "absentCapabilities": ["attach"],
      "request": {
        "method": "POST",
        "path": "/v1/services",
        "query": {"limit": "1"},
        "role": "admin",
        "headers": {"If-None-Match": "\"stale\"", "Idempotency-Key": "case-{{run}}"},
        "body": {"name": "conformance-ws-{{run}}"},
        "contentType": "application/vnd.swarmcracker.v1+json",
        "accept": "application/vnd.swarmcracker.v1+json"
      },
      "expect": {
        "status": 201,
        "kind": "Service",
        "items": 1,
        "nextPresent": true,
        "requestIdInBody": true,
        "requestIdPath": "partial.0.error.requestId",
        "headers": {"X-SwarmCracker-Api-Version": "1"},
        "jsonPaths": {"name": "conformance-ws-{{run}}"},
        "presentKeys": ["id", "createdAt"],
        "minValues": {"sizeBytes": 1},
        "absentKeys": ["items.0.spec.task.registryAuth"],
        "absentKeysAnywhere": ["registryAuth"]
      },
      "capture": {"service": "id"},
      "follow": {
        "path": "/v1/services",
        "query": {"cursor": "$next"},
        "expect": {"status": 200, "items": 1, "nextPresent": false}
      }
    }
  ]
}
```

The case above shows every document block in one place. A `stream` case replaces
the document expectation with `stream: {"frames": 2, "lastType": "exit"}` (a
deterministic framed stream), `stream: {"untilType": "eof", "minFrames": 1,
"maxFrames": 200}` (a framed stream whose length the server chooses) or
`stream: {"bytes": 14}` (a raw console); it carries no `kind`, no
`requestIdInBody` and no `follow`.

### Rules

- `role` is `reader`, `runner` or `admin`. The runner supplies the token; a
  fixture never contains one. A role with no token configured skips only the
  cases that need it; in the lab leg that skip fails the run unless
  `CONFORMANCE_ALLOW_SKIPS=1` is set.
- `expect.kind` checks the document `kind`; `expect.errorCode` checks
  `error.code`. Exactly one must be set, except for a documentless status
  (`204`/`304`) and for a `stream` case, which asserts frames instead.
- Every response, success or error, must carry
  `X-SwarmCracker-Api-Version: 1` and a non-empty `X-Request-Id`. A JSON
  response must carry the v1 media type. An error document must echo the request
  ID in `error.requestId`.
- `jsonPaths`, `absentKeys`, `presentKeys`, `minValues` and `absentKeysAnywhere`
  use dot paths with numeric array indexes (`items.0.name`). Values are compared
  as strings; numbers keep their exact JSON text.
  - A `*` segment matches any array element, so `items.*.action` means "at least
    one item has this action", without pinning its position in the page.
  - `absentKeys` is a path; it passes when the parent object is absent.
    `absentKeysAnywhere` is a key name scanned at every depth, which is what a
    payload field needs: the DTO never emits `data` on a secret, so a dotted path
    would pass for the wrong reason. Use it for secrets, config values and
    `registryAuth`.
  - `presentKeys` is the complement: a path that must exist. It is the only way
    to assert a value the fixture cannot name (a server-generated path, a size).
  - `minValues` asserts a path holds a JSON integer at least the given value. It
    is how a snapshot case says `sizeBytes` is a real total without pinning a
    byte count the node chooses.
- `requestIdInBody: false` asserts the request ID is not echoed at
  `requestIdPath` (default: the top-level `requestId`); `true` asserts it is, and
  that the value is this request's `X-Request-Id`. Every error document echoes it
  in `error.requestId` automatically. No success DTO carries a top-level
  `requestId`, so `true` is only usable with an explicit `requestIdPath`. The
  health partial (`partial.0.error.requestId`) is the one success document that
  carries a nested echo; the case that asserts it is `mockOnly`, so the `true`
  branch runs in the mock leg only.
- `expect.headers` are exact-match assertions.
- `request.body` is a JSON value. `null` or an absent body sends no body. A body
  is sent as the v1 media type unless `request.contentType` overrides it (a
  PATCH uses `application/merge-patch+json`).
- `request.accept` defaults to the v1 media type.
- A fixture may not set `Authorization`, `Cookie`, `Proxy-Authorization`,
  `Connection`, `Transfer-Encoding` or `Upgrade`: the runner owns the credential
  and the transport owns framing. The loader rejects them.
- `{{run}}` in a path, query, header, body, or expectation value is replaced by a
  per-run suffix (UTC timestamp plus random characters, e.g.
  `20261026T101530Z-9f2c1a4b`). Every mutating create name and every mutating
  `Idempotency-Key` **must** carry it: the loader rejects a literal one, so two
  runs — sequential or parallel, in CI or by hand — cannot collide on a name or
  replay a stored key response. The loader also rejects a name two mutating cases
  create.
- `$name` references: in a header or a query value only a whole-value reference
  is substituted (so a literal `"stale"` is never mistaken for a variable); in a
  **path** every reference is substituted, because a task id is one segment
  (`/v1/tasks/$task/exec`).
  - `$next` — the `next` cursor of the response being followed (in `follow`).
  - `$etag` — the `ETag` of the previous case **in the same file**. It is how a
    create is followed by a patch and a delete without hard-coding a revision.
    Cases in one file run in order.
  - `$task` — a task id the environment supplies (`task-a` in the mock,
    `CONFORMANCE_TASK_ID` in the lab). Without it, task-scoped stream and
    snapshot cases skip.
  - `capture: {alias: dot.path}` — store a value from the response as `$alias`
    for later cases in the same file. It is how a create's generated snapshot id
    reaches the `DELETE` that cleans it up. An alias may not shadow `$task`,
    `$etag`, `$next` or `$run`.
- `follow` walks exactly one next cursor. It requires `expect.nextPresent: true`:
  a follow without a cursor is a fixture bug, and the loader rejects it.
- `repeat: {times: N, sameBody: true}` sends the same request `N` times and
  asserts every response has the primary one's status and body. With an
  `Idempotency-Key` that is the proof of a replay: a second create would return a
  new resource with a new revision. `times` must be at least 2.
- `stream: {frames: N, lastType: ...}` reads an SSE or NDJSON response and returns
  as soon as it has seen `frames` data frames (or the terminal `lastType`, or the
  2 s deadline), then closes the body. It counts data frames, ignoring the `ready`
  and `ping` control frames. `lastType` names the frame that must end the stream:
  `exec` ends with `exit`. A pinned count is only for a deterministic stream.
- `stream: {untilType: "eof", minFrames: N, maxFrames: M}` reads to a named
  terminal frame instead of a count. It is the shape for a stream whose length the
  server chooses: a real console log carries as many lines as the guest printed, so
  a pinned count stops before `eof` and fails a correct server (this happened in
  the first lab run). `untilType` itself is the end marker, not payload;
  `minFrames` is how many payload frames must precede it, and `maxFrames` caps the
  read (terminal frame included) so a server that never sends the terminal frame
  fails with the observed frames instead of hanging. Both are required, and
  `maxFrames` must exceed `minFrames`.
- `stream: {bytes: N}` reads a raw `application/octet-stream` body instead — the
  attach console, which has no frames to count.
- `requiresCapabilities` / `absentCapabilities` gate a case on the target's
  `GET /v1/version` capability list. A leg that declares its capabilities (both
  do) skips a case whose requirement is missing, or whose absent capability is
  advertised; the skip is counted as a declared gate, never as a silent loss.
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
- A case gated by `requiresCapabilities` / `absentCapabilities` skips on a target
  whose `/v1/version` disagrees. Both are declared gates: the mock leg allows them
  while still failing on any other skip.
- **A missing required capability fails a strict lab run; an advertised absent
  capability does not.** `requiresCapabilities` skip means a precondition the
  operator could supply, so strict mode names the capability to enable.
  `absentCapabilities` is the opposite-mode twin: the case asserts the documented
  disabled behaviour (a `501`), so a manager that has the capability on is exactly
  the one configuration where it cannot apply — no setting would make it run, and
  the enabled case is what proves the feature. It is exempt from the strict policy
  like a `mockOnly` case, and its hint says "a manager that does not enable X".
- A task-scoped stream or snapshot case (`$task`) skips in the lab when
  `CONFORMANCE_TASK_ID` is not set: the cluster, not the runner, lacks the value.
  That skip stays strict.
- **The lab run is strict by default.** After the run, every case that did not
  run fails it, with a message listing each skipped case and the variable that
  would enable it (`CONFORMANCE_ALLOW_MUTATION=1`, `CONFORMANCE_TASK_ID`, a
  manager that advertises the capability, the earlier case that captures a
  value). A green lab run therefore means the cases were exercised, not skipped.
  `CONFORMANCE_ALLOW_SKIPS=1` is the only opt-out. A `mockOnly` case and an
  `absentCapabilities` case are never counted: no setting makes either run against
  a live manager, and the mock leg is where the disabled behaviour is proven.
- **The mock run may skip only a declared capability gate.** `TestConformanceMock`
  fails if any other case was skipped, and asserts both the exact number of cases
  in `expectedMockCases` and the exact number of gates in
  `expectedMockGatedCases`, so a case that silently stops running (a lost file, a
  filter, a widened skip rule) is a failure rather than a green run.

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
- `Proxy` serves a finite NDJSON exec stream, a finite log stream and a 14-byte
  raw attach console.

The backend is mutex-guarded, so the suite is clean under `-race`. It advertises
the capability set the shipped default handler advertises (`read`, `write`,
`events`, plus the broker's `exec`, `logs`, `files`, `health`, `attach`), and the
mock leg reads that list from `/v1/version` rather than hard-coding it: snapshots
are off, so the three snapshot cases in `write.json` and `errors.json` are the
only gated skips.

## The lab leg

`lab_test.go` reads:

| Variable | Meaning |
|----------|---------|
| `CONFORMANCE_HOST` | base URL of the manager's TLS listener (required, `https://` only) |
| `CONFORMANCE_CA` | path to a PEM CA to trust, in addition to the system roots (optional) |
| `CONFORMANCE_READER_TOKEN` | reader token (required) |
| `CONFORMANCE_RUNNER_TOKEN` | runner token; without it runner cases skip and the run fails (unless `CONFORMANCE_ALLOW_SKIPS=1`) |
| `CONFORMANCE_ADMIN_TOKEN` | admin token; without it admin cases skip and the run fails (unless `CONFORMANCE_ALLOW_SKIPS=1`) |
| `CONFORMANCE_TASK_ID` | a running task id for the exec/logs/attach and snapshot cases; without it they skip and the run fails (unless `CONFORMANCE_ALLOW_SKIPS=1`) |
| `CONFORMANCE_ALLOW_MUTATION` | `1` runs the mutating cases; default off |
| `CONFORMANCE_ALLOW_SKIPS` | `1` accepts skipped cases; unset is strict, and a skip fails the run |

Before running a case, the lab leg reads `GET /v1/version` with the reader token
and logs the daemon build and capability list. That is what gates the new cases
and what a lab report cites as the build under test; a manager that cannot answer
it fails the run instead of guessing.

The stream cases run in both legs. A live manager cannot be pinned to mock data,
so in lab mode the terminal frame is what is asserted (`exit` for exec, `eof` for
logs), extra data frames are allowed, and an event stream that opened and sent
only the `ready` control frame passes: a quiet cluster has no changes to report,
and the mock leg proves the frame content. The logs case reads **to** `eof`
(`untilType` with `minFrames: 1`) instead of pinning a frame count, because a
console log has as many lines as the guest printed — the first lab run saw two
lines plus `eof`, which a pinned `frames: 2` truncated before the terminal frame.
The attach case asserts the `200` and `application/octet-stream`; a guest console
that prints nothing before the 2 s deadline is logged and accepted, because the
mock leg proves the relay copies console bytes.

The snapshot cases are gated, so one fixture set fits both a default manager and
one started with `--snapshots-enable`:

- the seven `501 not_implemented` cases in `errors.json` carry
  `absentCapabilities: ["snapshots"]`, so they run against a default manager and
  skip against one that advertises the capability. That skip is the
  opposite-mode twin of the enabled cases, not a missing precondition, so a
  strict lab run passes on it (the first lab run failed here);
- `write.json` creates a snapshot (`presentKeys` + `minValues.sizeBytes`, then a
  `capture` of the id and a `DELETE` that cleans it up) and `errors.json` asserts
  `DELETE /v1/tasks/{id}/restored` → `404 no_restored_vm`, both
  `requiresCapabilities: ["snapshots"]`. They skip against a default manager.

Because a snapshot copies the per-task disk, the node records the copy as
`disk_path` in its own metadata and adds it to `sizeBytes`; that path is not part
of the wire DTO, so the case asserts the snapshot document and a positive total
size instead of inventing a field. `CONFORMANCE_TASK_ID` must name a **running**
task for the create (a VM is snapshotted live) and a task with **no restored VM**
for the `no_restored_vm` case; a task that does have one answers `204` and the
case would report it.

`make conformance-lab` skips cleanly (exit 0) when `CONFORMANCE_HOST` or
`CONFORMANCE_READER_TOKEN` is missing. It refuses a non-`https://` host: the run
carries bearer tokens, so there is no cleartext fallback. Tokens are never
printed; a failure names the request path, status and a body truncated to 2 KiB,
never the `Authorization` header.

Once the run starts, it is strict: a case that did not run fails it unless
`CONFORMANCE_ALLOW_SKIPS=1` (the `mockOnly` cases and the `absentCapabilities`
cases are exempt, because no setting would make either run). The failure lists
every skipped case with the variable that would enable it, so "the lab passed"
cannot mean "the lab skipped the interesting half".

The scheduled workflow is `.github/workflows/conformance-lab.yml`. It writes the
`CONFORMANCE_CA` secret to a temporary file, refuses to run without
`CONFORMANCE_HOST`, and deletes the file afterwards. Set the repository variable
`CONFORMANCE_TASK_ID` to a task that exists on the cluster to run the exec, logs,
attach and snapshot cases; without it they skip, which fails the run. The weekly
schedule sets `CONFORMANCE_ALLOW_SKIPS=1` because it never allows mutation and so
must skip the mutating cases by policy; a manual dispatch does not set it, so a
dispatched run has to exercise the cases (dispatch with `allow_mutation` enabled
to run the whole set). Operators who prefer to seed a cluster can use the
`mockOnly` case names as a checklist, but the lab run itself does not require
seeded data.

## Adding a case

1. Pick one file: `read`, `errors`, `write` or `streams`.
2. Add the case with the smallest assertion set that still proves the contract.
3. Ask: does this pass against an empty cluster? If not, it needs
   `"mockOnly": true`, a capability gate, or must be self-contained (create the
   object it uses).
4. A mutating create name and its Idempotency-Key carry `{{run}}`; the loader
   rejects a literal one, and the mock leg counts a capability-gated skip, so the
   fixture cannot drift into an unexpected skip.
5. Bump `expectedMockCases` — and `expectedMockGatedCases` when the new case is
   capability-gated — in `mock_test.go`; the mock run fails on a count mismatch by
   design.
6. `make conformance` and `go test ./test/conformance -count=1`.
