package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// TestParseLabelAssignments is the direct table test for --label-add parsing:
// every argument is either used or rejected, and a rejection names it.
func TestParseLabelAssignments(t *testing.T) {
	tests := []struct {
		name    string
		pairs   []string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "no pairs",
			pairs: nil,
			want:  map[string]string{},
		},
		{
			name:  "single pair",
			pairs: []string{"disk=ssd"},
			want:  map[string]string{"disk": "ssd"},
		},
		{
			name:  "multiple pairs",
			pairs: []string{"disk=ssd", "zone=a"},
			want:  map[string]string{"disk": "ssd", "zone": "a"},
		},
		{
			name:  "empty value is a valid label",
			pairs: []string{"disk="},
			want:  map[string]string{"disk": ""},
		},
		{
			name:  "value may contain the separator",
			pairs: []string{"rule=a=b"},
			want:  map[string]string{"rule": "a=b"},
		},
		{
			name:  "repeated key keeps the last value",
			pairs: []string{"disk=ssd", "disk=hdd"},
			want:  map[string]string{"disk": "hdd"},
		},
		{
			name:    "missing separator",
			pairs:   []string{"disk"},
			wantErr: `invalid label "disk": expected KEY=VALUE`,
		},
		{
			name:    "empty key",
			pairs:   []string{"=ssd"},
			wantErr: `invalid label "=ssd": key must not be empty`,
		},
		{
			name:    "blank key",
			pairs:   []string{" =ssd"},
			wantErr: `invalid label " =ssd": key must not be empty`,
		},
		{
			name:    "later argument is rejected and named",
			pairs:   []string{"disk=ssd", "zone"},
			wantErr: `invalid label "zone": expected KEY=VALUE`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLabelAssignments(tt.pairs)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestApplyLabelChanges is the direct table test for the label map maths:
// removals run before additions, a missing removal key is a no-op, and an
// invalid argument is an error rather than a silent skip.
func TestApplyLabelChanges(t *testing.T) {
	tests := []struct {
		name    string
		current map[string]string
		add     []string
		remove  []string
		want    map[string]string
		wantErr string
	}{
		{
			name: "add to a node with no labels",
			add:  []string{"disk=ssd"},
			want: map[string]string{"disk": "ssd"},
		},
		{
			name:    "add overwrites an existing key",
			current: map[string]string{"disk": "hdd"},
			add:     []string{"disk=ssd"},
			want:    map[string]string{"disk": "ssd"},
		},
		{
			name:    "remove leaves the other labels alone",
			current: map[string]string{"disk": "hdd", "zone": "a"},
			remove:  []string{"disk"},
			want:    map[string]string{"zone": "a"},
		},
		{
			name:    "removing an absent key is a no-op",
			current: map[string]string{"disk": "ssd"},
			remove:  []string{"nope"},
			want:    map[string]string{"disk": "ssd"},
		},
		{
			name:    "removals apply before additions",
			current: map[string]string{"disk": "hdd"},
			add:     []string{"disk=ssd"},
			remove:  []string{"disk"},
			want:    map[string]string{"disk": "ssd"},
		},
		{
			name:    "invalid add",
			current: map[string]string{"disk": "ssd"},
			add:     []string{"zone"},
			wantErr: `invalid label "zone"`,
		},
		{
			name:    "empty removal key",
			current: map[string]string{"disk": "ssd"},
			remove:  []string{""},
			wantErr: `invalid label removal "": key must not be empty`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyLabelChanges(tt.current, tt.add, tt.remove)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestApplyLabelChangesDoesNotMutateInput pins the copy semantics the update
// path relies on: the map fetched from the manager is never edited in place.
func TestApplyLabelChangesDoesNotMutateInput(t *testing.T) {
	current := map[string]string{"disk": "hdd", "zone": "a"}

	got, err := applyLabelChanges(current, []string{"disk=ssd"}, []string{"zone"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"disk": "ssd"}, got)
	assert.Equal(t, map[string]string{"disk": "hdd", "zone": "a"}, current,
		"applyLabelChanges must not mutate the map it is given")
}

// TestNodeUpdateCommandWiring verifies the flags exist, are repeatable string
// arrays, and that both are documented in the help the operator sees.
func TestNodeUpdateCommandWiring(t *testing.T) {
	cmd := newNodeUpdateCommand()

	assert.Equal(t, "update <node-id>", cmd.Use)

	for _, name := range []string{"label-add", "label-rm"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "node update must register --%s", name)
		assert.Equal(t, "stringArray", flag.Value.Type(), "--%s must be repeatable", name)
		assert.Equal(t, "[]", flag.DefValue)
	}

	usage := cmd.UsageString()
	assert.Contains(t, usage, "--label-add")
	assert.Contains(t, usage, "--label-rm")
	assert.Contains(t, cmd.Long, "no-op", "the help must say a removal of an absent key is a no-op")

	require.NotNil(t, cmd.Args, "node update must require exactly one node reference")
	assert.NoError(t, cmd.Args(cmd, []string{"abc"}))
	assert.Error(t, cmd.Args(cmd, nil))
	assert.Error(t, cmd.Args(cmd, []string{"abc", "def"}))
}

// fakeNodeClient embeds the generated ControlClient and overrides only the RPCs
// the node update path uses, counting every call so a test can assert that a
// rejected argument never reaches the manager.
type fakeNodeClient struct {
	api.ControlClient
	node        *api.Node
	listCalls   int
	getCalls    int
	updateCalls int
	updatedReq  *api.UpdateNodeRequest
}

func (f *fakeNodeClient) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	f.listCalls++
	if f.node == nil {
		return &api.ListNodesResponse{}, nil
	}
	return &api.ListNodesResponse{Nodes: []*api.Node{f.node}}, nil
}

func (f *fakeNodeClient) GetNode(_ context.Context, req *api.GetNodeRequest, _ ...grpc.CallOption) (*api.GetNodeResponse, error) {
	f.getCalls++
	if f.node == nil || f.node.ID != req.NodeID {
		return nil, fmt.Errorf("node not found: %s", req.NodeID)
	}
	return &api.GetNodeResponse{Node: f.node}, nil
}

func (f *fakeNodeClient) UpdateNode(_ context.Context, req *api.UpdateNodeRequest, _ ...grpc.CallOption) (*api.UpdateNodeResponse, error) {
	f.updateCalls++
	f.updatedReq = req
	return &api.UpdateNodeResponse{}, nil
}

// labelledNode builds a node carrying labels and a known version.
func labelledNode(id string, labels map[string]string) *api.Node {
	return &api.Node{
		ID:          id,
		Meta:        api.Meta{Version: api.Version{Index: 7}},
		Spec:        api.NodeSpec{Annotations: api.Annotations{Labels: labels}},
		Description: &api.NodeDescription{Hostname: "node-a"},
	}
}

// TestUpdateNodeLabelsWithClient_AppliesChanges exercises the update path with a
// fake client: the node reference resolves through a short prefix, the label
// changes land in the spec sent to UpdateNode with the current node version, and
// the result is printed.
func TestUpdateNodeLabelsWithClient_AppliesChanges(t *testing.T) {
	node := labelledNode("abcdef0123456789abcdef01", map[string]string{"disk": "hdd", "zone": "old"})
	client := &fakeNodeClient{node: node}
	var out bytes.Buffer

	require.NoError(t, updateNodeLabelsWithClient(context.Background(), client, &out, "abcdef",
		[]string{"disk=ssd", "tier=gold"}, []string{"zone"}))

	require.Equal(t, 1, client.listCalls)
	require.Equal(t, 1, client.getCalls)
	require.Equal(t, 1, client.updateCalls)

	require.NotNil(t, client.updatedReq)
	assert.Equal(t, "abcdef0123456789abcdef01", client.updatedReq.NodeID)
	require.NotNil(t, client.updatedReq.NodeVersion, "the update must carry the node version")
	assert.Equal(t, uint64(7), client.updatedReq.NodeVersion.Index)
	require.NotNil(t, client.updatedReq.Spec)
	assert.Equal(t, map[string]string{"disk": "ssd", "tier": "gold"}, client.updatedReq.Spec.Annotations.Labels)

	// The remaining labels are printed in sorted order for the operator.
	assert.Equal(t, "Node abcdef0123456789abcdef01 labels updated\n  disk=ssd\n  tier=gold\n", out.String())
}

// TestUpdateNodeLabelsWithClient_RemovingMissingKeyIsNoOp checks the documented
// behaviour: --label-rm of a key the node does not have is not an error.
func TestUpdateNodeLabelsWithClient_RemovingMissingKeyIsNoOp(t *testing.T) {
	node := labelledNode("abcdef0123456789abcdef01", map[string]string{"disk": "ssd"})
	client := &fakeNodeClient{node: node}

	require.NoError(t, updateNodeLabelsWithClient(context.Background(), client, io.Discard, "abcdef",
		nil, []string{"nope"}))

	require.Equal(t, 1, client.updateCalls)
	require.NotNil(t, client.updatedReq)
	assert.Equal(t, map[string]string{"disk": "ssd"}, client.updatedReq.Spec.Annotations.Labels)
}

// TestUpdateNodeLabelsWithClient_RejectsBeforeAnyRPC is the guard against
// accepting a flag and ignoring it: a bad argument (or an invocation that
// changes nothing) fails before the node is even looked up.
func TestUpdateNodeLabelsWithClient_RejectsBeforeAnyRPC(t *testing.T) {
	tests := []struct {
		name    string
		add     []string
		remove  []string
		wantErr string
	}{
		{
			name:    "add without separator",
			add:     []string{"disk"},
			wantErr: `invalid label "disk": expected KEY=VALUE`,
		},
		{
			name:    "add with empty key",
			add:     []string{"=ssd"},
			wantErr: `invalid label "=ssd": key must not be empty`,
		},
		{
			name:    "remove with empty key",
			remove:  []string{""},
			wantErr: `invalid label removal "": key must not be empty`,
		},
		{
			name:    "neither flag given",
			wantErr: "no label changes requested",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeNodeClient{node: labelledNode("abcdef0123456789abcdef01", nil)}

			err := updateNodeLabelsWithClient(context.Background(), client, io.Discard, "abcdef", tt.add, tt.remove)

			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
			assert.Zero(t, client.listCalls, "no node lookup may happen before validation")
			assert.Zero(t, client.getCalls)
			assert.Zero(t, client.updateCalls)
		})
	}
}

// TestUpdateNodeLabels_ValidatesBeforeOpeningAClient is the CLI-level guard: the
// flags are checked before the swarm socket is dialled, so on a host with no
// /var/run/swarmkit (which is what this test runs on) the error is about the
// offending argument, not about certificates.
func TestUpdateNodeLabels_ValidatesBeforeOpeningAClient(t *testing.T) {
	err := updateNodeLabels("abcdef", []string{"disk"}, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, `invalid label "disk"`)
	assert.NotContains(t, err.Error(), "certificate")

	err = updateNodeLabels("abcdef", nil, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "no label changes requested")
	assert.NotContains(t, err.Error(), "certificate")
}
