package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeServiceInspector struct {
	services []v1.Service
	service  v1.Service
	listErr  error
	getErr   error
	gotID    string
}

func (f *fakeServiceInspector) ListServices(context.Context) ([]v1.Service, error) {
	return f.services, f.listErr
}

func (f *fakeServiceInspector) GetService(_ context.Context, id string) (v1.Service, error) {
	f.gotID = id
	return f.service, f.getErr
}

func TestInspectServiceContractJSON(t *testing.T) {
	inspector := &fakeServiceInspector{
		services: []v1.Service{
			{ID: "svc-123", Name: "web"},
			{ID: "svc-456", Name: "api"},
		},
		service: v1.Service{
			ID:       "svc-123",
			Name:     "web",
			Revision: "17",
			Spec: v1.ServiceSpec{
				Mode:     "replicated",
				Replicas: ptrInt64(3),
				Task:     v1.TaskSpec{Image: "nginx:alpine"},
			},
			Status: v1.ServiceStatus{DesiredTasks: 3, RunningTasks: 2, Converged: false},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, inspectServiceContractWithClient(
		context.Background(), inspector, &buf, "web",
	))

	var doc v1.ServiceDocument
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, v1.KindService, doc.Kind)
	assert.Equal(t, "svc-123", doc.ID)
	assert.Equal(t, "web", doc.Name)
	assert.Equal(t, "nginx:alpine", doc.Spec.Task.Image)
	assert.Equal(t, 2, doc.Status.RunningTasks)
	assert.Equal(t, "svc-123", inspector.gotID)
}

func TestInspectServiceContractResolvesIDNameAndPrefix(t *testing.T) {
	services := []v1.Service{
		{ID: "abc111", Name: "web"},
		{ID: "def222", Name: "api"},
	}
	cases := []struct {
		ref  string
		want string
	}{
		{"abc111", "abc111"},
		{"api", "def222"},
		{"abc", "abc111"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			got, err := resolveV1ServiceRef(tc.ref, services)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestInspectServiceContractErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		_, err := resolveV1ServiceRef("missing", []v1.Service{{ID: "abc111", Name: "web"}})
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeNotFound, typed.Code)
	})

	t.Run("ambiguous prefix", func(t *testing.T) {
		_, err := resolveV1ServiceRef("abc", []v1.Service{{ID: "abc111"}, {ID: "abc222"}})
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeAmbiguous, typed.Code)
	})

	t.Run("empty reference", func(t *testing.T) {
		_, err := resolveV1ServiceRef("  ", nil)
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeUsage, typed.Code)
	})
}

func TestInspectServiceContractMapsRPCErrors(t *testing.T) {
	inspector := &fakeServiceInspector{
		services: []v1.Service{{ID: "svc-1", Name: "web"}},
		getErr:   status.Error(codes.Unavailable, "manager down"),
	}

	err := inspectServiceContractWithClient(context.Background(), inspector, &bytes.Buffer{}, "web")
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUnavailable, typed.Code)
}

func TestInspectServiceContractListFailure(t *testing.T) {
	inspector := &fakeServiceInspector{listErr: status.Error(codes.PermissionDenied, "no")}
	err := inspectServiceContractWithClient(context.Background(), inspector, &bytes.Buffer{}, "web")
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeForbidden, typed.Code)
}

func TestInspectServiceCommandRejectsUnsupportedOutput(t *testing.T) {
	old := outputFormat
	t.Cleanup(func() { outputFormat = old })
	outputFormat = "table"

	cmd := newServiceInspectCommand()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"web"})

	err := cmd.Execute()
	require.Error(t, err)
	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeUsage, typed.Code)
}

func ptrInt64(v int64) *int64 { return &v }
