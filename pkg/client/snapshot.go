package client

// This file owns the snapshot stop-restored call. The snapshot surface is
// manager-relayed: the public route resolves the task and the manager forwards
// the DELETE to the node that holds the restored VM, so the client cannot do the
// work on a local control socket.

import (
	"context"
	"net/http"

	"github.com/restuhaqza/swarmcracker/pkg/output"
)

// StopRestoredTask stops the restored VM a previous restore started for the task
// on the node that ran it. It is a DELETE /v1/tasks/{ref}/restored with no body
// and no query; the server answers 204, or 404 not_found with
// details.reason "no_restored_vm" when the node tracks no such VM.
//
// The local control socket has no equivalent: a restored VM is stopped by the
// node that tracks its process, so this method requires a remote endpoint.
func (c *Client) StopRestoredTask(ctx context.Context, ref string) error {
	if c.remote == nil {
		return output.NewError(output.CodeUsage,
			"stopping a restored VM needs --host: the process is tracked by the node, not the local control socket")
	}
	return c.remote.stopRestoredTask(ctx, ref)
}

// stopRestoredTask sends DELETE /v1/tasks/{ref}/restored.
func (r *remoteClient) stopRestoredTask(ctx context.Context, ref string) error {
	resp, err := r.do(ctx, remoteRequest{
		method:   http.MethodDelete,
		segments: []string{"v1", "tasks", ref, "restored"},
	})
	if err != nil {
		return err
	}
	if resp.status != http.StatusNoContent {
		// The server contract says 204. A 2xx with a body is a server that broke
		// the wire contract, not a successful stop.
		return output.NewErrorf(output.CodeInternal,
			"stop restored VM: server returned status %d, want 204", resp.status)
	}
	return nil
}
