package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedAgentBinary verifies the guest agent binary is embedded and is a
// real static linux/amd64 ELF executable.
func TestEmbeddedAgentBinary(t *testing.T) {
	require.NotEmpty(t, agentBinary, "agentBinary is empty - binary not embedded correctly")

	// The agent binary is ~1MB; anything tiny is not a real build.
	assert.Greater(t, len(agentBinary), 100000, "agentBinary appears too small")

	require.GreaterOrEqual(t, len(agentBinary), 4, "agentBinary too small to check ELF header")
	assert.Equal(t, []byte{0x7f, 'E', 'L', 'F'}, agentBinary[:4], "agentBinary is not an ELF binary")
}

// TestGenerateWrapperScript_StartsGuestAgent verifies the generated init wrapper
// starts the guest control agent in the background on the default vsock port,
// and does so before the final exec.
func TestGenerateWrapperScript_StartsGuestAgent(t *testing.T) {
	script := generateWrapperScript(&OCIImageInfo{ImageRef: "nginx"}, 10)

	assert.Contains(t, script, "/sbin/sc-agent -port 52", "wrapper must start the agent on the default vsock port")

	// The agent must be started in the background.
	var agentLine string
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "/sbin/sc-agent -port 52") {
			agentLine = line
			break
		}
	}
	require.NotEmpty(t, agentLine, "agent start line not found")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(agentLine), "&"), "agent must be started in the background: %q", agentLine)

	// It must tolerate /dev/vsock not being ready yet.
	assert.Contains(t, script, "/dev/vsock")

	// It must be emitted before the final exec line.
	agentIdx := strings.Index(script, "/sbin/sc-agent -port 52")
	execIdx := strings.Index(script, "exec ")
	require.NotEqual(t, -1, agentIdx)
	require.NotEqual(t, -1, execIdx)
	assert.Less(t, agentIdx, execIdx, "agent start must appear before the exec line")
}

// TestGenerateWrapperScript_CreatesMountpoints verifies the wrapper creates the
// /dev/pts and /dev/shm mountpoints before mounting them. devtmpfs does not
// provide these directories (and the kernel mounts devtmpfs over /dev at boot,
// hiding any the image baked in). Without a devpts instance the kernel refuses
// to open /dev/ptmx, so TTY sessions (`vm exec -t`) fail with ENOENT.
func TestGenerateWrapperScript_CreatesMountpoints(t *testing.T) {
	script := generateWrapperScript(&OCIImageInfo{ImageRef: "alpine"}, 10)

	for _, pair := range []struct{ mkdir, mount string }{
		{"mkdir -p /dev/pts", "mount -t devpts devpts /dev/pts"},
		{"mkdir -p /dev/shm", "mount -t tmpfs tmpfs /dev/shm"},
	} {
		mkdirIdx := strings.Index(script, pair.mkdir)
		mountIdx := strings.Index(script, pair.mount)
		require.NotEqual(t, -1, mkdirIdx, "wrapper must contain %q", pair.mkdir)
		require.NotEqual(t, -1, mountIdx, "wrapper must contain %q", pair.mount)
		assert.Less(t, mkdirIdx, mountIdx, "%q must appear before %q", pair.mkdir, pair.mount)
	}
}

// TestInjectTiniIntoDirWritesAgent verifies the guest agent is written to
// /sbin/sc-agent with mode 0755 and the exact embedded bytes.
func TestInjectTiniIntoDirWritesAgent(t *testing.T) {
	tmpDir := t.TempDir()

	injector := NewInitInjector(&InitSystemConfig{
		Type:           InitSystemTini,
		GracePeriodSec: 10,
	})

	require.NoError(t, injector.injectTiniIntoDir(tmpDir, nil))

	agentPath := filepath.Join(tmpDir, "sbin", "sc-agent")
	fi, err := os.Stat(agentPath)
	require.NoError(t, err, "/sbin/sc-agent not found")
	assert.Equal(t, os.FileMode(0755), fi.Mode().Perm(), "/sbin/sc-agent mode")

	content, err := os.ReadFile(agentPath)
	require.NoError(t, err)
	assert.Equal(t, agentBinary, content, "/sbin/sc-agent content must be the embedded bytes")
}

// TestInjectTiniIntoDirOverwritesAgentSymlink verifies a pre-existing symlink at
// /sbin/sc-agent is removed rather than written through.
func TestInjectTiniIntoDirOverwritesAgentSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))

	target := filepath.Join(tmpDir, "victim")
	require.NoError(t, os.WriteFile(target, []byte("do not clobber"), 0644))
	require.NoError(t, os.Symlink(target, filepath.Join(sbinDir, "sc-agent")))

	injector := NewInitInjector(&InitSystemConfig{Type: InitSystemTini, GracePeriodSec: 10})
	require.NoError(t, injector.injectTiniIntoDir(tmpDir, nil))

	// The symlink target must be untouched.
	victim, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "do not clobber", string(victim))

	fi, err := os.Lstat(filepath.Join(sbinDir, "sc-agent"))
	require.NoError(t, err)
	assert.True(t, fi.Mode().IsRegular(), "/sbin/sc-agent must be a regular file")
}

// TestInjectIntoDir_PreserveInitWritesAgent verifies the "preserve existing
// init" path still injects /sbin/sc-agent so the binary is present even though
// that path does not use our wrapper.
func TestInjectIntoDir_PreserveInitWritesAgent(t *testing.T) {
	tmpDir := t.TempDir()

	// dumb-init + /bin/sh makes DetectInitType preserve the existing init.
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(sbinDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "dumb-init"), []byte("#!/bin/sh\n"), 0755))
	binDir := filepath.Join(tmpDir, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "sh"), []byte("#!/bin/sh\n"), 0755))

	injector := NewInitInjector(&InitSystemConfig{Type: InitSystemDumbInit, GracePeriodSec: 10})
	require.NoError(t, injector.InjectIntoDir(tmpDir, nil))

	agentPath := filepath.Join(tmpDir, "sbin", "sc-agent")
	fi, err := os.Stat(agentPath)
	require.NoError(t, err, "/sbin/sc-agent not found on preserve-init path")
	assert.Equal(t, os.FileMode(0755), fi.Mode().Perm())

	content, err := os.ReadFile(agentPath)
	require.NoError(t, err)
	assert.Equal(t, agentBinary, content)
}

// TestGuestOverrideVersionPinned pins the guest override version so a wrapper
// change that needs a rootfs rebuild cannot silently reuse a stale cache.
func TestGuestOverrideVersionPinned(t *testing.T) {
	assert.Equal(t, "7", guestOverrideVersion)
}

// TestInjectAgentBinary_MkdirError verifies a failure to create /sbin surfaces
// as an error instead of being swallowed.
func TestInjectAgentBinary_MkdirError(t *testing.T) {
	tmpDir := t.TempDir()
	// /sbin exists as a regular file, so MkdirAll cannot create the directory.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "sbin"), []byte("not a dir"), 0644))

	err := injectAgentBinary(tmpDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sbin directory")
}

// TestInjectAgentBinary_WriteError verifies a failure to write the agent binary
// surfaces as an error. A non-empty directory at /sbin/sc-agent cannot be
// removed by os.Remove, so the subsequent write fails.
func TestInjectAgentBinary_WriteError(t *testing.T) {
	tmpDir := t.TempDir()
	sbinDir := filepath.Join(tmpDir, "sbin")
	require.NoError(t, os.MkdirAll(filepath.Join(sbinDir, "sc-agent"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sbinDir, "sc-agent", "blocker"), []byte("x"), 0644))

	err := injectAgentBinary(tmpDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "guest agent binary")
}
