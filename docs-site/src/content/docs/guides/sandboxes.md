---
title: "Sandboxes"
description: "Run agent sandboxes: microVMs booted from a sealed template, reached through a node's sandbox control plane."
---

Related: [Golden Image Recipes](/guides/golden-images/), [Snapshots](/guides/snapshots/), [API reference](/reference/api/), `pkg/sandbox/`, `pkg/sandbox/netpolicy/`, `cmd/sandboxd/`

A sandbox is one microVM booted from a sealed template, with its own disk, its own
network namespace and policy, and its own per-boot guest-agent credential. It is
**not** a task: nothing schedules it, nothing reconciles it, and its identity is not
a task ID. A caller creates it, uses it, and removes it — or lets the node's reaper
take it back when it has been idle for too long.

This page covers the control plane as it ships today: `sandbox create`, `sandbox
exec`, `sandbox rm`, the TTL reaper, and the tokens that are scoped to one sandbox.

## What you need

- A KVM host with the jailer (`/usr/local/bin/jailer`), iproute2 and nftables
  (**`/usr/sbin` must be on the daemon's `PATH`**, because `nft` lives there).
- At least one sealed template in the template store
  (`/var/lib/firecracker/golden` by default), built by *this* host's software — a
  template built on another Firecracker version or another kernel is refused.
  `recipes/sandbox-base.yaml`, `recipes/sandbox-python.yaml` and
  `recipes/sandbox-node.yaml` are the reference sandbox templates:

  ```bash
  sudo swarmcracker template build recipes/sandbox-base.yaml
  ```

- The node's cluster material under the state directory (the sandbox API is served
  on the same v1 surface as the cluster's, with the same token store), and root —
  creating a sandbox needs KVM, a network namespace and the jailer.

## Start the control plane

```bash
sudo sandboxd \
  --state-dir /var/lib/swarmkit \
  --golden-dir /var/lib/firecracker/golden \
  --rootfs-dir /var/lib/firecracker/rootfs \
  --jailer-chroot-dir /var/lib/swarmcracker/jailer \
  --socket /var/run/swarmcracker/sandboxd.sock \
  --reap-interval 30s
```

The daemon is the process that must keep running, for two reasons: a sandbox's VMM is
a process that must not die with the shell that asked for it, and an egress
allowlist's endpoints are sockets the daemon holds inside the sandbox's namespace. A
sandbox created by a one-shot command would lose its egress the moment that command
exited.

`sandboxd --help` lists the rest: per-sandbox defaults (`--vcpus`, `--memory`), the
jail uid range, the Jailer's parent cgroup and the VMM cgroup limits, the default idle
TTL (`--default-ttl`), the longest life any sandbox may have (`--max-ttl`), the admission
limits (`--quota-max-per-owner`, `--quota-memory-mb`, `--quota-disk-mb`,
`--quota-retry-after`), and an optional TCP listener (`--api-listen`)
for token-authenticated callers. The Unix socket's file mode is the credential for local
callers; the TCP listener is TLS and needs a bearer token.

## Create

```bash
sudo swarmcracker sandbox create sandbox-base --ttl 30m
```

```text
d22a0d18ff5f
  state:      ready
  template:   sandbox-base@1.0.0
  guest:      10.63.0.2 02:75:15:07:30:6e
  disk copy:  sparse
  agent:      src-7a2b9f9d0025a30f (boot a9c158a7db1a)
  idle ttl:   10m0s
  max ttl:    none (the node sets no maximum)
  ready in:   4.406s (node), 4.786s (this command)
```

`ready` means the guest agent answered its hello, not that a VMM started. The node's
own ready window (record created to the guest's answer) is printed beside the
command's wall clock, so the number is a measurement rather than a target. A cold boot
of the reference `sandbox-base` template on an idle KVM host measured **3.7–4.4 s**;
there is no pool yet (see [Not in this slice](#not-in-this-slice)).

Create works through these steps, in this order:

1. The template is resolved and its pin checked. A template built by other host
   software is refused with `template_pin_mismatch` before a disk is copied.
2. The node **admits** the sandbox against its quota — see
   [Quota and admission](#quota-and-admission). A refusal is answered here, while the
   attempt still holds nothing: no record, no namespace, no disk and no VM.
3. The sandbox record is written in state `created`, and rewritten as each resource is
   allocated — the namespace, then the disk — so a node that dies mid-create leaves a
   record its teardown can free instead of resources nothing names.
4. The network is attached: its own namespace, the fixed `tap0` in it, its own `/30`
   from the sandbox pool, and a default-deny nftables table whose only openings are
   the egress allowlist.
5. A per-instance disk is materialized from the template's artifact, and this boot's
   agent token is written into it, so the guest requires that credential from its
   first hello.
6. The VM is launched inside the jailer, in the sandbox's namespace, with an identity
   of its own (host name, machine id, entropy, clock, address, MAC) that the guest
   applies through the metadata service.
7. The record becomes `ready` with what the guest answered.

Egress is off by default. `--egress host:port` (repeatable) allows exactly that one
destination and nothing else — not its other ports, not ICMP, not the name's other
addresses. A name is resolved once, at attach, so the addresses the guest is told and
the addresses its namespace accepts are one list.

## Quota and admission

A node has finite memory and disk, and one caller must not be able to spend all of it.
`sandboxd` checks both before it allocates anything for a create, and it admits the
sandbox's lifetime in the same step — see
[Idle TTL, the maximum lifetime, and the reaper](#idle-ttl-the-maximum-lifetime-and-the-reaper):

| Limit | Flag | Default | Counted as |
|---|---|---|---|
| Sandboxes one owner may hold at once | `--quota-max-per-owner` | 10 | sandboxes |
| Guest memory all sandboxes may occupy | `--quota-memory-mb` | 8192 (8 GiB) | MiB |
| Disk all sandbox disks may occupy | `--quota-disk-mb` | 51200 (50 GiB) | MiB |

All three are checked on every create, not whichever is cheapest: a request the owner
has room for can still be one the node does not. Every field's `0` means the default
above it, and a negative value stops `sandboxd` at start — a node that cannot say what
it admits does not serve. The only way to widen a limit is to name a larger number;
there is no "unlimited".

What counts against a limit is a sandbox that exists or is being built — state
`created` or `ready`. A `failed` sandbox holds nothing (the create path freed it) and a
`stopped` one is on its way out, so neither is counted. A create that names no owner
shares one bucket with every other unnamed create, because a limit a caller can leave
by leaving out a field is not a limit.

The memory a create is counted at is the MiB it asked for. The disk is the size of the
template image it is about to copy — the most that copy can occupy, which is the
conservative side to be wrong on. An existing sandbox's disk is measured as the file
the node made.

A refusal answers `429` with code `quota_exceeded` — not `rate_limited`, because the
two are fixed differently: a rate limit clears with time, a quota clears when a sandbox
is removed or reaped:

```json
{
  "schemaVersion": 1,
  "kind": "Error",
  "error": {
    "code": "quota_exceeded",
    "message": "sandbox: owner ci already holds 10 of the 10 sandboxes this node allows one owner",
    "requestId": "req_9c3d1a57b204",
    "retryable": true,
    "details": {
      "resource": "sandboxes_per_owner",
      "owner": "ci",
      "limit": 10,
      "used": 10,
      "requested": 1,
      "retryAfterMs": 60000
    }
  }
}
```

`details.resource` names which limit was reached and therefore the unit of `limit`,
`used` and `requested`: `sandboxes_per_owner` counts sandboxes, `node_memory_mb` and
`node_disk_mb` are MiB. `Retry-After` carries the same hint in seconds. It is a hint,
not a promise: removing a sandbox, or waiting for the reaper, frees its share sooner.

**A refused create leaves nothing behind.** The check runs before the sandbox record is
written and before the first resource is allocated, so a rejection leaves no record, no
network namespace, no disk and no VM — there is nothing for `sandbox rm` to clean up and
nothing for the next `sandbox ls` to show.

A **fork** is admitted the same way, and the numbers come from the image it resumes
rather than from a template. Each clone occupies the memory that image is — a memory
image is exactly as large as the guest's memory, which is why a clone does not choose
its own size — and takes a copy of the checkpoint's disk. The chunks the memory image is
stored in are *not* counted: they belong to the checkpoint and every clone shares them,
so a fork adds no chunk bytes.

The whole fork is admitted or none of it is, and it is decided before anything exists:
the ledger advances clone by clone, so a fork of three is refused when the third does
not fit, and at that moment there is no record, no namespace, no disk and no staged
image. `sandbox fork <sandbox>` mints its checkpoint first, so its check runs *before*
the image is taken: a refused fork does not leave a gigabyte of chunks behind either.
The refusal names the same limits as a create's and carries the same `Retry-After`.

## Exec

```bash
sudo swarmcracker sandbox exec d22a0d18ff5f -- sh -c 'echo out; echo err >&2; exit 7'
```

```text
out
err
$ echo $?
7
```

Standard output, standard error and the exit status are the guest's own: a command
that fails is a failing command, not a successful `exec` of one. Commands run over the
sandbox's guest agent, which is the only way into its guest; there is no container
runtime inside a sandbox template.

A sandbox that is not `ready` cannot run anything, and the refusal names the state and,
for a failed sandbox, the reason it failed.

## Files, status and the console

```bash
sudo swarmcracker sandbox cp d22a0d18ff5f:workspace/app.log ./app.log   # out
sudo swarmcracker sandbox cp ./repo d22a0d18ff5f:workspace/repo          # in
swarmcracker sandbox status d22a0d18ff5f
swarmcracker sandbox logs d22a0d18ff5f | tail -20
```

Exactly one of `cp`'s two arguments names a sandbox, as `<id>:<guest-path>`. The guest
path is relative to the sandbox's **workspace root** (`/workspace` in the shipped
templates), which is the only directory its guest agent will read or write — a leading
`/` is that root — and it names what is created or read: `cp ./repo <id>:workspace/repo`
writes the tree at `/workspace/repo`, and `cp <id>:workspace/repo ./repo` reads it back.

A **file** and a **directory** both work in both directions. Neither is buffered: a tree
is a tar archive streamed through a pipe, so a repository-sized (or gigabyte-sized)
workspace costs no more memory than a chunk of it, on either side. A path that is neither
a file nor a directory — a symbolic link, a device, a socket — is refused: copying a link
would not copy what it points at.

Writing a tree needs the template's agent to speak **`untar`**, which is advertised as an
*optional* capability. A template built before it keeps working for everything else — a
single file still arrives, and `sandbox cp <local-file> <id>:<path>` uses it — and only
the tree is refused, naming the capability it lacks:

```json
{"error": {"code": "not_implemented",
  "details": {"reason": "template_capability_missing", "capability": "untar"}}}
```

An archive arriving from a sandbox is **untrusted input**, even when the sandbox is yours.
The node's guest-side extraction and this machine's own extraction hold the same rules: an
entry that would write outside the destination is refused rather than skipped, a symbolic
link is written only when its target stays inside, a hard link only when it names a file
the same archive wrote, and a device, fifo or socket is skipped and counted. A workspace
copy never becomes a way to write outside the workspace.

`sandbox status` prints the node's record **and the guest's own answer**: its hostname and
machine id, the kernel it is running, the vCPUs and memory it believes it has, its uptime,
the tools its template ships, and its network — interfaces, addresses, routes and the
resolver it consults. None of it is parsed from a command's output: the guest agent reads
its own `/proc`, `/etc/os-release` and network state, so a template needs no shell for it
and the answer does not change when a command's formatting does. `--format json` prints
the `SandboxStatus` document whole.

`sandbox logs` prints the **serial console**, which is a different thing from a task's
logs: it is the VMM's record of the guest's serial port, from the first instruction the
guest executes, so it exists for a sandbox that never became ready — which is where a boot
that failed explains itself.

## Expose a port

```bash
sudo swarmcracker sandbox expose d22a0d18ff5f --port 8080
```

```text
http://127.203.229.82:8080/?token=1b25014808e5b83ad7cc45ff43a86f5c86ab3ff2634c7c2ff9ae4435f9dbfefd
  sandbox:  d22a0d18ff5f
  host:     127.203.229.82:8080
  guest:    8080
  token:    1b25014808e5b83ad7cc45ff43a86f5c86ab3ff2634c7c2ff9ae4435f9dbfefd
  expires:  2026-11-16T11:01:47.543991811Z
```

A guest's port is not published by a host forward: the guest is on a TAP inside the
sandbox's own network namespace, and that namespace has no interface on the node, so there
is nothing on the node for a forward to point at. The node runs a listener of its own
instead — the preview proxy — and relays each accepted connection to the guest from
*inside* the namespace, the one place the guest's address means anything.

The listener binds a loopback address derived from the sandbox's ID, with the guest's own
port. Two things follow, and both are what a caller wants: the address is **stable** — the
same sandbox always gets the same one, with no allocator state to lose — and **distinct** —
two sandboxes exposing the same guest port listen on two different addresses, so neither
can answer for the other. Keeping the guest's port means a service that prints its own URL
prints something that is still true from the host. Because the address is on the node's
loopback, a preview is reachable from the host and from nowhere else: exposing a port does
not put a guest service on the wire.

A preview is not open. The listener requires a **preview token**, minted when the port is
exposed and printed once — the node keeps only its digest — and the check is at the HTTP
layer, because a preview is a link a person opens:

| Request to an exposed address | Answer |
|---|---|
| `GET http://<address>/` | `401` `Unauthorized` |
| `GET http://<address>/?token=<wrong>` | `401` `Unauthorized` |
| `GET http://<address>/?token=<token>` | the guest's own answer, relayed byte for byte |
| `GET http://<address>/` with `Authorization: Bearer <token>` | the guest's own answer |
| a guest port that was never exposed | connection refused: nothing is listening |

Nothing is rewritten on the way through: the request reaches the guest exactly as the
client sent it, token and all.

An exposure also expires. `--ttl` is how long it is served for (`0` means the node's
default, negative means never), and once it has elapsed the listener stays and answers
`410 Gone` to every request until the port is un-exposed or the sandbox is removed — the
listener is the thing that can say "this was here and is gone", which a connection refused
cannot.

`sandbox unexpose <id> --port <port>` closes the listener and takes the port back out of
the sandbox's own policy; the sandbox is unaffected, and the port can be exposed again
(which mints a new token). Nothing an exposure does changes what the sandbox may reach: a
preview is an opening *into* one guest, written in two chains of its own in the sandbox's
table, and the egress half of that table is never touched by one. An exposure's sockets are
the daemon's, so a restarted `sandboxd` re-opens the exposures its records still name: a URL
with a token a client kept keeps working.

## Remove

```bash
sudo swarmcracker sandbox rm d22a0d18ff5f
```

```text
d22a0d18ff5f removed
  process stopped:      true
  api socket removed:   true
  cgroup removed:       true
  jail removed:         true
  netns removed:        true
  tap removed:          true
  disk removed:         true
  agent token removed:  true
  instance record:      true
  api tokens revoked:   0
  sandbox record:       true
```

`rm` works on a running sandbox, not only an idle one. It stops the VMM first, and if
the VMM cannot be stopped **nothing else is freed**: the namespace, disk and jail are a
live VM's, and taking them from under it is not a removal. Then it frees the socket,
the cgroup, the jail, the namespace (with the TAP device, its addresses, its routes and
the nftables policy inside it — all state of the namespace), the disk, the host's copy
of the agent token, the node's instance record and every API token scoped to the
sandbox, and deletes the record last, because the record is the only list of what the
teardown had to free.

The VMM's serial console log (`<log-dir>/<id>.log`) is kept, as a task's log outlives
its task. Log retention is not part of this slice.

## States

| State | What it means | How it is reached |
|---|---|---|
| `created` | The record exists and the node is building the sandbox. | Written before anything is allocated; a create that dies leaves this. |
| `ready` | The guest agent answered its hello. The only usable state. | The launch returned, which it only does on a hello. |
| `failed` | The node could not make it usable, or lost it. `reason` says which. | A create step failed, or a reconciliation pass found the VMM gone. |
| `stopped` | The node ended it. | Written by `rm` before anything is freed; the record is deleted after. |

There is no `starting`, no `paused` and no `terminating`: a state a sandbox cannot
leave would be a label nothing can act on. A sandbox whose VMM exits on its own is
reported `failed` the next time the node lists it, with the reason.

## Idle TTL, the maximum lifetime, and the reaper

Every sandbox may carry an idle TTL (`--ttl`, or the daemon's `--default-ttl`; `0`
means the node's default, and a negative value means the reaper never takes it).
`sandboxd` reaps every `--reap-interval`, and a sandbox past its TTL is removed
exactly as `rm` removes it.

Idle is measured from **the last time the node saw the sandbox used**: its creation,
and every `exec` into it. The node has no way to observe activity inside a guest, so
"the guest is busy" is not a question it can answer; "nobody has asked this sandbox to
do anything in TTL" is, and that is what the reaper asks. A long-running sandbox should
be given a TTL that covers its gaps, or be exec'd in.

A node may also cap how long any sandbox may live (`sandboxd --max-ttl`), and a caller may
ask for a shorter life for one sandbox (`sandbox create --max-ttl`). This deadline is
measured from **the sandbox's creation** and nothing postpones it — using the sandbox
pushes the idle TTL out and leaves this one where it was, which is the difference between
the two settings and the reason both exist:

| Setting | Measured from | Moved by `exec` |
|---|---|---|
| Idle TTL (`--ttl`) | the last use | yes |
| Maximum lifetime (`--max-ttl`) | the creation | no |

The two run side by side and **whichever comes first is the one that decides**: the sandbox
is removed exactly as `rm` removes it, and the reaper reports which of the two took it. A
sandbox that hits its maximum lifetime is not failed or paused first — it is gone, and its
disk, namespace, jail and credential go with it, so a workload that must finish inside its
lifetime should be sized for it rather than run up against it.

The maximum lifetime is admitted with the rest of a create, before anything is allocated,
and it is **on the record** (`maxTtlSeconds`, and `maxExpiresAt` on the API's document), not
only in the daemon's configuration: the deadline a sandbox was admitted with survives a
restart of `sandboxd` and a later change of `--max-ttl`. A create asking for a longer life
than the node grants is refused — `400` with `details.reason` = `max_ttl_too_long`, plus the
`limit` and what was `requested`, leaving no record, namespace, disk or VM behind — rather
than being quietly given a shorter life than it asked for:

```bash
# A node granting at most a day, and a sandbox asking for two.
swarmcracker sandbox create sandbox-base --max-ttl 48h
#   error: the requested sandbox lifetime 48h0m0s is longer than this node allows (24h0m0s)

swarmcracker sandbox create sandbox-base --max-ttl 6h
```

A create that asks for no maximum on a node that has a ceiling gets the ceiling; on a node
with no `--max-ttl` it gets no maximum, and `sandbox create` prints `max ttl: none`. A
lifetime shorter than a second is rounded up to a second rather than down to none, because a
record counts its lifetime in whole seconds and "half a second" must not come out as "no
limit at all".

A fork is a sandbox like any other: each clone carries the node's ceiling as its own
deadline. A negative `--max-ttl` on `sandboxd` stops it at start rather than being read as
"no maximum", the same way every other limit in the daemon's configuration is refused.

## Tokens scoped to one sandbox

`sandbox exec` is a shell inside a guest, so a credential that can exec in one sandbox
must not be able to exec in another. The API expresses that with scopes and a sandbox
selector:

```bash
# On the node (the Unix socket's file mode is the credential: it is an admin).
sudo swarmcracker sandbox token d22a0d18ff5f --name ci-build
```

```text
sc_...
  id:       tok_9c3d1a57
  name:     sandbox d22a0d18ff5f
  sandbox:  d22a0d18ff5f
  scopes:   sandboxes:read, sandboxes:exec
```

The secret is printed once. That token can run commands in its sandbox and read it, and
nothing else. Over the API (`-H https://node:4250 --ca tls.crt --token-file …`):

| Attempt with that token | Result |
|---|---|
| `POST /v1/sandboxes/<its own id>/exec` | `200` and the command's stream |
| `POST /v1/sandboxes/<another id>/exec` | `403` `forbidden`, `details.reason` = `sandbox_selector_mismatch`, `details.sandboxId` = the sandbox asked about |
| `POST /v1/sandboxes` (register one) | `403` `forbidden`, `details.reason` = `scope_missing`, `details.requiredScope` = `sandboxes:write` |
| `POST /v1/sandboxes/<id>/tokens` (issue one) | `403` `forbidden`, `details.reason` = `scope_missing`, `details.requiredScope` = `sandboxes:token` |
| `GET`/`PUT /v1/sandboxes/<id>/files` (copy files) | `403` `forbidden`, `details.reason` = `scope_missing`, `details.requiredScope` = `sandboxes:files` |
| `GET /v1/sandboxes` (list the node's sandboxes) | `403` `forbidden`, `details.reason` = `sandbox_list_not_permitted`, `details.sandboxIds` = the sandboxes it is bound to |
| `GET /v1/sandboxes/<another id>` (read one) | `403` `forbidden`, `details.reason` = `sandbox_selector_mismatch`, `details.sandboxId` = the sandbox asked about |

The listing is refused rather than filtered: the node's inventory is every tenant's
sandboxes, and a credential for one is not a reader of it. Its own sandbox is still readable
by id, and `status`, `logs` and `files` answer for it when they are allowed at all.

A runner token that holds `sandboxes:exec` but names no sandbox at all is refused with
`details.reason` = `sandbox_selector_required`, because otherwise it would be a shell
inside every sandbox on the node.

**A token cannot outlive its sandbox.** Minted with no `--expires-at`, it is given the
sandbox's own deadline — its idle TTL measured from its last use, or its maximum lifetime,
**whichever comes first**, because that is when the reaper takes it back. An `--expires-at`
past that deadline is refused with `details.reason` = `token_outlives_sandbox` and
`details.sandboxExpiresAt`, rather than silently shortened. A sandbox with neither an idle
TTL nor a maximum lifetime never ends on its own, so nothing bounds its tokens; removing the
sandbox revokes them either way.

The sandbox's own agent credential is per boot and per sandbox too: it is written into
that sandbox's disk and published beside its muxer, and another sandbox's guest refuses
it. `sandbox rm` deletes both the agent token and the API tokens scoped to the sandbox,
so a credential never outlives what it authenticates.

The scopes are `sandboxes:read` (reader and up), `sandboxes:exec`, `sandboxes:files` and
`sandboxes:ports` (runner and up), `sandboxes:write` (operator and up, for create and
remove) and
`sandboxes:token` (admin only: issuing a credential is administrative work). `files` sits
beside `exec` because it is the same power: writing a file into a sandbox is writing code
into it, and reading one file can be what reading `/etc/shadow` would be. Checkpoints add
a sixth, `sandboxes:checkpoints` (operator and up), which covers taking, listing, forking
and removing them — see [Checkpoints and forks](#checkpoints-and-forks).

## Checkpoints and forks

A checkpoint is a sandbox's memory and disk, stored under a name, that a fork resumes.
Taking one needs a **ready** sandbox, and it is taken with the guest's writers stopped:
the guest receives `quiesce freeze`, freezes its filesystems, the images are copied, and
the guest thaws.

```bash
sudo swarmcracker sandbox checkpoint create d22a0d18ff5f before-upgrade
swarmcracker sandbox checkpoint ls            # every checkpoint on the node
swarmcracker sandbox checkpoint inspect before-upgrade
```

The name is the caller's to choose, because it is the only handle the image has later: a
sandbox does not have to exist for its checkpoint to be listed, forked or freed.

### What `fsfreeze` does and does not stop

`quiesce freeze` runs `sync` and then `FIFREEZE` on every **writable, block-device-backed**
mount — on the reference templates that is `/`, the ext4 root disk. What that leaves out
is deliberate and worth knowing before you rely on a checkpoint:

- **`tmpfs` and `/dev/shm` are not frozen.** Their contents are frozen by the VM pause
  that precedes the memory snapshot, but they are memory, not disk: they are captured
  as they were at the pause, not synced to a stable state first. State you care about
  belongs on the root disk.
- **An overlayfs upper layer is not frozen.** `FIFREEZE` applies to the filesystem a mount
  is backed by; an overlay's upper layer is reached through the overlay, and the freeze
  is refused there. A template whose root is an overlay gets no writer stop at all —
  check `sandbox checkpoint inspect <name>`, whose `quiesceMounts` lists what was
  actually frozen. If that list does not include the filesystem your data is on, the
  checkpoint's consistency on it is unknown.
- **A guest agent that does not implement `quiesce`** cannot be checkpointed safely. That
  is refused **per call**, with `details.reason` naming the capability the template's
  agent lacks, rather than `template_incompatible` and rather than quietly taking an
  image whose writers were running. Rebuild the template from a recipe whose agent
  advertises `quiesce` (`recipes/sandbox-{base,python,node}.yaml` all do).

Freezing stops the guest's writers for as long as the copy takes, so a checkpoint of a
sandbox with a 512 MB memory image pauses it for the duration of the memory write. The
sandbox resumes when the copy finishes; it is not restarted, and its disk is not
replaced.

### Fork

```bash
# Clone what a running sandbox is doing right now: the node takes the checkpoint for you.
swarmcracker sandbox fork d22a0d18ff5f --count 2

# Clone a checkpoint you already have; its sandbox need not exist.
swarmcracker sandbox fork --checkpoint before-upgrade
```

```text
ID              STATE  GUEST IP     TEMPLATE
5da74f0e06da    ready  10.63.0.3    sandbox-base@1.0.0
057f62e6e3c9    ready  10.63.0.4    sandbox-base@1.0.0

checkpoint d22a0d18ff5f-fork-1729 holds the image these were forked from.
Free it with `swarmcracker sandbox checkpoint rm d22a0d18ff5f-fork-1729` once no more clones are needed.
```

A fork of a running sandbox **mints** its checkpoint, so the response names it: a fork
that always left an image behind would leak one per call if the caller could not find it
to delete.

Each clone is a separate sandbox, not a second view of one: its own copy of the disk, its
own network namespace with its own MAC and IP, its own idle TTL and maximum lifetime (the
node's ceiling, since no caller gets a longer life for a clone than for a create), and its
own guest-agent
credential. The two identities a clone must *not* inherit are the ones the image froze in
place — the MAC, the IP, and the vsock path the muxer listens on — and the two it must be
given fresh are its network and its credential. A clone whose guest still holds the
checkpoint's credential would have nothing to rotate, so the host mints one per clone,
writes it beside the clone's own muxer, and the guest adopts it in the same `prepare` that
releases the freeze.

### Removing a checkpoint

```bash
swarmcracker sandbox checkpoint rm before-upgrade
```

```text
before-upgrade removed
  freed:  0 B (state 23.5 KB, disk 2.0 GB, memory chunks 0 B in 0 chunks)
  retained: 512.0 MB referenced by 1 checkpoint(s), counted once per reference and not deduplicated
```

A checkpoint's memory image is stored as 4 MiB chunks shared with every other checkpoint
of the same sandbox, so removing one of two forks frees its state document and its disk
copy but only the chunks the other does not use. The response separates the two:
`freed.freedBytes` is what came back, `freed.retainedBytes` is what the remaining
checkpoints still reference — **once per referencing checkpoint, not deduplicated**, so it
is a measure of what is referenced rather than of physical disk. `--json` prints that
document whole.

Forgetting a checkpoint does not stop the sandboxes forked from it; they have their own
disk and their own memory. Chunks nothing references are freed with the checkpoint that
last referenced them, and a daemon start sweeps the chunks a crashed checkpoint left
unnamed.

## API

The control plane is served on the node's v1 API. The routes are documented in
[`api/openapi.yaml`](https://github.com/restuhaqza/SwarmCracker/blob/main/api/openapi.yaml):

| Route | Scope |
|---|---|
| `GET /v1/sandboxes` | `sandboxes:read`, and no sandbox selector |
| `POST /v1/sandboxes` | `sandboxes:write` |
| `GET /v1/sandboxes/{id}` | `sandboxes:read`, and a sandbox selector must name it if the credential carries one |
| `DELETE /v1/sandboxes/{id}` | `sandboxes:write` + the sandbox selector |
| `POST /v1/sandboxes/{id}/exec` | `sandboxes:exec` + the sandbox selector |
| `GET /v1/sandboxes/{id}/files` | `sandboxes:files` + the sandbox selector |
| `PUT /v1/sandboxes/{id}/files` | `sandboxes:files` + the sandbox selector |
| `GET /v1/sandboxes/{id}/logs` | `sandboxes:read` + the sandbox selector |
| `GET /v1/sandboxes/{id}/status` | `sandboxes:read` + the sandbox selector |
| `POST /v1/sandboxes/{id}/ports` | `sandboxes:ports` + the sandbox selector |
| `DELETE /v1/sandboxes/{id}/ports/{port}` | `sandboxes:ports` + the sandbox selector |
| `POST /v1/sandboxes/{id}/tokens` | `sandboxes:token` + the sandbox selector |
| `POST /v1/sandboxes/{id}/checkpoints` | `sandboxes:checkpoints` + the sandbox selector |
| `GET /v1/sandboxes/{id}/checkpoints` | `sandboxes:checkpoints` + the sandbox selector |
| `POST /v1/sandboxes/{id}/checkpoints/{name}/restore` | `sandboxes:checkpoints` + the sandbox selector |
| `POST /v1/sandboxes/{id}/fork` | `sandboxes:checkpoints` + the sandbox selector |
| `GET /v1/checkpoints` | `sandboxes:checkpoints` |
| `GET /v1/checkpoints/{name}` | `sandboxes:checkpoints` |
| `DELETE /v1/checkpoints/{name}` | `sandboxes:checkpoints` |
| `POST /v1/checkpoints/{name}/fork` | `sandboxes:checkpoints` |

A checkpoint's name is unique on the node, which is why the four `/v1/checkpoints`
routes are node-scoped: a checkpoint outlives its sandbox, and once the sandbox is gone
those routes are the only names left that can find the image. A sandbox-scoped route asked
about a checkpoint of **another** sandbox answers `404`, not `403`: that route's resource
is the checkpoint of that sandbox, and it does not exist there. `sandboxes:checkpoints` is
an operator scope.

Exec is the same `application/x-ndjson` stream, frame for frame, as
`POST /v1/tasks/{id}/exec`, and it is produced by the same handler. The version
document advertises the `sandboxes` capability exactly when the node serves one; a node
without a control plane answers `501` with `details.capability` = `sandboxes`.

## Events

Sandbox changes are streamed on the same `GET /v1/events` stream as the cluster's,
selected with `types=sandbox`:

| Type | When |
|---|---|
| `sandbox.created` | A sandbox this node's API created, and each clone a fork made |
| `sandbox.failed` | A create that left a failed record behind, or a sandbox a reconcile pass found behind a VMM that is gone; `object.reason` says why |
| `sandbox.removed` | A sandbox an API call ended, or an idle one the reaper took back |

```bash
# SSE by default; the node's own events are selected with types=sandbox.
curl -sN -H "Authorization: Bearer $TOKEN" \
  "https://node:4250/v1/events?types=sandbox&include=object"
```

A sandbox is not a SwarmKit object, so nothing watches it for the API: these events come
from the node that serves them, which is why they are the only live-only events on that
stream. A sandbox event carries an empty `cursor`, so `since` does not resume across it,
and a watcher that connects after a change does not receive it — dedupe by
`resource.id`, and read `sandbox ls` for the current state rather than replaying a
history that does not exist.

The stream carries a change whether a caller asked for it or this node did. An idle sandbox
the reaper takes back arrives as `sandbox.removed` — with no object, exactly as a removal
through the API carries none, because the record is gone by then — and a sandbox the
reconcile pass finds behind a VMM that is gone arrives as `sandbox.failed`, whose
`object.reason` says so. Both origins publish on one bus (`pkg/sandbox/events.go`) that the
API subscribes to, so a watcher needs neither a second connection nor polling to learn that
a sandbox died or was reclaimed.

## Not in this slice

These are the boundaries of the first sandbox slice, not gaps in it:

- **One node.** The routes are served by a node's `sandboxd`; there is no
  cluster-wide placement, and a sandbox does not migrate.
- **No pool and no cluster-wide quota.** Warm pools and per-tenant quotas that span nodes
  are later slices. The quota above is one node's own admission, checked for both `sandbox
  create` and a fork. There is no warm pool, so the ready time above is a cold boot.
- **A preview is one port, on the node's own loopback.** `sandbox expose` serves a guest
  port to the machine the sandbox runs on; it is not a public ingress, there is no hostname
  per exposure and no cluster-wide route to one. The token check is at the HTTP layer, so a
  preview of a non-HTTP protocol is not something this route does.
- **Exec runs as the guest agent's user** (root inside the sandbox). A per-sandbox
  `--user` is not part of this slice; the isolation boundary is the microVM, not the
  uid inside it.
- **The console log is kept** after `rm`, as a task's log is.
- **`sandboxes:read` is narrowed by the selector for both doors.** A credential bound to
  specific sandboxes is refused the node's inventory (`GET /v1/sandboxes`) and is refused
  **another** sandbox's document by id; it reads only the sandboxes it names. A credential
  with no sandbox selector — an operator, an admin, the local socket, a runner narrowed by
  tasks only — reads exactly what its scope allowed before, so no role lost a permission:
  whether a selector-less runner should read a sandbox at all is a separate question, and
  neither rule answers it.
- **A sandbox-scoped token cannot copy files.** The token `sandbox token` mints holds
  exactly `sandboxes:read` and `sandboxes:exec` — read and exec, **not** `sandboxes:files`
  — so a file copy needs a credential that holds `sandboxes:files` (a runner, operator or
  admin token, or the local socket). It is not a boundary, because `exec` is already the
  power to write the workspace, and that is exactly why it is written down instead of left
  implicit: adding a capability to a credential that can leak needs a caller that needs it,
  not a symmetry argument, and no caller has needed a sandbox-scoped token to copy yet.
  When one does, the mint is where it changes.
