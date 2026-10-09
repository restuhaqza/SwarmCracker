package swarmkit

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
