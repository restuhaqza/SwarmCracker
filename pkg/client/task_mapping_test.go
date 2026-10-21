package client

import (
	"context"
	"errors"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListTasksMapsTask(t *testing.T) {
	task := testTask()
	control := &fakeControl{
		tasks: []*api.Task{task},
		nodes: []*api.Node{
			{ID: "node-1", Description: &api.NodeDescription{Hostname: "node-2"}},
			{ID: "node-unused", Description: nil},
		},
	}

	items, err := (&Client{control: control}).ListTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)

	got := items[0]
	assert.Equal(t, "task-1", got.ID)
	assert.Equal(t, "web.1.task-1", got.Name)
	assert.Equal(t, "42", got.Revision)
	assert.Equal(t, "svc-1", got.ServiceID)
	assert.Equal(t, "web", got.ServiceName)
	assert.Equal(t, "node-1", got.NodeID)
	assert.Equal(t, "node-2", got.NodeName)
	assert.Equal(t, uint64(1), got.Slot)
	assert.Equal(t, "running", got.State)
	assert.Equal(t, "running", got.DesiredState)
	assert.Equal(t, "started", got.Message)
	assert.Empty(t, got.Error)
	assert.Equal(t, "2023-11-14T22:13:20Z", got.CreatedAt)
	assert.Equal(t, "2023-11-14T22:15:00Z", got.UpdatedAt)
	assert.Equal(t, "nginx:alpine", got.Image)
	require.NotNil(t, got.Addresses)
	assert.Equal(t, []string{"10.0.0.5/24", "10.0.1.6/24", "10.0.1.7/24"}, got.Addresses)
	require.Len(t, got.Ports, 1)
	assert.Equal(t, uint32(80), got.Ports[0].Target)
	assert.Zero(t, got.Ports[0].Published)
	assert.Equal(t, "tcp", got.Ports[0].Protocol)
	assert.Empty(t, got.Ports[0].Mode)
	assert.Nil(t, got.ExitCode)
	assert.Nil(t, got.Health)
	assert.Equal(t, 1, control.listNodesCalls)
}

func TestListTasksPortFallback(t *testing.T) {
	task := testTask()
	task.Status.PortStatus = nil
	task.Endpoint = &api.Endpoint{Ports: []*api.PortConfig{{
		TargetPort:    8080,
		PublishedPort: 9090,
		Protocol:      api.ProtocolUDP,
		PublishMode:   api.PublishModeIngress,
	}}}

	control := &fakeControl{tasks: []*api.Task{task}}
	items, err := (&Client{control: control}).ListTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Len(t, items[0].Ports, 1)

	port := items[0].Ports[0]
	assert.Equal(t, uint32(8080), port.Target)
	assert.Equal(t, "udp", port.Protocol)
	assert.Zero(t, port.Published)
	assert.Empty(t, port.Mode)
}

func TestListTasksExitCode(t *testing.T) {
	completed := testTask()
	completed.ID = "task-completed"
	completed.Status.State = api.TaskStateCompleted
	completed.Status.RuntimeStatus = &api.TaskStatus_Container{
		Container: &api.ContainerStatus{ExitCode: 7},
	}

	failed := testTask()
	failed.ID = "task-failed"
	failed.Status.State = api.TaskStateFailed
	failed.Status.RuntimeStatus = &api.TaskStatus_Container{
		Container: &api.ContainerStatus{ExitCode: 7},
	}

	running := testTask()
	running.ID = "task-running"
	running.Status.State = api.TaskStateRunning
	running.Status.RuntimeStatus = &api.TaskStatus_Container{
		Container: &api.ContainerStatus{ExitCode: 7},
	}

	control := &fakeControl{tasks: []*api.Task{completed, failed, running}}
	items, err := (&Client{control: control}).ListTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 3)

	byID := make(map[string]int32)
	for _, item := range items {
		if item.ExitCode != nil {
			byID[item.ID] = *item.ExitCode
		}
	}
	assert.Equal(t, map[string]int32{
		"task-completed": 7,
		"task-failed":    7,
	}, byID)
}

func TestListTasksSortsByCreatedAtThenID(t *testing.T) {
	later := &gogotypes.Timestamp{Seconds: 1700000200}
	earlier := &gogotypes.Timestamp{Seconds: 1700000100}
	taskC := testTask()
	taskC.ID = "task-c"
	taskC.Meta.CreatedAt = later
	taskB := testTask()
	taskB.ID = "task-b"
	taskB.Meta.CreatedAt = later
	taskA := testTask()
	taskA.ID = "task-a"
	taskA.Meta.CreatedAt = earlier

	control := &fakeControl{tasks: []*api.Task{taskC, taskB, taskA}}
	items, err := (&Client{control: control}).ListTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 3)

	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	assert.Equal(t, []string{"task-a", "task-b", "task-c"}, ids)
}

func TestListTasksEmptySkipsNodeCall(t *testing.T) {
	control := &fakeControl{}

	items, err := (&Client{control: control}).ListTasks(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, items, "empty list must not be nil")
	assert.Empty(t, items)
	assert.Zero(t, control.listNodesCalls)
}

func TestListTasksNodeFailure(t *testing.T) {
	control := &fakeControl{
		tasks:   []*api.Task{testTask()},
		nodeErr: assert.AnError,
	}

	_, err := (&Client{control: control}).ListTasks(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, assert.AnError))
}

func TestListTasksTaskFailureIsReturned(t *testing.T) {
	control := &fakeControl{taskErr: assert.AnError}

	_, err := (&Client{control: control}).ListTasks(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestTaskName(t *testing.T) {
	tests := []struct {
		name        string
		annotations string
		serviceName string
		slot        uint64
		id          string
		want        string
	}{
		{
			name:        "annotation name wins",
			annotations: "custom",
			serviceName: "web",
			slot:        1,
			id:          "abcdef1234567890",
			want:        "custom",
		},
		{
			name:        "slot derivation truncates id",
			serviceName: "web",
			slot:        2,
			id:          "abcdef1234567890",
			want:        "web.2.abcdef123456",
		},
		{
			name:        "slotless derivation",
			serviceName: "web",
			id:          "abcdef1234567890",
			want:        "web.abcdef123456",
		},
		{
			name:        "short id is kept whole",
			serviceName: "web",
			slot:        1,
			id:          "task-1",
			want:        "web.1.task-1",
		},
		{
			name: "no service name yields empty",
			id:   "abcdef1234567890",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &api.Task{
				ID:                 tt.id,
				Annotations:        api.Annotations{Name: tt.annotations},
				ServiceAnnotations: api.Annotations{Name: tt.serviceName},
				Slot:               tt.slot,
			}
			assert.Equal(t, tt.want, taskName(task))
		})
	}
}

func testTask() *api.Task {
	return &api.Task{
		ID: "task-1",
		Meta: api.Meta{
			Version:   api.Version{Index: 42},
			CreatedAt: &gogotypes.Timestamp{Seconds: 1700000000},
			UpdatedAt: &gogotypes.Timestamp{Seconds: 1700000100},
		},
		Spec: api.TaskSpec{
			Runtime: &api.TaskSpec_Container{
				Container: &api.ContainerSpec{Image: "nginx:alpine"},
			},
		},
		ServiceID:          "svc-1",
		ServiceAnnotations: api.Annotations{Name: "web"},
		NodeID:             "node-1",
		Slot:               1,
		Status: api.TaskStatus{
			State:   api.TaskStateRunning,
			Message: "started",
			PortStatus: &api.PortStatus{Ports: []*api.PortConfig{{
				TargetPort:    80,
				PublishedPort: 8080,
				Protocol:      api.ProtocolTCP,
				PublishMode:   api.PublishModeIngress,
			}}},
		},
		DesiredState: api.TaskStateRunning,
		Networks: []*api.NetworkAttachment{
			{Addresses: []string{"10.0.0.5/24"}},
			{Addresses: []string{"10.0.1.6/24", "10.0.1.7/24"}},
		},
		Endpoint: &api.Endpoint{Ports: []*api.PortConfig{{
			TargetPort: 8080,
			Protocol:   api.ProtocolTCP,
		}}},
	}
}
