package client

// This file owns the client side of one live migration: the call that asks a
// manager to move one task's VM to another node and answers what it did.
//
//	POST /v1/migrations   {taskId, targetNode}   -> 201 Migration
//
// There is no GET, no list and no status route to mirror: the call is
// synchronous and its answer is the whole report. That also makes this one
// manager-API-only operation in pkg/client: a task's memory image moves between
// hosts, which only a manager can drive, so a client dialing the local control
// socket has nowhere to send the request.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
)

const (
	// migrationDefaultTimeout bounds one migration call when the caller's
	// context carries no deadline. It is the manager's own worst case - three
	// minutes of flow plus up to a minute of work it does before it answers -
	// with a margin, because the answer is worth waiting for: the move runs
	// whether or not the client is still listening.
	migrationDefaultTimeout = 5 * time.Minute
)

// Migrate asks the manager to move one task's VM to another node and returns the
// report of what it did.
//
// taskID and targetNode are references the manager resolves (exact ID, unique ID
// prefix or, for a node, its hostname), which is what keeps a caller from having
// to know how a manager names its own cluster. Nothing here names the source:
// the task's own node is the source.
//
// A failure is the manager's own typed error document, field for field - the
// failing phase in details.phase, the leader to retry at in details.leader - so
// a caller prints it and exits with its code instead of inventing a reason.
//
// The deadline is the caller's: a context with a deadline sets this call's
// request timeout too, because the client's own default (seconds) is shorter
// than any move. A move that lands after the client gave up still completes on
// the manager, so a timeout means "ask again later", never "it did not happen".
func (c *Client) Migrate(ctx context.Context, taskID, targetNode string) (v1.Migration, error) {
	if c.remote == nil {
		return v1.Migration{}, output.NewError(output.CodeUsage,
			"live migration requires the manager API: configure an endpoint with --host")
	}
	return c.remote.migrate(ctx, taskID, targetNode)
}

// migrate sends POST /v1/migrations and decodes the report.
func (r *remoteClient) migrate(ctx context.Context, taskID, targetNode string) (v1.Migration, error) {
	body, err := json.Marshal(v1.MigrationRequest{TaskID: taskID, TargetNode: targetNode})
	if err != nil {
		return v1.Migration{}, output.NewErrorf(output.CodeInternal,
			"encode the migration request: %v", err)
	}

	resp, err := r.do(ctx, remoteRequest{
		method:      http.MethodPost,
		segments:    []string{"v1", "migrations"},
		body:        body,
		contentType: mediaTypeV1,
		timeout:     migrationRequestTimeout(ctx),
	})
	if err != nil {
		return v1.Migration{}, err
	}

	var doc v1.Migration
	if err := decodeJSON("v1/migrations", resp.body, &doc); err != nil {
		return v1.Migration{}, err
	}
	// The route answers one document kind with the two fields every caller
	// prints. A different answer is a manager this build cannot read, not a
	// migration: reporting it as a report would let a caller print an outcome
	// that was never stated.
	if !validMigrationDocument(doc) {
		return v1.Migration{}, output.NewErrorf(output.CodeInternal,
			"the manager answered a %q document for a migration: want %q naming the task and the outcome",
			doc.Kind, v1.KindMigration)
	}
	return doc, nil
}

// migrationRequestTimeout is the request deadline one migration call uses: the
// caller's remaining deadline when its context has one, otherwise this
// package's default. Both are needed: the context bounds the whole call, and
// the request timeout replaces the client's own - which was chosen for reads -
// so a move is not cut off after seconds.
func migrationRequestTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return migrationDefaultTimeout
}

// validMigrationDocument reports whether doc is a report this build can read: the
// envelope kind it asked for, and the two fields every caller prints. A document
// that fails it is a manager this build cannot read.
func validMigrationDocument(doc v1.Migration) bool {
	return doc.Kind == v1.KindMigration &&
		strings.TrimSpace(doc.TaskID) != "" &&
		strings.TrimSpace(doc.Outcome) != ""
}
