package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteCreateConfigRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusCreated, v1.NewConfigDocument(v1.Config{
			ID: "cfg-9", Name: "app", Revision: "1",
		}))
	})

	created, err := c.CreateConfig(context.Background(), v1.ConfigWrite{
		Name:   "app",
		Labels: map[string]string{"team": "payments"},
		Data:   "listen 8080",
	}, v1.WriteOptions{IfNoneMatch: "*"})
	require.NoError(t, err)
	assert.Equal(t, "cfg-9", created.ID)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/configs", got.path)
	assert.Equal(t, testAccept, got.contentType)
	assert.Equal(t, "*", got.ifNoneMatch)

	var decoded v1.ConfigWrite
	require.NoError(t, json.Unmarshal(got.body, &decoded))
	assert.Equal(t, "app", decoded.Name)
	assert.Equal(t, "listen 8080", decoded.Data)
}

func TestRemoteUpdateConfigRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusOK, v1.NewConfigDocument(v1.Config{
			ID: "cfg-1", Name: "app", Revision: "5",
		}))
	})

	patch := []byte(`{"labels":{"team":"search"}}`)
	updated, err := c.UpdateConfig(context.Background(), "app", patch, v1.WriteOptions{IfMatch: `"4"`})
	require.NoError(t, err)
	assert.Equal(t, "5", updated.Revision)

	assert.Equal(t, http.MethodPatch, got.method)
	assert.Equal(t, "/v1/configs/app", got.path, "the server resolves the reference")
	assert.Equal(t, v1.MergePatchMediaType, got.contentType)
	assert.Equal(t, `"4"`, got.ifMatch)
	assert.Equal(t, patch, got.body)
}

func TestRemoteUpdateConfigRequiresIfMatch(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a missing If-Match must not reach the server")
	})

	_, err := c.UpdateConfig(context.Background(), "app", []byte(`{}`), v1.WriteOptions{})
	typed := codeOf(t, err)
	assert.Equal(t, "conflict", string(typed.Code))
	assert.Equal(t, "If-Match", typed.Details["required"])
}

func TestRemoteDeleteConfigGetsThenDeletes(t *testing.T) {
	var calls []capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, captureRequest(t, r))
		switch r.Method {
		case http.MethodGet:
			writeTestDocument(w, http.StatusOK, v1.NewConfigDocument(v1.Config{
				ID: "cfg-1", Name: "app", Revision: "4",
			}))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	previous, err := c.DeleteConfig(context.Background(), "app", v1.WriteOptions{
		IfMatch: `"4"`, IgnoreMissing: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "cfg-1", previous.ID)

	require.Len(t, calls, 2)
	assert.Equal(t, http.MethodGet, calls[0].method)
	assert.Equal(t, "/v1/configs/app", calls[0].path)
	assert.Equal(t, http.MethodDelete, calls[1].method)
	assert.Equal(t, `"4"`, calls[1].ifMatch)
	assert.Equal(t, "true", calls[1].ignore)
}

func TestRemoteCreateSecretSendsBase64Body(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusCreated, v1.NewSecretDocument(v1.Secret{
			ID: "sec-1", Name: "token", Revision: "1",
		}))
	})

	payload := base64.StdEncoding.EncodeToString([]byte("hunter2"))
	created, err := c.CreateSecret(context.Background(), v1.SecretWrite{
		Name: "token",
		Data: payload,
	}, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "sec-1", created.ID)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/secrets", got.path)
	assert.Equal(t, testAccept, got.contentType)

	var decoded v1.SecretWrite
	require.NoError(t, json.Unmarshal(got.body, &decoded))
	assert.Equal(t, payload, decoded.Data)
}

func TestRemoteUpdateSecretRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusOK, v1.NewSecretDocument(v1.Secret{
			ID: "sec-1", Name: "token", Revision: "4",
		}))
	})

	_, err := c.UpdateSecret(context.Background(), "token", []byte(`{"labels":{"env":"prod"}}`),
		v1.WriteOptions{IfMatch: `"3"`})
	require.NoError(t, err)

	assert.Equal(t, http.MethodPatch, got.method)
	assert.Equal(t, "/v1/secrets/token", got.path)
	assert.Equal(t, v1.MergePatchMediaType, got.contentType)
	assert.Equal(t, `"3"`, got.ifMatch)
}

func TestRemoteDeleteSecretGetsThenDeletes(t *testing.T) {
	var calls []capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, captureRequest(t, r))
		switch r.Method {
		case http.MethodGet:
			writeTestDocument(w, http.StatusOK, v1.NewSecretDocument(v1.Secret{
				ID: "sec-1", Name: "token", Revision: "3",
			}))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	previous, err := c.DeleteSecret(context.Background(), "token", v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "sec-1", previous.ID)

	require.Len(t, calls, 2)
	assert.Equal(t, http.MethodDelete, calls[1].method)
	assert.Equal(t, "/v1/secrets/token", calls[1].path)
}

func TestRemoteCreateNetworkRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusCreated, v1.NewNetworkDocument(v1.Network{
			ID: "net-1", Name: "frontend", Revision: "1",
		}))
	})

	created, err := c.CreateNetwork(context.Background(), v1.NetworkWrite{
		Name:   "frontend",
		IPAM:   &v1.NetworkIPAM{Subnet: "10.0.1.0/24", Gateway: "10.0.1.1"},
		Labels: map[string]string{"tier": "edge"},
	}, v1.WriteOptions{})
	require.NoError(t, err)
	assert.Equal(t, "net-1", created.ID)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/networks", got.path)

	var decoded v1.NetworkWrite
	require.NoError(t, json.Unmarshal(got.body, &decoded))
	assert.Equal(t, "frontend", decoded.Name)
	require.NotNil(t, decoded.IPAM)
	assert.Equal(t, "10.0.1.0/24", decoded.IPAM.Subnet)
}

func TestRemoteDeleteNetworkGetsThenDeletes(t *testing.T) {
	var calls []capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, captureRequest(t, r))
		switch r.Method {
		case http.MethodGet:
			writeTestDocument(w, http.StatusOK, v1.NewNetworkDocument(v1.Network{
				ID: "net-1", Name: "frontend", Revision: "2",
			}))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	previous, err := c.DeleteNetwork(context.Background(), "net-1", v1.WriteOptions{IfMatch: `"2"`})
	require.NoError(t, err)
	assert.Equal(t, "net-1", previous.ID)

	require.Len(t, calls, 2)
	assert.Equal(t, http.MethodDelete, calls[1].method)
	assert.Equal(t, `"2"`, calls[1].ifMatch)
}

func TestRemoteUpdateNodeRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusOK, v1.NewNodeDocument(v1.Node{
			ID: "node-1", Hostname: "worker-a", Revision: "7",
			Role: "worker", Membership: "accepted", Availability: "drain", State: "ready",
		}))
	})

	patch := []byte(`{"availability":"drain"}`)
	updated, err := c.UpdateNode(context.Background(), "worker-a", patch, v1.WriteOptions{IfMatch: `"6"`})
	require.NoError(t, err)
	assert.Equal(t, "drain", updated.Availability)

	assert.Equal(t, http.MethodPatch, got.method)
	assert.Equal(t, "/v1/nodes/worker-a", got.path)
	assert.Equal(t, v1.MergePatchMediaType, got.contentType)
	assert.Equal(t, `"6"`, got.ifMatch)
	assert.Equal(t, patch, got.body)
}

func TestRemoteUpdateNodeRequiresIfMatch(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a missing If-Match must not reach the server")
	})

	_, err := c.UpdateNode(context.Background(), "worker-a", []byte(`{}`), v1.WriteOptions{})
	typed := codeOf(t, err)
	assert.Equal(t, "If-Match", typed.Details["required"])
}
