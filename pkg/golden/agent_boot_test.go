package golden

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deletingRunner simulates a provision or seal script that removes a path a
// previous step installed, which is exactly how a build can end up with an
// image whose agent is not enabled.
type deletingRunner struct {
	calls     *[]string
	guestPath string
}

func (r *deletingRunner) Run(_ context.Context, rootfsDir, _ string) error {
	*r.calls = append(*r.calls, "run")
	return os.RemoveAll(filepath.Join(rootfsDir, r.guestPath))
}

// artifactAbsent asserts a failed build left no template behind that a later
// resolve could pick up.
func artifactAbsent(t *testing.T, outDir string) {
	t.Helper()
	assert.NoFileExists(t, filepath.Join(outDir, ArtifactBaseName("ubuntu-24.04-docker", "1.0.0")+".ext4"))
	assert.NoFileExists(t, filepath.Join(outDir, ArtifactBaseName("ubuntu-24.04-docker", "1.0.0")+".json"))
}

// TestBuilder_RefusesImageWhoseUnitIsNotEnabled proves a seal step cannot strip
// the enablement and still produce a template: the build verifies enablement
// after sealing, so the failure is caught before the artifact is recorded.
func TestBuilder_RefusesImageWhoseUnitIsNotEnabled(t *testing.T) {
	h := newHarness(t, false)
	h.builder.opts.Runner = &deletingRunner{
		calls:     &h.calls,
		guestPath: "/etc/systemd/system/multi-user.target.wants/sc-agent.service",
	}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would boot without a usable guest agent")
	assert.Contains(t, err.Error(), "not enabled")
	assert.NotContains(t, h.calls, "ext4", "the rootfs must not be packed once the agent is unproven")
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RefusesImageWhoseBinaryIsRemoved covers the other half: an image
// with the unit in place but no agent binary is just as unusable.
func TestBuilder_RefusesImageWhoseBinaryIsRemoved(t *testing.T) {
	h := newHarness(t, false)
	h.builder.opts.Runner = &deletingRunner{calls: &h.calls, guestPath: AgentPath}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/sbin/sc-agent is missing")
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RefusesAgentThatNeverAnswers proves the first-boot probe is part
// of the build contract, not an optional extra.
func TestBuilder_RefusesAgentThatNeverAnswers(t *testing.T) {
	h := newHarness(t, false)
	h.prober.err = errors.New("guest agent did not answer hello")

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not reach its guest agent")
	assert.Contains(t, err.Error(), "did not answer hello")
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RefusesAgentWithWrongProtocol proves the pin is checked against
// the agent that actually answered, so a template cannot claim a protocol its
// agent does not speak.
func TestBuilder_RefusesAgentWithWrongProtocol(t *testing.T) {
	h := newHarness(t, false)
	hello := healthyHello()
	hello.Version = agent.VersionLegacy
	h.prober.hello = hello

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins guest agent protocol")
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RefusesAgentMissingCapabilities proves the build holds the guest
// to the operations the host will rely on.
func TestBuilder_RefusesAgentMissingCapabilities(t *testing.T) {
	h := newHarness(t, false)
	hello := healthyHello()
	hello.Caps = []string{agent.CapHello, agent.CapExec}
	h.prober.hello = hello

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without advertising")
	assert.Contains(t, err.Error(), agent.CapPut)
	assert.Contains(t, err.Error(), agent.CapGet)
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RecordsAgentVersionFromHello proves the metadata pin describes the
// build that answered, not one the recipe assumed: the version *and* the
// operations, because the protocol number alone cannot tell an agent that can
// prepare a clone from one that cannot.
func TestBuilder_RecordsAgentVersionFromHello(t *testing.T) {
	h := newHarness(t, false)
	hello := healthyHello()
	hello.AgentVersion = "v9.9.9"
	h.prober.hello = hello

	res, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.NoError(t, err)

	md, err := ReadMetadata(res.MetadataPath)
	require.NoError(t, err)
	assert.Equal(t, AgentPin{Protocol: agent.Version, Version: "v9.9.9", Caps: agent.Capabilities()}, md.Agent)
}

// TestBuilder_RefusesPinnedVersionThatDiffers proves an explicit recipe pin is
// verified against the guest instead of being copied into metadata unread.
func TestBuilder_RefusesPinnedVersionThatDiffers(t *testing.T) {
	h := newHarness(t, false)
	r := mustRecipe(t, validRecipeYAML)
	r.Spec.Agent.Version = "v0.1.0"

	_, err := h.builder.Build(context.Background(), r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pins guest agent version")
	artifactAbsent(t, h.outDir)
}

// TestBuilder_RequiresAKernelForTheBootProbe proves a profile that does not
// resolve to a kernel fails the build instead of skipping verification.
func TestBuilder_RequiresAKernelForTheBootProbe(t *testing.T) {
	h := newHarness(t, false)
	h.builder.opts.KernelProfiles = map[string]string{}

	_, err := h.builder.Build(context.Background(), mustRecipe(t, validRecipeYAML))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not mapped to a kernel image")
	artifactAbsent(t, h.outDir)
}

// TestProbeBootArgs proves the probe boots the same way the executor will: the
// recipe's own arguments are appended to the shared base, and an init system
// without a /sbin/init path is refused rather than guessed.
func TestProbeBootArgs(t *testing.T) {
	got, err := probeBootArgs(InitSystemd, []string{"systemd.unified_cgroup_hierarchy=1"})
	require.NoError(t, err)
	assert.Equal(t,
		"console=ttyS0 reboot=k panic=1 pci=off nomodules init=/sbin/init systemd.unified_cgroup_hierarchy=1",
		got)

	for _, initSystem := range []string{InitSystemd, InitOpenRC, InitSysvinit} {
		got, err := probeBootArgs(initSystem, nil)
		require.NoError(t, err, initSystem)
		assert.Contains(t, got, "init=/sbin/init", initSystem)
	}

	_, err = probeBootArgs(InitTini, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no /sbin/init")
}
