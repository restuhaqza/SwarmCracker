// Package cni provides CNI-based network allocation for SwarmKit.
//
// This package implements the SwarmKit NetworkAllocator interface using
// standard CNI plugins (bridge, vxlan, host-local IPAM) to enable
// distributed network allocation for swarmd-firecracker clusters.
package cni

import (
	"net"
	"sync"
	"time"
)

// Constants for CNI configuration
const (
	// DefaultCNIVersion is the CNI spec version we support
	DefaultCNIVersion = "1.0.0"

	// DefaultBridgeName is the default bridge network name
	DefaultBridgeName = "cni0"

	// DefaultVXLANPort is the default VXLAN UDP port (IANA assigned)
	DefaultVXLANPort = 4789

	// DefaultSubnetPool is the default IP pool for overlay networks
	DefaultSubnetPool = "10.0.0.0/8"

	// DefaultSubnetSize is the default subnet size for networks
	DefaultSubnetSize = 24

	// IngressNetworkName is the name of the ingress network
	IngressNetworkName = "ingress"

	// GWBridgeNetworkName is the name of the gateway bridge
	GWBridgeNetworkName = "docker_gwbridge"

	// DefaultPluginDir is the default CNI plugin binary directory
	DefaultPluginDir = "/opt/cni/bin"

	// DefaultConfigDir is the default CNI configuration directory
	DefaultConfigDir = "/etc/cni/net.d"
)

// CNIConfig holds configuration for the CNI provider
type CNIConfig struct {
	// BridgeName is the name of the default bridge
	BridgeName string

	// SubnetPool is the IP pool for network allocation
	SubnetPool string

	// SubnetSize is the size of subnets allocated from the pool
	SubnetSize int

	// VXLANPort is the UDP port for VXLAN traffic
	VXLANPort uint32

	// IPAMType is the IPAM driver type (default: host-local)
	IPAMType string

	// PluginDir is the directory containing CNI plugin binaries
	PluginDir string

	// ConfigDir is the directory for CNI network configurations
	ConfigDir string

	// EnableIPMasq enables IP masquerading for external traffic
	EnableIPMasq bool
}

// DefaultCNIConfig returns a default CNI configuration
func DefaultCNIConfig() *CNIConfig {
	return &CNIConfig{
		BridgeName:   DefaultBridgeName,
		SubnetPool:   DefaultSubnetPool,
		SubnetSize:   DefaultSubnetSize,
		VXLANPort:    DefaultVXLANPort,
		IPAMType:     "host-local",
		PluginDir:    DefaultPluginDir,
		ConfigDir:    DefaultConfigDir,
		EnableIPMasq: true,
	}
}

// AllocatedNetwork represents an allocated network
type AllocatedNetwork struct {
	// ID is the SwarmKit network ID
	ID string

	// Name is the network name
	Name string

	// Driver is the network driver type (bridge, vxlan)
	Driver string

	// Subnet is the allocated IP subnet
	Subnet *net.IPNet

	// Gateway is the gateway IP address
	Gateway net.IP

	// VXLANID is the VXLAN network identifier (for overlay networks)
	VXLANID uint32

	// BridgeName is the bridge device name
	BridgeName string

	// Ingress indicates if this is an ingress network
	Ingress bool

	// Attachments holds node attachments to this network
	Attachments map[string]*NodeAttachment

	// Services holds VIP allocations for services
	Services map[string]*ServiceVIP

	// CreatedAt is when the network was allocated
	CreatedAt time.Time

	// mu protects concurrent access
	mu sync.RWMutex
}

// NodeAttachment represents a node's attachment to a network
type NodeAttachment struct {
	// NodeID is the SwarmKit node ID
	NodeID string

	// NetworkID is the network ID
	NetworkID string

	// IPAddress is the allocated IP address
	IPAddress net.IP

	// MACAddress is the MAC address (if assigned)
	MACAddress string

	// VXLANVNI is the VXLAN VNI for overlay networks
	VXLANVNI uint32

	// AllocatedAt is when the attachment was allocated
	AllocatedAt time.Time
}

// ServiceVIP represents a VIP allocation for a service
type ServiceVIP struct {
	// ServiceID is the SwarmKit service ID
	ServiceID string

	// NetworkID is the network ID
	NetworkID string

	// VIP is the virtual IP address
	VIP net.IP

	// PublishedPorts holds published port mappings
	PublishedPorts []PublishedPort

	// AllocatedAt is when the VIP was allocated
	AllocatedAt time.Time
}

// PublishedPort represents a published port mapping
type PublishedPort struct {
	// Port is the internal port
	Port uint32

	// PublishedPort is the externally published port
	PublishedPort uint32

	// Protocol is TCP or UDP
	Protocol string

	// PublishMode is ingress or host mode
	PublishMode string
}

// IPPool represents an IP allocation pool
type IPPool struct {
	// Subnet is the subnet for this pool
	Subnet *net.IPNet

	// Gateway is the gateway address
	Gateway net.IP

	// UsedIPs maps allocated IPs to their owner IDs
	UsedIPs map[string]string // IP -> owner ID

	// ReservedIPs are IPs reserved for special use
	ReservedIPs []net.IP

	// NextIP is the next IP to try for allocation
	NextIP net.IP

	// mu protects concurrent access
	mu sync.RWMutex
}

// CNIExecResult holds results from CNI plugin execution
type CNIExecResult struct {
	// Interfaces are the created network interfaces
	Interfaces []CNIInterface `json:"interfaces,omitempty"`

	// IPs are the allocated IP addresses
	IPs []CNIIPConfig `json:"ips,omitempty"`

	// Routes are the configured routes
	Routes []CNIRoute `json:"routes,omitempty"`

	// DNS is the DNS configuration
	DNS CNIDNS `json:"dns,omitempty"`
}

// CNIInterface represents a network interface created by CNI
type CNIInterface struct {
	// Name is the interface name
	Name string `json:"name"`

	// MAC is the MAC address
	MAC string `json:"mac,omitempty"`

	// Sandbox is the sandbox path (container network namespace)
	Sandbox string `json:"sandbox,omitempty"`
}

// CNIIPConfig represents an IP configuration
type CNIIPConfig struct {
	// Interface is the index of the interface this IP belongs to
	Interface int `json:"interface,omitempty"`

	// Address is the IP address with CIDR
	Address string `json:"address"`

	// Gateway is the gateway for this IP
	Gateway string `json:"gateway,omitempty"`
}

// CNIRoute represents a network route
type CNIRoute struct {
	// Destination is the destination network
	Destination string `json:"dst"`

	// Gateway is the route gateway
	Gateway string `json:"gw,omitempty"`
}

// CNIDNS represents DNS configuration
type CNIDNS struct {
	// Nameservers are the DNS server addresses
	Nameservers []string `json:"nameservers,omitempty"`

	// Domain is the search domain
	Domain string `json:"domain,omitempty"`

	// Search is the search list
	Search []string `json:"search,omitempty"`

	// Options are DNS options
	Options []string `json:"options,omitempty"`
}

// CNINetworkConfig represents a CNI network configuration file
type CNINetworkConfig struct {
	// CNIVersion is the CNI spec version
	CNIVersion string `json:"cniVersion"`

	// Name is the network name
	Name string `json:"name"`

	// Type is the plugin type (bridge, vxlan, etc.)
	Type string `json:"type"`

	// Additional fields are driver-specific
	// These are stored as raw JSON to support arbitrary plugin configs
	RawConfig map[string]interface{} `json:"-"`
}