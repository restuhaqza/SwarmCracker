package client

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// nodeFromSwarmkit maps one SwarmKit node to the v1 read model.
func nodeFromSwarmkit(node *api.Node) v1.Node {
	return v1.Node{
		ID:           node.ID,
		Hostname:     nodeHostname(node.Description),
		Revision:     strconv.FormatUint(node.Meta.Version.Index, 10),
		CreatedAt:    timestamp(node.Meta.CreatedAt),
		UpdatedAt:    timestamp(node.Meta.UpdatedAt),
		Role:         strings.ToLower(node.Spec.DesiredRole.String()),
		Membership:   strings.ToLower(node.Spec.Membership.String()),
		Availability: strings.ToLower(node.Spec.Availability.String()),
		State:        strings.ToLower(node.Status.State.String()),
		Address:      node.Status.Addr,
		Labels:       copyMap(node.Spec.Annotations.Labels),
		Resources:    nodeResources(node.Description),
		Manager:      nodeManager(node.ManagerStatus),
		AgentVersion: nodeAgentVersion(node.Description),
	}
}

// nodeHostname reads the agent-reported hostname; a node without a description
// has none.
func nodeHostname(description *api.NodeDescription) string {
	if description == nil {
		return ""
	}
	return description.Hostname
}

// nodeResources converts advertised capacity to the contract shape. The object
// is omitted when the node reports no capacity at all.
func nodeResources(description *api.NodeDescription) *v1.NodeResources {
	if description == nil || description.Resources == nil {
		return nil
	}
	resources := description.Resources
	if resources.NanoCPUs == 0 && resources.MemoryBytes == 0 {
		return nil
	}
	return &v1.NodeResources{
		CPUs:        float64(resources.NanoCPUs) / 1e9,
		MemoryBytes: resources.MemoryBytes,
	}
}

// nodeManager maps manager status. Workers carry no manager status, so their
// Manager stays nil and the key is omitted.
func nodeManager(status *api.ManagerStatus) *v1.NodeManager {
	if status == nil {
		return nil
	}
	return &v1.NodeManager{
		Leader:    status.Leader,
		Reachable: status.Reachability == api.RaftMemberStatus_REACHABLE,
		Address:   status.Addr,
		RaftID:    status.RaftID,
	}
}

func nodeAgentVersion(description *api.NodeDescription) string {
	if description == nil || description.Engine == nil {
		return ""
	}
	return description.Engine.EngineVersion
}

// nodeCreatedAt returns a sortable creation time; a nil timestamp sorts as the
// zero time.
func nodeCreatedAt(node *api.Node) time.Time {
	return timestampTime(node.Meta.CreatedAt)
}

// sortNodes orders by creation time ascending, then ID ascending. It sorts a
// copy and leaves the caller's slice untouched.
func sortNodes(nodes []*api.Node) []*api.Node {
	sorted := make([]*api.Node, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		sorted = append(sorted, node)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := nodeCreatedAt(sorted[i]), nodeCreatedAt(sorted[j])
		if !left.Equal(right) {
			return left.Before(right)
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}
