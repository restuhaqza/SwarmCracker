package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeNodeLister is the narrow lister the contract path needs, so the JSON and
// name rendering can be exercised without a cluster or certificates.
type fakeNodeLister struct {
	nodes []v1.Node
	err   error
}

func (f *fakeNodeLister) ListNodes(context.Context) ([]v1.Node, error) {
	return f.nodes, f.err
}

// fakeNodeListClient is the raw SwarmKit client the legacy path talks to. It is
// deliberately separate from fakeNodeClient (labels_test.go), which models the
// update path and its call counts.
type fakeNodeListClient struct {
	api.ControlClient
	nodes []*api.Node
	err   error
}

func (f *fakeNodeListClient) ListNodes(context.Context, *api.ListNodesRequest, ...grpc.CallOption) (*api.ListNodesResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &api.ListNodesResponse{Nodes: f.nodes}, nil
}

// contractNodes is a two-node fixture used by the JSON/filter tests and the
// filtered name-mode test: one manager in zone a with a disk label, one worker
// with no labels.
func contractNodes() []v1.Node {
	return []v1.Node{
		{
			ID: "id1", Hostname: "manager-a", Role: "manager", Membership: "accepted",
			Availability: "active", State: "ready", Labels: map[string]string{"zone": "a", "disk": "ssd"},
		},
		{
			ID: "id2", Hostname: "worker-b", Role: "worker", Membership: "accepted",
			Availability: "drain", State: "down",
		},
	}
}

func TestNodeListContractEmptyIsParseable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listNodesContractWithClient(
		context.Background(), &fakeNodeLister{}, &buf, "json", "",
	))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc), buf.String())
	assert.Equal(t, float64(v1.SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, v1.KindNodeList, doc["kind"])
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	next, ok := doc["next"]
	require.True(t, ok, "next must be present")
	assert.Nil(t, next)
}

func TestNodeListContractJSONAndFilters(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{name: "no filter keeps all", filter: "", want: []string{"id1", "id2"}},
		{name: "role selects the manager", filter: "role=manager", want: []string{"id1"}},
		{name: "label key and value", filter: "label=zone=a", want: []string{"id1"}},
		{name: "label key presence", filter: "label=zone", want: []string{"id1"}},
		{name: "label value is exact", filter: "label=zone=A", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, listNodesContractWithClient(
				context.Background(), &fakeNodeLister{nodes: contractNodes()}, &buf, "json", tt.filter,
			))

			var doc v1.NodeList
			require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
			var ids []string
			for _, node := range doc.Items {
				ids = append(ids, node.ID)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

func TestNodeListContractNameMode(t *testing.T) {
	lister := &fakeNodeLister{nodes: []v1.Node{{ID: "id1"}, {ID: "id2"}}}

	var buf bytes.Buffer
	require.NoError(t, listNodesContractWithClient(
		context.Background(), lister, &buf, "name", "",
	))
	assert.Equal(t, "id1\nid2\n", buf.String())
}

// TestNodeListContractNameModeWithFilter is success criterion 3 as a composite:
// the filter and the name rendering together, rather than each in isolation.
func TestNodeListContractNameModeWithFilter(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listNodesContractWithClient(
		context.Background(), &fakeNodeLister{nodes: contractNodes()}, &buf, "name", "role=manager",
	))
	assert.Equal(t, "id1\n", buf.String())
}

// TestNodeListContractFilteredEmptyItemsIsArray asserts the filtered-to-empty
// case at the raw JSON level: unmarshalling into v1.NodeList would accept
// `items: null`, so the value itself is checked here.
func TestNodeListContractFilteredEmptyItemsIsArray(t *testing.T) {
	lister := &fakeNodeLister{nodes: []v1.Node{{ID: "id2", Role: "worker"}}}

	var buf bytes.Buffer
	require.NoError(t, listNodesContractWithClient(
		context.Background(), lister, &buf, "json", "role=manager",
	))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc), buf.String())
	items, ok := doc["items"]
	require.True(t, ok, "items must be present")
	require.NotNil(t, items, "items must not be null")
	list, ok := items.([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, list)
}

func TestNodeListContractMapsRPCError(t *testing.T) {
	lister := &fakeNodeLister{err: status.Error(codes.NotFound, "missing")}
	err := listNodesContractWithClient(context.Background(), lister, &bytes.Buffer{}, "json", "")
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
}

func TestFilterV1Nodes(t *testing.T) {
	nodes := []v1.Node{
		{
			ID: "id1", Hostname: "Manager-A", Role: "manager", Membership: "accepted",
			Availability: "active", State: "ready", Labels: map[string]string{"zone": "a", "disk": "ssd"},
		},
		{
			ID: "id2", Hostname: "worker-b", Role: "worker", Membership: "accepted",
			Availability: "drain", State: "down", Labels: map[string]string{"zone": "b"},
		},
	}

	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{name: "empty filter keeps all", filter: "", want: []string{"id1", "id2"}},
		{name: "role", filter: "role=manager", want: []string{"id1"}},
		{name: "role is case-insensitive", filter: "role=MANAGER", want: []string{"id1"}},
		{name: "membership", filter: "membership=accepted", want: []string{"id1", "id2"}},
		{name: "membership is case-insensitive", filter: "membership=ACCEPTED", want: []string{"id1", "id2"}},
		{name: "availability", filter: "availability=drain", want: []string{"id2"}},
		{name: "availability is case-insensitive", filter: "availability=DRAIN", want: []string{"id2"}},
		{name: "state", filter: "state=ready", want: []string{"id1"}},
		{name: "state is case-insensitive", filter: "state=READY", want: []string{"id1"}},
		{name: "id is exact", filter: "id=id2", want: []string{"id2"}},
		{name: "id is not a prefix", filter: "id=id", want: nil},
		{name: "hostname is case-insensitive", filter: "hostname=manager-a", want: []string{"id1"}},
		{name: "hostname is exact", filter: "hostname=manager", want: nil},
		{name: "label key presence", filter: "label=zone", want: []string{"id1", "id2"}},
		{name: "label key presence with a unique key", filter: "label=disk", want: []string{"id1"}},
		{name: "label key and value", filter: "label=zone=b", want: []string{"id2"}},
		{name: "label value is case-sensitive", filter: "label=zone=A", want: nil},
		{name: "label value is exact", filter: "label=zone=ab", want: nil},
		{name: "unknown key keeps all", filter: "image=nginx", want: []string{"id1", "id2"}},
		{name: "missing separator keeps all", filter: "manager", want: []string{"id1", "id2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ids []string
			for _, node := range filterV1Nodes(nodes, tt.filter) {
				ids = append(ids, node.ID)
			}
			assert.Equal(t, tt.want, ids)
		})
	}
}

// TestFilterV1NodesEmptyIsNonNil keeps the contract path safe against a null
// items array: filtering an empty list returns an empty, non-nil slice.
func TestFilterV1NodesEmptyIsNonNil(t *testing.T) {
	got := filterV1Nodes(nil, "role=manager")
	require.NotNil(t, got)
	assert.Empty(t, got)

	got = filterV1Nodes(nil, "")
	require.NotNil(t, got)
	assert.Empty(t, got)
}

// TestLegacyNodeListJSONEmptyIsArray pins the one legacy shape change in this
// slice: an empty result in --format json is a parseable [] instead of prose.
func TestLegacyNodeListJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listNodesWithClient(
		context.Background(), &fakeNodeListClient{}, &buf, "json", "", false,
	))
	assert.JSONEq(t, "[]", buf.String())
}

// TestLegacyNodeListFilteredEmptyIsArray is the second half of the bug fix: a
// filter that matches nothing used to marshal a nil slice as `null`.
func TestLegacyNodeListFilteredEmptyIsArray(t *testing.T) {
	worker := &api.Node{
		ID:   "worker-1",
		Spec: api.NodeSpec{DesiredRole: api.NodeRoleWorker},
	}

	var buf bytes.Buffer
	require.NoError(t, listNodesWithClient(
		context.Background(), &fakeNodeListClient{nodes: []*api.Node{worker}}, &buf, "json", "role=manager", false,
	))
	assert.JSONEq(t, "[]", buf.String())
}

// TestLegacyNodeListTableEmptyKeepsProse keeps the human-facing message as it
// was: the fix applies to JSON only.
func TestLegacyNodeListTableEmptyKeepsProse(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listNodesWithClient(
		context.Background(), &fakeNodeListClient{}, &buf, "table", "", false,
	))
	assert.Contains(t, buf.String(), "No nodes found")
}

// TestLegacyNodeListTableNonEmptyPreservesShape pins the human-facing table on a
// non-empty result: the four columns, the 12-character ID truncation and the
// total line. Without it a renamed column, a lost truncation or a dropped total
// would stay green.
func TestLegacyNodeListTableNonEmptyPreservesShape(t *testing.T) {
	const fullID = "abcdef1234567890abcdef1234567890"

	node := &api.Node{
		ID:          fullID,
		Spec:        api.NodeSpec{Availability: api.NodeAvailabilityDrain},
		Description: &api.NodeDescription{Hostname: "manager-a"},
		Status:      api.NodeStatus{State: api.NodeStatus_READY},
	}

	var buf bytes.Buffer
	require.NoError(t, listNodesWithClient(
		context.Background(), &fakeNodeListClient{nodes: []*api.Node{node}}, &buf, "table", "", false,
	))
	out := buf.String()

	lines := strings.Split(out, "\n")
	require.GreaterOrEqual(t, len(lines), 2, out)
	for _, column := range []string{"ID", "STATUS", "HOSTNAME", "AVAILABILITY"} {
		assert.Contains(t, lines[0], column)
	}

	prefix := fullID[:12]
	var row string
	for _, line := range lines {
		if strings.Contains(line, prefix) {
			row = line
			break
		}
	}
	require.NotEmpty(t, row, "expected a row carrying the 12-character ID prefix:\n%s", out)
	assert.NotContains(t, row, fullID, "the table must truncate the node ID")
	// The legacy table prints the protobuf enum names verbatim, unlike the
	// lowercase v1 read model.
	assert.Contains(t, row, "READY")
	assert.Contains(t, row, "manager-a")
	assert.Contains(t, row, "DRAIN")

	assert.Contains(t, out, "Total: 1 node(s)")
}

// TestLegacyNodeListJSONNonEmptyPreservesRawArray pins the legacy JSON shape on a
// non-empty result: a raw SwarmKit []Node array, not the v1 envelope.
func TestLegacyNodeListJSONNonEmptyPreservesRawArray(t *testing.T) {
	node := &api.Node{
		ID:          "abcdef1234567890",
		Spec:        api.NodeSpec{DesiredRole: api.NodeRoleManager},
		Description: &api.NodeDescription{Hostname: "manager-a"},
		Status:      api.NodeStatus{State: api.NodeStatus_READY},
	}

	var buf bytes.Buffer
	require.NoError(t, listNodesWithClient(
		context.Background(), &fakeNodeListClient{nodes: []*api.Node{node}}, &buf, "json", "", false,
	))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
	require.Len(t, got, 1)
	assert.Equal(t, node.ID, got[0]["id"])
	assert.Contains(t, got[0], "spec")
	assert.Contains(t, got[0], "status")
	assert.NotContains(t, got[0], "schemaVersion")
	assert.NotContains(t, got[0], "kind")
}

// TestNodeListModeValidation proves every -o rejection happens before a client
// is built: each case must return CodeUsage, which the command cannot produce
// after listNodesContract dials. The global outputFormat is saved and restored,
// so this test must not run in parallel.
func TestNodeListModeValidation(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		wantMsg string
		setup   func(t *testing.T, cmd *cobra.Command)
	}{
		{name: "ndjson is unsupported", format: "ndjson"},
		{
			name:    "json with quiet",
			format:  "json",
			wantMsg: "use -o name instead of --quiet",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("quiet", "true"))
			},
		},
		{
			name:    "name with quiet",
			format:  "name",
			wantMsg: "do not combine -o name with --quiet",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("quiet", "true"))
			},
		},
		{
			// -o table with an explicit --format is a conflict too, matching
			// `task ls` and `service ls`; it must be rejected before the legacy
			// path dials.
			name:    "table with legacy format flag",
			format:  "table",
			wantMsg: "use either --output or --format, not both",
			setup: func(t *testing.T, cmd *cobra.Command) {
				require.NoError(t, cmd.Flags().Set("format", "json"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := outputFormat
			outputFormat = tt.format
			t.Cleanup(func() { outputFormat = prev })

			cmd := newNodeListCommand()
			if tt.setup != nil {
				tt.setup(t, cmd)
			}

			err := cmd.RunE(cmd, nil)
			require.Error(t, err)

			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeUsage, typed.Code)
			if tt.wantMsg != "" {
				assert.Contains(t, typed.Message, tt.wantMsg)
			}
		})
	}
}
