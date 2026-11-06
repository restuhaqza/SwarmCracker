package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCLI executes the real command tree, so a test exercises the same wiring a
// user does: registration, flags, and the command's own RunE.
func runCLI(args ...string) error {
	root := newRootCommand()
	root.SetArgs(args)
	return root.Execute()
}

// writeArtifact creates a template manifest (and its artifact file) in dir, and
// returns the manifest's path.
func writeArtifact(t *testing.T, dir, name, version string) string {
	t.Helper()
	base := golden.ArtifactBaseName(name, version)
	require.NoError(t, os.WriteFile(filepath.Join(dir, base+".ext4"), []byte("img"), 0644))

	md := golden.ArtifactMetadata{
		APIVersion:      golden.APIVersionV1Alpha1,
		Kind:            golden.KindGoldenImage,
		Name:            name,
		Version:         version,
		RecipeDigest:    "sha256:abc",
		BaseImageDigest: "sha256:def",
		KernelProfile:   "guest-runtime-6.1",
		Runtime:         golden.Runtime{Name: golden.RuntimeDocker},
		Agent:           golden.AgentPin{Protocol: golden.MinimumAgentProtocol, Version: "test"},
		Pin:             checkpoint.Pin{Firecracker: "v1.15.1", Kernel: "cafebabe"},
		Artifact:        base + ".ext4",
		SizeBytes:       1024,
		SHA256:          "deadbeef",
	}
	data, err := json.MarshalIndent(md, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(dir, base+".json")
	require.NoError(t, os.WriteFile(path, data, 0644))
	return path
}

// TestCLI_TemplateInspect_ShowsTheManifestAndThisNodesVerdict is the surface the
// layer exists for: the manifest, including the pin and the agent version, and
// what this node would decide about it.
func TestCLI_TemplateInspect_ShowsTheManifestAndThisNodesVerdict(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "ubuntu-24.04-docker", "1.0.0")

	var err error
	out := captureStdout(t, func() {
		err = runCLI("template", "inspect", "--output-dir", dir, "ubuntu-24.04-docker@1.0.0")
	})
	require.NoError(t, err)

	assert.Contains(t, out, `"recipeDigest": "sha256:abc"`)
	assert.Contains(t, out, `"baseImageDigest": "sha256:def"`)
	assert.Contains(t, out, `"firecracker": "v1.15.1"`, "the pin is part of what inspect shows")
	assert.Contains(t, out, `"kernel": "cafebabe"`)
	assert.Contains(t, out, `"agentVersion": "test"`, "so is the agent version")
	assert.Contains(t, out, "agent: protocol "+golden.MinimumAgentProtocol+", version test")

	// The pin verdict depends on the host the test runs on (Firecracker and the
	// kernel may not be installed), but it is always reported: a template whose
	// pin cannot be checked must not look like one that was.
	assert.True(t, strings.Contains(out, "usable on this node") ||
		strings.Contains(out, "NOT usable here") ||
		strings.Contains(out, "cannot be checked"),
		"inspect must say what this node decided about the pin, got:\n%s", out)
}

func TestCLI_TemplateInspect_RefusesAReferenceItCannotResolve(t *testing.T) {
	dir := t.TempDir()
	var err error
	captureStdout(t, func() {
		err = runCLI("template", "inspect", "--output-dir", dir, "ubuntu-24.04-docker")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "<name>@<version>")
}

func TestCLI_TemplateInspect_MissingManifest(t *testing.T) {
	var err error
	captureStdout(t, func() {
		err = runCLI("template", "inspect", "--output-dir", t.TempDir(), "nope@1.0.0")
	})
	require.Error(t, err)
}

// TestCLI_TemplateInspect_AcceptsAManifestPath pins the direct-path form, which
// is how a manifest that is not in the store is inspected.
func TestCLI_TemplateInspect_AcceptsAManifestPath(t *testing.T) {
	dir := t.TempDir()
	path := writeArtifact(t, dir, "alpine-3.20-docker", "1.0.0")
	require.NoError(t, os.Remove(filepath.Join(dir, "golden-alpine-3.20-docker-1.0.0.ext4")))

	var err error
	out := captureStdout(t, func() {
		err = runCLI("template", "inspect", "--output-dir", dir, path)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "warning: artifact")
	assert.Contains(t, out, "is missing")
}

func TestCLI_TemplateList(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "ubuntu-24.04-docker", "1.0.0")
	writeArtifact(t, dir, "alpine-3.20-docker", "1.0.0")

	var err error
	out := captureStdout(t, func() {
		err = runCLI("template", "list", "--output-dir", dir)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "alpine-3.20-docker")
	assert.Contains(t, out, "ubuntu-24.04-docker")
	assert.Contains(t, out, golden.MinimumAgentProtocol+"@test", "the listing shows which agent each template carries")
}

func TestCLI_TemplateList_Empty(t *testing.T) {
	var err error
	out := captureStdout(t, func() {
		err = runCLI("template", "list", "--output-dir", t.TempDir())
	})
	require.NoError(t, err)
	assert.Contains(t, out, "No templates found")
}

// TestCLI_TemplateList_ReportsAnUnreadableManifest pins that a manifest nobody
// can read is not silently dropped: a template that disappears from the listing
// looks like one that was never built.
func TestCLI_TemplateList_ReportsAnUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	writeArtifact(t, dir, "alpine-3.20-docker", "1.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "golden-broken-1.0.0.json"), []byte("{not json"), 0644))

	var err error
	out := captureStdout(t, func() {
		err = runCLI("template", "list", "--output-dir", dir)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "unreadable manifest")
}

// TestCLI_TemplateBuild_SurfacesABuildFailure pins the rule the CLI owes its
// caller: a build the builder refuses is an error and nothing else — no panic,
// no artifact, no success line. A recipe the golden builder does not build is
// the cheapest way to reach that path without a KVM host.
func TestCLI_TemplateBuild_SurfacesABuildFailure(t *testing.T) {
	dir := t.TempDir()
	recipe := filepath.Join(dir, "busybox.yaml")
	require.NoError(t, os.WriteFile(recipe, []byte(`
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata: {name: busybox, version: "1.0.0"}
spec:
  source: {type: oci, ref: docker.io/library/busybox:1.36}
  kernel: {profile: guest-runtime-6.1}
`), 0644))

	out := filepath.Join(dir, "out")
	var err error
	captured := captureStdout(t, func() {
		err = runCLI("template", "build", "--output-dir", out, recipe)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preparer")
	assert.NotContains(t, captured, "Built ", "a refused build must not report success")

	files, globErr := filepath.Glob(filepath.Join(out, "*"))
	require.NoError(t, globErr)
	assert.Empty(t, files, "a refused build must record no template")
}

func TestCLI_TemplateBuild_ReportsARecipeItCannotRead(t *testing.T) {
	dir := t.TempDir()
	var err error
	captureStdout(t, func() {
		err = runCLI("template", "build", "--output-dir", filepath.Join(dir, "out"), filepath.Join(dir, "absent.yaml"))
	})
	require.Error(t, err)
}

// TestCLI_ImageIsADeprecatedAlias pins that the old spelling is not a second
// implementation: it reaches the same commands, marked deprecated.
func TestCLI_ImageIsADeprecatedAlias(t *testing.T) {
	cmd := newImageCommand()
	require.Len(t, cmd.Commands(), 3)
	for _, sub := range cmd.Commands() {
		assert.Contains(t, sub.Deprecated, "template "+sub.Name())
	}

	dir := t.TempDir()
	writeArtifact(t, dir, "alpine-3.20-docker", "1.0.0")
	var err error
	out := captureStdout(t, func() {
		// The deprecation notice itself goes to stderr, which captureStdout
		// does not redirect; what matters here is that the same command still
		// runs and lists the same templates.
		err = runCLI("image", "list", "--output-dir", dir)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "alpine-3.20-docker")
}
