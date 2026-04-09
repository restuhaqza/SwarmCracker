# Networking Guide

> Configure VM networking — TAP devices, bridges, VXLAN overlay.

---

## Overview

Each Firecracker VM connects to a Linux bridge via a TAP device:

```
┌─────────────────────────────────────────────────────────┐
│                      Host System                         │
│                                                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │  swarm-br0 (192.168.127.1/24)                    │  │
│  └──────────────┬─────────────────────────────────┘  │
│         ┌───────┴───────┐                              │
│    ┌────▼───┐       ┌───▼────┐                        │
│    │ tap0   │       │ tap1   │                        │
│    └────┬───┘       └───┬────┘                        │
└─────────┼───────────────┼──────────────────────────────┘
     ┌────▼───┐       ┌───▼────┐
     │  VM 1  │       │  VM 2  │
     │.10     │       │.11     │
     └────────┘       └────────┘
```

**VMs can communicate:**
- **With each other** — Same bridge = direct communication
- **With host** — Via bridge IP
- **With internet** — Via NAT/masquerading

---

## Configuration

```yaml
network:
  bridge_name: "swarm-br0"
  subnet: "192.168.127.0/24"
  bridge_ip: "192.168.127.1/24"
  ip_mode: "static"      # or "dhcp"
  nat_enabled: true
```

| Option | Default | Description |
|--------|---------|-------------|
| `bridge_name` | `swarm-br0` | Linux bridge name |
| `subnet` | `192.168.127.0/24` | VM IP range |
| `bridge_ip` | `192.168.127.1/24` | Host gateway IP |
| `ip_mode` | `static` | IP allocation mode |
| `nat_enabled` | `true` | Enable internet access |

---

## IP Allocation

### Static Mode (Default)

IPs allocated deterministically from VM ID hash:

```bash
# Same VM ID = same IP
# Different VM ID = different IP (with high probability)
```

**Advantages:**
- No DHCP server needed
- Predictable IPs for debugging
- Faster startup

### DHCP Mode

Uses dnsmasq for dynamic allocation:

```yaml
network:
  ip_mode: "dhcp"
  dhcp_range_start: "192.168.127.10"
  dhcp_range_end: "192.168.127.250"
```

---

## Cross-Node Networking (VXLAN)

For multi-node clusters, VXLAN overlay enables VM-to-VM communication across nodes:

```
┌─────────────────┐          ┌─────────────────┐
│  Node 1         │          │  Node 2         │
│  swarm-br0      │          │  swarm-br0      │
│  ┌───┐┌───┐    │          │  ┌───┐┌───┐    │
│  │VM1││VM2│    │◄──VXLAN──►│  │VM3││VM4│    │
│  └───┘└───┘    │  UDP4789 │  └───┘└───┘    │
└─────────────────┘          └─────────────────┘
```

### VXLAN Setup

```bash
# On each node, create VXLAN interface
sudo ip link add vxlan0 type vxlan \
  id 42 \
  dstport 4789 \
  remote <other-node-ip> \
  local <this-node-ip> \
  dev eth0

sudo ip link set vxlan0 up
sudo ip link set vxlan0 master swarm-br0
```

### Firewall

```bash
# Allow VXLAN traffic
sudo iptables -A INPUT -p udp --dport 4789 -j ACCEPT
```

---

## TAP Device Management

SwarmCracker automatically creates TAP devices:

```bash
# TAP device naming: tap-<vm-id>
tap-svc-nginx-abc123
tap-svc-redis-def456
```

### Manual TAP Creation (Debug)

```bash
# Create TAP device
sudo ip tuntap add dev tap0 mode tap
sudo ip link set tap0 up
sudo ip link set tap0 master swarm-br0

# Delete TAP device
sudo ip link del tap0
```

---

## NAT and Internet Access

When `nat_enabled: true`, SwarmCracker configures iptables for outbound traffic:

```bash
# NAT rule (auto-configured)
iptables -t nat -A POSTROUTING \
  -s 192.168.127.0/24 \
  -j MASQUERADE
```

### Disable NAT (Internal Only)

```yaml
network:
  nat_enabled: false
```

VMs can only communicate with each other and host, not internet.

---

## Troubleshooting

### VMs Can't Communicate

```bash
# Check bridge exists
ip link show swarm-br0

# Check TAP devices
ip link show | grep tap

# Check VM IPs (inside VM)
ip addr show eth0
```

### No Internet Access

```bash
# Check NAT enabled
iptables -t nat -L POSTROUTING

# Check forwarding enabled
sysctl net.ipv4.ip_forward
# Should be 1

# Enable forwarding
sudo sysctl -w net.ipv4.ip_forward=1
```

### VXLAN Not Working

```bash
# Check VXLAN interface
ip link show vxlan0

# Check UDP port open
sudo iptables -L INPUT | grep 4789

# Check remote node reachable
ping <other-node-ip>
```

---

## Reference

| Topic | Description |
|-------|-------------|
| [Firecracker Network](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-interface.md) | Official docs |
| [Linux Bridge](https://wiki.linuxfoundation.org/networking/bridge) | Bridge fundamentals |
| [VXLAN RFC](https://tools.ietf.org/html/rfc7348) | VXLAN specification |

---

**See Also:** [Configuration](configuration.md) | [SwarmKit](swarmkit.md)