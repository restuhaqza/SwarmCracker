package image

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	localtypes "github.com/restuhaqza/swarmcracker/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentTokenImage builds a small ext4 image for the token tests, skipping when
// the ext4 tools are not installed.
func agentTokenImage(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	path := filepath.Join(t.TempDir(), "rootfs.ext4")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())
	out, err := exec.Command("mkfs.ext4", "-q", "-F", path).CombinedOutput()
	require.NoError(t, err, string(out))
	return path
}

func TestProvisionAgentTokenWritesIntoThePrivateCopy(t *testing.T) {
	socketDir := t.TempDir()
	preparer := &ImagePreparer{config: &PreparerConfig{SocketDir: socketDir, AgentRequireToken: true}}
	task := &localtypes.Task{ID: "task-1", Annotations: map[string]string{
		localtypes.AnnotationRootfsEphemeral: "true",
	}}
	rootfs := agentTokenImage(t)

	require.NoError(t, preparer.provisionAgentToken(context.Background(), task, rootfs))

	guestToken, err := agent.ReadTokenFromRootfs(context.Background(), rootfs)
	require.NoError(t, err)
	require.NotEmpty(t, guestToken)

	hostToken, found, err := agent.ReadTokenFile(agent.TokenFilePath(filepath.Join(socketDir, "task-1.vsock")))
	require.NoError(t, err)
	require.True(t, found, "the host copy must be published next to the VM's muxer")
	assert.Equal(t, guestToken, hostToken)
}

// TestProvisionAgentTokenRefusesASharedRootfs is the multi-tenant guard: a token
// written into an image cache or golden artifact would be shared by every VM
// built from it, so the node refuses instead of booting unauthenticated.
func TestProvisionAgentTokenRefusesASharedRootfs(t *testing.T) {
	preparer := &ImagePreparer{config: &PreparerConfig{SocketDir: t.TempDir(), AgentRequireToken: true}}
	task := &localtypes.Task{ID: "task-1", Annotations: map[string]string{}}

	err := preparer.provisionAgentToken(context.Background(), task, agentTokenImage(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a private per-task copy")
}

func TestProvisionAgentTokenRequiresASocketDirectory(t *testing.T) {
	preparer := &ImagePreparer{config: &PreparerConfig{AgentRequireToken: true}}
	task := &localtypes.Task{ID: "task-1", Annotations: map[string]string{
		localtypes.AnnotationRootfsEphemeral: "true",
	}}

	err := preparer.provisionAgentToken(context.Background(), task, agentTokenImage(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket directory")
}

func TestProvisionAgentTokenIsOffByDefault(t *testing.T) {
	socketDir := t.TempDir()
	preparer := &ImagePreparer{config: &PreparerConfig{SocketDir: socketDir}}
	task := &localtypes.Task{ID: "task-1", Annotations: map[string]string{}}

	// No ext4 tools needed: the disabled path must not touch the rootfs at all.
	require.NoError(t, preparer.provisionAgentToken(context.Background(), task, filepath.Join(t.TempDir(), "absent.ext4")))

	_, found, err := agent.ReadTokenFile(agent.TokenFilePath(filepath.Join(socketDir, "task-1.vsock")))
	require.NoError(t, err)
	assert.False(t, found)
}
