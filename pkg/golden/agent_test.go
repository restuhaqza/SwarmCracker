package golden

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckAgentPin pins the refusal a host applies to a template: only a pin at
// or above the minimum passes, and every way of failing carries the typed code
// so a caller never has to match message text.
func TestCheckAgentPin(t *testing.T) {
	tests := []struct {
		name string
		pin  AgentPin
		ok   bool
	}{
		{name: "current protocol", pin: AgentPin{Protocol: MinimumAgentProtocol}, ok: true},
		{name: "newer protocol", pin: AgentPin{Protocol: "10"}, ok: true},
		{name: "older protocol", pin: AgentPin{Protocol: agent.VersionLegacy}},
		{name: "unpinned template", pin: AgentPin{}},
		{name: "unparseable protocol", pin: AgentPin{Protocol: "v2"}},
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
		})
	}
}

// TestRecipe_DefaultsPinCurrentAgent proves every recipe records the agent this
// build ships, even when the author says nothing about it.
func TestRecipe_DefaultsPinCurrentAgent(t *testing.T) {
	r := mustRecipe(t, validRecipeYAML)
	require.NoError(t, r.Validate())

	assert.Equal(t, MinimumAgentProtocol, r.Spec.Agent.Protocol)
	assert.Equal(t, agent.AgentVersion, r.Spec.Agent.Version)
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
