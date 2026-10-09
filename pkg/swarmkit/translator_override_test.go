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
}
