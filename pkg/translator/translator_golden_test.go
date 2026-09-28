package translator

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
)

func TestBuildBootArgs_PrebuiltGolden(t *testing.T) {
	tr := NewTaskTranslator(&Config{NetworkConfig: types.NetworkConfig{BridgeIP: "192.168.127.1/24"}})

	task := &types.Task{
		ID:   "golden-task",
		Spec: types.TaskSpec{Runtime: &types.Container{Image: "golden:ubuntu-24.04-docker"}},
		Annotations: map[string]string{
			types.AnnotationPrebuiltRootfs: "true",
			types.AnnotationBootArgs:       "systemd.unified_cgroup_hierarchy=1 systemd.journald.forward_to_console=1",
		},
		Networks: []types.NetworkAttachment{
			{
				Network:   types.Network{ID: "net-1"},
				Addresses: []string{"192.168.127.50/24"},
			},
		},
	}

	got := tr.buildBootArgs(task)

	assert.Contains(t, got, "console=ttyS0")
	assert.Contains(t, got, "init=/sbin/init")
	assert.Contains(t, got, "nomodules")
	assert.Contains(t, got, "systemd.unified_cgroup_hierarchy=1")
	assert.Contains(t, got, "systemd.journald.forward_to_console=1")
	assert.Contains(t, got, "ip=192.168.127.50::192.168.127.1:255.255.255.0::eth0:off")
	assert.NotContains(t, got, "--", "a golden VM must not append an OCI command after '--'")
}

func TestBuildBootArgs_PrebuiltGolden_NoExtraArgs(t *testing.T) {
	tr := NewTaskTranslator(nil)
	task := &types.Task{
		ID:   "g",
		Spec: types.TaskSpec{Runtime: &types.Container{Image: "golden:x"}},
		Annotations: map[string]string{
			types.AnnotationPrebuiltRootfs: "true",
		},
	}

	got := tr.buildBootArgs(task)
	assert.Contains(t, got, "init=/sbin/init")
	assert.Contains(t, got, "ip=dhcp")
	assert.NotContains(t, got, "--")
}
