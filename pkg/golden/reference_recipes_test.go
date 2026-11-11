package golden

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file covers the reference templates (SBX-044): the recipes under
// recipes/sandbox-*, which are the templates a node is expected to be able to
// build and boot — a runtime-free base with a non-root user, a warm package
// cache and a /workspace mount point, and one language toolchain on top of it
// each.
//
// Everything a real build does needs a KVM host and is out of reach here: the
// OCI extraction, the provisioning chroot, the ext4 pack, the first boot that
// negotiates with the guest agent, and the host pin read from the running
// Firecracker. What is left is what decides whether such a build would even
// start — the recipe validates, the CLI would accept it, its kernel profile
// resolves on a default host, and it declares the host software it needs — plus
// the identity of each shipped name@version and the sandbox contract a recipe
// claims about the image it produces. Those are the parts whose regressions are
// quiet: a dropped /workspace, a half-regenerated pin, or an edited recipe left
// under its old version all build and boot cleanly on the machine that made
// them and fail somewhere else.

// referenceRecipes returns the reference template recipes.
func referenceRecipes(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "recipes", "sandbox-*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "recipes/sandbox-*.yaml must hold the reference templates")
	return paths
}

// TestReferenceRecipes_AreTemplatesTheBuildWouldAccept proves each reference
// recipe passes every check the builder makes without a host: it is a valid
// recipe, it is a golden build rather than one the task-time preparer
// assembles, and the kernel profile it names resolves through the default
// registry, so a node that has that kernel can boot what the recipe pins.
func TestReferenceRecipes_AreTemplatesTheBuildWouldAccept(t *testing.T) {
	profiles := config.DefaultKernelProfiles()

	for _, path := range referenceRecipes(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			recipe, err := LoadRecipe(path)
			require.NoError(t, err)
			require.NoError(t, recipe.Validate(), "a reference template must be a valid recipe")
			assert.Equal(t, BuildGolden, recipe.Spec.Build)
			assert.Contains(t, []string{InitSystemd, InitOpenRC, InitSysvinit}, recipe.Spec.Init.System,
				"a template boots through a real init, and the first-boot probe only knows these")
			assert.Contains(t, profiles, recipe.Spec.Kernel.Profile,
				"the recipe's kernel profile must resolve on a host that has the default registry")
		})
	}
}

var (
	// firecrackerVersionShape is what `firecracker --version` reports, which is
	// what a pin has to name for a build to accept it.
	firecrackerVersionShape = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+`)
	// kernelDigestShape is the SHA-256 a pin has to carry for a kernel image.
	kernelDigestShape = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// TestReferenceRecipes_ShareOneCompleteHostPin proves the set declares the host
// software it was built on, and declares the same software in every recipe.
//
// The values are read from a build host and are not derivable here (the test
// host's Firecracker and kernel are not the builder's), so what is checked is
// their shape and their agreement: a pin that names like a version and a
// SHA-256, present in all three, half-regenerated nowhere. Regenerating one
// recipe's pin and forgetting the others is the failure this catches, and it is
// the one that would otherwise turn up as a single template in the set that
// only one machine can build.
func TestReferenceRecipes_ShareOneCompleteHostPin(t *testing.T) {
	paths := referenceRecipes(t)
	require.NotEmpty(t, paths)

	var first *HostPin
	var firstName string
	for _, path := range paths {
		recipe, err := LoadRecipe(path)
		require.NoError(t, err)
		require.NotNil(t, recipe.Spec.Pin, "%s must declare the host software it needs", path)

		pin := recipe.Spec.Pin
		assert.Regexp(t, firecrackerVersionShape, pin.Firecracker, path)
		assert.Regexp(t, kernelDigestShape, pin.Kernel, path)

		if first == nil {
			first, firstName = pin, recipe.Metadata.Name
			continue
		}
		assert.Equal(t, first.Firecracker, pin.Firecracker,
			"%s and %s are one reference set and must require the same Firecracker", firstName, recipe.Metadata.Name)
		assert.Equal(t, first.Kernel, pin.Kernel,
			"%s and %s are one reference set and must require the same kernel", firstName, recipe.Metadata.Name)
	}
}

// TestReferenceRecipes_ShipTheSandboxContract proves each recipe provisions the
// two things #126 asks a sandbox template for and claims them in a form a boot
// can check: a non-root user, and a /workspace mount point that user can write.
//
// The claim is read off spec.verify rather than the provision script, because
// spec.verify is the part a job can execute against a booted guest: a recipe
// that provisions a user but asserts nothing about it leaves the requirement to
// whoever reads the script.
func TestReferenceRecipes_ShipTheSandboxContract(t *testing.T) {
	for _, path := range referenceRecipes(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			recipe, err := LoadRecipe(path)
			require.NoError(t, err)
			require.NoError(t, recipe.Validate())

			provision := recipe.Spec.Provision
			assert.True(t,
				strings.Contains(provision, "adduser") || strings.Contains(provision, "useradd"),
				"the recipe must create the non-root user a sandbox command runs as")
			assert.Contains(t, provision, "/workspace",
				"the recipe must provision the mount point the host hands a workload through")

			var workspace, nonRoot bool
			for _, check := range recipe.Spec.Verify {
				if strings.Contains(check.Guest, "/workspace") {
					workspace = true
				}
				if strings.Contains(check.Guest, "id -u") {
					nonRoot = true
				}
			}
			assert.True(t, workspace, "a boot must assert that /workspace is usable")
			assert.True(t, nonRoot, "a boot must assert that the sandbox user is not root")
		})
	}
}

// referenceDigests pins what every published reference template means. A recipe
// is content-addressed, so sandbox-base@1.0.0 has to name the same template
// forever: an edit to a shipped reference recipe has to come with a new version
// and a new entry here, and this table is what makes that impossible to forget.
//
// The values are the Recipe.Digest recorded by a KVM build of each recipe (see
// the SBX-044 build matrix); they are deliberately not recomputed from the
// recipe under test, because a digest compared against itself would accept any
// edit at all.
var referenceDigests = map[string]string{
	"sandbox-base":   "sha256:fc014cf0f734d347b4a514eed14c042b89e13f9ca80269c3d950cebfad622239",
	"sandbox-python": "sha256:a4799b950e84edc189f1f7a7a86f53dcc5de9f9adad18328c8d23e7c4485fdaf",
	"sandbox-node":   "sha256:c519aa2b77886c9eeffdcb4120d7f167b87e06bf9a931ca40b099c95b91703e9",
}

// TestReferenceRecipes_KeepTheirPublishedIdentity fails when a reference recipe
// is edited without the version bump its published digest depends on.
func TestReferenceRecipes_KeepTheirPublishedIdentity(t *testing.T) {
	seen := make(map[string]bool)

	for _, path := range referenceRecipes(t) {
		recipe, err := LoadRecipe(path)
		require.NoError(t, err)
		require.NoError(t, recipe.Validate())

		published, ok := referenceDigests[recipe.Metadata.Name]
		require.True(t, ok, "%s has no published digest: a new reference template needs one recorded", recipe.Metadata.Name)
		seen[recipe.Metadata.Name] = true

		assert.Equal(t, published, recipe.Digest(),
			"%s@%s was edited: bump metadata.version and record the new digest, or a node reuses the old %s as this template",
			recipe.Metadata.Name, recipe.Metadata.Version, recipe.Metadata.Name)
	}

	for name := range referenceDigests {
		assert.True(t, seen[name], "%s is recorded here but no longer a reference template", name)
	}
}
