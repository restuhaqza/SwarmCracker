package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/moby/swarmkit/v2/api"
	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// configFixture builds one SwarmKit config with a payload.
func configFixture(id, name string, revision uint64, labels map[string]string, data string) *api.Config {
	return &api.Config{
		ID: id,
		Meta: api.Meta{
			Version: api.Version{Index: revision},
		},
		Spec: api.ConfigSpec{
			Annotations: api.Annotations{Name: name, Labels: labels},
			Data:        []byte(data),
		},
	}
}

// secretFixture builds one SwarmKit secret with a payload.
func secretFixture(id, name string, revision uint64, labels map[string]string, data string) *api.Secret {
	return &api.Secret{
		ID: id,
		Meta: api.Meta{
			Version: api.Version{Index: revision},
		},
		Spec: api.SecretSpec{
			Annotations: api.Annotations{Name: name, Labels: labels},
			Data:        []byte(data),
		},
	}
}

// networkFixture builds one SwarmKit network.
func networkFixture(id, name string, revision uint64, driver string) *api.Network {
	return &api.Network{
		ID: id,
		Meta: api.Meta{
			Version: api.Version{Index: revision},
		},
		Spec: api.NetworkSpec{
			Annotations:  api.Annotations{Name: name},
			DriverConfig: &api.Driver{Name: driver},
		},
	}
}

// nodeFixture builds one SwarmKit node with a hostname.
func nodeFixture(id, hostname string, revision uint64) *api.Node {
	return &api.Node{
		ID: id,
		Meta: api.Meta{
			Version: api.Version{Index: revision},
		},
		Spec: api.NodeSpec{
			Annotations:  api.Annotations{Name: "", Labels: map[string]string{"zone": "a"}},
			DesiredRole:  api.NodeRoleWorker,
			Membership:   api.NodeMembershipAccepted,
			Availability: api.NodeAvailabilityActive,
		},
		Description: &api.NodeDescription{Hostname: hostname},
	}
}

// requirePrecondition asserts a conflict carries the contract's reason.
func requirePrecondition(t *testing.T, err error, reason string) *output.Error {
	t.Helper()
	typed := requireCode(t, err, output.CodeConflict)
	assert.Equal(t, reason, typed.Details["reason"])
	return typed
}

func TestCreateConfigPersistsUTF8Payload(t *testing.T) {
	control := &fakeControl{createdConfig: configFixture("cfg-9", "app", 1, nil, "listen 8080 — ünïcode")}

	write := v1.ConfigWrite{
		Name:   "app",
		Labels: map[string]string{"team": "payments"},
		Data:   "listen 8080 — ünïcode",
	}

	got, err := writeClient(control).CreateConfig(context.Background(), write, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "cfg-9", got.ID)
	require.NotNil(t, got.Data)
	assert.Equal(t, write.Data, *got.Data, "the created document carries the payload it was given")

	require.NotNil(t, control.createConfigReq)
	spec := control.createConfigReq.Spec
	require.NotNil(t, spec)
	assert.Equal(t, "app", spec.Annotations.Name)
	assert.Equal(t, map[string]string{"team": "payments"}, spec.Annotations.Labels)
	assert.Equal(t, []byte(write.Data), spec.Data, "the payload is stored as UTF-8 bytes")
}

func TestCreateConfigRejectsBadInput(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).CreateConfig(context.Background(), v1.ConfigWrite{}, v1.WriteOptions{})
		requireField(t, err, "name")
		assert.Zero(t, control.createConfigCall)
	})

	t.Run("binary payload", func(t *testing.T) {
		control := &fakeControl{}
		write := v1.ConfigWrite{Name: "app", Data: string([]byte{0xff, 0xfe})}
		_, err := writeClient(control).CreateConfig(context.Background(), write, v1.WriteOptions{})

		typed := requireCode(t, err, output.CodeInvalid)
		assert.Equal(t, "binary_not_supported", typed.Details["reason"],
			"contract §5.6 names the refusal reason")
		fields, ok := typed.Details["fields"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, fields, "data")
		assert.Zero(t, control.createConfigCall)
	})

	t.Run("If-None-Match other than star", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).CreateConfig(context.Background(),
			v1.ConfigWrite{Name: "app"}, v1.WriteOptions{IfNoneMatch: `"1"`})
		requireField(t, err, "If-None-Match")
	})
}

func TestUpdateConfigMergesLabels(t *testing.T) {
	current := configFixture("cfg-1", "app", 4, map[string]string{"team": "payments", "env": "prod"}, "listen 8080")
	updated := configFixture("cfg-1", "app", 5, map[string]string{"team": "search"}, "listen 8080")
	control := &fakeControl{config: current, updatedConfig: updated}

	patch := []byte(`{"labels":{"team":"search","env":null}}`)
	got, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", patch,
		v1.WriteOptions{IfMatch: `"4"`})
	require.NoError(t, err)
	assert.Equal(t, "5", got.Revision)

	require.NotNil(t, control.updateConfigReq)
	req := control.updateConfigReq
	assert.Equal(t, "cfg-1", req.ConfigID)
	require.NotNil(t, req.ConfigVersion)
	assert.EqualValues(t, 4, req.ConfigVersion.Index)
	assert.Equal(t, map[string]string{"team": "search"}, req.Spec.Annotations.Labels)
	assert.Equal(t, []byte("listen 8080"), req.Spec.Data, "a labels patch never rewrites the payload")
}

func TestUpdateConfigResolvesNameAndPrefix(t *testing.T) {
	byName := configFixture("cfg-1111", "app", 2, nil, "")
	byPrefix := configFixture("cfg-2222", "other", 3, nil, "")
	control := &fakeControl{configs: []*api.Config{byName, byPrefix}, updatedConfig: byName}

	_, err := writeClient(control).UpdateConfig(context.Background(), "app", []byte(`{"labels":{"a":"b"}}`),
		v1.WriteOptions{IfMatch: `"2"`})
	require.NoError(t, err)
	assert.Equal(t, "cfg-1111", control.updateConfigReq.ConfigID)

	_, err = writeClient(control).UpdateConfig(context.Background(), "cfg-2", []byte(`{"labels":{"a":"b"}}`),
		v1.WriteOptions{IfMatch: `"3"`})
	require.NoError(t, err)
	assert.Equal(t, "cfg-2222", control.updateConfigReq.ConfigID)
}

func TestUpdateConfigRejectsImmutableFields(t *testing.T) {
	current := configFixture("cfg-1", "app", 4, nil, "payload")

	for _, field := range []string{
		"data", "name", "id", "revision", "createdAt", "updatedAt", "schemaVersion", "kind",
	} {
		t.Run(field, func(t *testing.T) {
			control := &fakeControl{config: current}
			patch := []byte(`{"` + field + `":"other"}`)
			_, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", patch,
				v1.WriteOptions{IfMatch: `"4"`})
			requireField(t, err, field)
			assert.Zero(t, control.updateConfigCall)
		})
	}

	t.Run("unknown field", func(t *testing.T) {
		control := &fakeControl{config: current}
		_, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1",
			[]byte(`{"spec":{"x":1}}`), v1.WriteOptions{IfMatch: `"4"`})
		requireField(t, err, "spec")
	})
}

func TestUpdateConfigPreconditions(t *testing.T) {
	current := configFixture("cfg-1", "app", 4, nil, "")

	t.Run("missing If-Match", func(t *testing.T) {
		control := &fakeControl{config: current}
		_, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", []byte(`{}`), v1.WriteOptions{})
		typed := requirePrecondition(t, err, output.ReasonPreconditionRequired)
		assert.Equal(t, "If-Match", typed.Details["required"])
	})

	t.Run("stale revision", func(t *testing.T) {
		control := &fakeControl{config: current}
		_, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", []byte(`{}`),
			v1.WriteOptions{IfMatch: `"3"`})
		typed := requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Equal(t, "4", typed.Details["currentRevision"])
	})

	t.Run("weak validator", func(t *testing.T) {
		control := &fakeControl{config: current}
		_, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", []byte(`{}`),
			v1.WriteOptions{IfMatch: `W/"4"`})
		typed := requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Equal(t, true, typed.Details["weak"])
	})

	t.Run("missing config with If-Match is 412", func(t *testing.T) {
		_, err := writeClient(&fakeControl{}).UpdateConfig(context.Background(), "gone", []byte(`{}`),
			v1.WriteOptions{IfMatch: `"4"`})
		requirePrecondition(t, err, output.ReasonPreconditionFailed)
	})
}

func TestUpdateConfigEmptyPatchIsNoOp(t *testing.T) {
	current := configFixture("cfg-1", "app", 4, map[string]string{"team": "payments"}, "")
	control := &fakeControl{config: current}

	got, err := writeClient(control).UpdateConfig(context.Background(), "cfg-1", []byte(`{}`),
		v1.WriteOptions{IfMatch: `"4"`})
	require.NoError(t, err)
	assert.Equal(t, "4", got.Revision)
	assert.Zero(t, control.updateConfigCall, "an empty patch must not bump the revision")
}

func TestDeleteConfig(t *testing.T) {
	current := configFixture("cfg-1", "app", 4, nil, "")
	control := &fakeControl{config: current}

	got, err := writeClient(control).DeleteConfig(context.Background(), "app",
		v1.WriteOptions{IfMatch: `"4"`})
	require.NoError(t, err)
	assert.Equal(t, "cfg-1", got.ID, "delete returns the document as it was before removal")
	require.NotNil(t, control.removeConfigReq)
	assert.Equal(t, "cfg-1", control.removeConfigReq.ConfigID)

	t.Run("ignoreMissing", func(t *testing.T) {
		control := &fakeControl{}
		got, err := writeClient(control).DeleteConfig(context.Background(), "gone",
			v1.WriteOptions{IgnoreMissing: true})
		require.NoError(t, err)
		assert.Equal(t, v1.Config{}, got)
		assert.Zero(t, control.removeConfigCall)
	})

	t.Run("missing with If-Match is 412 before ignoreMissing", func(t *testing.T) {
		control := &fakeControl{}
		_, err := writeClient(control).DeleteConfig(context.Background(), "gone",
			v1.WriteOptions{IfMatch: `"4"`, IgnoreMissing: true})
		requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Zero(t, control.removeConfigCall)
	})

	t.Run("stale If-Match", func(t *testing.T) {
		control := &fakeControl{config: current}
		_, err := writeClient(control).DeleteConfig(context.Background(), "cfg-1",
			v1.WriteOptions{IfMatch: `"3"`})
		requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Zero(t, control.removeConfigCall)
	})
}

func TestCreateSecretFromBase64(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("hunter2"))
	created := secretFixture("sec-9", "token", 1, nil, "hunter2")
	control := &fakeControl{createdSecret: created}

	got, err := writeClient(control).CreateSecret(context.Background(), v1.SecretWrite{
		Name:   "token",
		Labels: map[string]string{"env": "prod"},
		Data:   payload,
	}, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "sec-9", got.ID)

	require.NotNil(t, control.createSecretReq)
	spec := control.createSecretReq.Spec
	assert.Equal(t, []byte("hunter2"), spec.Data)
	assert.Nil(t, spec.Driver)
	assert.Equal(t, map[string]string{"env": "prod"}, spec.Annotations.Labels)

	document, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(document), "hunter2")
	assert.NotContains(t, string(document), `"data"`, "no read document has a data field")
}

func TestCreateSecretFromDriver(t *testing.T) {
	control := &fakeControl{createdSecret: &api.Secret{
		ID:   "sec-2",
		Meta: api.Meta{Version: api.Version{Index: 2}},
		Spec: api.SecretSpec{
			Annotations: api.Annotations{Name: "external"},
			Driver:      &api.Driver{Name: "vault", Options: map[string]string{"path": "secret/app"}},
		},
	}}

	got, err := writeClient(control).CreateSecret(context.Background(), v1.SecretWrite{
		Name:   "external",
		Driver: &v1.SecretDriver{Name: "vault", Options: map[string]string{"path": "secret/app"}},
	}, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "vault", got.Driver)

	spec := control.createSecretReq.Spec
	assert.Nil(t, spec.Data)
	require.NotNil(t, spec.Driver)
	assert.Equal(t, "vault", spec.Driver.Name)
	assert.Equal(t, map[string]string{"path": "secret/app"}, spec.Driver.Options)
}

func TestCreateSecretRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		write v1.SecretWrite
		field string
	}{
		{"empty name", v1.SecretWrite{Data: "aGk="}, "name"},
		{"both data and driver", v1.SecretWrite{
			Name: "x", Data: "aGk=", Driver: &v1.SecretDriver{Name: "vault"},
		}, "driver"},
		{"neither", v1.SecretWrite{Name: "x"}, "data"},
		{"bad base64", v1.SecretWrite{Name: "x", Data: "not base64!!"}, "data"},
		{"driver without a name", v1.SecretWrite{Name: "x", Driver: &v1.SecretDriver{}}, "driver.name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := &fakeControl{}
			_, err := writeClient(control).CreateSecret(context.Background(), tc.write, v1.WriteOptions{})
			requireField(t, err, tc.field)
			assert.Zero(t, control.createSecretCall)
		})
	}
}

func TestUpdateSecretMergesLabelsOnly(t *testing.T) {
	current := secretFixture("sec-1", "token", 3,
		map[string]string{"env": "prod", "team": "payments"}, "hunter2")
	updated := secretFixture("sec-1", "token", 4, map[string]string{"env": "staging"}, "hunter2")
	control := &fakeControl{secret: current, updatedSecret: updated}

	got, err := writeClient(control).UpdateSecret(context.Background(), "token",
		[]byte(`{"labels":{"env":"staging","team":null}}`), v1.WriteOptions{IfMatch: `"3"`})
	require.NoError(t, err)
	assert.Equal(t, "4", got.Revision)

	req := control.updateSecretReq
	require.NotNil(t, req)
	assert.Equal(t, "sec-1", req.SecretID)
	assert.EqualValues(t, 3, req.SecretVersion.Index)
	assert.Equal(t, map[string]string{"env": "staging"}, req.Spec.Annotations.Labels)
	assert.Equal(t, []byte("hunter2"), req.Spec.Data,
		"SwarmKit requires the unchanged payload to be echoed back")
}

func TestUpdateSecretRejectsImmutableFields(t *testing.T) {
	current := secretFixture("sec-1", "token", 3, nil, "hunter2")

	for _, field := range []string{
		"data", "driver", "name", "id", "revision", "createdAt", "updatedAt",
		"schemaVersion", "kind",
	} {
		t.Run(field, func(t *testing.T) {
			control := &fakeControl{secret: current}
			_, err := writeClient(control).UpdateSecret(context.Background(), "sec-1",
				[]byte(`{"`+field+`":"x"}`), v1.WriteOptions{IfMatch: `"3"`})
			requireField(t, err, field)
			assert.Zero(t, control.updateSecretCall)
		})
	}
}

func TestDeleteSecret(t *testing.T) {
	current := secretFixture("sec-1", "token", 3, nil, "hunter2")
	control := &fakeControl{secret: current}

	got, err := writeClient(control).DeleteSecret(context.Background(), "token",
		v1.WriteOptions{IfMatch: `"3"`})
	require.NoError(t, err)
	assert.Equal(t, "sec-1", got.ID)
	require.NotNil(t, control.removeSecretReq)
	assert.Equal(t, "sec-1", control.removeSecretReq.SecretID)

	_, err = writeClient(&fakeControl{}).DeleteSecret(context.Background(), "gone",
		v1.WriteOptions{IgnoreMissing: true})
	require.NoError(t, err)
}

func TestCreateNetworkDefaultsToOverlay(t *testing.T) {
	control := &fakeControl{createdNetwork: networkFixture("net-9", "frontend", 1, "overlay")}

	got, err := writeClient(control).CreateNetwork(context.Background(), v1.NetworkWrite{
		Name:       "frontend",
		Labels:     map[string]string{"tier": "edge"},
		Attachable: true,
	}, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "net-9", got.ID)

	spec := control.createNetworkReq.Spec
	require.NotNil(t, spec.DriverConfig)
	assert.Equal(t, "overlay", spec.DriverConfig.Name, "the driver defaults to overlay")
	assert.True(t, spec.Attachable)
	assert.Equal(t, map[string]string{"tier": "edge"}, spec.Annotations.Labels)
	assert.Nil(t, spec.IPAM, "no ipam block when the request names none")
}

func TestCreateNetworkWithIPAM(t *testing.T) {
	control := &fakeControl{createdNetwork: networkFixture("net-2", "app", 1, "bridge")}

	_, err := writeClient(control).CreateNetwork(context.Background(), v1.NetworkWrite{
		Name:   "app",
		Driver: "bridge",
		IPAM:   &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
	}, v1.WriteOptions{})
	require.NoError(t, err)

	spec := control.createNetworkReq.Spec
	assert.Equal(t, "bridge", spec.DriverConfig.Name)
	require.NotNil(t, spec.IPAM)
	require.Len(t, spec.IPAM.Configs, 1)
	assert.Equal(t, "10.0.1.0/24", spec.IPAM.Configs[0].Subnet)
	assert.Equal(t, "10.0.1.1", spec.IPAM.Configs[0].Gateway)
}

func TestCreateNetworkRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		write v1.NetworkWrite
		field string
	}{
		{"empty name", v1.NetworkWrite{}, "name"},
		{"bad subnet", v1.NetworkWrite{Name: "x", IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0"}}, "ipam.subnet"},
		{"subnet with host bits", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.7/24"}}, "ipam.subnet"},
		{"gateway outside subnet", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.2.1"}}, "ipam.gateway"},
		{"gateway is the network address", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.0"}}, "ipam.gateway"},
		{"gateway is the broadcast address", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.255"}}, "ipam.gateway"},
		{"gateway without subnet", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Gateway: "10.0.2.1"}}, "ipam.gateway"},
		{"bad gateway", v1.NetworkWrite{Name: "x",
			IPAM: &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "nope"}}, "ipam.gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := &fakeControl{}
			_, err := writeClient(control).CreateNetwork(context.Background(), tc.write, v1.WriteOptions{})
			requireField(t, err, tc.field)
			assert.Zero(t, control.createNetworkCall)
		})
	}
}

func TestCreateNetworkNameConflictIsReturned(t *testing.T) {
	control := &fakeControl{createNetworkErr: status.Error(codes.AlreadyExists, "network exists")}

	_, err := writeClient(control).CreateNetwork(context.Background(),
		v1.NetworkWrite{Name: "frontend"}, v1.WriteOptions{})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestDeleteNetwork(t *testing.T) {
	current := networkFixture("net-1", "frontend", 2, "overlay")
	control := &fakeControl{network: current}

	got, err := writeClient(control).DeleteNetwork(context.Background(), "frontend", v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "net-1", got.ID)
	require.NotNil(t, control.removeNetworkReq)
	assert.Equal(t, "net-1", control.removeNetworkReq.NetworkID)

	t.Run("ignoreMissing", func(t *testing.T) {
		control := &fakeControl{}
		got, err := writeClient(control).DeleteNetwork(context.Background(), "gone",
			v1.WriteOptions{IgnoreMissing: true})
		require.NoError(t, err)
		assert.Equal(t, v1.Network{}, got)
	})

	t.Run("stale If-Match", func(t *testing.T) {
		control := &fakeControl{network: current}
		_, err := writeClient(control).DeleteNetwork(context.Background(), "net-1",
			v1.WriteOptions{IfMatch: `"1"`})
		requirePrecondition(t, err, output.ReasonPreconditionFailed)
		assert.Zero(t, control.removeNetworkCall)
	})
}

func TestUpdateNodeAvailabilityAndLabels(t *testing.T) {
	current := nodeFixture("node-1", "worker-a", 6)
	updated := nodeFixture("node-1", "worker-a", 7)
	updated.Spec.Availability = api.NodeAvailabilityDrain
	control := &fakeControl{node: current, updatedNode: updated}

	got, err := writeClient(control).UpdateNode(context.Background(), "worker-a",
		[]byte(`{"availability":"drain","labels":{"zone":"b","tier":null}}`),
		v1.WriteOptions{IfMatch: `"6"`})
	require.NoError(t, err)
	assert.Equal(t, "drain", got.Availability)

	req := control.updateNodeReq
	require.NotNil(t, req)
	assert.Equal(t, "node-1", req.NodeID)
	assert.EqualValues(t, 6, req.NodeVersion.Index)
	assert.Equal(t, api.NodeAvailabilityDrain, req.Spec.Availability)
	assert.Equal(t, map[string]string{"zone": "b"}, req.Spec.Annotations.Labels)
	assert.Equal(t, api.NodeRoleWorker, req.Spec.DesiredRole, "role is preserved")
	assert.Equal(t, api.NodeMembershipAccepted, req.Spec.Membership, "membership is preserved")
}

func TestUpdateNodeRejectsImmutableAndUnknownFields(t *testing.T) {
	current := nodeFixture("node-1", "worker-a", 6)

	for _, field := range []string{
		"role", "membership", "id", "hostname", "revision", "createdAt", "updatedAt",
		"state", "address", "resources", "manager", "agentVersion", "schemaVersion", "kind",
	} {
		t.Run(field, func(t *testing.T) {
			control := &fakeControl{node: current}
			_, err := writeClient(control).UpdateNode(context.Background(), "node-1",
				[]byte(`{"`+field+`":"x"}`), v1.WriteOptions{IfMatch: `"6"`})
			requireField(t, err, field)
			assert.Zero(t, control.updateNodeCall)
		})
	}

	t.Run("unknown field", func(t *testing.T) {
		control := &fakeControl{node: current}
		_, err := writeClient(control).UpdateNode(context.Background(), "node-1",
			[]byte(`{"drain":true}`), v1.WriteOptions{IfMatch: `"6"`})
		requireField(t, err, "drain")
	})

	t.Run("bad availability", func(t *testing.T) {
		control := &fakeControl{node: current}
		_, err := writeClient(control).UpdateNode(context.Background(), "node-1",
			[]byte(`{"availability":"sleep"}`), v1.WriteOptions{IfMatch: `"6"`})
		requireField(t, err, "availability")
	})
}

func TestUpdateNodePreconditionsAndRefs(t *testing.T) {
	current := nodeFixture("node-1111", "worker-a", 6)
	other := nodeFixture("node-2222", "worker-b", 2)
	control := &fakeControl{nodes: []*api.Node{current, other}, updatedNode: current}

	t.Run("missing If-Match", func(t *testing.T) {
		_, err := writeClient(control).UpdateNode(context.Background(), "node-1111",
			[]byte(`{}`), v1.WriteOptions{})
		requirePrecondition(t, err, output.ReasonPreconditionRequired)
	})

	t.Run("hostname resolves", func(t *testing.T) {
		_, err := writeClient(control).UpdateNode(context.Background(), "worker-b",
			[]byte(`{"labels":{"a":"b"}}`), v1.WriteOptions{IfMatch: `"2"`})
		require.NoError(t, err)
		assert.Equal(t, "node-2222", control.updateNodeReq.NodeID)
	})

	t.Run("prefix resolves", func(t *testing.T) {
		_, err := writeClient(control).UpdateNode(context.Background(), "node-1",
			[]byte(`{"labels":{"a":"b"}}`), v1.WriteOptions{IfMatch: `"6"`})
		require.NoError(t, err)
		assert.Equal(t, "node-1111", control.updateNodeReq.NodeID)
	})

	t.Run("empty patch is a no-op", func(t *testing.T) {
		control := &fakeControl{node: current}
		got, err := writeClient(control).UpdateNode(context.Background(), "node-1111",
			[]byte(`{}`), v1.WriteOptions{IfMatch: `"6"`})
		require.NoError(t, err)
		assert.Equal(t, "6", got.Revision)
		assert.Zero(t, control.updateNodeCall)
	})
}

func TestDuplicateHostnameIsAmbiguous(t *testing.T) {
	first := nodeFixture("node-1111", "worker-a", 6)
	second := nodeFixture("node-2222", "worker-a", 2)
	control := &fakeControl{nodes: []*api.Node{first, second}, updatedNode: first}
	client := writeClient(control)

	// A node hostname is agent-reported and not unique, so a repeated name must
	// never resolve by list order: that would read (or mutate) an arbitrary node.
	_, err := client.GetNode(context.Background(), "worker-a")
	typed := requireCode(t, err, output.CodeAmbiguous)
	candidates, ok := typed.Details["candidates"].([]map[string]string)
	require.True(t, ok, "%#v", typed.Details["candidates"])
	require.Len(t, candidates, 2)
	assert.Equal(t, "node-1111", candidates[0]["id"])
	assert.Equal(t, "worker-a", candidates[0]["name"])
	assert.Equal(t, "node-2222", candidates[1]["id"])

	_, err = client.UpdateNode(context.Background(), "worker-a",
		[]byte(`{"availability":"drain"}`), v1.WriteOptions{IfMatch: `"6"`})
	requireCode(t, err, output.CodeAmbiguous)
	assert.Zero(t, control.updateNodeCall, "an ambiguous reference must never reach UpdateNode")
}

func TestDuplicateResourceNameIsAmbiguous(t *testing.T) {
	// SwarmKit enforces unique config names, but the resolver is shared and must
	// not depend on that: two objects with one name is ambiguity, not a pick.
	configs := []*api.Config{
		configFixture("cfg-1111", "app", 1, nil, ""),
		configFixture("cfg-2222", "app", 2, nil, ""),
	}
	_, err := (&Client{control: &fakeControl{configs: configs}}).GetConfig(context.Background(), "app")
	typed := requireCode(t, err, output.CodeAmbiguous)
	candidates, ok := typed.Details["candidates"].([]map[string]string)
	require.True(t, ok)
	require.Len(t, candidates, 2)
}

func TestConfigMappingWithholdsNonUTF8Payload(t *testing.T) {
	control := &fakeControl{config: &api.Config{
		ID:   "cfg-bin",
		Meta: api.Meta{Version: api.Version{Index: 1}},
		Spec: api.ConfigSpec{
			Annotations: api.Annotations{Name: "binary"},
			Data:        []byte{0x1f, 0x8b, 0x08, 0x00, 0x61, 0xff, 0xfe},
		},
	}}

	got, err := (&Client{control: control}).GetConfig(context.Background(), "cfg-bin")
	require.NoError(t, err)
	assert.Equal(t, "cfg-bin", got.ID)
	assert.Equal(t, "binary", got.Name)
	assert.Nil(t, got.Data,
		"a non-UTF-8 payload is withheld, never transcoded to U+FFFD")
}

func TestCreateSecretAcceptsUnpaddedBase64(t *testing.T) {
	control := &fakeControl{createdSecret: secretFixture("sec-3", "token", 1, nil, "hi")}

	_, err := writeClient(control).CreateSecret(context.Background(), v1.SecretWrite{
		Name: "token",
		Data: "aGk", // "hi" without RFC 4648 padding
	}, v1.WriteOptions{})
	require.NoError(t, err)
	require.NotNil(t, control.createSecretReq)
	assert.Equal(t, []byte("hi"), control.createSecretReq.Spec.Data)
}

func TestCreateNetworkIPv6IPAM(t *testing.T) {
	control := &fakeControl{createdNetwork: networkFixture("net-6", "v6", 1, "overlay")}

	_, err := writeClient(control).CreateNetwork(context.Background(), v1.NetworkWrite{
		Name: "v6",
		IPAM: &v1.NetworkIPAM{Subnet: "fd00:1::/64", Gateway: "fd00:1::1"},
	}, v1.WriteOptions{})
	require.NoError(t, err, "IPv6 has no broadcast address, so the last address is a valid gateway")

	spec := control.createNetworkReq.Spec
	require.NotNil(t, spec.IPAM)
	assert.Equal(t, "fd00:1::/64", spec.IPAM.Configs[0].Subnet)
	assert.Equal(t, "fd00:1::1", spec.IPAM.Configs[0].Gateway)
}

func TestNamedGettersResolveNamesAndPrefixes(t *testing.T) {
	config := configFixture("cfg-aaaa", "app", 1, nil, "payload")
	secret := secretFixture("sec-aaaa", "token", 1, nil, "hunter2")
	network := networkFixture("net-aaaa", "frontend", 1, "overlay")
	node := nodeFixture("node-aaaa", "worker-a", 1)

	client := &Client{control: &fakeControl{
		configs:  []*api.Config{config},
		secrets:  []*api.Secret{secret},
		networks: []*api.Network{network},
		nodes:    []*api.Node{node},
	}}

	t.Run("config by name and prefix", func(t *testing.T) {
		got, err := client.GetConfig(context.Background(), "app")
		require.NoError(t, err)
		assert.Equal(t, "cfg-aaaa", got.ID)
		require.NotNil(t, got.Data)
		assert.Equal(t, "payload", *got.Data)

		got, err = client.GetConfig(context.Background(), "cfg-a")
		require.NoError(t, err)
		assert.Equal(t, "cfg-aaaa", got.ID)
	})

	t.Run("secret by name and prefix", func(t *testing.T) {
		got, err := client.GetSecret(context.Background(), "token")
		require.NoError(t, err)
		assert.Equal(t, "sec-aaaa", got.ID)

		got, err = client.GetSecret(context.Background(), "sec-a")
		require.NoError(t, err)
		assert.Equal(t, "sec-aaaa", got.ID)
	})

	t.Run("network by name and prefix", func(t *testing.T) {
		got, err := client.GetNetwork(context.Background(), "frontend")
		require.NoError(t, err)
		assert.Equal(t, "net-aaaa", got.ID)

		got, err = client.GetNetwork(context.Background(), "net-a")
		require.NoError(t, err)
		assert.Equal(t, "net-aaaa", got.ID)
	})

	t.Run("node by hostname and prefix", func(t *testing.T) {
		got, err := client.GetNode(context.Background(), "worker-a")
		require.NoError(t, err)
		assert.Equal(t, "node-aaaa", got.ID)

		got, err = client.GetNode(context.Background(), "node-a")
		require.NoError(t, err)
		assert.Equal(t, "node-aaaa", got.ID)
	})
}

func TestNamedGettersReportAmbiguousShortAndMissing(t *testing.T) {
	configs := []*api.Config{
		configFixture("cfg-aaaa", "a", 1, nil, ""),
		configFixture("cfg-abbb", "b", 1, nil, ""),
	}
	client := &Client{control: &fakeControl{configs: configs}}

	t.Run("ambiguous", func(t *testing.T) {
		_, err := client.GetConfig(context.Background(), "cfg-a")
		typed := requireCode(t, err, output.CodeAmbiguous)
		candidates, ok := typed.Details["candidates"].([]map[string]string)
		require.True(t, ok, "%#v", typed.Details["candidates"])
		require.Len(t, candidates, 2)
		assert.Equal(t, "cfg-aaaa", candidates[0]["id"])
		assert.Equal(t, "a", candidates[0]["name"])
	})

	t.Run("short non-exact", func(t *testing.T) {
		_, err := client.GetConfig(context.Background(), "cf")
		requireField(t, err, "id")
	})

	t.Run("missing", func(t *testing.T) {
		_, err := client.GetConfig(context.Background(), "nope")
		require.Error(t, err)
		assert.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("empty", func(t *testing.T) {
		_, err := client.GetConfig(context.Background(), "  ")
		requireField(t, err, "id")
	})
}
