package swarmkit

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestController_convertTask_NetworkAllocation(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "t1",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{Image: "nginx"}},
			},
			Networks: []*api.NetworkAttachment{
				{
					Network: &api.Network{
						ID: "net-1",
						Spec: api.NetworkSpec{
							Annotations:  api.Annotations{Name: "backend"},
							DriverConfig: &api.Driver{Name: "vxlan"},
						},
						DriverState: &api.Driver{
							Name:    "vxlan",
							Options: map[string]string{"bridge": "br-backend", "vxlan_vni": "4096"},
						},
						IPAM: &api.IPAMOptions{
							Configs: []*api.IPAMConfig{{Subnet: "10.10.0.0/24", Gateway: "10.10.0.1"}},
						},
					},
					Addresses: []string{"10.10.0.17/24"},
				},
			},
		},
	}

	internal := ctrl.convertTask()
	require.Len(t, internal.Networks, 1)
	got := internal.Networks[0]

	assert.Equal(t, "backend", got.Network.Spec.Name)
	assert.Equal(t, "vxlan", got.Network.Spec.Driver)
	assert.Equal(t, "10.10.0.0/24", got.Network.Spec.Subnet)
	assert.Equal(t, "10.10.0.1", got.Network.Spec.Gateway)
	assert.Equal(t, 4096, got.Network.Spec.VXLANID)
	require.NotNil(t, got.Network.Spec.DriverConfig)
	require.NotNil(t, got.Network.Spec.DriverConfig.Bridge)
	assert.Equal(t, "br-backend", got.Network.Spec.DriverConfig.Bridge.Name)
	assert.Equal(t, []string{"10.10.0.17/24"}, got.Addresses)
}

func TestController_convertTask_HostnameAndDNS(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID:        "t1",
			ServiceID: "svc-1",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{
						Image:    "nginx",
						Hostname: "web-1",
						DNSConfig: &api.ContainerSpec_DNSConfig{
							Nameservers: []string{"1.1.1.1", "8.8.8.8"},
						},
					},
				},
			},
		},
	}

	container, err := ctrl.convertTask().Spec.GetContainer()
	require.NoError(t, err)
	assert.Equal(t, "web-1", container.Hostname)
	assert.Equal(t, []string{"1.1.1.1", "8.8.8.8"}, container.DNS)
}
