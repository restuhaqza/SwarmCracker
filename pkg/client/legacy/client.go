package legacy

import (
	"github.com/moby/swarmkit/v2/api"
	"google.golang.org/grpc"
)

// ControlClient is the raw SwarmKit control surface used by command paths whose
// legacy JSON or table output still needs unmapped fields. It exists only inside
// this transitional package so command code never names the SwarmKit type
// directly. New contract paths use pkg/client instead.
type ControlClient = api.ControlClient

// NewControlClient wraps a SwarmKit connection for a legacy command path.
func NewControlClient(conn *grpc.ClientConn) ControlClient {
	return api.NewControlClient(conn)
}
