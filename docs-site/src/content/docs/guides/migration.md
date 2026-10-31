---
title: "Live Migration Guide"
description: "Move a running microVM to another node with `swarmcracker vm migrate`."
---

> Move a running microVM to another node with `swarmcracker vm migrate`.

---

## What a move is

SwarmKit has no task-reassignment operation, so a live migration is built out of
the one primitive it does have: a replacement. `swarmcracker vm migrate <task>
--to <node>` asks the manager to snapshot the task's VM on the node that runs
it, carry the artifact to the target, and then replace the task with one the
target starts from that artifact. The guest resumes from the instant of the
dump, with its processes, memory and uptime intact.

One request drives the whole move, in this order:

1. **validate** — this manager is the swarm leader, the task is running, its
   service is replicated with exactly one replica, the target is a different,
   ready and active node, and no other migration of that service is in flight.
2. **export** — the source pauses the VM, takes a Firecracker `Full` snapshot,
   copies the task disk, resumes the VM, and publishes the artifact in a
   directory of its own.
3. **relay** — each file of the artifact crosses from the source through the
   manager's spool to the target's staging directory, one at a time, checked
   against the manifest's size and SHA-256 on both hops.
4. **prepare** — the target records a directive: the next task of this service
   that starts here restores this artifact.
5. **replace** — the service gets a placement constraint pinning it to the
   target and is updated, so SwarmKit stops the old task and starts the
   replacement there.
6. **verify** — the manager waits for a task of that service on the target to
   report running, and measures the gap.
7. **cleanup** — the placement is restored, a `moved` guest's peers are told to
   forget its old network location, and the source's export is discarded. The
   export goes whenever the update landed — `pending` included — because the
   retry reads the target's staged copy.

The call is synchronous. It is bounded by three minutes, plus up to one minute
of cleanup the manager performs before it answers, so a client deadline shorter
than four minutes can drop the report of a move the manager still finishes. The
command's `--timeout` defaults to five minutes for that reason. Flags are in the
[CLI reference](/reference/cli/#swarmcracker-vm); the route and its errors are in
the [API reference](/reference/api/#migrations).

## What a move keeps

- **The guest itself.** Memory, CPU and device state, and the task's disk, all
  captured at one paused instant. Kernel uptime continues; processes do not
  restart.
- **The guest's host-side identity, as the snapshot recorded it.** Firecracker's
  state file names the TAP device, the vsock muxer socket and the block-device
  path of the source VM. The target creates that TAP device before the load,
  because the state file resolves the interface by name; the disk copy is
  materialised at the recorded path by the load itself, and Firecracker
  recreates the muxer socket there as it loads. The API socket and the console
  log are the exception: they are chosen when Firecracker starts, so they belong
  to the new task.
- **The guest's MAC address, and a new IP address.** The VM is the same VM, so
  its interface MAC is the one in its memory image; the guest is re-addressed to
  the address the replacement task's IPAM allocation gave it. The TAP stays down
  until that re-addressing is done, so the guest never answers on this node with
  the old node's address.
- **Published ports.** A restored task reprograms its host-side port rules on
  the target, after the guest has its new address, so a published service is
  reachable on the target's host ports.
- **`vm logs` and `vm exec`.** The console log of the replacement is
  `<log-dir>/<new-task-id>.log`, and the node records the restored VM under the
  new task ID, so both commands work on the moved task through the manager API
  as they would on any other task.

## What a move does not keep

- **Writes the source accepted after the dump instant.** The source resumes as
  soon as the dump and disk copy finish and keeps serving while the artifact
  crosses the network, so this is a real window, not a theoretical one. See
  [the consistency window](#the-consistency-window).
- **Volumes.** A named volume or a bind mount lives on the host that ran the
  task, and the restore creates the guest's disk from the artifact rather than
  re-creating the task's mounts. A volume is not carried to the target: data
  the workload wrote into it stays on the source node.
- **Every network attachment.** The restored guest is configured with one
  interface, so only the first attachment of the replacement task is attached to
  the VM. A task with more than one network loses the others; the node logs the
  dropped network names at the restore.
- **The task ID.** The replacement is a new SwarmKit task with a new ID, which
  is what the service, the console log, the API socket and the metrics series
  are keyed by.
- **The old task's files on the source node.** SwarmKit's teardown of the old
  task removes its VM, TAP device and disk. There is nothing left on the source
  to fall back to.

### The consistency window

The dump is one instant. The source resumes immediately afterwards and keeps
answering requests while its memory image and disk copy are transferred,
prepared and restored; the guest on the target resumes from the dump instant and
knows nothing that happened on the source after it. A write the source accepted
in that window — including a commit a database acknowledged — is not on the
target.

The window runs from the dump to the moment the replacement starts, so its
length is the transfer time plus the scheduling delay of the replacement:
seconds on a healthy cluster with a fast link. A move that answers `moved` ended
inside the manager's three-minute call bound, so its window is bounded by that
too; a move that answers `pending` was answered before the replacement started,
and nothing in the call bounds the window after it. If the workload cannot lose
writes in that window, quiesce its writers before the move, or drain it and
accept a restart instead.

## Requirements

- **One replica.** The move pins the service to the target with a placement
  constraint, and a constraint applies to every task of a service, so only a
  `replicated` service with exactly one replica can be moved. The command
  refuses anything else before it posts the request, and the manager refuses it
  again.
- **A running task.** A stopped, completed or failed task is refused.
- **A different, ready, active node.** A drained node, a node that is not
  `ready`, and the task's own node are all refused with a 400.
- **The same hardware and the same Firecracker version.** A Firecracker snapshot
  only loads on a host with the same CPU model and the same Firecracker version.
  The manager does not check this before it starts: point `--to` at a node that
  does not match and the move spends the transfer, fails at the target's
  restore, and keeps retrying that restore until the directive expires.
- **An admin token.** The route requires `migrations:write`, which only the
  `admin` role holds. A token minted before this feature existed carries the
  scope list it was minted with, so it does not have `migrations:write` until it
  is reminted; see [Operations](/guides/operations/#live-migration).
- **The manager API, with its node broker.** `vm migrate` needs `-H`, `--ca` and
  a token: the move crosses hosts, and only a manager can drive it. The command
  refuses to run without them. The manager must have its node broker and its
  migration spool wired; without them it does not advertise the `migrations`
  capability and the route answers `501`.
- **The swarm leader.** Only the manager that is the swarm's raft leader runs a
  migration; a follower answers `503 unavailable` with `details.reason:
  "not_leader"` and `details.leader` naming the node to retry against. A cluster
  that cannot report who its leader is does not block the move; the manager runs
  it rather than refusing every migration.

## Run a move

Find the task and its node, then move it:

```bash
swarmcracker task ls
swarmcracker service ps web

swarmcracker -H https://manager:4244 --ca ca.crt --token-file admin.token \
    vm migrate 5f3a1b2c9d --to worker-2
```

`<task>` is a task ID or a unique prefix of at least four characters. `--to`
takes a node ID, a hostname, or a unique prefix of a node ID. The command
resolves both against the cluster's own lists and posts the resolved IDs, since
the route takes an exact task ID. A prefix that matches more than one node is
refused with the candidates listed, and a typo is refused with the nodes the
cluster has.

A successful move prints the report the manager returned:

```
Migration mig-4f2a9c1e7b3d5a62
  task      5f3a1b2c9d4e5f60718293a4b5c6d7e8
  service   svc-0123456789abcdef
  from      k3m9p2q7r4s6t8v0w1x5y7z2a
  to        d8e2f5g9h1j3k6m0n4p7q2r5s
  validate  ok          12ms
  export    ok          2.9s
  relay     ok         11.2s
  prepare   ok          38ms
  replace   ok          1.4s
  verify    ok          2.1s
  cleanup   ok          3.4s
  downtime  1.2s
  outcome   moved
```

`from` and `to` are node IDs, not hostnames: the `--to worker-2` above resolved
to the second ID.

## Reading the report

There is one line per phase, in the order of the move, with the result and how
long the phase took. A phase that had not finished when the report was written
prints `-` for its duration — that is the pending verify described below.

The **downtime** is the number the move is judged by, and it is measured by the
manager, not by the guest: from the instant the old task was observed gone (or,
if it was never observed gone, from the instant the service update returned) to
the first instant a replacement reported running on the target. The manager
polls once a second, so the figure is accurate to about a second. It does not
include the transfer, because the source is serving during it. `not measured` is
printed when the report carries no downtime at all: either no replacement was
ever seen running — an unmeasured downtime is not zero seconds — or the
replacement was first seen running in the same one-second poll as the old task's
disappearance, where the gap is zero at the resolution the manager measures at.
A fast move can therefore print `not measured` for a downtime shorter than the
poll.

No number is quoted here for the shipped command, because the transfer cost
depends on the guest's memory size and the link. The spike behind this feature
measured a naive move — source paused for the dump, the transfer and the load —
at about 13 seconds of guest-visible downtime for a 512 MiB guest with a 100 MiB
disk, dominated by the memory transfer; the source in this design is not paused
for the transfer, so for a guest of that size the figure is an upper bound, not
this command's cost. The `downtime` line is the measurement for your cluster.

The **outcome** is `moved` when a replacement reported running, and `pending`
when it did not within the verify deadline.

## When the answer is `pending`

`pending` means the service was replaced but the manager did not see the
replacement report running within its two-minute verify deadline. Nothing
failed: the service update landed, the target keeps the directive it recorded,
and the artifact is still staged there. SwarmKit's own retry of the replacement
restores from that artifact, so a slow start finishes on its own - unless the
retry is scheduled on another node, because the manager restores the placement on
every landed outcome; a retry that lands elsewhere boots a fresh VM.

What the retry reads is the **target's** staged copy: the bytes the move staged
there, named by the manifest that was written beside them. The source's export is
not part of it, so the manager discards the source's export as soon as the service
update has landed — a `pending` move included. A `pending` move therefore leaves
nothing on the source node; the only copy that survives is the one on the target
that the retry may still consume, and the directive that expires below.

A `pending` move also skips the re-anneal: the manager only re-anneals when the
outcome is `moved`. So the guest that eventually starts keeps its old network
location in every node's bridge and neighbour table, and its peers can be
unable to reach it until those entries age out — about 16 seconds in the spike
— even though the guest itself is up.

What to do: read the verify line's message first. `replacement_not_running` means
the old task went away and the target has not reported the replacement running;
`old_task_not_gone` means the service never lost its old task, which is a
scheduling problem rather than a restore problem. Then look at the replacement
with `swarmcracker service ps <service>` (or `task ls`) and at the target node's
daemon log: a restore that fails there is retried by SwarmKit, and each retry
finds the directive again.

The directive expires ten minutes after it was prepared. After that a retry
finds no directive, so the next attempt boots a fresh VM for the service: the
workload comes back, without the state the move was carrying.

There is no command that cancels a migration. The staged copies on the target
stay where they are; the directive's ten minutes decide whether a retry still
uses them, and the replacement itself is SwarmKit's to finish. The source's export
is already gone: it is discarded as soon as the update lands, whatever the outcome.

## When a move fails

A failure the manager reports is a typed error whose `details.phase` names the
step that failed, so you know whether anything was replaced:

- **Before the update landed** (validate, export, relay, prepare, and a
  `replace` whose update the service refused) nothing was replaced: the service
  still points at the old task, which keeps running where it was. The manager
  removes the target's directive, discards the source's artifact, drops the
  spool entry and restores any placement it changed, then answers the error. The
  move can be retried.
- **After the update landed** (verify) the service already points at the target.
  The manager restores the placement and keeps the target's directive and the
  bytes staged there, because they are the copy a retry restores from; the
  source's export is discarded, exactly as a successful move discards it. A
  replacement that failed outright answers `503 unavailable` with
  `details.reason: "replacement_failed"`.

The split is whether the update landed, not which step failed: a `replace` that
answers an error is the first case, because before the manager reports that
error it reads the service back, and an update that landed with its answer lost
is treated as having landed.

The command exits with the error's status code (`2` invalid, `3` not found, `4`
conflict, `5` forbidden, `6` unavailable or timeout, `1` not implemented), so a
script can tell a refused request from a failed move.

## Rollback: the artifact, not the source VM

There is no rollback that returns the guest to the source node. A failure after
the service update happens after SwarmKit has been told to replace the old task,
and the old task's teardown removes the VM and the disk it ran on; nothing brings
that VM back. The rollback the feature offers is the artifact: the staged bytes
on the target, plus the directive that names them, are kept so the replacement's
retry restores the same VM state instead of booting a cold one.

That rollback has an end. The directive expires ten minutes after it was
prepared; a retry after that boots a fresh VM. A move whose service update landed
— `moved`, a `pending` move, and a verify that failed after the update — deletes
the artifact on the source as part of its cleanup, because the retry reads the
target's staged copy and never the source's; that staged copy is removed by the
restore that consumed it, so nothing is kept after a move lands.

To put the workload back on the source node, move it there with another
`vm migrate` once the source node is healthy again. That is a new move with its
own consistency window, not a rollback.

## After the move: re-annealing the network

The guest keeps its MAC address, so every node's bridge still has a forwarding
entry pointing that MAC at the source node's tunnel, and every neighbour table
still has the guest's address behind it. Nothing about the restored VM fixes
those entries, and they age out slowly: the spike measured about 16 seconds of
unreachability from a peer node before the stale entry expired, and under 400 ms
once the entry was removed.

So a successful move ends with a re-anneal: the manager asks every node —
the target included, because its bridge learned the guest's MAC on the overlay
port before the VM arrived — to delete the guest's neighbour and forwarding
entries for each of its IPv4 addresses. The next frame then re-learns the MAC on
the node that now runs the guest.

The re-anneal is best effort. A node that cannot be reached is logged and does
not turn a completed move into a failure; reachability there recovers on the
next ARP, on the order of the stale entry's lifetime. A guest with no IPv4
attachment has nothing to re-anneal and the step is skipped. A move that ends
`pending` does not re-anneal at all, as noted above.

## Limitations to plan around

- One replica per service, as above. A scaled service cannot be moved one
  replica at a time.
- Volumes and extra networks do not come along.
- The target must match the source's CPU model and Firecracker version.
- The artifact has to fit the manager's three-minute call bound: a guest whose
  memory and disk cannot cross the link in that window fails at whichever step
  runs out of time rather than streaming for half an hour.
- A manager restarted mid-move loses the in-flight record and the caller's
  report. What is on the nodes survives — the source's artifact, the target's
  directive, the staged bytes — so a replacement SwarmKit retries still restores
  from the artifact until the directive expires.
