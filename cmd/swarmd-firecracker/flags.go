package main

import (
	"github.com/restuhaqza/swarmcracker/pkg/cni"
	"github.com/urfave/cli/v2"
)

// newAppFlags returns the full set of CLI flags for swarmd-firecracker.
func newAppFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "state-dir",
			Aliases: []string{"d"},
			Usage:   "State directory for swarmd",
			Value:   defaultStateDir,
		},
		&cli.StringFlag{
			Name:  "join-addr",
			Usage: "Address of manager to join (format: host:port)",
		},
		&cli.StringFlag{
			Name:  "join-token",
			Usage: "Join token for cluster",
		},
		&cli.StringFlag{
			Name:  "listen-remote-api",
			Usage: "Listen address for remote API (use 127.0.0.1:4242 for worker-only nodes)",
			Value: "0.0.0.0:4242",
		},
		&cli.StringFlag{
			Name:  "listen-control-api",
			Usage: "Path to control API socket",
			Value: "/var/run/swarmkit/swarm.sock",
		},
		&cli.StringFlag{
			Name:  "advertise-remote-api",
			Usage: "Advertise address for remote API",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "hostname",
			Usage: "Hostname for this node",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "manager",
			Usage: "Start as manager (agent by default)",
			Value: false,
		},
		&cli.BoolFlag{
			Name:  "force-new-cluster",
			Usage: "Force new cluster from current state",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "kernel-path",
			Usage: "Path to Firecracker kernel image",
			Value: "/usr/share/firecracker/vmlinux",
		},
		&cli.StringFlag{
			Name:  "rootfs-dir",
			Usage: "Directory for container rootfs",
			Value: "/var/lib/firecracker/rootfs",
		},
		&cli.StringFlag{
			Name:  "socket-dir",
			Usage: "Directory for Firecracker sockets",
			Value: "/var/run/firecracker",
		},
		&cli.IntFlag{
			Name:  "default-vcpus",
			Usage: "Default VCPUs per microVM",
			Value: 1,
		},
		&cli.IntFlag{
			Name:  "default-memory",
			Usage: "Default memory (MB) per microVM",
			Value: 512,
		},
		&cli.StringFlag{
			Name:  "bridge-name",
			Usage: "Bridge name for VM networking",
			Value: "swarm-br0",
		},
		&cli.StringFlag{
			Name:  "subnet",
			Usage: "Subnet for VM IP allocation",
			Value: "192.168.127.0/24",
		},
		&cli.StringFlag{
			Name:  "bridge-ip",
			Usage: "Bridge IP address",
			Value: "192.168.127.1/24",
		},
		&cli.StringFlag{
			Name:  "ip-mode",
			Usage: "IP allocation mode",
			Value: "static",
		},
		&cli.BoolFlag{
			Name:  "nat-enabled",
			Usage: "Enable NAT for internet access",
			Value: true,
		},
		&cli.BoolFlag{
			Name:  "vxlan-enabled",
			Usage: "Enable VXLAN overlay for cross-node VM networking",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "vxlan-peers",
			Usage: "Comma-separated list of VXLAN peer worker IPs (e.g., 192.168.56.12,192.168.56.13)",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "Enable debug logging",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "health-addr",
			Usage: "Address for health check HTTP server",
			Value: "127.0.0.1:8080",
		},
		&cli.BoolFlag{
			Name:  "enable-jailer",
			Usage: "Enable Firecracker jailer for enhanced security isolation",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "jailer-path",
			Usage: "Path to jailer binary",
			Value: "/usr/local/bin/jailer",
		},
		&cli.IntFlag{
			Name:  "jailer-uid",
			Usage: "UID to run jailed Firecracker processes (default: 1000)",
			Value: 1000,
		},
		&cli.IntFlag{
			Name:  "jailer-gid",
			Usage: "GID to run jailed Firecracker processes (default: 1000)",
			Value: 1000,
		},
		&cli.StringFlag{
			Name:  "jailer-chroot-dir",
			Usage: "Base directory for jailer chroots",
			Value: "/var/lib/swarmcracker/jailer",
		},
		&cli.StringFlag{
			Name:  "parent-cgroup",
			Usage: "Parent cgroup for jailer VMs (e.g., firecracker)",
			Value: "firecracker",
		},
		&cli.StringFlag{
			Name:  "cgroup-version",
			Usage: "Cgroup version: v1 or v2 (default: auto-detect)",
			Value: "",
		},
		&cli.BoolFlag{
			Name:  "enable-cgroups",
			Usage: "Enable cgroup resource limits (requires jailer)",
			Value: true,
		},
		// CNI Network Provider flags
		&cli.BoolFlag{
			Name:  "enable-cni",
			Usage: "Enable CNI network provider for SwarmKit network allocation",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "cni-plugin-dir",
			Usage: "Directory containing CNI plugin binaries",
			Value: cni.DefaultPluginDir,
		},
		&cli.StringFlag{
			Name:  "cni-config-dir",
			Usage: "Directory for CNI network configurations",
			Value: cni.DefaultConfigDir,
		},
		&cli.StringFlag{
			Name:  "cni-subnet-pool",
			Usage: "IP pool for CNI network allocation",
			Value: cni.DefaultSubnetPool,
		},
		&cli.IntFlag{
			Name:  "cni-subnet-size",
			Usage: "Subnet size for CNI networks",
			Value: cni.DefaultSubnetSize,
		},
		&cli.IntFlag{
			Name:  "cni-vxlan-port",
			Usage: "VXLAN UDP port for overlay networks",
			Value: cni.DefaultVXLANPort,
		},
		// Consul service discovery
		// Consul service discovery
		&cli.BoolFlag{
			Name:  "consul-enabled",
			Usage: "Enable Consul service discovery for VXLAN peers",
			Value: false,
		},
		&cli.StringFlag{
			Name:  "consul-address",
			Usage: "Consul agent address",
			Value: "127.0.0.1:8500",
		},
		&cli.IntFlag{
			Name:  "heartbeat-tick",
			Usage: "Heartbeat tick in seconds",
			Value: 1,
		},
		&cli.IntFlag{
			Name:  "election-tick",
			Usage: "Election tick in seconds",
			Value: 10,
		},
	}
}
