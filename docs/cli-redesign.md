# SwarmCracker CLI Redesign - Phase 1

## Overview

The SwarmCracker CLI has been reorganized into a hierarchical command structure for better usability and consistency. This document describes the new command structure and provides migration guidance.

## New Command Structure

### `swarmcracker cluster` - Cluster Lifecycle

Manage the SwarmCracker cluster lifecycle.

**Commands:**
- `init` - Initialize a new cluster as a manager node
- `join <addr>` - Join an existing cluster
- `leave` - Leave the cluster
- `token [worker|manager]` - Manage join tokens
- `status` - Show cluster status
- `reset` - Reset cluster state on this node
- `deinit` - Deinitialize the cluster (complete cleanup)

**Examples:**
```bash
# Initialize a new cluster
swarmcracker cluster init --advertise-addr 192.168.1.10

# Join as a worker
swarmcracker cluster join --worker 192.168.1.10:4242

# View cluster status
swarmcracker cluster status

# Leave the cluster
swarmcracker cluster leave
```

### `swarmcracker node` - Node Management

Manage and inspect nodes in the cluster.

**Commands:**
- `ls` - List nodes
- `inspect <node-id>` - Inspect a node
- `drain <node-id>` - Drain a node (reschedule tasks)
- `activate <node-id>` - Activate a drained/paused node
- `promote <node-id>` - Promote worker to manager
- `rm <node-id>` - Remove a node

**Examples:**
```bash
# List all nodes
swarmcracker node ls

# Inspect a specific node
swarmcracker node inspect 123abc...

# Drain a node for maintenance
swarmcracker node drain 123abc...

# Activate after maintenance
swarmcracker node activate 123abc...
```

### `swarmcracker service` - Service Management

Manage services in the cluster.

**Commands:**
- `ls` - List services
- `inspect <service-id>` - Inspect a service
- `ps <service-id>` - List tasks for a service
- `create <image>` - Create a new service
- `update <service-id>` - Update service configuration
- `scale <service-id> <replicas>` - Scale a service
- `rm <service-id>` - Remove a service

**Examples:**
```bash
# Create a service
swarmcracker service create nginx:latest --name web --replicas 3

# List services
swarmcracker service ls

# Scale a service
swarmcracker service scale 123abc... 5

# Update a service
swarmcracker service update --image nginx:1.25 123abc...

# Remove a service
swarmcracker service rm 123abc...
```

### `swarmcracker task` - Task Management

Manage and inspect tasks.

**Commands:**
- `ls` - List tasks
- `inspect <task-id>` - Inspect a task

**Examples:**
```bash
# List all tasks
swarmcracker task ls

# List tasks for a specific node
swarmcracker task ls --node 123abc...

# Inspect a task
swarmcracker task inspect 456def...
```

### `swarmcracker vm` - VM Operations

Manage Firecracker microVMs.

**Commands:**
- `ls` - List running microVMs
- `inspect <vm-id>` - Inspect a microVM
- `create <image>` - Create a new microVM
- `stop <vm-id>` - Stop a running microVM
- `rm <vm-id>` - Remove a microVM
- `logs <vm-id>` - View microVM logs
- `snapshot <vm-id>` - Create a microVM snapshot

**Examples:**
```bash
# Create a VM
swarmcracker vm create nginx:latest --vcpus 2 --memory 1024

# List VMs
swarmcracker vm ls

# View logs
swarmcracker vm logs --follow 123abc...

# Create a snapshot
swarmcracker vm snapshot --output /backup/vm1.snap 123abc...
```

### `swarmcracker network` - Network Configuration

Manage cluster networks.

**Commands:**
- `ls` - List networks
- `inspect <network-id>` - Inspect a network
- `create <name>` - Create a new network
- `rm <network-id>` - Remove a network

**Examples:**
```bash
# Create an overlay network
swarmcracker network create --subnet 10.0.9.0/24 mynet

# List networks
swarmcracker network ls

# Inspect a network
swarmcracker network inspect 789ghi...
```

### `swarmcracker asset` - Asset Management

Manage kernels, rootfs images, and other assets.

**Commands:**
- `ls` - List available assets
- `pull <asset-name>` - Download an asset
- `rm <asset-name>` - Remove an asset

**Examples:**
```bash
# List all assets
swarmcracker asset ls

# List only kernels
swarmcracker asset ls --type kernel

# List only rootfs images
swarmcracker asset ls --type rootfs
```

### `swarmcracker config` - Configuration Management

Manage SwarmCracker configuration.

**Commands:**
- `view` - View current configuration
- `validate` - Validate configuration file
- `init` - Create a default configuration file
- `set <key> <value>` - Set a configuration value
- `unset <key>` - Unset a configuration value

**Examples:**
```bash
# View configuration
swarmcracker config view

# Validate configuration
swarmcracker config validate

# Create default configuration
swarmcracker config init --output /etc/swarmcracker/config.yaml

# Set a configuration value
swarmcracker config set executor.default_vcpus 2
```

## Migration Guide

### Legacy Commands

The following legacy commands are deprecated but still work with warnings:

| Legacy Command | New Command |
|----------------|-------------|
| `swarmcracker init` | `swarmcracker cluster init` |
| `swarmcracker join <addr>` | `swarmcracker cluster join <addr>` |
| `swarmcracker leave` | `swarmcracker cluster leave` |
| `swarmcracker deinit` | `swarmcracker cluster deinit` |
| `swarmcracker reset` | `swarmcracker cluster reset` |
| `swarmcracker run <image>` | `swarmcracker vm create <image>` |
| `swarmcracker validate` | `swarmcracker config validate` |
| `swarmcracker list` | `swarmcracker service ls` / `swarmcracker node ls` / `swarmcracker vm ls` |
| `swarmcracker status` | `swarmcracker cluster status` |
| `swarmcracker logs <vm-id>` | `swarmcracker vm logs <vm-id>` |
| `swarmcracker stop <vm-id>` | `swarmcracker vm stop <vm-id>` |
| `swarmcracker snapshot <vm-id>` | `swarmcracker vm snapshot <vm-id>` |

### Deprecation Timeline

- **Phase 1 (Current):** Legacy commands work with deprecation warnings
- **Phase 2 (Future):** Legacy commands will be removed
- **Migration:** Update scripts and documentation to use new commands

## Benefits of the New Structure

1. **Logical Grouping:** Commands are grouped by function (cluster, node, service, etc.)
2. **Consistency:** Follows patterns from other CLI tools (docker, kubectl)
3. **Discoverability:** `--help` shows all subcommands clearly
4. **Extensibility:** Easy to add new commands under appropriate groups
5. **Clarity:** Command names are more specific and less ambiguous

## Testing

Unit tests have been added for the new command structure:

```bash
# Run all tests
cd cmd/swarmcracker
go test -v

# Run specific test suite
go test -v -run TestClusterCommandStructure
go test -v -run TestServiceCommandStructure
go test -v -run TestVMCommandStructure
go test -v -run TestNodeCommandStructure
```

## Implementation Notes

### Backward Compatibility

- Legacy commands are implemented as wrappers that show deprecation warnings
- These wrappers delegate to the new command implementations
- All existing functionality is preserved

### Code Organization

- `cmd_cluster.go` - Cluster lifecycle commands
- `cmd_node.go` - Node management commands
- `cmd_service.go` - Service management commands
- `cmd_task.go` - Task management commands
- `cmd_vm.go` - VM operations commands
- `cmd_network.go` - Network configuration commands
- `cmd_asset.go` - Asset management commands
- `cmd_config.go` - Configuration management commands
- `cmd_deprecated.go` - Backward compatibility wrappers

### Testing

- `cmd_cluster_test.go` - Cluster command tests
- `cmd_service_test.go` - Service command tests
- `cmd_vm_test.go` - VM command tests
- `cmd_node_test.go` - Node command tests

## Future Work

### Phase 2

- [ ] Complete implementation of all command handlers
- [ ] Add integration tests
- [ ] Update all documentation
- [ ] Remove deprecated commands after migration period

### Phase 3

- [ ] Add shell completion support
- [ ] Add interactive mode
- [ ] Add command aliases for common operations
- [ ] Performance optimizations

## Contributing

When adding new commands:

1. Choose the appropriate command group
2. Follow the existing command structure pattern
3. Add unit tests in `cmd_<group>_test.go`
4. Update this documentation
5. Ensure backward compatibility if replacing a legacy command

## Support

For issues or questions:

- GitHub Issues: https://github.com/restuhaqza/swarmcracker/issues
- Documentation: https://github.com/restuhaqza/swarmcracker/docs
