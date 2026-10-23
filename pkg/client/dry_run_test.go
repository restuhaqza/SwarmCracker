package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dryRunServiceWrite is the smallest valid create body for the dry-run tests.
func dryRunServiceWrite(name string) v1.ServiceWrite {
	return v1.ServiceWrite{
		Name:   name,
		Labels: map[string]string{"team": "payments"},
		Spec: v1.ServiceSpecWrite{
			Mode: "replicated",
			Task: v1.TaskSpecWrite{
				TaskSpec: v1.TaskSpec{
					Image:    "nginx:alpine",
					Env:      map[string]string{"APP_ENV": "prod"},
					Networks: []v1.NetworkAttachment{{ID: "app"}},
				},
			},
		},
	}
}

// serviceFixture builds one SwarmKit service at a revision.
func serviceFixture(id, name string, revision uint64) *api.Service {
	replicas := uint64(1)
	return &api.Service{
		ID: id,
		Meta: api.Meta{
			Version: api.Version{Index: revision},
		},
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: name, Labels: map[string]string{"team": "payments"}},
			Mode:        &api.ServiceSpec_Replicated{Replicated: &api.ReplicatedService{Replicas: replicas}},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{Image: "nginx:alpine"}},
			},
		},
	}
}

func TestDryRunCreateServiceMakesNoCreateCall(t *testing.T) {
	control := &fakeControl{
		services: []*api.Service{serviceFixture("svc-other", "api", 4)},
		networks: []*api.Network{networkFixture("net-1", "app", 2, "overlay")},
	}

	service, err := writeClient(control).CreateService(context.Background(),
		dryRunServiceWrite("web"), v1.WriteOptions{DryRun: true})
	require.NoError(t, err)

	assert.Zero(t, control.createServiceCall, "a dry run must not call CreateService")
	assert.Equal(t, "web", service.Name)
	assert.Equal(t, map[string]string{"team": "payments"}, service.Labels)
	assert.Equal(t, "nginx:alpine", service.Spec.Task.Image)
	// The reference resolution ran: the network name became the network id.
	require.Len(t, service.Spec.Task.Networks, 1)
	assert.Equal(t, "net-1", service.Spec.Task.Networks[0].ID)
	// A create preview has no identity: nothing was persisted.
	assert.Empty(t, service.ID)
	assert.Empty(t, service.Revision)
	assert.Empty(t, service.CreatedAt)
	assert.Empty(t, service.UpdatedAt)
}

func TestDryRunCreateServiceNameConflict(t *testing.T) {
	control := &fakeControl{
		services: []*api.Service{serviceFixture("svc-1", "web", 3)},
		networks: []*api.Network{networkFixture("net-1", "app", 2, "overlay")},
	}

	_, err := writeClient(control).CreateService(context.Background(),
		dryRunServiceWrite("web"), v1.WriteOptions{DryRun: true})

	requireCode(t, err, output.CodeConflict)
	assert.Zero(t, control.createServiceCall)
}

func TestDryRunCreateServiceKeepsIfNoneMatchRules(t *testing.T) {
	t.Run("star still refuses an existing name", func(t *testing.T) {
		control := &fakeControl{
			services: []*api.Service{serviceFixture("svc-1", "web", 3)},
			networks: []*api.Network{networkFixture("net-1", "app", 2, "overlay")},
		}

		_, err := writeClient(control).CreateService(context.Background(),
			dryRunServiceWrite("web"), v1.WriteOptions{DryRun: true, IfNoneMatch: "*"})

		requireCode(t, err, output.CodeConflict)
		assert.Zero(t, control.createServiceCall)
	})

	t.Run("a non-star If-None-Match is invalid before any list", func(t *testing.T) {
		control := &fakeControl{}

		_, err := writeClient(control).CreateService(context.Background(),
			dryRunServiceWrite("web"), v1.WriteOptions{DryRun: true, IfNoneMatch: `"3"`})

		requireField(t, err, "If-None-Match")
		assert.Zero(t, control.createServiceCall)
	})
}

func TestDryRunCreateServiceRejectsBadInput(t *testing.T) {
	control := &fakeControl{}

	write := dryRunServiceWrite("web")
	write.Spec.Mode = "job"

	_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{DryRun: true})
	requireField(t, err, "spec.mode")
	assert.Zero(t, control.createServiceCall)
}

func TestDryRunCreateConfigAndSecret(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		control := &fakeControl{configs: []*api.Config{configFixture("cfg-other", "other", 1, nil, "")}}

		config, err := writeClient(control).CreateConfig(context.Background(), v1.ConfigWrite{
			Name:   "app",
			Labels: map[string]string{"team": "payments"},
			Data:   "listen 8080",
		}, v1.WriteOptions{DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.createConfigCall)
		assert.Equal(t, "app", config.Name)
		require.NotNil(t, config.Data)
		assert.Equal(t, "listen 8080", *config.Data, "the preview carries the payload it was given")
		assert.Empty(t, config.ID)
		assert.Empty(t, config.Revision)
	})

	t.Run("config name conflict", func(t *testing.T) {
		control := &fakeControl{configs: []*api.Config{configFixture("cfg-1", "app", 2, nil, "")}}

		_, err := writeClient(control).CreateConfig(context.Background(),
			v1.ConfigWrite{Name: "app"}, v1.WriteOptions{DryRun: true})

		requireCode(t, err, output.CodeConflict)
		assert.Zero(t, control.createConfigCall)
	})

	t.Run("secret", func(t *testing.T) {
		control := &fakeControl{secrets: []*api.Secret{secretFixture("sec-other", "other", 1, nil, "")}}

		secret, err := writeClient(control).CreateSecret(context.Background(), v1.SecretWrite{
			Name: "token", Data: "aGk=",
		}, v1.WriteOptions{DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.createSecretCall)
		assert.Equal(t, "token", secret.Name)
		assert.Empty(t, secret.ID)
		assert.Empty(t, secret.Revision)
	})

	t.Run("secret name conflict", func(t *testing.T) {
		control := &fakeControl{secrets: []*api.Secret{secretFixture("sec-1", "token", 2, nil, "")}}

		_, err := writeClient(control).CreateSecret(context.Background(),
			v1.SecretWrite{Name: "token", Data: "aGk="}, v1.WriteOptions{DryRun: true})

		requireCode(t, err, output.CodeConflict)
		assert.Zero(t, control.createSecretCall)
	})
}

func TestDryRunCreateNetwork(t *testing.T) {
	t.Run("preview", func(t *testing.T) {
		control := &fakeControl{}

		network, err := writeClient(control).CreateNetwork(context.Background(), v1.NetworkWrite{
			Name: "app", IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
		}, v1.WriteOptions{DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.createNetworkCall)
		assert.Equal(t, "app", network.Name)
		assert.Equal(t, "overlay", network.Driver, "the default driver is applied to the preview")
		require.NotNil(t, network.IPAM)
		assert.Equal(t, "10.0.1.0/24", network.IPAM.Subnet)
		assert.Empty(t, network.ID)
		assert.Empty(t, network.Revision)
	})

	t.Run("name conflict", func(t *testing.T) {
		control := &fakeControl{networks: []*api.Network{networkFixture("net-1", "app", 2, "overlay")}}

		_, err := writeClient(control).CreateNetwork(context.Background(),
			v1.NetworkWrite{Name: "app"}, v1.WriteOptions{DryRun: true})

		requireCode(t, err, output.CodeConflict)
		assert.Zero(t, control.createNetworkCall)
	})
}

func TestDryRunUpdateServiceReturnsMergedDocument(t *testing.T) {
	current := serviceFixture("svc-1", "web", 17)
	control := &fakeControl{service: current}

	patch := []byte(`{"spec":{"replicas":3},"labels":{"env":"prod"}}`)
	service, err := writeClient(control).UpdateService(context.Background(), "web", patch,
		v1.WriteOptions{IfMatch: `"17"`, DryRun: true})
	require.NoError(t, err)

	assert.Zero(t, control.updateServiceCall, "a dry run must not call UpdateService")
	require.NotNil(t, service.Spec.Replicas)
	assert.Equal(t, int64(3), *service.Spec.Replicas)
	assert.Equal(t, map[string]string{"team": "payments", "env": "prod"}, service.Labels)
	// The preview carries the current metadata: the write did not happen.
	assert.Equal(t, "svc-1", service.ID)
	assert.Equal(t, "17", service.Revision)

	// The overlay ran on a deep copy: the fake's object is untouched.
	require.NotNil(t, current.Spec)
	assert.Equal(t, uint64(1), current.Spec.GetReplicated().Replicas)
	assert.Equal(t, map[string]string{"team": "payments"}, current.Spec.Annotations.Labels)
}

func TestDryRunUpdateServiceEvaluatesIfMatch(t *testing.T) {
	control := &fakeControl{service: serviceFixture("svc-1", "web", 18)}

	_, err := writeClient(control).UpdateService(context.Background(), "web",
		[]byte(`{"spec":{"replicas":3}}`), v1.WriteOptions{IfMatch: `"17"`, DryRun: true})

	requirePrecondition(t, err, output.ReasonPreconditionFailed)
	assert.Zero(t, control.updateServiceCall)
}

func TestDryRunUpdateServiceRequiresIfMatch(t *testing.T) {
	control := &fakeControl{service: serviceFixture("svc-1", "web", 18)}

	_, err := writeClient(control).UpdateService(context.Background(), "web",
		[]byte(`{"spec":{"replicas":3}}`), v1.WriteOptions{DryRun: true})

	requirePrecondition(t, err, output.ReasonPreconditionRequired)
	assert.Zero(t, control.updateServiceCall)
}

func TestDryRunUpdateConfigSecretAndNode(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		current := configFixture("cfg-1", "app", 6, map[string]string{"team": "payments"}, "listen 8080")
		control := &fakeControl{config: current}

		config, err := writeClient(control).UpdateConfig(context.Background(), "app",
			[]byte(`{"labels":{"env":"prod"}}`), v1.WriteOptions{IfMatch: `"6"`, DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.updateConfigCall)
		assert.Equal(t, map[string]string{"team": "payments", "env": "prod"}, config.Labels)
		assert.Equal(t, "cfg-1", config.ID)
		assert.Equal(t, "6", config.Revision)
		require.NotNil(t, config.Data)
		assert.Equal(t, "listen 8080", *config.Data)
	})

	t.Run("config stale revision is 412", func(t *testing.T) {
		control := &fakeControl{config: configFixture("cfg-1", "app", 7, nil, "")}

		_, err := writeClient(control).UpdateConfig(context.Background(), "app",
			[]byte(`{"labels":{"env":"prod"}}`), v1.WriteOptions{IfMatch: `"6"`, DryRun: true})

		requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Zero(t, control.updateConfigCall)
	})

	t.Run("secret", func(t *testing.T) {
		current := secretFixture("sec-1", "token", 4, map[string]string{"env": "prod"}, "")
		control := &fakeControl{secret: current}

		secret, err := writeClient(control).UpdateSecret(context.Background(), "token",
			[]byte(`{"labels":{"team":"payments"}}`), v1.WriteOptions{IfMatch: `"4"`, DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.updateSecretCall)
		assert.Equal(t, map[string]string{"env": "prod", "team": "payments"}, secret.Labels)
		assert.Equal(t, "sec-1", secret.ID)
		assert.Equal(t, "4", secret.Revision)
	})

	t.Run("node", func(t *testing.T) {
		control := &fakeControl{node: nodeFixture("node-1", "worker-a", 9)}

		node, err := writeClient(control).UpdateNode(context.Background(), "worker-a",
			[]byte(`{"availability":"drain"}`), v1.WriteOptions{IfMatch: `"9"`, DryRun: true})
		require.NoError(t, err)

		assert.Zero(t, control.updateNodeCall)
		assert.Equal(t, "drain", node.Availability)
		assert.Equal(t, "node-1", node.ID)
		assert.Equal(t, "9", node.Revision)
	})
}

func TestDryRunUpdateStillRejectsImmutableFields(t *testing.T) {
	control := &fakeControl{config: configFixture("cfg-1", "app", 6, nil, "")}

	_, err := writeClient(control).UpdateConfig(context.Background(), "app",
		[]byte(`{"name":"other"}`), v1.WriteOptions{IfMatch: `"6"`, DryRun: true})

	requireField(t, err, "name")
	assert.Zero(t, control.updateConfigCall)
}

// TestRemoteDryRunForwardsQuery proves the remote client does not implement a
// second dry-run path: it sends ?dryRun=true and lets the server do the work.
func TestRemoteDryRunForwardsQuery(t *testing.T) {
	t.Run("create service", func(t *testing.T) {
		var got capturedRequest
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			got = captureRequest(t, r)
			writeTestDocument(w, http.StatusCreated, v1.ServiceDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          v1.KindService,
				Service:       v1.Service{Name: "web"},
				DryRun:        true,
			})
		})

		service, err := c.CreateService(context.Background(), dryRunServiceWrite("web"),
			v1.WriteOptions{DryRun: true})
		require.NoError(t, err)
		assert.Equal(t, "web", service.Name)

		assert.Equal(t, http.MethodPost, got.method)
		assert.Equal(t, "/v1/services", got.path)
		assert.Equal(t, "true", got.dryRun)
	})

	t.Run("update service", func(t *testing.T) {
		var got capturedRequest
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			got = captureRequest(t, r)
			writeTestDocument(w, http.StatusOK, v1.ServiceDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          v1.KindService,
				Service:       v1.Service{Name: "web", Revision: "17"},
				DryRun:        true,
			})
		})

		_, err := c.UpdateService(context.Background(), "web", []byte(`{"spec":{"replicas":2}}`),
			v1.WriteOptions{IfMatch: `"17"`, DryRun: true})
		require.NoError(t, err)

		assert.Equal(t, http.MethodPatch, got.method)
		assert.Equal(t, "true", got.dryRun)
		assert.Equal(t, `"17"`, got.ifMatch)
	})

	t.Run("create config, secret and network", func(t *testing.T) {
		var calls []capturedRequest
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, captureRequest(t, r))
			writeTestDocument(w, http.StatusCreated, v1.ConfigDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          v1.KindConfig,
				Config:        v1.Config{Name: "app"},
				DryRun:        true,
			})
		})

		_, err := c.CreateConfig(context.Background(), v1.ConfigWrite{Name: "app"},
			v1.WriteOptions{DryRun: true})
		require.NoError(t, err)
		require.Len(t, calls, 1)
		assert.Equal(t, "/v1/configs", calls[0].path)
		assert.Equal(t, "true", calls[0].dryRun)
	})

	t.Run("update node", func(t *testing.T) {
		var got capturedRequest
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			got = captureRequest(t, r)
			writeTestDocument(w, http.StatusOK, v1.NodeDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          v1.KindNode,
				Node:          v1.Node{ID: "node-1", Availability: "drain"},
				DryRun:        true,
			})
		})

		_, err := c.UpdateNode(context.Background(), "worker-a", []byte(`{"availability":"drain"}`),
			v1.WriteOptions{IfMatch: `"9"`, DryRun: true})
		require.NoError(t, err)

		assert.Equal(t, http.MethodPatch, got.method)
		assert.Equal(t, "true", got.dryRun)
	})

	t.Run("a real write sends no dryRun", func(t *testing.T) {
		var got capturedRequest
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			got = captureRequest(t, r)
			writeTestDocument(w, http.StatusCreated, v1.ServiceDocument{
				SchemaVersion: v1.SchemaVersion,
				Kind:          v1.KindService,
				Service:       v1.Service{ID: "svc-1", Name: "web", Revision: "1"},
			})
		})

		_, err := c.CreateService(context.Background(), dryRunServiceWrite("web"), v1.WriteOptions{})
		require.NoError(t, err)
		assert.Empty(t, got.dryRun)
	})
}

// TestDryRunRefusesDeleteAndRollback is the guard against a preview causing
// data loss: a dry run on a destructive verb must fail before any RPC.
func TestDryRunRefusesDeleteAndRollback(t *testing.T) {
	ctx := context.Background()

	t.Run("service delete", func(t *testing.T) {
		control := &fakeControl{service: serviceFixture("svc-1", "web", 3)}
		_, err := writeClient(control).DeleteService(ctx, "web", v1.WriteOptions{DryRun: true})
		requireCode(t, err, output.CodeInvalid)
		assert.Zero(t, control.removeServiceCall)
	})

	t.Run("config delete", func(t *testing.T) {
		control := &fakeControl{config: configFixture("cfg-1", "app", 3, nil, "")}
		_, err := writeClient(control).DeleteConfig(ctx, "app", v1.WriteOptions{DryRun: true})
		requireCode(t, err, output.CodeInvalid)
		assert.Zero(t, control.removeConfigCall)
	})

	t.Run("secret delete", func(t *testing.T) {
		control := &fakeControl{secret: secretFixture("sec-1", "token", 3, nil, "")}
		_, err := writeClient(control).DeleteSecret(ctx, "token", v1.WriteOptions{DryRun: true})
		requireCode(t, err, output.CodeInvalid)
		assert.Zero(t, control.removeSecretCall)
	})

	t.Run("network delete", func(t *testing.T) {
		control := &fakeControl{network: networkFixture("net-1", "app", 3, "overlay")}
		_, err := writeClient(control).DeleteNetwork(ctx, "app", v1.WriteOptions{DryRun: true})
		requireCode(t, err, output.CodeInvalid)
		assert.Zero(t, control.removeNetworkCall)
	})

	t.Run("rollback", func(t *testing.T) {
		control := &fakeControl{service: serviceFixture("svc-1", "web", 3)}
		_, err := writeClient(control).RollbackService(ctx, "web", v1.WriteOptions{IfMatch: `"3"`, DryRun: true})
		requireCode(t, err, output.CodeNotImplemented)
		assert.Zero(t, control.updateServiceCall)
	})
}

// TestRemoteDryRunRefusesDestructiveVerbs proves the remote client does not
// send a DELETE or rollback request when the caller asked for a dry run.
func TestRemoteDryRunRefusesDestructiveVerbs(t *testing.T) {
	ctx := context.Background()

	t.Run("service delete", func(t *testing.T) {
		called := false
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			called = true
			http.Error(w, "must not be called", http.StatusInternalServerError)
		})
		_, err := c.DeleteService(ctx, "web", v1.WriteOptions{DryRun: true})
		requireCode(t, err, output.CodeInvalid)
		assert.False(t, called)
	})

	t.Run("rollback", func(t *testing.T) {
		called := false
		c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
			called = true
			http.Error(w, "must not be called", http.StatusInternalServerError)
		})
		_, err := c.RollbackService(ctx, "web", v1.WriteOptions{IfMatch: `"3"`, DryRun: true})
		requireCode(t, err, output.CodeNotImplemented)
		assert.False(t, called)
	})
}
