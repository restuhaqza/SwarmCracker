package swarmkit

import (
	"context"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrepare_BridgesCpusetLabelToTaskAnnotation pins that a service's
// "swarmcracker.cpuset" label reaches the task annotation the VMM manager reads.
// Without this copy the label would be accepted on the manager and silently
// ignored on the node — the exact failure mode the balloon bridge avoids.
func TestPrepare_BridgesCpusetLabelToTaskAnnotation(t *testing.T) {
	ctrl := balloonBridgeController("")
	ctrl.task.ServiceAnnotations.Labels = map[string]string{types.CpusetLabel: " 0-2 "}

	require.NoError(t, ctrl.Prepare(context.Background()))

	require.NotNil(t, ctrl.internalTask)
	assert.Equal(t, "0-2", ctrl.internalTask.Annotations[types.CpusetLabel])
}

// TestPrepare_NoCpusetLabelAddsNoAnnotation pins the other side: a service that
// requests no cpuset gains no annotation, so its config is unchanged.
func TestPrepare_NoCpusetLabelAddsNoAnnotation(t *testing.T) {
	ctrl := balloonBridgeController("")

	require.NoError(t, ctrl.Prepare(context.Background()))

	require.NotNil(t, ctrl.internalTask)
	_, present := ctrl.internalTask.Annotations[types.CpusetLabel]
	assert.False(t, present, "a service without the label must not gain the annotation")
}
