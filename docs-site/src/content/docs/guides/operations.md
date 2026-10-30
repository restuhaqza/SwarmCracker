---
title: "Operations Guide — SwarmCracker"
description: "How to operate, monitor, troubleshoot, and maintain a SwarmCracker cluster in production."
---

> How to operate, monitor, troubleshoot, and maintain a SwarmCracker cluster in production.

---

## Health Checks

### Node-Level Health

Run the built-in doctor to verify node health:

```bash
swarmcracker doctor
```

**Passing checks:**

| Check | What it verifies | Healthy Output |
|-------|-----------------|----------------|
| CPU virtualization (KVM) | `/dev/kvm` exists and the KVM module is loaded | `✓ CPU virtualization (KVM) — KVM available` |
| Firecracker binary | `firecracker` is in PATH | `✓ Firecracker binary — found at /usr/local/bin/firecracker` |
| Firecracker version | `firecracker --version` runs | `✓ Firecracker version — v1.15.1` |
| Kernel image | Kernel present at the configured path | `✓ Kernel image — /usr/share/firecracker/vmlinux (25.0 MB)` |
| TUN/TAP device | `/dev/net/tun` is available | `✓ TUN/TAP device — /dev/net/tun available` |
| Available memory | Free memory from `/proc/meminfo` | `✓ Available memory — 16.0 GB total, 7.5 GB available` |
| CPU cores | Processor count | `✓ CPU cores — 8 CPU core(s)` |
| Bridge interface | `swarm-br0` exists | `✓ Bridge interface (swarm-br0) — swarm-br0 exists` |
| Port 4242 | SwarmKit API port availability | `✓ Port 4242 — port 4242 available` |
| Manager/Worker service | systemd unit is active | `✓ Worker service — worker service is active` |
| Join tokens | `/var/lib/swarmkit/join-tokens.txt` present | `✓ Join tokens — found at /var/lib/swarmkit/join-tokens.txt` |
| Firecracker processes | Running VM process count | `✓ Firecracker processes — 3 Firecracker process(es) running` |

### API Health Endpoint

The daemon exposes a health check endpoint on its health address
(`--health-addr`, default `127.0.0.1:8080`):

```bash
# Local
curl -s http://127.0.0.1:8080/healthz

# Response
{
  "healthy": true,
  "checks": {
    "kvm": { "status": "ok", "message": "..." },
    "bridge": { "status": "ok", "message": "..." },
    "firecracker": { "status": "ok", "message": "..." }
  }
}
```

The same HTTP server exposes Prometheus metrics at
`http://127.0.0.1:8080/metrics`.

### Cluster Health

```bash
# List all nodes
swarmcracker node ls

# Expected output:
# ID            STATUS  HOSTNAME    AVAILABILITY
# abc123def456  Ready   manager-1   Active
# def456abc789  Ready   worker-1    Active
# ghi789def012  Ready   worker-2    Active
```

### VM Health

```bash
# Check a specific VM
swarmcracker vm status <vm-id>

# Watch mode
watch -n2 'swarmcracker vm status <vm-id>'

# Cluster overview (nodes / services / tasks)
swarmcracker cluster status
```

### Container Healthchecks

A service can declare a **container healthcheck**: a command that runs inside the
microVM on a schedule. A task with a healthcheck is not considered running until
the check passes, so a rolling update only moves on once the new task is actually
serving.

```bash
swarmcracker service create --name api --image myapi:v2 --replicas 3 \
    --health-cmd 'curl -fsS http://localhost:8080/healthz' \
    --health-interval 10s \
    --health-timeout 3s \
    --health-retries 3 \
    --health-start-period 20s
```

The command runs **inside the guest** through the guest agent over vsock — the
same mechanism as [`vm exec`](/guides/guest-access/) — using the guest shell
(`/bin/sh -c`), and the check passes when the command exits `0`. It runs **as
root** in the guest, so treat the check like any other guest command. The check
therefore needs a reachable guest agent; see
[Guest Access → Limitations](/guides/guest-access/#limitations) for the images
and modes where it is unavailable.

| Flag | Default | Meaning |
|------|---------|---------|
| `--health-cmd` | — | Check command; enables the healthcheck |
| `--health-interval` | `30s` | Time between checks |
| `--health-timeout` | `30s` | Per-check timeout |
| `--health-retries` | `3` | Consecutive failures before the task is unhealthy |
| `--health-start-period` | `0` | Grace period during which failures do not count |

The four tuning flags apply **only** when `--health-cmd` is set. Without it there
is no healthcheck and a task starts exactly as before. An invalid value is
rejected (`--health-retries 0`, `--health-interval 0s`), and passing any other
`--health-*` flag without `--health-cmd` is an error — no flag is silently
ignored.

#### Health-gated rollouts

The healthcheck **gates the task's start**: the executor holds the task in
`STARTING` until a check passes, and only then reports it `RUNNING`. That is what
makes `--update-order start-first` wait for real readiness:

```bash
swarmcracker service update api \
    --image myapi:v3 \
    --update-order start-first \
    --update-failure-action rollback \
    --health-cmd 'curl -fsS http://localhost:8080/healthz'
```

With `start-first`, SwarmKit does not stop the old task until the new one is up —
and because a healthchecked task is not `RUNNING` until its check passes, the
rollout does not proceed until the new task is healthy. If the check never
passes, the retries are exhausted, the task fails, and
`--update-failure-action rollback` rolls the service back.

A task that becomes unhealthy **while running** is stopped, and the service's
restart policy (`--restart-condition`) decides what happens next. Each probe
(dial plus exec) is bounded by `--health-timeout`, and the health wait does not
hold the executor's lock, so `service rm` and stop are never stalled behind it.

#### Seeing health

`service ps` and `task ls` show a `HEALTH` column. When the CLI can get the
owning node's live verdict it carries that; when the node answered but has no
live row for the task — or the task is terminal — it carries the value derived
from the health-gated task state; when the node could not be reached at all it
carries `-`:

```bash
swarmcracker service ps api
# ID            STATUS    HEALTH     NODE          IMAGE      ERROR
# 5f3a1b2c9d0e  RUNNING   failing    abc123def456  myapi:v3   # live: probe failing, retries not exhausted
# 9c8d7e6f1a2b  STARTING  starting   def456abc789  myapi:v3   # live: not healthy yet
# 1a2b3c4d5e6f  RUNNING   RUNNING    ghi789def012  myapi:v3   # no live row (older daemon / not published): derived
# 7e6f1a2b9c8d  RUNNING   -          jkl012mno345  myapi:v3   # owning node unreachable
```

The rows also end in an `ERROR` column (empty above, because every task shown is
placed and running). It carries the task's `Status.Err` — why a task that SwarmKit
could not schedule, or that an enforcer rejected, is not running — truncated at 40
characters; `task inspect` prints the full text. See
[`service ps`](/reference/cli/#swarmcracker-service).

| Value | Source | Meaning |
|-------|--------|---------|
| `starting` | live | The task has not passed a check yet, so it is not reported `RUNNING` |
| `healthy` | live | The most recent probe passed |
| `failing` | live | The most recent probe failed, but the retries threshold is not reached — the task is still `RUNNING` and no restart has fired |
| `unhealthy` | live | The retries are exhausted; the VM is being stopped so SwarmKit reschedules the task |
| `STARTING` / `RUNNING` / `FAILED` | derived | Live health was not available (an older daemon, or a task that has not published yet), so the health-gated task state is shown |
| `-` | — | No healthcheck, the task has completed or stopped, or the owning node could not be reached |

`failing` is the one worth watching: it is the window between "a check started
failing" and "the task is restarted", up to `(retries - 1) × interval` (60s with
the defaults). It is the value the derived column cannot show.

`--format json` emits the display value as `health` and, when it was fetched,
the raw live observation as `live_health` (`state`, `failing_streak`, `output`,
`command`, `updated_at`).

`task inspect` shows the declared healthcheck and, when a check failed, the
reason the executor recorded:

```bash
swarmcracker task inspect --format table 5f3a1b2c
# Healthcheck:
#   Test: CMD-SHELL curl -fsS http://localhost:8080/healthz
#   Interval: 10s
#   Timeout: 3s
#   Retries: 3
#   Start Period: 20s
# Error: healthcheck failed: /bin/sh -c curl -fsS http://localhost:8080/healthz: ...
```

The default `--format json` output carries the same information in the task's
`Spec.Container.Healthcheck` and `Status.Err` fields.

#### Live health, cluster-wide

The live verdict comes from the node that owns the task. The executor publishes
every probe result into the per-task metadata file next to the VM socket, and
the daemon serves it on its health server:

```
GET http://<node-address>:8080/v1/tasks/health
{"tasks":[{"task_id":"5f3a1b2c…","state":"failing","failing_streak":2,"command":"…","output":"…","updated_at":"…"}]}
```

`service ps` / `task ls` resolve each task's node from SwarmKit and query the
node endpoints concurrently — bounded, each request capped at 3s and the whole
fan-out at 10s — then overlay the result. Tasks with no healthcheck are absent
from the response and keep their derived `-`; a node whose own address does not
answer is retried once on the CLI machine's loopback, as described below.

The daemon's health server listens on `--health-addr`, which defaults to
`127.0.0.1:8080` — node-local. To get a cluster-wide `HEALTH` column, bind it on
the cluster network when starting the daemon:

```bash
# In each node's systemd unit (or on the command line)
swarmd-firecracker --health-addr 0.0.0.0:8080 …
```

and, if the daemons bind a port other than 8080, tell the CLI:

```bash
swarmcracker service ps api --health-port 9090
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--health-port` | `8080` | Port of each node's health server |
| `--no-health` | `false` | Skip the fan-out; derive `HEALTH` from the task state only |

`--no-health` also skips the `ListNodes` call, so it is the escape hatch when the
endpoint is not exposed and you do not want any extra requests. An invalid
`--health-port` is rejected; it is never silently ignored.

**Trust model.** The endpoint returns health only — state, failing streak, the
probe command and its last (bounded) output, which can contain whatever the
check itself printed. There is no authentication. The bind address *is* the
boundary: the default `127.0.0.1:8080` exposes it to nothing, and binding `0.0.0.0`
exposes it to the cluster network exactly like the existing `/healthz` and
`/metrics` (which are on the same server).

The CLI queries each task's node at `Node.Status.Addr` (the address the manager
observed) on that port. If a node's own address does not answer, the CLI also
tries `127.0.0.1` there — the daemon's default bind — and accepts the answer only
for nodes that own a task it reports. So a daemon on the machine the CLI runs on
is reached even with the default loopback bind, while a genuinely remote node
whose daemon is not exposed shows `-` rather than a possibly-wrong value. A node
that *did* answer, but has no live row for a task (an older daemon without the
endpoint, or a task that has not started yet), falls back to the derived value.

Exposing the endpoint is therefore what buys a cluster-wide live column, not just
an optimisation. `--no-health` is the escape hatch when it is not exposed and you
would rather see the derived value than `-`.

#### Live health on a single node

The same data is available locally, without the fan-out. `vm status` / `vm list`
read the metadata file directly:

```bash
# Run this on the node hosting the task.
swarmcracker vm status 5f3a1b2c
# [Status]
#   State:        Running ✓
# [Health]
#   State:        Failing ✗ (retries not exhausted; task still running)
#   Failing:      2 consecutive probe(s)
#   Check:        /bin/sh -c curl -fsS http://localhost:8080/healthz
#   Last output:  curl: (7) Failed to connect to localhost port 8080
#   Observed:     2026-10-13 15:04:07
```

This is **node-local**: run the command on the node hosting the task. VMs started
in the foreground by `vm create` have no daemon and no monitor, so they never
carry health.

#### The API limitation

SwarmKit's API has no health field: `api.ContainerStatus` carries only
`ContainerID`, `PID` and `ExitCode`, and `api.TaskStatus` has none — unchanged in
SwarmKit v2.1.2 and upstream `master`. A new field alone would not fix it: while
a task is `RUNNING` the agent blocks in `Wait` and never polls its status, so any
value would stay frozen until the task exits. SwarmCracker therefore **derives**
the fallback `HEALTH` value from the health-gated task state, and gets the live
value — including `failing`, which the derived value cannot express — from each
node's health endpoint as described above.


---

## Monitoring

### Metrics

The node's health server exposes Prometheus metrics at
`http://127.0.0.1:8080/metrics`:

```bash
# Scrape this node's metrics
curl -s http://127.0.0.1:8080/metrics

# Per-task resource usage
swarmctl metrics <task-id>
```

> `swarmcracker metrics` shows live per-VM resource usage, including VMs started
> by the daemon for services. Use the Prometheus endpoint above for scraping.

**Metrics written today** (all prefixed `swarmcracker_`):

| Metric | Labels | Meaning |
|--------|--------|---------|
| `swarmcracker_vms_running` | — | VMs currently running on this node |
| `swarmcracker_vms_total` | `instance`, `status` (`started`/`stopped`/`crashed`) | VM lifecycle transitions |
| `swarmcracker_vm_boot_duration_seconds` | `instance`, `service` | Time from VM start to running |
| `swarmcracker_vm_boot_errors_total` | `instance`, `reason` (always `boot_failure`) | VMs that never reached running |
| `swarmcracker_vm_crashes_total` | `instance`, `service`, `reason` | VMs that failed after reaching running |
| `swarmcracker_vm_health` | `instance`, `service`, `state` | One-hot live healthcheck state |
| `swarmcracker_vm_balloon_mib` | `instance`, `task_id`, `service` | Configured memory-balloon target, in MiB |
| `swarmcracker_vm_cpu_seconds` | `instance`, `task_id`, `service` | Cumulative busy CPU time inside the guest, in seconds |
| `swarmcracker_vm_memory_bytes` | `instance`, `task_id`, `service` | Memory in use inside the guest (MemTotal − MemAvailable) |
| `swarmcracker_vm_net_rx_bytes` | `instance`, `task_id`, `service` | Cumulative bytes received on the guest's non-loopback interfaces |
| `swarmcracker_vm_net_tx_bytes` | `instance`, `task_id`, `service` | Cumulative bytes sent on the guest's non-loopback interfaces |
| `swarmcracker_disk_usage_bytes` | `instance`, `path` | Bytes used on a SwarmCracker directory |
| `swarmcracker_disk_total_bytes` | `instance`, `path` | Capacity of that filesystem |
| `swarmcracker_service_replicas` | `service` | Replica count the autoscaler last decided for an autoscaled service (written on the manager) |
| `swarmcracker_service_scale_events_total` | `service`, `direction` (`up`/`down`) | Load-driven replica changes the autoscaler applied (written on the manager) |

Two of these split what used to be one overloaded signal, and it is worth reading
them as a pair:

- `swarmcracker_vm_boot_errors_total` is written **only** on the boot path, when
  the VMM fails to start the VM (`reason` is always `boot_failure`). A VM that
  never started was never counted running, so this path does **not** move
  `swarmcracker_vms_running` — it previously decremented it, which could drive
  the gauge negative; that bug is fixed. This path also does not touch the crash
  counter.
- `swarmcracker_vm_crashes_total` is written only for a failure **after** the VM
  reached running — a VM that exited on its own (`unexpected_exit`), an exhausted
  healthcheck that force-stopped the VM (`unhealthy`), or a task that returned a
  non-zero exit code (`exit_error`). A crash decrements
  `swarmcracker_vms_running`, because the VM was counted when it started. A stop
  the desired state asked for (SwarmKit `Shutdown`/`Terminate` — `service rm`, a
  scale-down, a rolling update) is **not** a crash: it records
  `swarmcracker_vms_total{status="stopped"}` instead, so it never feeds the
  crash-loop rule. A clean self-exit — the workload finished on its own and the
  VM exited zero, so the task goes `COMPLETE` — also records
  `swarmcracker_vms_total{status="stopped"}`, so a `stopped` count is not
  necessarily operator-initiated.

`swarmcracker_vm_health` is a one-hot gauge over `starting`, `healthy`, `failing`
and `unhealthy` — exactly one has value `1` per `instance`/`service`. The four
series are **removed** when a task is torn down, so a stale verdict cannot linger.
It is not a second health source: it publishes the same `pkg/runtime` states the
node-local metadata sidecar carries, from the single place health state changes.

`swarmcracker_disk_usage_bytes` and `swarmcracker_disk_total_bytes` are sampled at
daemon start and on the five-minute ticker for the configured `rootfs`,
`snapshots` and `state` paths. A path that is unset, missing or unreadable emits
no series at all, so the pressure ratio is never computed against a directory the
daemon does not own.

`swarmcracker_vm_balloon_mib` is set when a **service** task's VM starts, and
only for a task that configured a balloon (`--balloon-mib` or the
`swarmcracker.balloon` label) — see [Memory Balloon](/guides/advanced/#memory-balloon).
It reports the target that was *configured*, not live usage, so an unconfigured
task emits no series and a VM created directly with `vm create --balloon-mib`
emits no series at all; removing or scaling down a task leaves its last reported
value in place until the node is restarted.

#### Per-task resource usage

The four `vm_cpu_seconds`, `vm_memory_bytes`, `vm_net_rx_bytes` and
`vm_net_tx_bytes` series describe what the **workload inside the VM** used, not
what the Firecracker process on the host used. The daemon asks the guest agent for
the VM's own `/proc` counters every **15 seconds** and records the answer, so a
busy guest and a busy VMM are distinguishable and a task can be attributed to a
service.

All four are cumulative, so read them with `rate()` or `increase()`:

```promql
# CPU cores used by a service, averaged over 5 minutes
sum by (service) (rate(swarmcracker_vm_cpu_seconds[5m]))

# Network throughput per task
sum by (task_id) (rate(swarmcracker_vm_net_rx_bytes[5m]))
```

What each one means:

- **CPU** is the guest's busy time — `user + nice + system + irq + softirq +
  steal` from `/proc/stat`, excluding idle and iowait. `guest`/`guest_nice` are
  not added again, because the kernel already counts them in `user`/`nice`.
- **Memory** is `MemTotal − MemAvailable`, the kernel's own estimate of what a new
  workload could claim. That is memory *in use*, not the VM's configured size, so
  it moves with the workload and with the balloon.
- **Network** sums every interface except loopback: traffic between two processes
  in the same guest never leaves the VM, and counting it would report network use
  the guest did not have.

Two behaviours are deliberate:

- **A failed sample writes nothing.** If the guest agent cannot answer (a VM still
  booting, or an image whose agent predates this feature), no series is published
  for that tick. A zero would be indistinguishable from an idle guest and would
  pin a busy task's series at 0. The poller logs a warning once per outage rather
  than every tick, and recovers on the next successful sample — the counters are
  cumulative, so a missed interval costs only that interval.
- **Teardown deletes the series.** A scaled-down or removed task leaves no stale
  row, so a per-service sum never includes a VM that no longer exists.

Worst-case staleness is one poll interval (15 s), and a `rate()` window shorter
than two intervals has too few samples to be meaningful — use `[1m]` or longer.
Nested virtualization does not change these readings: they come from the guest's
own kernel, which is what the workload sees.

**Declared but not populated.** The following series are registered so an
operator sees the intended shape, but no code path writes them — do not read them
as current values:

- `swarmcracker_vxlan_peers`, `swarmcracker_vxlan_expected_peers` — overlay peer
  counts.
- `swarmcracker_manager_health`, `swarmcracker_raft_health` — manager/Raft health.

The default alert rules read only series that are written today; the rules that
referenced the unpopulated series above were removed rather than left inert. See
[Alerting](/guides/alerting/) for the rule set and a verification recipe.

### Tracing

Metrics answer "how much" and logs answer "what was printed". Tracing answers
"where the time went" for one task: a single trace shows the image pull, the VM
boot, the running lifetime and the teardown as one timeline.

Tracing is **off by default**. With it off the daemon constructs no exporter and
dials nothing, so a stock install is byte-for-byte the same as before the feature
existed. Turn it on per node:

```bash
swarmd-firecracker \
  --tracing-enabled \
  --tracing-endpoint http://127.0.0.1:4318
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--tracing-enabled` | `false` | Build the exporter at all |
| `--tracing-endpoint` | — | OTLP/HTTP base URL of a collector; `/v1/traces` is appended |
| `--tracing-service-name` | `swarmd-firecracker` | `service.name` on every exported span |
| `--tracing-sample-ratio` | `1` | Fraction of traces to export, in `(0, 1]` |

The transport is **OTLP/HTTP**. `http://` exports in the clear, which is what a
collector on localhost, a sidecar or a private lab network wants; `https://`
verifies the collector's certificate against the system roots. A path in the
endpoint (`http://host:4318/v1/traces`) is rejected at startup rather than
silently ignored, and an unparseable endpoint fails the daemon instead of leaving
it exporting nothing.

#### What one task looks like

| Span | Covers |
|------|--------|
| `task.lifecycle` | The whole task, from the first prepare to teardown — the trace's root |
| `├─ task.prepare` | Turning the task into a bootable VM definition |
| `│  └─ image.prepare` | OCI pull and rootfs build — usually the longest step |
| `├─ task.start` | Translation, port publishing and the VM boot |
| `│  └─ task.translate` | Building the Firecracker config from the task |
| `├─ task.wait` | The task's running lifetime — this span is as long as the VM lives |
| `├─ task.shutdown` / `task.terminate` | Graceful stop / forced stop |
| `└─ task.remove` | Resource teardown |

One task is **one trace**, even though SwarmKit calls `Prepare`, `Start`, `Wait`
and the teardown separately with a fresh context each time: the root span is
opened on the first phase and the rest parent to it, while the work still runs
under the caller's own context so its cancellation is never lost. Searching a
collector for `task.id` therefore shows the entire life of one task.

Every span carries `task.id`, `service.id`, `node.id`, `node.hostname` and
`image.ref` when the task has them. A span whose operation returned an error is
exported with an error status and the message, so a failed `task.prepare` is
visible as a failure rather than as a fast success.

#### Seeing a trace

Any OTLP/HTTP collector works. A single container is enough to look at one:

```bash
docker run --rm -p 16686:16686 -p 4318:4318 \
  jaegertracing/all-in-one:latest \
  --set receivers.otlp.protocols.http.endpoint=0.0.0.0:4318
```

Start the daemon with `--tracing-enabled --tracing-endpoint http://127.0.0.1:4318`,
run one service, then open `http://localhost:16686` and search for the
`swarmd-firecracker` service. `--tracing-sample-ratio 0.1` makes a busy node
export a tenth of its traces instead.

#### Limitations

- **Spans are per node.** A task's trace is produced entirely by the node running
  it. Operations the manager relays over the API (exec, logs, snapshot, cross-node
  calls) do **not** yet propagate a trace context, so a request that crosses nodes
  appears as two unrelated traces. Propagating `traceparent` over the broker is a
  separate change.
- **Buffered export.** Spans are batched and flushed on exit (bounded to 5 s) or
  when the batch fills. A `kill -9` of the daemon loses the spans still in the
  buffer.
- **No metrics or log pipeline.** This is traces only; metrics stay on the
  Prometheus endpoint and logs stay on the node's console files (see
  [Reading logs across nodes](#reading-logs-across-nodes)).

### Logging

**Log locations:**

| Log | Path | Rotation |
|-----|------|----------|
| swarmd-firecracker (daemon) | `/var/log/swarmcracker/daemon.log` | systemd journal |
| VM console logs | `<state-dir>/<vm-id>.log` (`/var/run/swarmcracker` as root) | Appended across restarts; deleted with the task |
| dnsmasq (DHCP) | `/var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log` | Manual |
| Firecracker stderr | captured by daemon | — |

dnsmasq's per-bridge files — the log, the pid file, the DHCP lease file and the
service-name records — live in `/var/lib/swarmcracker/dnsmasq/`, a root-owned
directory that only root and dnsmasq's own user can write (when that user cannot
be resolved, the directory stays root-owned and only root can). They are
deliberately not in `/tmp`, where a predictable name would let a local user plant a
file the daemon would write through or signal as root.

That path is under the project's state directory because the shipped systemd units
run the daemon with `ProtectSystem=strict`: only the paths those units list in
`ReadWritePaths` are writable, and `/var/lib/swarmcracker` is one of them while
`/run` is not.

Both CLI-created VMs and daemon-managed service tasks mirror their serial
console to `<state-dir>/<vm-id>.log`, so `swarmcracker vm logs` works for either.
The CLI state directory is `/var/run/swarmcracker` when running as root and
`~/.swarmcracker` otherwise.

#### Reading logs across nodes

A console log is a file on the node that ran the VM, so `vm logs <task-id>` reads
it directly and needs no cluster access — that is the single-node path and it
still works with no API, no collector and no extra configuration.

For a task on **another** node, add `--host`: the manager answers the request and
relays it to the node that owns the task, over the same mTLS broker it uses for
`vm exec`.

```bash
# One task, wherever it runs
swarmcracker -H https://manager:4244 --ca ca.crt --token-file token vm logs --tail 50 5f3a1b2c9d

# Every task of a service, merged into one stream
swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs web

# Same, following
swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs -f web
```

`service logs` prefixes every line with a short task ID, because merged output
without attribution is unreadable once a service has more than one replica, and
interleaves streams as they arrive rather than sorting by time — the console log
carries no per-line timestamp. A replica that has not written anything yet is
skipped rather than failing the command, so asking during a rollout still works.
It requires `--host` by construction: only the manager can reach every node's
files.

There is no central log store to operate. The aggregation is a query-time fan-out
from the manager, so a node that is down makes its own tasks' logs unreadable
while the rest keep working, and no shipping agent, buffer or disk quota has to be
sized.

#### Retention

Console logs follow the task's lifetime:

- A VM **restart under the same task ID** appends to the existing file and writes
  a `=== swarmcracker: console restarted at ... (earlier output kept) ===` line,
  so the output before a crash is never lost and a restart is never silent.
- A task that **exits or crashes** keeps its log; that is what a post-mortem
  reads.
- A task SwarmKit **removes** (a scale-down, a rolling update past the history
  limit, `service rm`) takes its log with it, so a long-lived node does not
  accumulate orphans.
- `--since` and `--tail` bound what you read, not what is stored. `--tail -1` asks
  the API for the node's maximum of 10000 lines per task.

A file left behind by a crash of the daemon itself is not tracked by any of this;
prune those by age, for example
`find /var/run/swarmcracker -name '*.log' -mtime +7 -delete`.

**View logs:**
```bash
# Daemon logs
sudo journalctl -u swarmcracker-worker -f

# Specific VM console (CLI-created VM or service task)
swarmcracker vm logs --follow <vm-id>

# Logs for each task in a service (find the task IDs first)
swarmcracker service ps <service-name>
swarmcracker vm logs --follow <task-id>

# Every task of a service, merged, from any machine with an API token
swarmcracker -H https://manager:4244 --ca ca.crt --token-file token service logs -f <service-name>

# dnsmasq DHCP logs
tail -f /var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log
```

**Attach to a VM console:**

`swarmcracker vm attach` connects your terminal to the microVM's serial console
(the same channel the guest kernel and `ttyS0` use). For an image whose command
is an interactive shell (for example `CMD ["/bin/bash"]`), this gives you a shell
inside the VM.

```bash
# Find the task ID (service ps shows a 12-character prefix)
swarmcracker task ls

# Attach using the full ID or any unique prefix
swarmcracker vm attach 5f3a1b2c9d

# Non-default socket directory (must match the daemon's --socket-dir)
swarmcracker vm attach --socket-dir /var/run/firecracker 5f3a1b2c9d
```

Press **Ctrl-P Ctrl-Q** to detach; the VM keeps running. The console socket is
owned by the daemon (`root`, mode `0600`), so run the command as root or with
`sudo`.

From another machine, add the global `-H`, `--ca` and `--token-file` flags:
`swarmcracker -H https://manager:4244 --ca ca.crt --token-file runner.token vm
attach <task>` streams the same console through the manager's API
(`POST /v1/tasks/{id}/attach`, scope `tasks:exec`), so no node access or root is
needed. Ctrl-P Ctrl-Q still detaches and leaves the VM running. That path needs
the node broker and an HTTP/1.1 connection; see the [API
reference](/reference/api/#task-exec-attach-logs-files-and-health).

> **Note:** console attach is available for VMs managed by the daemon (service
> tasks). VMs started in the foreground by `swarmcracker vm create` are not
> attachable, because the CLI process that owns their console exits with the VM.

To run a command or copy a file instead of driving the console, use
[`swarmcracker vm exec` and `swarmcracker vm cp`](/guides/guest-access/).

**Logging:** the daemon has one switch, `--debug`; there is no `--log-level`
flag on `swarmd-firecracker` (that flag belongs to the `swarmcracker` CLI).
Without it the daemon logs at `info`; the `logging.level` key in `config.yaml`
is parsed but does not change the daemon's level.

With `--debug` the daemon also logs the diagnostic detail referenced on this
page, for example an autoscaler pass that holds and the reason it held:
```bash
swarmd-firecracker --debug
```

---

## Placement

`--constraint` and `--placement-pref` are SwarmKit's own scheduling inputs, not
a SwarmCracker extension, so the scheduler evaluates them against
`node.hostname`, `node.role`, `node.labels.*` and the spread preferences; the
grammar and the `node update --label-add` workflow that makes a
`node.labels.<key>` constraint satisfiable are in
[`service create`](/reference/cli/#swarmcracker-service) and
[Architecture Constraints](/guides/advanced/#architecture-constraints).

### When nothing can be placed

The `ERROR` column holds the scheduler's verdict, which is generic and truncated
at 40 characters, so when no task of the listing is placed **and** at least one
unplaced `NEW`/`PENDING` task carries a scheduler error, the command adds one
line after the table:

```text
no task is running: constraint "node.labels.disk==ssd" matches no node (available node labels: disk=hdd, hostname=sc-lab-node1, role=manager, role=worker)
```

That line names the constraint only when the task error carries the constraint
filter's own explanation; otherwise it quotes the scheduler and lists the
declared constraints as possibly not the cause. Both message shapes, and why the
distinction matters, are in the
[CLI reference](/reference/cli/#swarmcracker-service).

Nothing placed but no error either — a task the scheduler has not looked at yet
— is not a refusal: the listing prints no line at all, because there is no
verdict to report.

---

## Autoscaling

A service can keep its own replica count instead of having one fixed number.
The policy is declared per service with `--autoscale`; the **manager** acts on
it, so no node needs to know the policy exists. Only the manager the cluster
reports as leader reconciles: every manager runs the loop, but one pass decides
for the service, so three managers cannot sample the same replicas and arm three
different cooldowns. The gate fails open — a cluster that reports no leader, or
a manager that cannot read the node list, reconciles anyway rather than silently
stopping autoscaling for the whole cluster.

Autoscaling reads in-guest CPU samples through the manager's node broker, which
the programmatic API builds. The API is on by default; `--api-disabled` (or
missing cluster TLS material) turns autoscaling off as well, and the daemon logs
a warning naming that reason at startup.

### Declaring a policy

```bash
swarmcracker service create --name web --image nginx:alpine --replicas 2 \
    --autoscale min=2,max=10,metric=cpu,target=70
```

The service starts at 2 replicas — inside its own bounds, so the first pass
measures it instead of realigning it — aiming at 70 % of one vCPU per replica.

The flag grammar, the six `swarmcracker.autoscale.*` labels the policy is stored
as, the cooldown defaults, the tri-state `service update` behaviour and the
combinations that are refused are in the
[CLI reference](/reference/cli/#swarmcracker-service). This page describes what
the manager does with a stored policy.

### What the loop does

Every 30 seconds the leader lists every service and reconciles the ones whose
labels parse into a policy. For each of those, it reads, for every **running**
replica, the last sample its node recorded — the node's poller asks the guest
agent every 15 seconds, so the manager reads a sample at most one poll interval
old without scraping Prometheus or dialling the guest. Two readings of the same
replica become cores per replica: the change in that replica's cumulative guest
CPU seconds divided by the wall time between the two samples. The mean across the
replicas is compared with the target:

| Mean cores per replica | Result |
|------------------------|--------|
| above `target × 1.1` | Add one replica on the first such sample |
| below `target × 0.9` | Remove one replica, after two consecutive samples below the band |
| between the two | Hold |

A change is always exactly one replica — scaling is linear, so a service far from
its target gains one replica per settled decision rather than jumping to the
target — and it never leaves `[min,max]` or goes below 1. Scale-up reacts on a
single hot sample because a busy service should get capacity while the load is
still there; scale-down needs two consecutive cold samples because a replica that
has just booted reads low while its workload warms up, and one dip during a
scrape should not cost a boot later.

While the cooldown for the direction of the last change is running, nothing
changes in **either** direction. A cold sample still counts toward the
hysteresis while it waits, so a cooldown delays a scale-down rather than
discarding the evidence for it. The defaults are `60s` after a scale-up and
`300s` after a scale-down; a cooldown of `0s` means "never wait in this
direction".

A pass can also end without a load-driven change — in ways that include, but are
not limited to, these:

- **No data.** A replica that has not produced two samples over a positive
  window — still booting, a guest whose agent predates the metrics operation, a
  node that could not be read — contributes nothing. If no replica contributes,
  the pass decides nothing: that is "nothing measured", not "0% busy", so a
  failed probe can never scale a service down on its own. Nothing is published
  either, so the gauge keeps whatever it last published — and has no series at
  all if it never published one (see
  [Watching it](#watching-it)).
- **A pause.** A service at 0 replicas is left alone; restarting a paused
  service is an explicit action.
- **A count outside the policy.** The loop aligns it (see below) instead of
  treating it as a load signal.
- **A patch that did not land.** When the control API rejects the scale —
  `Autoscaler: cannot scale service ...`, or `cannot align service ...` for an
  alignment — the loop logs it and commits nothing: the cooldown is not armed and
  the hysteresis is not reset, so a change that never happened costs one interval,
  not a whole cooldown.
- **An unreplicated service.** A `--mode global` service whose autoscale labels
  were set by hand has no replica count to move; the loop notes it at debug and
  skips it.

Cooldowns, the below-target counters and the per-replica CPU baselines live only
in the running loop's memory. A restarted manager — or a leader change to another
manager — starts without them: its next pass records a baseline and takes no load
decision, and its first scale-down still needs two fresh samples.

A policy typed on the command line or as `--label` is validated before the
service is stored, so a malformed one fails at the command that made it, naming
the offending key and the valid form. The loop re-validates what it reads: a
stored label set that does not parse — written through the API, or by a tool that
did not validate it — makes it log `Autoscaler: service <id> declares an invalid
autoscale policy; skipping it`, with the parser's reason, on every pass, and that
service is never scaled until the labels are fixed.

With the daemon's `--debug` flag, a pass that holds logs the count and the reason
it held, for example `holding: 0.62 cores is within the band around target 0.70`.

### Alignment when a count is outside the policy

A service can be outside its own policy: it ran at 5 replicas and the policy was
later tightened to `min=2,max=3`, or the labels were set by hand. The loop moves
such a service to the nearest bound once and logs the alignment:

```text
Autoscaler: aligned service <id> from 5 to 3 replicas to fit its policy; not counted as a scale event
```

An alignment is not a scale event: it does not increment
`swarmcracker_service_scale_events_total` and does not arm a cooldown, so the
first load decision afterwards still reasons from a clean state. It only runs
while the count is outside the bounds; once inside, every move comes from a
sample.

### Watching it

The manager publishes two series on its own health address (`--health-addr`,
default `127.0.0.1:8080`) — the same `/metrics` endpoint a node exposes, read
from the manager because that is where the loop runs:

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `swarmcracker_service_replicas` | gauge | `service` | Replica count the last decision settled on — written on every decided pass, changed or not, so it is the current count rather than the last scale event |
| `swarmcracker_service_scale_events_total` | counter | `service`, `direction` (`up`/`down`) | Load-driven replica changes that were actually applied; a rejected patch or an alignment does not count |

Both label `service` with the **service ID**, the same value the per-VM series
use, so one query can line up the desired count with what each node measures:

```promql
# Replicas the autoscaler currently wants, per service
swarmcracker_service_replicas

# Scale-ups and scale-downs in the last hour
sum by (service, direction) (increase(swarmcracker_service_scale_events_total[1h]))
```

The per-task CPU series give the other half of the picture: the per-service rate
divided by the replica count is roughly the figure the loop compared with the
target, and both sides carry the service ID, so one query lines them up:

```promql
sum by (service) (rate(swarmcracker_vm_cpu_seconds[5m])) / swarmcracker_service_replicas
```

If a service's replica count does not track its load — the gauge is flat, or
absent — while its `vm_cpu_seconds` rate sits well above or below the target,
read it one of two ways. Either the loop is holding — the gauge is current and
the debug log says why — or the pass never reached a decision because no replica
had two samples yet. Which of those you see depends on whether anything was ever
published for that service:

- In a live manager, a series that was published before and whose later passes
  measured nothing (a replica that has not been running long enough to give two
  samples) keeps the last count the loop published: flat, but stale, not the
  count it would settle on now.
- After a manager restart, and for a service that was only just deployed,
  nothing has been published at all. The gauge is an in-process value created
  when the manager started, so the series is **absent** — the query returns no
  sample rather than a flat number. An empty result means "no decision yet",
  and is not a zero.

The gauge is deleted when a service stops autoscaling — the service is deleted,
or its policy is removed — so a stale count never lingers for a service that no
longer has a policy; the counter is history and stays.

### Pausing a service

`service scale <service> 0`, or `service update --replicas 0`, stops every task
of the service and leaves the policy in place. A paused service is not a service
that drifted below its minimum: the loop publishes the gauge at `0`, takes no
decision, and never starts it again. Scaling back up is the explicit un-pause,
and the count must be inside `[min,max]` like any other explicit count — a
`service scale` to a number outside the policy is refused with both bounds
named, not clamped.

`service inspect` shows the policy while it is paused, so the way back is always
visible:

```text
Autoscale:
  metric: cpu
  target: 70% of one vCPU
  replicas: 2-10
  cooldown: up 1m0s, down 5m0s
```

---

## Common Troubleshooting

### VM Won't Start

**Symptoms:** `vm create` or `service create` returns error.

**Checklist:**

1. **KVM available?**
   ```bash
   ls -la /dev/kvm
   # If missing: modprobe kvm && modprobe kvm-intel (or kvm-amd)
   ```

2. **Firecracker binary?**
   ```bash
   which firecracker
   firecracker --version  # Should be v1.15.1+
   ```

3. **Kernel image?**
   ```bash
   ls -la /usr/share/firecracker/vmlinux
   # Expected: ~25MB ELF kernel
   ```

4. **Rootfs exists?**
   ```bash
   ls -la /var/lib/firecracker/rootfs/
   # Should show .ext4 files for each pulled image
   ```

5. **Bridge exists?**
   ```bash
   ip link show swarm-br0
   # If missing: swarmcracker cluster init (recreates infrastructure)
   ```

6. **Socket directory writable?**
   ```bash
   ls -la /var/run/firecracker/
   # Permissions should be 0755, owned by the daemon user
   ```

7. **Sufficient resources?**
   ```bash
   swarmcracker doctor  # Check memory/CPU available
   ```

### VM Crashes / Exits Immediately

**Symptoms:** VM starts then stops within seconds.

**Checklist:**

1. **Check VM console log:**
   ```bash
   swarmcracker vm logs <vm-id>
   ```

2. **Common causes:**
   - **Kernel panic:** Wrong kernel or missing modules. Check boot args.
   - **Rootfs not found:** Verify path in config.
   - **Init system failure:** Check if tini/dumb-init is in rootfs.
   - **OOM:** VM has insufficient memory. Increase `--memory` / `-m`.
   - **Missing command:** Container image doesn't have the specified command.

3. **Check Firecracker output:**
   ```bash
   journalctl -u swarmcracker-worker | grep <vm-id>
   ```

### Image Pull Fails (Private Registry)

**Symptoms:** a task on a worker stays pending or fails because its image could
not be pulled; the node's daemon log shows `401 Unauthorized` /
`pull access denied` for the image.

The credential is resolved on the **manager** and stored on the service spec, so
a worker never reads its own Docker config. Diagnose on the manager.

1. **Is a credential attached to the service?**
   ```bash
   swarmcracker service inspect <service> | grep -i registry_auth
   # a line showing "registry_auth": "***" means a credential is attached;
   # no matching line at all means the service carries none
   ```
   `***` is the redacted payload — the value is never shown. The key is
   `registry_auth` (the spec's snake_case JSON tag), not `registryAuth`.

2. **No credential attached?** Log in on the manager as the user that runs
   `swarmcracker`, then re-apply it:
   ```bash
   sudo docker login <registry-host>
   swarmcracker service update <service> --image <ref> --with-registry-auth
   ```

3. **Wrong registry attached?** Omitting the flag on `service update` preserves
   the existing credential, so an `--image` in a different registry keeps the
   old registry's payload. Re-resolve for the new image:
   ```bash
   swarmcracker service update <service> --image <new-ref> --with-registry-auth
   ```

4. **The create/update error names the registry and the remedy:**
   ```text
   --with-registry-auth: no registry credentials found for registry.example.com: run `docker login registry.example.com`
   ```

5. **Credential revoked or expired?** Log in again and re-resolve:
   ```bash
   sudo docker login registry.example.com
   swarmcracker service update <service> --image <ref> --with-registry-auth
   ```

6. **`--with-registry-auth=false`** clears the credential; do this only for an
   image the nodes can pull anonymously, or pulls start being denied.

See [Registry Credentials](/guides/configuration/#registry-credentials) for the
credential sources and [Registry
authentication](/reference/cli/#registry-authentication) for the flag semantics.

### Network Issues

#### VMs Can't Reach Internet

1. **NAT enabled?**
   ```bash
   iptables -t nat -L POSTROUTING | grep MASQUERADE
   # Should show rule for 192.168.127.0/24
   ```

2. **IP forwarding?**
   ```bash
   sysctl net.ipv4.ip_forward
   # Should be 1
   ```

3. **DHCP working?**
   ```bash
   cat /var/lib/swarmcracker/dnsmasq/dnsmasq-<bridge>.log | tail -20
   # Should show DHCPOFFER/DHCPACK
   ```

#### Cross-Node VM Communication Fails

1. **VXLAN enabled on both nodes?**
   ```bash
   ip link show | grep vxlan
   # Should show swarm-br0-vxlan
   ```

2. **VXLAN peers correct?**
   ```bash
   swarmcracker network vxlan ls
   # Should list all worker IPs
   ```

3. **UDP 4789 open between nodes?**
   ```bash
   nc -zvu <other-node-ip> 4789
   ```

4. **Bridge FDB entries correct?**
   ```bash
   bridge fdb show dev swarm-br0-vxlan
   ```

5. **Consul registration?**
   ```bash
   consul catalog services
   # Should show swarmcracker-worker
   ```

#### VM Has No IP Address

1. **Check TAP device:**
   ```bash
   ip link show | grep tap-
   ```

2. **Check IP allocator:**
   ```bash
   swarmcracker doctor  # Checks IPAM state
   ```

3. **Static IP with no DHCP fallback?**
   If using static IP mode and the VM expects DHCP, add `ip=dhcp` to kernel args.

### Cluster Issues

#### Worker Can't Join

1. **Connectivity to manager:**
   ```bash
   nc -zv <manager-ip> 4242
   ```

2. **Valid join token?**
   ```bash
   # On manager (prints the worker and manager join tokens)
   swarmcracker cluster token
   ```

3. **Firewall?**
   Ports needed:
   - `4242` (SwarmKit gRPC API)
   - `4789` UDP (VXLAN overlay)
   - `8500` (Consul, if enabled)

4. **Time sync?**
   ```bash
   timedatectl status
   # Clocks must be within a few seconds
   ```

#### Manager Lost Quorum

If you lose 2 of 3 managers:

1. On the remaining manager:
   ```bash
   swarmd-firecracker --manager --force-new-cluster
   ```

2. Rejoin workers:
   ```bash
   # On each worker
   swarmcracker cluster join --token <new-token> <manager-ip>:4242
   ```

### Performance Issues

#### VMs Are Slow

1. **Check host CPU pressure:**
   ```bash
   top -b -n1 | head -20
   vmstat 1 5
   ```
   Attribute load with the per-task series: `swarmcracker_vm_cpu_seconds` tells you
   which VM is burning CPU (read it with `rate()`; it is sampled from the guest
   every 15 s), and `swarmcracker_vm_memory_bytes` tells you whether it is the
   workload or the host that is under pressure.

2. **Check memory pressure:**
   ```bash
   free -h
   swarmcracker doctor  # Shows available memory
   ```

3. **Disk I/O bottleneck?**
   - Use `block` driver instead of `dir` for database workloads
   - Check rootfs is on fast storage (SSD/NVMe)

4. **Network throughput?**
   - VXLAN adds ~50 bytes overhead per packet
   - For overlay networks, MTU is set to 1450 automatically

---

## Backup and Restore

### VM Snapshots

```bash
# Create snapshot of running VM
swarmcracker vm snapshot create <task-id>

# List snapshots
swarmcracker vm snapshot list --task <task-id>

# Restore from snapshot
swarmcracker vm snapshot restore <snapshot-id>
```

### Configuration Backup

```bash
# Backup config directory
tar czf swarmcracker-config-$(date +%Y%m%d).tar.gz /etc/swarmcracker/

# Backup state (includes certs, tokens, task state)
tar czf swarmcracker-state-$(date +%Y%m%d).tar.gz /var/lib/swarmcracker/
```

### Volume Backup

```bash
# Backup a volume
tar czf volume-<name>-$(date +%Y%m%d).tar.gz /var/lib/swarmcracker/volumes/<name>/
```

### Full Node Backup

```bash
#!/bin/bash
# Full backup script
BACKUP_DIR="/backup/swarmcracker/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# Config
cp -r /etc/swarmcracker "$BACKUP_DIR/config"

# State (stop the daemon first if possible)
systemctl stop swarmcracker-worker   # or swarmcracker-manager on a manager node
cp -r /var/lib/swarmcracker "$BACKUP_DIR/state"
systemctl start swarmcracker-worker

# Rootfs images
cp -r /var/lib/firecracker/rootfs "$BACKUP_DIR/rootfs"

# Volumes
cp -r /var/lib/swarmcracker/volumes "$BACKUP_DIR/volumes"

# Compress
tar czf "$BACKUP_DIR.tar.gz" -C "$(dirname "$BACKUP_DIR")" "$(basename "$BACKUP_DIR")"
rm -rf "$BACKUP_DIR"
```

---

## Cluster Upgrade

### Rolling Upgrade (Zero Downtime)

1. **Drain a worker:**
   ```bash
   swarmcracker node drain worker-1
   ```

2. **Wait for all VMs to move:**
   ```bash
   swarmcracker task ls --node worker-1
   # Should show no running tasks
   ```

3. **Upgrade the worker:**
   ```bash
   # On worker-1
   systemctl stop swarmcracker-worker
   # Deploy new binary
   cp new-swarmd-firecracker /usr/local/bin/
   systemctl start swarmcracker-worker
   ```

4. **Activate the worker:**
   ```bash
   swarmcracker node activate worker-1
   ```

5. **Verify health:**
   ```bash
   swarmcracker node ls | grep worker-1
   # Should show Ready, Active
   ```

6. **Repeat for remaining workers, then managers.**

### Manager Upgrade

Managers must be upgraded one at a time:

1. **Verify quorum:**
   ```bash
   swarmcracker node ls | grep manager
   # Need 2+ managers healthy for quorum
   ```

2. **Drain and upgrade:**
   ```bash
   # Same as worker upgrade, but re-initialize if needed
   swarmcracker cluster leave
   # Upgrade binary, then re-join
   swarmcracker cluster join --token <token> <leader-ip>:4242
   ```

---

## Resource Management

### Capacity Planning

| Per-VM Overhead | Value |
|----------------|-------|
| Firecracker process | ~50 MB RSS |
| TAP device | Negligible |
| Bridge + VXLAN | ~5 MB kernel memory |
| dnsmasq (shared) | ~10 MB RSS |
| State tracking | ~5 MB per VM |

**Formula:**
```
Available VMs = (Total_RAM - System_Reserved - 200MB) / (VM_Size + 50MB)
```

Example for a 16GB worker with 512MB VMs:
```
(16GB - 2GB - 0.2GB) / (0.512GB + 0.05GB) = 24.5 → 24 VMs max
```

### Scaling Up

```bash
# Add workers
# 1. Provision new node with Firecracker + kernel
# 2. Get join token from manager
swarmcracker cluster token worker

# 3. On new worker
swarmcracker cluster join <manager-ip>:4242 --token SWMTKN-1-xxx

# 4. Verify
swarmcracker node ls
# Should show new node as Ready, Active
```

### Scaling Down

```bash
# Drain worker
swarmcracker node drain worker-3

# Wait for VMs to reschedule
swarmcracker task ls --node worker-3

# Leave cluster
swarmcracker cluster leave

# Clean up
swarmcracker cluster reset
```

### Memory Balloon

`--balloon-mib <n>` makes the node reclaim up to `n` MiB of a VM's memory from
boot, regardless of host pressure, while the **guest** can take that memory back
when *it* comes under pressure instead of being OOM-killed. Two operational
points matter:

- **It is set at boot, not at runtime.** A running VM cannot gain a balloon, so
  `service update --balloon-mib <n>` rolls the task with the new target and
  `--balloon-mib 0` removes it.
- **The guest must cooperate.** The guest takes memory back through
  `deflate_on_oom`, and that only works if the guest kernel ships the
  virtio-balloon driver; otherwise the device exists but the guest's memory is
  unchanged.

See [Memory Balloon](/guides/advanced/#memory-balloon) for units, validation, the
`swarmcracker.balloon` label, and the metric.

---

## Security Operations

### Certificate Rotation

SwarmCracker uses SwarmKit's built-in mutual TLS, and node certificates are
rotated automatically by SwarmKit. There is no CLI command to force a
rotation: `swarmcracker cluster token` only displays join tokens.

### Secret Management

SwarmCracker does not currently expose secret management through its CLI:
neither `swarmcracker` nor `swarmctl` has a `secret` command, and
`swarmcracker service create` has no `--secret` flag. Distribute sensitive
data through your image build or a mounted volume until secret support lands.

### Firewall Rules

Minimum required ports:

| Port | Protocol | Source | Destination | Purpose |
|------|----------|--------|-------------|---------|
| 4242 | TCP | All nodes | Managers | SwarmKit gRPC |
| 4789 | UDP | All workers | All workers | VXLAN overlay |
| 8500 | TCP | All nodes | Consul nodes | Service discovery |
| 4242/tcp | TCP | Admin | Managers | CLI access |

```bash
# Example iptables rules
iptables -A INPUT -p tcp --dport 4242 -s 192.168.1.0/24 -j ACCEPT
iptables -A INPUT -p udp --dport 4789 -s 192.168.1.0/24 -j ACCEPT
```

---

## Routine Maintenance

### Daily
- [ ] Check node health: `swarmcracker doctor`
- [ ] Check running VMs: `swarmcracker vm list`
- [ ] Check disk space: `df -h /var/lib/firecracker/rootfs`

### Weekly
- [ ] Run image cleanup: check `/var/log/swarmcracker/daemon.log` for "Periodic cleanup completed"
- [ ] Check for orphaned VMs: review daemon logs for "Found orphaned VM"
- [ ] Review metrics trends
- [ ] Check available disk for snapshots: `du -sh /var/lib/firecracker/snapshots/`

### Monthly
- [ ] Test backup restoration
- [ ] Review and update firewall rules
- [ ] Check for SwarmCracker updates
- [ ] Rotate Consul tokens (if using ACLs)
- [ ] Verify VXLAN cross-node connectivity

---

## Emergency Procedures

### Node Failure

**Worker failure:**
1. Drain the failed node: `swarmcracker node drain <worker>`
2. SwarmKit reschedules VMs to other workers
3. Replace hardware, reprovision, rejoin

**Manager failure (1 of 3):**
- Cluster continues operating. Replace the failed manager.

**Manager failure (2 of 3 — loss of quorum):**
1. On the surviving manager: `swarmd-firecracker --manager --force-new-cluster`
2. Replace failed managers, join as followers
3. Rejoin workers

### Full Cluster Recovery

1. Stop all `swarmd-firecracker` processes
2. Restore from backup on the designated manager node
3. Start with `--force-new-cluster`
4. Restore workers from backups, rejoin one at a time
5. Verify: `swarmcracker node ls` should show all nodes Ready

### Disk Full

1. **Immediate:** Delete old snapshots
   ```bash
   swarmcracker vm snapshot cleanup --max-age 168h
   ```

2. **Short-term:** Remove unused rootfs images
   ```bash
   # The rootfs directory holds the per-image cache (<image>-<tag>.ext4, never
   # booted); each task's private copy lives in its tasks/ subdirectory and is
   # deleted when the task or VM goes away.
   ls -lh /var/lib/firecracker/rootfs/ /var/lib/firecracker/rootfs/tasks/
   ```
   The periodic cleanup only reaps a copy whose **access time** is old, so a
   running VM's rootfs is normally left alone — but on a filesystem mounted
   `noatime` (or for a task running longer than the retention window) an old copy
   can be reaped while its VM still runs. The VM keeps working from the open file;
   a restart or a snapshot of it then fails.

3. **Long-term:** Configure snapshot retention. (Image cleanup already runs
   automatically every 24 hours, removing rootfs images older than 7 days.)
   ```yaml
   snapshot:
     max_snapshots: 10
     max_age: 168h  # 7 days
   ```

---

## Configuration Reference

See [Configuration Guide](/guides/configuration/) for all config keys and defaults.

---

## Quick Reference Card

```bash
# Health
swarmcracker doctor                     # Node health check
curl 127.0.0.1:8080/healthz             # API health check

# Cluster
swarmcracker node ls                    # List nodes
swarmcracker node inspect <node>        # Node details
swarmcracker cluster token worker       # Get a worker join token

# VMs
swarmcracker vm list                    # List VMs (CLI-created + service tasks)
swarmcracker vm status <vm-id>          # VM details (service task IDs work too)
swarmcracker vm logs -f <vm-id>         # Follow VM logs (CLI-created VMs and service tasks)
swarmcracker vm attach <vm-id>          # Attach to the VM serial console
swarmcracker vm exec <vm-id> -- <cmd>   # Run a command in the guest (see Guest Access guide)
swarmcracker vm cp <file> <vm-id>:<dst> # Copy a file in/out of the guest
swarmcracker vm stop <vm-id>            # Stop a CLI-created VM
swarmcracker cluster status             # Cluster overview (nodes/services/tasks)
swarmcracker service scale <service> 0  # Stop a service's tasks (scale to zero)

# Services
swarmcracker service ls                 # List services
swarmcracker service ps <service>       # Service tasks

# Snapshots
swarmcracker vm snapshot create <vm-id> # Snapshot VM
swarmcracker vm snapshot list           # List snapshots
swarmcracker vm snapshot restore <snap> # Restore from snapshot

# Network
swarmcracker network vxlan ls           # VXLAN peers

# Recovery
swarmcracker cluster reset --hard       # Reset node
swarmcracker cluster leave              # Leave cluster
```

---

**See Also:** [Architecture Overview](/architecture/) | [Configuration Guide](/guides/configuration/) | [Networking Guide](/guides/networking/) | [Security Guide](/guides/security/)
