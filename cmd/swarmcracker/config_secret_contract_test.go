package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/restuhaqza/swarmcracker/pkg/api/v1"
	"github.com/restuhaqza/swarmcracker/pkg/output"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeConfigLister struct {
	configs []v1.Config
	err     error
}

func (f *fakeConfigLister) ListConfigs(context.Context) ([]v1.Config, error) {
	return f.configs, f.err
}

type fakeSecretLister struct {
	secrets []v1.Secret
	err     error
}

func (f *fakeSecretLister) ListSecrets(context.Context) ([]v1.Secret, error) {
	return f.secrets, f.err
}

func TestConfigListContractJSONOmitsData(t *testing.T) {
	lister := &fakeConfigLister{configs: []v1.Config{
		{ID: "cfg-1", Name: "app", Revision: "3", Labels: map[string]string{"team": "payments"}},
	}}

	var buf bytes.Buffer
	require.NoError(t, listConfigsContractWithClient(context.Background(), lister, &buf, "json"))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, float64(v1.SchemaVersion), doc["schemaVersion"])
	assert.Equal(t, v1.KindConfigList, doc["kind"])
	items, ok := doc["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "app", item["name"])
	assert.NotContains(t, item, "data")
}

func TestConfigListContractNameMode(t *testing.T) {
	lister := &fakeConfigLister{configs: []v1.Config{{ID: "cfg-1"}, {ID: "cfg-2"}}}
	var buf bytes.Buffer
	require.NoError(t, listConfigsContractWithClient(context.Background(), lister, &buf, "name"))
	assert.Equal(t, "cfg-1\ncfg-2\n", buf.String())
}

func TestSecretListContractJSONOmitsData(t *testing.T) {
	lister := &fakeSecretLister{secrets: []v1.Secret{
		{ID: "sec-1", Name: "token", Revision: "4", Driver: "external", Labels: map[string]string{"env": "prod"}},
	}}

	var buf bytes.Buffer
	require.NoError(t, listSecretsContractWithClient(context.Background(), lister, &buf, "json"))

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, v1.KindSecretList, doc["kind"])
	items := doc["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "external", item["driver"])
	assert.NotContains(t, item, "data")
}

func TestSecretListContractNameMode(t *testing.T) {
	lister := &fakeSecretLister{secrets: []v1.Secret{{ID: "sec-1"}, {ID: "sec-2"}}}
	var buf bytes.Buffer
	require.NoError(t, listSecretsContractWithClient(context.Background(), lister, &buf, "name"))
	assert.Equal(t, "sec-1\nsec-2\n", buf.String())
}

func TestConfigAndSecretListContractErrors(t *testing.T) {
	cfgErr := listConfigsContractWithClient(context.Background(),
		&fakeConfigLister{err: status.Error(codes.Unavailable, "down")}, &bytes.Buffer{}, "json")
	require.Error(t, cfgErr)
	var typed *output.Error
	require.ErrorAs(t, cfgErr, &typed)
	assert.Equal(t, output.CodeUnavailable, typed.Code)

	secretErr := listSecretsContractWithClient(context.Background(),
		&fakeSecretLister{err: status.Error(codes.PermissionDenied, "no")}, &bytes.Buffer{}, "json")
	require.Error(t, secretErr)
	require.ErrorAs(t, secretErr, &typed)
	assert.Equal(t, output.CodeForbidden, typed.Code)
}

func TestNamedListCommandsRejectContractFlagCombinations(t *testing.T) {
	old := outputFormat
	t.Cleanup(func() { outputFormat = old })

	constructors := map[string]func() *cobra.Command{
		"config": newConfigListCommand,
		"secret": newSecretListCommand,
	}
	cases := []struct {
		name     string
		output   string
		setFlags map[string]string
	}{
		{name: "unsupported", output: "ndjson"},
		{name: "output and format", output: "json", setFlags: map[string]string{"format": "json"}},
		{name: "quiet with json", output: "json", setFlags: map[string]string{"quiet": "true"}},
		{name: "quiet with name", output: "name", setFlags: map[string]string{"quiet": "true"}},
	}

	for resource, constructor := range constructors {
		for _, tc := range cases {
			t.Run(resource+"/"+tc.name, func(t *testing.T) {
				outputFormat = tc.output
				cmd := constructor()
				cmd.SilenceUsage = true
				cmd.SilenceErrors = true
				for flag, value := range tc.setFlags {
					require.NoError(t, cmd.Flags().Set(flag, value))
				}
				cmd.SetArgs(nil)

				err := cmd.Execute()
				require.Error(t, err)
				var typed *output.Error
				require.ErrorAs(t, err, &typed)
				assert.Equal(t, output.CodeUsage, typed.Code)
			})
		}
	}
}
