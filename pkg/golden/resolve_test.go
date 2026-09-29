package golden

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeGolden(t *testing.T, dir, name, version string) string {
	t.Helper()
	base := ArtifactBaseName(name, version)
	artifact := filepath.Join(dir, base+".ext4")
	require.NoError(t, os.WriteFile(artifact, []byte("img"), 0644))

	md := ArtifactMetadata{
		APIVersion:   APIVersionV1Alpha1,
		Kind:         KindGoldenImage,
		Name:         name,
		Version:      version,
		RecipeDigest: "sha256:test",
		Artifact:     base + ".ext4",
		SizeBytes:    3,
		SHA256:       "abc",
		Init:         Init{System: InitSystemd, BootArgs: []string{"systemd.unified_cgroup_hierarchy=1"}},
	}
	require.NoError(t, writeMetadataAtomic(filepath.Join(dir, base+".json"), md))
	return artifact
}

func TestResolve_ByName(t *testing.T) {
	dir := t.TempDir()
	artifact := writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")

	art, err := Resolve(dir, "ubuntu-24.04-docker")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-24.04-docker@1.0.0", art.Ref)
	assert.Equal(t, artifact, art.Path)
	assert.Equal(t, InitSystemd, art.Metadata.Init.System)
}

func TestResolve_ByVersion(t *testing.T) {
	dir := t.TempDir()
	want := writeGolden(t, dir, "alpine-3.20-docker", "1.0.0")
	writeGolden(t, dir, "alpine-3.20-docker", "2.0.0")

	art, err := Resolve(dir, "alpine-3.20-docker@1.0.0")
	require.NoError(t, err)
	assert.Equal(t, want, art.Path)
}

func TestResolve_UnversionedPicksNewest(t *testing.T) {
	dir := t.TempDir()
	old := writeGolden(t, dir, "debian-12-docker", "1.0.0")
	newer := writeGolden(t, dir, "debian-12-docker", "2.0.0")

	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "golden-debian-12-docker-1.0.0.json"), past, past))

	art, err := Resolve(dir, "debian-12-docker")
	require.NoError(t, err)
	assert.Equal(t, newer, art.Path)
	assert.NotEqual(t, old, art.Path)
}

func TestResolve_Errors(t *testing.T) {
	dir := t.TempDir()
	writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")

	_, err := Resolve(dir, "")
	require.Error(t, err)

	_, err = Resolve(dir, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = Resolve(dir, "ubuntu-24.04-docker@9.9.9")
	require.Error(t, err)

	_, err = Resolve(dir, "../../etc/passwd")
	require.Error(t, err)
}

func TestResolve_MissingArtifactFile(t *testing.T) {
	dir := t.TempDir()
	artifact := writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")
	require.NoError(t, os.Remove(artifact))

	_, err := Resolve(dir, "ubuntu-24.04-docker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}
