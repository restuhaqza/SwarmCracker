---
title: "Developer Docs"
---

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
├── network/              # Bridges, TAP, VXLAN, NAT, CNI
├── swarmkit/             # SwarmKit executor/controller integration
├── image/                # OCI image extraction, rootfs preparation
├── golden/               # Prebuilt golden image artifacts (ext4 + JSON sidecar)
├── lifecycle/            # VM start/stop/configure logic
├── jailer/               # Security sandbox (cgroups, seccomp)
├── storage/              # Volumes, secrets, configs
├── snapshot/             # VM state snapshots
├── config/               # YAML config loading
├── metrics/              # Prometheus metrics
├── health/               # Health check server
├── translator/           # Task → VMM config translation
├── runtime/              # Runtime state management
├── discovery/            # Consul/VXLAN peer auto-discovery
├── types/                # Shared type definitions
├── cni/                  # CNI network allocator
└── logging/              # Logging setup

infrastructure/
├── ansible/              # Cluster deployment roles
└── observability/        # Prometheus, Grafana configs
test-automation/          # tests + multi-node lab (test-automation/multinode/)
docs-site/                # Published docs (this page lives here; Astro Starlight)
docs/                     # Internal docs (reports, agent plans — not published)
```

---

## Build

```bash
make all
```

`make all` builds the three runtime binaries:

```bash
go build -o build/swarmcracker ./cmd/swarmcracker
go build -o build/swarmd-firecracker ./cmd/swarmd-firecracker
go build -o build/swarmcracker-agent ./cmd/swarmcracker-agent
```

The debug CLI `swarmctl` is not part of `make all`; build it directly:

```bash
go build -o build/swarmctl ./cmd/swarmctl
```

---

## Test

```bash
make test
```

Unit tests are in `pkg/*/*_test.go`. Integration tests need a cluster.

---

## Test Cluster

`make test-e2e` runs the Go E2E suite against a local swarmd.

For a real multi-node cluster with microVMs placed on separate nodes, the
single-host lab creates nested VMs and forms the cluster for you:

```bash
sudo test-automation/multinode/cluster-lab.sh up 2   # create + provision + cluster
sudo test-automation/multinode/cluster-lab.sh test   # cross-host matrix
sudo test-automation/multinode/cluster-lab.sh destroy
```

Ansible remains the advanced/production option (`infrastructure/ansible/`).

---

## Debugging

### Executor Logs

```bash
sudo journalctl -u swarmcracker-worker -f    # worker
sudo journalctl -u swarmcracker-manager -f   # manager
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

1. Build: `make all`
2. Bring up a lab node: `sudo test-automation/multinode/cluster-lab.sh up 2`
3. Copy the new binary: `scp build/swarmd-firecracker root@<node-ip>:/tmp/`
4. Install + restart: `ssh root@<node-ip> "mv /tmp/swarmd-firecracker /usr/local/bin/ && systemctl restart swarmcracker-worker"`
5. Check logs: `sudo journalctl -u swarmcracker-worker -f`

---

## More

- [API Reference](/reference/api/) — gRPC API, versioning, services
- [Image Package Reference](/contributing/reference/image/) — Per-package documentation
- [Unit Tests](/contributing/testing/unit-tests/) — Unit and e2e test details
- [Architecture](/architecture/) — SwarmKit integration specifics
- [Contributing](/contributing/guidelines/) — PR guidelines
- [Architecture Overview](/architecture/) — System design