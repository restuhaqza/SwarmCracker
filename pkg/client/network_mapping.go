package client

import (
	"sort"
	"strconv"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

func networkFromSwarmkit(network *api.Network) v1.Network {
	out := v1.Network{
		ID:         network.ID,
		Name:       network.Spec.Annotations.Name,
		Revision:   strconv.FormatUint(network.Meta.Version.Index, 10),
		CreatedAt:  timestamp(network.Meta.CreatedAt),
		Labels:     copyMap(network.Spec.Annotations.Labels),
		Attachable: network.Spec.Attachable,
	}
	if network.Spec.DriverConfig != nil {
		out.Driver = network.Spec.DriverConfig.Name
	}
	if network.Spec.IPAM != nil {
		for _, config := range network.Spec.IPAM.Configs {
			if config == nil {
				continue
			}
			if config.Subnet == "" && config.Gateway == "" {
				continue
			}
			out.IPAM = &v1.NetworkIPAM{Subnet: config.Subnet, Gateway: config.Gateway}
			break
		}
	}
	return out
}

func isBuiltinNetwork(network *api.Network) bool {
	if network.Spec.Ingress {
		return true
	}
	switch network.Spec.Annotations.Name {
	case "ingress", "docker_gwbridge":
		return true
	default:
		return false
	}
}

func sortNetworks(networks []*api.Network) []*api.Network {
	sorted := make([]*api.Network, 0, len(networks))
	for _, network := range networks {
		if network != nil {
			sorted = append(sorted, network)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		left, right := timestampTime(sorted[i].Meta.CreatedAt), timestampTime(sorted[j].Meta.CreatedAt)
		if !left.Equal(right) {
			return left.Before(right)
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}
