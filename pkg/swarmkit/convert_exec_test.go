package swarmkit

import (
	"context"
	"os"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	configstore "github.com/restuhaqza/swarmcracker/pkg/swarmkit/configs"
	secretstore "github.com/restuhaqza/swarmcracker/pkg/swarmkit/secrets"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestController_convertSecrets_WithData(t *testing.T) {
	store := secretstore.NewStore()
	store.Add(api.Secret{ID: "s1", Spec: api.SecretSpec{Data: []byte("hunter2")}})

	ctrl := &Controller{
		task: &api.Task{
			Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
				Secrets: []*api.SecretReference{{
					SecretID:   "s1",
					SecretName: "db-pass",
					Target: &api.SecretReference_File{
						File: &api.FileTarget{Name: "/run/secrets/db", Mode: 0o400, UID: "1000"},
					},
				}},
			}}},
		},
		secrets: store,
	}

	refs := ctrl.convertSecrets()
	require.Len(t, refs, 1)
	assert.Equal(t, []byte("hunter2"), refs[0].Data, "payload must come from the store")
	assert.Equal(t, "/run/secrets/db", refs[0].Target)
	assert.Equal(t, os.FileMode(0o400), refs[0].Mode)
	assert.Equal(t, "1000", refs[0].UID)
}

func TestController_convertConfigs_WithData(t *testing.T) {
	store := configstore.NewStore()
	store.Add(api.Config{ID: "c1", Spec: api.ConfigSpec{Data: []byte("key: value")}})

	ctrl := &Controller{
		task: &api.Task{
			Spec: api.TaskSpec{Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
				Configs: []*api.ConfigReference{{
					ConfigID:   "c1",
					ConfigName: "app-yaml",
					Target: &api.ConfigReference_File{
						File: &api.FileTarget{Name: "/config/app.yaml", Mode: 0o444},
					},
				}},
			}}},
		},
		configs: store,
	}

	refs := ctrl.convertConfigs()
	require.Len(t, refs, 1)
	assert.Equal(t, []byte("key: value"), refs[0].Data)
	assert.Equal(t, "/config/app.yaml", refs[0].Target)
	assert.Equal(t, os.FileMode(0o444), refs[0].Mode)
}

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

// TestController_convertTask_LocalGatewayForManagedNetwork covers a multi-node
// configuration: the attachment's gateway is the address this node holds on the
// network, so the guest's default route follows the node it runs on. The single-node
// case — and the nil-config safety — is covered by
// TestController_convertTask_NetworkAllocation above, which keeps the cluster-wide
// gateway.
func TestController_convertTask_LocalGatewayForManagedNetwork(t *testing.T) {
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
		config: &Config{BridgeIP: "192.168.127.2/24", VXLANPeers: []string{"192.168.127.1"}},
	}

	internal := ctrl.convertTask()
	require.Len(t, internal.Networks, 1)
	assert.Equal(t, "10.10.0.2", internal.Networks[0].Network.Spec.Gateway,
		"a node's guests use its own address on the network")
}

func TestController_convertTask_RegistryAuth(t *testing.T) {
	const payload = "eyJ1c2VybmFtZSI6ImJvYiIsInBhc3N3b3JkIjoic2VjcmV0In0="

	tests := []struct {
		name     string
		pullOpts *api.ContainerSpec_PullOptions
		expected string
	}{
		{
			name:     "carries the service spec credential",
			pullOpts: &api.ContainerSpec_PullOptions{RegistryAuth: payload},
			expected: payload,
		},
		{
			name:     "tolerates nil PullOptions",
			pullOpts: nil,
			expected: "",
		},
		{
			name:     "tolerates an empty payload",
			pullOpts: &api.ContainerSpec_PullOptions{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := &Controller{
				task: &api.Task{
					ID: "t1",
					Spec: api.TaskSpec{
						Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
							Image:       "private.example.com/app:1",
							PullOptions: tt.pullOpts,
						}},
					},
				},
			}

			container, err := ctrl.convertTask().Spec.GetContainer()
			require.NoError(t, err)
			assert.Equal(t, tt.expected, container.RegistryAuth)
		})
	}
}

// TestController_convertTask_RegistryAuthReachesPreparer proves the copy survives
// to the image preparer: the preparer test double observes the same task, with
// the credential attached, that convertTask produced.
func TestController_convertTask_RegistryAuthReachesPreparer(t *testing.T) {
	const payload = "eyJ1c2VybmFtZSI6ImJvYiJ9"

	ctrl := &Controller{
		task: &api.Task{
			ID: "t1",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
					Image:       "private.example.com/app:1",
					PullOptions: &api.ContainerSpec_PullOptions{RegistryAuth: payload},
				}},
			},
		},
	}

	var seen *types.Task
	prep := &MockImagePreparer{
		PrepareFunc: func(_ context.Context, task *types.Task) error {
			seen = task
			return nil
		},
	}

	internal := ctrl.convertTask()
	require.NoError(t, prep.Prepare(context.Background(), internal))
	require.NotNil(t, seen)
	container, err := seen.Spec.GetContainer()
	require.NoError(t, err)
	assert.Equal(t, payload, container.RegistryAuth)
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

// TestController_convertTask_CopiesResourceLimits verifies the agent carries
// both arms of the API resource requirements: the CLI writes Limits, so a
// conversion that dropped it left the VM silently at its defaults (#95).
func TestController_convertTask_CopiesResourceLimits(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "t-resources",
			Spec: api.TaskSpec{
				Resources: &api.ResourceRequirements{
					Limits: &api.Resources{
						NanoCPUs:    2e9,
						MemoryBytes: 1024 * 1024 * 1024,
					},
					Reservations: &api.Resources{
						NanoCPUs:    1e9,
						MemoryBytes: 256 * 1024 * 1024,
					},
				},
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "app"},
				},
			},
		},
	}

	internal := ctrl.convertTask()

	require.NotNil(t, internal.Spec.Resources.Limits, "an explicit limit must survive conversion")
	assert.Equal(t, int64(2e9), internal.Spec.Resources.Limits.NanoCPUs)
	assert.Equal(t, int64(1024*1024*1024), internal.Spec.Resources.Limits.MemoryBytes)

	require.NotNil(t, internal.Spec.Resources.Reservations, "a reservation must still survive conversion")
	assert.Equal(t, int64(1e9), internal.Spec.Resources.Reservations.NanoCPUs)
	assert.Equal(t, int64(256*1024*1024), internal.Spec.Resources.Reservations.MemoryBytes)
}

// TestController_convertTask_LimitsWithoutReservations covers the CLI's actual
// shape: service create writes Limits alone and no reservation.
func TestController_convertTask_LimitsWithoutReservations(t *testing.T) {
	ctrl := &Controller{
		task: &api.Task{
			ID: "t-limits-only",
			Spec: api.TaskSpec{
				Resources: &api.ResourceRequirements{
					Limits: &api.Resources{
						NanoCPUs:    3e9,
						MemoryBytes: 1024 * 1024 * 1024,
					},
				},
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "app"},
				},
			},
		},
	}

	internal := ctrl.convertTask()

	require.NotNil(t, internal.Spec.Resources.Limits)
	assert.Equal(t, int64(3e9), internal.Spec.Resources.Limits.NanoCPUs)
	assert.Equal(t, int64(1024*1024*1024), internal.Spec.Resources.Limits.MemoryBytes)
	assert.Nil(t, internal.Spec.Resources.Reservations)
}
