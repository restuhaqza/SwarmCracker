package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	gogotypes "github.com/gogo/protobuf/types"
	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// writeClient builds a local client over one fake control surface.
func writeClient(control *fakeControl) *Client { return &Client{control: control} }

func int64Ptr(v int64) *int64 { return &v }

// networkByName is one resolvable network for create/update tests.
func networkByName(id, name string) *api.Network {
	return &api.Network{ID: id, Spec: api.NetworkSpec{Annotations: api.Annotations{Name: name}}}
}

func requireCode(t *testing.T, err error, code output.Code) *output.Error {
	t.Helper()
	var typed *output.Error
	require.Truef(t, errors.As(err, &typed), "want *output.Error, got %T: %v", err, err)
	assert.Equal(t, code, typed.Code, typed.Message)
	return typed
}

func requireField(t *testing.T, err error, path string) {
	t.Helper()
	typed := requireCode(t, err, output.CodeInvalid)
	fields, ok := typed.Details["fields"].(map[string]any)
	require.Truef(t, ok, "details.fields missing: %#v", typed.Details)
	assert.Contains(t, fields, path, "details.fields must name the offending path")
}

func TestCreateServiceMapsEveryModeledField(t *testing.T) {
	created := &api.Service{
		ID: "svc-9",
		Meta: api.Meta{
			Version:   api.Version{Index: 5},
			CreatedAt: &gogotypes.Timestamp{Seconds: 1700000000},
		},
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{Name: "web"},
			Mode:        &api.ServiceSpec_Replicated{Replicated: &api.ReplicatedService{Replicas: 3}},
		},
	}
	control := &fakeControl{
		networks:       []*api.Network{networkByName("net-1", "app")},
		createdService: created,
	}

	write := v1.ServiceWrite{
		Name:   "web",
		Labels: map[string]string{"team": "payments"},
		Spec: v1.ServiceSpecWrite{
			Mode:     "replicated",
			Replicas: int64Ptr(3),
			Task: v1.TaskSpecWrite{
				TaskSpec: v1.TaskSpec{
					Image:   "nginx:1.27-alpine",
					Command: []string{"nginx"},
					Args:    []string{"-g", "daemon off;"},
					Env:     map[string]string{"Z_LAST": "z", "A_FIRST": "a"},
					User:    "101:101",
					Workdir: "/usr/share/nginx/html",
					VCPUs:   2,
					// 512 MiB
					MemoryBytes: 512 << 20,
					Ports: []v1.Port{{
						Target: 80, Published: 8080, Protocol: "tcp", Mode: "ingress",
					}},
					Mounts: []v1.Mount{{
						Type: "volume", Source: "webdata", Target: "/data",
					}},
					Networks: []v1.NetworkAttachment{{ID: "app", Aliases: []string{"web"}}},
					RestartPolicy: &v1.RestartPolicy{
						Condition: "on-failure", DelayMs: 5000, MaxAttempts: 3, WindowMs: 1000,
					},
					Healthcheck: &v1.Healthcheck{
						Test:       []string{"CMD", "true"},
						IntervalMs: 10000,
						TimeoutMs:  3000,
						Retries:    3,
						// 5 s
						StartPeriodMs: 5000,
					},
					StopSignal:     "SIGTERM",
					StopGraceMs:    10000,
					ReadOnlyRootfs: true,
				},
				RegistryAuth: "cmVnaXN0cnktYXV0aA==",
			},
			Update: &v1.UpdateConfig{
				Parallelism: 2, DelayMs: 1000, FailureAction: "rollback",
				Order: "start-first", MonitorMs: 30000, MaxFailureRatio: 0.25,
			},
			Rollback:  &v1.UpdateConfig{Parallelism: 1, FailureAction: "pause"},
			Placement: &v1.Placement{Constraints: []string{"node.labels.zone==a"}},
		},
	}

	got, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "svc-9", got.ID)

	require.NotNil(t, control.createServiceReq)
	spec := control.createServiceReq.Spec
	require.NotNil(t, spec)

	assert.Equal(t, "web", spec.Annotations.Name)
	assert.Equal(t, map[string]string{"team": "payments"}, spec.Annotations.Labels)

	replicated := spec.GetReplicated()
	require.NotNil(t, replicated, "mode must be replicated")
	assert.EqualValues(t, 3, replicated.Replicas)

	container := spec.Task.GetContainer()
	require.NotNil(t, container)
	assert.Equal(t, "nginx:1.27-alpine", container.Image)
	assert.Equal(t, []string{"nginx"}, container.Command)
	assert.Equal(t, []string{"-g", "daemon off;"}, container.Args)
	assert.Equal(t, []string{"A_FIRST=a", "Z_LAST=z"}, container.Env, "env must be sorted")
	assert.Equal(t, "101:101", container.User)
	assert.Equal(t, "/usr/share/nginx/html", container.Dir)
	assert.Equal(t, "SIGTERM", container.StopSignal)
	assert.EqualValues(t, 10_000, durationMs(container.StopGracePeriod))
	assert.True(t, container.ReadOnly)
	require.Len(t, container.Mounts, 1)
	assert.Equal(t, api.MountTypeVolume, container.Mounts[0].Type)
	assert.Equal(t, "webdata", container.Mounts[0].Source)
	assert.Equal(t, "/data", container.Mounts[0].Target)
	require.NotNil(t, container.Healthcheck)
	assert.Equal(t, []string{"CMD", "true"}, container.Healthcheck.Test)
	assert.EqualValues(t, 10_000, durationMs(container.Healthcheck.Interval))
	assert.EqualValues(t, 3_000, durationMs(container.Healthcheck.Timeout))
	assert.EqualValues(t, 5_000, durationMs(container.Healthcheck.StartPeriod))
	assert.EqualValues(t, 3, container.Healthcheck.Retries)
	require.NotNil(t, container.PullOptions)
	assert.Equal(t, "cmVnaXN0cnktYXV0aA==", container.PullOptions.RegistryAuth)

	require.NotNil(t, spec.Task.Resources)
	require.NotNil(t, spec.Task.Resources.Limits)
	assert.EqualValues(t, 2*nanoCPUsPerVCPU, spec.Task.Resources.Limits.NanoCPUs)
	assert.EqualValues(t, 512<<20, spec.Task.Resources.Limits.MemoryBytes)

	require.NotNil(t, spec.Task.Restart)
	assert.Equal(t, api.RestartOnFailure, spec.Task.Restart.Condition)
	assert.EqualValues(t, 5_000, durationMs(spec.Task.Restart.Delay))
	assert.EqualValues(t, 3, spec.Task.Restart.MaxAttempts)
	assert.EqualValues(t, 1_000, durationMs(spec.Task.Restart.Window))

	require.NotNil(t, spec.Task.Placement)
	assert.Equal(t, []string{"node.labels.zone==a"}, spec.Task.Placement.Constraints)

	require.Len(t, spec.Task.Networks, 1)
	assert.Equal(t, "net-1", spec.Task.Networks[0].Target, "a network name resolves to its id")
	assert.Equal(t, []string{"web"}, spec.Task.Networks[0].Aliases)

	require.NotNil(t, spec.Endpoint)
	require.Len(t, spec.Endpoint.Ports, 1)
	assert.EqualValues(t, 80, spec.Endpoint.Ports[0].TargetPort)
	assert.EqualValues(t, 8080, spec.Endpoint.Ports[0].PublishedPort)
	assert.Equal(t, api.ProtocolTCP, spec.Endpoint.Ports[0].Protocol)
	assert.Equal(t, api.PublishModeIngress, spec.Endpoint.Ports[0].PublishMode)

	require.NotNil(t, spec.Update)
	assert.EqualValues(t, 2, spec.Update.Parallelism)
	assert.EqualValues(t, 1_000, spec.Update.Delay.Milliseconds())
	assert.Equal(t, api.UpdateConfig_ROLLBACK, spec.Update.FailureAction)
	assert.Equal(t, api.UpdateConfig_START_FIRST, spec.Update.Order)
	assert.EqualValues(t, 30_000, durationMs(spec.Update.Monitor))
	assert.InDelta(t, 0.25, spec.Update.MaxFailureRatio, 1e-6)

	require.NotNil(t, spec.Rollback)
	assert.EqualValues(t, 1, spec.Rollback.Parallelism)

	// The write-only credential must never come back on the response document.
	document, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(document), "registryAuth")
}

func TestCreateServiceGlobalModeAndDefaults(t *testing.T) {
	t.Run("global omits replicas", func(t *testing.T) {
		control := &fakeControl{createdService: &api.Service{ID: "svc-g", Meta: api.Meta{Version: api.Version{Index: 2}}}}
		write := v1.ServiceWrite{
			Name: "global",
			Spec: v1.ServiceSpecWrite{
				Mode: "global",
				Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{Image: "busybox"}},
			},
		}

		_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
		require.NoError(t, err)
		assert.NotNil(t, control.createServiceReq.Spec.GetGlobal())
		assert.Nil(t, control.createServiceReq.Spec.GetReplicated())
	})

	t.Run("empty mode and replicas default", func(t *testing.T) {
		control := &fakeControl{createdService: &api.Service{ID: "svc-d", Meta: api.Meta{Version: api.Version{Index: 2}}}}
		write := v1.ServiceWrite{
			Name: "default",
			Spec: v1.ServiceSpecWrite{
				Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{Image: "busybox"}},
			},
		}

		_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
		require.NoError(t, err)
		replicated := control.createServiceReq.Spec.GetReplicated()
		require.NotNil(t, replicated)
		assert.EqualValues(t, 1, replicated.Replicas)
	})
}

func TestCreateServiceValidation(t *testing.T) {
	valid := v1.ServiceWrite{
		Name: "web",
		Spec: v1.ServiceSpecWrite{Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{Image: "nginx"}}},
	}

	tests := []struct {
		name   string
		mutate func(*v1.ServiceWrite)
		field  string
	}{
		{"missing name", func(w *v1.ServiceWrite) { w.Name = "  " }, "name"},
		{"missing image", func(w *v1.ServiceWrite) { w.Spec.Task.Image = "" }, "spec.task.image"},
		{"unknown mode", func(w *v1.ServiceWrite) { w.Spec.Mode = "replicated-job" }, "spec.mode"},
		{"global with replicas", func(w *v1.ServiceWrite) {
			w.Spec.Mode = "global"
			w.Spec.Replicas = int64Ptr(2)
		}, "spec.replicas"},
		{"negative replicas", func(w *v1.ServiceWrite) { w.Spec.Replicas = int64Ptr(-1) }, "spec.replicas"},
		{"negative vcpus", func(w *v1.ServiceWrite) { w.Spec.Task.VCPUs = -1 }, "spec.task.vcpus"},
		{"negative memory", func(w *v1.ServiceWrite) { w.Spec.Task.MemoryBytes = -1 }, "spec.task.memoryBytes"},
		{"negative stop grace", func(w *v1.ServiceWrite) { w.Spec.Task.StopGraceMs = -1 }, "spec.task.stopGraceMs"},
		{"empty env key", func(w *v1.ServiceWrite) {
			w.Spec.Task.Env = map[string]string{"": "x"}
		}, "spec.task.env"},
		{"env key with equals", func(w *v1.ServiceWrite) {
			w.Spec.Task.Env = map[string]string{"A=B": "x"}
		}, "spec.task.env"},
		{"env key with surrounding space", func(w *v1.ServiceWrite) {
			w.Spec.Task.Env = map[string]string{" A": "x"}
		}, "spec.task.env"},
		{"bad port protocol", func(w *v1.ServiceWrite) {
			w.Spec.Task.Ports = []v1.Port{{Target: 80, Protocol: "tcp6"}}
		}, "spec.task.ports[0].protocol"},
		{"bad publish mode", func(w *v1.ServiceWrite) {
			w.Spec.Task.Ports = []v1.Port{{Target: 80, Mode: "Ingress"}}
		}, "spec.task.ports[0].mode"},
		{"bad mount type", func(w *v1.ServiceWrite) {
			w.Spec.Task.Mounts = []v1.Mount{{Type: "bind9", Target: "/data"}}
		}, "spec.task.mounts[0].type"},
		{"empty healthcheck", func(w *v1.ServiceWrite) {
			w.Spec.Task.Healthcheck = &v1.Healthcheck{}
		}, "spec.task.healthcheck.test"},
		{"bad restart condition", func(w *v1.ServiceWrite) {
			w.Spec.Task.RestartPolicy = &v1.RestartPolicy{Condition: "always"}
		}, "spec.task.restartPolicy.condition"},
		{"bad update failure action", func(w *v1.ServiceWrite) {
			w.Spec.Update = &v1.UpdateConfig{FailureAction: "retry"}
		}, "spec.update.failureAction"},
		{"bad update order", func(w *v1.ServiceWrite) {
			w.Spec.Update = &v1.UpdateConfig{Order: "parallel"}
		}, "spec.update.order"},
		{"max failure ratio", func(w *v1.ServiceWrite) {
			w.Spec.Update = &v1.UpdateConfig{MaxFailureRatio: 2}
		}, "spec.update.maxFailureRatio"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			write := valid
			tc.mutate(&write)

			control := &fakeControl{}
			_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
			requireField(t, err, tc.field)
			assert.Zero(t, control.createServiceCall, "an invalid write must not reach SwarmKit")
		})
	}
}

func TestCreateServiceUnknownNetwork(t *testing.T) {
	control := &fakeControl{networks: []*api.Network{networkByName("net-1", "app")}}
	write := v1.ServiceWrite{
		Name: "web",
		Spec: v1.ServiceSpecWrite{
			Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{
				Image:    "nginx",
				Networks: []v1.NetworkAttachment{{ID: "missing"}},
			}},
		},
	}

	_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
	requireField(t, err, "spec.task.networks[0].id")
	assert.Zero(t, control.createServiceCall)
}

func TestCreateServiceConflictAndIfNoneMatch(t *testing.T) {
	write := v1.ServiceWrite{
		Name: "web",
		Spec: v1.ServiceSpecWrite{Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{Image: "nginx"}}},
	}

	t.Run("duplicate name is conflict", func(t *testing.T) {
		control := &fakeControl{createServiceErr: status.Error(codes.AlreadyExists, "service web already exists")}
		_, err := writeClient(control).CreateService(context.Background(), write, v1.WriteOptions{})
		require.Error(t, err)
		assert.Equal(t, codes.AlreadyExists, status.Code(err))
		// pkg/output maps AlreadyExists to the contract's conflict code.
		requireCode(t, output.FromGRPC(err), output.CodeConflict)
	})

	t.Run("If-None-Match star is accepted", func(t *testing.T) {
		control := &fakeControl{createdService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 1}}}}
		_, err := writeClient(control).CreateService(context.Background(), write,
			v1.WriteOptions{IfNoneMatch: "*"})
		require.NoError(t, err)
	})

	t.Run("any other If-None-Match is invalid", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).CreateService(context.Background(), write,
			v1.WriteOptions{IfNoneMatch: `"1"`})
		requireField(t, err, "If-None-Match")
		assert.Zero(t, control.createServiceCall)
	})
}

// serviceWithUnmodeledFields is the current object for the overlay tests. It
// carries SwarmKit fields the DTO does not model.
func serviceWithUnmodeledFields() *api.Service {
	return &api.Service{
		ID:   "svc-1",
		Meta: api.Meta{Version: api.Version{Index: 17}},
		Spec: api.ServiceSpec{
			Annotations: api.Annotations{
				Name:   "web",
				Labels: map[string]string{"team": "payments", "keep": "me"},
			},
			Mode: &api.ServiceSpec_Replicated{Replicated: &api.ReplicatedService{Replicas: 3}},
			Task: api.TaskSpec{
				Runtime: &api.TaskSpec_Container{Container: &api.ContainerSpec{
					Image:      "nginx:alpine",
					Env:        []string{"A=1", "B=2"},
					Args:       []string{"-g", "daemon off;"},
					StopSignal: "SIGTERM",
					Mounts: []api.Mount{{
						Type:     api.MountTypeVolume,
						Source:   "data",
						Target:   "/data",
						ReadOnly: false,
						// Unmodeled: a named volume's driver options.
						VolumeOptions: &api.Mount_VolumeOptions{NoCopy: true},
					}},
					Privileges: &api.Privileges{
						CredentialSpec: &api.Privileges_CredentialSpec{},
					},
					StopGracePeriod: gogotypes.DurationProto(10_000_000_000),
					PullOptions:     &api.ContainerSpec_PullOptions{RegistryAuth: "old-auth"},
				}},
				Resources: &api.ResourceRequirements{
					Limits: &api.Resources{
						NanoCPUs:    2_000_000_000,
						MemoryBytes: 512 << 20,
					},
				},
				Placement: &api.Placement{
					Constraints: []string{"node.labels.zone==a"},
					// Unmodeled: topology spread preferences.
					Preferences: []*api.PlacementPreference{{}},
					MaxReplicas: 4,
				},
			},
			Endpoint: &api.EndpointSpec{
				// Unmodeled: the endpoint resolver mode.
				Mode:  api.ResolutionModeDNSRoundRobin,
				Ports: []*api.PortConfig{{TargetPort: 80, PublishedPort: 8080}},
			},
			Update: &api.UpdateConfig{Parallelism: 1, Delay: 10_000_000_000, FailureAction: api.UpdateConfig_PAUSE},
		},
	}
}

func TestUpdateServiceOverlayPreservesUnmodeledFields(t *testing.T) {
	current := serviceWithUnmodeledFields()
	control := &fakeControl{
		service: current,
		updatedService: &api.Service{
			ID:   "svc-1",
			Meta: api.Meta{Version: api.Version{Index: 18}},
			Spec: *current.Spec.Copy(),
		},
	}

	patch := []byte(`{"labels":{"owner":"platform"},"spec":{"task":{"env":{"A":"10"}}}}`)
	_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
		v1.WriteOptions{IfMatch: `"17"`})
	require.NoError(t, err)

	require.NotNil(t, control.updateServiceReq)
	assert.EqualValues(t, 17, control.updateServiceReq.ServiceVersion.Index,
		"the update must carry the revision it read")

	spec := control.updateServiceReq.Spec
	container := spec.Task.GetContainer()
	require.NotNil(t, container)

	// Modeled changes landed.
	assert.Equal(t, "platform", spec.Annotations.Labels["owner"])
	assert.Equal(t, "me", spec.Annotations.Labels["keep"], "a label the patch omits survives")
	assert.Equal(t, []string{"A=10", "B=2"}, container.Env)

	// Unmodeled fields survived byte for byte.
	require.Len(t, container.Mounts, 1)
	require.NotNil(t, container.Mounts[0].VolumeOptions, "mount options must survive an unrelated patch")
	assert.True(t, container.Mounts[0].VolumeOptions.NoCopy)
	require.NotNil(t, container.Privileges)
	assert.EqualValues(t, 10_000, durationMs(container.StopGracePeriod))
	assert.Equal(t, "SIGTERM", container.StopSignal)
	assert.Equal(t, []string{"-g", "daemon off;"}, container.Args)

	require.NotNil(t, spec.Task.Placement)
	assert.Equal(t, []string{"node.labels.zone==a"}, spec.Task.Placement.Constraints)
	assert.Len(t, spec.Task.Placement.Preferences, 1, "placement preferences must survive")
	assert.EqualValues(t, 4, spec.Task.Placement.MaxReplicas)

	require.NotNil(t, spec.Endpoint)
	assert.Equal(t, api.ResolutionModeDNSRoundRobin, spec.Endpoint.Mode,
		"the endpoint resolver mode must survive a ports-untouched patch")
	assert.Len(t, spec.Endpoint.Ports, 1)

	require.NotNil(t, spec.Update)
	assert.EqualValues(t, 1, spec.Update.Parallelism)

	assert.EqualValues(t, 2*nanoCPUsPerVCPU, spec.Task.Resources.Limits.NanoCPUs)
	assert.EqualValues(t, 512<<20, spec.Task.Resources.Limits.MemoryBytes)

	document, err := json.Marshal(serviceFromSwarmkit(control.updatedService, taskCounts{}))
	require.NoError(t, err)
	assert.NotContains(t, string(document), "registryAuth")
}

func TestUpdateServiceMergePatchRules(t *testing.T) {
	t.Run("null clears registry auth", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"registryAuth":null}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Nil(t, control.updateServiceReq.Spec.Task.GetContainer().PullOptions)
	})

	t.Run("a string replaces registry auth", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"registryAuth":"new-auth"}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		require.NotNil(t, control.updateServiceReq.Spec.Task.GetContainer().PullOptions)
		assert.Equal(t, "new-auth", control.updateServiceReq.Spec.Task.GetContainer().PullOptions.RegistryAuth)
	})

	t.Run("null deletes a map key", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"env":{"A":null}}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Equal(t, []string{"B=2"}, control.updateServiceReq.Spec.Task.GetContainer().Env)
	})

	t.Run("env null clears every key", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"env":null}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Empty(t, control.updateServiceReq.Spec.Task.GetContainer().Env)
	})

	t.Run("empty array clears", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"args":[]}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Empty(t, control.updateServiceReq.Spec.Task.GetContainer().Args)
	})

	t.Run("null resets replicas to one", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields(), updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}}}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"replicas":null}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.EqualValues(t, 1, control.updateServiceReq.Spec.GetReplicated().Replicas)
	})

	t.Run("array replacement replaces the whole array", func(t *testing.T) {
		current := serviceWithUnmodeledFields()
		current.Spec.Networks = []*api.NetworkAttachmentConfig{{Target: "legacy-net"}}
		control := &fakeControl{
			service:        current,
			networks:       []*api.Network{networkByName("net-2", "frontend")},
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"networks":[{"id":"frontend"}]}}}`),
			v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		networks := control.updateServiceReq.Spec.Task.Networks
		require.Len(t, networks, 1)
		assert.Equal(t, "net-2", networks[0].Target)
		assert.Nil(t, control.updateServiceReq.Spec.Networks,
			"the deprecated ServiceSpec.Networks shadow must be cleared with the replacement")
	})
}

func TestUpdateServiceRejectsReplicasOnGlobal(t *testing.T) {
	current := serviceWithUnmodeledFields()
	current.Spec.Mode = &api.ServiceSpec_Global{Global: &api.GlobalService{}}
	control := &fakeControl{service: current}

	_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
		[]byte(`{"spec":{"replicas":2}}`), v1.WriteOptions{IfMatch: `"17"`})
	requireField(t, err, "spec.replicas")
	assert.Zero(t, control.updateServiceCall)
}

func TestUpdateServiceRejectsImmutableAndUnknownFields(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		field string
	}{
		{"id", `{"id":"svc-2"}`, "id"},
		{"name", `{"name":"other"}`, "name"},
		{"revision", `{"revision":"18"}`, "revision"},
		{"createdAt", `{"createdAt":"2026-01-01T00:00:00Z"}`, "createdAt"},
		{"status", `{"status":{"converged":true}}`, "status"},
		{"mode", `{"spec":{"mode":"global"}}`, "spec.mode"},
		{"unknown top level", `{"bogus":1}`, "bogus"},
		{"unknown spec field", `{"spec":{"bogus":1}}`, "spec.bogus"},
		{"unknown task field", `{"spec":{"task":{"bogus":1}}}`, "spec.task.bogus"},
		{"unknown port field", `{"spec":{"task":{"ports":[{"target":80,"bogus":1}]}}}`, "spec.task.ports[0].bogus"},
		{"unknown placement field", `{"spec":{"placement":{"bogus":[]}}}`, "spec.placement.bogus"},
		{"unknown update field", `{"spec":{"update":{"bogus":1}}}`, "spec.update.bogus"},
		{"task null", `{"spec":{"task":null}}`, "spec.task"},
		{"empty image", `{"spec":{"task":{"image":""}}}`, "spec.task.image"},
		{"fractional replicas", `{"spec":{"replicas":1.5}}`, "spec.replicas"},
		{"empty patch", ``, "body"},
		{"array body", `[]`, "body"},
		{"trailing data", `{"labels":{}} {}`, "body"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			control := &fakeControl{service: serviceWithUnmodeledFields()}
			_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
				[]byte(tc.patch), v1.WriteOptions{IfMatch: `"17"`})
			requireField(t, err, tc.field)
			assert.Zero(t, control.updateServiceCall, "an invalid patch must not reach SwarmKit")
		})
	}
}

func TestUpdateServicePreconditions(t *testing.T) {
	patch := []byte(`{"spec":{"replicas":2}}`)

	t.Run("missing If-Match is conflict/required", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields()}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch, v1.WriteOptions{})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, "If-Match", typed.Details["required"])
		assert.Equal(t, output.ReasonPreconditionRequired, typed.Details["reason"])
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("stale revision reports both revisions", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields()}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: `"16"`})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, "16", typed.Details["expectedRevision"])
		assert.Equal(t, "17", typed.Details["currentRevision"])
		assert.Equal(t, output.ReasonPreconditionFailed, typed.Details["reason"])
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("weak validator never matches", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields()}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: `W/"17"`})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, true, typed.Details["weak"])
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("bare revision matches", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: "17"})
		require.NoError(t, err)
	})

	t.Run("a strong member of a list satisfies If-Match", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: `W/"16", "17"`})
		require.NoError(t, err)
	})

	t.Run("star matches any revision", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: "*"})
		require.NoError(t, err)
	})

	t.Run("missing resource with If-Match is a failed precondition", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).UpdateService(context.Background(), "nope", patch,
			v1.WriteOptions{IfMatch: `"3"`})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, output.ReasonPreconditionFailed, typed.Details["reason"])
		assert.Zero(t, control.updateServiceCall)
	})
}

func TestUpdateServiceReturnsNewRevision(t *testing.T) {
	control := &fakeControl{
		service: serviceWithUnmodeledFields(),
		updatedService: &api.Service{
			ID:   "svc-1",
			Meta: api.Meta{Version: api.Version{Index: 18}},
			Spec: serviceWithUnmodeledFields().Spec,
		},
	}

	got, err := writeClient(control).UpdateService(context.Background(), "web",
		[]byte(`{"spec":{"replicas":2}}`), v1.WriteOptions{IfMatch: `"17"`})
	require.NoError(t, err)
	assert.Equal(t, "18", got.Revision)
	assert.Equal(t, "svc-1", got.ID)
}

func TestResolveServiceRef(t *testing.T) {
	services := []*api.Service{
		{ID: "svc-aaaa1111", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}},
		{ID: "svc-aaaa2222", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "api"}}},
		{ID: "svc-bbbb3333", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "worker"}}},
	}

	tests := []struct {
		name string
		ref  string
		want string
		code output.Code
	}{
		{name: "exact id", ref: "svc-bbbb3333", want: "svc-bbbb3333"},
		{name: "exact name", ref: "web", want: "svc-aaaa1111"},
		{name: "short exact name wins over prefix", ref: "api", want: "svc-aaaa2222"},
		{name: "unique four character prefix", ref: "svc-b", want: "svc-bbbb3333"},
		{name: "ambiguous prefix", ref: "svc-a", code: output.CodeAmbiguous},
		{name: "unknown", ref: "svc-cccc", code: output.CodeNotFound},
		{name: "short prefix", ref: "sv", code: output.CodeInvalid},
		{name: "empty", ref: "  ", code: output.CodeInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			control := &fakeControl{services: services}
			got, err := writeClient(control).resolveServiceRef(context.Background(), tc.ref)
			if tc.code != "" {
				if tc.code == output.CodeNotFound {
					require.Error(t, err)
					assert.Equal(t, codes.NotFound, status.Code(err))
					return
				}
				typed := requireCode(t, err, tc.code)
				if tc.code == output.CodeAmbiguous {
					candidates, ok := typed.Details["candidates"].([]map[string]string)
					require.True(t, ok, "details.candidates missing")
					require.Len(t, candidates, 2)
					assert.Equal(t, "svc-aaaa1111", candidates[0]["id"])
					assert.Equal(t, "web", candidates[0]["name"])
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGetServiceResolvesName(t *testing.T) {
	services := []*api.Service{
		{ID: "svc-aaaa1111", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}},
	}
	control := &fakeControl{services: services}

	got, err := writeClient(control).GetService(context.Background(), "web")
	require.NoError(t, err)
	assert.Equal(t, "svc-aaaa1111", got.ID)
}

func TestListServiceTasksResolvesRef(t *testing.T) {
	services := []*api.Service{
		{ID: "svc-aaaa1111", Spec: api.ServiceSpec{Annotations: api.Annotations{Name: "web"}}},
	}
	control := &fakeControl{services: services}

	_, err := writeClient(control).ListServiceTasks(context.Background(), "web")
	require.NoError(t, err)
	require.NotNil(t, control.lastTaskReq)
	require.NotNil(t, control.lastTaskReq.Filters)
	assert.Equal(t, []string{"svc-aaaa1111"}, control.lastTaskReq.Filters.ServiceIDs)
}

func TestGetServiceUnknownRefIsTyped(t *testing.T) {
	control := &fakeControl{}
	_, err := writeClient(control).GetService(context.Background(), "nope")
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestDeleteService(t *testing.T) {
	t.Run("returns the previous document", func(t *testing.T) {
		control := &fakeControl{services: []*api.Service{serviceWithUnmodeledFields()}}
		got, err := writeClient(control).DeleteService(context.Background(), "web", v1.WriteOptions{})
		require.NoError(t, err)
		assert.Equal(t, "svc-1", got.ID)
		assert.Equal(t, "web", got.Name)
		require.NotNil(t, control.removeServiceReq)
		assert.Equal(t, "svc-1", control.removeServiceReq.ServiceID)
	})

	t.Run("missing is not_found", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).DeleteService(context.Background(), "nope", v1.WriteOptions{})
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
		assert.Zero(t, control.removeServiceCall)
	})

	t.Run("ignoreMissing is a zero success", func(t *testing.T) {
		control := &fakeControl{}
		got, err := writeClient(control).DeleteService(context.Background(), "nope",
			v1.WriteOptions{IgnoreMissing: true})
		require.NoError(t, err)
		assert.Equal(t, v1.Service{}, got)
		assert.Zero(t, control.removeServiceCall)
	})

	t.Run("missing with If-Match is a failed precondition", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).DeleteService(context.Background(), "nope",
			v1.WriteOptions{IfMatch: `"4"`})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, output.ReasonPreconditionFailed, typed.Details["reason"])
		assert.Zero(t, control.removeServiceCall)
	})

	t.Run("stale If-Match fails before the RPC", func(t *testing.T) {
		control := &fakeControl{services: []*api.Service{serviceWithUnmodeledFields()}}
		_, err := writeClient(control).DeleteService(context.Background(), "web",
			v1.WriteOptions{IfMatch: `"16"`})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, "16", typed.Details["expectedRevision"])
		assert.Equal(t, "17", typed.Details["currentRevision"])
		assert.Zero(t, control.removeServiceCall)
	})

	t.Run("matching If-Match removes", func(t *testing.T) {
		control := &fakeControl{services: []*api.Service{serviceWithUnmodeledFields()}}
		_, err := writeClient(control).DeleteService(context.Background(), "web",
			v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Equal(t, 1, control.removeServiceCall)
	})

	t.Run("remove failure is returned", func(t *testing.T) {
		control := &fakeControl{
			services:         []*api.Service{serviceWithUnmodeledFields()},
			removeServiceErr: status.Error(codes.FailedPrecondition, "service is being updated"),
		}
		_, err := writeClient(control).DeleteService(context.Background(), "web", v1.WriteOptions{})
		require.Error(t, err)
		assert.Equal(t, codes.FailedPrecondition, status.Code(err))
		requireCode(t, output.FromGRPC(err), output.CodeConflict)
	})
}

func TestRollbackService(t *testing.T) {
	t.Run("rolls back with the expected version", func(t *testing.T) {
		control := &fakeControl{
			services:       []*api.Service{serviceWithUnmodeledFields()},
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}

		got, err := writeClient(control).RollbackService(context.Background(), "web",
			v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Equal(t, "18", got.Revision)

		require.NotNil(t, control.updateServiceReq)
		assert.Equal(t, api.UpdateServiceRequest_PREVIOUS, control.updateServiceReq.Rollback)
		assert.EqualValues(t, 17, control.updateServiceReq.ServiceVersion.Index)
		assert.Nil(t, control.updateServiceReq.Spec, "a rollback never sends a spec")
	})

	t.Run("If-Match is required", func(t *testing.T) {
		control := &fakeControl{services: []*api.Service{serviceWithUnmodeledFields()}}
		_, err := writeClient(control).RollbackService(context.Background(), "web", v1.WriteOptions{})
		typed := requireCode(t, err, output.CodeConflict)
		assert.Equal(t, "If-Match", typed.Details["required"])
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("stale If-Match", func(t *testing.T) {
		control := &fakeControl{services: []*api.Service{serviceWithUnmodeledFields()}}
		_, err := writeClient(control).RollbackService(context.Background(), "web",
			v1.WriteOptions{IfMatch: `"1"`})
		requireCode(t, err, output.CodeConflict)
		assert.Zero(t, control.updateServiceCall)
	})
}

func TestServiceWriteNamingIsResolved(t *testing.T) {
	// A write by name and by ID must resolve to the same service.
	services := []*api.Service{serviceWithUnmodeledFields()}
	for _, ref := range []string{"svc-1", "web", "svc-"} {
		control := &fakeControl{services: services}
		_, err := writeClient(control).DeleteService(context.Background(), ref, v1.WriteOptions{})
		require.NoError(t, err, ref)
		require.NotNil(t, control.removeServiceReq)
		assert.Equal(t, "svc-1", control.removeServiceReq.ServiceID, ref)
	}
}

func TestBindMountsRequireAdmin(t *testing.T) {
	writeWithBind := func() v1.ServiceWrite {
		return v1.ServiceWrite{
			Name: "web",
			Spec: v1.ServiceSpecWrite{Task: v1.TaskSpecWrite{TaskSpec: v1.TaskSpec{
				Image:  "nginx",
				Mounts: []v1.Mount{{Type: "bind", Source: "/srv/data", Target: "/data"}},
			}}},
		}
	}

	t.Run("create is forbidden without the admin role", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).CreateService(context.Background(), writeWithBind(), v1.WriteOptions{})
		typed := requireCode(t, err, output.CodeForbidden)
		assert.Equal(t, "admin", typed.Details["requiredRole"])
		assert.Equal(t, "bind_mount_requires_admin", typed.Details["reason"])
		assert.Zero(t, control.createServiceCall, "a forbidden bind mount never reaches SwarmKit")
	})

	t.Run("create is allowed for admin", func(t *testing.T) {
		control := &fakeControl{createdService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 1}}}}
		_, err := writeClient(control).CreateService(context.Background(), writeWithBind(),
			v1.WriteOptions{AllowBindMounts: true})
		require.NoError(t, err)
		require.Len(t, control.createServiceReq.Spec.Task.GetContainer().Mounts, 1)
		assert.Equal(t, api.MountTypeBind, control.createServiceReq.Spec.Task.GetContainer().Mounts[0].Type)
	})

	t.Run("patch is forbidden without the admin role", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields()}
		patch := []byte(`{"spec":{"task":{"mounts":[{"type":"bind","source":"/srv","target":"/data"}]}}}`)
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: `"17"`})
		typed := requireCode(t, err, output.CodeForbidden)
		assert.Equal(t, "admin", typed.Details["requiredRole"])
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("patch is allowed for admin", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		patch := []byte(`{"spec":{"task":{"mounts":[{"type":"bind","source":"/srv","target":"/data"}]}}}`)
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1", patch,
			v1.WriteOptions{IfMatch: `"17"`, AllowBindMounts: true})
		require.NoError(t, err)
		require.Len(t, control.updateServiceReq.Spec.Task.GetContainer().Mounts, 1)
	})

	t.Run("an untouched existing bind mount is not re-checked", func(t *testing.T) {
		current := serviceWithUnmodeledFields()
		current.Spec.Task.GetContainer().Mounts = []api.Mount{{
			Type: api.MountTypeBind, Source: "/srv/old", Target: "/old",
		}}
		control := &fakeControl{
			service:        current,
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}

		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"replicas":2}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		require.Len(t, control.updateServiceReq.Spec.Task.GetContainer().Mounts, 1)
		assert.Equal(t, api.MountTypeBind, control.updateServiceReq.Spec.Task.GetContainer().Mounts[0].Type)
	})
}

// TestUpdateServiceSeedsLimitsFromReservations is the regression test for a
// one-field resource patch neutralising an existing reservation: every consumer
// reads Limits and falls back to Reservations only when Limits is absent.
func TestUpdateServiceSeedsLimitsFromReservations(t *testing.T) {
	newCurrent := func() *api.Service {
		current := serviceWithUnmodeledFields()
		current.Spec.Task.Resources = &api.ResourceRequirements{
			Reservations: &api.Resources{
				NanoCPUs:    1_000_000_000,
				MemoryBytes: 256 << 20,
			},
		}
		return current
	}

	t.Run("vcpus-only patch keeps the memory reservation effective", func(t *testing.T) {
		control := &fakeControl{
			service:        newCurrent(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"vcpus":2}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)

		resources := control.updateServiceReq.Spec.Task.Resources
		require.NotNil(t, resources.Limits, "the patch must materialise a limit")
		assert.EqualValues(t, 2*nanoCPUsPerVCPU, resources.Limits.NanoCPUs)
		assert.EqualValues(t, 256<<20, resources.Limits.MemoryBytes,
			"the unpatched half must be seeded from Reservations, not zeroed")
		require.NotNil(t, resources.Reservations)
		assert.EqualValues(t, 1_000_000_000, resources.Reservations.NanoCPUs)
		assert.EqualValues(t, 256<<20, resources.Reservations.MemoryBytes)

		read := serviceFromSwarmkit(&api.Service{Spec: *control.updateServiceReq.Spec}, taskCounts{})
		assert.EqualValues(t, 2, read.Spec.Task.VCPUs)
		assert.EqualValues(t, 256<<20, read.Spec.Task.MemoryBytes,
			"the read document must not regress when only vcpus is patched")
	})

	t.Run("memory-only patch keeps the CPU reservation effective", func(t *testing.T) {
		control := &fakeControl{
			service:        newCurrent(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"memoryBytes":1073741824}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)

		resources := control.updateServiceReq.Spec.Task.Resources
		require.NotNil(t, resources.Limits)
		assert.EqualValues(t, 1_000_000_000, resources.Limits.NanoCPUs,
			"the unpatched half must be seeded from Reservations, not zeroed")
		assert.EqualValues(t, 1<<30, resources.Limits.MemoryBytes)

		read := serviceFromSwarmkit(&api.Service{Spec: *control.updateServiceReq.Spec}, taskCounts{})
		assert.EqualValues(t, 1, read.Spec.Task.VCPUs)
		assert.EqualValues(t, 1<<30, read.Spec.Task.MemoryBytes)
	})
}

func TestUpdateServiceEmptyPatchIsNoOp(t *testing.T) {
	control := &fakeControl{service: serviceWithUnmodeledFields()}

	got, err := writeClient(control).UpdateService(context.Background(), "svc-1",
		[]byte(`{}`), v1.WriteOptions{IfMatch: `"17"`})
	require.NoError(t, err)
	assert.Equal(t, "17", got.Revision, "an empty patch must not bump the revision")
	assert.Zero(t, control.updateServiceCall, "an empty patch must not reach SwarmKit")
}

func TestUpdateServicePatchEdgeCases(t *testing.T) {
	t.Run("registryAuth empty string clears", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"registryAuth":""}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Nil(t, control.updateServiceReq.Spec.Task.GetContainer().PullOptions)
	})

	t.Run("healthcheck test [] clears the healthcheck", func(t *testing.T) {
		current := serviceWithUnmodeledFields()
		current.Spec.Task.GetContainer().Healthcheck = &api.HealthConfig{
			Test: []string{"CMD", "true"},
		}
		control := &fakeControl{
			service:        current,
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}

		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"healthcheck":{"test":[]}}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)
		assert.Nil(t, control.updateServiceReq.Spec.Task.GetContainer().Healthcheck)
	})

	t.Run("env key with equals is rejected", func(t *testing.T) {
		control := &fakeControl{service: serviceWithUnmodeledFields()}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"env":{"A=B":"x"}}}}`), v1.WriteOptions{IfMatch: `"17"`})
		requireField(t, err, "spec.task.env")
		assert.Zero(t, control.updateServiceCall)
	})

	t.Run("ports replacement preserves the endpoint resolver mode", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"task":{"ports":[{"target":443}]}}}`), v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)

		endpoint := control.updateServiceReq.Spec.Endpoint
		require.NotNil(t, endpoint)
		assert.Equal(t, api.ResolutionModeDNSRoundRobin, endpoint.Mode)
		require.Len(t, endpoint.Ports, 1)
		assert.EqualValues(t, 443, endpoint.Ports[0].TargetPort)
	})

	t.Run("constraints replacement preserves placement preferences", func(t *testing.T) {
		control := &fakeControl{
			service:        serviceWithUnmodeledFields(),
			updatedService: &api.Service{ID: "svc-1", Meta: api.Meta{Version: api.Version{Index: 18}}},
		}
		_, err := writeClient(control).UpdateService(context.Background(), "svc-1",
			[]byte(`{"spec":{"placement":{"constraints":["node.labels.zone==b"]}}}`),
			v1.WriteOptions{IfMatch: `"17"`})
		require.NoError(t, err)

		placement := control.updateServiceReq.Spec.Task.Placement
		require.NotNil(t, placement)
		assert.Equal(t, []string{"node.labels.zone==b"}, placement.Constraints)
		assert.Len(t, placement.Preferences, 1, "preferences must survive a constraints patch")
		assert.EqualValues(t, 4, placement.MaxReplicas)
	})
}
