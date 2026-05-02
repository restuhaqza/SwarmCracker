# Consul Service Discovery Integration

## Overview

Integrate Consul for automatic VXLAN peer discovery in SwarmCracker clusters.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Consul Cluster                           │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐               │
│  │ Consul 1  │  │ Consul 2  │  │ Consul 3  │               │
│  │ (Manager) │  │ (Worker)  │  │ (Worker)  │               │
│  └───────────┘  └───────────┘  └───────────┘               │
│                                                              │
│  Services:                                                   │
│  - swarmcracker-vxlan: {IP, Port, VXLAN_ID, Status}        │
│                                                              │
│  KV:                                                         │
│  - swarmcracker/config/vxlan_port: 4789                    │
│  - swarmcracker/config/vxlan_id: 100                       │
│  - swarmcracker/config/subnet: 192.168.127.0/24            │
└─────────────────────────────────────────────────────────────┘

┌─────────────┐    ┌─────────────┐    ┌─────────────┐
│  Manager    │    │  Worker 1   │    │  Worker 2   │
│             │    │             │    │             │
│ 1. Register │    │ 1. Register │    │ 1. Register │
│    service  │    │    service  │    │    service  │
│             │    │             │    │             │
│ 2. Discover │    │ 2. Discover │    │ 2. Discover │
│    peers    │───▶│    peers    │───▶│    peers    │
│             │    │             │    │             │
│ 3. Setup    │    │ 3. Setup    │    │ 3. Setup    │
│    VXLAN    │    │    VXLAN    │    │    VXLAN    │
└─────────────┘    └─────────────┘    ┌─────────────┘
```

## Implementation Plan

### Phase 1: Consul Deployment (Ansible)

1. Add Consul role to Ansible
2. Deploy Consul agent on all nodes
3. Manager runs Consul server mode
4. Workers run Consul client mode

### Phase 2: Consul Integration (Go)

1. Create `pkg/discovery/consul.go`
2. Implement ConsulNodeDiscovery
3. Register service on startup
4. Discover peers for VXLAN FDB

### Phase 3: Testing

1. Deploy cluster with Consul
2. Verify service registration
3. Test VXLAN peer discovery
4. Test node join/leave

## Service Definition

```json
{
  "Name": "swarmcracker-vxlan",
  "ID": "swarm-worker-1",
  "Tags": ["vxlan", "worker"],
  "Address": "192.168.121.153",
  "Port": 4789,
  "Meta": {
    "vxlan_id": "100",
    "bridge_ip": "192.168.127.1",
    "hostname": "swarm-worker-1"
  },
  "Check": {
    "ID": "vxlan-health",
    "Name": "VXLAN Interface Health",
    "Args": ["check-vxlan.sh"],
    "Interval": "10s",
    "Timeout": "1s"
  }
}
```

## Code Structure

```
pkg/discovery/
├── consul.go          # Consul client integration
├── consul_test.go     # Unit tests
└── health_check.go    # VXLAN health check script

infrastructure/ansible/roles/consul/
├── defaults/main.yml
├── tasks/main.yml
├── templates/
│   ├── consul.service.j2
│   └── consul-config.json.j2
```

## Configuration

```yaml
# cmd/swarmd-firecracker flags
--consul-address: Consul agent address (default: "127.0.0.1:8500")
--consul-service-name: Service name (default: "swarmcracker-vxlan")
--consul-register: Enable service registration (default: true)
--consul-discovery: Enable peer discovery (default: true)
```

## Workflow

### On Startup

```go
func (e *Executor) Init(ctx context.Context) error {
    // 1. Initialize network (bridge, VXLAN)
    nm.Init(ctx)

    // 2. Connect to Consul
    consulClient := discovery.NewConsulClient(config.ConsulAddress)

    // 3. Register service
    consulClient.RegisterService(discovery.ServiceConfig{
        Name:    "swarmcracker-vxlan",
        ID:      hostname,
        Address: localIP,
        Port:    vxlanPort,
        Meta: map[string]string{
            "vxlan_id":  "100",
            "bridge_ip": bridgeIP,
        },
    })

    // 4. Discover peers
    peers := consulClient.DiscoverPeers("swarmcracker-vxlan")

    // 5. Update VXLAN FDB
    nm.vxlanMgr.UpdatePeers(peers)

    // 6. Start periodic discovery
    go consulClient.WatchPeers(ctx, func(peers []string) {
        nm.vxlanMgr.UpdatePeers(peers)
    })

    return nil
}
```

### On Shutdown

```go
func (e *Executor) Close() error {
    // Deregister service
    consulClient.DeregisterService(hostname)
    return nil
}
```

## Dependencies

```
go get github.com/hashicorp/consul/api
```

## Benefits

| Feature | Manual Config | Consul |
|---------|---------------|--------|
| Peer discovery | Ansible inventory | Automatic |
| Dynamic scaling | Manual update | Auto-detect |
| Health checking | Manual | Built-in |
| Node removal | Manual cleanup | Auto-deregister |
| Configuration | Static files | KV store |

## Alternative: Consul vs etcd

| Feature | Consul | etcd |
|---------|--------|------|
| Service discovery | ✅ Native | ❌ Need proxy |
| Health checking | ✅ Built-in | ❌ External |
| DNS interface | ✅ Built-in | ❌ External |
| Web UI | ✅ Built-in | ❌ External |
| Kubernetes native | ❌ No | ✅ Core component |

**Recommendation: Consul** for SwarmCracker standalone clusters.
**etcd** if integrating with Kubernetes later.

## Questions for User

1. Should Consul run embedded in swarmd-firecracker or as separate agent?
2. Manager as Consul server, or separate Consul cluster?
3. Use Consul KV for SwarmCracker config storage?

---

Next step: Implement `pkg/discovery/consul.go`?