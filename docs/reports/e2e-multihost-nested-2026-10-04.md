# SwarmCracker Multi-Host E2E Report — microVMs on separate nodes

**Date:** 2026-10-04
**Tester:** OpenCode agent (automated)
**Build:** `main@2729053` (SwarmCracker CLI `v0.9.2`), Firecracker `v1.15.1`
**Topology:** 1 physical KVM host → 2 nested Ubuntu VMs → 2-node SwarmKit cluster → microVMs on both nodes, connected over a VXLAN overlay
**Result:** ✅ **A microVM deployed on one node is reachable (ICMP + HTTP 200) from the other node across the VXLAN overlay.** One blocking bug was found and fixed to get there.

---

## 1. Executive summary

Two independent host kernels (two nested VMs) were joined into a real SwarmKit
cluster with the SwarmCracker `swarmd-firecracker` executor. A 2-replica service
was scheduled one microVM per node, and each node's host reached the **remote**
microVM over the VXLAN overlay:

```
sc-node1 -> microVM on sc-node2   ping=OK   http=200   ("Welcome to nginx!")
sc-node2 -> microVM on sc-node1   ping=OK   http=200   ("Welcome to nginx!")
```

| Capability | Result |
|---|---|
| Nested KVM on host (`/dev/kvm`, `vmx`, nested=Y) | ✅ |
| Two nested VMs with working nested KVM | ✅ |
| 2-node cluster, both nodes `READY` | ✅ |
| VXLAN overlay up on both nodes (FDB → peer) | ✅ |
| Node↔node overlay reachability (`192.168.127.1` ↔ `.2`) | ✅ 0% loss |
| Service scheduled across **different** nodes | ✅ 1 replica per node |
| Cross-host ICMP to remote microVM | ✅ 0% loss |
| Cross-host HTTP to remote microVM | ✅ HTTP 200 (both directions) |
| Remote guest MAC learned via VXLAN FDB | ✅ |
| **Blocking bug found** | ❌ → ✅ **fixed**: all microVMs shared MAC `AA:FC:00:00:00:00` |

Only one physical test server (`192.168.18.25`) was reachable, so "multi-node"
was realized as two nested VMs on that host. The two nodes are separate kernels
with separate SwarmKit daemons; the overlay between them is a real VXLAN tunnel
over the `192.168.122.0/24` L2 provided by the host's `virbr0`.

---

## 2. Environment

### Host — `192.168.18.25` (Parrot OS)

| Item | Value |
|---|---|
| CPU / RAM / disk | 4 vCPU (Intel i7-3520M, `vmx`) / 15 GiB / 97 GiB free |
| KVM | `kvm_intel` loaded, `nested=Y`, `/dev/kvm` present |
| Tooling | `virt-install`, `qemu-img`, `genisoimage`, `qemu-kvm`, Go 1.26.8 |
| Base image | `noble-server-cloudimg-amd64.img` (Ubuntu 24.04) |

> The host already ran a *separate* single-node SwarmCracker manager. The E2E
> cluster was built entirely inside two new nested VMs so the existing
> deployment was not touched.

### Nodes (nested VMs, Ubuntu 24.04.5, `--cpu host-passthrough`)

| Name | Role | IP (transport) | vCPU/RAM | Kernel | Nested KVM |
|---|---|---|---|---|---|
| `sc-node1` | manager | 192.168.122.55 | 2 / 2 GiB | 6.8.0-142 | `nested=Y`, `/dev/kvm` ✅ |
| `sc-node2` | worker | 192.168.122.56 | 2 / 2 GiB | 6.8.0-142 | `nested=Y`, `/dev/kvm` ✅ |

Overlay plumbing per node: `swarm-br0` (node1 `192.168.127.1/24`, node2
`192.168.127.2/24`), `swarm-br0-vxlan` (VNI 100, dst port 4789, MTU 1450),
FDB peer pointing at the other node's transport IP.

---

## 3. What was run

1. Built `main@2729053` on the host (`swarmcracker`, `swarmd-firecracker`,
   `swarmcracker-agent`, `swarmctl`, `swarmcracker-cni`).
2. Created two nested Ubuntu VMs with `/dev/kvm` passthrough and provisioned
   Firecracker + guest kernel + rootfs + CNI plugins.
3. `swarmcracker cluster init` on node1 with `--vxlan-enabled --vxlan-peers <node2>`.
4. `swarmcracker cluster join` on node2 with `--vxlan-enabled --vxlan-peers <node1>`.
5. `swarmcracker service create --name e2e-web --image nginx:alpine --replicas 2`.
6. Cross-host reachability matrix + VXLAN/ARP evidence.

---

## 4. Results

### 4.1 Cluster

```
ID                   STATUS       HOSTNAME             AVAILABILITY
jnove18yixtk         READY        sc-node1             ACTIVE
mk37e8mblmik         READY        sc-node2             ACTIVE
```

### 4.2 Scheduling — one microVM per node

```
ID                   STATUS       NODE                 IMAGE
m795y8b1cfhw         RUNNING      jnove18yixtk         nginx:alpine   # sc-node1
rn2it6b2vjyu         RUNNING      mk37e8mblmik         nginx:alpine   # sc-node2
```

The two per-task static IPs were distinct: node1 → `192.168.127.68`,
node2 → `192.168.127.99`.

### 4.3 Cross-host reachability matrix

| From (node host) | To | ping | HTTP |
|---|---|---|---|
| sc-node1 | microVM on sc-node1 (local) | OK | 200 |
| sc-node1 | microVM on sc-node2 (**remote**) | OK | 200 |
| sc-node2 | microVM on sc-node2 (local) | OK | 200 |
| sc-node2 | microVM on sc-node1 (**remote**) | OK | 200 |

Remote payload verified both directions: `<title>Welcome to nginx!</title>`.

### 4.4 Overlay evidence

```
node1 sees remote 192.168.127.99:  dev swarm-br0 lladdr aa:fc:79:78:a9:c9 REACHABLE
node2 sees remote 192.168.127.68:  dev swarm-br0 lladdr aa:fc:72:cb:d7:82 REACHABLE

node1 swarm-br0-vxlan learned: aa:fc:79:78:a9:c9 master swarm-br0   # remote MAC via VXLAN
node2 swarm-br0-vxlan learned: aa:fc:72:cb:d7:82 master swarm-br0   # remote MAC via VXLAN
```

---

## 5. Bug found and fixed — duplicate guest MAC (`AA:FC:00:00:00:00`)

### Symptom

Before the fix, **ping worked but TCP did not, asymmetrically**:
`sc-node1 → node2 microVM:80` succeeded while `sc-node2 → node1 microVM:80`
timed out (and it was flaky run-to-run). Node ARP entries showed `FAILED` for the
remote guest.

### Root cause

`pkg/swarmkit/translator.go` generated the guest NIC MAC from the interface
**index only**:

```go
func generateMAC(index int) string { ... }   // index is always 0 for the first NIC
```

Every microVM therefore used `AA:FC:00:00:00:00`. All microVMs from all nodes
share one L2 segment (the VXLAN overlay), so duplicate MACs made the Linux
bridge FDB flap between the local TAP and the VXLAN peer — frames destined for
one guest were delivered to another node's guest. ICMP sometimes survived; TCP
connections broke.

### Fix

Derive the MAC from a hash of the **task ID + interface index** (still locally
administered and unicast, deterministic, stable across restarts):

```go
func generateMAC(taskID string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", taskID, index)))
	return fmt.Sprintf("AA:FC:%02X:%02X:%02X:%02X", sum[0], sum[1], sum[2], sum[3])
}
```

Call site updated to `generateMAC(task.ID, i)`. After redeploying the daemon on
both nodes and re-creating the service, MACs were unique per VM
(`AA:FC:72:CB:D7:82` vs `AA:FC:79:78:A9:C9`), ARP went `REACHABLE`, the remote
MACs were learned on the VXLAN port, and the full matrix (4.3) passed.

Files changed (uncommitted working tree):

```
pkg/swarmkit/translator.go        (+/- MAC generation + call site)
pkg/swarmkit/translator_test.go   (+ cross-task MAC uniqueness assertions)
```

`go test ./pkg/swarmkit/ -run TestGenerateMAC` passes.

---

## 6. Other findings

| # | Sev | Area | Summary | Status |
|---|---|---|---|---|
| F1 | S1 | `cmd/swarmcracker/cmd_deprecated.go` | The deprecated top-level `swarmcracker join <addr>` **ignored the manager address**: its wrapper overwrote `PreRun` and no longer set `cfg.ManagerAddr`, so the connectivity probe targeted `:4242` and always failed ("cannot reach manager"). | ✅ **Fixed** — wrappers now chain the original `PreRun` (`wrapDeprecated`); re-verified end-to-end on a fresh node. |
| F2 | S3 | `cmd/swarmcracker/cmd_join.go` | The manager reachability check required `nc`; its bash `/dev/tcp` fallback was malformed (`/dev/tcp/<host> <port>`). | ✅ **Fixed** — replaced with `net.DialTimeout` (no external dependency). |
| F3 | S3 | ops | Both nodes used the same VM subnet `192.168.127.0/24` with different bridge IPs (`.1`/`.2`); per-task static IPs are hashed, so cross-node collisions are possible in principle (none observed: `.68`/`.99`, then `.101`/`.181`). | Open (documented) |
| F4 | S3 | tests | 3 pre-existing `pkg/swarmkit` failures, reproduced on pristine `HEAD` and unrelated to the MAC fix: `TestStartDirect_Push_SocketDirError`, `TestController_Prepare_Errors` (subtest `prepare_with_valid_image`), `TestPrepare_WithNetworks`. | Open (pre-existing) |

### Fixes / automation added

```
pkg/swarmkit/translator.go            unique per-task guest MAC (see §5)
pkg/swarmkit/translator_test.go       cross-task MAC uniqueness assertions
cmd/swarmcracker/cmd_deprecated.go    preserve wrapped command PreRun (fixes F1)
cmd/swarmcracker/cmd_join.go          TCP dial instead of nc (fixes F2)
cmd/swarmcracker/cmd_join_test.go     tests for both fixes
test-automation/multinode/            reproducible multi-node lab automation
```

---

## 7. Reproduce / cleanup

Use the automation added in this change to reproduce the whole lab:

```bash
sudo test-automation/multinode/cluster-lab.sh up 2      # create + provision + cluster
sudo test-automation/multinode/cluster-lab.sh test     # cross-host matrix
sudo test-automation/multinode/cluster-lab.sh destroy
```

The manual cluster from this run is left running for inspection:

```bash
# host
ssh root@192.168.18.25

# nodes
ssh -i /root/e2e/id_ed25519 root@192.168.122.55   # sc-node1 (manager)
ssh -i /root/e2e/id_ed25519 root@192.168.122.56   # sc-node2 (worker)

# cluster / workload
ssh -i /root/e2e/id_ed25519 root@192.168.122.55 \
  "/usr/local/bin/swarmcracker node ls; /usr/local/bin/swarmcracker service ps e2e-web"
```

Teardown:

```bash
# host
virsh destroy sc-node1 sc-node2 && virsh undefine sc-node1 sc-node2
rm -rf /root/e2e/vms /root/e2e/seeds /root/e2e/http
```

Artifacts: `/root/e2e/` on the host (binaries, VM disks, scripts, logs).

---

## 8. Verdict

Multi-host microVM networking through the SwarmCracker VXLAN overlay is **proven**
in a real 2-node cluster: a microVM scheduled on one node is reachable over
ICMP and HTTP from the other node, with the remote guest MAC learned through the
VXLAN forwarding database. The feature was blocked by a duplicate-MAC defect
that has been identified, fixed, and re-verified.
