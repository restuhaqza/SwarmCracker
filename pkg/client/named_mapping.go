package client

import (
	"sort"
	"strconv"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
)

// configFromSwarmkit maps one SwarmKit config. The payload is carried here; the
// HTTP layer is what withholds it from an identity without configs:read-values,
// so a local caller of pkg/client still sees the value it is entitled to.
func configFromSwarmkit(config *api.Config) v1.Config {
	out := v1.Config{
		ID:        config.ID,
		Name:      config.Spec.Annotations.Name,
		Revision:  strconv.FormatUint(config.Meta.Version.Index, 10),
		CreatedAt: timestamp(config.Meta.CreatedAt),
		UpdatedAt: timestamp(config.Meta.UpdatedAt),
		Labels:    copyMap(config.Spec.Annotations.Labels),
	}
	if len(config.Spec.Data) > 0 {
		data := string(config.Spec.Data)
		out.Data = &data
	}
	return out
}

func secretFromSwarmkit(secret *api.Secret) v1.Secret {
	driver := ""
	if secret.Spec.Driver != nil {
		driver = secret.Spec.Driver.Name
	}
	return v1.Secret{
		ID:        secret.ID,
		Name:      secret.Spec.Annotations.Name,
		Revision:  strconv.FormatUint(secret.Meta.Version.Index, 10),
		CreatedAt: timestamp(secret.Meta.CreatedAt),
		UpdatedAt: timestamp(secret.Meta.UpdatedAt),
		Labels:    copyMap(secret.Spec.Annotations.Labels),
		Driver:    driver,
	}
}

func sortConfigs(configs []*api.Config) []*api.Config {
	sorted := make([]*api.Config, 0, len(configs))
	for _, config := range configs {
		if config != nil {
			sorted = append(sorted, config)
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

func sortSecrets(secrets []*api.Secret) []*api.Secret {
	sorted := make([]*api.Secret, 0, len(secrets))
	for _, secret := range secrets {
		if secret != nil {
			sorted = append(sorted, secret)
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
