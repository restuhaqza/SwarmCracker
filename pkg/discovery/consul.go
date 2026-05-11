// Package discovery provides service discovery for SwarmCracker nodes.
package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
)

// ConsulClient implements NodeDiscovery using Consul service registry.
type ConsulClient struct {
	client        *api.Client
	serviceID     string
	localIP       string
	localHostname string
	vxlanPort     int
}

// ConsulConfig holds Consul client configuration.
type ConsulConfig struct {
	Address       string // Consul agent address (e.g., "127.0.0.1:8500")
	ServiceID     string // Unique service ID (hostname)
	LocalIP       string // This node's IP for VXLAN
	LocalHostname string
	VXLANPort     int    // VXLAN UDP port (default: 4789)
	Token         string // ACL token (optional)
	UseTLS        bool   // Enable TLS for Consul communication
	TLSCertFile   string // Path to TLS certificate file
	TLSKeyFile    string // Path to TLS key file
	TLSCAFile     string // Path to TLS CA certificate file (optional)
}

// NewConsulClient creates a new Consul discovery client.
func NewConsulClient(cfg ConsulConfig) (*ConsulClient, error) {
	if cfg.Address == "" {
		cfg.Address = "127.0.0.1:8500"
	}
	if cfg.VXLANPort == 0 {
		cfg.VXLANPort = 4789
	}

	config := api.DefaultConfig()
	config.Address = cfg.Address
	if cfg.Token != "" {
		config.Token = cfg.Token
	}
	if cfg.UseTLS {
		config.Scheme = "https"
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			config.TLSConfig.Address = cfg.Address
			config.TLSConfig.CertFile = cfg.TLSCertFile
			config.TLSConfig.KeyFile = cfg.TLSKeyFile
			if cfg.TLSCAFile != "" {
				config.TLSConfig.CAFile = cfg.TLSCAFile
			}
		}
	}

	client, err := api.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Consul client: %w", err)
	}

	return &ConsulClient{
		client:        client,
		serviceID:     cfg.ServiceID,
		localIP:       cfg.LocalIP,
		localHostname: cfg.LocalHostname,
		vxlanPort:     cfg.VXLANPort,
	}, nil
}

// RegisterService registers this node as a VXLAN-capable service.
func (c *ConsulClient) RegisterService(vxlanID int, bridgeIP string) error {
	service := &api.AgentServiceRegistration{
		ID:      c.serviceID,
		Name:    "swarmcracker-vxlan",
		Address: c.localIP,
		Port:    c.vxlanPort,
		Tags:    []string{"vxlan", "swarmcracker"},
		Meta: map[string]string{
			"vxlan_id":  fmt.Sprintf("%d", vxlanID),
			"bridge_ip": bridgeIP,
			"hostname":  c.localHostname,
			"status":    "ready",
		},
		// Skip health check for UDP port - can't check UDP with TCP
	}

	return c.client.Agent().ServiceRegister(service)
}

// DeregisterService removes this node from the service registry.
func (c *ConsulClient) DeregisterService() error {
	return c.client.Agent().ServiceDeregister(c.serviceID)
}

// GetNodes returns all VXLAN-capable nodes from Consul.
func (c *ConsulClient) GetNodes() ([]types.NodeInfo, error) {
	// Query catalog directly without health filter
	services, _, err := c.client.Catalog().Service("swarmcracker-vxlan", "", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query services: %w", err)
	}

	nodes := []types.NodeInfo{}
	for _, entry := range services {
		if entry.ServiceID == c.serviceID {
			continue // Skip ourselves
		}

		vxlanIP := entry.ServiceAddress
		if vxlanIP == "" {
			vxlanIP = entry.Address
		}

		nodes = append(nodes, types.NodeInfo{
			ID:       entry.ServiceID,
			IP:       vxlanIP,
			VXLANIP:  vxlanIP,
			Status:   "ready",
			Hostname: entry.ServiceMeta["hostname"],
		})
	}

	return nodes, nil
}

// WatchPeers watches for peer changes and calls callback.
func (c *ConsulClient) WatchPeers(ctx context.Context, callback func(peers []string)) {
	lastIndex := uint64(0)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			opts := &api.QueryOptions{
				WaitIndex: lastIndex,
				WaitTime:  30 * time.Second,
			}

			// Query catalog service directly (not health) since VXLAN UDP checks may fail
			services, meta, err := c.client.Catalog().Service("swarmcracker-vxlan", "", opts)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}

			lastIndex = meta.LastIndex

			peers := []string{}
			for _, entry := range services {
				if entry.ServiceID == c.serviceID {
					continue
				}
				if entry.ServiceAddress != "" {
					peers = append(peers, entry.ServiceAddress)
				} else if entry.Address != "" {
					peers = append(peers, entry.Address)
				}
			}

			if len(peers) > 0 {
				callback(peers)
			}
		}
	}()
}

// Close closes the Consul client.
func (c *ConsulClient) Close() error {
	return c.DeregisterService()
}