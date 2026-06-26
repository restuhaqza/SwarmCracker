# Developer Docs

For people working on SwarmCracker itself.

---

## Repo Layout

```
cmd/
├── swarmctl/             # Debug CLI (swarmctl service create, etc)
├── swarmd-firecracker/   # Daemon (SwarmKit agent + executor)
├── swarmcracker/         # Main CLI (cluster mgmt, service, VM, config)
├── swarmcracker-agent/   # Remote deployment agent
├── swarmcracker-cni/     # CNI network plugin
└── get-join-token/       # Join token helper

pkg/
├── apiversion/           # gRPC API versioning protocol
├── executor/             # Turns SwarmKit tasks into Firecracker configs
├── network/              # Bridges, TAP, VXLAN, NAT, CNI, discovery
├── swarmkit/             # SwarmKit executor/controller integration
├── image/                # OCI image extraction, rootfs preparation
├── lifecycle/            # VM start/stop/configure logic
├── jailer/               # Security sandbox (cgroups, seccomp)
├── storage/              # Volumes, secrets, configs
├── snapshot/             # VM state snapshots
├── config/               # YAML config loading
├── metrics/              # Prometheus metrics
├── health/               # Health check server
├── security/             # Seccomp, capabilities
├── translator/           # Task → VMM config translation
├── runtime/              # Runtime state management
├── discovery/            # Consul/VXLAN peer auto-discovery
├── types/                # Shared type definitions
├── cni/                  # CNI network allocator
└── logging/              # Logging setup

infrastructure/
├── ansible/              # Cluster deployment roles
└── observability/        # Prometheus, Grafana configs
test-automation/          # Vagrant + e2e test infra
docs/                     # Documentation (you are here)
```

---

## Build

```bash
make build
```

Or just:

```bash
go build -o bin/swarmd-firecracker ./cmd/swarmd-firecracker
go build -o bin/swarmctl ./cmd/swarmctl
```

---

## Test

```bash
make test
```

Unit tests are in `pkg/*/*_test.go`. Integration tests need a cluster.

---

## Test Cluster

The Vagrant setup in `test-automation/` gives you a 3-node cluster:

```bash
cd test-automation
vagrant up
```

Manager at 192.168.121.18, workers at .153 and .59.

Ansible deploys everything:

```bash
ansible-playbook -i inventory/libvirt site.yml
```

---

## Debugging

### Executor Logs

```bash
journalctl -u swarmd -f
```

### VM Issues

```bash
# Check running VMs
ps aux | grep firecracker

# Check network
ip link show swarm-br0
ip link show swarm-br0-vxlan
bridge fdb show dev swarm-br0-vxlan
```

### Consul

```bash
curl http://127.0.0.1:8500/v1/catalog/service/swarmcracker-vxlan
```

---

## Making Changes

### Network Code

`pkg/network/manager.go` handles bridge and TAP setup. `vxlan.go` is VXLAN-specific. Changes here affect how VMs communicate.

### Executor

`pkg/swarmkit/executor.go` is where SwarmKit tasks become VM configs. If you want to add new VM options, this is the spot.

### CLI

`cmd/swarmctl/main.go` defines commands. `cmd/swarmd-firecracker/main.go` has daemon flags.

---

## Testing Changes

1. Build: `make build`
2. Upload to test VM: `vagrant upload bin/swarmd-firecracker /tmp/ worker1`
3. Install: `vagrant ssh worker1 -c "sudo mv /tmp/swarmd-firecracker /usr/local/bin/"`
4. Restart: `vagrant ssh worker1 -c "sudo systemctl restart swarmd-worker"`
5. Check logs: `journalctl -u swarmd-worker -f`

---

## More

- [API Reference](reference/api.md) — gRPC API, versioning, services
- [Package References](reference/) — Per-package documentation
- [Testing](testing/) — Unit and e2e test details
- [Architecture](architecture/) — SwarmKit integration specifics
- [Contributing](contributing.md) — PR guidelines
- [Architecture Overview](../architecture/overview.md) — System design