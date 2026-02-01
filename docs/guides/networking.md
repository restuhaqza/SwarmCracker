# VM Networking Guide

SwarmCracker provides comprehensive networking support for Firecracker microVMs, enabling VM-to-VM communication, host connectivity, and internet access through NAT.

## Overview

Each Firecracker VM gets a TAP device that connects to a Linux bridge on the host. VMs can communicate with:
- **Each other** - VMs on the same bridge can communicate directly
- **Host** - VMs can reach the bridge IP on the host
- **Internet** - NAT/masquerading provides external connectivity

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      Host System                         │
│                                                          │
│  ┌──────────────────────────────────────────────────┐  │
│  │  swarm-br0 (192.168.127.1/24)                    │  │
│  │  Bridge - Connects all VM TAP devices            │  │
│  └──────────────┬─────────────────────────────────┘  │
│                 │                                       │
│         ┌───────┴───────┐                              │
│         │               │                              │
│    ┌────▼───┐       ┌───▼────┐                        │
│    │ tapeth0│       │ tapeth1│  ...                   │
│    └────┬───┘       └───┬────┘                        │
│         │               │                              │
└─────────┼───────────────┼──────────────────────────────┘
          │               │
     ┌────▼───┐       ┌───▼────┐
     │  VM 1  │       │  VM 2  │
     │.10     │       │.11     │
     └────────┘       └────────┘
```

## Configuration

Network configuration is specified in `/etc/swarmcracker/config.yaml`:

```yaml
network:
  # Bridge device name on host
  bridge_name: "swarm-br0"

  # Subnet for VM network (CIDR notation)
  subnet: "192.168.127.0/24"

  # Bridge IP address (CIDR notation)
  bridge_ip: "192.168.127.1/24"

  # IP allocation mode: "static" or "dhcp"
  # "static" - deterministic IP allocation based on VM ID hash
  # "dhcp" - requires external DHCP server (not yet implemented)
  ip_mode: "static"

  # Enable NAT for internet access
  nat_enabled: true
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `bridge_name` | string | `swarm-br0` | Name of the Linux bridge |
| `subnet` | string | `192.168.127.0/24` | Subnet for VM IPs |
| `bridge_ip` | string | `192.168.127.1/24` | Bridge IP address |
| `ip_mode` | string | `static` | IP allocation: `static` or `dhcp` |
| `nat_enabled` | bool | `true` | Enable NAT for internet access |

## IP Allocation

### Static IP Allocation (Default)

When `ip_mode: static`, SwarmCracker allocates IPs deterministically based on the VM ID:

1. VM ID is hashed using SHA-256
2. Hash is mapped to an IP in the configured subnet
3. Same VM ID always gets the same IP
4. Different VM IDs get different IPs (with high probability)

**Example:**
```bash
# VM "task-abc123" might get 192.168.127.42
# VM "task-def456" might get 192.168.127.187
```

**Benefits:**
- Predictable IPs - same VM always gets same IP
- No DHCP daemon required
- Simple and reliable

**IP Range:**
- Usable range: `192.168.127.2 - 192.168.127.254`
- Bridge (gateway): `192.168.127.1`
- Network address: `192.168.127.0` (not used)

### DHCP Mode (Future)

DHCP mode will allow integration with `dnsmasq` or similar for dynamic IP allocation. This is planned for a future release.

## Host Setup

### One-Time Setup

The first time you use networking, you may need to configure the host:

```bash
# 1. Enable IP forwarding (for NAT)
sudo sysctl -w net.ipv4.ip_forward=1
# To make permanent:
echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
sudo sysctl -p

# 2. SwarmCracker will automatically:
#    - Create the bridge (swarm-br0)
#    - Assign the bridge IP (192.168.127.1/24)
#    - Setup NAT/masquerading rules
#    - Create TAP devices for each VM

# 3. Verify bridge exists (after starting first VM)
ip addr show swarm-br0
```

### Manual Bridge Creation (Optional)

If you prefer to create the bridge manually:

```bash
# Create bridge
sudo ip link add swarm-br0 type bridge

# Assign IP
sudo ip addr add 192.168.127.1/24 dev swarm-br0

# Bring it up
sudo ip link set swarm-br0 up

# Enable NAT
sudo iptables -t nat -A POSTROUTING -s 192.168.127.0/24 -j MASQUERADE
sudo iptables -A FORWARD -i swarm-br0 -j ACCEPT
sudo iptables -A FORWARD -o swarm-br0 -j ACCEPT
```

### Cleanup

To remove the bridge:

```bash
sudo ip link delete swarm-br0
```

## Usage

### Starting a VM with Networking

Networking is automatically enabled when you start a VM:

```bash
swarmcracker-agent run nginx:alpine
```

The VM will:
1. Get a TAP device (e.g., `tapeth0`)
2. Be attached to `swarm-br0`
3. Get allocated an IP (e.g., `192.168.127.42`)
4. Have internet access via NAT

### Checking VM IP

```bash
# From the host, check the TAP device
ip addr show tapeth0

# Inside the VM, check the network interface
# (requires VM to be running with init system)
swarmcracker-agent exec <task-id> ip addr show eth0
```

### Testing Connectivity

```bash
# From VM to host (bridge IP)
swarmcracker-agent exec <task-id> ping -c 3 192.168.127.1

# From VM to VM
swarmcracker-agent exec <task-id-1> ping -c 3 192.168.127.42

# From VM to internet
swarmcracker-agent exec <task-id> ping -c 3 8.8.8.8
```

## Network Security

### Default Security

- VMs are **isolated** from host network interfaces
- VMs can only communicate through the bridge
- NAT prevents direct inbound connections from internet

### Advanced Security (Manual)

You can add additional firewall rules:

```bash
# Block VM-to-VM communication (isolate VMs)
sudo iptables -A FORWARD -i swarm-br0 -o swarm-br0 -j DROP

# Allow only specific VMs to communicate
sudo iptables -A FORWARD -i swarm-br0 -o swarm-br0 \
  -s 192.168.127.10 -d 192.168.127.20 -j ACCEPT
sudo iptables -A FORWARD -i swarm-br0 -o swarm-br0 -j DROP

# Block internet access for specific VM
sudo iptables -A FORWARD -i swarm-br0 -s 192.168.127.10 -j DROP
```

## Troubleshooting

### VM has no IP address

**Symptom:** VM boots but `ip addr` inside VM shows no IP.

**Solutions:**
1. Check if `ip_mode: static` is set in config
2. Verify subnet is configured: `subnet: "192.168.127.0/24"`
3. Check bridge exists: `ip addr show swarm-br0`
4. Check TAP device exists: `ip addr show tapeth0`

### VM cannot reach internet

**Symptom:** VM can ping bridge but not internet.

**Solutions:**
1. Check IP forwarding is enabled: `sysctl net.ipv4.ip_forward`
2. Check NAT rules: `sudo iptables -t nat -L -n -v`
3. Verify `nat_enabled: true` in config
4. Check host has internet connectivity

### VM cannot reach host

**Symptom:** VM cannot ping bridge IP.

**Solutions:**
1. Verify bridge IP: `ip addr show swarm-br0`
2. Check bridge is up: `ip link show swarm-br0`
3. Verify TAP is connected to bridge: `bridge link`

### Permission denied creating TAP

**Symptom:** `Permission denied` when creating TAP device.

**Solutions:**
1. Run with `sudo` or with appropriate capabilities
2. Add user to `kvm` group: `sudo usermod -aG kvm $USER`
3. Log out and back in for group change to take effect

### Bridge already exists

**Symptom:** Cannot create bridge because it already exists.

**Solutions:**
1. SwarmCracker will use existing bridge if found
2. To start fresh: `sudo ip link delete swarm-br0`
3. Or use different bridge name in config

## Performance Tuning

### TAP Device Queues

Firecracker uses virtio-net with configurable queue sizes. Defaults in translator:
- RxQueueSize: 256
- TxQueueSize: 256

For high-bandwidth applications, you can increase these by modifying the translator.

### Bridge MTU

Default MTU is 1500. For jumbo frames:

```bash
sudo ip link set swarm-br0 mtu 9000
```

### Rate Limiting

Rate limiting can be enabled in config:

```yaml
network:
  enable_rate_limit: true
  max_packets_per_sec: 10000
```

## Advanced Topics

### Multiple Bridges

You can configure multiple bridges by using different bridge names in network attachments:

```yaml
# In task spec
networks:
  - network:
      id: "network-1"
      spec:
        driver_config:
          bridge:
            name: "swarm-br0"
  - network:
      id: "network-2"
      spec:
        driver_config:
          bridge:
            name: "swarm-br1"
```

### Custom Subnets

For isolation, use different subnets:

```yaml
# config.yaml
network:
  subnet: "10.0.0.0/24"
  bridge_ip: "10.0.0.1/24"
```

### IPv6 Support

IPv6 is planned for a future release. Currently, only IPv4 is supported.

## Integration with SwarmKit

When using SwarmKit service discovery, VMs can be reached via their allocated IPs:

```bash
# List tasks and their IPs
swarmcracker-agent ps

# Task will show allocated IP in status
```

## Examples

See the `examples/networking/` directory for complete examples:
- `basic-networking/` - Simple VM with networking
- `multi-vm/` - Multiple VMs communicating
- `firewall/` - Firewall rules for isolation

## References

- [Firecracker Network Interface](https://github.com/firecracker-microvm/firecracker/blob/main/docs/network-interface.md)
- [Linux Bridge Documentation](https://wiki.linuxfoundation.org/networking/bridge)
- [iptables NAT HOWTO](https://tldp.org/HOWTO/IP-Masquerade-HOWTO/)

## Support

For issues or questions:
1. Check troubleshooting section above
2. Search existing GitHub issues
3. Create new issue with logs and configuration

## Changelog

### v1.0.0 (Current)
- Static IP allocation
- NAT/masquerading for internet access
- Bridge auto-configuration
- IP forwarding setup
- Comprehensive testing

### Planned Features
- DHCP server integration (dnsmasq)
- IPv6 support
- Network metrics/monitoring
- Per-VM firewall rules
- VLAN support
