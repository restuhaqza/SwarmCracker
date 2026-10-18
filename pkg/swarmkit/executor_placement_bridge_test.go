package swarmkit

import (
	"context"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/jailer"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// placementBridgeController builds a Controller around a service task whose
// service carries exactly the given labels, using the package's successful mocks
// so Prepare runs its full prepare path. It is the production shape: CLI-set
// labels arrive here as ServiceAnnotations.Labels, never as task annotations.
func placementBridgeController(labels map[string]string) *Controller {
	return &Controller{
		task: &api.Task{
			ID: "task-placement-bridge",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx:latest"},
				},
			},
			ServiceAnnotations: api.Annotations{Labels: labels},
		},
		config:     &Config{RootfsDir: "/tmp"},
		imagePrep:  &mockImagePrepSuccess{},
		networkMgr: &mockNetworkManagerFull{},
		vmmMgr:     &mockVMMManagerSuccess{},
	}
}

// TestPrepare_BridgesAllPlacementLabelsToTaskAnnotations pins the bridge for
// every label a service uses to size or place its VM. Each consumer on the node
// reads the task annotation (translators for balloon/hugepages, the cgroup
// policy for cpuset/NUMA), and a CLI flag only ever becomes a service label, so
// a missing branch here means the flag is accepted and silently ignored.
func TestPrepare_BridgesAllPlacementLabelsToTaskAnnotations(t *testing.T) {
	ctrl := placementBridgeController(map[string]string{
		types.BalloonLabel:   " 256 ",
		types.CpusetLabel:    " 0-2 ",
		types.HugepagesLabel: " true ",
		types.NumaLabel:      " 0-1 ",
	})

	require.NoError(t, ctrl.Prepare(context.Background()))
	require.NotNil(t, ctrl.internalTask)

	annotations := ctrl.internalTask.Annotations
	require.NotNil(t, annotations)
	assert.Equal(t, "256", annotations[types.BalloonLabel])
	assert.Equal(t, "0-2", annotations[types.CpusetLabel])
	assert.Equal(t, "true", annotations[types.HugepagesLabel])
	assert.Equal(t, "0-1", annotations[types.NumaLabel])
}

// TestPrepare_NoPlacementLabelsInventsNoAnnotations pins the other side: a
// service that asks for no placement gains none of the four annotations, so its
// VM config and cgroup request are exactly what they were before the labels
// existed. Whitespace-only values must count as "not asked".
func TestPrepare_NoPlacementLabelsInventsNoAnnotations(t *testing.T) {
	for name, labels := range map[string]map[string]string{
		"no labels":       nil,
		"whitespace only": {types.BalloonLabel: " ", types.CpusetLabel: "\t", types.HugepagesLabel: " ", types.NumaLabel: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			ctrl := placementBridgeController(labels)

			require.NoError(t, ctrl.Prepare(context.Background()))
			require.NotNil(t, ctrl.internalTask)

			for _, key := range []string{types.BalloonLabel, types.CpusetLabel, types.HugepagesLabel, types.NumaLabel} {
				_, present := ctrl.internalTask.Annotations[key]
				assert.False(t, present, "a service without the label must not gain %s", key)
			}
		})
	}
}

// TestPrepare_BridgedHugepagesReachesMachineConfig is the chain test the T2
// tests missed: it starts from a service label (as the CLI creates it), runs the
// real bridge and the daemon's translator, and asserts the Firecracker machine
// config the VMM layer is handed carries huge_pages. It also asserts the request
// is visible to the start-time pool check, which silently skips a task that has
// no annotation.
func TestPrepare_BridgedHugepagesReachesMachineConfig(t *testing.T) {
	ctrl := placementBridgeController(map[string]string{
		types.HugepagesLabel: " true ",
	})

	require.NoError(t, ctrl.Prepare(context.Background()))
	require.NotNil(t, ctrl.internalTask)

	requested, err := types.ParseHugepages(ctrl.internalTask.Annotations[types.HugepagesLabel])
	require.NoError(t, err)
	assert.True(t, requested, "the start-time pool check must see the request")

	config := translatePrepared(t, ctrl)
	machine, ok := config["machine-config"].(map[string]interface{})
	require.True(t, ok, "Translate must emit a machine-config section")
	assert.Equal(t, "2M", machine["huge_pages"])
}

// TestPrepare_BridgedNumaLabelReachesCgroupCpusetMems pins the placement half of
// the chain: a NUMA-only service label must survive the bridge and reach the
// per-task cgroup request as cpuset.mems. Before the bridge existed the label
// never became an annotation, so the policy saw no placement and wrote no mems.
func TestPrepare_BridgedNumaLabelReachesCgroupCpusetMems(t *testing.T) {
	ctrl := placementBridgeController(map[string]string{
		types.NumaLabel: " 0-1 ",
	})

	require.NoError(t, ctrl.Prepare(context.Background()))
	require.NotNil(t, ctrl.internalTask)

	mgr := &fakeCgroupManager{online: "0-3"}
	created, err := jailer.CreateVMCgroup(mgr, nil, zerolog.Nop(), jailer.CgroupRequest{
		Task:     ctrl.internalTask,
		VCPUs:    1,
		MemoryMB: 512,
	})
	require.NoError(t, err)
	require.True(t, created, "a NUMA request must produce a cgroup")

	groups, _, _, _ := mgr.snapshot()
	require.Len(t, groups, 1)
	assert.Equal(t, "0-1", groups[0].limits.CpusetMems)
	assert.Empty(t, groups[0].limits.CpusetCPUs, "a NUMA-only request must not invent a CPU set")
}
