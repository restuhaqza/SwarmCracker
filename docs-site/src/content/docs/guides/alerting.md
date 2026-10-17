---
title: "Alerting"
description: "Turn on SwarmCracker's default Prometheus alert rules, deliver them to a real sink, and verify the whole path end to end."
---

> Alerting ships with the monitoring stack in `infrastructure/observability/`.
> This page documents the default rules, the notification sink, how the rules
> are wired to Prometheus, and how to prove an alert actually reaches the sink.

---

## Overview

SwarmCracker exposes Prometheus metrics on each node's health server (see
[Metrics](/guides/operations/#metrics)). On top of those series the repository
ships a small, default rule set and an Alertmanager:

| Piece | File |
|-------|------|
| Rule set | `infrastructure/observability/prometheus/alerts.yml` |
| Prometheus config | `infrastructure/observability/prometheus/prometheus.yml` |
| Alertmanager config | `infrastructure/observability/alertmanager/alertmanager.yml` |
| Stack | `infrastructure/observability/docker-compose.yml` (prometheus, alertmanager, grafana) |

Every `expr` in `alerts.yml` reads a series this repository actually exports.
That is enforced by `pkg/metrics/alertrules_test.go`, which gathers the default
Prometheus registry and fails any rule that names a metric the registry does not
carry, filters on a `state` value the daemon never publishes, or names a scrape
`job` `prometheus.yml` does not define.

## The default rules

The group is `swarmcracker`. Each rule below reads one of the nine written
series in [Metrics](/guides/operations/#metrics); the rules that used to read
series this repo never exports were removed (see
[Removed rules](#removed-rules)).

| Alert | Reads | Condition | `for:` | Severity |
|-------|-------|-----------|--------|----------|
| `SwarmCrackerNodeDown` | `up{job="swarmcracker"}` | `== 0` | 2m | critical |
| `SwarmCrackerVMBootFailureLoop` | `swarmcracker_vm_boot_errors_total` | `increase(...[15m]) > 3` | 5m | warning |
| `SwarmCrackerVMCrashLoop` | `swarmcracker_vm_crashes_total` | `increase(...[15m]) >= 3` | 5m | critical |
| `SwarmCrackerTaskHealthcheckFailing` | `swarmcracker_vm_health{state="failing"}` | `> 0` | 30s | warning |
| `SwarmCrackerTaskUnhealthy` | `swarmcracker_vm_health{state="unhealthy"}` | `> 0` | 0s | warning |
| `SwarmCrackerDiskWarning` | `swarmcracker_disk_usage_bytes / swarmcracker_disk_total_bytes` | `> 0.8` | 10m | warning |
| `SwarmCrackerDiskCritical` | `swarmcracker_disk_usage_bytes / swarmcracker_disk_total_bytes` | `> 0.9` | 5m | critical |

### Failure mode 1 — VMs that never reach running

`swarmcracker_vm_boot_errors_total{instance,reason}` is written in exactly one
place: the boot path, when the VMM fails to start the VM. The `reason` is always
`boot_failure`. A VM that failed to boot was never counted as running, so this
path deliberately does **not** move `swarmcracker_vms_running` or the crash
counter. `SwarmCrackerVMBootFailureLoop` fires on more than three boot failures
in fifteen minutes, held for five.

### Failure mode 2 — VMs that fail after reaching running (crash loop)

`swarmcracker_vm_crashes_total{instance,service,reason}` is written on the
post-start paths only, with one of four `reason` values:

| `reason` | When |
|----------|------|
| `unexpected_exit` | `Wait` returned an error the VM process did not explain |
| `unhealthy` | the healthcheck exhausted its retries and the monitor force-stopped the VM |
| `exit_error` | the task returned a non-zero exit code |
| `force_terminate` | the task was force-terminated (an abnormal shutdown) |

A crash decrements `swarmcracker_vms_running`, because the VM *was* counted when
it started. `SwarmCrackerVMCrashLoop` fires on three or more crashes in fifteen
minutes for the same `instance`/`service`/`reason`, held for five.

### Failure mode 3 — a task whose healthcheck keeps failing

`swarmcracker_vm_health{instance,service,state}` is a one-hot gauge over the four
`pkg/runtime` health states — `starting`, `healthy`, `failing`, `unhealthy` —
published from the single place health state changes (`publishHealth`). When a
task is torn down, all four series for it are **deleted**, so a stale verdict
cannot fire an alert that never resolves.

The rule watches `state="failing"`, not the terminal `state="unhealthy"`. That is
deliberate and is the difference between a rule that fires and one that cannot:

- While a probe keeps failing but the retries threshold has not been reached, the
  task is still `RUNNING` and the daemon publishes `failing` on every failed
  probe. The gauge stays at `1` continuously between probes, so it is the
  sustained signal.
- When the threshold is reached the monitor publishes `unhealthy` and then
  immediately force-stops the VM; the teardown choke point clears all four
  health series seconds later. A rule with a real `for:` on that terminal state
  would always resolve before it could fire.

At the CLI defaults (`--health-interval 30s`, `--health-retries 3`) the failing
streak covers `(retries-1) x interval = 60s`, and `for: 30s` means "at least two
consecutive failed probes" — so
`SwarmCrackerTaskHealthcheckFailing` fires about one interval before the VM is
force-stopped, while the task is still alive and reschedulable, then resolves as
soon as the state flips back to `healthy` or the task is torn down.
`global.evaluation_interval` is `15s` in `prometheus.yml` (the built-in default
is `1m`) so that 30s window is actually observed; the guard test pins
`evaluation_interval <= the shortest rule for:`.

`SwarmCrackerTaskUnhealthy` is a best-effort companion with `for: 0s`. It exists
so the terminal verdict is still visible if the stop path ever stalls; in normal
operation the series is gone before a scrape and an evaluation can land on it.
The rule that fires is `SwarmCrackerTaskHealthcheckFailing`.

### Failure mode 4 — disk pressure on a SwarmCracker path

`swarmcracker_disk_usage_bytes{instance,path}` and
`swarmcracker_disk_total_bytes{instance,path}` are published together for each
configured path that exists — `path` is `rootfs`, `snapshots` or `state` (from
`rootfs_dir`, `snapshot.snapshot_dir` and `state_dir`). The sampler runs at
daemon start and on the five-minute ticker. A path that is unset, missing or
unreadable produces **no series** rather than a misleading zero, so the ratio is
never computed against a directory the daemon does not own.

That is why the disk rules are a ratio (`usage / total > 0.8` and `> 0.9`)
rather than an absolute byte threshold: the two series are published together
and always describe the same filesystem.

## How the rules are wired

`infrastructure/observability/prometheus/prometheus.yml`:

```yaml
global:
  evaluation_interval: 15s

rule_files:
  - alerts.yml            # mounted at /etc/prometheus/alerts.yml

alerting:
  alertmanagers:
    - static_configs:
        - targets:
            - alertmanager:9093   # the compose service, same monitoring network

scrape_configs:
  - job_name: swarmcracker
    scrape_interval: 15s
    scrape_timeout: 5s
    static_configs:
      - targets: []       # intentionally empty — add your nodes
        labels:
          cluster: production
          env: prod
```

`docker-compose.yml` mounts both files into the Prometheus container. Rule files
are resolved relative to `prometheus.yml`, which is why `rule_files` is the bare
`alerts.yml`.

## Pointing the stack at a cluster

The `swarmcracker` scrape job ships with **empty** `targets`, and the placeholders
that used to live there (`manager1..3`, `worker1..3`) were commented out: they are
not services in the compose file, so a stock `docker compose up` could never scrape
them and `SwarmCrackerNodeDown` would fire forever for all six.

Each node's daemon serves `/metrics` on its health server
(`swarmd-firecracker --health-addr`, default `127.0.0.1:8080`). The default bind
is node-local and is **not** reachable from the Prometheus container, so bind the
daemon to an address the container can route to on every node you want to scrape
— the node's LAN address, or `0.0.0.0:8080`. Then list one `host:port` per node:

```yaml
    static_configs:
      - targets:
          - manager1:8080
          - manager2:8080
          - manager3:8080
          - worker1:8080
          - worker2:8080
          - worker3:8080
        labels:
          cluster: production
          env: prod
```

Reload without a restart (the compose command enables the lifecycle endpoint):

```bash
curl -X POST http://localhost:9090/-/reload
```

**Single-node / local run.** Inside the compose network, `127.0.0.1` is the
Prometheus container itself, so a daemon on the host must be reached by another
name. Either add `extra_hosts: ["host.docker.internal:host-gateway"]` to the
`prometheus` service and target `host.docker.internal:8080`, or use the
monitoring network's gateway
(`docker network inspect monitoring -f '{{(index .IPAM.Config 0).Gateway}}'`).
Use a bare compose service name only when a service by that name actually runs on
the monitoring network.

## The notification sink

The default route in `alertmanager/alertmanager.yml` sends every alert to a
loopback webhook:

```yaml
route:
  receiver: default-webhook
  group_by: [alertname, instance]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h

receivers:
  - name: default-webhook
    webhook_configs:
      - url: http://127.0.0.1:9095/swarmcracker-alerts
        send_resolved: true
```

Nothing listens on `127.0.0.1:9095` unless you start a listener yourself. That is
deliberately safe as a default — loopback-only, no credentials, no third party,
and unlike a real sink it cannot leak cluster metadata. It also means alerts are
visible before you wire up Slack or email: `docker compose logs alertmanager`
shows every notification and the POST attempts.

**To use a real sink**, comment out the `default-webhook` receiver, uncomment the
Slack or email block (both are placeholders — fill in the webhook URL, channel,
relay and addresses), point `route.receiver` at the receiver's `name`, and
reload Alertmanager:

```bash
docker compose exec alertmanager kill -HUP 1
```

The guard test only requires that the routed receiver exists in `receivers` and
that its webhook endpoint is non-empty; it does not read your credentials.

## Verify it

The recipe below forces each signal, watches the alert mature in Prometheus, and
shows it reaching the sink. It needs a node whose daemon is scraped (see
[Pointing the stack at a cluster](#pointing-the-stack-at-a-cluster)).

### 0. Check the configuration before starting

Inside the Prometheus image, `promtool` validates both files:

```bash
docker compose run --rm prometheus promtool check config /etc/prometheus/prometheus.yml
docker compose run --rm prometheus promtool check rules  /etc/prometheus/alerts.yml
```

Both must print `SUCCESS`. If you are on a host without the image (or without
Docker), this step is unavailable — see
[What is not machine-verified](#what-is-not-machine-verified).

### 1. Start the stack

```bash
cd infrastructure/observability
docker compose up -d
```

- Prometheus UI: <http://localhost:9090>
- Alertmanager UI: <http://localhost:9093>

Check Prometheus loaded the rules and is scraping the node:
`http://localhost:9090/rules` shows the `swarmcracker` group;
`http://localhost:9090/targets` shows the `swarmcracker` job `UP`. If it is
`DOWN`, the daemon is not bound to an address the container can reach — fix the
`--health-addr` bind first.

### 2. Force a crash loop (failure mode 2)

Run a task that boots, reaches running, then exits with an error, restarting on
every failure. Replace `--restart-condition` with the default (`any`) if needed:

```bash
swarmcracker service create --name crashdemo --image busybox:latest --replicas 1 \
  --command /bin/sh --args "-c,echo booted; sleep 3; exit 1" \
  --restart-condition any
```

Each restart records one `swarmcracker_vm_crashes_total` (reason `unexpected_exit`
or `exit_error`). Watch it climb:

```bash
curl -s http://127.0.0.1:8080/metrics | grep swarmcracker_vm_crashes_total
```

After three crashes in fifteen minutes, `SwarmCrackerVMCrashLoop` is `Pending`
for five minutes and then `Firing` at <http://localhost:9090/alerts>. Clean up:

```bash
swarmcracker service rm crashdemo
```

### 3. Force a failing healthcheck (failure mode 3)

A service with a healthcheck that always fails — but which does not exit — stays
`RUNNING` while the failing streak builds:

```bash
swarmcracker service create --name healthdemo --image busybox:latest --replicas 1 \
  --command /bin/sh --args "-c,while true; do sleep 3600; done" \
  --health-cmd "exit 1"
```

With the defaults (`--health-interval 30s`, `--health-retries 3`), the daemon
publishes `swarmcracker_vm_health{state="failing"} 1` from the first failed probe.
Verify:

```bash
curl -s http://127.0.0.1:8080/metrics | grep swarmcracker_vm_health
```

`SwarmCrackerTaskHealthcheckFailing` goes `Pending` after the first failed probe
and `Firing` after `for: 30s` (about one interval) — while the task is still
alive. About one interval later the retries are exhausted and the VM is
force-stopped; the health series are cleared and the alert resolves.
`SwarmCrackerTaskUnhealthy` fires only if a scrape and an evaluation catch the
brief terminal state. Clean up:

```bash
swarmcracker service rm healthdemo
```

### 4. Force disk pressure (failure mode 4)

The disk sampler only reports paths that exist, so point one at a small,
fillable filesystem. A `tmpfs` of fixed size makes the ratio easy to control:

```bash
# On the node, create a 256 MiB tmpfs and fill it past 80%.
sudo mkdir -p /mnt/sc-disk
sudo mount -t tmpfs -o size=256M tmpfs /mnt/sc-disk
sudo dd if=/dev/zero of=/mnt/sc-disk/fill bs=1M count=220 status=none
```

Point the daemon at it, for example as the snapshot directory, and restart the
daemon with a config that sets `snapshot.snapshot_dir: /mnt/sc-disk` (or point
`rootfs_dir`/`state_dir` at it). On the next five-minute tick the sampler
publishes:

```bash
curl -s http://127.0.0.1:8080/metrics | grep 'swarmcracker_disk_\(usage\|total\)_bytes'
```

The ratio is over 0.8, then over 0.9 after another `dd`. `SwarmCrackerDiskWarning`
fires after ten minutes, `SwarmCrackerDiskCritical` after five. Clean up:

```bash
sudo rm -f /mnt/sc-disk/fill
sudo umount /mnt/sc-disk
```

### 5. See the alert reach the sink

The default sink is the loopback webhook described above, and nothing listens on
it. To watch a delivery land, run a throwaway listener that shares the
Alertmanager container's network namespace and answers `200`:

```bash
docker run --rm --network container:swarmcracker-alertmanager python:3-alpine \
  python -c '
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        print("ALERT:", self.rfile.read(n).decode()[:400], flush=True)
        self.send_response(200); self.end_headers(); self.wfile.write(b"ok")
HTTPServer(("127.0.0.1", 9095), H).serve_forever()'
```

With that process running, the next notification is printed as
`ALERT: {...}` — the Alertmanager webhook payload for the firing alert. Without
it, `docker compose logs alertmanager` still shows each POST attempt and its
failure, which is enough to prove the rule fired and Alertmanager tried to
deliver.

To verify the whole path once, point `route.receiver` at a receiver you control
(step in [The notification sink](#the-notification-sink)) and confirm the message
arrives there.

## Removed rules

The earlier rule file referenced series this repository never exported. A rule on
a series no writer produces is inert — it can never fire, and it hides the fact
that the signal is missing. These were removed rather than left looking
functional:

| Removed rule | Series it read — never exported (no writer, no exporter in this stack) |
|--------------|--------------------------------------------------------------------------|
| `SwarmCrackerVXLANPartition`, `SwarmCrackerVXLANPeerLost` | `swarmcracker_vxlan_peers`, `swarmcracker_vxlan_expected_peers` |
| `SwarmCrackerDiskWarning`, `SwarmCrackerDiskCritical` (old `expr`) | `node_filesystem_avail_bytes`, `node_filesystem_size_bytes` (no node-exporter in this stack) |
| `SwarmCrackerConsulDown` | `consul_health_node_status` (no Consul exporter in this stack) |
| `SwarmCrackerManagerUnhealthy`, `SwarmCrackerNoManager` | `swarmcracker_manager_health` |
| `SwarmCrackerRaftUnhealthy` | `swarmcracker_raft_health` |
| `SwarmCrackerVMCountLow` | `swarmcracker_vms_expected` |

Two rules read a series that *did* exist but changed meaning, so they were
rewritten rather than removed:

- `SwarmCrackerVMBootFailure` and `SwarmCrackerVMBootFailureHigh` read
  `swarmcracker_vm_boot_errors_total`, which was once written by an overloaded
  helper (`RecordVMCrashed`) on both the boot *and* every post-start path, and
  which also decremented `swarmcracker_vms_running` — the bug that let the gauge
  go negative. The boot and crash signals are now split, and the pair is replaced
  by `SwarmCrackerVMBootFailureLoop`.
- `SwarmCrackerDiskWarning` and `SwarmCrackerDiskCritical` are kept by name but
  their `expr` now use the `swarmcracker_disk_*` series instead of the missing
  `node_filesystem_*`.

`SwarmCrackerNodeDown` was kept and reads `up`, which Prometheus synthesises for
every scrape target.

## What is not machine-verified

The repository's verification of this rule set is the Go guard test
`pkg/metrics/alertrules_test.go` plus the docs pipeline. One thing cannot be
checked there:

- **`promtool` was not run.** The Linux test server has no `promtool` binary, and
  the compose stack was not started as part of this change, so the YAML was not
  validated with `promtool check config` / `promtool check rules` and no alert
  was observed reaching a live sink. The guard test checks the *content* of the
  rules (metric names against the default registry, `state`/`job` label values,
  the evaluation interval against the shortest `for:`, the `rule_files` and
  Alertmanager wiring, and that the routed receiver delivers somewhere) and parses
  the YAML with the Go YAML parser — but it does not parse PromQL. **Step 0 above
  is the missing check; run it where the Prometheus image is available.**

## See also

- [Metrics](/guides/operations/#metrics) — every series and which ones are live.
- [Disk Full](/guides/operations/#disk-full) — what to do when a disk rule fires.
- [Container Healthchecks](/guides/operations/#container-healthchecks) — the
  states and the retries behaviour the health rules depend on.
- [Disaster Recovery](/guides/disaster-recovery/#scenario-1-worker-node-failure) —
  runbook for `SwarmCrackerNodeDown` and the boot-failure rule.
