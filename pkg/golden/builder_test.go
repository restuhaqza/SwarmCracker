package golden

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
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

// fakeProber stands in for the first boot. Its default hello is what a healthy
// v2 agent answers, so a test only states the disagreement it cares about.
type fakeProber struct {
	calls   *[]string
	hello   agent.Hello
	err     error
	targets *[]BootTarget
}

func (f *fakeProber) Probe(_ context.Context, target BootTarget) (agent.Hello, error) {
	*f.calls = append(*f.calls, "probe")
	if f.targets != nil {
		*f.targets = append(*f.targets, target)
	}
	if f.err != nil {
		return agent.Hello{}, f.err
	}
	return f.hello, nil
}

// healthyHello is the handshake a correctly built template produces.
func healthyHello() agent.Hello {
	return agent.Hello{
		Version:      agent.Version,
		Versions:     []string{agent.Version},
		Caps:         agent.Capabilities(),
		AgentVersion: agent.AgentVersion,
		BuildID:      "test-build",
		BootID:       "test-boot",
	}
}

type harness struct {
	calls   []string
	scripts []string
	extract *fakeExtractor
	runner  *fakeRunner
	ext4    *fakeExt4
	prober  *fakeProber
	outDir  string
	builder *Builder
}

func newHarness(t *testing.T, force bool) *harness {
	t.Helper()
	h := &harness{outDir: t.TempDir()}
	h.extract = &fakeExtractor{calls: &h.calls, markFile: true}
	h.runner = &fakeRunner{calls: &h.calls, scripts: &h.scripts}
	h.ext4 = &fakeExt4{calls: &h.calls}
	h.prober = &fakeProber{calls: &h.calls, hello: healthyHello()}
	b, err := NewBuilder(BuilderOptions{
		WorkDir:   t.TempDir(),
		OutputDir: h.outDir,
		Force:     force,
		Extractor: h.extract,
		Runner:    h.runner,
		Ext4:      h.ext4,
		Prober:    h.prober,
		KernelProfiles: map[string]string{
			"guest-runtime-6.1": "/usr/share/firecracker/vmlinux-runtime",
		},
		Now: func() time.Time { return time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC) },
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

	// Order: extract, provision, seal, ext4, first-boot probe.
	require.Len(t, h.calls, 5)
	assert.Equal(t, "extract:docker.io/library/ubuntu:24.04", h.calls[0])
	assert.Equal(t, "run", h.calls[1])
	assert.Equal(t, "run", h.calls[2])
	assert.Equal(t, "ext4", h.calls[3])
	assert.Equal(t, "probe", h.calls[4], "the artifact must be booted before it is recorded")
	assert.Contains(t, h.scripts[0], "apt-get update")
	assert.Contains(t, h.scripts[1], "machine-id")

	// Metadata is readable and consistent.
	md, err := ReadMetadata(res.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, r.Digest(), md.RecipeDigest)
	assert.Equal(t, res.SHA256, md.SHA256)
	assert.Equal(t, int64(5*1<<30), md.RootMinSizeBytes)
	assert.Equal(t, "guest-runtime-6.1", md.KernelProfile)
	// The template records the agent it carries, so a host can refuse one it
	// cannot drive instead of guessing from the artifact's age.
	assert.Equal(t, MinimumAgentProtocol, md.Agent.Protocol)
	assert.Equal(t, agent.AgentVersion, md.Agent.Version)
}

// TestBuilder_Build_ProbesTheSealedArtifact pins what the first boot is asked
// to prove: it boots the built ext4 with the recipe's kernel profile, init
// system and boot arguments, not some other combination.
func TestBuilder_Build_ProbesTheSealedArtifact(t *testing.T) {
	h := newHarness(t, false)
	var targets []BootTarget
	h.prober.targets = &targets

	res, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, res.ArtifactPath, targets[0].ArtifactPath)
	assert.Equal(t, "/usr/share/firecracker/vmlinux-runtime", targets[0].KernelPath)
	assert.Equal(t, InitSystemd, targets[0].InitSystem)
	assert.Equal(t, []string{"systemd.unified_cgroup_hierarchy=1"}, targets[0].BootArgs)
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
		Prober:    &fakeProber{calls: &calls, err: errors.New("must not be called")},
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
		Prober:    h.prober,
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

// sysvRecipeYAML is a golden recipe that boots sysvinit, whose enablement needs
// the image's own insserv pass in addition to the runlevel links.
const sysvRecipeYAML = `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: debian-12-sysv
  version: 1.0.0
spec:
  arch: [amd64]
  source:
    type: oci
    ref: docker.io/library/debian:12
  init:
    system: sysvinit
  kernel:
    profile: guest-runtime-6.1
  runtime:
    name: none
  disk:
    rootMinSize: 2GiB
  seal:
    - rm -rf /var/lib/apt/lists/*
`

// TestBuilder_SysvRegistersStartOrder proves a sysvinit build runs the image's
// insserv before the seal steps: Debian's startpar only starts services listed
// in /etc/init.d/.depend.start, so the runlevel links alone are not enough.
func TestBuilder_SysvRegistersStartOrder(t *testing.T) {
	h := newHarness(t, false)

	res, err := h.builder.Build(context.Background(), mustRecipe(t, sysvRecipeYAML))
	require.NoError(t, err)

	require.Len(t, h.scripts, 2, "insserv runs after install and before the seal step")
	assert.Contains(t, h.scripts[0], "insserv -f sc-agent")
	assert.Equal(t, "rm -rf /var/lib/apt/lists/*", strings.TrimSpace(h.scripts[1]))

	md, err := ReadMetadata(res.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, InitSysvinit, md.Init.System)
}

// TestBuilder_SystemdDoesNotRunInsserv keeps the sysvinit step scoped: a
// systemd build must not invoke another init system's tooling.
func TestBuilder_SystemdDoesNotRunInsserv(t *testing.T) {
	h := newHarness(t, false)

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.NoError(t, err)
	for _, script := range h.scripts {
		assert.NotContains(t, script, "insserv")
	}
}

// TestNewBuilder_DefaultsToARealProbe proves a caller that injects nothing
// still gets the first-boot proof: the default prober is the Firecracker one
// and the default kernel registry resolves the recipes' profile.
func TestNewBuilder_DefaultsToARealProbe(t *testing.T) {
	b, err := NewBuilder(BuilderOptions{OutputDir: t.TempDir()})
	require.NoError(t, err)

	assert.NotNil(t, b.opts.Prober)
	assert.Contains(t, b.opts.KernelProfiles, "guest-runtime-6.1")
}
