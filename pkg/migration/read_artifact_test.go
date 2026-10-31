package migration

// The reader a node's executor uses at task start: a pending-restore directive names a
// staging directory, and the manifest in it is the only description of what the bytes
// are. These tests pin the two things that make reading it safe — it is validated, and
// it is read without following a link planted in a directory another node wrote.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stageArtifact writes artifact as the manifest of a staged artifact in a fresh
// directory and returns that directory.
func stageArtifact(t *testing.T, artifact Artifact) string {
	t.Helper()
	dir := t.TempDir()
	body, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ArtifactDocumentName), body, 0o644))
	return dir
}

// TestReadArtifactReturnsTheStagedManifest pins the round trip: what a stage published is
// what the executor reads, described by the same rules that accepted it.
func TestReadArtifactReturnsTheStagedManifest(t *testing.T) {
	want := validArtifact()
	dir := stageArtifact(t, want)

	got, err := ReadArtifact(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestReadArtifactRefusesUnusableManifests pins the rejection paths. A manifest that
// cannot be restored from must fail here, before any device or process exists.
func TestReadArtifactRefusesUnusableManifests(t *testing.T) {
	cases := []struct {
		name    string
		stage   func(t *testing.T) string
		wantErr string
	}{
		{
			name: "a directory that holds no manifest",
			stage: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: "artifact.json",
		},
		{
			name: "a manifest that does not decode",
			stage: func(t *testing.T) string {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, ArtifactDocumentName), []byte("{nope"), 0o644))
				return dir
			},
			wantErr: "does not decode",
		},
		{
			name: "a manifest that describes nothing",
			stage: func(t *testing.T) string {
				return stageArtifact(t, Artifact{})
			},
			wantErr: "schemaVersion",
		},
		{
			name: "a manifest naming an artifact that cannot be one",
			stage: func(t *testing.T) string {
				artifact := validArtifact()
				artifact.ID = "../../etc"
				return stageArtifact(t, artifact)
			},
			wantErr: "id",
		},
		{
			name: "a relative directory",
			stage: func(t *testing.T) string {
				return "relative/dir"
			},
			wantErr: "is relative",
		},
		{
			name: "an empty directory",
			stage: func(t *testing.T) string {
				return " "
			},
			wantErr: "is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadArtifact(tc.stage(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestReadArtifactClassifiesWhatItRefuses pins the two classes a caller acts on: a
// directory that holds no manifest is "not staged" (the directive is stale), and a
// manifest that is present but unusable is ErrInvalidArtifact (the bytes are wrong).
func TestReadArtifactClassifiesWhatItRefuses(t *testing.T) {
	_, err := ReadArtifact(t.TempDir())
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist), "a directory without a manifest is not staged: %v", err)
	assert.False(t, errors.Is(err, ErrInvalidArtifact), "an absent manifest is not an invalid one: %v", err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ArtifactDocumentName), []byte("{}"), 0o644))
	_, err = ReadArtifact(dir)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidArtifact), "a manifest that describes nothing is invalid: %v", err)
}

// TestReadArtifactDoesNotFollowALink pins the hardening: the staging directory is
// written by a transfer from another node, so a link planted there must not hand this
// reader a manifest from anywhere else — another artifact's, or one outside the root.
func TestReadArtifactDoesNotFollowALink(t *testing.T) {
	elsewhere := stageArtifact(t, validArtifact())

	dir := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(elsewhere, ArtifactDocumentName),
		filepath.Join(dir, ArtifactDocumentName)))

	_, err := ReadArtifact(dir)
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "schemaVersion"),
		"the linked manifest must not be read at all: %v", err)
}

// TestReadArtifactRefusesAnOversizedManifest pins the bound. A manifest is kilobytes; a
// larger file is not one, and reading it would be an allocation decided by a remote node.
func TestReadArtifactRefusesAnOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	body := `{"kind":"` + ArtifactKind + `","pad":"` + strings.Repeat("x", maxArtifactDocumentBytes+1) + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ArtifactDocumentName), []byte(body), 0o644))

	_, err := ReadArtifact(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "larger than")
}
