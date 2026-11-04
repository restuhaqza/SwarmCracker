package migration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adoptTokenImage builds a small ext4 image for the token adoption tests,
// skipping when the ext4 tools are not installed.
func adoptTokenImage(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	path := filepath.Join(t.TempDir(), "disk.ext4")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())
	out, err := exec.Command("mkfs.ext4", "-q", "-F", path).CombinedOutput()
	require.NoError(t, err, string(out))
	return path
}

// TestAdoptAgentTokenPublishesTheDiskToken is the restore half of token auth: a
// resumed guest keeps the token baked into its disk, so the target must publish
// that exact value for the dial to authenticate.
func TestAdoptAgentTokenPublishesTheDiskToken(t *testing.T) {
	disk := adoptTokenImage(t)
	require.NoError(t, agent.InjectToken(context.Background(), disk, "source-boot-token"))
	vsock := filepath.Join(t.TempDir(), "source-task.vsock")

	require.NoError(t, adoptAgentToken(context.Background(), disk, vsock))

	got, found, err := agent.ReadTokenFile(agent.TokenFilePath(vsock))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "source-boot-token", got)
}

// TestAdoptAgentTokenWithoutATokenIsANoOp covers a VM booted before token auth:
// nothing to publish, and the restore must not fail.
func TestAdoptAgentTokenWithoutATokenIsANoOp(t *testing.T) {
	disk := adoptTokenImage(t)
	vsock := filepath.Join(t.TempDir(), "legacy-task.vsock")

	require.NoError(t, adoptAgentToken(context.Background(), disk, vsock))

	_, found, err := agent.ReadTokenFile(agent.TokenFilePath(vsock))
	require.NoError(t, err)
	assert.False(t, found)
}

// TestAdoptAgentTokenReadFailureIsAWarning keeps a legacy restore working on a
// host whose ext4 tools cannot read the disk: the dial then reports the guest's
// own verdict instead of the restore failing earlier.
func TestAdoptAgentTokenReadFailureIsAWarning(t *testing.T) {
	vsock := filepath.Join(t.TempDir(), "task.vsock")

	require.NoError(t, adoptAgentToken(context.Background(), t.TempDir(), vsock))

	_, found, err := agent.ReadTokenFile(agent.TokenFilePath(vsock))
	require.NoError(t, err)
	assert.False(t, found)
}

// TestAdoptAgentTokenWriteFailureIsAnError is the fail-closed half: if the disk
// carries a token the host cannot publish, the restore must stop rather than
// dial unauthenticated.
func TestAdoptAgentTokenWriteFailureIsAnError(t *testing.T) {
	disk := adoptTokenImage(t)
	require.NoError(t, agent.InjectToken(context.Background(), disk, "source-boot-token"))

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	err := adoptAgentToken(context.Background(), disk, filepath.Join(blocker, "task.vsock"))
	require.Error(t, err)
}
