package golden

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedRecipeYAML pins the host software this recipe requires, so a build on
// anything else must be refused rather than recorded.
const pinnedRecipeYAML = `
apiVersion: swarmcracker.io/v1alpha1
kind: GoldenImage
metadata:
  name: ubuntu-24.04-docker
  version: 1.0.0
spec:
  arch: [amd64]
  source:
    type: oci
    ref: docker.io/library/ubuntu:24.04
  init:
    system: systemd
  pin:
    firecracker: v1.15.0
    kernel: dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
  kernel:
    profile: guest-runtime-6.1
  runtime:
    name: docker
  disk:
    rootMinSize: 5GiB
  provision: |
    apt-get update
`

// --- the pin ---

func TestArtifactMetadata_VerifyPin_AcceptsOnlyThisHostsSoftware(t *testing.T) {
	pin := checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)}
	md := &ArtifactMetadata{Name: "ubuntu-24.04-docker", Version: "1.0.0", Pin: pin}

	require.NoError(t, md.VerifyPin(pin), "the host that built it can use it")

	for name, node := range map[string]checkpoint.Pin{
		"another Firecracker": {Firecracker: "v1.15.2", Kernel: pin.Kernel},
		"another kernel":      {Firecracker: pin.Firecracker, Kernel: strings.Repeat("b", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			err := md.VerifyPin(node)
			require.Error(t, err)

			var mismatch *PinMismatchError
			require.ErrorAs(t, err, &mismatch)
			assert.Equal(t, CodeTemplatePinMismatch, mismatch.Code())
			assert.Equal(t, pin, mismatch.Got)
			assert.Equal(t, node, mismatch.Want)
			// The same class a checkpoint reports for the same disagreement, so
			// a caller that already branches on it branches on this too.
			assert.ErrorIs(t, err, checkpoint.ErrPinMismatch)
			assert.Contains(t, err.Error(), "ubuntu-24.04-docker@1.0.0")
		})
	}
}

// TestArtifactMetadata_VerifyPin_RefusesAnUnpinnedTemplate pins the rule that an
// absent pin is not a wildcard: a template built before pinning existed says
// nothing about where it can be resumed, so it is refused rather than trusted.
func TestArtifactMetadata_VerifyPin_RefusesAnUnpinnedTemplate(t *testing.T) {
	md := &ArtifactMetadata{Name: "old", Version: "0.9.0"}
	err := md.VerifyPin(checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)})
	require.Error(t, err)

	var mismatch *PinMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Contains(t, err.Error(), "unpinned")
	assert.ErrorIs(t, err, checkpoint.ErrPinMismatch)
}

func TestArtifactMetadata_VerifyPin_RefusesAnIncompleteNodePin(t *testing.T) {
	md := &ArtifactMetadata{
		Name: "tpl", Version: "1.0.0",
		Pin: checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)},
	}

	err := md.VerifyPin(checkpoint.Pin{Firecracker: "v1.15.1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this node's pin is incomplete")

	var mismatch *PinMismatchError
	assert.False(t, errors.As(err, &mismatch), "an incomplete pin is the caller's bug, not a mismatch")
}

// TestArtifactMetadata_VerifyPin_GatesACheckpointFromTheTemplate proves the two
// pins are the same two identities: a checkpoint taken from a VM booted from
// this template carries this template's pin, so the checkpoint package refuses
// it on exactly the nodes this manifest refuses.
func TestArtifactMetadata_VerifyPin_GatesACheckpointFromTheTemplate(t *testing.T) {
	pin := checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)}
	md := &ArtifactMetadata{Name: "tpl", Version: "1.0.0", Pin: pin}

	ckpt := checkpoint.Manifest{ID: "tpl-task-1", Pin: md.Pin}
	require.NoError(t, ckpt.VerifyPin(pin))

	other := checkpoint.Pin{Firecracker: "v1.15.2", Kernel: pin.Kernel}
	assert.ErrorIs(t, ckpt.VerifyPin(other), checkpoint.ErrPinMismatch)
	assert.ErrorIs(t, md.VerifyPin(other), checkpoint.ErrPinMismatch)
}

// TestHostPin_ReadsTheRunningVMMAndHashesTheKernel pins what a build records:
// the version the VMM binary answers with, and the SHA-256 of the kernel image
// the recipe's profile resolved to — not a path, and not a version string that
// two different builds of a kernel can share.
func TestHostPin_ReadsTheRunningVMMAndHashesTheKernel(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "firecracker")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\necho 'Firecracker v1.15.1'\necho '  commit: 6f9b8a1'\n"), 0o755))

	kernel := filepath.Join(dir, "vmlinux")
	kernelBytes := []byte("kernel bytes")
	require.NoError(t, os.WriteFile(kernel, kernelBytes, 0o644))

	pin, err := NodePin(context.Background(), binary, kernel)
	require.NoError(t, err)

	sum := sha256.Sum256(kernelBytes)
	assert.Equal(t, checkpoint.Pin{Firecracker: "v1.15.1", Kernel: hex.EncodeToString(sum[:])}, pin)
	assert.NoError(t, pin.Validate(), "the recorded pin is one the checkpoint package accepts as complete")
}

func TestHostPin_RefusesWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	kernel := filepath.Join(dir, "vmlinux")
	require.NoError(t, os.WriteFile(kernel, []byte("kernel bytes"), 0o644))

	notAFirecracker := filepath.Join(dir, "not-firecracker")
	require.NoError(t, os.WriteFile(notAFirecracker, []byte("#!/bin/sh\necho 'hello'\n"), 0o755))

	t.Run("a version this build cannot read", func(t *testing.T) {
		pin, err := NodePin(context.Background(), notAFirecracker, kernel)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "version line")
		assert.Equal(t, checkpoint.Pin{}, pin)
	})

	t.Run("a kernel image that is not there", func(t *testing.T) {
		_, err := NodePin(context.Background(), notAFirecracker, filepath.Join(dir, "absent"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "hash the kernel image")
	})

	t.Run("no kernel image at all", func(t *testing.T) {
		_, err := NodePin(context.Background(), notAFirecracker, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no kernel image to pin")
	})
}

// --- the digest ---

// TestBuilder_Digest_IsTheSameOnAnyHost is the guarantee the whole layer rests
// on: two builds of the same recipe produce the same digest, on any host, and
// the host they really ran on is recorded in the manifest instead of being
// folded into the digest.
func TestBuilder_Digest_IsTheSameOnAnyHost(t *testing.T) {
	r := mustRecipe(t, validRecipeYAML)

	first := newHarness(t, false)
	first.hostPin = checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)}
	res1, err := first.builder.Build(context.Background(), r)
	require.NoError(t, err)

	// The same recipe built on another host: another work and output directory,
	// another kernel image, another Firecracker.
	second := newHarness(t, false)
	second.hostPin = checkpoint.Pin{Firecracker: "v1.15.2", Kernel: strings.Repeat("c", 64)}
	res2, err := second.builder.Build(context.Background(), r)
	require.NoError(t, err)

	assert.Equal(t, res1.Digest, res2.Digest, "the digest identifies the recipe, not the host that built it")

	md1, err := ReadMetadata(res1.MetadataPath)
	require.NoError(t, err)
	md2, err := ReadMetadata(res2.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, first.hostPin, md1.Pin)
	assert.Equal(t, second.hostPin, md2.Pin, "the manifest records the host software the template is valid against")
	assert.NotEqual(t, md1.Pin, md2.Pin, "the two builds must really have run on different software")
}

// TestBuilder_Manifest_RecordsTheBaseImageDigestOutsideTheDigest states the
// limit of the guarantee rather than hiding it: a moving source tag keeps the
// recipe digest — which is what lets an up-to-date artifact be reused without a
// registry call — and the build records what the tag resolved to so the two
// artifacts can still be told apart.
func TestBuilder_Manifest_RecordsTheBaseImageDigestOutsideTheDigest(t *testing.T) {
	r := mustRecipe(t, validRecipeYAML)

	before := newHarness(t, false)
	before.baseDigest = "sha256:" + strings.Repeat("1", 64)
	resBefore, err := before.builder.Build(context.Background(), r)
	require.NoError(t, err)

	after := newHarness(t, false)
	after.baseDigest = "sha256:" + strings.Repeat("2", 64)
	resAfter, err := after.builder.Build(context.Background(), r)
	require.NoError(t, err)

	assert.Equal(t, resBefore.Digest, resAfter.Digest, "the digest stays offline-computable, so a moved tag does not change it")

	mdBefore, err := ReadMetadata(resBefore.MetadataPath)
	require.NoError(t, err)
	mdAfter, err := ReadMetadata(resAfter.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, before.baseDigest, mdBefore.BaseImageDigest)
	assert.Equal(t, after.baseDigest, mdAfter.BaseImageDigest,
		"the manifest is what tells two builds of a moved tag apart")
}

// TestBuilder_Digest_ChangesWithTheRecipesContent is the other half: the digest
// must move when the template's content moves.
func TestBuilder_Digest_ChangesWithTheRecipesContent(t *testing.T) {
	base := mustRecipe(t, validRecipeYAML)
	extra := mustRecipe(t, strings.Replace(validRecipeYAML, "    apt-get update\n", "    apt-get update\n    apt-get install -y jq\n", 1))
	assert.NotEqual(t, base.Digest(), extra.Digest(), "an added package changes the recipe and must change the digest")
}

// TestRecipe_Digest_IncludesADeclaredHostPin pins that a requirement on the
// build host is content, not environment: the same recipe text with a pin
// describes a different template than one without.
func TestRecipe_Digest_IncludesADeclaredHostPin(t *testing.T) {
	unpinned := mustRecipe(t, strings.Replace(pinnedRecipeYAML, "  pin:\n    firecracker: v1.15.0\n    kernel: dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\n", "", 1))
	pinned := mustRecipe(t, pinnedRecipeYAML)

	assert.Contains(t, pinned.Spec.Pin.Firecracker, "v1.15.0", "the fixture must really declare a pin")
	assert.Nil(t, unpinned.Spec.Pin)
	assert.NotEqual(t, unpinned.Digest(), pinned.Digest())
}

// --- the build-time pin check ---

func TestBuilder_Build_RefusesAMismatchedHostPin(t *testing.T) {
	h := newHarness(t, false)
	h.hostPin = checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("a", 64)}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, pinnedRecipeYAML))
	require.Error(t, err)

	var mismatch *PinMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, CodeTemplatePinMismatch, mismatch.Code())
	assert.Equal(t, checkpoint.Pin{
		Firecracker: "v1.15.0",
		Kernel:      strings.Repeat("d", 64),
	}, mismatch.Want, "the recipe's requirement is what the build reports as wanted")
	assert.Equal(t, h.hostPin, mismatch.Got)
	assert.ErrorIs(t, err, checkpoint.ErrPinMismatch)
	assert.Empty(t, h.calls, "a recipe this host cannot satisfy must fail before any build work")
}

func TestBuilder_Build_AcceptsARecipePinnedToThisHost(t *testing.T) {
	h := newHarness(t, false)
	h.hostPin = checkpoint.Pin{Firecracker: "v1.15.0", Kernel: strings.Repeat("d", 64)}

	res, err := h.builder.Build(context.Background(), mustRecipe(t, pinnedRecipeYAML))
	require.NoError(t, err)

	md, err := ReadMetadata(res.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, h.hostPin, md.Pin)
}

func TestBuilder_Build_ReusesOnlyATemplatePinnedToThisHost(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)

	first, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	require.False(t, first.Skipped)

	again, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	require.True(t, again.Skipped, "the template this node built is this node's template")

	// The same node upgraded: the artifact is still there, and its recipe is
	// unchanged, but it is not a template this node can use.
	h.hostPin = checkpoint.Pin{Firecracker: "v1.15.2", Kernel: strings.Repeat("c", 64)}
	rebuilt, err := h.builder.Build(context.Background(), r)
	require.NoError(t, err)
	assert.False(t, rebuilt.Skipped, "a template built on other host software is rebuilt, not handed back")

	md, err := ReadMetadata(rebuilt.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, h.hostPin, md.Pin)
}

// --- the recipe's declaration ---

func TestRecipe_Validate_RequiresAWholeHostPin(t *testing.T) {
	for name, tc := range map[string]struct{ pin, want string }{
		"no firecracker": {
			pin:  "  pin:\n    kernel: dddd\n",
			want: "pin.firecracker is required",
		},
		"no kernel": {
			pin:  "  pin:\n    firecracker: v1.15.1\n",
			want: "pin.kernel is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := ParseRecipe([]byte(strings.Replace(validRecipeYAML, "  kernel:\n", tc.pin+"  kernel:\n", 1)))
			require.NoError(t, err)
			require.NotNil(t, r.Spec.Pin, "the fixture must really declare a partial pin")
			err = r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
