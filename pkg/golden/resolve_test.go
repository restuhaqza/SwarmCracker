package golden

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/checkpoint"
	"github.com/restuhaqza/swarmcracker/pkg/config"
)

// fixtureKernel is the SHA-256 a golden fixture records as the kernel its
// template was built against. It is a value, not a real kernel: a test states
// the software a template claims, and pinnedTo states the software the node
// answering for it runs.
var fixtureKernel = strings.Repeat("ab", 32)

// fixturePin is the software the fixtures in this file were built on.
var fixturePin = checkpoint.Pin{Firecracker: "v1.15.1", Kernel: fixtureKernel}

// pinnedTo is the pin source of a node that runs the software in pin, for every
// profile. It is what a test that is not about the host uses.
func pinnedTo(pin checkpoint.Pin) PinSource {
	return func(context.Context, string) (checkpoint.Pin, error) { return pin, nil }
}

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
		// The pin a node is held to: what the agent in the image actually
		// advertised when the template was proven. A template with no recorded
		// capabilities is refused, so the fixture records the real set.
		Agent: AgentPin{
			Protocol: MinimumAgentProtocol,
			Version:  "test",
			Caps:     agent.Capabilities(),
		},
		// The host software the template was built on, which a node must match.
		Pin: fixturePin,
	}
	require.NoError(t, writeMetadataAtomic(filepath.Join(dir, base+".json"), md))
	return artifact
}

func TestResolve_ByName(t *testing.T) {
	dir := t.TempDir()
	artifact := writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")

	art, err := Resolve(context.Background(), dir, "ubuntu-24.04-docker", pinnedTo(fixturePin))
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-24.04-docker@1.0.0", art.Ref)
	assert.Equal(t, artifact, art.Path)
	assert.Equal(t, InitSystemd, art.Metadata.Init.System)
}

func TestResolve_ByVersion(t *testing.T) {
	dir := t.TempDir()
	want := writeGolden(t, dir, "alpine-3.20-docker", "1.0.0")
	writeGolden(t, dir, "alpine-3.20-docker", "2.0.0")

	art, err := Resolve(context.Background(), dir, "alpine-3.20-docker@1.0.0", pinnedTo(fixturePin))
	require.NoError(t, err)
	assert.Equal(t, want, art.Path)
}

func TestResolve_UnversionedPicksNewest(t *testing.T) {
	dir := t.TempDir()
	old := writeGolden(t, dir, "debian-12-docker", "1.0.0")
	newer := writeGolden(t, dir, "debian-12-docker", "2.0.0")

	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "golden-debian-12-docker-1.0.0.json"), past, past))

	art, err := Resolve(context.Background(), dir, "debian-12-docker", pinnedTo(fixturePin))
	require.NoError(t, err)
	assert.Equal(t, newer, art.Path)
	assert.NotEqual(t, old, art.Path)
}

func TestResolve_Errors(t *testing.T) {
	dir := t.TempDir()
	writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")

	_, err := Resolve(context.Background(), dir, "", pinnedTo(fixturePin))
	require.Error(t, err)

	_, err = Resolve(context.Background(), dir, "nope", pinnedTo(fixturePin))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = Resolve(context.Background(), dir, "ubuntu-24.04-docker@9.9.9", pinnedTo(fixturePin))
	require.Error(t, err)

	_, err = Resolve(context.Background(), dir, "../../etc/passwd", pinnedTo(fixturePin))
	require.Error(t, err)
}

func TestResolve_MissingArtifactFile(t *testing.T) {
	dir := t.TempDir()
	artifact := writeGolden(t, dir, "ubuntu-24.04-docker", "1.0.0")
	require.NoError(t, os.Remove(artifact))

	_, err := Resolve(context.Background(), dir, "ubuntu-24.04-docker", pinnedTo(fixturePin))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

// --- the host pin ---

// TestResolve_RefusesATemplateBuiltOnOtherSoftware is the refusal M4 asks for: a
// template pinned to a Firecracker version or a kernel this node does not run is
// refused with the code the build path uses, before its rootfs is materialized
// and before any Firecracker process exists.
//
// The two cases are checked apart because either half of the pin can differ and
// a version match says nothing about the kernel the memory image was written
// against.
func TestResolve_RefusesATemplateBuiltOnOtherSoftware(t *testing.T) {
	cases := []struct {
		name string
		node checkpoint.Pin
	}{
		{"another Firecracker version", checkpoint.Pin{Firecracker: "v1.16.0", Kernel: fixtureKernel}},
		{"another kernel", checkpoint.Pin{Firecracker: "v1.15.1", Kernel: strings.Repeat("cd", 32)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := t.TempDir()
			writeGolden(t, store, "ubuntu-24.04-docker", "1.0.0")

			art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker", pinnedTo(tc.node))
			require.Error(t, err)
			assert.Nil(t, art, "a refused template must not be handed back for a caller to materialize")

			assert.Equal(t, CodeTemplatePinMismatch, codeOf(err))
			assert.ErrorIs(t, err, checkpoint.ErrPinMismatch,
				"the class a checkpoint reports for the same disagreement, so one branch answers both")

			var mismatch *PinMismatchError
			require.ErrorAs(t, err, &mismatch)
			assert.Equal(t, fixturePin, mismatch.Got, "the template's pin")
			assert.Equal(t, tc.node, mismatch.Want, "this node's pin")
			assert.Contains(t, err.Error(), CodeTemplatePinMismatch)
		})
	}
}

// TestResolve_RefusesAnUnpinnedTemplate pins the rule for a template built before
// pinning existed: it says nothing about the software it needs, so it is refused
// rather than read as a wildcard.
func TestResolve_RefusesAnUnpinnedTemplate(t *testing.T) {
	store := t.TempDir()
	writeGolden(t, store, "ubuntu-24.04-docker", "1.0.0")

	sidecar := artifactPathFor(store, "ubuntu-24.04-docker", "1.0.0")
	md, err := ReadMetadata(sidecar)
	require.NoError(t, err)
	md.Pin = checkpoint.Pin{}
	require.NoError(t, writeMetadataAtomic(sidecar, *md))

	art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker", pinnedTo(fixturePin))
	require.Error(t, err)
	assert.Nil(t, art)
	assert.Equal(t, CodeTemplatePinMismatch, codeOf(err))
	assert.ErrorIs(t, err, checkpoint.ErrPinMismatch)
	assert.Contains(t, err.Error(), "unpinned")
}

// TestResolve_HoldsTheTemplateToTheKernelItsProfileResolvesToHere proves the pin
// this node is held to is read from the node: the same template resolves where
// the profile maps to the kernel it records, and is refused where the profile
// maps to another kernel.
//
// It is the case a check against a constant would miss. A node whose registry
// points the template's profile at a different kernel boots a VM its template is
// not pinned to, and the pin is the only thing that says so.
func TestResolve_HoldsTheTemplateToTheKernelItsProfileResolvesToHere(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "firecracker")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\necho 'Firecracker v1.15.1'\n"), 0o755))

	pinnedBytes := []byte("the kernel the template was built against")
	pinnedPath := filepath.Join(dir, "vmlinux-pinned")
	require.NoError(t, os.WriteFile(pinnedPath, pinnedBytes, 0o644))
	sum := sha256.Sum256(pinnedBytes)

	otherPath := filepath.Join(dir, "vmlinux-other")
	require.NoError(t, os.WriteFile(otherPath, []byte("another kernel"), 0o644))

	store := t.TempDir()
	writeGolden(t, store, "ubuntu-24.04-docker", "1.0.0")

	// The template names a profile and records the kernel that profile resolved
	// to where it was built. Only the node's mapping of that profile varies below.
	sidecar := artifactPathFor(store, "ubuntu-24.04-docker", "1.0.0")
	md, err := ReadMetadata(sidecar)
	require.NoError(t, err)
	md.KernelProfile = config.KernelProfileGuestRuntime
	md.Pin = checkpoint.Pin{Firecracker: "v1.15.1", Kernel: hex.EncodeToString(sum[:])}
	require.NoError(t, writeMetadataAtomic(sidecar, *md))

	t.Run("the profile resolves to the kernel the template records", func(t *testing.T) {
		art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker",
			NodeHostPin(binary, "", map[string]string{config.KernelProfileGuestRuntime: pinnedPath}))
		require.NoError(t, err)
		assert.Equal(t, "ubuntu-24.04-docker@1.0.0", art.Ref)
	})

	t.Run("the profile resolves to another kernel", func(t *testing.T) {
		art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker",
			NodeHostPin(binary, "", map[string]string{config.KernelProfileGuestRuntime: otherPath}))
		require.Error(t, err)
		assert.Nil(t, art, "a node that would boot another kernel must not be handed the template")

		var mismatch *PinMismatchError
		require.ErrorAs(t, err, &mismatch)
		assert.Equal(t, checkpoint.Pin{Firecracker: "v1.15.1", Kernel: hex.EncodeToString(sum[:])}, mismatch.Got)
		assert.NotEqual(t, mismatch.Want.Kernel, mismatch.Got.Kernel)
		assert.Equal(t, "v1.15.1", mismatch.Want.Firecracker, "the VMM agrees, so only the kernel is wrong")
	})
}

// TestResolve_RefusesWhenThisNodeCannotSayWhatItRuns keeps the two failures apart:
// a node whose software differs from the template's is a mismatch, and a node
// that cannot name its own software at all is not — it is refused as an
// unanswerable question, with no mismatch code to branch on, because a caller
// that read it as a mismatch would report the wrong reason.
func TestResolve_RefusesWhenThisNodeCannotSayWhatItRuns(t *testing.T) {
	store := t.TempDir()
	writeGolden(t, store, "ubuntu-24.04-docker", "1.0.0")

	t.Run("no kernel for the template's profile and no default", func(t *testing.T) {
		art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker", NodeHostPin("", "", nil))
		require.Error(t, err)
		assert.Nil(t, art)
		assert.Contains(t, err.Error(), "cannot be checked against this node")
		assert.NotErrorIs(t, err, checkpoint.ErrPinMismatch)
		assert.Empty(t, codeOf(err))
	})

	t.Run("no pin source at all", func(t *testing.T) {
		art, err := Resolve(context.Background(), store, "ubuntu-24.04-docker", nil)
		require.Error(t, err)
		assert.Nil(t, art)
		assert.Contains(t, err.Error(), "cannot be checked against this node")
	})
}
