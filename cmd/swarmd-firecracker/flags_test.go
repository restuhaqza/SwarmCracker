package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
)

// TestAgentRequireTokenFlagIsRegistered proves the flag the generated units and
// the init/join commands pass is the flag this daemon actually parses. The two
// live in different packages, so a rename on one side would otherwise be a
// silent downgrade: the unit would pass an unknown flag (the daemon would refuse
// to start) or, worse, the daemon would ignore a flag it never registered.
func TestAgentRequireTokenFlagIsRegistered(t *testing.T) {
	var found *cli.BoolFlag
	for _, flag := range newAppFlags() {
		if boolFlag, ok := flag.(*cli.BoolFlag); ok && boolFlag.Name == config.AgentRequireTokenFlag {
			found = boolFlag
			break
		}
	}
	if found == nil {
		t.Fatalf("newAppFlags() does not register the bool flag %q that the systemd units pass", config.AgentRequireTokenFlag)
	}
	if found.Value {
		t.Errorf("%s must default to false: a daemon started without the flag (an upgraded unit) keeps the old, unauthenticated behaviour. New units pass the flag because init/join default it on.", config.AgentRequireTokenFlag)
	}
}

// TestAgentRequireTokenComesOnlyFromTheFlag pins the precedence between the two
// surfaces. The daemon never loads the YAML config file, so the flag is the only
// input: a config that says true cannot turn auth on, and one that says false
// cannot turn it off. The two named disagreements are the middle rows.
func TestAgentRequireTokenComesOnlyFromTheFlag(t *testing.T) {
	tests := []struct {
		name      string
		configKey string // empty means the config has no agent section
		args      []string
		want      bool
	}{
		{name: "flag absent, key absent", configKey: "", args: nil, want: false},
		{name: "flag absent, key true", configKey: "agent:\n  require_token: true\n", args: nil, want: false},
		{name: "flag present, key absent", configKey: "", args: []string{"--" + config.AgentRequireTokenFlag}, want: true},
		{name: "flag present, key false", configKey: "agent:\n  require_token: false\n", args: []string{"--" + config.AgentRequireTokenFlag}, want: true},
		{name: "flag explicitly off, key true", configKey: "agent:\n  require_token: true\n", args: []string{"--" + config.AgentRequireTokenFlag + "=false"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "version: 1\n"
			if tt.configKey != "" {
				content += tt.configKey
			}
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			t.Setenv("SWARMCRACKER_CONFIG", path)

			var got bool
			app := cli.NewApp()
			app.Flags = newAppFlags()
			app.Action = func(ctx *cli.Context) error {
				got = agentRequireToken(ctx)
				return nil
			}
			require.NoError(t, app.Run(append([]string{"swarmd-firecracker"}, tt.args...)))
			assert.Equal(t, tt.want, got)
		})
	}
}
