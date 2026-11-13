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
TTL, and an optional TCP listener (`--api-listen`) for token-authenticated callers.
The Unix socket's file mode is the credential for local callers; the TCP listener is
TLS and needs a bearer token.

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
2. The sandbox record is written in state `created`, and rewritten as each resource is
   allocated — the namespace, then the disk — so a node that dies mid-create leaves a
   record its teardown can free instead of resources nothing names.
3. The network is attached: its own namespace, the fixed `tap0` in it, its own `/30`
   from the sandbox pool, and a default-deny nftables table whose only openings are
   the egress allowlist.
4. A per-instance disk is materialized from the template's artifact, and this boot's
   agent token is written into it, so the guest requires that credential from its
   first hello.
5. The VM is launched inside the jailer, in the sandbox's namespace, with an identity
   of its own (host name, machine id, entropy, clock, address, MAC) that the guest
   applies through the metadata service.
6. The record becomes `ready` with what the guest answered.

Egress is off by default. `--egress host:port` (repeatable) allows exactly that one
destination and nothing else — not its other ports, not ICMP, not the name's other
addresses. A name is resolved once, at attach, so the addresses the guest is told and
the addresses its namespace accepts are one list.

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

## Idle TTL and the reaper

Every sandbox may carry an idle TTL (`--ttl`, or the daemon's `--default-ttl`; `0`
means the node's default, and a negative value means the reaper never takes it).
`sandboxd` reaps every `--reap-interval`, and a sandbox past its TTL is removed
exactly as `rm` removes it.

Idle is measured from **the last time the node saw the sandbox used**: its creation,
and every `exec` into it. The node has no way to observe activity inside a guest, so
"the guest is busy" is not a question it can answer; "nobody has asked this sandbox to
do anything in TTL" is, and that is what the reaper asks. A long-running sandbox should
be given a TTL that covers its gaps, or be exec'd in.

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

A runner token that holds `sandboxes:exec` but names no sandbox at all is refused with
`details.reason` = `sandbox_selector_required`, because otherwise it would be a shell
inside every sandbox on the node.

The sandbox's own agent credential is per boot and per sandbox too: it is written into
that sandbox's disk and published beside its muxer, and another sandbox's guest refuses
it. `sandbox rm` deletes both the agent token and the API tokens scoped to the sandbox,
so a credential never outlives what it authenticates.

The scopes are `sandboxes:read` (reader and up), `sandboxes:exec` (runner and up),
`sandboxes:write` (operator and up, for create and remove) and `sandboxes:token`
(admin only: issuing a credential is administrative work).

## API

The control plane is served on the node's v1 API. The routes are documented in
[`api/openapi.yaml`](https://github.com/restuhaqza/SwarmCracker/blob/main/api/openapi.yaml):

| Route | Scope |
|---|---|
| `GET /v1/sandboxes` | `sandboxes:read` |
| `POST /v1/sandboxes` | `sandboxes:write` |
| `GET /v1/sandboxes/{id}` | `sandboxes:read` |
| `DELETE /v1/sandboxes/{id}` | `sandboxes:write` + the sandbox selector |
| `POST /v1/sandboxes/{id}/exec` | `sandboxes:exec` + the sandbox selector |
| `POST /v1/sandboxes/{id}/tokens` | `sandboxes:token` + the sandbox selector |

Exec is the same `application/x-ndjson` stream, frame for frame, as
`POST /v1/tasks/{id}/exec`, and it is produced by the same handler. The version
document advertises the `sandboxes` capability exactly when the node serves one; a node
without a control plane answers `501` with `details.capability` = `sandboxes`.

## Not in this slice

These are the boundaries of the first sandbox slice, not gaps in it:

- **One node.** The routes are served by a node's `sandboxd`; there is no
  cluster-wide placement, and a sandbox does not migrate.
- **No file copy, no exposure, no preview proxy, no pool and no quota.**
  `sandbox cp`, `sandbox expose`, the preview proxy, warm pools and per-tenant quotas
  are later slices. There is no warm pool, so the ready time above is a cold boot.
- **Exec runs as the guest agent's user** (root inside the sandbox). A per-sandbox
  `--user` is not part of this slice; the isolation boundary is the microVM, not the
  uid inside it.
- **The console log is kept** after `rm`, as a task's log is.
- **`sandboxes:read` is not narrowed by selectors.** A sandbox-scoped token lists the
  node's sandboxes, exactly as a task-scoped token lists the cluster's tasks; the
  selector gates exec and removal.
