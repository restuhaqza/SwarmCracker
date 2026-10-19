package client

import (
	"context"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListNodesMapsNode(t *testing.T) {
	control := &fakeControl{nodes: []*api.Node{testManagerNode()}}

	items, err := (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)

	got := items[0]
	assert.Equal(t, "node-manager-1", got.ID)
	assert.Equal(t, "manager-1", got.Hostname)
	assert.Equal(t, "7", got.Revision)
	assert.Equal(t, "2023-11-14T22:13:20.123Z", got.CreatedAt)
	assert.Equal(t, "2023-11-14T22:15:00Z", got.UpdatedAt)
	assert.Equal(t, "manager", got.Role)
	assert.Equal(t, "accepted", got.Membership)
	assert.Equal(t, "active", got.Availability)
	assert.Equal(t, "ready", got.State)
	assert.Equal(t, "10.0.0.10", got.Address)
	assert.Equal(t, map[string]string{"zone": "a", "rack": "r1"}, got.Labels)
	require.NotNil(t, got.Resources)
	assert.Equal(t, 2.0, got.Resources.CPUs)
	assert.Equal(t, int64(4<<30), got.Resources.MemoryBytes)
	require.NotNil(t, got.Manager)
	assert.True(t, got.Manager.Leader)
	assert.True(t, got.Manager.Reachable)
	assert.Equal(t, "10.0.0.10:4242", got.Manager.Address)
	assert.Equal(t, uint64(42), got.Manager.RaftID)
	assert.Equal(t, "27.3.1", got.AgentVersion)
	assert.Equal(t, 1, control.listNodesCalls)
}

func TestListNodesWorkerOmitsManager(t *testing.T) {
	worker := testManagerNode()
	worker.ID = "node-worker-1"
	worker.Spec.DesiredRole = api.NodeRoleWorker
	worker.ManagerStatus = nil

	control := &fakeControl{nodes: []*api.Node{worker}}

	items, err := (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)

	assert.Equal(t, "worker", items[0].Role)
	assert.Nil(t, items[0].Manager)
}

func TestListNodesResourcesAndAgentVersion(t *testing.T) {
	control := &fakeControl{nodes: []*api.Node{testManagerNode()}}

	items, err := (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].Resources)
	assert.Equal(t, 2.0, items[0].Resources.CPUs)
	assert.Equal(t, int64(4<<30), items[0].Resources.MemoryBytes)
	assert.Equal(t, "27.3.1", items[0].AgentVersion)

	fractional := testManagerNode()
	fractional.ID = "node-fractional"
	fractional.Description.Resources = &api.Resources{NanoCPUs: 3_500_000_000}
	control = &fakeControl{nodes: []*api.Node{fractional}}

	items, err = (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].Resources)
	assert.Equal(t, 3.5, items[0].Resources.CPUs)

	empty := testManagerNode()
	empty.ID = "node-empty"
	empty.Description.Resources = &api.Resources{}
	empty.Description.Engine = nil
	control = &fakeControl{nodes: []*api.Node{empty}}

	items, err = (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Nil(t, items[0].Resources)
	assert.Empty(t, items[0].AgentVersion)

	undescribed := testManagerNode()
	undescribed.ID = "node-undescribed"
	undescribed.Description = nil
	control = &fakeControl{nodes: []*api.Node{undescribed}}

	items, err = (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].Hostname)
	assert.Nil(t, items[0].Resources)
	assert.Empty(t, items[0].AgentVersion)
}

func TestListNodesManagerReachability(t *testing.T) {
	tests := []struct {
		name         string
		reachability api.RaftMemberStatus_Reachability
		want         bool
	}{
		{name: "reachable", reachability: api.RaftMemberStatus_REACHABLE, want: true},
		{name: "unreachable", reachability: api.RaftMemberStatus_UNREACHABLE, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := testManagerNode()
			node.ManagerStatus.Reachability = tt.reachability
			control := &fakeControl{nodes: []*api.Node{node}}

			items, err := (&Client{control: control}).ListNodes(context.Background())
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.NotNil(t, items[0].Manager)
			assert.Equal(t, tt.want, items[0].Manager.Reachable)
		})
	}
}

func TestListNodesSortsByCreatedAtThenID(t *testing.T) {
	later := &gogotypes.Timestamp{Seconds: 1700000200}
	earlier := &gogotypes.Timestamp{Seconds: 1700000100}

	nodeC := testManagerNode()
	nodeC.ID = "node-c"
	nodeC.Meta.CreatedAt = later
	nodeB := testManagerNode()
	nodeB.ID = "node-b"
	nodeB.Meta.CreatedAt = later
	nodeA := testManagerNode()
	nodeA.ID = "node-a"
	nodeA.Meta.CreatedAt = earlier

	control := &fakeControl{nodes: []*api.Node{nodeC, nodeB, nodeA}}

	items, err := (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 3)

	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	assert.Equal(t, []string{"node-a", "node-b", "node-c"}, ids)
}

func TestListNodesEmptyIsNonNil(t *testing.T) {
	control := &fakeControl{}

	items, err := (&Client{control: control}).ListNodes(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, items, "empty list must not be nil")
	assert.Empty(t, items)
	assert.Equal(t, 1, control.listNodesCalls)
}

func TestListNodesFailure(t *testing.T) {
	control := &fakeControl{nodeErr: assert.AnError}

	_, err := (&Client{control: control}).ListNodes(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func testManagerNode() *api.Node {
	return &api.Node{
		ID: "node-manager-1",
		Meta: api.Meta{
			Version:   api.Version{Index: 7},
			CreatedAt: &gogotypes.Timestamp{Seconds: 1700000000, Nanos: 123000000},
			UpdatedAt: &gogotypes.Timestamp{Seconds: 1700000100},
		},
		Spec: api.NodeSpec{
			Annotations: api.Annotations{
				Labels: map[string]string{"zone": "a", "rack": "r1"},
			},
			DesiredRole:  api.NodeRoleManager,
			Membership:   api.NodeMembershipAccepted,
			Availability: api.NodeAvailabilityActive,
		},
		Description: &api.NodeDescription{
			Hostname:  "manager-1",
			Resources: &api.Resources{NanoCPUs: 2_000_000_000, MemoryBytes: 4 << 30},
			Engine:    &api.EngineDescription{EngineVersion: "27.3.1"},
		},
		Status: api.NodeStatus{
			State: api.NodeStatus_READY,
			Addr:  "10.0.0.10",
		},
		ManagerStatus: &api.ManagerStatus{
			RaftID:       42,
			Addr:         "10.0.0.10:4242",
			Leader:       true,
			Reachability: api.RaftMemberStatus_REACHABLE,
		},
	}
}
