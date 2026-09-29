package cni

import (
	"net"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ===== Service Allocation =====

func TestCNINetworkAllocator_AllocateService(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated before the service")

	// A second, ingress network so the published-ports branch of AllocateService
	// has somewhere to store the service.
	ingress := &api.Network{
		ID: "net-ingress",
		Spec: api.NetworkSpec{
			Annotations: api.Annotations{Name: "ingress"},
			Ingress:     true,
		},
	}
	require.NoError(t, allocator.Allocate(ingress), "ingress network must be allocated")

	service := &api.Service{
		ID: "svc-1",
		Spec: api.ServiceSpec{
			Networks: []*api.NetworkAttachmentConfig{{Target: "net-1"}},
			Endpoint: &api.EndpointSpec{
				Ports: []*api.PortConfig{
					{TargetPort: 80, PublishedPort: 8080, Protocol: api.ProtocolTCP, PublishMode: api.PublishModeIngress},
				},
			},
		},
	}

	require.False(t, allocator.IsServiceAllocated(service), "service should not be allocated before AllocateService")

	err := allocator.AllocateService(service)
	require.NoError(t, err, "AllocateService should succeed")

	assert.True(t, allocator.IsServiceAllocated(service), "service should be allocated after AllocateService")

	// VIP is stored against the attached network.
	allocatedNet, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "allocated network should be retrievable")
	require.Contains(t, allocatedNet.Services, "svc-1", "service VIP should be recorded on the attached network")

	vip := allocatedNet.Services["svc-1"]
	require.NotNil(t, vip, "stored service VIP should not be nil")
	assert.NotNil(t, vip.VIP, "a VIP should have been assigned")
	assert.NotNil(t, net.ParseIP(vip.VIP.String()), "VIP should parse as an IP")
	assert.Equal(t, "svc-1", vip.ServiceID, "VIP record should carry the service ID")
	assert.Equal(t, "net-1", vip.NetworkID, "VIP record should carry the network ID")
	assert.False(t, vip.AllocatedAt.IsZero(), "VIP record should have an allocation timestamp")
	require.Len(t, vip.PublishedPorts, 1, "published ports should be parsed onto the VIP")
	assert.Equal(t, uint32(80), vip.PublishedPorts[0].Port, "published port target should be preserved")

	// The service is also recorded on the ingress network because it exposes ports.
	allocatedIngress, err := allocator.GetAllocatedNetwork("net-ingress")
	require.NoError(t, err, "ingress network should be retrievable")
	require.Contains(t, allocatedIngress.Services, "svc-1", "service should be recorded on the ingress network")
	assert.Len(t, allocatedIngress.Services["svc-1"].PublishedPorts, 1, "ingress record should carry published ports")
}

func TestCNINetworkAllocator_AllocateService_NoNetworks(t *testing.T) {
	allocator := newTestAllocator(t)

	service := &api.Service{
		ID:   "svc-empty",
		Spec: api.ServiceSpec{},
	}

	err := allocator.AllocateService(service)
	assert.NoError(t, err, "AllocateService with no networks and no endpoint should be a no-op success")

	// IsServiceAllocated is vacuously true for a service with zero networks:
	// the loop has nothing to disprove, so no network state was required.
	assert.True(t, allocator.IsServiceAllocated(service), "a service with no networks is vacuously allocated")
}

func TestCNINetworkAllocator_IsServiceAllocated_Nil(t *testing.T) {
	allocator := newTestAllocator(t)

	assert.False(t, allocator.IsServiceAllocated(nil), "IsServiceAllocated(nil) should be false")
}

func TestCNINetworkAllocator_DeallocateService(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated")

	service := &api.Service{
		ID: "svc-del",
		Spec: api.ServiceSpec{
			Networks: []*api.NetworkAttachmentConfig{{Target: "net-1"}},
		},
	}
	require.NoError(t, allocator.AllocateService(service), "AllocateService should succeed")
	require.True(t, allocator.IsServiceAllocated(service), "service should be allocated before deallocation")

	err := allocator.DeallocateService(service)
	require.NoError(t, err, "DeallocateService should succeed")
	assert.False(t, allocator.IsServiceAllocated(service), "service should no longer be allocated")

	allocatedNet, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "allocated network should still exist")
	assert.NotContains(t, allocatedNet.Services, "svc-del", "VIP record should be removed on deallocation")
}

// ===== Task Allocation =====

func TestCNINetworkAllocator_AllocateTask(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated before the task")

	task := &api.Task{
		ID:       "task-1",
		Networks: []*api.NetworkAttachment{{Network: network}},
	}

	require.False(t, allocator.IsTaskAllocated(task), "task should not be allocated before AllocateTask")

	err := allocator.AllocateTask(task)
	require.NoError(t, err, "AllocateTask should succeed")

	require.Len(t, task.Networks[0].Addresses, 1, "task attachment should get exactly one address")
	ip := net.ParseIP(task.Networks[0].Addresses[0])
	assert.NotNil(t, ip, "task address should parse as an IP")
	assert.True(t, allocator.IsTaskAllocated(task), "task should be allocated after AllocateTask")

	// A task attached to a network that was never allocated must fail.
	orphan := &api.Task{
		ID:       "task-orphan",
		Networks: []*api.NetworkAttachment{{Network: &api.Network{ID: "missing"}}},
	}
	assert.Error(t, allocator.AllocateTask(orphan), "AllocateTask should fail for an unallocated network")
}

func TestCNINetworkAllocator_DeallocateTask(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated")

	task := &api.Task{
		ID:       "task-1",
		Networks: []*api.NetworkAttachment{{Network: network}},
	}
	require.NoError(t, allocator.AllocateTask(task), "AllocateTask should succeed")
	require.True(t, allocator.IsTaskAllocated(task), "task should be allocated before deallocation")

	err := allocator.DeallocateTask(task)
	require.NoError(t, err, "DeallocateTask should succeed")

	assert.Empty(t, task.Networks[0].Addresses, "task addresses should be cleared after deallocation")
	assert.False(t, allocator.IsTaskAllocated(task), "task should no longer be allocated")
}

// ===== Node Attachment Allocation =====

func TestCNINetworkAllocator_AllocateAttachment_Unit(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated before the attachment")

	node := &api.Node{ID: "node-1"}
	na := &api.NetworkAttachment{Network: network}

	require.False(t, allocator.IsAttachmentAllocated(node, na), "attachment should not be allocated beforehand")

	err := allocator.AllocateAttachment(node, na)
	require.NoError(t, err, "AllocateAttachment should succeed")

	require.Len(t, na.Addresses, 1, "attachment should get exactly one address")
	ip := net.ParseIP(na.Addresses[0])
	require.NotNil(t, ip, "attachment address should parse as an IP")

	allocatedNet, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "allocated network should be retrievable")

	require.Contains(t, allocatedNet.Attachments, "node-1", "node attachment record should be stored")
	stored := allocatedNet.Attachments["node-1"]
	require.NotNil(t, stored, "stored attachment should not be nil")
	assert.Equal(t, "node-1", stored.NodeID, "stored attachment should carry the node ID")
	assert.Equal(t, "net-1", stored.NetworkID, "stored attachment should carry the network ID")
	assert.NotNil(t, stored.IPAddress, "stored attachment should carry the allocated IP")
	assert.Equal(t, ip.String(), stored.IPAddress.String(), "stored IP should match the attachment address")
	// MACAddress is currently never generated by AllocateAttachment; document that.
	assert.Empty(t, stored.MACAddress, "MACAddress is not assigned by the current implementation")
	assert.False(t, stored.AllocatedAt.IsZero(), "stored attachment should have an allocation timestamp")

	assert.True(t, allocator.IsAttachmentAllocated(node, na), "attachment should be allocated after AllocateAttachment")
}

func TestCNINetworkAllocator_DeallocateAttachment(t *testing.T) {
	allocator := newTestAllocator(t)

	network := testNetwork()
	require.NoError(t, allocator.Allocate(network), "network must be allocated")

	node := &api.Node{ID: "node-1"}
	na := &api.NetworkAttachment{Network: network}
	require.NoError(t, allocator.AllocateAttachment(node, na), "AllocateAttachment should succeed")
	require.True(t, allocator.IsAttachmentAllocated(node, na), "attachment should be allocated before deallocation")

	err := allocator.DeallocateAttachment(node, na)
	require.NoError(t, err, "DeallocateAttachment should succeed")

	assert.False(t, allocator.IsAttachmentAllocated(node, na), "attachment should no longer be allocated")
	assert.Empty(t, na.Addresses, "attachment addresses should be cleared")
	assert.Nil(t, na.DriverAttachmentOpts, "driver attachment opts should be cleared")

	allocatedNet, err := allocator.GetAllocatedNetwork("net-1")
	require.NoError(t, err, "allocated network should still exist")
	assert.NotContains(t, allocatedNet.Attachments, "node-1", "attachment record should be removed")
}

// ===== parsePublishedPorts =====

func TestParsePublishedPorts(t *testing.T) {
	tests := []struct {
		name    string
		service *api.Service
		want    []PublishedPort
	}{
		{
			name:    "nil endpoint",
			service: &api.Service{},
			want:    nil,
		},
		{
			name: "no ports",
			service: &api.Service{
				Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{}},
			},
			want: []PublishedPort{},
		},
		{
			name: "tcp ingress",
			service: &api.Service{
				Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{
					Ports: []*api.PortConfig{
						{TargetPort: 80, PublishedPort: 8080, Protocol: api.ProtocolTCP, PublishMode: api.PublishModeIngress},
					},
				}},
			},
			want: []PublishedPort{
				{Port: 80, PublishedPort: 8080, Protocol: "TCP", PublishMode: "INGRESS"},
			},
		},
		{
			name: "udp ingress",
			service: &api.Service{
				Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{
					Ports: []*api.PortConfig{
						{TargetPort: 53, PublishedPort: 5353, Protocol: api.ProtocolUDP, PublishMode: api.PublishModeIngress},
					},
				}},
			},
			want: []PublishedPort{
				{Port: 53, PublishedPort: 5353, Protocol: "UDP", PublishMode: "INGRESS"},
			},
		},
		{
			name: "tcp host mode",
			service: &api.Service{
				Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{
					Ports: []*api.PortConfig{
						{TargetPort: 443, PublishedPort: 8443, Protocol: api.ProtocolTCP, PublishMode: api.PublishModeHost},
					},
				}},
			},
			want: []PublishedPort{
				{Port: 443, PublishedPort: 8443, Protocol: "TCP", PublishMode: "HOST"},
			},
		},
		{
			name: "multiple ports",
			service: &api.Service{
				Spec: api.ServiceSpec{Endpoint: &api.EndpointSpec{
					Ports: []*api.PortConfig{
						{TargetPort: 80, PublishedPort: 8080, Protocol: api.ProtocolTCP, PublishMode: api.PublishModeIngress},
						{TargetPort: 53, PublishedPort: 5353, Protocol: api.ProtocolUDP, PublishMode: api.PublishModeHost},
					},
				}},
			},
			want: []PublishedPort{
				{Port: 80, PublishedPort: 8080, Protocol: "TCP", PublishMode: "INGRESS"},
				{Port: 53, PublishedPort: 5353, Protocol: "UDP", PublishMode: "HOST"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePublishedPorts(tt.service)

			if tt.want == nil {
				assert.Nil(t, got, "nil Endpoint should yield nil ports")
				return
			}

			require.Len(t, got, len(tt.want), "port count should match")
			for i := range tt.want {
				assert.Equal(t, tt.want[i].Port, got[i].Port, "Port should match at index %d", i)
				assert.Equal(t, tt.want[i].PublishedPort, got[i].PublishedPort, "PublishedPort should match at index %d", i)
				assert.Equal(t, tt.want[i].Protocol, got[i].Protocol, "Protocol should match at index %d", i)
				assert.Equal(t, tt.want[i].PublishMode, got[i].PublishMode, "PublishMode should match at index %d", i)
			}
		})
	}
}
