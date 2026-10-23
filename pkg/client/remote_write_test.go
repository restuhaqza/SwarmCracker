package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedRequest is what a remote write test asserts: the wire, not the
// local mapping.
type capturedRequest struct {
	method      string
	path        string
	contentType string
	ifMatch     string
	ifNoneMatch string
	dryRun      string
	ignore      string
	body        []byte
}

func captureRequest(t *testing.T, r *http.Request) capturedRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	return capturedRequest{
		method:      r.Method,
		path:        r.URL.Path,
		contentType: r.Header.Get("Content-Type"),
		ifMatch:     r.Header.Get("If-Match"),
		ifNoneMatch: r.Header.Get("If-None-Match"),
		dryRun:      r.URL.Query().Get("dryRun"),
		ignore:      r.URL.Query().Get("ignoreMissing"),
		body:        body,
	}
}

func TestRemoteCreateServiceRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{Token: "s3cret"}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusCreated, v1.NewServiceDocument(v1.Service{
			ID: "svc-9", Name: "web", Revision: "1",
		}))
	})

	write := v1.ServiceWrite{
		Name: "web",
		Spec: v1.ServiceSpecWrite{
			Mode: "replicated",
			Task: v1.TaskSpecWrite{
				TaskSpec:     v1.TaskSpec{Image: "nginx:alpine"},
				RegistryAuth: "auth",
			},
		},
	}

	svc, err := c.CreateService(context.Background(), write, v1.WriteOptions{IfNoneMatch: "*"})
	require.NoError(t, err)
	assert.Equal(t, "svc-9", svc.ID)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/services", got.path)
	assert.Equal(t, testAccept, got.contentType)
	assert.Equal(t, "*", got.ifNoneMatch)

	var decoded v1.ServiceWrite
	require.NoError(t, json.Unmarshal(got.body, &decoded))
	assert.Equal(t, "web", decoded.Name)
	assert.Equal(t, "auth", decoded.Spec.Task.RegistryAuth)
}

func TestRemoteUpdateServiceRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{
			ID: "svc-1", Name: "web", Revision: "18",
		}))
	})

	patch := []byte(`{"spec":{"replicas":2}}`)
	svc, err := c.UpdateService(context.Background(), "web", patch, v1.WriteOptions{IfMatch: `"17"`})
	require.NoError(t, err)
	assert.Equal(t, "18", svc.Revision)

	assert.Equal(t, http.MethodPatch, got.method)
	assert.Equal(t, "/v1/services/web", got.path)
	assert.Equal(t, v1.MergePatchMediaType, got.contentType)
	assert.Equal(t, `"17"`, got.ifMatch)
	assert.Equal(t, patch, got.body, "the raw merge patch is forwarded unchanged")
}

func TestRemoteUpdateServiceRequiresIfMatch(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a missing If-Match must not reach the server")
	})

	_, err := c.UpdateService(context.Background(), "web", []byte(`{}`), v1.WriteOptions{})
	typed := codeOf(t, err)
	assert.Equal(t, `conflict`, string(typed.Code))
	assert.Equal(t, "If-Match", typed.Details["required"])
}

func TestRemoteDeleteServiceGetsThenDeletes(t *testing.T) {
	var calls []capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, captureRequest(t, r))
		switch r.Method {
		case http.MethodGet:
			writeTestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{
				ID: "svc-1", Name: "web", Revision: "17",
			}))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	svc, err := c.DeleteService(context.Background(), "web", v1.WriteOptions{
		IfMatch: `"17"`, IgnoreMissing: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "svc-1", svc.ID, "the previous document comes from the GET")

	require.Len(t, calls, 2)
	assert.Equal(t, http.MethodGet, calls[0].method)
	assert.Equal(t, "/v1/services/web", calls[0].path)
	assert.Equal(t, http.MethodDelete, calls[1].method)
	assert.Equal(t, `"17"`, calls[1].ifMatch)
	assert.Equal(t, "true", calls[1].ignore)
}

func TestRemoteDeleteServiceMissingIsNotFound(t *testing.T) {
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
			Code: "not_found", Message: "service not found", Retryable: false,
		}))
	})

	_, err := c.DeleteService(context.Background(), "web", v1.WriteOptions{})
	typed := codeOf(t, err)
	assert.Equal(t, "not_found", string(typed.Code))
}

func TestRemoteDeleteServiceIgnoreMissingIsSuccess(t *testing.T) {
	var deletes int
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeTestDocument(w, http.StatusNotFound, v1.NewErrorDocument(v1.Error{
				Code: "not_found", Message: "service not found", Retryable: false,
			}))
		case http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusNoContent)
		}
	})

	svc, err := c.DeleteService(context.Background(), "web", v1.WriteOptions{IgnoreMissing: true})
	require.NoError(t, err)
	assert.Equal(t, v1.Service{}, svc)
	assert.Equal(t, 1, deletes)
}

func TestRemoteRollbackServiceRequest(t *testing.T) {
	var got capturedRequest
	c := newTestRemote(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		got = captureRequest(t, r)
		writeTestDocument(w, http.StatusOK, v1.NewServiceDocument(v1.Service{
			ID: "svc-1", Name: "web", Revision: "19",
		}))
	})

	svc, err := c.RollbackService(context.Background(), "web", v1.WriteOptions{IfMatch: `"18"`})
	require.NoError(t, err)
	assert.Equal(t, "19", svc.Revision)

	assert.Equal(t, http.MethodPost, got.method)
	assert.Equal(t, "/v1/services/web/rollback", got.path)
	assert.Equal(t, `"18"`, got.ifMatch)
	assert.Empty(t, got.body)
	assert.Empty(t, got.contentType)
}
