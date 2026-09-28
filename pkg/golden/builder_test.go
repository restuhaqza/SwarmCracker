package golden

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes for the builder seams ---

type fakeExtractor struct {
	calls    *[]string
	err      error
	markFile bool
}

func (f *fakeExtractor) Extract(_ context.Context, src Source, dest string) error {
	*f.calls = append(*f.calls, "extract:"+src.Ref)
	if f.err != nil {
		return f.err
	}
	if f.markFile {
		if err := os.MkdirAll(dest, 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, "base-marker"), []byte("base"), 0644)
	}
	return nil
}

type fakeRunner struct {
	calls   *[]string
	scripts *[]string
	err     error
}

func (f *fakeRunner) Run(_ context.Context, _ string, script string) error {
	*f.calls = append(*f.calls, "run")
	*f.scripts = append(*f.scripts, script)
	return f.err
}

type fakeExt4 struct {
	calls *[]string
	err   error
}

func (f *fakeExt4) Build(_ context.Context, _ string, outputPath string, _ int64) error {
	*f.calls = append(*f.calls, "ext4")
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(outputPath, []byte("fake-ext4-content"), 0644)
}

type harness struct {
	calls   []string
	scripts []string
	extract *fakeExtractor
	runner  *fakeRunner
	ext4    *fakeExt4
	outDir  string
	builder *Builder
}

func newHarness(t *testing.T, force bool) *harness {
	t.Helper()
	h := &harness{outDir: t.TempDir()}
	h.extract = &fakeExtractor{calls: &h.calls, markFile: true}
	h.runner = &fakeRunner{calls: &h.calls, scripts: &h.scripts}
	h.ext4 = &fakeExt4{calls: &h.calls}
	b, err := NewBuilder(BuilderOptions{
		WorkDir:   t.TempDir(),
		OutputDir: h.outDir,
		Force:     force,
		Extractor: h.extract,
		Runner:    h.runner,
		Ext4:      h.ext4,
		Now:       func() time.Time { return time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	h.builder = b
	return h
}

func mustRecipe(t *testing.T, yaml string) *Recipe {
	t.Helper()
	r, err := ParseRecipe([]byte(yaml))
	require.NoError(t, err)
	require.NoError(t, r.Validate())
	return r
}

func TestBuilder_Build_HappyPath(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)

	res, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.False(t, res.Skipped)
	assert.Equal(t, "ubuntu-24.04-docker", res.Name)
	assert.Equal(t, "1.0.0", res.Version)
	assert.Equal(t, r.Digest(), res.Digest)
	assert.FileExists(t, res.ArtifactPath)
	assert.FileExists(t, res.MetadataPath)
	assert.NotEmpty(t, res.SHA256)
	assert.Greater(t, res.SizeBytes, int64(0))

	// Order: extract, provision, seal, ext4.
	require.Len(t, h.calls, 4)
	assert.Equal(t, "extract:docker.io/library/ubuntu:24.04", h.calls[0])
	assert.Equal(t, "run", h.calls[1])
	assert.Equal(t, "run", h.calls[2])
	assert.Equal(t, "ext4", h.calls[3])
	assert.Contains(t, h.scripts[0], "apt-get update")
	assert.Contains(t, h.scripts[1], "machine-id")

	// Metadata is readable and consistent.
	md, err := ReadMetadata(res.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, r.Digest(), md.RecipeDigest)
	assert.Equal(t, res.SHA256, md.SHA256)
	assert.Equal(t, int64(5*1<<30), md.RootMinSizeBytes)
	assert.Equal(t, "guest-runtime-6.1", md.KernelProfile)
}

func TestBuilder_Build_InvalidRecipe_DoesNotBuild(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)
	r.Metadata.Name = "" // invalidate after validation

	_, err := h.builder.Build(context.Background(), r)
	require.Error(t, err)
	assert.Empty(t, h.calls, "nothing must run for an invalid recipe")
}

func TestBuilder_Build_ExtractError(t *testing.T) {
	h := newHarness(t, false)
	h.extract.err = errors.New("registry boom")

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract")
	assert.Equal(t, []string{"extract:docker.io/library/ubuntu:24.04"}, h.calls)
}

func TestBuilder_Build_ProvisionError(t *testing.T) {
	h := newHarness(t, false)
	h.runner.err = errors.New("chroot failed")

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision")
	// Should have stopped before creating the image.
	assert.Equal(t, "extract:docker.io/library/ubuntu:24.04", h.calls[0])
	assert.NotContains(t, h.calls, "ext4")
}

func TestBuilder_Build_SkipsWhenUpToDate(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)

	first, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	require.False(t, first.Skipped)

	// A second builder over the same output dir, with fakes that fail if called,
	// proves the up-to-date artifact is reused without any work.
	var calls []string
	b2, err := NewBuilder(BuilderOptions{
		WorkDir:   t.TempDir(),
		OutputDir: h.outDir,
		Extractor: &fakeExtractor{calls: &calls, err: errors.New("must not be called")},
		Runner:    &fakeRunner{calls: &calls, scripts: &[]string{}, err: errors.New("must not be called")},
		Ext4:      &fakeExt4{calls: &calls, err: errors.New("must not be called")},
	})
	require.NoError(t, err)

	second, err := b2.Build(context.Background(), r)
	require.NoError(t, err)
	require.True(t, second.Skipped)
	assert.Empty(t, calls)
	assert.Equal(t, first.SHA256, second.SHA256)
}

func TestBuilder_Build_ForceRebuilds(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)
	_, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)

	// Without force, the artifact is reused even though the extractor now fails.
	h.extract.err = errors.New("rebuilt")
	res, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	assert.True(t, res.Skipped, "an up-to-date artifact must be reused without force")

	// With force, the extractor runs and its error surfaces.
	fb, err := NewBuilder(BuilderOptions{
		WorkDir:   t.TempDir(),
		OutputDir: h.outDir,
		Force:     true,
		Extractor: h.extract,
		Runner:    h.runner,
		Ext4:      h.ext4,
	})
	require.NoError(t, err)
	_, err = fb.Build(context.Background(), r)
	require.Error(t, err, "force must invoke the extractor")
	assert.Contains(t, err.Error(), "rebuilt")
}

func TestBuilder_Build_RejectsPreparerRecipe(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: busybox, version: "1.0.0"}
spec:
  source: {type: oci, ref: docker.io/library/busybox:1.36}
  kernel: {profile: firecracker-ci-6.1}
`)
	require.Equal(t, BuildPreparer, r.Spec.Build, "tini recipes default to the preparer strategy")

	_, err := h.builder.Build(context.Background(), r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preparer")
	assert.Empty(t, h.calls, "a preparer recipe must not be built here")
}

func TestNewBuilder_RequiresOutputDir(t *testing.T) {
	_, err := NewBuilder(BuilderOptions{OutputDir: ""})
	assert.Error(t, err)
}
