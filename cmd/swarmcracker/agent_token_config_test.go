package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// generatedAgentConfig is the only part of a generated init/join config this
// test reads. The generated files use their own schema (manager:/worker:/
// firecracker:), so they are parsed as YAML rather than through pkg/config.
type generatedAgentConfig struct {
	Agent struct {
		RequireToken bool `yaml:"require_token"`
	} `yaml:"agent"`
}

// TestRenderManagerConfigRequiresTokenByDefault pins the new-install decision on
// the init side: the config `init` writes authenticates its guests, and the
// value survives a real YAML parse.
func TestRenderManagerConfigRequiresTokenByDefault(t *testing.T) {
	rendered := renderManagerConfig(newTestInitConfig())

	var parsed generatedAgentConfig
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &parsed), "the generated config must be valid YAML")
	assert.True(t, parsed.Agent.RequireToken, "a new manager config must set agent.require_token: true")
}

// TestRenderWorkerConfigRequiresTokenByDefault is the join half.
func TestRenderWorkerConfigRequiresTokenByDefault(t *testing.T) {
	rendered := renderWorkerConfig(newTestJoinConfig())

	var parsed generatedAgentConfig
	require.NoError(t, yaml.Unmarshal([]byte(rendered), &parsed), "the generated config must be valid YAML")
	assert.True(t, parsed.Agent.RequireToken, "a new worker config must set agent.require_token: true")
}

// TestInitAndJoinEnableTokenByDefault pins the other half of a new installation:
// the unit only renders --agent-require-token when the command's flag is set, so
// the flag default has to be true or a fresh node would write the config key but
// start the daemon unauthenticated.
func TestInitAndJoinEnableTokenByDefault(t *testing.T) {
	initFlag := newInitCommand().Flags().Lookup(config.AgentRequireTokenFlag)
	require.NotNil(t, initFlag, "init must define %s", config.AgentRequireTokenFlag)
	assert.Equal(t, "true", initFlag.DefValue, "a new manager must authenticate its guests")

	joinFlag := newJoinCommand().Flags().Lookup(config.AgentRequireTokenFlag)
	require.NotNil(t, joinFlag, "join must define %s", config.AgentRequireTokenFlag)
	assert.Equal(t, "true", joinFlag.DefValue, "a new worker must authenticate its guests")
}

// TestInitAndJoinWriteAConfigThatAgreesWithTheirUnit pins the generation
// boundary: init/join render the config key and the unit flag from one field, so
// a node they produce can never be given two different answers. When the value
// is false both artifacts say off; when true both say on.
func TestInitAndJoinWriteAConfigThatAgreesWithTheirUnit(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run("enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
			initCfg := newTestInitConfig()
			initCfg.AgentRequireToken = enabled
			managerUnit, err := renderManagerService(initCfg)
			require.NoError(t, err)

			var managerDoc generatedAgentConfig
			require.NoError(t, yaml.Unmarshal([]byte(renderManagerConfig(initCfg)), &managerDoc))
			assert.Equal(t, enabled, managerDoc.Agent.RequireToken, "manager config key must follow the setting")
			assert.Equal(t, enabled, strings.Contains(string(managerUnit), "--"+config.AgentRequireTokenFlag),
				"manager unit flag must follow the same setting")

			joinCfg := newTestJoinConfig()
			joinCfg.AgentRequireToken = enabled
			workerUnit, err := renderWorkerService(joinCfg)
			require.NoError(t, err)

			var workerDoc generatedAgentConfig
			require.NoError(t, yaml.Unmarshal([]byte(renderWorkerConfig(joinCfg)), &workerDoc))
			assert.Equal(t, enabled, workerDoc.Agent.RequireToken, "worker config key must follow the setting")
			assert.Equal(t, enabled, strings.Contains(string(workerUnit), "--"+config.AgentRequireTokenFlag),
				"worker unit flag must follow the same setting")
		})
	}
}
