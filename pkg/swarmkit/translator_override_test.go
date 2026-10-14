package swarmkit

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTranslatorImpl_NetworkGateway(t *testing.T) {
	tr, err := NewTaskTranslator("/kernel", "192.168.127.1")
	require.NoError(t, err)
	impl := tr.(*taskTranslatorImpl)

	task := &types.Task{
		ID:   "t1",
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Networks: []types.NetworkAttachment{{
			Network: types.Network{
				ID:   "n1",
				Spec: types.NetworkSpec{Subnet: "10.10.0.0/24", Gateway: "10.10.0.1"},
			},
			Addresses: []string{"10.10.0.17/24"},
		}},
	}

	got := impl.buildBootArgs(task)
	assert.Contains(t, got, "ip=10.10.0.17::10.10.0.1:255.255.255.0::eth0:off",
		"a user-defined network's own gateway and mask must be used")
}

func TestTaskTranslatorImpl_MultipleNetworks(t *testing.T) {
	tr, err := NewTaskTranslator("/kernel", "192.168.127.1")
	require.NoError(t, err)
	impl := tr.(*taskTranslatorImpl)

	task := &types.Task{
		ID:   "t1",
		Spec: types.TaskSpec{Runtime: &types.Container{}},
		Networks: []types.NetworkAttachment{
			{
				Network:   types.Network{ID: "n1", Spec: types.NetworkSpec{}},
				Addresses: []string{"192.168.127.20/24"},
			},
			{
				Network:   types.Network{ID: "n2", Spec: types.NetworkSpec{Subnet: "10.10.0.0/24", Gateway: "10.10.0.1"}},
				Addresses: []string{"10.10.0.17/24"},
			},
		},
	}

	got := impl.buildBootArgs(task)
	// eth0 gets the default gateway; the extra interface carries its address
	// only, so per-network gateways do not fight over the default route.
	assert.Contains(t, got, "ip=192.168.127.20::192.168.127.1:255.255.255.0::eth0:off")
	assert.Contains(t, got, "ip=10.10.0.17:::255.255.255.0::eth1:off")

	// Every attachment gets its own NIC.
	ifaces := impl.buildNetworkInterfaces(task)
	require.Len(t, ifaces, 2)
	assert.Equal(t, "eth0", ifaces[0]["iface_id"])
	assert.Equal(t, "eth1", ifaces[1]["iface_id"])
}

func TestTaskTranslatorImpl_GuestOverrides(t *testing.T) {
	tr, err := NewTaskTranslator("/kernel", "10.0.0.1")
	require.NoError(t, err)
	impl := tr.(*taskTranslatorImpl)

	t.Run("hostname and dns", func(t *testing.T) {
		task := &types.Task{
			ID: "t1",
			Spec: types.TaskSpec{
				Runtime: &types.Container{Hostname: "web-1", DNS: []string{"1.1.1.1", "9.9.9.9"}},
			},
		}
		got := impl.buildBootArgs(task)
		assert.Contains(t, got, "sc.hostname=web-1")
		assert.Contains(t, got, "sc.dns=1.1.1.1,9.9.9.9")
	})

	t.Run("none", func(t *testing.T) {
		task := &types.Task{ID: "t2", Spec: types.TaskSpec{Runtime: &types.Container{}}}
		got := impl.buildBootArgs(task)
		assert.NotContains(t, got, "sc.hostname=")
		assert.NotContains(t, got, "sc.dns=")
	})

	t.Run("bridge resolver is the default", func(t *testing.T) {
		task := &types.Task{
			ID:          "t3",
			Spec:        types.TaskSpec{Runtime: &types.Container{}},
			Annotations: map[string]string{types.AnnotationResolver: "192.168.127.1"},
		}
		got := impl.buildBootArgs(task)
		assert.Contains(t, got, "sc.dns=192.168.127.1",
			"a guest on a bridge with a live resolver must use it")
	})

	t.Run("an explicit --dns beats the bridge resolver", func(t *testing.T) {
		task := &types.Task{
			ID:          "t4",
			Spec:        types.TaskSpec{Runtime: &types.Container{DNS: []string{"1.1.1.1"}}},
			Annotations: map[string]string{types.AnnotationResolver: "192.168.127.1"},
		}
		got := impl.buildBootArgs(task)
		assert.Contains(t, got, "sc.dns=1.1.1.1")
		assert.NotContains(t, got, "192.168.127.1")
	})
}
