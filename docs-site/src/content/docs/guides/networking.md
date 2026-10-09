---
title: "Networking"
---

VMs need to talk to each other and to the outside world. Here's how SwarmCracker handles that.

---

## The Basic Setup

Each VM gets a TAP device connected to a Linux bridge:

```
Host
├── swarm-br0 (192.168.127.1)
│   ├── tap0 ── VM1 (192.168.127.10)
│   └── tap1 ── VM2 (192.168.127.11)
```

VMs on the same bridge can talk directly. The host talks via the bridge IP. Internet access goes through NAT.

---

## Config Options

```yaml
network:
  bridge_name: "swarm-br0"
  subnet: "192.168.127.0/24"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"   # static | dhcp
  nat_enabled: true
```

| Setting | Default | What It Does |
|---------|---------|--------------|
| `bridge_name` | swarm-br0 | The bridge name |
| `subnet` | 192.168.127.0/24 | IP range for VMs |
| `bridge_ip` | 192.168.127.1/24 | Host's IP on the bridge |
| `ip_mode` | static | `static` (deterministic) or `dhcp` (dnsmasq) |
| `nat_enabled` | true | Let VMs reach internet |

---

## IP Allocation

### Static (Default)

IPs come from hashing the VM ID. Same ID always gets the same IP. No DHCP needed, which makes startup faster.

The first host addresses `.1`–`.16` are reserved for infrastructure: on a
multi-node cluster every node shares one L2 overlay subnet and each node's
bridge takes a low address (`.1`, `.2`, …), so guests are only allocated from
`.17` upward to avoid colliding with a sibling node's bridge.

### DHCP

If you want dynamic IPs, switch to dnsmasq-backed DHCP:

```yaml
network:
  ip_mode: "dhcp"
```

SwarmCracker starts a minimal `dnsmasq` instance bound to the bridge when
`ip_mode: "dhcp"`. If `dnsmasq` is not installed, the bridge is still created
but DHCP allocation is unavailable.

---

## Talking Across Nodes

If you have VMs on different workers, they need VXLAN to communicate.

```
Node 1                    Node 2
swarm-br0                 swarm-br0
┌───┐┌───┐                ┌───┐┌───┐
│VM1││VM2│  ← VXLAN UDP → │VM3││VM4│
└───┘└───┘     4789       └───┘└───┘
```

### VXLAN Config

When you start `swarmd-firecracker` with `--vxlan-enabled`, it creates a VXLAN
interface named after the bridge (`swarm-br0-vxlan` for the default
`swarm-br0`) and attaches it to the bridge:

```bash
swarmd-firecracker \
  --vxlan-enabled \
  --vxlan-peers 192.168.56.12,192.168.56.13 \
  --bridge-name swarm-br0 \
  --subnet 192.168.127.0/24
```

With a single static peer you can substitute `--vxlan-peers <ip>`; for dynamic
discovery use `--consul-enabled` instead (see below).

### Consul for Peer Discovery

Each node registers itself in Consul. When a new peer shows up, the VXLAN forwarding database gets updated automatically.

```bash
swarmd-firecracker \
  --consul-enabled \
  --consul-address 127.0.0.1:8500 \
  --vxlan-enabled
```

### Firewall

VXLAN uses UDP port 4789:

```bash
sudo iptables -A INPUT -p udp --dport 4789 -j ACCEPT
```

---

## TAP Devices

SwarmCracker creates TAP devices automatically. Names follow the pattern
`tap-<8-char-task-hash>-<index>`, where the hash is the first 8 hex characters
of `sha256(task-id)`:

```
tap-a1b2c3d4-0
tap-9f8e7d6c-0
```

### Manual Creation (for debugging)

```bash
# Create
sudo ip tuntap add dev tap0 mode tap
sudo ip link set tap0 up
sudo ip link set tap0 master swarm-br0

# Delete
sudo ip link del tap0
```

---

## NAT and Internet

When `nat_enabled: true`, iptables masquerades outbound traffic:

```bash
iptables -t nat -A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE
```

### Disable Internet Access

```yaml
network:
  nat_enabled: false
```

VMs can only talk to each other and the host.

---

## Finding a VM's IP

Every microVM that boots gets an IP on the bridge, but where you read it back
from depends on how the VM was created:

| VM origin | Source of the IP | How to read it |
|-----------|------------------|----------------|
| `swarmcracker vm create -d` | CLI state file (`state.json`) | `swarmcracker vm status <vm>` |
| `swarmcracker service create` (daemon-managed) | daemon network allocation | `swarmcracker vm status <task>` / `vm list --format json` |

The daemon records each running VM's network details in a
`<task-id>.net.json` file next to its Firecracker socket (default
`/var/run/firecracker`). `vm status` and `vm list --format json` read that file,
so the guest IP — including the fallback TAP/DHCP allocation, which is never
written back into the SwarmKit task store — is visible for service VMs too.

The `vm list` table has no IP column, so parse `--format json` or use
`vm status`. On the host you can also confirm an address directly:

```bash
ip neigh show dev swarm-br0
```

---

## Publishing a Service Port

`service create --publish` (alias `-p`) exposes a port inside the microVM, so you
do not need to look up the guest IP:

```bash
swarmcracker service create --name web --image nginx:alpine --publish 8080:80
```

The syntax is `[host:]container[/tcp|udp]`:

```bash
# TCP (default): host 8080 -> guest 80
swarmcracker service create --name web --image nginx --publish 8080:80

# UDP
swarmcracker service create --name dns --image coredns/coredns --publish 53:53/udp

# Several mappings at once
swarmcracker service create --name app --image myapp --publish 8080:80 --publish 8443:443
```

`--publish-mode` selects how the port is exposed:

| Mode | Behavior |
|------|----------|
| `ingress` (default) | Cluster-wide: one entry point that load-balances across the service's healthy replicas, including replicas on other nodes. |
| `host` | Per-replica: forwards the host port on the node running each replica. |

```bash
# Load-balanced across replicas (default)
swarmcracker service create --name web --image nginx --replicas 3 --publish 8080:80

# Pin the host port to each node running a replica
swarmcracker service create --name web --image nginx --replicas 3 --publish 8080:80 --publish-mode host
```

`service ls` shows the mapping, and `service inspect` prints the published ports
and, for ingress services, the service VIP:

```console
$ swarmcracker service ls
ID                   NAME                 REPLICAS   PORTS             IMAGE
web                  web                  3          8080:80/tcp       nginx:alpine
```

### Ingress mode

For an ingress service the manager allocates a per-service **VIP** (from the top
of the overlay subnet) and attaches every replica to the ingress network, so
each replica gets a cluster-unique address on the shared L2 overlay. A reconciler
on manager nodes programs L4 (TCP/UDP) load balancing: new connections to the
published host port are distributed across the healthy replicas and forwarded to
the target port inside the chosen replica — including replicas running on worker
nodes.

- One entry point: reach the published port on any **manager** node.
- Balancing is L4 and per-connection; sticky sessions are not provided (front the
  service with a proxy if you need them).
- Replicas that are not `RUNNING` are removed from rotation automatically, and a
  service with no healthy replica stops being forwarded.

:::note[Current scope]
The routing mesh runs on **manager** nodes. Worker nodes do not yet listen on the
published port themselves (per-node fan-out is the next step of
[#36](https://github.com/restuhaqza/SwarmCracker/issues/36)). Reach an ingress
service through a manager, or use `--publish-mode host` if you need every node
to listen.
:::

### Host mode

In host mode the daemon programs DNAT rules on the node running the task, so
`host:8080` (on `127.0.0.1`, the LAN IP, or any interface) is forwarded to
`<guest-ip>:80`. The rules are tagged with the task ID and are removed
automatically when the task is removed or the service is scaled down.

Because each replica is published on its own node's host port, two replicas on
the same node cannot bind the same host port. If a host port is already in use,
the task fails with an explicit error (for example
`host port 8080/tcp is already published by task <id>`) instead of silently
being ignored. `--replicas 2` across two nodes makes both nodes listen on the
host port, but there is no single entry point — use `ingress` mode for that.

### Inspecting the rules

```bash
# Host-mode rules are tagged with the task ID; ingress rules with the service ID.
iptables -t nat -S PREROUTING  | grep swarmcracker
iptables -t nat -S OUTPUT      | grep swarmcracker
```

`PREROUTING` handles traffic from other hosts; `OUTPUT` handles traffic that
originates on the node itself (so `curl 127.0.0.1:8080` works, like Docker).

---

## Problems

### VMs Can't Talk to Each Other

```bash
ip link show swarm-br0    # Bridge exists?
ip link show | grep tap   # TAP devices attached?
```

Inside the VM, check `ip addr show eth0`.

### No Internet

```bash
iptables -t nat -L POSTROUTING   # NAT rule there?
sysctl net.ipv4.ip_forward       # Should be 1
```

Enable forwarding if needed:

```bash
sudo sysctl -w net.ipv4.ip_forward=1
```

### VXLAN Not Working

```bash
ip link show swarm-br0-vxlan      # VXLAN interface up?
iptables -L INPUT | grep 4789    # Port open?
ping <other-node-ip>             # Underlay reachable?
```

If FDB entries are missing, check Consul:

```bash
curl http://127.0.0.1:8500/v1/catalog/service/swarmcracker-vxlan
```

---

## More Reading

- [Firecracker network docs](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-interface.md)
- [Linux bridge fundamentals](https://wiki.linuxfoundation.org/networking/bridge)
- [VXLAN RFC 7348](https://tools.ietf.org/html/rfc7348)