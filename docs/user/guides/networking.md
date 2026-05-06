# Networking

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
  nat_enabled: true
```

| Setting | Default | What It Does |
|---------|---------|--------------|
| `bridge_name` | swarm-br0 | The bridge name |
| `subnet` | 192.168.127.0/24 | IP range for VMs |
| `bridge_ip` | 192.168.127.1/24 | Host's IP on the bridge |
| `nat_enabled` | true | Let VMs reach internet |

---

## IP Allocation

### Static (Default)

IPs come from hashing the VM ID. Same ID always gets the same IP. No DHCP needed, which makes startup faster.

### DHCP

If you want dynamic IPs, use dnsmasq:

```yaml
network:
  ip_mode: "dhcp"
  dhcp_range_start: "192.168.127.10"
  dhcp_range_end: "192.168.127.250"
```

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

When you start swarmd with `--vxlan-enabled`, it creates a VXLAN interface and attaches it to the bridge:

```bash
swarmd-firecracker \
  --vxlan-enabled \
  --bridge-name swarm-br0 \
  --subnet 192.168.127.0/24
```

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

SwarmCracker creates TAP devices automatically. Names follow the pattern `tap-<vm-id>`:

```
tap-svc-nginx-abc123
tap-svc-redis-def456
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
ip link show vxlan0              # VXLAN interface up?
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