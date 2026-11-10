package golden

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckAgentPin pins the refusal a host applies to a template: only a pin at
// or above the minimum *and* advertising every operation the host needs passes,
// and every way of failing carries the typed code so a caller never has to match
// message text.
//
// The capability cases are the point of recording capabilities at all: protocol 2
// covers both an agent that can prepare a restored clone and one that cannot, so a
// template whose agent was not asked, or was asked and said no, must be refused
// rather than restored into an instance wearing its source's identity.
func TestCheckAgentPin(t *testing.T) {
	caps := agent.Capabilities()
	withoutPrepare := slices.DeleteFunc(slices.Clone(caps), func(cap string) bool { return cap == agent.CapPrepare })

	tests := []struct {
		name string
		pin  AgentPin
		ok   bool
		// missing is what the refusal must name, for the cases where the
		// capability set is what fails.
		missing []string
	}{
		{
			name: "current protocol with every operation",
			pin:  AgentPin{Protocol: MinimumAgentProtocol, Caps: caps},
			ok:   true,
		},
		{
			name: "newer protocol with every operation",
			pin:  AgentPin{Protocol: "10", Caps: caps},
			ok:   true,
		},
		{
			name:    "current protocol without the hook",
			pin:     AgentPin{Protocol: MinimumAgentProtocol, Caps: withoutPrepare},
			missing: []string{agent.CapPrepare},
		},
		{
			name:    "current protocol with no recorded capabilities",
			pin:     AgentPin{Protocol: MinimumAgentProtocol},
			missing: []string{agent.CapPrepare},
		},
		{name: "older protocol", pin: AgentPin{Protocol: agent.VersionLegacy, Caps: caps}},
		{name: "unpinned template", pin: AgentPin{}},
		{name: "unparseable protocol", pin: AgentPin{Protocol: "v2", Caps: caps}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckAgentPin("tmpl@1", tc.pin)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var incompatible *IncompatibleError
			require.True(t, errors.As(err, &incompatible), "want *IncompatibleError, got %T", err)
			assert.Equal(t, CodeTemplateIncompatible, incompatible.Code())
			assert.Equal(t, MinimumAgentProtocol, incompatible.Minimum)
			assert.Contains(t, err.Error(), CodeTemplateIncompatible)
			assert.Contains(t, err.Error(), "tmpl@1")
			if len(tc.missing) > 0 {
				assert.Equal(t, tc.missing, incompatible.Missing)
				assert.Contains(t, err.Error(), agent.CapPrepare,
					"the refusal names the operation the template's agent is missing")
			}
		})
	}
}

// TestRecipe_DefaultsPinCurrentAgent proves every recipe records the agent this
// build ships, even when the author says nothing about it.
func TestRecipe_DefaultsPinCurrentAgent(t *testing.T) {
	r := mustRecipe(t, validRecipeYAML)
	require.NoError(t, r.Validate())

	assert.Equal(t, MinimumAgentProtocol, r.Spec.Agent.Protocol)
	assert.Empty(t, r.Spec.Agent.Version,
		"the agent version is recorded from the booted agent, not assumed by the recipe")
}

// TestRecipe_RefusesOlderAgentPin proves a recipe cannot build a template the
// host would refuse to use: the build fails instead of producing a dead image.
func TestRecipe_RefusesOlderAgentPin(t *testing.T) {
	r := mustRecipe(t, validRecipeYAML)
	r.Spec.Agent = AgentPin{Protocol: agent.VersionLegacy, Version: "0.1.0"}

	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), CodeTemplateIncompatible)
}

// TestResolve_RefusesIncompatibleTemplate proves the use path applies the same
// rule: a store entry pinned below the minimum cannot be resolved, so no caller
// can boot an agent that is missing an operation.
func TestResolve_RefusesIncompatibleTemplate(t *testing.T) {
	dir := t.TempDir()
	writeGolden(t, dir, "legacy", "1.0.0")

	// Rewrite the sidecar with an agent from before v2.
	md, err := ReadMetadata(artifactPathFor(dir, "legacy", "1.0.0"))
	require.NoError(t, err)
	md.Agent = AgentPin{Protocol: agent.VersionLegacy, Version: "0.1.0"}
	require.NoError(t, writeMetadataAtomic(artifactPathFor(dir, "legacy", "1.0.0"), *md))

	_, err = Resolve(dir, "legacy")
	require.Error(t, err)
	assert.Equal(t, CodeTemplateIncompatible, codeOf(err))
}

// artifactPathFor returns the metadata sidecar path of a fixture written by
// writeGolden.
func artifactPathFor(dir, name, version string) string {
	return filepath.Join(dir, ArtifactBaseName(name, version)+".json")
}

// codeOf returns the stable code of a golden refusal, or "" for any other error.
func codeOf(err error) string {
	var coder interface{ Code() string }
	if errors.As(err, &coder) {
		return coder.Code()
	}
	return ""
}
