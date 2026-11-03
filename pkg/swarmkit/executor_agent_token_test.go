package swarmkit

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/restuhaqza/swarmcracker/pkg/agent"
	"github.com/restuhaqza/swarmcracker/pkg/golden"
	"github.com/restuhaqza/swarmcracker/pkg/image"
	"github.com/restuhaqza/swarmcracker/test/mocks"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRealGoldenStore creates a golden artifact whose rootfs is a real ext4
// image, because token provisioning writes into it with debugfs.
func writeRealGoldenStore(t *testing.T, name, version string) (dir, template string) {
	t.Helper()
	for _, tool := range []string{"mkfs.ext4", "debugfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	dir = t.TempDir()
	template = filepath.Join(dir, "golden-"+name+"-"+version+".ext4")
	f, err := os.Create(template)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(8<<20))
	require.NoError(t, f.Close())
	out, err := exec.Command("mkfs.ext4", "-q", "-F", template).CombinedOutput()
	require.NoError(t, err, string(out))

	md := golden.ArtifactMetadata{
		APIVersion: "swarmcracker/v1",
		Kind:       golden.KindGoldenImage,
		Name:       name,
		Version:    version,
		Artifact:   filepath.Base(template),
	}
	data, err := json.Marshal(md)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "golden-"+name+"-"+version+".json"), data, 0o644))
	return dir, template
}

// TestControllerPrepare_GoldenService_ProvisionsTokenIntoTheCopy proves the
// golden path — which skips the image preparer — still authenticates the guest,
// and that the shared template is never given a token.
func TestControllerPrepare_GoldenService_ProvisionsTokenIntoTheCopy(t *testing.T) {
	goldenDir, template := writeRealGoldenStore(t, "ubuntu-24.04-docker", "1.0.0")
	rootfsDir := t.TempDir()
	socketDir := t.TempDir()

	task := goldenServiceTask("task-golden", "ubuntu-24.04-docker@1.0.0")
	ctrl := &Controller{
		task: task,
		config: &Config{
			GoldenDir:         goldenDir,
			RootfsDir:         rootfsDir,
			SocketDir:         socketDir,
			AgentRequireToken: true,
		},
		imagePrep:  &recordingImagePreparer{},
		networkMgr: mocks.NewMockNetworkManager(),
		socketPath: filepath.Join(socketDir, task.ID+".sock"),
		logger:     log.With().Str("task_id", task.ID).Logger(),
	}

	require.NoError(t, ctrl.Prepare(context.Background()))

	copied := image.TaskRootfsPath(rootfsDir, task.ID)
	guestToken, err := agent.ReadTokenFromRootfs(context.Background(), copied)
	require.NoError(t, err)
	require.NotEmpty(t, guestToken)

	hostToken, found, err := agent.ReadTokenFile(agent.TokenFilePath(vsockUDSPath(ctrl.socketPath)))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, guestToken, hostToken)

	_, err = agent.ReadTokenFromRootfs(context.Background(), template)
	assert.ErrorIs(t, err, agent.ErrNoToken, "the shared template must never carry a VM's token")
}

// TestControllerPrepare_GoldenService_WithoutTokenLeavesNoCredential pins the
// default: with require_token off, a golden task gets no token file at all.
func TestControllerPrepare_GoldenService_WithoutTokenLeavesNoCredential(t *testing.T) {
	goldenDir, _ := writeRealGoldenStore(t, "ubuntu-24.04-docker", "1.0.0")
	rootfsDir := t.TempDir()
	socketDir := t.TempDir()

	task := goldenServiceTask("task-golden", "ubuntu-24.04-docker@1.0.0")
	ctrl := &Controller{
		task: task,
		config: &Config{
			GoldenDir: goldenDir,
			RootfsDir: rootfsDir,
			SocketDir: socketDir,
		},
		imagePrep:  &recordingImagePreparer{},
		networkMgr: mocks.NewMockNetworkManager(),
		socketPath: filepath.Join(socketDir, task.ID+".sock"),
		logger:     log.With().Str("task_id", task.ID).Logger(),
	}

	require.NoError(t, ctrl.Prepare(context.Background()))

	_, found, err := agent.ReadTokenFile(agent.TokenFilePath(vsockUDSPath(ctrl.socketPath)))
	require.NoError(t, err)
	assert.False(t, found)

	_, err = agent.ReadTokenFromRootfs(context.Background(), image.TaskRootfsPath(rootfsDir, task.ID))
	assert.ErrorIs(t, err, agent.ErrNoToken)
}
