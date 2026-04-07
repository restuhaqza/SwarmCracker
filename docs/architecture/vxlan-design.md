# SwarmCracker VXLAN Overlay Networking

## Overview

SwarmCracker now supports automated VXLAN overlay networking for cross-node VM communication. This allows VMs running on different worker nodes to communicate as if they were on the same Layer 2 network.

## Architecture

```
worker1 (192.168.121.24)              worker2 (192.168.121.143)
swarm-br0: 172.20.0.1/24              swarm-br0: 172.20.0.1/24
    TAP devices (VMs)                     TAP devices (VMs)
            │                                      │
            └────── VXLAN (VNI 100) ───────────────┘
                   UDP port 4789
                   L2 overlay (shared 172.20.0.0/24)
```

## Components

### 1. Go Code: `pkg/network/vxlan.go`

Automated VXLAN management using `github.com/vishvananda/netlink`:

```go
vxlanManager := network.NewVXLANManager(
    "swarm-br0",           // Bridge name
    100,                   // VXLAN ID
    "10.30.0.1/24",        // Overlay IP
    []string{"192.168.56.12"}, // Peer IPs
)

err := vxlanManager.SetupVXLAN("enp0s8", "192.168.56.11")
```

Features:
- Creates VXLAN interface
- Attaches to bridge
- Configures overlay IP
- Adds peer forwarding entries
- Enables proxy ARP and IP forwarding
- Adds routes to remote VM subnets

### 2. Integration: `pkg/network/manager.go`

The `NetworkManager` automatically sets up VXLAN when enabled:

```go
func (nm *NetworkManager) setupVXLANOverlay(ctx context.Context) error {
    // Discovers physical interface and local IP
    // Creates VXLAN interface
    // Attaches to bridge
    // Configures proxy ARP
}
```

### 3. Shell Script: `scripts/setup-vxlan-overlay.sh`

Standalone script for manual setup or provisioning:

```bash
./setup-vxlan-overlay.sh \\
    swarm-br0 \\         # Bridge name
    10.30.0.1/24 \\      # Overlay IP
    100 \\               # VXLAN ID
    enp0s8 \\            # Physical interface
    192.168.56.11 \\     # Local IP
    192.168.56.12        # Peer IPs...
```

Features:
- Automatic VXLAN interface creation
- Bridge attachment
- Sysctl configuration (proxy ARP, forwarding)
- Peer forwarding entries
- Status reporting

### 4. Vagrant Integration: `scripts/setup-worker.sh`

The worker setup script now includes VXLAN configuration:

```bash
# Automatically calculates overlay IP based on worker index
# Worker 1: 10.30.101.1/24
# Worker 2: 10.30.102.1/24

# Adds routes to remote worker VM subnets
ip route add 192.168.128.0/24 via 10.30.102 dev swarm-br0
```

## Configuration

### Via Config File

```yaml
# /etc/swarmcracker/config.yaml
network:
  bridge_name: "swarm-br0"
  bridge_ip: "172.20.0.1/24"
  vxlan_enabled: true
  vxlan_vni: 100
  vxlan_peers:
    - "192.168.121.143"
    - "192.168.121.25"
```

### Via CLI Flags

```bash
swarmd-firecracker \\
  --vxlan-enabled \\
  --vxlan-id 100 \\
  --vxlan-tunnel-ip 10.30.0.1/24 \\
  --vxlan-peer 192.168.56.12 \\
  --vxlan-peer 192.168.56.13
```

## Usage Examples

### Manual Setup

```bash
# On worker1
sudo ./scripts/setup-vxlan-overlay.sh \\
    swarm-br0 \\
    10.30.0.1/24 \\
    100 \\
    enp0s8 \\
    192.168.56.11 \\
    192.168.56.12

# Add route to worker2's VM subnet
sudo ip route add 192.168.128.0/24 via 10.30.0.2 dev swarm-br0
```

```bash
# On worker2
sudo ./scripts/setup-vxlan-overlay.sh \\
    swarm-br0 \\
    10.30.0.2/24 \\
    100 \\
    enp0s8 \\
    192.168.56.12 \\
    192.168.56.11

# Add route to worker1's VM subnet
sudo ip route add 192.168.127.0/24 via 10.30.0.1 dev swarm-br0
```

### Automated (via Vagrant)

```bash
vagrant up worker1 worker2
# VXLAN is configured automatically during provisioning
```

## Verification

### Check VXLAN Interface

```bash
$ ip addr show swarm-br0-vxlan
31: swarm-br0-vxlan: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc noqueue master swarm-br0 state UNKNOWN
    link/ether 66:b8:84:7f:09:36 brd ff:ff:ff:ff:ff:ff
```

### Check Overlay IP

```bash
$ ip addr show swarm-br0 | grep 10.30.0
    inet 10.30.0.1/24 scope global swarm-br0
```

### Check Peer Forwarding

```bash
$ bridge fdb show dev swarm-br0-vxlan | grep dst
00:00:00:00:00:00 dst 192.168.56.12 self permanent
```

### Test Connectivity

```bash
# From worker1
$ ping -c 3 10.30.0.2
64 bytes from 10.30.0.2: icmp_seq=1 ttl=64 time=3.85 ms

# Ping remote worker's bridge
$ ping -c 3 192.168.128.1
64 bytes from 192.168.128.1: icmp_seq=1 ttl=64 time=6.50 ms
```

## How It Works

### Layer 2 over Layer 3

VXLAN encapsulates Layer 2 Ethernet frames within Layer 3 UDP packets:

```
VM1 (192.168.127.52)                  VM2 (192.168.128.52)
     │                                         │
     │ Ethernet frame                         │
     └─────────────────────────────────────────┘
                     │
                     ▼
        VXLAN encapsulation (VNI 100)
                     │
                     ▼
        UDP packet (port 4789)
                     │
                     ▼
        IP packet (192.168.56.11 → 192.168.56.12)
                     │
                     ▼
        Ethernet frame (physical NIC)
```

### Proxy ARP

Proxy ARP allows VMs to find each other without knowing the overlay network:

```
VM1: "Who has 192.168.128.52?"
worker1 bridge: "I have it!" (proxy ARP)
VM1 sends packet to worker1 bridge
worker1 routes via VXLAN to worker2
worker2 delivers to VM2
```

### Routing

Each worker has a route to the other worker's VM subnet:

```
worker1: ip route add 192.168.128.0/24 via 10.30.0.2 dev swarm-br0
worker2: ip route add 192.168.127.0/24 via 10.30.0.1 dev swarm-br0
```

## Scalability

### Adding a Third Worker

```bash
# On worker3 (192.168.56.13)
sudo ./scripts/setup-vxlan-overlay.sh \\
    swarm-br0 \\
    10.30.0.3/24 \\
    100 \\
    enp0s8 \\
    192.168.56.13 \\
    192.168.56.11 192.168.56.12

# On existing workers, add worker3 as peer
sudo bridge fdb append to 00:00:00:00:00:00 dst 192.168.56.13 dev swarm-br0-vxlan

# Add route to worker3's VM subnet
sudo ip route add 192.168.129.0/24 via 10.30.0.3 dev swarm-br0
```

## Troubleshooting

### VXLAN Interface Not Created

```bash
# Check if VXLAN module is loaded
lsmod | grep vxlan
sudo modprobe vxlan

# Check physical interface exists
ip link show enp0s8
```

### Cannot Ping Peer

```bash
# Check physical connectivity
ping 192.168.56.12

# Check VXLAN interface is UP
ip link show swarm-br0-vxlan

# Check forwarding database
bridge fdb show dev swarm-br0-vxlan

# Check firewall rules (UDP 4789)
sudo iptables -L -n | grep 4789
```

### Proxy ARP Not Working

```bash
# Check sysctl settings
sysctl net.ipv4.conf.swarm-br0.proxy_arp
sysctl net.ipv4.conf.swarm-br0.forwarding

# Enable if needed
sudo sysctl -w net.ipv4.conf.swarm-br0.proxy_arp=1
sudo sysctl -w net.ipv4.conf.swarm-br0.forwarding=1
```

## Performance Considerations

- **MTU**: VXLAN adds 50 bytes overhead. MTU is reduced to 1450.
- **CPU**: VXLAN encapsulation is done in kernel (minimal overhead).
- **Bandwidth**: Each packet has ~50 bytes of VXLAN headers.
- **Latency**: Typically adds 1-5ms per hop.

## Future Enhancements

- [ ] SwarmKit node discovery integration
- [ ] Automatic peer discovery via etcd/consul
- [ ] VXLAN over multicast (reduces peer configuration)
- [ ] BGP/EVPN for large-scale deployments
- [ ] VXLAN security (IPsec encryption)
- [ ] Dynamic VXLAN ID allocation

## References

- [VXLAN RFC 7348](https://tools.ietf.org/html/rfc7348)
- [Linux VXLAN Documentation](https://www.kernel.org/doc/Documentation/networking/vxlan.txt)
- [vishvananda/netlink](https://github.com/vishvananda/netlink)
