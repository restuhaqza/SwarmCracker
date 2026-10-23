package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeNetworkLister struct {
	networks []v1.Network
	err      error
}

func (f *fakeNetworkLister) ListNetworks(context.Context) ([]v1.Network, error) {
	return f.networks, f.err
}

func TestNetworkListContractJSON(t *testing.T) {
	lister := &fakeNetworkLister{networks: []v1.Network{
		{
			ID: "net-1", Name: "app", Revision: "2", Driver: "overlay",
			Attachable: true,
			Labels:     map[string]string{"tier": "data"},
			IPAM:       &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
		},
	}}

	var buf bytes.Buffer
	require.NoError(t, listNetworksContractWithClient(context.Background(), lister, &buf, "json"))

	var doc v1.NetworkList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, v1.KindNetworkList, doc.Kind)
	require.Len(t, doc.Items, 1)
	assert.Equal(t, "app", doc.Items[0].Name)
	assert.True(t, doc.Items[0].Attachable)
	require.NotNil(t, doc.Items[0].IPAM)
	assert.Equal(t, "10.0.1.0/24", doc.Items[0].IPAM.Subnet)
}

func TestNetworkListContractEmptyAndName(t *testing.T) {
	empty := &fakeNetworkLister{}
	var buf bytes.Buffer
	require.NoError(t, listNetworksContractWithClient(context.Background(), empty, &buf, "json"))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	items, ok := doc["items"].([]any)
	require.True(t, ok)
	assert.Empty(t, items)

	named := &fakeNetworkLister{networks: []v1.Network{{ID: "net-1"}, {ID: "net-2"}}}
	buf.Reset()
	require.NoError(t, listNetworksContractWithClient(context.Background(), named, &buf, "name"))
	assert.Equal(t, "net-1\nnet-2\n", buf.String())
}

func TestNetworkListContractMapsRPCError(t *testing.T) {
	lister := &fakeNetworkLister{err: status.Error(codes.Unavailable, "down")}
	err := listNetworksContractWithClient(context.Background(), lister, &bytes.Buffer{}, "json")
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
}

func TestNetworkListCommandRejectsContractFlagCombinations(t *testing.T) {
	old := outputFormat
	t.Cleanup(func() { outputFormat = old })

	cases := []struct {
		name     string
		output   string
		setFlags map[string]string
	}{
		{name: "unsupported", output: "ndjson"},
		{name: "output and format", output: "json", setFlags: map[string]string{"format": "json"}},
		{name: "quiet with json", output: "json", setFlags: map[string]string{"quiet": "true"}},
		{name: "quiet with name", output: "name", setFlags: map[string]string{"quiet": "true"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputFormat = tc.output
			cmd := newNetworkListCommand()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			for flag, value := range tc.setFlags {
				require.NoError(t, cmd.Flags().Set(flag, value))
			}
			cmd.SetArgs(nil)

			err := cmd.Execute()
			require.Error(t, err)
			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeUsage, typed.Code)
		})
	}
}
