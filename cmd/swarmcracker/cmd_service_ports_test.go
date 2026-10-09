package main

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildEndpointPorts(t *testing.T) {
	got := buildEndpointPorts([]types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
		{Protocol: "udp", TargetPort: 53, PublishedPort: 5353},
	}, types.PublishModeHost)
	require.Len(t, got, 2)
	assert.Equal(t, api.ProtocolTCP, got[0].Protocol)
	assert.Equal(t, uint32(8080), got[0].PublishedPort)
	assert.Equal(t, uint32(80), got[0].TargetPort)
	assert.Equal(t, api.PublishModeHost, got[0].PublishMode)
	assert.Equal(t, api.ProtocolUDP, got[1].Protocol)
}

func TestBuildEndpointPorts_IngressDefault(t *testing.T) {
	got := buildEndpointPorts([]types.PublishedPort{
		{Protocol: "tcp", TargetPort: 80, PublishedPort: 8080},
	}, types.PublishModeIngress)
	require.Len(t, got, 1)
	assert.Equal(t, api.PublishModeIngress, got[0].PublishMode)
}

func TestBuildEndpointPorts_Empty(t *testing.T) {
	assert.Empty(t, buildEndpointPorts(nil, types.PublishModeIngress))
}

func TestFormatServicePorts(t *testing.T) {
	assert.Equal(t, "-", formatServicePorts(&api.Service{}))

	svc := &api.Service{
		Spec: api.ServiceSpec{
			Endpoint: &api.EndpointSpec{Ports: []*api.PortConfig{
				{Protocol: api.ProtocolTCP, TargetPort: 80, PublishedPort: 8080},
				{Protocol: api.ProtocolUDP, TargetPort: 53, PublishedPort: 5353},
			}},
		},
	}
	assert.Equal(t, "8080:80/tcp,5353:53/udp", formatServicePorts(svc))
}
