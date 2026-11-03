package main

import (
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
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
