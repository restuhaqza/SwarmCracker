package swarmkit

import (
	"context"
	"sync"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// balloonBridgeController builds a Controller around a service task whose service
// carries the given swarmcracker.balloon label, using the package's successful
// mocks so Prepare runs its full prepare path.
func balloonBridgeController(label string) *Controller {
	serviceLabels := map[string]string{}
	if label != "" {
		serviceLabels[types.BalloonLabel] = label
	}
	return &Controller{
		task: &api.Task{
			ID: "task-balloon-bridge",
			Spec: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{
					Container: &api.ContainerSpec{Image: "nginx:latest"},
				},
			},
			ServiceAnnotations: api.Annotations{Labels: serviceLabels},
		},
		config:     &Config{RootfsDir: "/tmp"},
		imagePrep:  &mockImagePrepSuccess{},
		networkMgr: &mockNetworkManagerFull{},
		vmmMgr:     &mockVMMManagerSuccess{},
		mu:         sync.Mutex{},
	}
}

// translatePrepared translates the task Prepare stored, the way Start does, so a
// test can prove the bridge reaches the VM config and not just the annotation.
func translatePrepared(t *testing.T, ctrl *Controller) map[string]interface{} {
	t.Helper()
	require.NotNil(t, ctrl.internalTask)

	trans, err := NewTaskTranslator("/test/kernel", "192.168.127.1/24")
	require.NoError(t, err)

	result, err := trans.Translate(ctrl.internalTask)
	require.NoError(t, err)
	config, ok := result.(map[string]interface{})
	require.True(t, ok, "Translate must return map[string]interface{}")
	return config
}

// TestPrepare_BridgesBalloonLabelToTaskAnnotation pins the bridge that makes a
// service label reach the VM: both translators read the balloon from
// task.Annotations, and a service label only reaches the executor through
// ServiceAnnotations.Labels, so without this copy `--balloon-mib` would be
// accepted on the manager and silently ignored on the node.
func TestPrepare_BridgesBalloonLabelToTaskAnnotation(t *testing.T) {
	ctrl := balloonBridgeController("256")
	require.NoError(t, ctrl.Prepare(context.Background()))

	require.NotNil(t, ctrl.internalTask)
	assert.Equal(t, "256", ctrl.internalTask.Annotations[types.BalloonLabel])

	// ...and therefore the config the VMM layer is handed carries the device.
	config := translatePrepared(t, ctrl)
	device, ok := config["balloon"].(map[string]interface{})
	require.True(t, ok, "a service with the label must produce a balloon section")
	assert.Equal(t, int64(256), device["amount_mib"])
	assert.Equal(t, true, device["deflate_on_oom"])
}

// TestPrepare_NoBalloonLabelLeavesNoAnnotation pins the no-op property: a service
// without the label leaves the annotation unset, so the VM config is
// byte-identical to before (no balloon section at all).
func TestPrepare_NoBalloonLabelLeavesNoAnnotation(t *testing.T) {
	ctrl := balloonBridgeController("")
	require.NoError(t, ctrl.Prepare(context.Background()))

	require.NotNil(t, ctrl.internalTask)
	_, present := ctrl.internalTask.Annotations[types.BalloonLabel]
	assert.False(t, present, "a service without the label must not gain the annotation")

	config := translatePrepared(t, ctrl)
	_, present = config["balloon"]
	assert.False(t, present, "an unconfigured task must emit no balloon key")
}
