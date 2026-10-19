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

type fakeServiceLister struct {
	services []v1.Service
	err      error
}

func (f *fakeServiceLister) ListServices(context.Context) ([]v1.Service, error) {
	return f.services, f.err
}

func TestServiceListContractEmptyIsParseable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listServicesContractWithClient(
		context.Background(), &fakeServiceLister{}, &buf, "json", "",
	))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc), buf.String())
	assert.Equal(t, float64(v1.SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, v1.KindServiceList, doc["kind"])
	items, ok := doc["items"].([]any)
	require.True(t, ok, "items must be a JSON array")
	assert.Empty(t, items)
	assert.Nil(t, doc["next"])
}

func TestServiceListContractJSONAndFilter(t *testing.T) {
	lister := &fakeServiceLister{services: []v1.Service{
		{ID: "svc-1", Name: "web", Revision: "3", Spec: v1.ServiceSpec{Mode: "replicated"}},
		{ID: "svc-2", Name: "api", Revision: "4", Spec: v1.ServiceSpec{Mode: "replicated"}},
	}}

	var buf bytes.Buffer
	require.NoError(t, listServicesContractWithClient(
		context.Background(), lister, &buf, "json", "name=web",
	))

	var doc v1.ServiceList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	require.Len(t, doc.Items, 1)
	assert.Equal(t, "web", doc.Items[0].Name)
	assert.Equal(t, "svc-1", doc.Items[0].ID)
}

func TestServiceListContractNameMode(t *testing.T) {
	lister := &fakeServiceLister{services: []v1.Service{
		{ID: "svc-1", Name: "web"},
		{ID: "svc-2", Name: "api"},
	}}

	var buf bytes.Buffer
	require.NoError(t, listServicesContractWithClient(
		context.Background(), lister, &buf, "name", "",
	))
	assert.Equal(t, "svc-1\nsvc-2\n", buf.String())
}

func TestServiceListContractMapsRPCError(t *testing.T) {
	lister := &fakeServiceLister{err: status.Error(codes.NotFound, "missing")}
	err := listServicesContractWithClient(context.Background(), lister, &bytes.Buffer{}, "json", "")
	require.Error(t, err)

	var typed *output.Error
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, output.CodeNotFound, typed.Code)
}

func TestLegacyServiceListJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listServicesWithClient(
		context.Background(), &fakeServiceTaskClient{}, &buf, "json", "", false,
	))
	assert.JSONEq(t, "[]", buf.String())
}

func TestLegacyServiceListTableEmptyKeepsProse(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, listServicesWithClient(
		context.Background(), &fakeServiceTaskClient{}, &buf, "table", "", false,
	))
	assert.Contains(t, buf.String(), "No services found")
}

func TestCLIExitCodeMapsTypedErrors(t *testing.T) {
	assert.Equal(t, 0, cliExitCode(nil))
	assert.Equal(t, 2, cliExitCode(output.NewError(output.CodeUsage, "bad flag")))
	assert.Equal(t, 3, cliExitCode(output.NewError(output.CodeNotFound, "missing")))
	assert.Equal(t, 4, cliExitCode(output.NewError(output.CodeConflict, "stale")))
	assert.Equal(t, 5, cliExitCode(output.NewError(output.CodeForbidden, "no")))
	assert.Equal(t, 6, cliExitCode(output.NewError(output.CodeUnavailable, "down")))
	assert.Equal(t, 8, cliExitCode(&exitCodeError{code: 8}))
}
