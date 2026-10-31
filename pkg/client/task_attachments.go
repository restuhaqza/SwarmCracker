package client

// A task's network attachments are not part of the v1 Task document: the document
// flattens them into one address list (v1.Task.Addresses), which cannot say which
// network an address belongs to. A live migration needs that pairing - the
// re-anneal has to tell each node which attachment a moved guest's address was on,
// so the node can forget the guest on that network's bridge rather than on the
// node's flat one - so it is read here, from the one place that still holds it: the
// SwarmKit task behind the document.

import (
	"context"

	"github.com/moby/swarmkit/v2/api"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TaskAttachment is one network attachment of a task: the network the task is
// attached to, and the addresses the task holds there.
//
// It is not a document. It is the per-attachment view of the same facts
// v1.Task.Addresses flattens, and it exists because the flat list cannot answer
// "which bridge was this guest on".
type TaskAttachment struct {
	// NetworkID is the attachment's SwarmKit network ID. It names a network, never
	// a device: each node derives its own bridge for that network.
	NetworkID string
	// Network is the network's name, for a log line an operator can read.
	Network string
	// Addresses are the task's addresses on this network, in CIDR form, in the
	// order SwarmKit allocated them.
	Addresses []string
}

// TaskAttachments returns a task's network attachments in attachment order, which
// is also the order of the guest's interfaces: interface index i of the VM is
// attachment i, and the guest's MAC on it is runtime.GuestMAC(taskID, i).
//
// A task with no attachment answers an empty list, which is not an error: a guest
// with no network has no address a peer could have cached.
//
// It reads the local control socket, because only that view still has the
// attachment structure. A client configured with an Endpoint (the HTTP API) answers
// a typed unavailable: the document it can read flatten attachments, so it cannot
// say which network an address is on. A caller that needs this is the manager, and
// a manager always holds the local client.
func (c *Client) TaskAttachments(ctx context.Context, taskID string) ([]TaskAttachment, error) {
	if c.remote != nil {
		return nil, output.NewError(output.CodeUnavailable,
			"task network attachments are not part of the HTTP API's task document: read them over the control socket").
			WithDetails(map[string]any{"reason": "attachments_not_in_document"})
	}

	resp, err := c.control.GetTask(ctx, &api.GetTaskRequest{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Task == nil {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	return taskAttachments(resp.Task), nil
}
