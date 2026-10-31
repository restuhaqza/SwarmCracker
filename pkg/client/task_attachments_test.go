package client

// TaskAttachments is the per-attachment view of a task's addresses: the network
// each address belongs to, which the v1 task document flattens away. The tests pin
// the pairing, its order, and the one client that cannot answer it.

import (
	"context"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// TestTaskAttachmentsPairsAddressesWithTheirNetwork pins what the flat document
// throws away: which network each address was allocated on, and the order that makes
// interface index i mean attachment i.
func TestTaskAttachmentsPairsAddressesWithTheirNetwork(t *testing.T) {
	control := &fakeControl{task: &api.Task{
		ID: "web-1",
		Networks: []*api.NetworkAttachment{
			{
				Network:   &api.Network{ID: "net-flat", Spec: api.NetworkSpec{Annotations: api.Annotations{Name: "ingress"}}},
				Addresses: []string{"10.0.1.7/24"},
			},
			{
				Network: &api.Network{ID: "net-user", Spec: api.NetworkSpec{
					Annotations: api.Annotations{Name: "backend"},
				}},
				Addresses: []string{"10.77.0.9/24", "fd00::9/64"},
			},
		},
	}}

	got, err := (&Client{control: control}).TaskAttachments(context.Background(), "web-1")
	require.NoError(t, err)
	assert.Equal(t, []TaskAttachment{
		{NetworkID: "net-flat", Network: "ingress", Addresses: []string{"10.0.1.7/24"}},
		{NetworkID: "net-user", Network: "backend", Addresses: []string{"10.77.0.9/24", "fd00::9/64"}},
	}, got)
}

// TestTaskAttachmentsSkipsAnUnresolvedNetwork pins the one attachment that cannot be
// named: without a network ID no node can derive a bridge for it, so it is left out
// rather than sent with an empty network.
func TestTaskAttachmentsSkipsAnUnresolvedNetwork(t *testing.T) {
	control := &fakeControl{task: &api.Task{
		ID: "web-1",
		Networks: []*api.NetworkAttachment{
			nil,
			{Addresses: []string{"10.0.9.7/24"}},
			{Network: &api.Network{ID: "net-flat"}, Addresses: []string{"10.0.1.7/24"}},
		},
	}}

	got, err := (&Client{control: control}).TaskAttachments(context.Background(), "web-1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "net-flat", got[0].NetworkID)
}

// TestTaskAttachmentsReportsAMissingTask pins the not-found answer: a task the
// control socket does not hold is an error, not an empty attachment list, because the
// caller must not read "no networks" out of "no task".
func TestTaskAttachmentsReportsAMissingTask(t *testing.T) {
	_, err := (&Client{control: &fakeControl{}}).TaskAttachments(context.Background(), "missing")
	require.Error(t, err)
}

// TestTaskAttachmentsRefusesTheHTTPClient pins the honest gap: the HTTP API's task
// document flattens attachments, so a remote client answers a typed unavailable
// rather than inventing a network for an address.
func TestTaskAttachmentsRefusesTheHTTPClient(t *testing.T) {
	c := &Client{remote: &remoteClient{}}

	_, err := c.TaskAttachments(context.Background(), "web-1")
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
}
