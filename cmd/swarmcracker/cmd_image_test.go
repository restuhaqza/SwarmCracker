package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeArtifact creates a metadata sidecar (and its artifact file) in dir.
func writeArtifact(t *testing.T, dir, name, version string) {
	t.Helper()
	base := golden.ArtifactBaseName(name, version)
	artifact := filepath.Join(dir, base+".ext4")
	require.NoError(t, os.WriteFile(artifact, []byte("img"), 0644))

	md := golden.ArtifactMetadata{
		APIVersion:    golden.APIVersionV1Alpha1,
		Kind:          golden.KindGoldenImage,
		Name:          name,
		Version:       version,
		RecipeDigest:  "sha256:abc",
		KernelProfile: "guest-runtime-6.1",
		Runtime:       golden.Runtime{Name: golden.RuntimeDocker},
		Artifact:      base + ".ext4",
		SizeBytes:     1024,
		SHA256:        "deadbeef",
	}
	data, err := json.MarshalIndent(md, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, base+".json"), data, 0644))
}

func TestRunImageInspect_ByName(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "ubuntu-24.04-docker", "1.0.0")

	err := runImageInspect("ubuntu-24.04-docker@1.0.0", dir)
	require.NoError(t, err)
}

func TestRunImageInspect_BadReference(t *testing.T) {
	err := runImageInspect("ubuntu-24.04-docker", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "<name>@<version>")
}

func TestRunImageInspect_MissingMetadata(t *testing.T) {
	err := runImageInspect("nope@1.0.0", t.TempDir())
	require.Error(t, err)
}

func TestRunImageList_WithArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "ubuntu-24.04-docker", "1.0.0")
	writeArtifact(t, dir, "alpine-3.20-docker", "1.0.0")

	require.NoError(t, runImageList(dir))
}

func TestRunImageList_Empty(t *testing.T) {
	require.NoError(t, runImageList(t.TempDir()))
}
