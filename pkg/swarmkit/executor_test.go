package swarmkit

import (
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
)

func TestController_convertTask_NetworkDriver(t *testing.T) {
	// Setup swarmkit task with network driver info (Spec.DriverConfig)
	skTask1 := &api.Task{
		ID: "test-task-1",
		Networks: []*api.NetworkAttachment{
			{
				Network: &api.Network{
					ID: "net-1",
					Spec: api.NetworkSpec{
						Annotations: api.Annotations{Name: "my-overlay"},
						DriverConfig: &api.Driver{
							Name: "overlay",
						},
					},
				},
				Addresses: []string{"10.0.0.1/24"},
			},
		},
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "nginx",
				},
			},
		},
	}

	ctrl1 := &Controller{
		task: skTask1,
	}

	internalTask1 := ctrl1.convertTask()
	assert.Equal(t, 1, len(internalTask1.Networks))
	assert.Equal(t, "overlay", internalTask1.Networks[0].Network.Spec.Driver)
	assert.Equal(t, "my-overlay", internalTask1.Networks[0].Network.Spec.Name)

	// Setup swarmkit task with network driver info (DriverState - preferred if available)
	skTask2 := &api.Task{
		ID: "test-task-2",
		Networks: []*api.NetworkAttachment{
			{
				Network: &api.Network{
					ID: "net-2",
					Spec: api.NetworkSpec{
						Annotations: api.Annotations{Name: "my-bridge"},
					},
					DriverState: &api.Driver{
						Name: "bridge",
					},
				},
				Addresses: []string{"192.168.1.2/24"},
			},
		},
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "redis",
				},
			},
		},
	}

	ctrl2 := &Controller{
		task: skTask2,
	}

	internalTask2 := ctrl2.convertTask()
	assert.Equal(t, 1, len(internalTask2.Networks))
	assert.Equal(t, "bridge", internalTask2.Networks[0].Network.Spec.Driver)
	assert.Equal(t, "my-bridge", internalTask2.Networks[0].Network.Spec.Name)

	// Default fallback
	skTask3 := &api.Task{
		ID: "test-task-3",
		Networks: []*api.NetworkAttachment{
			{
				Network: &api.Network{
					ID: "net-3",
					Spec: api.NetworkSpec{
						Annotations: api.Annotations{Name: "default-net"},
					},
				},
				Addresses: []string{"172.16.0.2/24"},
			},
		},
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{
					Image: "alpine",
				},
			},
		},
	}

	ctrl3 := &Controller{
		task: skTask3,
	}

	internalTask3 := ctrl3.convertTask()
	assert.Equal(t, 1, len(internalTask3.Networks))
	assert.Equal(t, "bridge", internalTask3.Networks[0].Network.Spec.Driver, "Should default to bridge")
}
