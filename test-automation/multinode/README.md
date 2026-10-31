# Multi-node SwarmCracker lab

Reusable automation for standing up a **multi-node SwarmCracker cluster with the
VXLAN overlay** and smoke-testing cross-host microVM networking.

It creates `N` nested Ubuntu VMs on a single KVM/libvirt host, provisions the
Firecracker executor on each, forms a SwarmKit cluster, and verifies that a
microVM scheduled on one node is reachable from the other nodes.

This is the reproducible form of that flow, automated by
[`cluster-lab.sh`](cluster-lab.sh).

## Why this exists

The older `test-automation/*.sh` cluster scripts were legacy — hard-coded
`/home/kali/...` paths, old `swarmd-manager`/`swarmd-worker` unit names, and
Vagrant-only — and have been removed. Ansible (`infrastructure/ansible/`) remains
as the advanced/production path but requires you to supply the hosts.

This script is self-contained and creates a working multi-node cluster on a
single modern KVM host.

## Prerequisites (on the host)

- Linux with KVM (`/dev/kvm`) and **nested virtualization** enabled
  (`cat /sys/module/kvm_intel/parameters/nested` → `Y`).
- `libvirt` + `virsh`, `virt-install`, `qemu-img`, `genisoimage` (or `mkisofs`),
  `python3`, `rsync`, `curl`, Go 1.26+.
- Host runtime assets used to seed the guests, produced by running the installer
  once on the host:

  ```bash
  sudo swarmcracker setup install --download-kernel --download-rootfs --download-cni
  ```

  Expected: `/usr/local/bin/firecracker` (+`jailer`), `/usr/share/firecracker/vmlinux`,
  `/var/lib/firecracker/rootfs/*.ext4`, `/opt/cni/bin/*`.

## Usage

```bash
# One-shot: build binaries, fetch base image, create 2 VMs, provision,
# form the cluster and print status.
sudo test-automation/multinode/cluster-lab.sh up 2

# Or step by step
sudo test-automation/multinode/cluster-lab.sh build
sudo test-automation/multinode/cluster-lab.sh image
sudo test-automation/multinode/cluster-lab.sh create 3
sudo test-automation/multinode/cluster-lab.sh provision
sudo test-automation/multinode/cluster-lab.sh cluster
sudo test-automation/multinode/cluster-lab.sh test
sudo test-automation/multinode/cluster-lab.sh ingress
sudo test-automation/multinode/cluster-lab.sh three-tier
sudo test-automation/multinode/cluster-lab.sh autoscale

# Inspect / enter / tear down
sudo test-automation/multinode/cluster-lab.sh status
sudo test-automation/multinode/cluster-lab.sh ssh 2
sudo test-automation/multinode/cluster-lab.sh destroy
```

`cluster` is safe to re-run: it deinitializes an existing manager (purging its
state and removing the bridge it created) before initializing a new cluster, so
`up` and `cluster` no longer need a manual `cluster deinit` first.

`provision` seeds each guest with the host's cached rootfs images (to skip a
rebuild) **and their guest-version markers**: the daemon only trusts a cached
image whose marker matches its own guest build, so seeding an image without its
marker can leave a stale one looking current.

`test` deploys one replica of `nginx:alpine` per node and prints a
from-node → to-node matrix of `ping` and `HTTP` results. A healthy cluster shows
`ping=OK http=200` for **remote** microVMs, not just the local one.

`ingress` exercises the ingress routing mesh end to end: it publishes a service
and asserts that **every node** serves the published port (fan-out), that scaling
to a single replica keeps every node serving (failover), that UDP datagrams are
DNAT'd on every node (rule counters advance), and that removing the services
clears the rules everywhere. It needs `curl` and, for the UDP check, `nc` on the
host.

`three-tier` is the roadmap epic's acceptance test: it deploys `web` (published
on the ingress mesh), `api` and `db` (with a secret and a `--command` whose arg
contains a space), waits for every replica to be healthchecked into `RUNNING`,
then asserts that (1) the published port answers on every node, (2) a `web`
guest resolves `api` **by service name** and gets its page, (3) the secret exists
inside the `db` guest at the requested path with mode `0400`, (4) PID 1 runs the
overridden command, and (5) a task whose healthcheck never passes never reaches
`RUNNING`. It needs `curl` on the host and leaves the three services running for
inspection.

`autoscale` is the reproducible form of the autoscaling acceptance test, which
used to be a recorded manual run: it deploys one replica of `alpine:3.20` whose
workload burns CPU until `/tmp/stop` exists, under
`--autoscale min=1,max=3,metric=cpu,target=40,cooldown-up=30s,cooldown-down=60s`,
and asserts that (1) the count reaches 3 on the autoscaler's own decision and
all three replicas boot, (2) the manager's
`swarmcracker_service_scale_events_total{direction="up"}` counter increased —
read from the manager's `--health-addr` over ssh, for this service's own series
and as a sum over every service, (3) writing `/tmp/stop` inside every replica
brings the count back to 1 and moves the `down` counter, and (4) a manual
`service scale 0` stays at 0 for 95 s (three 30 s autoscaler passes) with this
service's own counter unmoved. Then it removes the service.

Both counter assertions poll instead of trusting one scrape, because the event is
emitted asynchronously, right after the replica patch. A single read is retried
every 2 s for `LAB_AUTOSCALE_READ_DEADLINE` (20 s) when it is not the manager's
complete exposition — curl failed, the body is empty, it has no `swarmcracker_`
lines, or it never reaches the last family (`swarmcracker_vxlan_peers`), which is
how a cut-off body is told from a quiet manager — and **also** when the
exposition is complete but has no scale-event family yet (a `CounterVec` with no
children renders nothing at all; that case is read as `0` only for the
pre-create baseline). The assertion then keeps polling every 5 s for up to
`LAB_AUTOSCALE_EVENT_DEADLINE` (45 s) until the counter has crossed its baseline.
No reading is ever read as "0 events"; only a deadline turns it into a failure,
and the message carries curl's exit status, the body's size, its `swarmcracker_`
line count and whether the exposition was complete. A slow run therefore looks
like a run of

```
  5s  up: unreadable, retrying: http://127.0.0.1:8080/metrics on node 1 ...
 10s  up: sum=2 (want 1) this service=2 (want 1)
```

lines before the `PASS`, and it can sit there for up to 45 s per counter without
being broken. The pause window counts only readable reads, and fewer than two of
them is a `FAIL`, so neither assertion can pass on nothing.

Its runtime is the daemon's, not the script's: the loop reconciles every 30 s, a
scale-up reacts on the first hot sample, a scale-down needs two consecutive cold
samples plus the 60 s cooldown, and a scale moves one replica at a time — so
3 → 1 is two events. Expect **about 6 minutes** on a healthy cluster. The
passing-path budgets — 150 s for the count to reach 3 plus 45 s for those guests
to boot, 45 s for the `up` poll, 150 s back to 1 plus 45 s for the `down` poll,
45 s to reach 0 and the 95 s pause window — add up to **575 s (9.6 min)**, which
is the ceiling and not the expected runtime: a passing run does not sit on the
counter budgets, because the event is emitted before the count reaches its
target, so the first poll answers. A re-run that first has to wait out an earlier
run's microVMs (up to 120 s) can take ~11.6 min in the worst case.
The workload burns one guest's CPU in a shell loop with a 50 ms sleep between
bursts, which measures ~75 % of one vCPU and leaves the guest agent — and
`vm exec` — a scheduling window.

## Configuration

All settings are environment variables (defaults in the script header). Common
ones:

| Variable | Default | Meaning |
|---|---|---|
| `LAB_PREFIX` | `sc-lab` | VM name prefix (`<prefix>-node1`, ...) |
| `LAB_NODES` | `2` | default node count |
| `LAB_NET` | `default` | libvirt network |
| `LAB_CPUS` / `LAB_MEM` / `LAB_DISK` | `2` / `2048` / `24G` | per-VM resources |
| `LAB_ROOT` | `/var/lib/swarmcracker-lab` | state, images, VM disks |
| `LAB_IMAGE` | `<root>/noble-server-cloudimg-amd64.img` | base cloud image |
| `LAB_BIN` | `<root>/bin` | where binaries are built |
| `LAB_SUBNET` | `192.168.127.0/24` | microVM overlay subnet (must be `/24`) |
| `LAB_BRIDGE` | `swarm-br0` | microVM bridge name |
| `LAB_IMAGE_OCI` | `nginx:alpine` | image used by `test` |
| `LAB_AUTOSCALE_PREFIX` | `scas` | service name the `autoscale` scenario drives |
| `LAB_AUTOSCALE_HEALTH_ADDR` | `127.0.0.1:8080` | the manager's `--health-addr`, read from inside node 1 |
| `LAB_AUTOSCALE_UP_DEADLINE` / `LAB_AUTOSCALE_DOWN_DEADLINE` | `150` / `150` | seconds each half of `autoscale` may take |
| `LAB_AUTOSCALE_EVENT_DEADLINE` | `45` | seconds each counter poll waits for a scale event to appear |
| `LAB_AUTOSCALE_READ_DEADLINE` | `20` | seconds a single counter read retries before it is fatal |

Example with 3 nodes and a custom overlay subnet:

```bash
sudo LAB_NODES=3 LAB_SUBNET=192.168.140.0/24 \
  test-automation/multinode/cluster-lab.sh up
```

## Topology

```
host (KVM + libvirt network 192.168.122.0/24)
 ├─ <prefix>-node1   manager   192.168.122.x   swarm-br0 = 192.168.127.1/24
 ├─ <prefix>-node2   worker    192.168.122.y   swarm-br0 = 192.168.127.2/24
 └─ <prefix>-nodeN   worker    192.168.122.z   swarm-br0 = 192.168.127.N/24
        └─ VXLAN (VNI 100, UDP 4789) forms one L2 segment across nodes
```

Each node's `--vxlan-peers` is the list of the *other* nodes' transport IPs, so
every microVM bridge is reachable across hosts.

## Notes and limitations

- The two nodes share the overlay subnet with distinct bridge IPs. Per-task IPs
  are hashed from the task ID, so cross-node IP collisions are unlikely but not
  impossible; use a wider subnet for large labs.
- Guest MAC addresses are unique per task (see the duplicate-MAC fix in the
  report). Older binaries that emit a constant `AA:FC:00:00:00:00` will not work
  across nodes.
- The cloud-init seed is served over HTTP on the libvirt bridge and injected via
  the SMBIOS serial (`ds=nocloud-net;...`), which is reliable across modern
  cloud images.
