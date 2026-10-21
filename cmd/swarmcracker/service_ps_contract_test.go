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

type fakeServiceTaskLister struct {
	services []v1.Service
	tasks    []v1.Task
	listErr  error
	taskErr  error
	gotID    string
}

func (f *fakeServiceTaskLister) ListServices(context.Context) ([]v1.Service, error) {
	return f.services, f.listErr
}

func (f *fakeServiceTaskLister) ListServiceTasks(_ context.Context, id string) ([]v1.Task, error) {
	f.gotID = id
	return f.tasks, f.taskErr
}

func TestServicePSContractJSONAndFilter(t *testing.T) {
	lister := &fakeServiceTaskLister{
		services: []v1.Service{{ID: "svc-1", Name: "web"}},
		tasks: []v1.Task{
			{ID: "task-1", ServiceID: "svc-1", State: "running", DesiredState: "running"},
			{ID: "task-2", ServiceID: "svc-1", State: "failed", DesiredState: "shutdown"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksContractWithClient(
		context.Background(), lister, &buf, "web", "json", "state=running",
	))

	var doc v1.TaskList
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, v1.SchemaVersion, doc.SchemaVersion)
	assert.Equal(t, v1.KindTaskList, doc.Kind)
	require.Len(t, doc.Items, 1)
	assert.Equal(t, "task-1", doc.Items[0].ID)
	assert.Equal(t, "svc-1", lister.gotID)
}

func TestServicePSContractNameMode(t *testing.T) {
	lister := &fakeServiceTaskLister{
		services: []v1.Service{{ID: "svc-1", Name: "web"}},
		tasks: []v1.Task{
			{ID: "task-1", ServiceID: "svc-1", State: "running", DesiredState: "running"},
			{ID: "task-2", ServiceID: "svc-1", State: "running", DesiredState: "running"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, listServiceTasksContractWithClient(
		context.Background(), lister, &buf, "svc-1", "name", "",
	))
	assert.Equal(t, "task-1\ntask-2\n", buf.String())
}

func TestServicePSContractErrors(t *testing.T) {
	t.Run("service not found", func(t *testing.T) {
		lister := &fakeServiceTaskLister{}
		err := listServiceTasksContractWithClient(
			context.Background(), lister, &bytes.Buffer{}, "missing", "json", "",
		)
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeNotFound, typed.Code)
	})

	t.Run("task rpc failure", func(t *testing.T) {
		lister := &fakeServiceTaskLister{
			services: []v1.Service{{ID: "svc-1", Name: "web"}},
			taskErr:  status.Error(codes.Unavailable, "manager down"),
		}
		err := listServiceTasksContractWithClient(
			context.Background(), lister, &bytes.Buffer{}, "web", "json", "",
		)
		require.Error(t, err)
		var typed *output.Error
		require.ErrorAs(t, err, &typed)
		assert.Equal(t, output.CodeUnavailable, typed.Code)
	})
}

func TestServicePSCommandRejectsContractFlagCombinations(t *testing.T) {
	old := outputFormat
	t.Cleanup(func() { outputFormat = old })

	cases := []struct {
		name        string
		output      string
		setFlags    map[string]string
		wantMessage string
	}{
		{
			name:        "unsupported format",
			output:      "ndjson",
			wantMessage: "unsupported output format",
		},
		{
			name:        "output and format",
			output:      "json",
			setFlags:    map[string]string{"format": "json"},
			wantMessage: "use either --output or --format",
		},
		{
			name:        "quiet with json",
			output:      "json",
			setFlags:    map[string]string{"quiet": "true"},
			wantMessage: "use -o name instead of --quiet",
		},
		{
			name:        "quiet with name",
			output:      "name",
			setFlags:    map[string]string{"quiet": "true"},
			wantMessage: "do not combine -o name with --quiet",
		},
		{
			name:        "health port with json",
			output:      "json",
			setFlags:    map[string]string{"health-port": "9000"},
			wantMessage: "--health-port is not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputFormat = tc.output
			cmd := newServicePSCommand()
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			for flag, value := range tc.setFlags {
				require.NoError(t, cmd.Flags().Set(flag, value))
			}
			cmd.SetArgs([]string{"web"})

			err := cmd.Execute()
			require.Error(t, err)
			var typed *output.Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, output.CodeUsage, typed.Code)
			assert.Contains(t, typed.Message, tc.wantMessage)
		})
	}
}
