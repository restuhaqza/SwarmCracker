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